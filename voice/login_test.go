package voice

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/7cav/cavbot2/utils/gatewaytest"
	"github.com/getsentry/sentry-go"
)

// Tests of a recorder's real discordgo sessions, each login a session of its
// own, against the scripted gateway.

// loginOutageAfter is how long these tests' recorders stay offline before
// Sentry hears of it: longer than a login to the scripted gateway takes.
const loginOutageAfter = 300 * time.Millisecond

// testDiscord is the production Opener with the tests' outage delay.
func testDiscord() *Discord {
	return &Discord{offlineAfter: loginOutageAfter}
}

// outageEvents waits for Sentry to have at least n events, then waits out
// three more outage delays, so an event sent in them is counted too, and
// returns those that report a gateway outage.
func outageEvents(t *testing.T, events *sentryTransport, n int) []*sentry.Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(events.Events()) < n && time.Now().Before(deadline) {
		time.Sleep(loginOutageAfter / 10)
	}
	time.Sleep(3 * loginOutageAfter)
	var out []*sentry.Event
	for _, e := range events.Events() {
		if e.Tags["message"] == utils.GatewayOfflineMessage {
			out = append(out, e)
		}
	}
	return out
}

// A recorder is offline from its first login on, so one whose first login
// fails is in a gateway outage from that try. Sentry hears of it once the
// delay runs out, with no login since.
func TestARecorderWhoseFirstLoginFailsIsOfflineFromThatTry(t *testing.T) {
	events := recordSentry(t)
	gatewaytest.New(t, func(int) gatewaytest.Login { return gatewaytest.Login{Refuse: true} })

	if _, err := testDiscord().Open(1, "recorder-token"); err == nil {
		t.Fatal("Open of a login the gateway refused = nil error, want one")
	}

	if got := outageEvents(t, events, 1); len(got) != 1 {
		t.Errorf("Sentry got %d outage events, want 1", len(got))
	}
}

// A cut-short recorder is offline from its logout until a login's READY,
// so a relogin whose login hangs, and never fails, still reaches Sentry
// once the delay runs out. The one event names the recorder by its place in
// RECORDER_TOKENS, as "Recorder failed to connect" does, and by its user
// ID. The token is the recorder account, so the event carries it nowhere.
func TestARecorderOfflineFromItsLogoutReachesSentryWhileItsLoginHangs(t *testing.T) {
	events := recordSentry(t)
	gatewaytest.New(t, func(login int) gatewaytest.Login {
		if login == 1 {
			return gatewaytest.Login{Answer: "READY"}
		}
		// Hello, and no answer to the identify.
		return gatewaytest.Login{}
	})
	const token = "recorder-token-7f3a"
	r := Connect(token, testDiscord())
	defer r.Close()

	r.TakeOffline(gatewaytest.UserID)()

	got := outageEvents(t, events, 1)
	if len(got) != 1 {
		t.Fatalf("Sentry got %d outage events, want 1", len(got))
	}
	extra := got[0].Contexts["extra"]
	if place, id := fmt.Sprint(extra["recorder"]), fmt.Sprint(extra["user_id"]); place != "1" || id != gatewaytest.UserID {
		t.Errorf("event names recorder %s with user ID %s, want recorder 1 with user ID %s", place, id, gatewaytest.UserID)
	}
	for _, text := range eventTexts(got[0]) {
		if strings.Contains(text, token) {
			t.Errorf("event carries the recorder's token in %q", text)
		}
	}
}

// waitUntil waits up to ten seconds for done to hold, and fails the test
// unless it does.
func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting until %s", what)
		}
		time.Sleep(loginOutageAfter / 10)
	}
}

// linesSaying counts the records at level with message msg that carry the
// attribute key=value.
func (l *logRecorder) linesSaying(level slog.Level, msg, key, value string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.records {
		if r.Level != level || r.Message != msg {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && a.Value.String() == value {
				n++
				return false
			}
			return true
		})
	}
	return n
}

// A recorder offline long enough for Sentry to hear of it says so in the
// log once a login's READY ends the outage, as any gateway session back
// from an outage does. Its relogin's logins fail until Sentry has heard.
func TestARecorderBackFromAnOutageSaysItIsBackOnline(t *testing.T) {
	logs := captureLogs(t)
	events := recordSentry(t)
	gatewaytest.New(t, func(login int) gatewaytest.Login {
		if login == 1 || len(events.Events()) > 0 {
			return gatewaytest.Login{Answer: "READY"}
		}
		return gatewaytest.Login{Refuse: true}
	})
	r := Connect("recorder-token", testDiscord())
	defer r.Close()

	r.TakeOffline(gatewaytest.UserID)()
	waitUntil(t, "the recorder is back online", func() bool { return r.State(gatewaytest.UserID) == RecorderOnline })

	if got := outageEvents(t, events, 1); len(got) != 1 {
		t.Fatalf("Sentry got %d outage events, want 1", len(got))
	}
	if n := logs.linesSaying(slog.LevelInfo, utils.GatewayBackOnlineMessage, "recorder", "1"); n != 1 {
		t.Errorf("%d INFO lines say recorder 1 is back online, want 1", n)
	}
}
