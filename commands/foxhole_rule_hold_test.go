package commands

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
)

// A Foxhole page action holds the one-action-at-a-time rule from its start
// until its report's end is written, and nothing but its own end gives the
// rule back (#492).

// secondStarter is a forum user who starts a page action after
// pageStarter's, so the running action's starter says whose runs.
var secondStarter = ForumUser{ID: 2468, Username: "Jones.K"}

// endHold is the store fake with each report end write held inside the
// write until the test lets it through.
type endHold struct {
	store.Store
	entered chan int64
	release chan struct{}
	done    chan struct{}
	free    chan struct{}
}

// newEndHold holds the end writes of st. The test's end lets every held
// write go.
func newEndHold(t *testing.T, st store.Store) endHold {
	t.Helper()
	h := endHold{Store: st, entered: make(chan int64, 16), release: make(chan struct{}),
		done: make(chan struct{}, 16), free: make(chan struct{})}
	t.Cleanup(func() { close(h.free) })
	return h
}

func (h endHold) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	h.entered <- id
	select {
	case <-h.release:
	case <-h.free:
	}
	err := h.Store.EndFoxholeReport(ctx, id, diff)
	h.done <- struct{}{}
	return err
}

// next waits for a report's end write to reach the store, leaves it held,
// and returns the report's ID.
func (h endHold) next(t *testing.T) int64 {
	t.Helper()
	select {
	case id := <-h.entered:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("no report end write reached the store")
		return 0
	}
}

// pass lets the held end write through, and waits until it has returned.
func (h endHold) pass(t *testing.T) {
	t.Helper()
	select {
	case h.release <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("no report end write is held")
	}
	awaitSignal(t, h.done, "the report end write's return")
}

// startsWhileRunningRetried calls start, a page action's start, again
// while another Foxhole action holds the rule, the way a manager would,
// and fails the test unless it starts within 5 seconds.
func startsWhileRunningRetried(t *testing.T, what string, start func() error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := start()
		if err == nil {
			return
		}
		if !errors.Is(err, ErrActionRunning) || time.Now().After(deadline) {
			t.Fatalf("%s didn't start: %v", what, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// reportOf reads the report with the ID given from the store.
func reportOf(t *testing.T, st store.Store, id int64) ActionReport {
	t.Helper()
	stored, err := st.FoxholeReport(context.Background(), id)
	if err != nil {
		t.Fatalf("read report %d: %v", id, err)
	}
	var report ActionReport
	if err := json.Unmarshal(stored.Entry.Diff, &report); err != nil {
		t.Fatalf("decode report %d: %v", id, err)
	}
	return report
}

// A page action holds the rule while its report's end is written, and the
// next one, started once that write returns, holds it in turn: the page
// shows it, Stop works on it, and nothing else starts beside it.
func TestAPageActionHoldsTheRuleThroughItsEndAndTheNextHoldsItAfter(t *testing.T) {
	fake := store.NewFake()
	ends := newEndHold(t, fake)
	fx, hold := newPageRuntimeOver(t, ends)
	ctx := context.Background()
	if err := fx.Purge(ctx, PurgeBoth, pageStarter); err != nil {
		t.Fatalf("the first page purge didn't start: %v", err)
	}
	for range 3 {
		hold.next(t)
		hold.pass(t)
	}
	ends.next(t)

	if running, ok := fx.Running(); !ok || running.StartedBy != pageStarter.Username {
		t.Fatalf("while its end write is held, Running() = %+v, %v, want the action %s started", running, ok, pageStarter.Username)
	}
	if !fx.Busy() {
		t.Error("while its end write is held, Busy() is false")
	}
	if err := fx.Purge(ctx, PurgeBoth, secondStarter); !errors.Is(err, ErrActionRunning) {
		t.Fatalf("a page purge started while the first's end write was held: %v, want ErrActionRunning", err)
	}
	end, refused := fx.startCommand(CommandRun{Command: "/foxhole add", StartedAt: time.Now()})
	end()
	if refused == nil || refused.StartedBy != pageStarter.Username {
		t.Errorf("while its end write is held, startCommand refused with %+v, want the action %s started", refused, pageStarter.Username)
	}

	ends.pass(t)
	startsWhileRunningRetried(t, "the second page purge", func() error { return fx.Purge(ctx, PurgeBoth, secondStarter) })
	hold.next(t)

	if running, ok := fx.Running(); !ok || running.StartedBy != secondStarter.Username {
		t.Fatalf("with the second purge mid-run, Running() = %+v, %v, want the action %s started", running, ok, secondStarter.Username)
	}
	if !fx.Busy() {
		t.Error("with the second purge mid-run, Busy() is false")
	}
	if err := fx.Purge(ctx, PurgeBoth, pageStarter); !errors.Is(err, ErrActionRunning) {
		t.Errorf("a third page purge started beside the second: %v, want ErrActionRunning", err)
	}
	running, err := fake.RunningFoxholeReports(ctx)
	if err != nil || len(running) != 1 {
		t.Fatalf("the store's running reports = %+v, %v, want the second purge's", running, err)
	}
	second := running[0].ID
	if !fx.Stop(second, pageStarter) {
		t.Fatalf("Stop with the second purge's report %d stopped nothing", second)
	}
	hold.pass(t)
	if got := ends.next(t); got != second {
		t.Fatalf("report %d's end was written, want the second purge's, %d", got, second)
	}
	ends.pass(t)

	if got := reportOf(t, fake, second).Outcome; got != ReportStopped {
		t.Errorf("the second purge's report ended %q, want %q", got, ReportStopped)
	}
}

// listPanics is the store fake with its first read of the Foxhole records
// panicking, as a fault in the store's code would.
type listPanics struct {
	store.Store
	once sync.Once
}

func (s *listPanics) ListFoxholeRecords(ctx context.Context, guildID string) ([]store.FoxholeRecord, error) {
	s.once.Do(func() { panic("store: Foxhole records fault") })
	return s.Store.ListFoxholeRecords(ctx, guildID)
}

// A page action that panics as it starts leaves the rule free: nothing
// shows as running, and the next page action or command starts.
func TestAPageActionThatPanicsAsItStartsLeavesTheRuleFree(t *testing.T) {
	fx, _ := newPageRuntimeOver(t, &listPanics{Store: store.NewFake()})
	ctx := context.Background()
	func() {
		defer func() { _ = recover() }()
		_ = fx.Purge(ctx, PurgeBoth, pageStarter)
	}()

	if running, ok := fx.Running(); ok {
		t.Errorf("after the failed start, Running() reports %+v", running)
	}
	if fx.Busy() {
		t.Error("after the failed start, Busy() is true")
	}
	end, refused := fx.startCommand(CommandRun{Command: "/foxhole add", StartedAt: time.Now()})
	end()
	if refused != nil {
		t.Errorf("after the failed start, startCommand refused with %+v", refused)
	}
	if err := fx.Purge(ctx, PurgeBoth, pageStarter); err != nil {
		t.Errorf("the next page purge didn't start: %v", err)
	}
}

// progressPanics is the store fake with its first report progress write
// panicking, as a fault in the store's code would. ended receives each
// report ID whose end write has returned.
type progressPanics struct {
	store.Store
	once  sync.Once
	ended chan int64
}

func (s *progressPanics) UpdateFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	s.once.Do(func() { panic("store: Foxhole report fault") })
	return s.Store.UpdateFoxholeReport(ctx, id, diff)
}

func (s *progressPanics) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	err := s.Store.EndFoxholeReport(ctx, id, diff)
	s.ended <- id
	return err
}

// idsOf are the members' IDs, sorted.
func idsOf(members []ReportMember) []string {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids
}

// A page action that panics partway through its run ends its report as a
// restart would: the members it changed as changed, the rest as not
// attempted. The rule comes free, so the report's Retry starts.
func TestAPageActionThatPanicsMidRunEndsItsReportAsARestart(t *testing.T) {
	fake := store.NewFake()
	st := &progressPanics{Store: fake, ended: make(chan int64, 16)}
	fx, hold := newPageRuntimeOver(t, st)
	hold.open()
	ctx := context.Background()
	if err := fx.Purge(ctx, PurgeBoth, pageStarter); err != nil {
		t.Fatalf("the page purge didn't start: %v", err)
	}
	var id int64
	select {
	case id = <-st.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the page purge's report never ended")
	}
	var answered []string
	for drained := false; !drained; {
		select {
		case member := <-hold.entered:
			answered = append(answered, member)
		default:
			drained = true
		}
	}
	slices.Sort(answered)

	report := reportOf(t, fake, id)
	if report.Outcome != ReportRestart {
		t.Errorf("the report ended %q, want %q", report.Outcome, ReportRestart)
	}
	if got := idsOf(report.Changed); len(got) == 0 || !slices.Equal(got, answered) {
		t.Errorf("the report lists %v as changed, want the members Discord changed, %v", got, answered)
	}
	holders := []string{"100000000000000001", "100000000000000002", "100000000000000003"}
	rest := slices.DeleteFunc(holders, func(id string) bool { return slices.Contains(answered, id) })
	if got := idsOf(report.NotAttempted); !slices.Equal(got, rest) {
		t.Errorf("the report lists %v as not attempted, want the members the run never reached, %v", got, rest)
	}
	startsWhileRunningRetried(t, "the report's Retry", func() error { return fx.Retry(ctx, id, pageStarter) })
}
