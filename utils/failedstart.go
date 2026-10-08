package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/getsentry/sentry-go"
)

// failedStartRecord names the file, in the temp dir, that holds the last
// failed start sent to Sentry. Docker keeps a container's files across a
// restart and drops them when a deploy recreates it.
const failedStartRecord = "cavbot2-failed-start.json"

// repeatWindow is how long a failed start with the message last sent stays
// out of Sentry. A restart loop would otherwise send an event every minute or
// faster and spend the org's monthly quota in days.
const repeatWindow = time.Hour

// StartWatch sends a failed start to Sentry before the process exits.
type StartWatch struct {
	now     func() time.Time
	running bool
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
// process still exits with status 2 and the panic on stderr.
func (w *StartWatch) ReportFailure() {
	r := recover()
	if r == nil {
		return
	}
	if !w.running && sentry.CurrentHub().Client() != nil {
		w.send(fmt.Sprint(r))
	}
	panic(r)
}

func (w *StartWatch) send(msg string) {
	record := filepath.Join(os.TempDir(), failedStartRecord)
	now := w.now()
	if data, err := os.ReadFile(record); err == nil {
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
	if err := os.WriteFile(record, data, 0o600); err != nil {
		Warn("Cannot record the failed start sent to Sentry", "error", err)
	}
}

// Running marks the start as reached running. The next failed start sends,
// whatever its message.
func (w *StartWatch) Running() {
	w.running = true
	err := os.Remove(filepath.Join(os.TempDir(), failedStartRecord))
	if err != nil && !os.IsNotExist(err) {
		Warn("Cannot clear the failed start sent to Sentry", "error", err)
	}
}
