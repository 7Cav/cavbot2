package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/getsentry/sentry-go"
	"github.com/go-logfmt/logfmt"
)

// TestMain sets the utils Logger once, before any test runs, so Connect's
// log lines do not dereference a nil *slog.Logger. Written once, before any
// test goroutine: a later write would race the goroutines that read it.
func TestMain(m *testing.M) {
	utils.Logger = slog.New(testLogs)
	os.Exit(m.Run())
}

// testLogs keeps every record at Info and above the package logs in its
// tests.
var testLogs = &logRecorder{}

// logRecorder is a slog handler that keeps the records it handles. The
// voice package binds no attributes or groups to its logger, and no test
// reads the name disgo binds to its own, so the handler keeps none.
type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (l *logRecorder) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}
func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logRecorder) WithGroup(string) slog.Handler      { return l }

func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r.Clone())
	return nil
}

// captureLogs forgets the records logged so far and returns the log the
// test writes from then on.
func captureLogs(t *testing.T) *logRecorder {
	t.Helper()
	testLogs.mu.Lock()
	defer testLogs.mu.Unlock()
	testLogs.records = nil
	return testLogs
}

// logfmtWriter is a writer that hands l each line of slog's text format
// written to it. Each line becomes a record of its level, message and other pairs,
// dated when it is written, which is when it was logged.
type logfmtWriter struct{ l *logRecorder }

func (w logfmtWriter) Write(p []byte) (int, error) {
	dec := logfmt.NewDecoder(bytes.NewReader(p))
	for dec.ScanRecord() {
		var level slog.Level
		var msg string
		var attrs []slog.Attr
		for dec.ScanKeyval() {
			switch key := string(dec.Key()); key {
			case "time":
			case "level":
				if err := level.UnmarshalText(dec.Value()); err != nil {
					return 0, err
				}
			case "msg":
				msg = string(dec.Value())
			default:
				attrs = append(attrs, slog.String(key, string(dec.Value())))
			}
		}
		r := slog.NewRecord(time.Now(), level, msg, 0)
		r.AddAttrs(attrs...)
		_ = w.l.Handle(context.Background(), r)
	}
	return len(p), dec.Err()
}

// errorsFrom is the records at ERROR or above logged from start until now.
// Records carry the clock of the bubble that logged them, so a test under
// synctest reads its own bubble's.
func (l *logRecorder) errorsFrom(start time.Time) []slog.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	var out []slog.Record
	for _, r := range l.records {
		if r.Level >= slog.LevelError && !r.Time.Before(start) && !r.Time.After(now) {
			out = append(out, r)
		}
	}
	return out
}

// messagesAt is the message of each record at level.
func (l *logRecorder) messagesAt(level slog.Level) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, r := range l.records {
		if r.Level == level {
			out = append(out, r.Message)
		}
	}
	return out
}

// messages is the message of each record.
func messages(records []slog.Record) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Message)
	}
	return out
}

// duration is the time.Duration the first record at level with message
// msg carries, and whether there is one.
func (l *logRecorder) duration(level slog.Level, msg string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.records {
		if r.Level != level || r.Message != msg {
			continue
		}
		var d time.Duration
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Value.Kind() == slog.KindDuration {
				d, found = a.Value.Duration(), true
			}
			return !found
		})
		if found {
			return d, true
		}
	}
	return 0, false
}

// fakeSession is one recorder's login as the fake gateway opens it. conns is
// every voice connection it joined, in order. The gateway's mutex guards
// both.
type fakeSession struct {
	gw     *fakeGateway
	userID string
	closed bool
	conns  []*fakeConn
}

func (s *fakeSession) UserID() string { return s.userID }

// LogOut is the session's logout for a relogin, and Close its logout at
// shutdown. Each returns once the gateway's logoutTakes has passed.
func (s *fakeSession) LogOut() error { return s.Close() }

func (s *fakeSession) Close() error {
	s.gw.mu.Lock()
	takes := s.gw.logoutTakes
	s.gw.mu.Unlock()
	time.Sleep(takes)
	s.gw.mu.Lock()
	defer s.gw.mu.Unlock()
	s.closed = true
	s.gw.history = append(s.gw.history, gatewayLogout)
	return nil
}

func (s *fakeSession) Join(_ context.Context, guildID, channelID string) (Conn, error) {
	s.gw.mu.Lock()
	defer s.gw.mu.Unlock()
	c := &fakeConn{gw: s.gw, guildID: guildID, channelID: channelID}
	s.conns = append(s.conns, c)
	return c, nil
}

// fakeConn is one voice connection a fake session joined, until it leaves.
// The gateway's mutex guards left.
type fakeConn struct {
	gw                 *fakeGateway
	guildID, channelID string
	left               bool
}

// Leave returns once the gateway's leaveTakes has passed.
func (c *fakeConn) Leave(context.Context) {
	c.gw.mu.Lock()
	takes := c.gw.leaveTakes
	c.gw.mu.Unlock()
	time.Sleep(takes)
	c.gw.mu.Lock()
	defer c.gw.mu.Unlock()
	c.left = true
}

func (c *fakeConn) Receive(Listener)     {}
func (c *fakeConn) OwnLeaves() int       { return 0 }
func (c *fakeConn) GapReport() GapReport { return GapReport{} }

// errAuthenticationFailed is the fake gateway's answer to a token it does
// not know, as Discord closes a session whose token is bad or revoked.
var errAuthenticationFailed = errors.New("websocket: close 4004: Authentication failed")

// errUnauthorized is the fake REST API's answer to a token it does not
// know.
var errUnauthorized = errors.New(`HTTP 401 Unauthorized, {"message": "401: Unauthorized", "code": 0}`)

// errGatewayDown is the fake gateway's answer to a login while it is down
// for that token.
var errGatewayDown = errors.New("websocket: bad handshake")

// fakeGateway stands in for Discord as a recorder reaches it: its gateway,
// which logs a recorder in as the account its token belongs to, and its
// REST API, which names that account. Both refuse a token they don't know.
// A token in down fails to log in for now, while REST still names its
// account, as in a gateway outage.
type fakeGateway struct {
	mu       sync.Mutex
	accounts map[string]string
	down     map[string]bool
	// refused is the tokens whose logins Discord refuses from now on, as
	// for a token reset after startup.
	refused map[string]bool
	// tries is every login try, in order, and places the place in the
	// list each token was opened as. opened is every session the gateway
	// opened, in order.
	tries  []loginTry
	places map[string]int
	opened []*fakeSession
	// history is every login try and every logout's return, in order.
	history []string
	// logoutTakes is how long each logout takes to return, and leaveTakes
	// each leave of a voice channel, none until a test sets them.
	logoutTakes, leaveTakes time.Duration
}

// The entries of the gateway's history.
const (
	gatewayLogin  = "login"
	gatewayLogout = "logout"
)

// loginTry is one login the gateway was asked for.
type loginTry struct {
	token string
	at    time.Time
}

func newFakeGateway(accounts map[string]string) *fakeGateway {
	return &fakeGateway{accounts: accounts, down: map[string]bool{}, refused: map[string]bool{}, places: map[string]int{}}
}

func (g *fakeGateway) Open(recorder int, token string) (GatewaySession, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tries = append(g.tries, loginTry{token: token, at: time.Now()})
	g.history = append(g.history, gatewayLogin)
	g.places[token] = recorder
	userID, ok := g.accounts[token]
	switch {
	case !ok:
		return nil, errAuthenticationFailed
	case g.refused[token]:
		return nil, fmt.Errorf("websocket: close 4004: %w", ErrTokenRefused)
	case g.down[token]:
		return nil, errGatewayDown
	}
	s := &fakeSession{gw: g, userID: userID}
	g.opened = append(g.opened, s)
	return s, nil
}

func (g *fakeGateway) FailedToConnect(int) {}

func (g *fakeGateway) UserID(_ context.Context, token string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	userID, ok := g.accounts[token]
	if !ok {
		return "", errUnauthorized
	}
	return userID, nil
}

// setDown has every login of token fail from now on while down is true,
// and succeed again once it is false.
func (g *fakeGateway) setDown(token string, down bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.down[token] = down
}

// setLogoutTakes makes every logout from now on take d to return.
func (g *fakeGateway) setLogoutTakes(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logoutTakes = d
}

// setLeaveTakes makes every leave of a voice channel from now on take d to
// return.
func (g *fakeGateway) setLeaveTakes(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.leaveTakes = d
}

// joinsOf is the channel each voice connection of the nth session the
// gateway opened, counted from 1, joined, as "guild/channel", in order.
func (g *fakeGateway) joinsOf(n int) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n > len(g.opened) {
		return nil
	}
	var out []string
	for _, c := range g.opened[n-1].conns {
		out = append(out, c.guildID+"/"+c.channelID)
	}
	return out
}

// loggedOutBeforeLogin reports whether a logout returned before the nth
// login try, counted from 1, and whether there was an nth try.
func (g *fakeGateway) loggedOutBeforeLogin(n int) (before, tried bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	logins := 0
	for _, entry := range g.history {
		switch entry {
		case gatewayLogout:
			before = true
		case gatewayLogin:
			if logins++; logins == n {
				return before, true
			}
		}
	}
	return false, false
}

// refuse has Discord refuse every login of token from now on.
func (g *fakeGateway) refuse(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refused[token] = true
}

// triesOf is when each login of token was tried, in order.
func (g *fakeGateway) triesOf(token string) []time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []time.Time
	for _, try := range g.tries {
		if try.token == token {
			out = append(out, try.at)
		}
	}
	return out
}

// asked is every token the gateway was asked to log in, in order.
func (g *fakeGateway) asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.tries))
	for _, try := range g.tries {
		out = append(out, try.token)
	}
	return out
}

// sentryTransport records the events a Sentry client sends.
type sentryTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *sentryTransport) Configure(sentry.ClientOptions)        {}
func (t *sentryTransport) Flush(time.Duration) bool              { return true }
func (t *sentryTransport) FlushWithContext(context.Context) bool { return true }
func (t *sentryTransport) Close()                                {}
func (t *sentryTransport) SendEvent(e *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, e)
}

func (t *sentryTransport) Events() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event(nil), t.events...)
}

// recordSentry binds a Sentry client that records its events, and unbinds
// it after the test.
func recordSentry(t *testing.T) *sentryTransport {
	t.Helper()
	rec := &sentryTransport{}
	if err := sentry.Init(sentry.ClientOptions{Dsn: "https://test@example.com/1", Transport: rec}); err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
	return rec
}

// assertRecorders fails unless the connected recorders are exactly want, in
// any order.
func assertRecorders(t *testing.T, r *Recorders, want ...string) {
	t.Helper()
	got := slices.Sorted(slices.Values(r.UserIDs()))
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("recorders = %v, want %v", got, want)
	}
}

// Each configured token connects one recorder, as the account it belongs
// to, and the recorders' user IDs are what temp VC gets to ignore.
func TestEachTokenConnectsOneRecorder(t *testing.T) {
	gw := newFakeGateway(map[string]string{"tok-a": "id-a", "tok-b": "id-b"})

	r := Connect("tok-a,tok-b", gw)

	assertRecorders(t, r, "id-a", "id-b")
}

// failedToConnect counts the events Sentry got for a recorder that failed
// to connect.
func failedToConnect(events *sentryTransport) int {
	n := 0
	for _, e := range events.Events() {
		if e.Tags["message"] == recorderFailedToConnectMessage {
			n++
		}
	}
	return n
}

// A recorder whose first login and user ID read both fail, as a bad or
// revoked token's do, is out until a restart: Sentry hears of it while
// Connect runs, it has no user ID to give, and it never tries to log in
// again. The recorders on either side of it in the list still connect.
func TestARecorderWhoseLoginAndUserIDReadFailIsOut(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		gw := newFakeGateway(map[string]string{"tok-a": "id-a", "tok-c": "id-c"})

		r := Connect("tok-a,tok-bad,tok-c", gw)
		defer r.Close()

		if got := failedToConnect(events); got != 1 {
			t.Errorf("Sentry events for a recorder that failed to connect = %d, want 1 as Connect returns", got)
		}
		assertRecorders(t, r, "id-a", "id-c")
		time.Sleep(loginRetryMax)
		synctest.Wait()
		if tries := gw.triesOf("tok-bad"); len(tries) != 1 {
			t.Errorf("logins of the bad token = %d, want only the first", len(tries))
		}
	})
}

// A recorder whose first login fails while Discord's REST API still names
// its account, as in a gateway outage, doesn't hold up startup: Connect
// returns with the recorder offline, under the user ID the REST API gave,
// so temp VC ignores it from the start. It goes on logging in in the
// background, and is online once a login goes through.
func TestARecorderWhoseFirstLoginFailsLogsInInTheBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gw := newFakeGateway(map[string]string{"tok-a": "id-a"})
		gw.setDown("tok-a", true)

		r := Connect("tok-a", gw)
		defer r.Close()

		assertRecorders(t, r, "id-a")
		if got := r.State("id-a"); got != RecorderOffline {
			t.Errorf("recorder as Connect returns = %v, want %v", got, RecorderOffline)
		}
		gw.setDown("tok-a", false)
		time.Sleep(loginRetryMax)
		synctest.Wait()
		if got := r.State("id-a"); got != RecorderOnline {
			t.Errorf("recorder once a login goes through = %v, want %v", got, RecorderOnline)
		}
	})
}

// With no token configured, no recorder connects and nothing goes to the
// gateway, so a host without recorders starts as it did before them.
func TestNoTokenConnectsNoRecorder(t *testing.T) {
	gw := newFakeGateway(map[string]string{"tok-a": "id-a"})

	r := Connect("", gw)

	if len(gw.asked()) != 0 {
		t.Errorf("gateway asked to open %q, want nothing with no token", gw.asked())
	}
	assertRecorders(t, r)
}

// Shutdown closes every recorder's session, so each goes offline.
func TestCloseClosesEveryRecorderSession(t *testing.T) {
	gw := newFakeGateway(map[string]string{"tok-a": "id-a", "tok-b": "id-b"})
	r := Connect("tok-a,tok-b", gw)

	r.Close()

	if len(gw.opened) != 2 {
		t.Fatalf("opened %d sessions, want 2", len(gw.opened))
	}
	for _, s := range gw.opened {
		if !s.closed {
			t.Errorf("recorder %s still connected after Close", s.userID)
		}
	}
}

// A join goes through the session of the recorder it names, into the guild
// and channel given, and leaving takes that recorder back out.
func TestJoinGoesThroughTheNamedRecorder(t *testing.T) {
	gw := newFakeGateway(map[string]string{"tok-a": "id-a", "tok-b": "id-b"})
	r := Connect("tok-a,tok-b", gw)

	conn, err := r.Join(context.Background(), "id-b", "guild-1", "vc-1")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}

	a, b := gw.opened[0], gw.opened[1]
	if len(a.conns) != 0 {
		t.Errorf("recorder id-a joined %d channels, want none", len(a.conns))
	}
	if len(b.conns) != 1 || b.conns[0].guildID != "guild-1" || b.conns[0].channelID != "vc-1" {
		t.Fatalf("recorder id-b joined %+v, want guild-1's vc-1", b.conns)
	}
	conn.Leave(context.Background())
	if !b.conns[0].left {
		t.Error("recorder id-b still in vc-1 after Leave")
	}
}

// Each recorder opens as its token's place in RECORDER_TOKENS, from 1, the
// number its reports name it by. A token that fails keeps its place.
func TestEachRecorderOpensAsItsPlaceInTheList(t *testing.T) {
	gw := newFakeGateway(map[string]string{"tok-a": "id-a", "tok-c": "id-c"})

	Connect("tok-a,tok-bad,tok-c", gw)

	want := map[string]int{"tok-a": 1, "tok-bad": 2, "tok-c": 3}
	if !maps.Equal(gw.places, want) {
		t.Errorf("recorders opened at places %v, want %v", gw.places, want)
	}
}

// warnings counts the WARN records with message msg.
func (l *logRecorder) warnings(msg string) int {
	return countOf(l.messagesAt(slog.LevelWarn), msg)
}

// countOf is how many of msgs are msg.
func countOf(msgs []string, msg string) int {
	n := 0
	for _, m := range msgs {
		if m == msg {
			n++
		}
	}
	return n
}

// A recorder taken offline after a cut short logs back in only once its
// old login's logout has returned, here 2 s on, inside its bound. So one
// login's logout never lands while the next one joins a call.
func TestARecorderLogsBackInOnlyAfterItsLogout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gw := newFakeGateway(map[string]string{"tok-a": "id-a"})
		r := Connect("tok-a", gw)
		defer r.Close()
		gw.setLogoutTakes(2 * time.Second)

		r.TakeOffline("id-a")()
		time.Sleep(logoutTimeout)
		synctest.Wait()

		before, tried := gw.loggedOutBeforeLogin(2)
		if !tried {
			t.Fatal("the recorder never tried to log back in")
		}
		if !before {
			t.Error("the recorder tried to log back in before its logout returned")
		}
	})
}

// A logout that hangs holds up the next login no longer than its bound:
// the login goes out at the bound, and one WARN names the logout.
func TestAHungLogoutLetsTheNextLoginStartAtItsBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureLogs(t)
		gw := newFakeGateway(map[string]string{"tok-a": "id-a"})
		r := Connect("tok-a", gw)
		// The bubble ends only once the hung logouts have returned, after
		// Close has given up on its own.
		defer time.Sleep(time.Hour)
		defer r.Close()
		gw.setLogoutTakes(time.Hour)

		took := time.Now()
		r.TakeOffline("id-a")()
		time.Sleep(logoutTimeout)
		synctest.Wait()

		tries := gw.triesOf("tok-a")
		if len(tries) != 2 || tries[1].Sub(took) != logoutTimeout {
			t.Errorf("logins after the recorder was taken offline at %v = %v, want one at %v", took, tries[1:], took.Add(logoutTimeout))
		}
		if got := logs.warnings(logoutTimedOutMessage); got != 1 {
			t.Errorf("WARN records %q = %d, want 1", logoutTimedOutMessage, got)
		}
	})
}

// Shutdown stops a recorder that is logging in: once Close has returned,
// no login goes out however long the clock runs. That holds for one whose
// first login failed at startup, and for one whose relogin after a cut
// short comes only after Close.
func TestCloseStopsARecorderLoggingIn(t *testing.T) {
	for _, tc := range []struct {
		name            string
		firstLoginFails bool
		// close has the recorder logging in, or about to, and closes r.
		close func(r *Recorders)
	}{
		{"after a failed first login", true, func(r *Recorders) { r.Close() }},
		{"relogin after Close", false, func(r *Recorders) {
			relogin := r.TakeOffline("id-a")
			r.Close()
			relogin()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gw := newFakeGateway(map[string]string{"tok-a": "id-a"})
				gw.setDown("tok-a", tc.firstLoginFails)
				r := Connect("tok-a", gw)

				tc.close(r)
				tried := len(gw.triesOf("tok-a"))
				time.Sleep(loginRetryMax)
				synctest.Wait()

				if got := len(gw.triesOf("tok-a")); got != tried {
					t.Errorf("logins after Close = %d, want none", got-tried)
				}
			})
		})
	}
}

// failingRelogin takes the recorder offline and logs it back in while
// Discord's gateway is down for it, so every login of the relogin fails. It
// runs the clock long enough for the waits between logins to reach their
// cap and stay there twice, since the waits before it add up to less than
// two caps, and returns the waits between the relogin's logins.
func failingRelogin(t *testing.T, logs *logRecorder) (waits []time.Duration) {
	t.Helper()
	gw := newFakeGateway(map[string]string{"tok-a": "id-a"})
	r := Connect("tok-a", gw)
	defer r.Close()
	gw.setDown("tok-a", true)

	r.TakeOffline("id-a")()
	time.Sleep(4 * loginRetryMax)
	synctest.Wait()

	tries := gw.triesOf("tok-a")[1:]
	if got := logs.warnings(loginFailedMessage); got != len(tries) {
		t.Errorf("WARN records %q = %d, want one for each of the %d failed logins", loginFailedMessage, got, len(tries))
	}
	for i := 1; i < len(tries); i++ {
		waits = append(waits, tries[i].Sub(tries[i-1]))
	}
	if len(waits) < 2 {
		t.Fatalf("waits between failed logins = %v, want at least two", waits)
	}
	return waits
}

// A relogin whose first login fails tries again after the first retry's
// wait, and each failed login logs a WARN.
func TestAFailedLoginRetriesAfterTheFirstWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureLogs(t)
		if waits := failingRelogin(t, logs); waits[0] != loginRetryFirst {
			t.Errorf("wait before the second login = %v, want %v", waits[0], loginRetryFirst)
		}
	})
}

// Each wait between failed logins doubles the one before it.
func TestTheWaitBetweenFailedLoginsDoubles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		waits := failingRelogin(t, captureLogs(t))
		if waits[1] != 2*waits[0] {
			t.Errorf("wait before the third login = %v, want twice the wait before the second, %v", waits[1], 2*waits[0])
		}
	})
}

// The wait between failed logins stops growing at its cap, so a recorder
// whose logins fail for hours still tries as often as the cap lets it.
func TestTheWaitBetweenFailedLoginsStopsAtItsCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		waits := failingRelogin(t, captureLogs(t))
		i := slices.Index(waits, loginRetryMax)
		if i < 0 || i == len(waits)-1 {
			t.Fatalf("waits between failed logins = %v, want one of %v and one after it", waits, loginRetryMax)
		}
		if waits[i+1] != loginRetryMax {
			t.Errorf("wait after one of %v = %v, want it no longer", loginRetryMax, waits[i+1])
		}
	})
}

// A login whose token Discord refuses stops the recorder logging in: it is
// out until a restart, no login goes out however long the clock runs, and
// the refusal logs once.
func TestARefusedTokenTakesTheRecorderOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureLogs(t)
		gw := newFakeGateway(map[string]string{"tok-a": "id-a"})
		r := Connect("tok-a", gw)
		defer r.Close()
		gw.refuse("tok-a")

		r.TakeOffline("id-a")()
		time.Sleep(loginRetryMax)
		synctest.Wait()

		if tries := gw.triesOf("tok-a"); len(tries) != 2 {
			t.Errorf("logins = %d, want the startup's and the one refused", len(tries))
		}
		if got := r.State("id-a"); got != RecorderOut {
			t.Errorf("recorder after its token was refused = %v, want %v", got, RecorderOut)
		}
		if got := logs.warnings(loginRefusedMessage); got != 1 {
			t.Errorf("WARN records %q = %d, want 1", loginRefusedMessage, got)
		}
	})
}

// joinResult is what a join returned.
type joinResult struct {
	conn Conn
	err  error
}

// A join of a recorder taken offline after a cut short waits for its next
// login, then joins through it, into the guild and channel given. The
// recorder's leave of its old channel still hangs, and holds nothing up.
func TestAJoinOfAnOfflineRecorderWaitsForItsLogin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gw := newFakeGateway(map[string]string{"tok-a": "id-a"})
		r := Connect("tok-a", gw)
		defer r.Close()
		old, err := r.Join(context.Background(), "id-a", "guild-1", "vc-1")
		if err != nil {
			t.Fatalf("first Join: %v", err)
		}
		gw.setLeaveTakes(time.Hour)
		left := make(chan struct{})
		go func() {
			defer close(left)
			old.Leave(context.Background())
		}()
		// The bubble ends only once the hung leave has returned.
		defer func() { <-left }()
		relogin := r.TakeOffline("id-a")

		joined := make(chan joinResult, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			conn, err := r.Join(ctx, "id-a", "guild-1", "vc-2")
			joined <- joinResult{conn, err}
		}()
		synctest.Wait()
		select {
		case res := <-joined:
			t.Fatalf("join of the offline recorder returned %+v before its login", res)
		default:
		}

		relogin()
		synctest.Wait()

		select {
		case res := <-joined:
			if res.err != nil {
				t.Fatalf("join once the recorder logged back in: %v", res.err)
			}
		default:
			t.Fatal("join still waiting once the recorder logged back in")
		}
		if got := gw.joinsOf(2); !slices.Equal(got, []string{"guild-1/vc-2"}) {
			t.Errorf("joins through the new login = %v, want [guild-1/vc-2]", got)
		}
	})
}

// A join of an offline recorder whose context ends before the recorder logs
// back in says so with ErrRecorderOffline, which a caller can tell from a
// join that failed.
func TestAJoinOfAnOfflineRecorderThatRunsOutOfTimeSaysSo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gw := newFakeGateway(map[string]string{"tok-a": "id-a"})
		gw.setDown("tok-a", true)
		r := Connect("tok-a", gw)
		defer r.Close()

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := r.Join(ctx, "id-a", "guild-1", "vc-1")

		if !errors.Is(err, ErrRecorderOffline) {
			t.Errorf("join of a recorder still offline at its deadline = %v, want %v", err, ErrRecorderOffline)
		}
	})
}
