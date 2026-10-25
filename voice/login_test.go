package voice

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
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
// three more of the recorders' outage delays, after, so an event sent in
// them is counted too, and returns those that report a gateway outage.
func outageEvents(t *testing.T, events *sentryTransport, n int, after time.Duration) []*sentry.Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(events.Events()) < n && time.Now().Before(deadline) {
		time.Sleep(after / 10)
	}
	time.Sleep(3 * after)
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

	if got := outageEvents(t, events, 1, loginOutageAfter); len(got) != 1 {
		t.Errorf("Sentry got %d outage events, want 1", len(got))
	}
}

// hungLoginOutageAfter is the outage delay of the hung-login test: longer
// than the logout, which discordgo's close holds for a second, so the
// relogin's login starts inside the delay.
const hungLoginOutageAfter = 1500 * time.Millisecond

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
	r := Connect(token, &Discord{offlineAfter: hungLoginOutageAfter})
	defer r.Close()
	// discordgo hands READY to the watch on a goroutine of its own, so the
	// first login's outage ends a moment after Connect returns.
	time.Sleep(hungLoginOutageAfter + hungLoginOutageAfter/2)
	if n := len(events.Events()); n != 0 {
		t.Fatalf("Sentry got %d events before the logout, want none", n)
	}

	r.TakeOffline(gatewaytest.UserID)()

	got := outageEvents(t, events, 1, hungLoginOutageAfter)
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

// values is the value of every key attribute of the first record at level
// with message msg.
func (l *logRecorder) values(level slog.Level, msg, key string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.records {
		if r.Level != level || r.Message != msg {
			continue
		}
		var out []string
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				out = append(out, a.Value.String())
			}
			return true
		})
		return out
	}
	return nil
}

// A recorder offline long enough for Sentry to hear of it says so in the
// log once a login's READY ends the outage, as any gateway session back
// from an outage does, naming the recorder by its place and its user ID. Its relogin's logins fail until Sentry has heard.
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

	if got := outageEvents(t, events, 1, loginOutageAfter); len(got) != 1 {
		t.Fatalf("Sentry got %d outage events, want 1", len(got))
	}
	if n := logs.linesSaying(slog.LevelInfo, utils.GatewayBackOnlineMessage, "recorder", "1"); n != 1 {
		t.Errorf("%d INFO lines say recorder 1 is back online, want 1", n)
	}
	if ids := logs.values(slog.LevelInfo, utils.GatewayBackOnlineMessage, "user_id"); !slices.Equal(ids, []string{gatewaytest.UserID}) {
		t.Errorf("the back-online line names user IDs %q, want %s once", ids, gatewaytest.UserID)
	}
}

// refusalLines counts the records, at any level, that say Discord refused
// a token: the adapter's and the gateway outage watch's.
func (l *logRecorder) refusalLines() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.records {
		if r.Message == loginRefusedMessage || r.Message == utils.TokenRefusedMessage {
			n++
		}
	}
	return n
}

// Discord closes a login whose token it refuses with 4004, whether it
// answers the identify that way or answers it with Op 9 and the identify
// after that with the 4004. Either way the relogin stops and the recorder
// is out, and the bot logs the refusal once. A login closed with any other
// code is a failed login, which the relogin tries again.
func TestARecorderWhoseLoginDiscordRefusesWith4004IsOut(t *testing.T) {
	for _, tc := range []struct {
		name  string
		login gatewaytest.Login
		out   bool
	}{
		{"4004 for the identify", gatewaytest.Login{Close: 4004}, true},
		{"4004 after an Op 9", gatewaytest.Login{Invalid: true, Close: 4004}, true},
		{"4000", gatewaytest.Login{Close: 4000}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			g := gatewaytest.New(t, func(login int) gatewaytest.Login {
				if login == 1 {
					return gatewaytest.Login{Answer: "READY"}
				}
				return tc.login
			})
			// No outage reaches Sentry during the test.
			r := Connect("recorder-token", &Discord{offlineAfter: time.Hour})
			defer r.Close()

			r.TakeOffline(gatewaytest.UserID)()

			if !tc.out {
				waitUntil(t, "the relogin tries again", func() bool { return g.Logins() >= 3 })
				if state := r.State(gatewaytest.UserID); state == RecorderOut {
					t.Errorf("recorder state after a login closed %d = %s, want it still logging in", tc.login.Close, state)
				}
				return
			}
			waitUntil(t, "the recorder is out and the log says why", func() bool {
				return r.State(gatewaytest.UserID) == RecorderOut && logs.refusalLines() > 0
			})
			if n := logs.refusalLines(); n != 1 {
				t.Errorf("%d records say Discord refused the token, want 1", n)
			}
		})
	}
}

// beatEvery is how often the new login in the old-login test heartbeats,
// so its heartbeats are seen to go on within the test.
const beatEvery = 100 * time.Millisecond

// discordgo may be reconnecting a recorder's login when the recorder logs
// out of it, and the logout doesn't stop that. So the old login is logged
// out again with 1000 the moment it comes back, which ends discordgo's
// reconnects and takes the recorder out of its old call. The new login is
// a session of its own, which identifies, and stays up meanwhile.
func TestARecorderLogsOutAnOldLoginThatComesBackAfterItsLogout(t *testing.T) {
	var newLoginUp atomic.Bool
	beats := make(chan struct{}, 1000)
	oldClosed := make(chan int, 1)
	g := gatewaytest.New(t, func(login int) gatewaytest.Login {
		switch {
		case login == 1:
			// The old login, which the gateway drops, so discordgo
			// reconnects it with Resume.
			return gatewaytest.Login{Answer: "READY", Close: 4000}
		case !newLoginUp.Load():
			return gatewaytest.Login{
				// The new login identifies, and stays up.
				Answer: "READY", Heartbeat: beatEvery,
				Beat: func() {
					newLoginUp.Store(true)
					beats <- struct{}{}
				},
				// The old login's resumes fail until the new login is up.
				Resume: &gatewaytest.Login{Close: 4000},
			}
		}
		return gatewaytest.Login{Resume: &gatewaytest.Login{Answer: "RESUMED", Closed: func(code int) { oldClosed <- code }}}
	})
	r := Connect("recorder-token", &Discord{offlineAfter: time.Hour})
	defer r.Close()
	waitUntil(t, "discordgo is reconnecting the old login", func() bool { return g.Logins() >= 2 })

	r.TakeOffline(gatewaytest.UserID)()

	select {
	case code := <-oldClosed:
		if code != 1000 {
			t.Errorf("the old login came back and was closed with %d, want 1000", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the old login came back and was never closed")
	}
	for len(beats) > 0 {
		<-beats
	}
	for n := range 2 {
		select {
		case <-beats:
		case <-time.After(10 * beatEvery):
			t.Fatalf("the new login sent %d heartbeats after the old login's close, want them to go on", n)
		}
	}
}

// Shutdown logs every recorder out on purpose, so Sentry hears of no
// outage, however long the bot takes to exit.
func TestShutdownSendsNoOutage(t *testing.T) {
	events := recordSentry(t)
	closed := make(chan int, 1)
	gatewaytest.New(t, func(int) gatewaytest.Login {
		return gatewaytest.Login{Answer: "READY", Closed: func(code int) { closed <- code }}
	})
	r := Connect("recorder-token", testDiscord())

	r.Close()

	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the recorder never closed its login at shutdown")
	}
	if got := outageEvents(t, events, 0, loginOutageAfter); len(got) != 0 {
		t.Errorf("Sentry got %d outage events after shutdown, want none", len(got))
	}
}
