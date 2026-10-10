package commands

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
)

// --- The delete delay (#372): on a hub with one, a spawned channel that
// empties is deleted only once it has stayed empty that long. Every case
// runs on a fake clock, so no test waits real minutes, and judges a channel
// by whether the fake manager was asked to delete it. ---

// fakeClock stands in for the runtimes' clocks and timers: tempVCNow and
// recordingNow read its time and tempVCAfterFunc schedules on it. advance
// moves time forward and runs each timer that falls due, earliest first, on
// the test's own goroutine, so a wait ends in step with the test and the
// suite needs no lock around what the runtime does when it does.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// fakeTimer is one scheduled call. done is set once it has run or been
// stopped, and it never runs after that.
type fakeTimer struct {
	at   time.Time
	f    func()
	done bool
}

// installFakeClock puts a fake clock under the runtime for the length of
// the test.
func installFakeClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{now: time.Date(2026, time.September, 27, 18, 0, 0, 0, time.UTC)}
	prevNow, prevAfter, prevRecordingNow, prevRecordingAfter := tempVCNow, tempVCAfterFunc, recordingNow, recordingAfterFunc
	tempVCNow, tempVCAfterFunc, recordingNow, recordingAfterFunc = c.read, c.afterFunc, c.read, c.afterFunc
	t.Cleanup(func() {
		tempVCNow, tempVCAfterFunc, recordingNow, recordingAfterFunc = prevNow, prevAfter, prevRecordingNow, prevRecordingAfter
	})
	return c
}

func (c *fakeClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// afterFunc schedules f for d from now. A d of 0 or less is due at once and
// runs at the next advance, as time.AfterFunc runs it on a goroutine of its
// own rather than inside the caller. The stop it returns keeps f from
// running if it has not run yet.
func (c *fakeClock) afterFunc(d time.Duration, f func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, tm)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		tm.done = true
	}
}

// advance moves the clock forward by d, running every timer due by then in
// deadline order, each with the clock at its deadline. A timer scheduled by
// one that runs is picked up if it falls due inside d. advance(0) runs the
// timers already due.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, tm := range c.timers {
			if !tm.done && !tm.at.After(target) && (next == nil || tm.at.Before(next.at)) {
				next = tm
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.done = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// delayHub is the test hub with a delete delay.
func delayHub(minutes int) store.Hub {
	hub := testHub()
	hub.DeleteDelayMinutes = minutes
	return hub
}

// assertNotDeleted fails the test when the fake manager was asked to
// delete the channel.
func assertNotDeleted(t *testing.T, fake *fakeTempVCManager, channelID, when string) {
	t.Helper()
	for _, id := range fake.deletedIDs() {
		if id == channelID {
			t.Fatalf("%s: %s was deleted, want it kept", when, channelID)
		}
	}
}

// assertDeleted fails the test unless the fake manager deleted the channel
// exactly once.
func assertDeleted(t *testing.T, fake *fakeTempVCManager, channelID, when string) {
	t.Helper()
	n := 0
	for _, id := range fake.deletedIDs() {
		if id == channelID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s: %s deleted %d times, want once (deleted: %v)", when, channelID, n, fake.deletedIDs())
	}
}

// A channel that empties on a hub with a delay of 10 minutes is still there
// a second before the 10 minutes are up, row and all, and is deleted with
// its row once they are.
func TestDeleteDelayKeepsAnEmptiedChannelUntilTheDelayPasses(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	tv := newTestTempVC(t, fake, st)

	spawnAndLeave(tv, fake, "user-a", "chan-x")

	clock.advance(10*time.Minute - time.Second)
	assertNotDeleted(t, fake, "chan-x", "a second before the delay")
	if _, ok := rowFor(t, st, "chan-x"); !ok {
		t.Fatal("the row of chan-x is gone a second before the delay, want it kept")
	}

	clock.advance(time.Second)
	assertDeleted(t, fake, "chan-x", "once the delay passed")
	if _, ok := rowFor(t, st, "chan-x"); ok {
		t.Error("the row of chan-x is still stored after the delete")
	}
}

// A join during the wait keeps the channel, and the next empty starts a
// fresh, full wait. The joiner leaves at 7 minutes, before the first wait
// would have run out at 10, so the channel is kept past 10 and goes 10
// minutes after the second empty.
func TestDeleteDelayJoinKeepsTheChannelAndTheNextEmptyWaitsInFull(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	tv := newTestTempVC(t, fake, seedStore(t, delayHub(10)))
	spawnAndLeave(tv, fake, "user-a", "chan-x")

	clock.advance(5 * time.Minute)
	fake.deliver(tv, voiceEvent("user-b", "chan-x", member("B")))
	clock.advance(2 * time.Minute)
	fake.deliver(tv, voiceEvent("user-b", "", member("B")))

	clock.advance(10*time.Minute - time.Second)
	assertNotDeleted(t, fake, "chan-x", "a second before the second wait ends")
	clock.advance(time.Second)
	assertDeleted(t, fake, "chan-x", "10 minutes after the second empty")
}

// storedTestHub reads the test hub back from the store, ID and all, the way
// the panel holds it before a save.
func storedTestHub(t *testing.T, st store.Store) store.Hub {
	t.Helper()
	hub, err := st.GetHub(context.Background(), storedHubID(t, st))
	if err != nil {
		t.Fatalf("GetHub: %v", err)
	}
	return hub
}

// waitingPair empties chan-a and, 6 minutes later, chan-b on the test hub
// with a delay of 10, and leaves the clock 2 minutes after that: chan-a has
// been empty 8 minutes and chan-b 2.
func waitingPair(t *testing.T) (*fakeClock, *fakeTempVCManager, *store.Fake, *TempVC) {
	t.Helper()
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	tv := newTestTempVC(t, fake, st)
	spawnAndLeave(tv, fake, "user-a", "chan-a")
	clock.advance(6 * time.Minute)
	spawnAndLeave(tv, fake, "user-b", "chan-b")
	clock.advance(2 * time.Minute)
	return clock, fake, st, tv
}

// The bot reads the delay as it stands now. A save that lowers it deletes
// the hub's channels already empty that long, without waiting for the next
// empty, and the rest go once they have been empty for the new delay,
// counted from when each emptied. A save of 0 deletes every waiting one.
func TestDeleteDelayLoweredDeletesChannelsAlreadyEmptyThatLong(t *testing.T) {
	t.Run("lowered to 5", func(t *testing.T) {
		clock, fake, st, tv := waitingPair(t)
		hub := storedTestHub(t, st)
		hub.DeleteDelayMinutes = 5

		tv.ApplyHub(hub)
		clock.advance(0)

		assertDeleted(t, fake, "chan-a", "after the save, empty 8 minutes")
		assertNotDeleted(t, fake, "chan-b", "after the save, empty 2 minutes")
		clock.advance(3*time.Minute - time.Second)
		assertNotDeleted(t, fake, "chan-b", "a second before 5 minutes empty")
		clock.advance(time.Second)
		assertDeleted(t, fake, "chan-b", "5 minutes empty")
	})
	t.Run("lowered to 0", func(t *testing.T) {
		clock, fake, st, tv := waitingPair(t)
		hub := storedTestHub(t, st)
		hub.DeleteDelayMinutes = 0

		tv.ApplyHub(hub)
		clock.advance(0)

		assertDeleted(t, fake, "chan-a", "after a save of 0")
		assertDeleted(t, fake, "chan-b", "after a save of 0")
	})
}

// A save that raises the delay lengthens the waits already running: a
// channel empty 5 minutes when its hub goes from 10 to 20 is kept past its
// old deadline and deleted 20 minutes after it emptied.
func TestDeleteDelayRaisedKeepsAWaitingChannelUntilTheNewDelay(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	tv := newTestTempVC(t, fake, st)
	spawnAndLeave(tv, fake, "user-a", "chan-x")
	clock.advance(5 * time.Minute)
	hub := storedTestHub(t, st)
	hub.DeleteDelayMinutes = 20

	tv.ApplyHub(hub)

	clock.advance(15*time.Minute - time.Second)
	assertNotDeleted(t, fake, "chan-x", "a second before 20 minutes empty")
	clock.advance(time.Second)
	assertDeleted(t, fake, "chan-x", "20 minutes empty")
}

// A channel whose hub row is gone reads the delay as 0, so removing a hub
// deletes its waiting channels. A disabled hub keeps its settings, delay
// included, so its channels wait out the delay as before.
func TestDeleteDelayRemovedHubDeletesItsWaitingChannelsAndADisabledOneKeepsThem(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		clock, fake, _, tv := waitingPair(t)

		tv.RemoveHub(testTempVCHub)
		clock.advance(0)

		assertDeleted(t, fake, "chan-a", "after the hub was removed")
		assertDeleted(t, fake, "chan-b", "after the hub was removed")
	})
	t.Run("disabled", func(t *testing.T) {
		clock, fake, st, tv := waitingPair(t)
		hub := storedTestHub(t, st)
		hub.Enabled = false

		tv.ApplyHub(hub)
		clock.advance(0)

		assertNotDeleted(t, fake, "chan-a", "after the hub was disabled")
		clock.advance(2*time.Minute - time.Second)
		assertNotDeleted(t, fake, "chan-a", "a second before 10 minutes empty")
		clock.advance(time.Second)
		assertDeleted(t, fake, "chan-a", "10 minutes empty")
	})
}

// The restart sweep does not delete an empty recorded channel of a hub with
// a delay. It starts a fresh, full wait counted from the sweep, and the
// channel goes once that runs out with nobody joining.
func TestDeleteDelayRestartSweepStartsAFullWait(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	seedRow(t, st, "chan-x", 1, "")
	tv := newTestTempVC(t, fake, st)

	fake.deliverGuildCreate(tv, sweepPayload([]string{"chan-x"}, nil))

	assertNotDeleted(t, fake, "chan-x", "after the sweep")
	clock.advance(10*time.Minute - time.Second)
	assertNotDeleted(t, fake, "chan-x", "a second before 10 minutes after the sweep")
	clock.advance(time.Second)
	assertDeleted(t, fake, "chan-x", "10 minutes after the sweep")
	if _, ok := rowFor(t, st, "chan-x"); ok {
		t.Error("the row of chan-x is still stored after the delete")
	}
}

// GUILD_CREATE re-fires on every gateway reconnect. A sweep that finds a
// channel still empty and already waiting in this process keeps the wait's
// start, so reconnects never hold an empty channel past its delay (#372
// Q7): a channel empty since T, swept at T+4 and T+8, goes at T+10.
func TestDeleteDelayReconnectSweepKeepsTheStartOfARunningWait(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	tv := newTestTempVC(t, fake, seedStore(t, delayHub(10)))
	spawnAndLeave(tv, fake, "user-a", "chan-x")

	for range 2 {
		clock.advance(4 * time.Minute)
		fake.deliverGuildCreate(tv, sweepPayload([]string{"chan-x"}, nil))
		assertNotDeleted(t, fake, "chan-x", "after a reconnect sweep")
	}

	clock.advance(2*time.Minute - time.Second)
	assertNotDeleted(t, fake, "chan-x", "a second before 10 minutes empty")
	clock.advance(time.Second)
	assertDeleted(t, fake, "chan-x", "10 minutes after it emptied")
}

// The compensating delete after a failed move-into stays immediate on a hub
// with a delay: the channel never had anyone in it to empty.
func TestDeleteDelayLeavesAFailedMoveIntoDeletedAtOnce(t *testing.T) {
	installFakeClock(t)
	fake := newFakeTempVCManager()
	fake.moveErr = errors.New("target user is not connected to voice")
	tv := newTestTempVC(t, fake, seedStore(t, delayHub(10)))

	fake.deliver(tv, voiceEvent("user-1", testTempVCHub, member("A")))

	assertDeleted(t, fake, "new-chan", "right after the failed move-into")
}

// The handover rule is unchanged on a waiting channel: the owner who is the
// last one out leaves it with no owner, and the ownership notice says so,
// naming nobody, as it does when the owner leaves with no rank holder
// inside.
func TestDeleteDelayOwnerLastOutLeavesTheChannelWithNoOwner(t *testing.T) {
	installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	tv := newTestTempVC(t, fake, st)
	sgt := member("Sgt", testRankSGT)
	spawnInto(tv, fake, "sgt-1", "chan-x", sgt)

	fake.deliver(tv, voiceEvent("sgt-1", "", sgt))

	if owner, tracked := tv.Owner("chan-x"); owner != "" || !tracked {
		t.Errorf("Owner(chan-x) = %q, %v, want no owner, tracked", owner, tracked)
	}
	if row, ok := rowFor(t, st, "chan-x"); !ok || row.OwnerUserID != "" {
		t.Errorf("row = %+v (present %v), want an empty owner", row, ok)
	}
	notices := noticesIn(t, fake, "chan-x")
	if len(notices) != 2 {
		t.Fatalf("notices = %d, want a second one saying there is no owner", len(notices))
	}
	if c := notices[1].data.Content; strings.Contains(c, "sgt-1") {
		t.Errorf("notice %q names the owner who left, want no user ID", c)
	}
}

// The wait ends in the guarded delete, which checks the cache first. A
// member whose join the cache already shows, but whose join handler has not
// run, is inside: the channel is not deleted.
func TestDeleteDelayWaitEndingOnAChannelTheCacheShowsOccupiedKeepsIt(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	tv := newTestTempVC(t, fake, seedStore(t, delayHub(10)))
	spawnAndLeave(tv, fake, "user-a", "chan-x")
	fake.setVoice("user-b", "chan-x")

	clock.advance(10 * time.Minute)

	assertNotDeleted(t, fake, "chan-x", "when the wait ended with user-b inside")
}

// A wait start logs one INFO line naming the channel, its hub and the delay
// in minutes, and a join that cancels the wait logs one naming the channel,
// its hub and the member who joined.
func TestDeleteDelayLogsAWaitStartAndAJoinThatCancelsIt(t *testing.T) {
	installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	tv := newTestTempVC(t, fake, st)
	hubID := strconv.FormatInt(storedHubID(t, st), 10)
	logs := captureLogs(t)

	spawnAndLeave(tv, fake, "user-a", "chan-x")

	started := map[string]string{"channel_id": "chan-x", "hub_id": hubID, "delete_delay_minutes": "10"}
	if lines := logRecordsWith(t, logs, "INFO", started); len(lines) != 1 {
		t.Errorf("INFO lines carrying %v = %d, want one for the wait start", started, len(lines))
	}

	fake.deliver(tv, voiceEvent("user-b", "chan-x", member("B")))

	cancelled := map[string]string{"channel_id": "chan-x", "hub_id": hubID, "user_id": "user-b"}
	if lines := logRecordsWith(t, logs, "INFO", cancelled); len(lines) != 1 {
		t.Errorf("INFO lines carrying %v = %d, want one for the join that cancelled the wait", cancelled, len(lines))
	}
}

// At the restart sweep a stored owner who is no longer in the channel
// counts as having left (#264). An empty channel that the sweep leaves
// waiting on a hub with a delay therefore has no owner: its row loses the
// owner and the notice says so, naming nobody. A row that names no owner
// changes nothing and posts nothing.
func TestDeleteDelayRestartSweepLeavesAnEmptyWaitingChannelWithNoOwner(t *testing.T) {
	installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	seedRow(t, st, "chan-x", 1, "sgt-1")
	seedRow(t, st, "chan-y", 2, "")
	tv := newTestTempVC(t, fake, st)

	fake.deliverGuildCreate(tv, sweepPayload([]string{"chan-x", "chan-y"}, nil))

	if owner, tracked := tv.Owner("chan-x"); owner != "" || !tracked {
		t.Errorf("Owner(chan-x) = %q, %v, want no owner, tracked", owner, tracked)
	}
	if row, ok := rowFor(t, st, "chan-x"); !ok || row.OwnerUserID != "" {
		t.Errorf("row = %+v (present %v), want an empty owner", row, ok)
	}
	notices := noticesIn(t, fake, "chan-x")
	if len(notices) != 1 {
		t.Fatalf("notices in chan-x = %d, want one saying there is no owner", len(notices))
	}
	if c := notices[0].data.Content; strings.Contains(c, "sgt-1") {
		t.Errorf("notice %q names the owner who left, want no user ID", c)
	}
	if n := len(noticesIn(t, fake, "chan-y")); n != 0 {
		t.Errorf("notices in chan-y = %d, want none: its row named no owner", n)
	}
}

// A locked channel that empties keeps its lock through the wait (#372 Q5),
// and a member on its guest list can get back in: G, inside at the lock,
// can still join and M still cannot. G's rejoin cancels the wait, which
// logs its line, and the channel outlives the delay.
func TestDeleteDelayLockedChannelStaysLockedAndAGuestCanRejoin(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	hub := lockingHub(store.PermissionCategory)
	hub.DeleteDelayMinutes = 10
	st := seedStore(t, hub)
	tv := newTestTempVC(t, fake, st)
	spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
	enter(tv, fake, permG, "chan-1")
	lockAs(t, tv, lockOwner)
	enter(tv, fake, permG, "")
	enter(tv, fake, lockOwner, "")
	clock.advance(5 * time.Minute)

	assertJoins(t, fake, "chan-1", "5 minutes into the wait",
		[]permMember{permG, lockOwner, permMOD}, []permMember{permM})
	if lock := rowLock(t, st, "chan-1"); !lock.Locked {
		t.Errorf("row lock = %+v 5 minutes into the wait, want it locked", lock)
	}

	logs := captureLogs(t)
	enter(tv, fake, permG, "chan-1")
	clock.advance(10 * time.Minute)

	cancelled := map[string]string{"channel_id": "chan-1", "hub_id": strconv.FormatInt(storedHubID(t, st), 10), "user_id": permG.id}
	if lines := logRecordsWith(t, logs, "INFO", cancelled); len(lines) != 1 {
		t.Errorf("INFO lines carrying %v = %d, want one for G's rejoin cancelling the wait", cancelled, len(lines))
	}
	assertNotDeleted(t, fake, "chan-1", "10 minutes after G rejoined")
}

// The last owner out leaves the channel with no owner, and the first
// rank-role holder to join during the wait takes over. A handover is
// final, so a higher rank who joins after them does not.
func TestDeleteDelayFirstRankHolderToJoinDuringTheWaitBecomesOwner(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	tv := newTestTempVC(t, fake, st)
	sgt := member("Sgt", testRankSGT)
	spawnInto(tv, fake, "sgt-1", "chan-x", sgt)
	fake.deliver(tv, voiceEvent("sgt-1", "", sgt))
	clock.advance(5 * time.Minute)

	fake.deliver(tv, voiceEvent("pvt-1", "chan-x", member("Pvt", testRankPVT)))
	fake.deliver(tv, voiceEvent("cpt-1", "chan-x", member("Cpt", testRankCPT)))

	if owner, tracked := tv.Owner("chan-x"); owner != "pvt-1" || !tracked {
		t.Errorf("Owner(chan-x) = %q, %v, want pvt-1, the first rank holder to join, tracked", owner, tracked)
	}
	if row, ok := rowFor(t, st, "chan-x"); !ok || row.OwnerUserID != "pvt-1" {
		t.Errorf("row = %+v (present %v), want owner pvt-1", row, ok)
	}
}

// No path the delete delay adds reaches Sentry (#372 Q12): a wait's start,
// a join that cancels it, a save that re-times it, the sweep's kept and
// fresh waits, a wait's end with its delete, and a removed hub's waits
// going at once.
func TestDeleteDelayPathsSendNoSentryEvent(t *testing.T) {
	clock := installFakeClock(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, delayHub(10))
	seedRow(t, st, "chan-s", 2, "")
	tv := newTestTempVC(t, fake, st)
	captures := countCaptures(t)

	spawnAndLeave(tv, fake, "user-a", "chan-x")
	fake.deliver(tv, voiceEvent("user-b", "chan-x", member("B")))
	fake.deliver(tv, voiceEvent("user-b", "", member("B")))
	clock.advance(time.Minute)
	hub := storedTestHub(t, st)
	hub.DeleteDelayMinutes = 5
	tv.ApplyHub(hub)
	fake.deliverGuildCreate(tv, sweepPayload([]string{"chan-x", "chan-s"}, nil))
	clock.advance(4 * time.Minute)
	assertDeleted(t, fake, "chan-x", "5 minutes after it emptied")
	tv.RemoveHub(testTempVCHub)
	clock.advance(0)
	assertDeleted(t, fake, "chan-s", "after the hub was removed")

	if *captures != 0 {
		t.Errorf("captures = %d, want 0", *captures)
	}
}
