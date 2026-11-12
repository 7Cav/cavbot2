package utils

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/getsentry/sentry-go"
)

// failedStartFile names the file, in the temp dir, that holds the last
// failed start sent to Sentry. Docker keeps a container's files across a
// restart and drops them when a deploy recreates it.
const failedStartFile = "cavbot2-failed-start.json"

// repeatWindow is how long a failed start with the message last sent stays
// out of Sentry. A restart loop would otherwise send an event every minute or
// faster and spend the org's monthly quota in days.
const repeatWindow = time.Hour

// minUptimeAfterLogin is how long a process that logged in to Discord runs
// before a failed start exits. Docker restarts it at once, and Discord resets
// a token after about 1,000 logins a day, so this caps a restart loop at 720
// logins a day per token.
const minUptimeAfterLogin = 2 * time.Minute

// StartWatch sends a failed start to Sentry before the process exits.
type StartWatch struct {
	now   func() time.Time
	began time.Time
	// ended is set once the bot is running or a stop has ended the start.
	ended bool
	// login is set once a login has started, and its end ends the wait a
	// failed start then makes before it exits.
	login context.Context
}

// sentFailure is the record of the last failed start sent to Sentry. It
// keeps the SHA-256 digest of the message, which is all a later start needs
// to tell a repeat, so the panic's text goes to Sentry and nowhere else
// (ADR 0018).
type sentFailure struct {
	Digest string    `json:"digest"`
	SentAt time.Time `json:"sent_at"`
}

// NewStartWatch returns a watch over this start, which begins now. now is
// the clock.
func NewStartWatch(now func() time.Time) *StartWatch {
	return &StartWatch{now: now, began: now()}
}

// LoggingIn records that a login to Discord is about to start. A failed start
// from here on waits until the process has run minUptimeAfterLogin before it
// exits, or until ctx ends.
func (w *StartWatch) LoggingIn(ctx context.Context) {
	w.login = ctx
}

// ReportFailure must be deferred directly by main, after InitSentry. It sends
// a panic that ends the start to Sentry, waits as LoggingIn says, then lets
// the panic go on, so the process still exits with status 2 and the panic on
// stderr. With no Sentry and no login, or once the start has ended, it leaves
// the panic alone and Go prints it as it always has.
func (w *StartWatch) ReportFailure() {
	sentryOn := sentry.CurrentHub().Client().IsEnabled()
	if w.ended || (!sentryOn && w.login == nil) {
		return
	}
	r := recover()
	if r == nil {
		return
	}
	if sentryOn {
		err, ok := r.(error)
		if !ok {
			err = fmt.Errorf("%v", r)
		}
		w.send(err)
	}
	w.holdAfterLogin()
	panic(r)
}

// holdAfterLogin waits out the rest of minUptimeAfterLogin once a login has
// started. A stop ends the wait, and a second signal kills the process.
func (w *StartWatch) holdAfterLogin() {
	if w.login == nil {
		return
	}
	left := w.began.Add(minUptimeAfterLogin).Sub(w.now())
	if left <= 0 {
		return
	}
	Warn("Failed start after a Discord login, waiting before exiting", "wait", left.Round(time.Second))
	t := time.NewTimer(left)
	defer t.Stop()
	select {
	case <-t.C:
	case <-w.login.Done():
	}
}

// send captures a message event rather than going through CaptureError,
// whose exception carries a stack trace. Sentry groups an event with no stack
// trace by its message, so each cause of a failed start gets its own issue
// instead of every cause from one panic site sharing one.
func (w *StartWatch) send(failure error) {
	msg := failure.Error()
	sum := sha256.Sum256([]byte(msg))
	digest := hex.EncodeToString(sum[:])
	path := recordPath()
	now := w.now()
	if data, err := os.ReadFile(path); err == nil {
		var last sentFailure
		if json.Unmarshal(data, &last) == nil && last.Digest == digest && now.Sub(last.SentAt) < repeatWindow {
			Info("Failed start already sent to Sentry", "sent_at", last.SentAt)
			return
		}
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelFatal)
		sentry.CaptureMessage(msg)
	})
	sentry.Flush(2 * time.Second)

	data, _ := json.Marshal(sentFailure{Digest: digest, SentAt: now})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		Warn("Cannot record the failed start sent to Sentry", "error", err)
	}
}

// MarkRunning records that the start reached running. A panic from here on
// is no failed start, and the next failed start sends, whatever its message.
func (w *StartWatch) MarkRunning() {
	w.ended = true
	err := os.Remove(recordPath())
	if err != nil && !os.IsNotExist(err) {
		Warn("Cannot clear the failed start sent to Sentry", "error", err)
	}
}

// Stopping records that a stop ended the start. A panic from here on, as the
// shutdown runs, is no failed start. The record stays, since the bot never
// ran.
func (w *StartWatch) Stopping() {
	w.ended = true
}

func recordPath() string {
	return filepath.Join(os.TempDir(), failedStartFile)
}
