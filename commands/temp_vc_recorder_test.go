package commands

import (
	"context"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
)

// --- Temp VC ignores recorders (#384): a recorder is absent from every hub
// and spawned channel. Each case puts the recorder in the fake cache by
// delivering its voice events, the way the gateway does, and judges the
// runtime by what it asked the fake manager to do. ---

// testRecorder is the recorder account's user ID.
const testRecorder = "user-recorder"

// A recorder joining a hub spawns nothing and nobody gets moved.
func TestTempVCRecorderJoiningAHubSpawnsNothing(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)
	tv.ignoreRecorders([]string{testRecorder})

	fake.deliver(tv, voiceEvent(testRecorder, testTempVCHub, member("Recorder")))

	if creates := fake.recordedCreates(); len(creates) != 0 {
		t.Errorf("created %d channels, want none for a recorder", len(creates))
	}
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Errorf("moves = %+v, want none for a recorder", moves)
	}
}

// A spawned channel left holding only a recorder counts as empty: its
// delete delay starts when the last member leaves, and the channel is
// deleted when the delay passes.
func TestTempVCChannelHoldingOnlyARecorderIsDeletedAfterItsDelay(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	tv := newTestTempVC(t, fake, seedStore(t, delayHub(10)))
	tv.ignoreRecorders([]string{testRecorder})
	spawnInto(tv, fake, "user-a", "chan-x", member("A"))
	fake.deliver(tv, voiceEvent(testRecorder, "chan-x", member("Recorder")))

	fake.deliver(tv, voiceEvent("user-a", "", member("A")))

	clock.advance(10*time.Minute - time.Second)
	assertNotDeleted(t, fake, "chan-x", "a second before the delay, the recorder inside")
	clock.advance(time.Second)
	assertDeleted(t, fake, "chan-x", "once the delay passed, the recorder inside")
}

// Locking a spawned channel with a recorder inside leaves the recorder off
// the guest list and gives it no overwrite. The recorder holds the member
// role, which lets it join the channel while it is unlocked, so only a
// guest's overwrite could let it join the locked channel.
func TestVoiceLockWithARecorderInsideLeavesItOffTheGuestList(t *testing.T) {
	recorder := permMember{id: testRecorder, roles: []string{permRoleMember}}
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	tv.ignoreRecorders([]string{recorder.id})
	enter(tv, fake, recorder, "chan-1")

	lockAs(t, tv, lockOwner)

	assertJoins(t, fake, "chan-1", "locked with the recorder inside", nil, []permMember{recorder})
}

// The restart sweep treats a spawned channel holding only a recorder as
// empty: on a hub with no delete delay it deletes the channel at once.
func TestTempVCRestartSweepDeletesAChannelHoldingOnlyARecorder(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	row := store.SpawnedChannel{ChannelID: "chan-x", HubID: storedHubID(t, st), Number: 1}
	if err := st.UpsertSpawnedChannel(context.Background(), row); err != nil {
		t.Fatalf("UpsertSpawnedChannel: %v", err)
	}
	tv := newTestTempVC(t, fake, st)
	tv.ignoreRecorders([]string{testRecorder})

	fake.deliverGuildCreate(tv, sweepPayload([]string{"chan-x"}, map[string]string{testRecorder: "chan-x"}))

	assertDeleted(t, fake, "chan-x", "after the sweep, the recorder inside")
}
