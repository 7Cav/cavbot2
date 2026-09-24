package commands

import (
	"net/http"
	"sync"
	"testing"
	"time"

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

// windowManager runs a test's hooks inside the fake's calls, the windows
// while a call is in flight. The runtime holds no lock across either call,
// so a hook can deliver a gateway event or start another interaction.
//   - The edit hook runs once, inside the next overwrite replace, after the
//     fake has applied it and before the runtime reads the answer: a lock
//     or unlock in flight with its edit already applied on Discord's side.
//   - The set hook runs once, inside the next overwrite set, before the
//     fake applies it: a guest add in flight.
type windowManager struct {
	*fakeTempVCManager
	hooks                 sync.Mutex
	duringEdit, duringSet func()
}

// onEdit and onSet install the edit and set hooks.
func (m *windowManager) onEdit(hook func()) { m.install(&m.duringEdit, hook) }
func (m *windowManager) onSet(hook func())  { m.install(&m.duringSet, hook) }

func (m *windowManager) install(slot *func(), hook func()) {
	m.hooks.Lock()
	defer m.hooks.Unlock()
	*slot = hook
}

// take removes a hook and returns it, nil when none is installed, so each
// runs once.
func (m *windowManager) take(slot *func()) func() {
	m.hooks.Lock()
	defer m.hooks.Unlock()
	hook := *slot
	*slot = nil
	return hook
}

func (m *windowManager) ChannelOverwritesReplace(channelID string, overwrites []*discordgo.PermissionOverwrite, reason string) error {
	err := m.fakeTempVCManager.ChannelOverwritesReplace(channelID, overwrites, reason)
	if hook := m.take(&m.duringEdit); hook != nil {
		hook()
	}
	return err
}

func (m *windowManager) ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64, reason string) error {
	if hook := m.take(&m.duringSet); hook != nil {
		hook()
	}
	return m.fakeTempVCManager.ChannelPermissionSet(channelID, targetID, targetType, allow, deny, reason)
}

// newWindowScene is newLockScene over a windowManager: lockOwner spawns
// chan-1 on a hub that allows locking and G comes in. Nothing is locked
// yet.
func newWindowScene(t *testing.T) (*fakeTempVCManager, *windowManager, *store.Fake, *TempVC) {
	t.Helper()
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	mgr := &windowManager{fakeTempVCManager: fake}
	st := seedStore(t, lockingHub(store.PermissionCategory))
	tv := newTestTempVC(t, mgr, st)
	spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
	enter(tv, fake, permG, "chan-1")
	return fake, mgr, st, tv
}

// concurrentGrace is how long an interaction started inside another's
// window gets to return while that window is open. A runtime that makes it
// wait for the change in flight holds it past the grace, and it runs once
// the change ends. A runtime that let it straight through would have it
// back well inside the grace, while the first change is still in flight.
const concurrentGrace = 150 * time.Millisecond

// started is an interaction delivered on a goroutine of its own, answering
// through a responder of its own. It never fails the test itself: reply
// checks its answer on the test's goroutine once it has returned.
type started struct {
	f    *fakeResponder
	done chan struct{}
}

// startInteraction delivers an interaction on its own goroutine, through
// deliver, and gives it concurrentGrace to return before going on.
func startInteraction(deliver func(f *fakeResponder)) *started {
	s := &started{f: &fakeResponder{}, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		deliver(s.f)
	}()
	s.grace()
	return s
}

// grace waits up to concurrentGrace for the interaction to return.
func (s *started) grace() {
	select {
	case <-s.done:
	case <-time.After(concurrentGrace):
	}
}

// reply waits for the interaction to return, checks its answer took the
// deferred-ephemeral shape, and returns it.
func (s *started) reply(t *testing.T) string {
	t.Helper()
	<-s.done
	return ephemeralReply(t, s.f)
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
	fake, mgr, _, tv := newWindowScene(t)
	mgr.onEdit(func() { enter(tv, fake, permV, "chan-1") })

	lockAs(t, tv, lockOwner)
	enter(tv, fake, permV, "")

	assertJoins(t, fake, "chan-1", "after V's visit", []permMember{permV}, []permMember{permM})
}

// A guest who comes back while the unlock is landing is not carried past
// it: the next lock, with the guest outside, keeps them out.
func TestGuestRejoinWhileTheUnlockIsInFlightIsCleared(t *testing.T) {
	fake, mgr, _, tv := newWindowScene(t)
	lockAs(t, tv, lockOwner)
	enter(tv, fake, permG, "")
	mgr.onEdit(func() { enter(tv, fake, permG, "chan-1") })

	unlockAs(t, tv, lockOwner)
	enter(tv, fake, permG, "")
	lockAs(t, tv, lockOwner)

	assertJoins(t, fake, "chan-1", "after the second lock", []permMember{lockOwner}, []permMember{permG})
}

// A member who gets in while an unlock is in flight, when Discord then
// refuses the unlock, is a guest of the channel that stayed locked.
func TestGuestJoinWhileARefusedUnlockIsInFlight(t *testing.T) {
	fake, mgr, _, tv := newWindowScene(t)
	lockAs(t, tv, lockOwner)
	fake.editErr = restError(http.StatusInternalServerError, 0, rawBodyMarker)
	mgr.onEdit(func() { enter(tv, fake, permV, "chan-1") })

	unlockAs(t, tv, lockOwner)
	enter(tv, fake, permV, "")

	assertJoins(t, fake, "chan-1", "after the refused unlock", []permMember{permV}, []permMember{permM})
}

// A member who joins while a lock is landing, with an unlock waiting on that
// lock, is added to the guest list before the unlock goes out, so the unlock
// drops them: afterwards V has the verdict the source alone gives. The
// unlock gets its grace again inside V's guest add, where it would land
// first if nothing held it back.
func TestGuestHeldByALockIsAddedBeforeTheUnlockWaitingOnIt(t *testing.T) {
	fake, mgr, st, tv := newWindowScene(t)
	var unlock *started
	mgr.onEdit(func() {
		enter(tv, fake, permV, "chan-1")
		unlock = startInteraction(func(f *fakeResponder) {
			runVoiceUnlock(f, tv, lockInteraction(lockOwner))
		})
		mgr.onSet(unlock.grace)
	})

	lockAs(t, tv, lockOwner)
	unlock.reply(t)
	enter(tv, fake, permV, "")

	assertUnlocked(t, fake, st, "chan-1", "after the lock and the unlock")
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
