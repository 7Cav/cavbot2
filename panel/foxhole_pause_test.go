package panel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
)

// pauseClock stands in for the clock a Foxhole action's pause is timed on:
// commands.FoxholeAfterFunc schedules on it. A test waits for an action to
// pause by waiting for the timers a pause schedules, with no sleep, and
// advance runs each timer that falls due on the test's own goroutine.
type pauseClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*pauseTimer
	// scheduled receives, without blocking, each time a timer is scheduled.
	scheduled chan struct{}
}

// pauseTimer is one scheduled call, which never runs once done is set.
type pauseTimer struct {
	after time.Duration
	at    time.Duration
	f     func()
	done  bool
}

// installPauseClock puts a pause clock under every Foxhole runtime built
// for the rest of the test. Each runtime takes the clock when it is built,
// so a runtime left running by an earlier test keeps its own.
func installPauseClock(t *testing.T) *pauseClock {
	t.Helper()
	c := &pauseClock{scheduled: make(chan struct{}, 1)}
	prev := commands.FoxholeAfterFunc
	commands.FoxholeAfterFunc = c.afterFunc
	t.Cleanup(func() { commands.FoxholeAfterFunc = prev })
	return c
}

func (c *pauseClock) afterFunc(d time.Duration, f func()) func() {
	c.mu.Lock()
	tm := &pauseTimer{after: d, at: c.now + d, f: f}
	c.timers = append(c.timers, tm)
	c.mu.Unlock()
	select {
	case c.scheduled <- struct{}{}:
	default:
	}
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		tm.done = true
	}
}

// pending reports whether a timer of d is waiting to run.
func (c *pauseClock) pending(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tm := range c.timers {
		if !tm.done && tm.after == d {
			return true
		}
	}
	return false
}

// awaitPause waits for a Foxhole action to pause: its pause limit and its
// next read of the member list are both waiting on the clock.
func (c *pauseClock) awaitPause(t *testing.T) {
	t.Helper()
	deadline := time.After(hangLimit)
	for !c.pending(commands.FoxholePauseLimit) || !c.pending(commands.FoxholePausePoll) {
		select {
		case <-c.scheduled:
		case <-deadline:
			t.Fatal("the Foxhole action never paused")
		}
	}
}

// advance moves the clock forward by d and runs each timer due by then.
func (c *pauseClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	var due []*pauseTimer
	for _, tm := range c.timers {
		if !tm.done && tm.at <= c.now {
			tm.done = true
			due = append(due, tm)
		}
	}
	c.mu.Unlock()
	for _, tm := range due {
		tm.f()
	}
}

// pauseCases are the two ways the member list stops being fit to check a
// member against: it goes partial, or the bot's gateway connection drops
// with the list complete.
var pauseCases = []struct {
	name      string
	status    commands.MemberListStatus
	connected bool
}{
	{"the member list goes partial", commands.MemberListArriving, true},
	{"the bot disconnects", commands.MemberListComplete, false},
}

// pauseMidRun starts a purge of Internal, takes the member list away as
// status and connected say while its first change is in flight, lets every
// change through, and waits until the purge pauses before its next member.
func pauseMidRun(t *testing.T, w *testWorld, status commands.MemberListStatus, connected bool) {
	t.Helper()
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	hold.next(t)
	w.discord.setListStatus(status, connected)
	hold.open()
	w.pause.awaitPause(t)
}

// A running action pauses before its next member while the member list is
// partial or the bot is disconnected. The progress block, shown beside the
// member list notice when the list is partial, says it is waiting for the
// member list, and its Stop still stops the action, with the members it
// never reached listed as not attempted.
func TestActionPausesBeforeItsNextMemberAndStopStillWorks(t *testing.T) {
	for _, tc := range pauseCases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			w.p.pageBudget = partialListBudget
			pauseMidRun(t, w, tc.status, tc.connected)

			progress := progressBlock(t, parseHTML(t, w.b.get(foxholePath)))

			if findElement(progress, "", "data-field", "paused") == nil {
				t.Error("the progress block doesn't say the action is paused for the member list")
			}
			assertRedirect(t, submit(t, w.b, stopForm(t, progress)), foxholePath)
			w.awaitActionEnd(t)
			report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
			if got := outcomeOf(report); got != "stopped" {
				t.Errorf("the report's outcome is %q, want stopped", got)
			}
			if got := fieldText(t, reportList(t, report, "not-attempted"), "count"); got != "2" {
				t.Errorf("the report counts %s not attempted, want the 2 Internal holders it never reached", got)
			}
			if writes := w.discord.roleChanges(); len(writes) != 1 {
				t.Errorf("Discord got %d role changes, want only the one made before the pause", len(writes))
			}
		})
	}
}

// A pause that outlasts the pause limit stops the action: it sends Discord
// nothing more, and its report says the member list didn't arrive and lists
// the members it never reached as not attempted.
func TestPauseLongerThanTheLimitStopsTheAction(t *testing.T) {
	w := newFoxholeWorld(t)
	w.p.pageBudget = partialListBudget
	pauseMidRun(t, w, commands.MemberListArriving, true)

	w.pause.advance(commands.FoxholePauseLimit)

	w.awaitActionEnd(t)
	report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
	if got := outcomeOf(report); got != "member-list" {
		t.Errorf("the report's outcome is %q, want member-list", got)
	}
	if got := fieldText(t, reportList(t, report, "not-attempted"), "count"); got != "2" {
		t.Errorf("the report counts %s not attempted, want the 2 Internal holders it never reached", got)
	}
	if writes := w.discord.roleChanges(); len(writes) != 1 {
		t.Errorf("Discord got %d role changes, want only the one made before the pause", len(writes))
	}
}

// A paused action carries on once the member list is complete again, and
// runs to its end.
func TestPausedActionRunsOnOnceTheMemberListIsBack(t *testing.T) {
	w := newFoxholeWorld(t)
	pauseMidRun(t, w, commands.MemberListArriving, true)

	w.discord.setListStatus(commands.MemberListComplete, true)
	w.pause.advance(commands.FoxholePausePoll)

	w.awaitActionEnd(t)
	report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
	if got := outcomeOf(report); got != "done" {
		t.Errorf("the report's outcome is %q, want done", got)
	}
	if got := fieldText(t, reportList(t, report, "changed"), "count"); got != "3" {
		t.Errorf("the report counts %s changed, want all 3 Internal holders", got)
	}
}

// A purge confirmed while the bot is disconnected, with the member list
// complete, starts paused: it changes nobody, and its progress block says
// it is waiting, until the connection is back. Then it runs to its end.
func TestPurgeConfirmedDuringADisconnectStartsPausedAndRunsOnceConnected(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.setListStatus(commands.MemberListComplete, false)
	confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "internal")))

	assertRedirect(t, confirmPurge(t, w.b, context.Background(), confirm), foxholePath)

	w.pause.awaitPause(t)
	progress := progressBlock(t, parseHTML(t, w.b.get(foxholePath)))
	if findElement(progress, "", "data-field", "paused") == nil {
		t.Error("the progress block doesn't say the purge is paused for the member list")
	}
	if done := fieldText(t, progress, "done"); done != "0" {
		t.Errorf("the progress block says %s done while disconnected, want 0", done)
	}
	if writes := w.discord.roleChanges(); len(writes) != 0 {
		t.Errorf("Discord got %d role changes while the bot was disconnected, want none", len(writes))
	}
	w.discord.setListStatus(commands.MemberListComplete, true)
	w.pause.advance(commands.FoxholePausePoll)
	w.awaitActionEnd(t)
	report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
	if got := outcomeOf(report); got != "done" {
		t.Errorf("the report's outcome is %q, want done", got)
	}
	if got := fieldText(t, reportList(t, report, "changed"), "count"); got != "3" {
		t.Errorf("the report counts %s changed, want all 3 Internal holders", got)
	}
}
