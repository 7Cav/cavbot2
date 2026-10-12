package utils

import (
	"errors"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// GatewayOfflineAfter is how long a gateway session stays disconnected
// before Sentry hears of it. A Discord blip reconnects in seconds, so one
// still off after a minute is a gateway outage worth hearing of.
const GatewayOfflineAfter = time.Minute

// GatewayEvents is where a gateway watch registers its handlers. Outside
// the tests it is a *discordgo.Session.
type GatewayEvents interface {
	AddHandler(handler interface{}) func()
}

// errGatewayOffline is the error a gateway outage goes to Sentry with.
var errGatewayOffline = errors.New("gateway session disconnected and not reconnected")

// GatewayWatch watches one gateway session for a gateway outage (see
// GLOSSARY.md). discordgo emits Disconnect when the session's gateway
// connection closes, then retries the connection forever, backing off to
// 600 s between tries, and tells only its logger when a try fails. It emits
// Connect once a try gets back on. Sentry hears of the outage from here,
// since no log line reaches it (ADR 0001).
type GatewayWatch struct {
	after time.Duration
	kv    []any

	mu sync.Mutex
	// event numbers the session's Connect and Disconnect events, so a report
	// fires only while the Disconnect that set it is the latest event.
	// reported is set once Sentry has heard of the outage.
	event     uint64
	reported  bool
	stoodDown bool
}

// WatchGateway watches the session whose events these are. When it has been
// disconnected for after with no Connect, Sentry gets one event carrying kv,
// the pairs that name the session, and the Connect that ends that outage
// logs an INFO line with them. A Connect within after sends nothing. Watch a
// session before it opens, or once it has, and stand the watch down before
// closing the session on purpose, since that close emits Disconnect too.
func WatchGateway(events GatewayEvents, after time.Duration, kv ...any) *GatewayWatch {
	w := &GatewayWatch{after: after, kv: kv}
	events.AddHandler(func(_ *discordgo.Session, _ *discordgo.Connect) { w.connected() })
	events.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) { w.disconnected() })
	return w
}

// disconnected starts an outage, and the report that fires after it unless
// a Connect comes first. A second Disconnect for one drop, as when
// discordgo's reader and heartbeat both close the session, sets the report
// again, and the first one finds itself stale.
func (w *GatewayWatch) disconnected() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stoodDown {
		return
	}
	w.event++
	event := w.event
	time.AfterFunc(w.after, func() { w.fire(event) })
}

// fire sends the outage to Sentry, unless the session has had another event
// since the Disconnect numbered event, or the watch stood down.
func (w *GatewayWatch) fire(event uint64) {
	defer RecoverPanic("gateway-watch", w.kv...)
	w.mu.Lock()
	if event != w.event || w.stoodDown {
		w.mu.Unlock()
		return
	}
	w.reported = true
	w.mu.Unlock()
	CaptureError("Gateway session offline and can't reconnect", errGatewayOffline, w.kv...)
}

// connected ends an outage, and logs that the session is back when Sentry
// heard of the outage.
func (w *GatewayWatch) connected() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.event++
	if w.reported {
		w.reported = false
		Info("Gateway session back online", w.kv...)
	}
}

// StandDown stops the watch. Call it before closing the session on purpose.
// The watch sends nothing after it, not even a report already set.
func (w *GatewayWatch) StandDown() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stoodDown = true
}
