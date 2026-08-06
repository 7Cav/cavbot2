package utils

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

type mockTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *mockTransport) Configure(_ sentry.ClientOptions)      {}
func (t *mockTransport) Flush(_ time.Duration) bool            { return true }
func (t *mockTransport) FlushWithContext(_ context.Context) bool { return true }
func (t *mockTransport) Close()                                {}
func (t *mockTransport) SendEvent(e *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, e)
}
func (t *mockTransport) Events() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event(nil), t.events...)
}

func resetSentryHub(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil) })
}

func initSentryWithTransport(t *testing.T) *mockTransport {
	t.Helper()
	tr := &mockTransport{}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://test@example.com/1",
		Transport: tr,
	}); err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	resetSentryHub(t)
	return tr
}

func TestInitSentry_NoDSN(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	flush := InitSentry("v0.0.0-test")
	if flush == nil {
		t.Fatal("expected non-nil flush func")
	}
	flush() // must not panic
}

func TestInitSentry_NoDSN_CaptureErrorSendsNothing(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	resetSentryHub(t)

	InitSentry("v0.0.0-test")
	CaptureError("test error", errors.New("oops"))

	if sentry.CurrentHub().Client() != nil {
		t.Fatal("expected no Sentry client when DSN is empty")
	}
}

func TestCaptureError_SendsEvent(t *testing.T) {
	tr := initSentryWithTransport(t)

	err := errors.New("something broke")
	CaptureError("db query failed", err, "table", "users", "query_id", 42)

	events := tr.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]

	if len(ev.Exception) == 0 {
		t.Fatal("expected at least one exception")
	}
	if ev.Exception[0].Value != "something broke" {
		t.Errorf("exception value = %q, want %q", ev.Exception[0].Value, "something broke")
	}

	if ev.Tags["message"] != "db query failed" {
		t.Errorf("tag message = %q, want %q", ev.Tags["message"], "db query failed")
	}

	extras, ok := ev.Contexts["extra"]
	if !ok {
		t.Fatal("expected 'extra' context on event")
	}
	if extras["table"] != "users" {
		t.Errorf("extra[table] = %v, want %q", extras["table"], "users")
	}
	if extras["query_id"] != 42 {
		t.Errorf("extra[query_id] = %v, want 42", extras["query_id"])
	}
}

// Usage lives in Grafana and failures live in Sentry (ADR 0011), so "which
// command is failing, and how often" has to be answerable on the Sentry side
// alone. That needs command to be a tag: Sentry groups and filters on tags,
// not on the extra context where it landed before.
func TestCaptureError_CommandIsGroupable(t *testing.T) {
	tr := initSentryWithTransport(t)

	CaptureError("roster fetch failed", errors.New("upstream 500"), "command", "afsm", "guild_id", "9001")

	events := tr.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if got := events[0].Tags["command"]; got != "afsm" {
		t.Errorf("tag command = %q, want %q", got, "afsm")
	}
}

func TestCaptureError_SentryDisabled_NoPanic(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	resetSentryHub(t)

	// No sentry client bound — CaptureError must not panic.
	CaptureError("safe error", errors.New("noop"))

	if sentry.CurrentHub().Client() != nil {
		t.Fatal("expected no Sentry client when DSN is empty")
	}
}

func TestRecoverPanic_SwallowsAndSendsEvent(t *testing.T) {
	tr := initSentryWithTransport(t)

	triggerPanic := func() {
		defer RecoverPanic("test-ctx")
		panic("boom")
	}

	// Must not panic or call t.Fatal from a goroutine — run inline.
	triggerPanic()

	events := tr.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]
	// A string panic is captured as a message event, not an exception event.
	if ev.Message != "boom" {
		t.Errorf("event message = %q, want %q", ev.Message, "boom")
	}
}

func TestRecoverPanic_NoPanic_NoEvent(t *testing.T) {
	tr := initSentryWithTransport(t)

	func() {
		defer RecoverPanic("test-ctx-no-panic")
		// no panic
	}()

	if len(tr.Events()) != 0 {
		t.Errorf("expected 0 events, got %d", len(tr.Events()))
	}
}

// A panic is a failure like any other, so it has to be attributable to the
// command that raised it — otherwise every handler panic groups under one
// dispatcher-level context and per-command error rate goes blind exactly where
// it matters most.
func TestRecoverPanic_CommandIsGroupable(t *testing.T) {
	tr := initSentryWithTransport(t)

	func() {
		defer RecoverPanic("slash-command", "command", "warden")
		panic("nil map write")
	}()

	events := tr.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if got := events[0].Tags["command"]; got != "warden" {
		t.Errorf("tag command = %q, want %q", got, "warden")
	}
}

func TestRecoverPanic_TagsContextOnEvent(t *testing.T) {
	tr := initSentryWithTransport(t)

	func() {
		defer RecoverPanic("test-goroutine")
		panic("tag-test")
	}()

	events := tr.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Tags["context"] != "test-goroutine" {
		t.Errorf("tag context = %q, want %q", events[0].Tags["context"], "test-goroutine")
	}
}
