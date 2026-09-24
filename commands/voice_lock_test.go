package commands

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// /voice-lock and /voice-unlock (#349). Every case drives the invoker into a
// spawned channel through the gateway helpers, runs the handler through the
// fake responder, and judges the channel by who can join it: the can-join
// oracle over the overwrite list the fake holds for it. Nothing here asserts
// the shape of that list, a bitmask, reply wording or a log line.

// lockingHub is the permission fixture's hub with "Locking allowed" on.
func lockingHub(source store.PermissionSource) store.Hub {
	hub := permFixtureHub(source)
	hub.LockingAllowed = true
	return hub
}

// lockOwner holds the member role and a rank role, so the channel they
// spawn is theirs to lock.
var lockOwner = permMember{id: "user-o", roles: []string{permRoleMember, testRankSGT}}

// newLockScene builds the permission fixture over a hub that allows
// locking, spawns chan-1 for lockOwner, and brings G in. MOD stays outside,
// so MOD joins a locked channel on the moderator role alone. Nothing is
// locked yet.
func newLockScene(t *testing.T, source store.PermissionSource) (*fakeTempVCManager, *store.Fake, *TempVC) {
	t.Helper()
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	st := seedStore(t, lockingHub(source))
	tv := newTestTempVC(t, fake, st)
	spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
	enter(tv, fake, permG, "chan-1")
	return fake, st, tv
}

// enter delivers m's move into a channel, or their disconnect for an empty
// channel ID.
func enter(tv *TempVC, fake *fakeTempVCManager, m permMember, channelID string) {
	fake.deliver(tv, voiceEvent(m.id, channelID, m.discordMember()))
}

// lockInteraction builds the interaction /voice-lock or /voice-unlock
// receives from m: a guild command with no options.
func lockInteraction(m permMember) *discordgo.InteractionCreate {
	i := fakeAppCommandInteraction()
	i.GuildID = testTempVCGuild
	i.Member = &discordgo.Member{User: &discordgo.User{ID: m.id, Username: "tester"}, Roles: slices.Clone(m.roles)}
	return i
}

// lockAs runs /voice-lock as m, checks the reply took the deferred-ephemeral
// shape, and returns it.
func lockAs(t *testing.T, tv *TempVC, m permMember) string {
	t.Helper()
	f := &fakeResponder{}
	runVoiceLock(f, tv, lockInteraction(m))
	return ephemeralReply(t, f)
}

// unlockAs runs /voice-unlock as m the same way.
func unlockAs(t *testing.T, tv *TempVC, m permMember) string {
	t.Helper()
	f := &fakeResponder{}
	runVoiceUnlock(f, tv, lockInteraction(m))
	return ephemeralReply(t, f)
}

// assertJoins checks who can join a channel now: every member of can, and
// none of cannot.
func assertJoins(t *testing.T, fake *fakeTempVCManager, channelID, when string, can, cannot []permMember) {
	t.Helper()
	list := fake.overwritesOf(t, channelID)
	for _, m := range can {
		if !canJoin(t, list, m) {
			t.Errorf("%s: %s cannot join %s, want can", when, m.id, channelID)
		}
	}
	for _, m := range cannot {
		if canJoin(t, list, m) {
			t.Errorf("%s: %s can join %s, want cannot", when, m.id, channelID)
		}
	}
}

// assertJoinsLikeSource checks each fixture member gets the same verdict on
// a channel as on the permission source's own list.
func assertJoinsLikeSource(t *testing.T, fake *fakeTempVCManager, channelID, sourceID, when string) {
	t.Helper()
	got, source := fake.overwritesOf(t, channelID), fake.overwritesOf(t, sourceID)
	for _, m := range permMembers {
		if g, w := canJoin(t, got, m), canJoin(t, source, m); g != w {
			t.Errorf("%s: %s can join %s = %v, want %v as under the source %s", when, m.id, channelID, g, w, sourceID)
		}
	}
}

// joinVerdicts records every fixture member's can-join verdict on a
// channel, for a later comparison.
func joinVerdicts(t *testing.T, fake *fakeTempVCManager, channelID string) map[string]bool {
	t.Helper()
	list := fake.overwritesOf(t, channelID)
	out := make(map[string]bool, len(permMembers))
	for _, m := range permMembers {
		out[m.id] = canJoin(t, list, m)
	}
	return out
}

// rowLock reads a spawned channel's lock back from the store.
func rowLock(t *testing.T, st store.Store, channelID string) store.ChannelLock {
	t.Helper()
	row, ok := rowFor(t, st, channelID)
	if !ok {
		t.Fatalf("no row for %s", channelID)
	}
	return row.Lock
}

// pushHub changes the stored test hub and applies it to the runtime, the
// write then push the panel's save makes.
func pushHub(t *testing.T, st *store.Fake, tv *TempVC, change func(*store.Hub)) {
	t.Helper()
	hub, err := st.GetHub(context.Background(), storedHubID(t, st))
	if err != nil {
		t.Fatalf("GetHub: %v", err)
	}
	change(&hub)
	stored, err := st.UpsertHub(context.Background(), hub)
	if err != nil {
		t.Fatalf("UpsertHub: %v", err)
	}
	tv.ApplyHub(stored)
}

// permFixturePayload is a GUILD_CREATE for the permission fixture: its
// category and hub channel with their lists, the given spawned channels, and
// who is where.
func permFixturePayload(spawned []string, voice map[string]string) *discordgo.GuildCreate {
	g := sweepPayload(spawned, voice)
	for _, ch := range g.Channels {
		if ch.ID == testTempVCHub {
			ch.PermissionOverwrites = permHubChannelOverwrites()
		}
	}
	g.Channels = append(g.Channels, &discordgo.Channel{
		ID: testTempVCCategory, Type: discordgo.ChannelTypeGuildCategory,
		PermissionOverwrites: permCategoryOverwrites(),
	})
	return g
}

// A lock admits the people inside and the hub's moderators, and nobody else.
// Under the category source M and MOD can join and V and N cannot. Once the
// owner locks, M cannot, G (inside at the lock) and MOD (outside, on the
// moderator role) can, and V and N still cannot. The row records the lock
// and who set it.
func TestVoiceLockAdmitsGuestsAndModeratorsOnly(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	assertJoins(t, fake, "chan-1", "before the lock",
		[]permMember{permM, permMOD}, []permMember{permV, permN})

	lockAs(t, tv, lockOwner)

	assertJoins(t, fake, "chan-1", "locked",
		[]permMember{permG, permMOD}, []permMember{permM, permV, permN})
	if lock := rowLock(t, st, "chan-1"); !lock.Locked || lock.LockerUserID != lockOwner.id {
		t.Errorf("row lock = %+v, want locked by %s", lock, lockOwner.id)
	}
}

// Unlock puts the permission source back: afterwards every fixture member
// has the verdict the source alone gives, the guest included, for either
// source kind. The row reads back unlocked.
func TestVoiceUnlockGivesBackTheSourceVerdicts(t *testing.T) {
	for _, tc := range []struct {
		source store.PermissionSource
		from   string
	}{
		{store.PermissionCategory, testTempVCCategory},
		{store.PermissionHubChannel, testTempVCHub},
	} {
		t.Run(string(tc.source), func(t *testing.T) {
			fake, st, tv := newLockScene(t, tc.source)
			lockAs(t, tv, lockOwner)

			unlockAs(t, tv, lockOwner)

			assertJoinsLikeSource(t, fake, "chan-1", tc.from, "after the unlock")
			if lock := rowLock(t, st, "chan-1"); lock != (store.ChannelLock{}) {
				t.Errorf("row lock = %+v, want unlocked", lock)
			}
		})
	}
}

// Unlock clears the guest list, so the next lock starts from whoever is
// inside then. G, a guest of the first lock, leaves before the second and
// cannot join afterwards. It holds when the second lock comes before
// Discord's CHANNEL_UPDATE for the unlock reaches the cache: the cache has
// seen the first lock and none of what came after.
func TestVoiceLockAfterAnUnlockStartsANewGuestList(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	fake.lagCacheBehindEdits()
	unlockAs(t, tv, lockOwner)
	enter(tv, fake, permG, "")

	lockAs(t, tv, lockOwner)

	assertJoins(t, fake, "chan-1", "after the second lock",
		[]permMember{lockOwner, permMOD}, []permMember{permG, permM})
}

// A source with no overwrites at all still unlocks the channel, and leaves
// it with no overwrites, exactly the source. The join verdicts cannot tell
// an empty list from one holding an overwrite that grants and denies
// nothing, but Discord can: it shows a channel as synced with its category
// only when the two lists match.
func TestVoiceUnlockOfASourceWithNoOverwritesOpensTheChannel(t *testing.T) {
	for _, tc := range []struct {
		source store.PermissionSource
		from   string
	}{
		{store.PermissionCategory, testTempVCCategory},
		{store.PermissionHubChannel, testTempVCHub},
	} {
		t.Run(string(tc.source), func(t *testing.T) {
			fake := newFakeTempVCManager()
			installPermFixture(fake)
			fake.channels[tc.from].PermissionOverwrites = nil
			st := seedStore(t, lockingHub(tc.source))
			tv := newTestTempVC(t, fake, st)
			spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
			enter(tv, fake, permG, "chan-1")
			lockAs(t, tv, lockOwner)
			assertJoins(t, fake, "chan-1", "locked", nil, []permMember{permM})

			unlockAs(t, tv, lockOwner)

			assertJoinsLikeSource(t, fake, "chan-1", tc.from, "after the unlock")
			if list := fake.overwritesOf(t, "chan-1"); len(list) != 0 {
				t.Errorf("chan-1 holds %d overwrites after the unlock, want none, as the source %s has", len(list), tc.from)
			}
		})
	}
}

// An unlock that cannot read the hub's permission source is refused and the
// channel stays locked: no fallback list could be trusted not to open it
// wider than its hub allowed. The refusal sends no edit or message, leaves
// the lock notice alone, and changes no row.
func TestVoiceUnlockRefusedWhenTheSourceCannotBeRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source store.PermissionSource
		breaks func(tv *TempVC, fake *fakeTempVCManager)
	}{
		{"hub removed", store.PermissionCategory, func(tv *TempVC, _ *fakeTempVCManager) { tv.RemoveHub(testTempVCHub) }},
		{"hub channel gone", store.PermissionHubChannel, func(_ *TempVC, f *fakeTempVCManager) { f.dropChannel(testTempVCHub) }},
		{"category gone", store.PermissionCategory, func(_ *TempVC, f *fakeTempVCManager) { f.dropChannel(testTempVCCategory) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, st, tv := newLockScene(t, tc.source)
			lockAs(t, tv, lockOwner)
			tc.breaks(tv, fake)
			before := sceneOf(t, fake, st, "chan-1")

			unlockAs(t, tv, lockOwner)

			assertUnchanged(t, fake, st, "chan-1", "the refused unlock", before)
			assertJoins(t, fake, "chan-1", "after the refused unlock", nil, []permMember{permM})
		})
	}
}

// Turning "Locking allowed" off leaves an existing lock in place, and
// /voice-unlock still opens the channel. Only a new lock is refused.
func TestVoiceLockSettingTurnedOffKeepsExistingLocks(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)

	pushHub(t, st, tv, func(h *store.Hub) { h.LockingAllowed = false })

	assertJoins(t, fake, "chan-1", "after locking was turned off", nil, []permMember{permM})
	unlockAs(t, tv, lockOwner)
	assertJoinsLikeSource(t, fake, "chan-1", testTempVCCategory, "after the unlock")

	lockAs(t, tv, lockOwner)
	assertJoins(t, fake, "chan-1", "after a lock with locking off", []permMember{permM}, nil)
}

// Authority reads the moderator roles live: once the guild-wide set drops
// role R, an R holder who locked the channel can no longer unlock it.
func TestVoiceUnlockReadsModeratorRolesLive(t *testing.T) {
	const roleR = "role-r"
	holder := permMember{id: "user-r", roles: []string{permRoleMember, roleR}}
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	tv.ApplyGuildModeratorRoles([]string{roleR})
	enter(tv, fake, holder, "chan-1")
	lockAs(t, tv, holder)
	assertJoins(t, fake, "chan-1", "locked by the R holder", nil, []permMember{permM})

	tv.ApplyGuildModeratorRoles([]string{})
	before := sceneOf(t, fake, st, "chan-1")
	unlockAs(t, tv, holder)

	assertUnchanged(t, fake, st, "chan-1", "the R holder's unlock", before)
	assertJoins(t, fake, "chan-1", "after the R holder's unlock", nil, []permMember{permM})
}

// A lock Discord refuses changes nothing: every verdict is the one before
// the attempt and the row reads back unlocked. The next lock goes through.
func TestVoiceLockRefusedByDiscordChangesNothing(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	before := joinVerdicts(t, fake, "chan-1")
	fake.editErr = restError(http.StatusInternalServerError, 0, rawBodyMarker)

	lockAs(t, tv, lockOwner)

	for id, want := range before {
		if got := joinVerdicts(t, fake, "chan-1")[id]; got != want {
			t.Errorf("%s can join = %v after the refused lock, want %v as before", id, got, want)
		}
	}
	if lock := rowLock(t, st, "chan-1"); lock != (store.ChannelLock{}) {
		t.Errorf("row lock = %+v, want unlocked", lock)
	}

	fake.editErr = nil
	lockAs(t, tv, lockOwner)
	assertJoins(t, fake, "chan-1", "after the retry", nil, []permMember{permM})
}

// Lock and unlock failures classify as rename failures do. A 5xx reaches
// Sentry once and a 429 does not, and neither reply carries the raw error.
// Unknown Channel means the channel is gone: it is untracked and its row
// dropped, with no capture.
func TestVoiceLockAndUnlockFailuresClassifyLikeRename(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		captures int
		gone     bool
	}{
		{"5xx", restError(http.StatusInternalServerError, 0, rawBodyMarker), 1, false},
		{"429", rateLimitError(time.Minute), 0, false},
		{"unknown channel", restError(http.StatusNotFound, discordgo.ErrCodeUnknownChannel, rawBodyMarker), 0, true},
	} {
		for _, action := range []string{"lock", "unlock"} {
			t.Run(tc.name+" on "+action, func(t *testing.T) {
				fake, st, tv := newLockScene(t, store.PermissionCategory)
				if action == "unlock" {
					lockAs(t, tv, lockOwner)
				}
				captures := countCaptures(t)
				fake.editErr = tc.err

				var reply string
				if action == "lock" {
					reply = lockAs(t, tv, lockOwner)
				} else {
					reply = unlockAs(t, tv, lockOwner)
				}

				if strings.Contains(reply, rawBodyMarker) {
					t.Errorf("reply %q leaks the raw Discord error", reply)
				}
				if *captures != tc.captures {
					t.Errorf("captures = %d, want %d", *captures, tc.captures)
				}
				_, tracked := tv.Owner("chan-1")
				_, hasRow := rowFor(t, st, "chan-1")
				if tracked == tc.gone || hasRow == tc.gone {
					t.Errorf("tracked %v, row %v; want both %v", tracked, hasRow, !tc.gone)
				}
			})
		}
	}
}

// A /voice-unlock sent while a lock is landing waits for the lock, then
// unlocks: afterwards the channel has its source's verdicts and its row
// reads unlocked.
func TestVoiceUnlockSentWhileALockIsInFlightWaitsThenUnlocks(t *testing.T) {
	fake, mgr, st, tv := newWindowScene(t)
	var unlock *started
	mgr.onEdit(func() {
		unlock = startInteraction(func(f *fakeResponder) {
			runVoiceUnlock(f, tv, lockInteraction(lockOwner))
		})
	})

	lockAs(t, tv, lockOwner)
	unlock.reply(t)

	assertUnlocked(t, fake, st, "chan-1", "after the lock and the unlock")
}

// A /voice-lock sent while an unlock is landing waits for the unlock, then
// locks: afterwards G, inside, and MOD can join, M cannot, and the row reads
// locked.
func TestVoiceLockSentWhileAnUnlockIsInFlightWaitsThenLocks(t *testing.T) {
	fake, mgr, st, tv := newWindowScene(t)
	lockAs(t, tv, lockOwner)
	var lock *started
	mgr.onEdit(func() {
		lock = startInteraction(func(f *fakeResponder) {
			runVoiceLock(f, tv, lockInteraction(lockOwner))
		})
	})

	unlockAs(t, tv, lockOwner)
	lock.reply(t)

	assertJoins(t, fake, "chan-1", "after the unlock and the lock",
		[]permMember{permG, permMOD}, []permMember{permM})
	if lock := rowLock(t, st, "chan-1"); !lock.Locked {
		t.Errorf("row lock = %+v, want locked", lock)
	}
}

// After a restart the sweep restores the lock from the row, so /voice-unlock
// on the restarted runtime opens the channel.
func TestVoiceUnlockAfterARestartSweep(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)

	restarted := newTestTempVC(t, fake, st)
	fake.deliverGuildCreate(restarted, permFixturePayload([]string{"chan-1"},
		map[string]string{permG.id: "chan-1", lockOwner.id: "chan-1"}))
	unlockAs(t, restarted, lockOwner)

	assertJoinsLikeSource(t, fake, "chan-1", testTempVCCategory, "after the unlock")
	if lock := rowLock(t, st, "chan-1"); lock != (store.ChannelLock{}) {
		t.Errorf("row lock = %+v, want unlocked", lock)
	}
}

// lockFailingStore wraps a Fake and refuses every lock write.
type lockFailingStore struct {
	*store.Fake
}

func (lockFailingStore) SetSpawnedChannelLock(context.Context, string, store.ChannelLock) error {
	return errors.New("connection reset")
}

// A lock whose row write fails still stands, captured, and a reconnect's
// sweep keeps it: the row is stale, and the runtime's record is the newer
// of the two. /voice-unlock then opens the channel.
func TestVoiceLockWithAFailedRowWriteSurvivesAReconnect(t *testing.T) {
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	base := seedStore(t, lockingHub(store.PermissionCategory))
	tv := newTestTempVC(t, fake, lockFailingStore{base})
	spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
	enter(tv, fake, permG, "chan-1")
	captures := countCaptures(t)

	lockAs(t, tv, lockOwner)

	if *captures != 1 {
		t.Errorf("captures = %d, want 1 for the failed row write", *captures)
	}
	assertJoins(t, fake, "chan-1", "locked", nil, []permMember{permM})

	fake.deliverGuildCreate(tv, permFixturePayload([]string{"chan-1"},
		map[string]string{permG.id: "chan-1", lockOwner.id: "chan-1"}))
	unlockAs(t, tv, lockOwner)

	assertJoinsLikeSource(t, fake, "chan-1", testTempVCCategory, "after the unlock")
}

// A lock leaves ownership, handover and rename alone. The owner locks and
// leaves; the handover names the next rank holder, the row keeps the lock,
// the channel stays locked, and the old owner comes back as a guest. The new
// owner renames the channel.
func TestVoiceLockSurvivesAHandoverAndLeavesRenameAlone(t *testing.T) {
	owner := lockOwner
	next := permMember{id: "user-p", roles: []string{permRoleMember, testRankPVT}}
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	st := seedStore(t, lockingHub(store.PermissionCategory))
	tv := newTestTempVC(t, fake, st)
	spawnInto(tv, fake, owner.id, "chan-1", owner.discordMember())
	enter(tv, fake, next, "chan-1")
	lockAs(t, tv, owner)

	enter(tv, fake, owner, "")

	if got, tracked := tv.Owner("chan-1"); got != next.id || !tracked {
		t.Errorf("Owner(chan-1) = %q, %v; want %s by handover", got, tracked, next.id)
	}
	row, _ := rowFor(t, st, "chan-1")
	if row.OwnerUserID != next.id || !row.Lock.Locked || row.Lock.LockerUserID != owner.id {
		t.Errorf("row = %+v, want owner %s and the lock by %s kept", row, next.id, owner.id)
	}
	assertJoins(t, fake, "chan-1", "after the handover", []permMember{owner}, []permMember{permM})

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction(next.id, next.roles, "Briefing"))
	edits := fake.recordedEdits()
	if last := edits[len(edits)-1]; last.name != "Briefing" {
		t.Errorf("last edit = %+v, want the new owner's rename to Briefing", last)
	}
	ephemeralReply(t, f)
}

// Each lock and unlock edit carries an audit log reason naming whoever ran
// the command.
func TestVoiceLockAndUnlockEditsNameTheInvoker(t *testing.T) {
	owner := lockOwner
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	tv := newTestTempVC(t, fake, seedStore(t, lockingHub(store.PermissionCategory)))
	spawnInto(tv, fake, owner.id, "chan-1", owner.discordMember())
	enter(tv, fake, permMOD, "chan-1")

	lockAs(t, tv, permMOD)
	unlockAs(t, tv, owner)

	edits := fake.recordedEdits()
	if len(edits) != 2 {
		t.Fatalf("edits = %+v, want the lock and the unlock", edits)
	}
	if !strings.Contains(edits[0].reason, permMOD.id) {
		t.Errorf("lock audit reason %q does not name %s", edits[0].reason, permMOD.id)
	}
	if !strings.Contains(edits[1].reason, owner.id) {
		t.Errorf("unlock audit reason %q does not name %s", edits[1].reason, owner.id)
	}
}

// Every refusal answers with the ephemeral shape and changes nothing: no
// edit, no message, no edit of the lock notice, no change to the row's lock,
// and nobody's join verdict moves.
func TestVoiceLockAndUnlockRefusals(t *testing.T) {
	owner := lockOwner
	for _, tc := range []struct {
		name string
		// hub is the stored hub; setup puts people in place and may lock.
		hub   store.Hub
		setup func(t *testing.T, tv *TempVC, fake *fakeTempVCManager)
		run   func(t *testing.T, tv *TempVC) string
	}{
		{"lock, not in voice", lockingHub(store.PermissionCategory),
			func(*testing.T, *TempVC, *fakeTempVCManager) {},
			func(t *testing.T, tv *TempVC) string { return lockAs(t, tv, permM) }},
		{"lock, not a spawned channel", lockingHub(store.PermissionCategory),
			func(_ *testing.T, tv *TempVC, fake *fakeTempVCManager) { enter(tv, fake, permM, "perm-1") },
			func(t *testing.T, tv *TempVC) string { return lockAs(t, tv, permM) }},
		{"lock, neither owner nor moderator", lockingHub(store.PermissionCategory),
			func(_ *testing.T, tv *TempVC, fake *fakeTempVCManager) { enter(tv, fake, permM, "chan-1") },
			func(t *testing.T, tv *TempVC) string { return lockAs(t, tv, permM) }},
		{"lock, locking not turned on", permFixtureHub(store.PermissionCategory),
			func(*testing.T, *TempVC, *fakeTempVCManager) {},
			func(t *testing.T, tv *TempVC) string { return lockAs(t, tv, owner) }},
		{"lock, already locked", lockingHub(store.PermissionCategory),
			func(t *testing.T, tv *TempVC, _ *fakeTempVCManager) { lockAs(t, tv, owner) },
			func(t *testing.T, tv *TempVC) string { return lockAs(t, tv, permMOD) }},
		{"lock, channel missing from the state cache", lockingHub(store.PermissionCategory),
			func(_ *testing.T, _ *TempVC, fake *fakeTempVCManager) { fake.channelErr = discordgo.ErrStateNotFound },
			func(t *testing.T, tv *TempVC) string { return lockAs(t, tv, owner) }},
		{"unlock, not locked", lockingHub(store.PermissionCategory),
			func(*testing.T, *TempVC, *fakeTempVCManager) {},
			func(t *testing.T, tv *TempVC) string { return unlockAs(t, tv, owner) }},
		{"unlock, not in voice", lockingHub(store.PermissionCategory),
			func(t *testing.T, tv *TempVC, _ *fakeTempVCManager) { lockAs(t, tv, owner) },
			func(t *testing.T, tv *TempVC) string { return unlockAs(t, tv, permM) }},
		{"unlock, not a spawned channel", lockingHub(store.PermissionCategory),
			func(t *testing.T, tv *TempVC, fake *fakeTempVCManager) {
				lockAs(t, tv, owner)
				enter(tv, fake, permM, "perm-1")
			},
			func(t *testing.T, tv *TempVC) string { return unlockAs(t, tv, permM) }},
		{"unlock, neither owner nor moderator", lockingHub(store.PermissionCategory),
			func(t *testing.T, tv *TempVC, fake *fakeTempVCManager) {
				enter(tv, fake, permM, "chan-1")
				lockAs(t, tv, owner)
			},
			func(t *testing.T, tv *TempVC) string { return unlockAs(t, tv, permM) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTempVCManager()
			installPermFixture(fake)
			st := seedStore(t, tc.hub)
			tv := newTestTempVC(t, fake, st)
			spawnInto(tv, fake, owner.id, "chan-1", owner.discordMember())
			enter(tv, fake, permMOD, "chan-1")
			tc.setup(t, tv, fake)
			before := sceneOf(t, fake, st, "chan-1")

			tc.run(t, tv)

			assertUnchanged(t, fake, st, "chan-1", "the refusal", before)
		})
	}
}

// The registry declares both commands whatever the runtime, with no options
// and no default member permissions: Server Settings alone decide who may
// run them.
func TestRegistryDeclaresVoiceLockAndUnlock(t *testing.T) {
	for _, name := range []string{"voice-lock", "voice-unlock"} {
		var def *discordgo.ApplicationCommand
		for _, d := range NewRegistry(nil).GetCommands() {
			if d.Name == name {
				def = d
			}
		}
		if def == nil {
			t.Errorf("%s is not registered", name)
			continue
		}
		if len(def.Options) != 0 {
			t.Errorf("%s options = %+v, want none", name, def.Options)
		}
		if def.DefaultMemberPermissions != nil {
			t.Errorf("%s default member permissions = %d, want none set", name, *def.DefaultMemberPermissions)
		}
	}
}

// On a host with no bot store there is no runtime, and both handlers refuse
// with the ephemeral shape instead of dereferencing a nil runtime.
func TestVoiceLockAndUnlockNoRuntimeRefused(t *testing.T) {
	lockAs(t, nil, permMOD)
	unlockAs(t, nil, permMOD)
}
