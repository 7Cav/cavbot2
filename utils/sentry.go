package utils

import (
	"context"
	"os"
	"runtime/debug"
	"time"

	"github.com/getsentry/sentry-go"
)

// sentryReleasePrefix starts cavbot2's release names in Sentry. Sentry keeps
// one release namespace per organization, and other projects in the org ship
// bare versions too, so a bare name could share a release with one of them.
// The sentry job in .github/workflows/build_and_push.yml creates the release
// as this prefix plus the tag; the two names must match byte for byte. The
// version stays bare everywhere else, the image tag and panel footer included.
const sentryReleasePrefix = "cavbot2@"

// InitSentry configures the Sentry client; an empty SENTRY_DSN is a valid no-op (zero events, zero overhead).
// Events carry the release cavbot2@<version>.
func InitSentry(version string) func() {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		Info("Sentry disabled (SENTRY_DSN not set)")
		return func() {}
	}

	release := sentryReleasePrefix + version
	env := os.Getenv("APP_ENV")
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      env,
		Release:          release,
		TracesSampleRate: 1.0,
		// The default telemetry buffer can take an event out of its buffer
		// before handing it to the transport, and a Flush between the two
		// returns true with the event unsent. A failed start captures and
		// exits, so it would lose its event. The older transport queues the
		// event inside the capture call, and Flush waits for it.
		DisableTelemetryBuffer: true,
	})
	if err != nil {
		Warn("Sentry init failed, continuing without it", "error", err)
		return func() {}
	}

	Info("Sentry initialized", "environment", env, "release", release)
	return func() { sentry.Flush(2 * time.Second) }
}

// tagKeys are the keys that, wherever they appear in a capture's key/values
// with a string value, are promoted to Sentry tags: "command", so failures
// are groupable by command, and "gap_cause", "recorder" and "close_code", so
// cut-short recordings are countable by what started the gap, which
// recorder it was, and the voice gateway close behind it.
var tagKeys = map[string]bool{"command": true, "gap_cause": true, "recorder": true, "close_code": true}

// promoteTags lifts the tagKeys key/values out of a capture's key/values and
// onto the scope as tags. Sentry groups and filters on tags only, so this is
// what makes per-command error rate answerable — the failure-side
// counterpart to the usage metrics, which deliberately carry no error signal
// (ADR 0011). Promotion is additive: every capture still records the same
// pairs in its extra context.
//
// Only these keys are promoted, not every string pair. Tags are a bounded
// dimension in Sentry the same way labels are in Prometheus, and captures
// routinely carry identifiers like guild_id and discord_id that have no
// business becoming one. A host has only a few recorders, so "recorder"
// stays small.
func promoteTags(scope *sentry.Scope, kv []any) {
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok || !tagKeys[key] {
			continue
		}
		if value, ok := kv[i+1].(string); ok && value != "" {
			scope.SetTag(key, value)
		}
	}
}

// CaptureError logs err at ERROR under msg with kv, and sends it to Sentry
// with msg as its "message" tag and kv in its extra context. Sentry groups
// the event by its stack trace.
func CaptureError(msg string, err error, kv ...any) {
	CaptureErrorIn(SentryIssue{}, msg, err, kv...)
}

// SentryIssue says which Sentry issue a capture's event lands in. The zero
// value leaves it to Sentry, which groups by the stack trace.
type SentryIssue struct {
	// Fingerprint names the issue. Events with the same fingerprint share
	// one, whatever their stack trace or text.
	Fingerprint []string
	// Warning sends the event at warning level rather than error. Sentry
	// gives an issue opened at warning level medium priority, so an alert
	// on high-priority issues skips it.
	Warning bool
}

// CaptureErrorIn is CaptureError with the event filed as issue says. A
// capture site that reports several kinds of failure needs it, since
// every event from one site has the same stack trace.
func CaptureErrorIn(issue SentryIssue, msg string, err error, kv ...any) {
	Logger.Error(msg, append([]any{"error", err}, kv...)...)

	if !sentry.CurrentHub().Client().IsEnabled() {
		return
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("message", msg)
		setExtras(scope, kv)
		promoteTags(scope, kv)
		if len(issue.Fingerprint) > 0 {
			scope.SetFingerprint(issue.Fingerprint)
		}
		if issue.Warning {
			scope.SetLevel(sentry.LevelWarning)
		}

		sentry.CaptureException(err)
	})
}

// RecoverPanic swallows a panic, logs it, and forwards it to Sentry the way
// ReportPanic does.
func RecoverPanic(ctx string, kv ...any) {
	r := recover()
	if r == nil {
		return
	}
	ReportPanic(ctx, r, kv...)
}

// ReportPanic logs a panic its caller has already recovered and forwards it
// to Sentry, for a caller that still has to act once the panic is caught,
// such as answer the request it ended. RecoverPanic recovers the panic
// itself and leaves its caller no way to learn of it. The event carries ctx
// as its "context" tag and kv's string-keyed pairs in its extra context, the
// way CaptureError's carries its pairs. The pairs of tagKeys also become tags.
func ReportPanic(ctx string, r any, kv ...any) {
	Error("panic recovered", append([]any{"context", ctx, "panic", r, "stack", string(debug.Stack())}, kv...)...)

	if !sentry.CurrentHub().Client().IsEnabled() {
		return
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("context", ctx)
		promoteTags(scope, kv)
		setExtras(scope, kv)
		sentry.CurrentHub().RecoverWithContext(context.Background(), r)
	})
	sentry.Flush(2 * time.Second)
}

// setExtras puts a capture's string-keyed key/values on the scope as its
// extra context, when there are any. It hands them straight to the scope, so
// an error's data a caller puts in kv goes to Sentry and nowhere else
// (ADR 0018).
func setExtras(scope *sentry.Scope, kv []any) {
	extras := make(map[string]any)
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		extras[key] = kv[i+1]
	}
	if len(extras) > 0 {
		scope.SetContext("extra", extras)
	}
}
