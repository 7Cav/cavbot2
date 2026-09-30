package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
)

// A hub page that runs out of time says, in its Sentry event, how its reads
// used the time budget (#395): every read it started, in page order, how
// long each took, which one the budget ran out during, and each Discord
// read's time split into waiting inside discordgo and trips to Discord.

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
	// Discord is set on a Discord read alone.
	Discord *struct {
		WaitingMS int64 `json:"waiting_ms"`
		TripsMS   int64 `json:"trips_ms"`
		Attempts  int   `json:"attempts"`
	} `json:"discord"`
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

// The shape of CAVBOT2-A: the guild channels read returns after the budget
// ran out. The hub list read before it finished, and the slow read carries
// the manager's split of its time, each part in its own place.
func TestSlowGuildChannelsReadIsTheOneTheBudgetRanOutDuring(t *testing.T) {
	const budget, delay = 100 * time.Millisecond, 300 * time.Millisecond
	const waiting, trips, attempts = 200 * time.Millisecond, 100 * time.Millisecond, 2
	w := newTestWorld(t, testHub())
	w.p.pageBudget = budget
	signIn(t, w.forum, w.b)
	reported := recordSentry(t)
	w.discord.setListDelay(delay)
	w.discord.setListTiming(commands.ReadTiming{Waiting: waiting, Trips: trips, Attempts: attempts})

	w.b.get("/")

	tb := onlyTimeBudget(t, reported)
	assertReads(t, tb, guildChannelsSlowReads...)
	assertElapsedCoversBudget(t, tb, budget)
	if len(tb.Reads) != len(guildChannelsSlowReads) {
		return
	}
	slow := tb.Reads[1]
	if slow.TookMS < delay.Milliseconds() {
		t.Errorf("guild channels took_ms = %d, want at least its delay, %d", slow.TookMS, delay.Milliseconds())
	}
	if slow.Discord == nil {
		t.Fatal("guild channels carries no Discord split")
	}
	// The split is the manager's as it reported it, not a time the panel
	// measured, so it is compared exactly.
	if slow.Discord.WaitingMS != waiting.Milliseconds() {
		t.Errorf("guild channels waiting_ms = %d, want the manager's %d", slow.Discord.WaitingMS, waiting.Milliseconds())
	}
	if slow.Discord.TripsMS != trips.Milliseconds() {
		t.Errorf("guild channels trips_ms = %d, want the manager's %d", slow.Discord.TripsMS, trips.Milliseconds())
	}
	if slow.Discord.Attempts != attempts {
		t.Errorf("guild channels attempts = %d, want the manager's %d", slow.Discord.Attempts, attempts)
	}
}

// guildChannelsSlowReads is the report of a page whose guild channels read
// ran past the budget.
var guildChannelsSlowReads = []string{"list hubs=" + wantFinished, "guild channels=" + wantRanOut}

// A store read after both Discord reads fails on the budget's deadline.
// Every read before it finished, a Discord read that took real time among
// them, and the elapsed time counts from the budget's start, not the slow
// read's.
func TestSlowStoreReadAfterTheDiscordReadsIsTheOneTheBudgetRanOutDuring(t *testing.T) {
	const budget, discordDelay = 100 * time.Millisecond, 50 * time.Millisecond
	w, st := newCtxWorld(t, testHub())
	w.p.pageBudget = budget
	signIn(t, w.forum, w.b)
	reported := recordSentry(t)
	w.discord.setListDelay(discordDelay)
	st.blockRead("ListModeratorChanges", nil)

	w.b.get("/")

	tb := onlyTimeBudget(t, reported)
	assertReads(t, tb,
		"list hubs="+wantFinished, "guild channels="+wantFinished, "guild read="+wantFinished,
		"read guild moderator roles="+wantFinished, "list moderator changes="+wantRanOut)
	assertElapsedCoversBudget(t, tb, budget)
}

// A Discord read that fails after the budget ran out, on an error that
// matches the deadline the way Go's HTTP client timeout does, still reports
// the reads: the read that failed is the one the budget ran out during.
func TestDiscordReadFailingAfterTheBudgetRanOutReportsTheReads(t *testing.T) {
	const budget, delay = 100 * time.Millisecond, 300 * time.Millisecond
	w := newTestWorld(t, testHub())
	w.p.pageBudget = budget
	signIn(t, w.forum, w.b)
	reported := recordSentry(t)
	w.discord.setListDelay(delay)
	w.discord.setListErr(fmt.Errorf("Get \"https://discord.com/api/v9/guilds/guild-1/channels\": %w", context.DeadlineExceeded))

	w.b.get("/")

	assertReads(t, onlyTimeBudget(t, reported), guildChannelsSlowReads...)
}
