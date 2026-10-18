package voice

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"testing"
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

// fakeSession is one recorder's gateway session as the fake gateway opens it.
// conns is every voice connection it joined, in order.
type fakeSession struct {
	userID string
	closed bool
	conns  []*fakeConn
}

func (s *fakeSession) UserID() string { return s.userID }
func (s *fakeSession) Close() error   { s.closed = true; return nil }

func (s *fakeSession) Join(_ context.Context, guildID, channelID string) (Conn, error) {
	c := &fakeConn{guildID: guildID, channelID: channelID}
	s.conns = append(s.conns, c)
	return c, nil
}

// fakeConn is one voice connection a fake session joined, until it leaves.
type fakeConn struct {
	guildID, channelID string
	left               bool
}

func (c *fakeConn) Leave(context.Context) { c.left = true }
func (c *fakeConn) Receive(Listener)      {}
func (c *fakeConn) Rejoining() bool       { return false }

// errAuthenticationFailed is the fake gateway's answer to a token it does
// not know, as Discord closes a session whose token is bad or revoked.
var errAuthenticationFailed = errors.New("websocket: close 4004: Authentication failed")

// fakeGateway stands in for Discord's gateway. It opens a session for each
// token it knows, as the recorder account that token belongs to, and refuses
// any other.
type fakeGateway struct {
	accounts map[string]string
	// asked is every token the gateway was asked to open, in order, and
	// places the place in the list each was opened as.
	asked  []string
	places map[string]int
	opened []*fakeSession
}

func newFakeGateway(accounts map[string]string) *fakeGateway {
	return &fakeGateway{accounts: accounts, places: map[string]int{}}
}

func (g *fakeGateway) open(recorder int, token string) (GatewaySession, error) {
	g.asked = append(g.asked, token)
	g.places[token] = recorder
	userID, ok := g.accounts[token]
	if !ok {
		return nil, errAuthenticationFailed
	}
	s := &fakeSession{userID: userID}
	g.opened = append(g.opened, s)
	return s, nil
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

	r := Connect("tok-a,tok-b", gw.open)

	assertRecorders(t, r, "id-a", "id-b")
}

// A bad or revoked token is reported to Sentry, and the recorders on either
// side of it in the list still connect.
func TestABadTokenIsReportedAndTheOtherRecordersConnect(t *testing.T) {
	events := recordSentry(t)
	gw := newFakeGateway(map[string]string{"tok-a": "id-a", "tok-c": "id-c"})

	r := Connect("tok-a,tok-bad,tok-c", gw.open)

	if len(events.Events()) == 0 {
		t.Error("no Sentry event for the bad token")
	}
	assertRecorders(t, r, "id-a", "id-c")
}

// With no token configured, no recorder connects and nothing goes to the
// gateway, so a host without recorders starts as it did before them.
func TestNoTokenConnectsNoRecorder(t *testing.T) {
	gw := newFakeGateway(map[string]string{"tok-a": "id-a"})

	r := Connect("", gw.open)

	if len(gw.asked) != 0 {
		t.Errorf("gateway asked to open %q, want nothing with no token", gw.asked)
	}
	assertRecorders(t, r)
}

// Shutdown closes every recorder's session, so each goes offline.
func TestCloseClosesEveryRecorderSession(t *testing.T) {
	gw := newFakeGateway(map[string]string{"tok-a": "id-a", "tok-b": "id-b"})
	r := Connect("tok-a,tok-b", gw.open)

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
	r := Connect("tok-a,tok-b", gw.open)

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

	Connect("tok-a,tok-bad,tok-c", gw.open)

	want := map[string]int{"tok-a": 1, "tok-bad": 2, "tok-c": 3}
	if !maps.Equal(gw.places, want) {
		t.Errorf("recorders opened at places %v, want %v", gw.places, want)
	}
}
