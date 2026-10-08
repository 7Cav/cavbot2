package utils

import (
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

// StartWatch sends a failed start to Sentry before the process exits.
type StartWatch struct {
	now func() time.Time
	// ended is set once the bot is running or a stop has ended the start.
	ended bool
}

// sentFailure is the record of the last failed start sent to Sentry.
type sentFailure struct {
	Message string    `json:"message"`
	SentAt  time.Time `json:"sent_at"`
}

// NewStartWatch returns a watch over this start. now is the clock.
func NewStartWatch(now func() time.Time) *StartWatch {
	return &StartWatch{now: now}
}

// ReportFailure must be deferred directly by main, after InitSentry. It sends
// a panic that ends the start to Sentry, then lets the panic go on, so the
// process still exits with status 2 and the panic on stderr. With no Sentry,
// or once the start has ended, it leaves the panic alone and Go prints it as
// it always has.
func (w *StartWatch) ReportFailure() {
	if w.ended || sentry.CurrentHub().Client() == nil {
		return
	}
	r := recover()
	if r == nil {
		return
	}
	w.send(fmt.Sprint(r))
	panic(r)
}

// send captures a message event rather than going through CaptureError,
// whose exception carries a stack trace. Sentry groups an event with no stack
// trace by its message, so each cause of a failed start gets its own issue
// instead of every cause from one panic site sharing one.
func (w *StartWatch) send(msg string) {
	path := recordPath()
	now := w.now()
	if data, err := os.ReadFile(path); err == nil {
		var last sentFailure
		if json.Unmarshal(data, &last) == nil && last.Message == msg && now.Sub(last.SentAt) < repeatWindow {
			Info("Failed start already sent to Sentry", "sent_at", last.SentAt)
			return
		}
	}

	event := sentry.NewEvent()
	event.Level = sentry.LevelFatal
	event.Message = msg
	sentry.CaptureEvent(event)
	sentry.Flush(2 * time.Second)

	data, _ := json.Marshal(sentFailure{Message: msg, SentAt: now})
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
