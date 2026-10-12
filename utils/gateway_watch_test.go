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

// watchAfter is how long the tests' watches wait before they report, short
// enough to wait out.
const watchAfter = 50 * time.Millisecond

// fakeGatewayEvents stands in for a discordgo session's handler registry. It
// keeps the handlers added to it, and hands each event to every handler a
// real session would call for it: one taking that event's type, or one
// taking every event.
type fakeGatewayEvents struct {
	mu       sync.Mutex
	handlers []*any
}

func (g *fakeGatewayEvents) AddHandler(handler interface{}) func() {
	g.mu.Lock()
	defer g.mu.Unlock()
	h := &handler
	g.handlers = append(g.handlers, h)
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		for i, kept := range g.handlers {
			if kept == h {
				g.handlers = append(g.handlers[:i], g.handlers[i+1:]...)
				return
			}
		}
	}
}

// deliver hands e to the handlers before it returns. A session emits its
// Connect and Disconnect events a network round trip or more apart, so they
// reach its handlers in the order they happen, as they do here.
func (g *fakeGatewayEvents) deliver(e any) {
	g.mu.Lock()
	handlers := append([]*any(nil), g.handlers...)
	g.mu.Unlock()
	for _, h := range handlers {
		switch h := (*h).(type) {
		case func(*discordgo.Session, *discordgo.Connect):
			if e, ok := e.(*discordgo.Connect); ok {
				h(nil, e)
			}
		case func(*discordgo.Session, *discordgo.Disconnect):
			if e, ok := e.(*discordgo.Disconnect); ok {
				h(nil, e)
			}
		case func(*discordgo.Session, interface{}):
			h(nil, e)
		}
	}
}

// waitForEvents waits for Sentry to have at least n events, then waits out
// several more delays, so a report sent after them is counted too.
func waitForEvents(t *testing.T, tr *mockTransport, n int) []*sentry.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(tr.Events()) < n && time.Now().Before(deadline) {
		time.Sleep(watchAfter / 5)
	}
	time.Sleep(4 * watchAfter)
	return tr.Events()
}

// A session disconnected for the whole delay is offline, and Sentry hears of
// it once, with the pairs that name the session.
func TestGatewayWatch_ADisconnectNotFollowedByAConnectSendsOneEventNamingTheSession(t *testing.T) {
	tr := initSentryWithTransport(t)
	gw := &fakeGatewayEvents{}
	WatchGateway(gw, watchAfter, "session", "main")
	gw.deliver(&discordgo.Connect{})

	gw.deliver(&discordgo.Disconnect{})

	events := waitForEvents(t, tr, 1)
	if len(events) != 1 {
		t.Fatalf("Sentry got %d events, want 1", len(events))
	}
	if got := fmt.Sprint(events[0].Contexts["extra"]["session"]); got != "main" {
		t.Errorf("event names session %q, want main", got)
	}
}

// A session that reconnects within the delay had a blip, and Sentry hears
// nothing of it.
func TestGatewayWatch_AConnectWithinTheDelaySendsNothing(t *testing.T) {
	tr := initSentryWithTransport(t)
	gw := &fakeGatewayEvents{}
	WatchGateway(gw, watchAfter, "session", "main")
	gw.deliver(&discordgo.Connect{})

	gw.deliver(&discordgo.Disconnect{})
	gw.deliver(&discordgo.Connect{})

	if events := waitForEvents(t, tr, 0); len(events) != 0 {
		t.Errorf("Sentry got %d events, want none", len(events))
	}
}

// infoLinesWith counts the INFO records carrying key=value.
func (r *logRecorder) infoLinesWith(key, value string) int {
	return r.infoLinesWhere(func(a slog.Attr) bool { return a.Key == key && a.Value.String() == value })
}

// The Connect that ends an outage Sentry heard of is logged, so the log says
// when the session came back.
func TestGatewayWatch_AConnectAfterTheEventLogsThatTheSessionIsBack(t *testing.T) {
	logs := recordLogs(t)
	tr := initSentryWithTransport(t)
	gw := &fakeGatewayEvents{}
	WatchGateway(gw, watchAfter, "session", "main")
	gw.deliver(&discordgo.Connect{})
	gw.deliver(&discordgo.Disconnect{})
	if events := waitForEvents(t, tr, 1); len(events) != 1 {
		t.Fatalf("Sentry got %d events, want 1", len(events))
	}
	before := logs.infoLinesWith("session", "main")

	gw.deliver(&discordgo.Connect{})

	if after := logs.infoLinesWith("session", "main"); after <= before {
		t.Error("no INFO line naming the session after it reconnected")
	}
}

// Each outage is its own: a session that came back and then dropped again
// for the whole delay sends a second event.
func TestGatewayWatch_ALaterOutageSendsANewEvent(t *testing.T) {
	tr := initSentryWithTransport(t)
	gw := &fakeGatewayEvents{}
	WatchGateway(gw, watchAfter, "session", "main")
	gw.deliver(&discordgo.Connect{})
	gw.deliver(&discordgo.Disconnect{})
	waitForEvents(t, tr, 1)
	gw.deliver(&discordgo.Connect{})

	gw.deliver(&discordgo.Disconnect{})

	if events := waitForEvents(t, tr, 2); len(events) != 2 {
		t.Errorf("Sentry got %d events over two outages, want 2", len(events))
	}
}

// One drop can emit Disconnect twice, when discordgo's reader and its
// heartbeat each close the session. It is still one outage, and one event.
func TestGatewayWatch_ASecondDisconnectInOneOutageSendsNothingMore(t *testing.T) {
	tr := initSentryWithTransport(t)
	gw := &fakeGatewayEvents{}
	WatchGateway(gw, watchAfter, "session", "main")
	gw.deliver(&discordgo.Connect{})

	gw.deliver(&discordgo.Disconnect{})
	gw.deliver(&discordgo.Disconnect{})

	if events := waitForEvents(t, tr, 1); len(events) != 1 {
		t.Errorf("Sentry got %d events for one outage, want 1", len(events))
	}
}

// Closing a session on purpose emits Disconnect too, and a watch stood down
// first sends nothing for it.
func TestGatewayWatch_AStoodDownWatchSendsNothing(t *testing.T) {
	tr := initSentryWithTransport(t)
	gw := &fakeGatewayEvents{}
	w := WatchGateway(gw, watchAfter, "session", "main")
	gw.deliver(&discordgo.Connect{})

	w.StandDown()
	gw.deliver(&discordgo.Disconnect{})

	if events := waitForEvents(t, tr, 0); len(events) != 0 {
		t.Errorf("Sentry got %d events after the watch stood down, want none", len(events))
	}
}
