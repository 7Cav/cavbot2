package voice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/getsentry/sentry-go"
)

// TestMain sets the utils Logger once, before any test runs, so Connect's
// log lines do not dereference a nil *slog.Logger.
func TestMain(m *testing.M) {
	utils.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	os.Exit(m.Run())
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
func (c *fakeConn) Receive(func(Frame))   {}

// errAuthenticationFailed is the fake gateway's answer to a token it does
// not know, as Discord closes a session whose token is bad or revoked.
var errAuthenticationFailed = errors.New("websocket: close 4004: Authentication failed")

// fakeGateway stands in for Discord's gateway. It opens a session for each
// token it knows, as the recorder account that token belongs to, and refuses
// any other.
type fakeGateway struct {
	accounts map[string]string
	// asked is every token the gateway was asked to open, in order.
	asked  []string
	opened []*fakeSession
}

func newFakeGateway(accounts map[string]string) *fakeGateway {
	return &fakeGateway{accounts: accounts}
}

func (g *fakeGateway) open(token string) (GatewaySession, error) {
	g.asked = append(g.asked, token)
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
