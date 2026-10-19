package utils

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

// reconnectCheckEvery is how often the reconnect hook looks for its file.
const reconnectCheckEvery = 250 * time.Millisecond

// reconnectRetryMax caps the wait between the hook's tries to log in again,
// as discordgo caps its own.
const reconnectRetryMax = 10 * time.Minute

// smokeReconnectMessage is the line the hook logs as it closes the session.
const smokeReconnectMessage = "Smoke hook reconnecting gateway session"

// WatchReconnects is a smoke hook (docs/smoke-test.md). Each time file
// appears, the session closes its gateway connection and logs in again, the
// two ways Discord can make it. A file whose first word is "identify"
// closes with 1000, which ends the session at Discord, so Discord answers
// the resume after it with Op 9 and discordgo identifies anew, as after a
// heartbeat Discord never acknowledged. Any other file closes with 1012, as
// discordgo does on Discord's Op 7, which keeps the session, so the login
// after it resumes. A duration after the word, such as "resume 30s", keeps
// the session offline at least that long before it logs in. Only the file's
// appearance counts, so a file left in place reconnects once. kv names the
// session in the hook's log lines. It looks for file until stop, which
// returns once a reconnect under way has ended and leaves the session as
// that left it.
func WatchReconnects(dg *discordgo.Session, file string, kv ...any) (stop func()) {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer RecoverPanic("smoke-gateway-reconnect", kv...)
		tick := time.NewTicker(reconnectCheckEvery)
		defer tick.Stop()
		present := false
		for {
			select {
			case <-done:
				return
			case <-tick.C:
			}
			content, err := os.ReadFile(file)
			appeared := err == nil && !present
			present = err == nil
			if appeared {
				parseReconnect(string(content)).run(dg, kv, done)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}
}

// smokeReconnect is one reconnect the hook makes.
type smokeReconnect struct {
	// identify ends the session, so the login after it identifies anew.
	identify bool
	// offline is how long the session stays offline before it logs in.
	offline time.Duration
}

// parseReconnect reads the reconnect a hook file asks for.
func parseReconnect(content string) smokeReconnect {
	fields := strings.Fields(content)
	var r smokeReconnect
	if len(fields) > 0 {
		r.identify = fields[0] == "identify"
	}
	if len(fields) > 1 {
		if d, err := time.ParseDuration(fields[1]); err == nil {
			r.offline = d
		}
	}
	return r
}

// run closes the session's gateway connection and logs it in again,
// retrying as discordgo's own reconnect does until a login goes through or
// done closes.
func (r smokeReconnect) run(dg *discordgo.Session, kv []any, done <-chan struct{}) {
	code := websocket.CloseServiceRestart
	if r.identify {
		code = websocket.CloseNormalClosure
	}
	Warn(smokeReconnectMessage, append(kv[:len(kv):len(kv)], "identify", r.identify, "offline", r.offline)...)
	_ = dg.CloseWithCode(code)
	if !waitOrDone(r.offline, done) {
		return
	}
	for wait := time.Second; ; wait = min(2*wait, reconnectRetryMax) {
		err := dg.Open()
		if err == nil || errors.Is(err, discordgo.ErrWSAlreadyOpen) {
			return
		}
		Warn("Smoke hook couldn't log the gateway session in again", append([]any{"error", err}, kv...)...)
		if !waitOrDone(wait, done) {
			return
		}
	}
}

// waitOrDone waits d, and reports false when done closes first.
func waitOrDone(d time.Duration, done <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-done:
		return false
	}
}
