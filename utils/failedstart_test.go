package utils

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// sentryEvent is the part of an event item the failed start tests read.
type sentryEvent struct {
	Message   string            `json:"message"`
	Release   string            `json:"release"`
	Exception []json.RawMessage `json:"exception"`
	Threads   []json.RawMessage `json:"threads"`
}

// sentryListener stands in for Sentry. It is a local HTTP server named in
// SENTRY_DSN, and it keeps the event items of the envelopes it receives, so
// a test sends through the client InitSentry sets up, transport included.
type sentryListener struct {
	mu     sync.Mutex
	events []sentryEvent
}

func listenAsSentry(t *testing.T, version string) *sentryListener {
	t.Helper()
	l := &sentryListener{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read envelope: %v", err)
			return
		}
		l.keepEvents(t, body)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("SENTRY_DSN", "http://key@"+strings.TrimPrefix(srv.URL, "http://")+"/1")
	resetSentryHub(t)
	InitSentry(version)
	return l
}

// keepEvents reads an envelope: a header line, then an item header line and
// a payload line for each item.
func (l *sentryListener) keepEvents(t *testing.T, envelope []byte) {
	lines := bufio.NewScanner(bytes.NewReader(envelope))
	lines.Buffer(nil, 1<<20)
	lines.Scan()
	for lines.Scan() {
		var item struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(lines.Bytes(), &item); err != nil {
			t.Errorf("item header %q: %v", lines.Text(), err)
			return
		}
		if !lines.Scan() {
			t.Errorf("item %q has no payload", item.Type)
			return
		}
		if item.Type != "event" {
			continue
		}
		var e sentryEvent
		if err := json.Unmarshal(lines.Bytes(), &e); err != nil {
			t.Errorf("event payload: %v", err)
			return
		}
		l.mu.Lock()
		l.events = append(l.events, e)
		l.mu.Unlock()
	}
}

func (l *sentryListener) Events() []sentryEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]sentryEvent(nil), l.events...)
}

// failStart runs a start that fails with msg, watched by w, and returns the
// value that leaves ReportFailure: what the process would die with.
func failStart(w *StartWatch, msg string) (left any) {
	defer func() { left = recover() }()
	defer w.ReportFailure()
	panic(msg)
}

func clockAt(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

var firstFailure = time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)

// The container log keeps the panic, and Sentry has the failure by the time
// the process exits with it.
func TestFailedStart_SendsItsMessageBeforeThePanicGoesOn(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	sentryEvents := listenAsSentry(t, "1.2.3")
	const msg = "Bot store unavailable: ping bot database after 10 attempts: connection refused"

	left := failStart(NewStartWatch(clockAt(firstFailure)), msg)
	got := sentryEvents.Events()

	if left != msg {
		t.Errorf("panic left as %v, want %q", left, msg)
	}
	if len(got) != 1 {
		t.Fatalf("Sentry had %d events when the panic left, want 1", len(got))
	}
	if got[0].Message != msg {
		t.Errorf("event message = %q, want %q", got[0].Message, msg)
	}
	if got[0].Release != "cavbot2@1.2.3" {
		t.Errorf("event release = %q, want %q", got[0].Release, "cavbot2@1.2.3")
	}
}

func TestFailedStart_WithoutSentryPanicsAsBefore(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("SENTRY_DSN", "")
	resetSentryHub(t)
	InitSentry("1.2.3")
	const msg = "Panel misconfigured: PANEL_ADDR: missing port"

	if left := failStart(NewStartWatch(clockAt(firstFailure)), msg); left != msg {
		t.Errorf("panic left as %v, want %q", left, msg)
	}

	// Nothing went to Sentry, so no line may say it did.
	logs := recordLogs(t)
	failStart(NewStartWatch(clockAt(firstFailure.Add(time.Minute))), msg)
	if got := logs.infoLinesCarrying(firstFailure); got != 0 {
		t.Errorf("%d INFO lines carry the first failure's time, want none", got)
	}
}

// A stop during startup ends main by returning, not by a panic.
func TestFailedStart_AStartThatEndsWithoutAPanicSendsNothing(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	sentryEvents := listenAsSentry(t, "1.2.3")

	func() {
		defer NewStartWatch(clockAt(firstFailure)).ReportFailure()
	}()
	sentry.Flush(2 * time.Second)

	if got := sentryEvents.Events(); len(got) != 0 {
		t.Errorf("Sentry got %d events, want none: %+v", len(got), got)
	}
}

// Sentry groups an event with no stack trace by its message, so two causes
// from one startup step, such as a database that doesn't answer and a
// migration that fails, land in two issues.
func TestFailedStart_EventCarriesNoExceptionOrStackTrace(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	sentryEvents := listenAsSentry(t, "1.2.3")

	failStart(NewStartWatch(clockAt(firstFailure)), "Bot store unavailable: migrate: syntax error at line 3")
	got := sentryEvents.Events()

	if len(got) != 1 {
		t.Fatalf("Sentry got %d events, want 1", len(got))
	}
	if len(got[0].Exception) != 0 {
		t.Errorf("event carries an exception: %s", got[0].Exception)
	}
	if len(got[0].Threads) != 0 {
		t.Errorf("event carries a stack trace: %s", got[0].Threads)
	}
}

// logRecorder keeps every record logged through Logger.
type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}

func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *logRecorder) WithGroup(string) slog.Handler { return r }

func recordLogs(t *testing.T) *logRecorder {
	t.Helper()
	rec := &logRecorder{}
	prev := Logger
	t.Cleanup(func() { Logger = prev })
	Logger = slog.New(rec)
	return rec
}

// infoLinesCarrying counts the INFO records with a time attribute at when.
func (r *logRecorder) infoLinesCarrying(when time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.records {
		if rec.Level != slog.LevelInfo {
			continue
		}
		carries := false
		rec.Attrs(func(a slog.Attr) bool {
			v := a.Value.Resolve()
			carries = v.Kind() == slog.KindTime && v.Time().Equal(when)
			return !carries
		})
		if carries {
			n++
		}
	}
	return n
}

// A start that fails the same way in a restart loop would spend the org's
// Sentry quota, so a repeat within the hour stays in the container log.
func TestFailedStart_RepeatWithinTheHourSendsNothingAndSaysWhenItWent(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	sentryEvents := listenAsSentry(t, "1.2.3")
	const msg = "Discord session unavailable: no READY within 30s"
	failStart(NewStartWatch(clockAt(firstFailure)), msg)

	logs := recordLogs(t)
	failStart(NewStartWatch(clockAt(firstFailure.Add(59*time.Minute))), msg)
	sentry.Flush(2 * time.Second)

	if got := len(sentryEvents.Events()); got != 1 {
		t.Errorf("Sentry got %d events, want only the first start's", got)
	}
	if got := logs.infoLinesCarrying(firstFailure); got != 1 {
		t.Errorf("%d INFO lines carry the first send's time, want 1", got)
	}
}

// While a restart loop runs, its issue's "last seen" stays within the hour.
func TestFailedStart_RepeatAnHourLaterSendsAgain(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	sentryEvents := listenAsSentry(t, "1.2.3")
	const msg = "Panel unavailable: listen tcp :8080: bind: address already in use"
	failStart(NewStartWatch(clockAt(firstFailure)), msg)

	failStart(NewStartWatch(clockAt(firstFailure.Add(time.Hour))), msg)

	if got := len(sentryEvents.Events()); got != 2 {
		t.Errorf("Sentry got %d events, want 2", got)
	}
}

func TestFailedStart_DifferentMessageWithinTheHourSends(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	sentryEvents := listenAsSentry(t, "1.2.3")
	failStart(NewStartWatch(clockAt(firstFailure)), "Bot store unavailable: ping bot database after 10 attempts: connection refused")

	const migration = "Bot store unavailable: migrate: syntax error at line 3"
	failStart(NewStartWatch(clockAt(firstFailure.Add(time.Minute))), migration)

	got := sentryEvents.Events()
	if len(got) != 2 {
		t.Fatalf("Sentry got %d events, want 2", len(got))
	}
	if got[1].Message != migration {
		t.Errorf("second event message = %q, want %q", got[1].Message, migration)
	}
}

// A bot that ran in between broke again, which is news even if it broke the
// same way.
func TestFailedStart_AfterAStartReachesRunningTheNextFailureSends(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	sentryEvents := listenAsSentry(t, "1.2.3")
	const msg = "Cannot register commands: 50035 Invalid Form Body"
	failStart(NewStartWatch(clockAt(firstFailure)), msg)

	NewStartWatch(clockAt(firstFailure.Add(30 * time.Second))).Running()
	failStart(NewStartWatch(clockAt(firstFailure.Add(time.Minute))), msg)

	if got := len(sentryEvents.Events()); got != 2 {
		t.Errorf("Sentry got %d events, want 2", got)
	}
}

func TestFailedStart_SendsWhenItsRecordIsOutOfReach(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "tmp")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", notADir)
	sentryEvents := listenAsSentry(t, "1.2.3")

	failStart(NewStartWatch(clockAt(firstFailure)), "Error creating Discord session: bad token")

	if got := len(sentryEvents.Events()); got != 1 {
		t.Errorf("Sentry got %d events, want 1", got)
	}
}

// A panic once the bot is running, such as the deferred Discord close at
// shutdown, is not a failed start.
func TestFailedStart_PanicAfterRunningSendsNothing(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	sentryEvents := listenAsSentry(t, "1.2.3")
	const msg = "Error closing Discord connection: websocket: close sent"
	w := NewStartWatch(clockAt(firstFailure))

	left := func() (left any) {
		defer func() { left = recover() }()
		defer w.ReportFailure()
		w.Running()
		panic(msg)
	}()
	sentry.Flush(2 * time.Second)

	if left != msg {
		t.Errorf("panic left as %v, want %q", left, msg)
	}
	if got := sentryEvents.Events(); len(got) != 0 {
		t.Errorf("Sentry got %d events, want none: %+v", len(got), got)
	}
}
