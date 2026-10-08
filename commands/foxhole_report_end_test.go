package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
)

// A page action's report whose end write fails every attempt at the
// action's end keeps trying in the background, every wait of the runtime's
// clock, until the store takes it, so the report shows how the action
// really ended without a restart (#513).

// errStoreDown is the error each write the outage refuses returns.
var errStoreDown = errors.New("store: connection refused")

// storeOutage is the store fake with its report writes refused while the
// matching switch is on: progressDown refuses the progress writes, endsDown
// the end writes. Reads always go through.
type storeOutage struct {
	store.Store
	progressDown, endsDown atomic.Bool
}

func (s *storeOutage) UpdateFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	if s.progressDown.Load() {
		return errStoreDown
	}
	return s.Store.UpdateFoxholeReport(ctx, id, diff)
}

func (s *storeOutage) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	if s.endsDown.Load() {
		return errStoreDown
	}
	return s.Store.EndFoxholeReport(ctx, id, diff)
}

// installFoxholeClock puts a fake clock under every Foxhole runtime built
// for the rest of the test.
func installFoxholeClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{now: time.Date(2026, time.October, 8, 18, 0, 0, 0, time.UTC)}
	prev := FoxholeAfterFunc
	FoxholeAfterFunc = c.afterFunc
	t.Cleanup(func() { FoxholeAfterFunc = prev })
	return c
}

// awaitFree waits until no Foxhole action holds the one-action rule, as it
// frees once the attempts at an action's end are over.
func awaitFree(t *testing.T, fx *FoxholeRuntime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for fx.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("the Foxhole action never gave the rule back")
		}
		time.Sleep(time.Millisecond)
	}
}

// lastReportID is the ID of the newest report the store holds.
func lastReportID(t *testing.T, st store.Store) int64 {
	t.Helper()
	last, err := st.LastFoxholeReport(context.Background())
	if err != nil {
		t.Fatalf("read the last report: %v", err)
	}
	return last.Entry.ID
}

// failingRoleChange is the page's held Discord with one member's role
// change refused, as Discord refuses a change the bot lacks permission for.
type failingRoleChange struct {
	*pageHold
	memberID string
}

func (f failingRoleChange) GuildMemberRoleAdd(guildID, userID, roleID, reason string) error {
	if err := f.pageHold.GuildMemberRoleAdd(guildID, userID, roleID, reason); err != nil || userID != f.memberID {
		return err
	}
	return restError(403, 50013, "Missing Permissions")
}

// The page's members, as pageGuild lists them, and bob, who isn't in the
// server.
const (
	kestrelID = "100000000000000001"
	ashID     = "100000000000000002"
	doeID     = "100000000000000003"
	bobID     = "100000000000000009"
)

// A re-add whose store is down from its first progress write through every
// end write ends, once the store is back, with how the action ended: the
// members it changed, skipped and failed on, the one it never reached, and
// who stopped it. The report then offers its Retry.
func TestAReportWhoseEndWriteFailsEndsOnceTheStoreIsBack(t *testing.T) {
	clock := installFoxholeClock(t)
	fake := store.NewFake()
	ctx := context.Background()
	approved := []store.MemberNames{
		{MemberID: kestrelID, Username: "kestrel"}, {MemberID: ashID, Username: "ash"},
		{MemberID: doeID, Username: "doe"}, {MemberID: bobID, DisplayName: "bob", Username: "bob"},
	}
	if err := fake.ApproveFoxholeMembers(ctx, "guild-1", approved,
		store.ChangeLogEntry{Action: store.ChangeApprove, Diff: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("approve the collaborators: %v", err)
	}
	st := &storeOutage{Store: fake}
	t.Setenv(foxholeRoleBaseNameEnv, "")
	t.Setenv(foxholeRoleBaseNameOldEnv, "")
	hold := &pageHold{entered: make(chan string, 16), release: make(chan struct{}), free: make(chan struct{})}
	t.Cleanup(hold.open)
	fx, err := NewFoxholeRuntime(pageGuild{}, failingRoleChange{hold, doeID}, st, "guild-1")
	if err != nil {
		t.Fatalf("NewFoxholeRuntime: %v", err)
	}

	if err := fx.ReAdd(ctx, pageStarter); err != nil {
		t.Fatalf("the re-add didn't start: %v", err)
	}
	id := lastReportID(t, fake)
	hold.next(t)
	st.progressDown.Store(true)
	st.endsDown.Store(true)
	hold.pass(t)
	hold.next(t)
	if !fx.Stop(id, pageStarter) {
		t.Fatal("Stop stopped nothing")
	}
	hold.pass(t)
	awaitFree(t, fx)
	for range 5 {
		clock.advance(foxholeEndRetryWait)
	}
	st.progressDown.Store(false)
	st.endsDown.Store(false)
	clock.advance(foxholeEndRetryWait)

	stored, err := fake.FoxholeReport(ctx, id)
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	if stored.Running {
		t.Fatal("one wait after the store came back, the report still reads running")
	}
	report := reportOf(t, fake, id)
	if report.Outcome != ReportStopped {
		t.Errorf("the report ended %q, want %q", report.Outcome, ReportStopped)
	}
	for _, list := range []struct {
		name string
		got  []ReportMember
		want []string
	}{
		{"changed", report.Changed, []string{ashID}},
		{"skipped", report.Skipped, []string{bobID}},
		{"failed", report.Failed, []string{doeID}},
		{"not attempted", report.NotAttempted, []string{kestrelID}},
	} {
		if got := idsOf(list.got); !slices.Equal(got, list.want) {
			t.Errorf("the report lists %v as %s, want %v", got, list.name, list.want)
		}
	}
	if err := fx.Retry(ctx, id, pageStarter); err != nil {
		t.Errorf("the report's Retry didn't start: %v", err)
	}
}

// lostAnswer is the store fake with its end writes refused until the one
// numbered commitAt, which the store takes but whose answer is lost, so it
// returns an error. Later end writes go through. ends counts every end
// write.
type lostAnswer struct {
	store.Store
	commitAt int32
	ends     atomic.Int32
}

func (s *lostAnswer) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	n := s.ends.Add(1)
	switch {
	case n < s.commitAt:
		return errStoreDown
	case n == s.commitAt:
		if err := s.Store.EndFoxholeReport(ctx, id, diff); err != nil {
			return err
		}
		return errors.New("store: connection reset after the write")
	}
	return s.Store.EndFoxholeReport(ctx, id, diff)
}

// An end write the store took although its answer was lost counts as
// written once the next attempt finds the report ended, whether that
// attempt comes at the action's end or in the background: the report ends
// with the action's outcome, no more end writes follow, and Sentry hears
// only of the attempts at the action's end that all failed.
func TestAnEndWriteTheStoreAlreadyTookCountsAsWritten(t *testing.T) {
	cases := []struct {
		name     string
		commitAt int32
		ends     int32
		captures int
	}{
		{"at the action's end", 1, 2, 0},
		{"in the background", foxholeEndAttempts + 1, foxholeEndAttempts + 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := installFoxholeClock(t)
			captures := countCaptures(t)
			fake := store.NewFake()
			st := &lostAnswer{Store: fake, commitAt: tc.commitAt}
			fx, hold := newPageRuntimeOver(t, st)
			hold.open()

			if err := fx.Purge(context.Background(), PurgeBoth, pageStarter); err != nil {
				t.Fatalf("the purge didn't start: %v", err)
			}
			awaitFree(t, fx)
			for range 5 {
				clock.advance(foxholeEndRetryWait)
			}

			id := lastReportID(t, fake)
			stored, err := fake.FoxholeReport(context.Background(), id)
			if err != nil {
				t.Fatalf("read the report: %v", err)
			}
			if got := reportOf(t, fake, id).Outcome; stored.Running || got != ReportDone {
				t.Errorf("the report reads running %v, ended %q, want ended %q", stored.Running, got, ReportDone)
			}
			if got := st.ends.Load(); got != tc.ends {
				t.Errorf("%d end writes reached the store, want %d", got, tc.ends)
			}
			// The run captures before it frees the rule, which awaitFree waits for.
			if got := *captures; got != tc.captures {
				t.Errorf("%d captures reached Sentry, want %d", got, tc.captures)
			}
		})
	}
}

// A report whose end write fails across many waits before it lands sends
// one event to Sentry, after the attempts at the action's end.
func TestAReportWhoseEndWriteFailsForLongReachesSentryOnce(t *testing.T) {
	clock := installFoxholeClock(t)
	captures := countCaptures(t)
	fake := store.NewFake()
	st := &storeOutage{Store: fake}
	st.endsDown.Store(true)
	fx, hold := newPageRuntimeOver(t, st)
	hold.open()

	if err := fx.Purge(context.Background(), PurgeBoth, pageStarter); err != nil {
		t.Fatalf("the purge didn't start: %v", err)
	}
	awaitFree(t, fx)
	for range 5 {
		clock.advance(foxholeEndRetryWait)
	}
	st.endsDown.Store(false)
	clock.advance(foxholeEndRetryWait)

	id := lastReportID(t, fake)
	stored, err := fake.FoxholeReport(context.Background(), id)
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	if got := reportOf(t, fake, id).Outcome; stored.Running || got != ReportDone {
		t.Errorf("the report reads running %v, ended %q, want ended %q", stored.Running, got, ReportDone)
	}
	if *captures != 1 {
		t.Errorf("%d captures reached Sentry, want 1", *captures)
	}
}

// A page action starts while an older report's end write is pending, and
// the late write, landing while it runs, leaves it as it was: the page
// shows the same action at the same progress, and its report is untouched.
// It then runs to its own end.
func TestALateEndWriteLeavesThePageActionRunningNowAsItWas(t *testing.T) {
	clock := installFoxholeClock(t)
	fake := store.NewFake()
	st := &storeOutage{Store: fake}
	fx, hold := newPageRuntimeOver(t, st)
	ctx := context.Background()
	if err := fx.Purge(ctx, PurgeBoth, pageStarter); err != nil {
		t.Fatalf("the first purge didn't start: %v", err)
	}
	first := lastReportID(t, fake)
	st.endsDown.Store(true)
	for range 3 {
		hold.next(t)
		hold.pass(t)
	}
	awaitFree(t, fx)
	st.endsDown.Store(false)

	if err := fx.Purge(ctx, PurgeBoth, otherManager); err != nil {
		t.Fatalf("the second purge didn't start while the first's end write was pending: %v", err)
	}
	second := lastReportID(t, fake)
	hold.next(t)
	hold.pass(t)
	hold.next(t)
	running, _ := fx.Running()
	before, err := fake.FoxholeReport(ctx, second)
	if err != nil {
		t.Fatalf("read the second report: %v", err)
	}
	clock.advance(foxholeEndRetryWait)

	if got := reportOf(t, fake, first).Outcome; got != ReportDone {
		t.Errorf("the first report ended %q, want %q", got, ReportDone)
	}
	if now, ok := fx.Running(); !ok || now != running {
		t.Errorf("after the late write, Running() = %+v, %v, want %+v as before it", now, ok, running)
	}
	after, err := fake.FoxholeReport(ctx, second)
	if err != nil {
		t.Fatalf("read the second report: %v", err)
	}
	if !after.Running || !bytes.Equal(after.Entry.Diff, before.Entry.Diff) {
		t.Errorf("the late write changed the second report: running %v, %s, want running, %s",
			after.Running, after.Entry.Diff, before.Entry.Diff)
	}

	hold.pass(t)
	hold.next(t)
	hold.pass(t)
	awaitFree(t, fx)
	report := reportOf(t, fake, second)
	if want := []string{kestrelID, ashID, doeID}; report.Outcome != ReportDone || !slices.Equal(idsOf(report.Changed), slices.Sorted(slices.Values(want))) {
		t.Errorf("the second report ended %q with %v changed, want %q with %v", report.Outcome, idsOf(report.Changed), ReportDone, want)
	}
}
