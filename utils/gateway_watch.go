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

// closeAuthenticationFailed is the close code Discord ends a session with
// when it refuses the session's token. Discord says not to reconnect after
// it.
const closeAuthenticationFailed = 4004

// tokenRefusedMessage is the line a session whose token Discord refused
// logs, once.
const tokenRefusedMessage = "Discord refused the gateway session's token"

// errGatewayOffline is the error a gateway outage goes to Sentry with.
var errGatewayOffline = errors.New("gateway session disconnected and not reconnected")

// GatewayWatch watches one gateway session for a gateway outage (see
// GLOSSARY.md). discordgo emits Disconnect when the session's gateway
// connection closes, then retries the connection forever, backing off to
// 600 s between tries, and tells only its logger when a try fails. Sentry
// hears of the outage from here, since no log line reaches it (ADR 0001).
//
// Only READY or RESUMED ends an outage. discordgo v0.29.0 emits Connect for
// a login Discord answers with Op 9 Invalid Session too, and counts it a
// success, so it logs in again at once when the connection closes. Discord
// does that to a session whose token it refused: Op 9, then a 4004 close
// after the identify. discordgo tells no handler of the close code, so the
// watch reads the session's closes itself, and a 4004 stops the logins. Its
// tests play that sequence against a scripted gateway, so a discordgo
// upgrade that changes it fails them.
type GatewayWatch struct {
	dg    *discordgo.Session
	after time.Duration

	mu sync.Mutex
	kv []any
	// outage numbers the session's outages, so the report an ended one set
	// finds itself stale. down is set from the Disconnect that starts an
	// outage until the READY or RESUMED that ends it, and reported once
	// Sentry has heard of it.
	outage    uint64
	down      bool
	reported  bool
	stoodDown bool
}

// WatchGateway watches the session whose events these are. When it has been
// disconnected for after, Sentry gets one event carrying kv, the pairs that
// name the session, and the READY or RESUMED that ends that outage logs an
// INFO line with them. Watch a session before it opens, so the watch reads the
// close of every connection it makes, and stand the watch down before
// closing the session on purpose, since that close emits Disconnect too.
func WatchGateway(dg *discordgo.Session, after time.Duration, kv ...any) *GatewayWatch {
	w := &GatewayWatch{dg: dg, after: after, kv: kv}
	dg.Dialer = gatewayDialer(w.closed)
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) { w.online() })
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { w.online() })
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) { w.disconnected() })
	return w
}

// closed hears the code of a close Discord sent the session. After a 4004,
// Discord has refused the session's token, so no login can succeed, and the
// session logs in no more. It runs on the goroutine that read the close.
// Live, that is discordgo's listener, which reconnects from there next and
// reads ShouldReconnectOnError as it starts. A 4004 that fails an Open
// instead leaves a reconnect loop already running to back off as for any
// failed login. discordgo's Open holds the session's lock while it reads,
// so closed takes none of the session's locks.
func (w *GatewayWatch) closed(code int) {
	if code != closeAuthenticationFailed {
		return
	}
	w.dg.ShouldReconnectOnError = false
	Warn(tokenRefusedMessage, w.pairs()...)
}

// disconnected starts an outage, and the report that fires after it. A
// Disconnect during an outage changes nothing: the outage's delay runs from
// its first, however many logins fail after it.
func (w *GatewayWatch) disconnected() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stoodDown || w.down {
		return
	}
	w.down = true
	w.outage++
	outage := w.outage
	time.AfterFunc(w.after, func() { w.fire(outage) })
}

// fire sends the outage numbered outage to Sentry, unless it has ended or
// the watch stood down.
func (w *GatewayWatch) fire(outage uint64) {
	defer RecoverPanic("gateway-watch", w.pairs()...)
	w.mu.Lock()
	if !w.down || outage != w.outage || w.stoodDown {
		w.mu.Unlock()
		return
	}
	w.reported = true
	kv := w.kv
	w.mu.Unlock()
	CaptureError("Gateway session offline and can't reconnect", errGatewayOffline, kv...)
}

// online ends an outage, since Discord started the session or resumed it,
// and logs that the session is back when Sentry heard of the outage.
func (w *GatewayWatch) online() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.down = false
	if w.reported {
		w.reported = false
		Info("Gateway session back online", w.kv...)
	}
}

// Name adds pairs that name the session, for what only READY tells, such
// as a recorder's user ID. It builds the pairs anew, so those a report
// already read stay as they were.
func (w *GatewayWatch) Name(kv ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.kv = append(w.kv[:len(w.kv):len(w.kv)], kv...)
}

// pairs is the pairs that name the session, as they stand.
func (w *GatewayWatch) pairs() []any {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.kv
}

// StandDown stops the watch. Call it before closing the session on purpose.
// The watch sends nothing after it, not even a report already set.
func (w *GatewayWatch) StandDown() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stoodDown = true
}
