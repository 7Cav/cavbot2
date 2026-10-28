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

// TokenRefusedMessage is the line a session whose token Discord refused
// logs, once.
const TokenRefusedMessage = "Discord refused the gateway session's token"

// GatewayOfflineMessage is the Sentry event of a gateway outage that lasts.
const GatewayOfflineMessage = "Gateway session offline and can't reconnect"

// GatewayBackOnlineMessage is the INFO line the READY or RESUMED that ends
// an outage Sentry heard of logs.
const GatewayBackOnlineMessage = "Gateway session back online"

// errGatewayOffline is the error a gateway outage goes to Sentry with.
var errGatewayOffline = errors.New("gateway session disconnected and not reconnected")

// GatewayWatch watches one account's gateway sessions, the bot's own or a
// recorder's, for a gateway outage (see GLOSSARY.md). It reads one session
// at a time. The bot keeps one session for the whole run, and a recorder
// logs in with a new session each time, so one outage can span several of
// its logins. discordgo emits Disconnect when a session's gateway
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
	after time.Duration

	mu sync.Mutex
	kv []any
	// session is the session the watch reads, nil while it reads none, as
	// between a recorder's logins. What a session it read before does
	// changes nothing.
	session *watched
	// outage numbers the account's outages, so the report an ended one set
	// finds itself stale. down is set from the start of an outage until the
	// READY or RESUMED that ends it, and reported once Sentry has heard of
	// it.
	outage    uint64
	down      bool
	reported  bool
	stoodDown bool
}

// watched is a session a watch reads, or read before. The watch's mutex
// guards up and refused.
type watched struct {
	dg *discordgo.Session
	// up is set once READY or RESUMED has come on one of the session's
	// connections, and refused once Discord has closed one with 4004.
	up, refused bool
	// onRefused, when set, hears of the refusal of the session once it was
	// up, in place of the watch's own line.
	onRefused func()
}

// NewGatewayWatch watches an account, and reads none of its sessions until
// Watch. When the account has been in a gateway outage for after, Sentry
// gets one event carrying kv, the pairs that name the account, and the
// READY or RESUMED that ends that outage logs an INFO line with them.
func NewGatewayWatch(after time.Duration, kv ...any) *GatewayWatch {
	return &GatewayWatch{after: after, kv: kv}
}

// WatchGateway watches the account of dg, reading dg, as NewGatewayWatch
// and Watch do.
func WatchGateway(dg *discordgo.Session, after time.Duration, kv ...any) *GatewayWatch {
	w := NewGatewayWatch(after, kv...)
	w.Watch(dg)
	return w
}

// Watch reads dg from now on, in place of the session the watch read
// before. Watch a session before it opens, so the watch reads the close of
// every connection it makes. A close on purpose emits Disconnect too, so
// close a session the watch reads with CloseSession, or stand the watch
// down first.
func (w *GatewayWatch) Watch(dg *discordgo.Session) {
	w.watch(&watched{dg: dg})
}

// WatchWithRefusal reads dg as Watch does, and when Discord refuses dg's
// token once dg was up, calls refused in place of logging
// TokenRefusedMessage, so the caller tells of the refusal in its own words.
// refused runs once, on the goroutine that read the close, with no lock of
// the watch held.
func (w *GatewayWatch) WatchWithRefusal(dg *discordgo.Session, refused func()) {
	w.watch(&watched{dg: dg, onRefused: refused})
}

// watch reads ws from now on, in place of the session the watch read
// before.
func (w *GatewayWatch) watch(ws *watched) {
	dg := ws.dg
	dg.Dialer = gatewayDialer(func(code int) { w.closed(ws, code) })
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) { w.online(ws) })
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { w.online(ws) })
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) { w.disconnected(ws) })
	w.mu.Lock()
	defer w.mu.Unlock()
	w.session = ws
}

// closed hears the code of a close Discord sent the session ws. After a
// 4004, Discord has refused the session's token, so no login can succeed,
// and the session logs in no more. A session refused before it was ever up
// failed to open, and the caller of its open tells of the refusal, so the
// watch tells of it only for a session that was up, once: to the session's
// onRefused when it has one, else in the log. It runs on the goroutine
// that read the close. For a session that was running, that is discordgo's
// listener. Discord answers its resume with Op 9, discordgo sends the
// identify from inside Open, and the 4004 for that identify comes after Open
// has returned. The listener reconnects next, and reads
// ShouldReconnectOnError as it starts. A 4004 during the session's first
// Open fails its start instead. discordgo's Open holds the session's lock
// while it reads, so closed takes none of the session's locks. It writes
// ShouldReconnectOnError under the watch's mutex, as CloseSession does, and
// tells of the refusal after letting go of it.
func (w *GatewayWatch) closed(ws *watched, code int) {
	if code != closeAuthenticationFailed {
		return
	}
	w.mu.Lock()
	ws.dg.ShouldReconnectOnError = false
	tell := !ws.refused && ws == w.session && ws.up
	ws.refused = true
	kv := w.kv
	w.mu.Unlock()
	switch {
	case !tell:
	case ws.onRefused != nil:
		ws.onRefused()
	default:
		Warn(TokenRefusedMessage, kv...)
	}
}

// disconnected starts an outage when ws is the session the watch reads.
func (w *GatewayWatch) disconnected(ws *watched) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ws == w.session {
		w.startLocked()
	}
}

// Offline starts an outage now, though the account went offline on
// purpose, as a recorder does before its first login. The account is
// offline until a session the watch reads next reaches READY.
func (w *GatewayWatch) Offline() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.startLocked()
}

// startLocked starts an outage, and the report that fires after it. An
// outage already under way goes on: its delay runs from its start, however
// many logins fail after it. Caller holds mu.
func (w *GatewayWatch) startLocked() {
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
	CaptureError(GatewayOfflineMessage, errGatewayOffline, kv...)
}

// online ends an outage when ws is the session the watch reads, since
// Discord started the session or resumed it, and logs that the session is
// back when Sentry heard of the outage.
func (w *GatewayWatch) online(ws *watched) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ws.up = true
	if ws != w.session {
		return
	}
	w.down = false
	if w.reported {
		w.reported = false
		Info(GatewayBackOnlineMessage, w.kv...)
	}
}

// AddPairs adds pairs that name the account, for what only READY tells,
// such as a recorder's user ID. It builds the pairs anew, so those a report
// already read stay as they were.
func (w *GatewayWatch) AddPairs(kv ...any) {
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

// StandDown stops the watch for good, as at shutdown. Call it before
// closing the session on purpose for good. The watch sends nothing after
// it, not even a report already set.
func (w *GatewayWatch) StandDown() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stoodDown = true
}

// CloseSession closes the session the watch reads on purpose, and the
// session logs in no more. Use it to close a session that failed to open,
// and at a recorder's logout, after Offline. The watch reads it no more,
// and an outage under way goes on, so the failed login of an offline
// recorder leaves it offline.
// discordgo's Open starts the session's heartbeat, and a close right after
// Open nearly always beats the heartbeat's first write. That write fails, so
// the heartbeat closes the session again and reconnects, unless
// ShouldReconnectOnError is off. The heartbeat's own close waits for the
// lock this close holds, so the heartbeat sees the flag off.
//
// CloseSession reports whether Discord refused the session's token, closing
// one of its connections with 4004. Discord sends the 4004 for an identify
// that answers its Op 9 after the open has returned. discordgo's close waits
// a second for Discord to close the connection, and its listener reads the
// connection meanwhile, so the close reads that 4004 too.
func (w *GatewayWatch) CloseSession() (refused bool, err error) {
	w.mu.Lock()
	ws := w.session
	w.session = nil
	if ws != nil {
		ws.dg.ShouldReconnectOnError = false
	}
	w.mu.Unlock()
	if ws == nil {
		return false, nil
	}
	err = ws.dg.Close()
	w.mu.Lock()
	defer w.mu.Unlock()
	return ws.refused, err
}
