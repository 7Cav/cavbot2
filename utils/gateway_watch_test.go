package utils

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/getsentry/sentry-go"
)

// gatewayWatchAfter is how long the scripted gateway tests' watches wait
// before they report: longer than a login to the scripted gateway takes,
// and shorter than the second discordgo waits in its close.
const gatewayWatchAfter = 300 * time.Millisecond

// openWatchedSession opens a session on the scripted gateway the test stood
// up, watched as the bot's own. The test's end stands the watch down and
// closes the session.
func openWatchedSession(t *testing.T, after time.Duration) *discordgo.Session {
	t.Helper()
	dg, err := discordgo.New("Bot fake-token-for-test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	w := WatchGateway(dg, after, "session", "main")
	t.Cleanup(func() {
		w.StandDown()
		_ = dg.Close()
	})
	if err := dg.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return dg
}

// waitForOutageEvents waits for Sentry to have at least n events, then
// waits out three more of the gateway tests' delays, so a report sent in
// them is counted too.
func waitForOutageEvents(t *testing.T, tr *mockTransport, n int) []*sentry.Event {
	t.Helper()
	waitForSentry(tr, n)
	time.Sleep(3*gatewayWatchAfter + gatewayWatchAfter/2)
	return tr.Events()
}

// waitForSentry waits for Sentry to have at least n events.
func waitForSentry(tr *mockTransport, n int) []*sentry.Event {
	waitUntil(func() bool { return len(tr.Events()) >= n })
	return tr.Events()
}

// waitUntil waits up to ten seconds for done to hold.
func waitUntil(done func() bool) {
	deadline := time.Now().Add(10 * time.Second)
	for !done() && time.Now().Before(deadline) {
		time.Sleep(gatewayWatchAfter / 10)
	}
}

// linesSaying counts the records, at any level, with message msg and the
// attribute key=value.
func (r *logRecorder) linesSaying(msg, key, value string) int {
	n := 0
	for _, rec := range r.all() {
		if rec.Message == msg && carries(rec, func(a slog.Attr) bool { return a.Key == key && a.Value.String() == value }) {
			n++
		}
	}
	return n
}

// A token Discord stops taking, as after a reset in the Developer Portal:
// Discord closes the session with 4004, Authentication failed, then answers
// every resume with Op 9 and closes again with 4004 after the identify. The
// session logs in no more after the first close, says why in the log, and
// Sentry hears of the outage once.
func TestGatewayWatch_ARefusedTokenStopsTheLoginsAndSendsOneEvent(t *testing.T) {
	logs := recordLogs(t)
	tr := initSentryWithTransport(t)
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		if login == 1 {
			return gatewayLogin{answer: "READY", close: 4004}
		}
		return gatewayLogin{invalid: true, close: 4004}
	})
	openWatchedSession(t, gatewayWatchAfter)

	events := waitForOutageEvents(t, tr, 1)

	if len(events) != 1 {
		t.Fatalf("Sentry got %d events, want 1", len(events))
	}
	if got := fmt.Sprint(events[0].Contexts["extra"]["session"]); got != "main" {
		t.Errorf("event names session %q, want main", got)
	}
	if n := g.loginCount(); n != 1 {
		t.Errorf("the gateway saw %d logins, want 1: none after the 4004", n)
	}
	if n := logs.linesSaying(tokenRefusedMessage, "session", "main"); n != 1 {
		t.Errorf("%d lines say the token was refused for session main, want 1", n)
	}
}

// dropRecorder keeps the time of each Disconnect a session emits.
type dropRecorder struct {
	mu sync.Mutex
	at []time.Time
}

// recordDrops stamps each Disconnect dg emits from now on.
func recordDrops(dg *discordgo.Session) *dropRecorder {
	r := &dropRecorder{}
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.at = append(r.at, time.Now())
	})
	return r
}

func (r *dropRecorder) times() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.at...)
}

// handlerSlack is room for the goroutines discordgo runs its handlers on to
// start late, so a drop's stamp can trail the watch's own by that much.
const handlerSlack = 500 * time.Millisecond

// waitForLogins waits for the gateway to have seen at least n logins.
func waitForLogins(t *testing.T, g *scriptedGateway, n int) {
	t.Helper()
	waitUntil(func() bool { return g.loginCount() >= n })
	if got := g.loginCount(); got < n {
		t.Errorf("the gateway saw %d logins, want at least %d", got, n)
	}
}

// Discord answering every resume with Op 9, and dropping the connection
// after the identify with no 4004, leaves the session offline however often
// it logs in. It keeps logging in, and Sentry hears of the outage once, a
// delay after the first drop. The delay outlasts the second or so between
// drops, so a watch that restarted it on each drop would never report.
func TestGatewayWatch_LoginsThatGetNoReadyStayOneOutage(t *testing.T) {
	tr := initSentryWithTransport(t)
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		if login == 1 {
			return gatewayLogin{answer: "READY", close: 4000}
		}
		return gatewayLogin{invalid: true, close: 4000}
	})
	openWatchedSession(t, 2*time.Second)
	t.Cleanup(func() { g.settle(t, 5*time.Second) })

	waitForSentry(tr, 1)
	waitForLogins(t, g, g.loginCount()+3)

	if n := len(tr.Events()); n != 1 {
		t.Errorf("Sentry got %d events, want 1", n)
	}
}

// A session that drops and resumes within the delay had a blip, and Sentry
// hears nothing of it.
func TestGatewayWatch_AResumeWithinTheDelaySendsNothing(t *testing.T) {
	tr := initSentryWithTransport(t)
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		if login == 1 {
			return gatewayLogin{answer: "READY", close: 4000}
		}
		return gatewayLogin{answer: "RESUMED"}
	})
	openWatchedSession(t, gatewayWatchAfter)

	waitForLogins(t, g, 2)

	if events := waitForOutageEvents(t, tr, 0); len(events) != 0 {
		t.Errorf("Sentry got %d events, want none", len(events))
	}
}

// Discord answering a resume with Op 9 and the identify after it with READY
// is an ordinary reconnect: the session is back, and Sentry hears nothing.
func TestGatewayWatch_AReadyAfterAnInvalidSessionSendsNothing(t *testing.T) {
	tr := initSentryWithTransport(t)
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		if login == 1 {
			return gatewayLogin{answer: "READY", close: 4000}
		}
		return gatewayLogin{invalid: true, answer: "READY"}
	})
	openWatchedSession(t, gatewayWatchAfter)

	waitForLogins(t, g, 2)

	if events := waitForOutageEvents(t, tr, 0); len(events) != 0 {
		t.Errorf("Sentry got %d events, want none", len(events))
	}
}

// A session that can't reach the gateway keeps trying, and Sentry hears of
// the outage once. The RESUMED that ends the outage is logged, so the log
// says when the session came back.
func TestGatewayWatch_ASessionThatCantReachTheGatewayRetriesAndReportsOnce(t *testing.T) {
	logs := recordLogs(t)
	tr := initSentryWithTransport(t)
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		switch login {
		case 1:
			return gatewayLogin{answer: "READY", close: 4000}
		case 2:
			return gatewayLogin{refuse: true}
		}
		return gatewayLogin{answer: "RESUMED"}
	})
	openWatchedSession(t, gatewayWatchAfter)

	waitForLogins(t, g, 3)
	events := waitForOutageEvents(t, tr, 1)

	if len(events) != 1 {
		t.Errorf("Sentry got %d events, want 1", len(events))
	}
	if n := logs.linesSaying("Gateway session back online", "session", "main"); n != 1 {
		t.Errorf("%d lines say session main is back online, want 1", n)
	}
}

// Each outage is its own: a session that came back and then dropped again
// for the whole delay sends a second event.
func TestGatewayWatch_ALaterOutageSendsANewEvent(t *testing.T) {
	tr := initSentryWithTransport(t)
	newScriptedGateway(t, func(login int) gatewayLogin {
		switch login {
		case 1:
			return gatewayLogin{answer: "READY", close: 4000}
		case 2, 4:
			return gatewayLogin{refuse: true}
		case 3:
			return gatewayLogin{answer: "RESUMED", close: 4000}
		}
		return gatewayLogin{answer: "RESUMED"}
	})
	openWatchedSession(t, gatewayWatchAfter)

	if events := waitForOutageEvents(t, tr, 2); len(events) != 2 {
		t.Errorf("Sentry got %d events over two outages, want 2", len(events))
	}
}

// A timer from an outage that has ended reports no later outage early. Each
// outage gets its whole delay from its own first drop.
func TestGatewayWatch_EachOutageGetsItsWholeDelay(t *testing.T) {
	const after = 2 * time.Second
	tr := initSentryWithTransport(t)
	g := newScriptedGateway(t, func(login int) gatewayLogin {
		switch login {
		case 1:
			return gatewayLogin{answer: "READY", close: 4000}
		case 2:
			return gatewayLogin{answer: "RESUMED", close: 4000}
		}
		return gatewayLogin{refuse: true}
	})
	drops := recordDrops(openWatchedSession(t, after))
	t.Cleanup(func() { g.settle(t, 10*time.Second) })

	events := waitForSentry(tr, 1)

	if drops := drops.times(); len(events) != 1 || len(drops) < 2 {
		t.Fatalf("Sentry got %d events after %d drops, want 1 after 2", len(events), len(drops))
	}
	if early := drops.times()[1].Add(after).Sub(events[0].Timestamp); early > handlerSlack {
		t.Errorf("the second outage reached Sentry %v before its delay ran out", early)
	}
}

// Closing a session on purpose emits Disconnect too, and a watch stood down
// first sends nothing for it.
func TestGatewayWatch_AStoodDownWatchSendsNothing(t *testing.T) {
	tr := initSentryWithTransport(t)
	dg, err := discordgo.New("Bot fake-token-for-test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	w := WatchGateway(dg, gatewayWatchAfter, "session", "main")

	w.StandDown()
	_ = dg.Close()

	if events := waitForOutageEvents(t, tr, 0); len(events) != 0 {
		t.Errorf("Sentry got %d events after the watch stood down, want none", len(events))
	}
}
