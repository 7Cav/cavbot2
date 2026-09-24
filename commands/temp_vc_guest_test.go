package commands

import (
	"net/http"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// The guest list (#350). Anyone who gets into a locked channel, however
// they got in, can leave and come back, and a restart never strands anyone.
// Every case judges the channel with the can-join oracle over the list the
// fake holds for it, never the shape of that list.

// The spec's sweep members: O is inside a locked channel when the restart
// sweep runs, X joins after it. Both hold the viewer role, so the source
// alone never lets them in.
var (
	permO = permMember{id: "user-o-viewer", roles: []string{permRoleViewer}}
	permX = permMember{id: "user-x", roles: []string{permRoleViewer}}
)

// editWindowManager runs a hook once, inside the next channel edit, after
// the fake has answered it and before the runtime reads the answer: the
// window while a lock or unlock is in flight with its edit already applied
// on Discord's side. The runtime holds no lock across the edit, so the hook
// can deliver a gateway event through the fake.
type editWindowManager struct {
	*fakeTempVCManager
	duringEdit func()
}

func (m *editWindowManager) ChannelEdit(channelID string, data *discordgo.ChannelEdit, reason string) (*discordgo.Channel, error) {
	ch, err := m.fakeTempVCManager.ChannelEdit(channelID, data, reason)
	if hook := m.duringEdit; hook != nil {
		m.duringEdit = nil
		hook()
	}
	return ch, err
}

// newEditWindowScene is newLockScene over an editWindowManager: lockOwner
// spawns chan-1 on a hub that allows locking and G comes in. Nothing is
// locked yet.
func newEditWindowScene(t *testing.T) (*fakeTempVCManager, *editWindowManager, *TempVC) {
	t.Helper()
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	mgr := &editWindowManager{fakeTempVCManager: fake}
	tv := newTestTempVC(t, mgr, seedStore(t, lockingHub(store.PermissionCategory)))
	spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
	enter(tv, fake, permG, "chan-1")
	return fake, mgr, tv
}

// V gets into the locked channel (a stubbed join, as a moderator's drag
// arrives), leaves, and can join again. Nobody else gains anything: M
// still cannot join.
func TestGuestJoinIntoALockedChannelCanRejoin(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	assertJoins(t, fake, "chan-1", "locked", nil, []permMember{permV})

	enter(tv, fake, permV, "chan-1")
	enter(tv, fake, permV, "")

	assertJoins(t, fake, "chan-1", "after V's visit", []permMember{permV}, []permMember{permM})
}

// A moderator who walks into a locked channel is a guest too, so losing the
// moderator role does not shut them out of the meeting they were in.
func TestGuestModeratorWalkingInKeepsAccessWithoutTheRole(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)

	enter(tv, fake, permMOD, "chan-1")
	enter(tv, fake, permMOD, "")

	demoted := permMember{id: permMOD.id, roles: []string{permRoleMember}}
	assertJoins(t, fake, "chan-1", "after the moderator's visit", []permMember{demoted}, []permMember{permM})
}

// Unlock clears the guest list. V is dragged in and leaves, the channel
// unlocks and locks again with V outside, and V cannot join.
func TestGuestListClearedByUnlock(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	enter(tv, fake, permV, "chan-1")
	enter(tv, fake, permV, "")

	unlockAs(t, tv, lockOwner)
	lockAs(t, tv, lockOwner)

	assertJoins(t, fake, "chan-1", "after the second lock", []permMember{permG}, []permMember{permV})
}

// Sweep (a): O is inside the locked channel when the sweep runs, with no
// guest overwrite, because the bot never saw O's join. After the sweep O
// leaves and can join. It holds after a restart, which reads the lock from
// the row, and after a reconnect, which keeps the runtime's own.
func TestGuestSweepAddsEveryoneInside(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime func(t *testing.T, tv *TempVC, fake *fakeTempVCManager, st *store.Fake) *TempVC
	}{
		{"restart", func(t *testing.T, _ *TempVC, fake *fakeTempVCManager, st *store.Fake) *TempVC {
			return newTestTempVC(t, fake, st)
		}},
		{"reconnect", func(_ *testing.T, tv *TempVC, _ *fakeTempVCManager, _ *store.Fake) *TempVC { return tv }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, st, tv := newLockScene(t, store.PermissionCategory)
			lockAs(t, tv, lockOwner)
			swept := tc.runtime(t, tv, fake, st)

			fake.deliverGuildCreate(swept, permFixturePayload([]string{"chan-1"},
				map[string]string{lockOwner.id: "chan-1", permG.id: "chan-1", permO.id: "chan-1"}))
			enter(swept, fake, permO, "")

			assertJoins(t, fake, "chan-1", "after O left", []permMember{permO}, []permMember{permM})
		})
	}
}

// Sweep (b): after a restart, X joins the locked channel and leaves, and X
// can join. The restarted runtime knows the channel is locked from its row.
func TestGuestJoinAfterARestartSweep(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	restarted := newTestTempVC(t, fake, st)
	fake.deliverGuildCreate(restarted, permFixturePayload([]string{"chan-1"},
		map[string]string{lockOwner.id: "chan-1", permG.id: "chan-1"}))

	enter(restarted, fake, permX, "chan-1")
	enter(restarted, fake, permX, "")

	assertJoins(t, fake, "chan-1", "after X's visit", []permMember{permX}, []permMember{permM})
}

// A member who gets in while the lock is landing, after the lock took its
// guest list and before it finished, is a guest too.
func TestGuestJoinWhileTheLockIsInFlight(t *testing.T) {
	fake, mgr, tv := newEditWindowScene(t)
	mgr.duringEdit = func() { enter(tv, fake, permV, "chan-1") }

	lockAs(t, tv, lockOwner)
	enter(tv, fake, permV, "")

	assertJoins(t, fake, "chan-1", "after V's visit", []permMember{permV}, []permMember{permM})
}

// A guest who comes back while the unlock is landing is not carried past
// it: the next lock, with the guest outside, keeps them out.
func TestGuestRejoinWhileTheUnlockIsInFlightIsCleared(t *testing.T) {
	fake, mgr, tv := newEditWindowScene(t)
	lockAs(t, tv, lockOwner)
	enter(tv, fake, permG, "")
	mgr.duringEdit = func() { enter(tv, fake, permG, "chan-1") }

	unlockAs(t, tv, lockOwner)
	enter(tv, fake, permG, "")
	lockAs(t, tv, lockOwner)

	assertJoins(t, fake, "chan-1", "after the second lock", []permMember{lockOwner}, []permMember{permG})
}

// A member who gets in while an unlock is in flight, when Discord then
// refuses the unlock, is a guest of the channel that stayed locked.
func TestGuestJoinWhileARefusedUnlockIsInFlight(t *testing.T) {
	fake, mgr, tv := newEditWindowScene(t)
	lockAs(t, tv, lockOwner)
	fake.editErr = restError(http.StatusInternalServerError, 0, rawBodyMarker)
	mgr.duringEdit = func() { enter(tv, fake, permV, "chan-1") }

	unlockAs(t, tv, lockOwner)
	enter(tv, fake, permV, "")

	assertJoins(t, fake, "chan-1", "after the refused unlock", []permMember{permV}, []permMember{permM})
}

// A guest add never shows anyone a channel the permission source hides
// from them. N holds no role, and H holds the member role but the category
// hides its channels from H by a member overwrite. Each is dragged into the
// locked channel and leaves, and neither can join.
func TestGuestAddNeverShowsAHiddenChannel(t *testing.T) {
	hidden := permMember{id: "user-h", roles: []string{permRoleMember}}
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	category := fake.channels[testTempVCCategory]
	category.PermissionOverwrites = append(category.PermissionOverwrites, &discordgo.PermissionOverwrite{
		ID: hidden.id, Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionViewChannel,
	})
	tv := newTestTempVC(t, fake, seedStore(t, lockingHub(store.PermissionCategory)))
	spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
	assertJoins(t, fake, "chan-1", "before the lock", []permMember{permM}, []permMember{hidden, permN})
	lockAs(t, tv, lockOwner)

	for _, m := range []permMember{permN, hidden} {
		enter(tv, fake, m, "chan-1")
		enter(tv, fake, m, "")
	}

	assertJoins(t, fake, "chan-1", "after their visits", nil, []permMember{permN, hidden})
}

// A guest add Discord refuses is a WARN line and never a Sentry event, and
// the channel stays locked.
func TestGuestAddRefusedByDiscordCapturesNothing(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	captures := countCaptures(t)
	fake.permissionSetErr = restError(http.StatusInternalServerError, 0, rawBodyMarker)

	enter(tv, fake, permV, "chan-1")

	if *captures != 0 {
		t.Errorf("captures = %d after a refused guest add, want 0", *captures)
	}
	assertJoins(t, fake, "chan-1", "after the refused add", []permMember{permG}, []permMember{permM})
}
