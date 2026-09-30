package panel

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// A hub page that runs out of time says, in its Sentry event, how its reads
// used the time budget (#395): every store read it started, in page order,
// how long each took, and which one the budget ran out during.

// timeBudget is the time_budget object in the event's extra context, as
// Sentry receives it. Its keys, the whole milliseconds under _ms keys and
// the outcome tokens are a test contract, like data-failure on the page.
type timeBudget struct {
	BudgetMS  *int64       `json:"budget_ms"`
	ElapsedMS *int64       `json:"elapsed_ms"`
	Reads     []budgetRead `json:"reads"`
}

type budgetRead struct {
	Read    string `json:"read"`
	Outcome string `json:"outcome"`
	TookMS  int64  `json:"took_ms"`
}

// The outcome tokens: a read that finished within the budget, and the read
// the budget ran out during.
const (
	wantFinished = "finished"
	wantRanOut   = "ran-out"
)

// onlyTimeBudget returns the time_budget of the one event recorded, and
// fails the test when there is not exactly one event or it carries none.
func onlyTimeBudget(t *testing.T, rec *sentryRecorder) timeBudget {
	t.Helper()
	events := rec.sent()
	if len(events) != 1 {
		t.Fatalf("sent %d Sentry events, want 1", len(events))
	}
	var event struct {
		Contexts struct {
			Extra struct {
				TimeBudget *timeBudget `json:"time_budget"`
			} `json:"extra"`
		} `json:"contexts"`
	}
	if err := json.Unmarshal(events[0], &event); err != nil {
		t.Fatalf("event %s: %v", events[0], err)
	}
	tb := event.Contexts.Extra.TimeBudget
	if tb == nil {
		t.Fatalf("event carries no time_budget in its extra context: %s", events[0])
	}
	if tb.BudgetMS == nil || tb.ElapsedMS == nil {
		t.Fatalf("time_budget has no budget_ms or elapsed_ms: %s", events[0])
	}
	return *tb
}

// assertReads checks the reads the report lists, in order, each as
// "name=outcome".
func assertReads(t *testing.T, tb timeBudget, want ...string) {
	t.Helper()
	var got []string
	for _, r := range tb.Reads {
		got = append(got, r.Read+"="+r.Outcome)
	}
	if !slices.Equal(got, want) {
		t.Errorf("reads = %q, want %q", got, want)
	}
}

// assertElapsedCoversBudget checks the report carries the budget as set, and
// an elapsed time from the budget's start of at least the budget.
func assertElapsedCoversBudget(t *testing.T, tb timeBudget, budget time.Duration) {
	t.Helper()
	if *tb.BudgetMS != budget.Milliseconds() {
		t.Errorf("budget_ms = %d, want %d", *tb.BudgetMS, budget.Milliseconds())
	}
	if *tb.ElapsedMS < budget.Milliseconds() {
		t.Errorf("elapsed_ms = %d, want at least the budget, %d", *tb.ElapsedMS, budget.Milliseconds())
	}
}

// A store read late in the page fails on the budget's deadline. Every read
// before it finished, and the elapsed time counts from the budget's start,
// not the slow read's.
func TestSlowStoreReadIsTheOneTheBudgetRanOutDuring(t *testing.T) {
	const budget = 100 * time.Millisecond
	w, st := newCtxWorld(t, testHub())
	w.p.pageBudget = budget
	signIn(t, w.forum, w.b)
	reported := recordSentry(t)
	st.blockRead("ListModeratorChanges", nil)

	w.b.get("/")

	tb := onlyTimeBudget(t, reported)
	assertReads(t, tb,
		"list hubs="+wantFinished, "read guild moderator roles="+wantFinished, "list moderator changes="+wantRanOut)
	assertElapsedCoversBudget(t, tb, budget)
}
