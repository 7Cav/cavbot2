package utils

import (
	"context"
	"os"
	"runtime/debug"
	"time"

	"github.com/getsentry/sentry-go"
)

// InitSentry configures the Sentry client; an empty SENTRY_DSN is a valid no-op (zero events, zero overhead).
func InitSentry(release string) func() {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		Info("Sentry disabled (SENTRY_DSN not set)")
		return func() {}
	}

	env := os.Getenv("APP_ENV")
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      env,
		Release:          release,
		TracesSampleRate: 1.0,
	})
	if err != nil {
		Warn("Sentry init failed, continuing without it", "error", err)
		return func() {}
	}

	Info("Sentry initialized", "environment", env, "release", release)
	return func() { sentry.Flush(2 * time.Second) }
}

// commandTagKey is the key that, wherever it appears in a capture's key/values,
// is promoted to a Sentry tag so failures are groupable by command.
const commandTagKey = "command"

// promoteCommandTag lifts a "command" key/value out of a capture's key/values
// and onto the scope as a tag. Sentry groups and filters on tags only, so this
// is what makes per-command error rate answerable — the failure-side
// counterpart to the usage metrics, which deliberately carry no error signal
// (ADR 0011). Promotion is additive: CaptureError still records the same pair
// in its extra context.
//
// Only "command" is promoted, not every string pair. Tags are a bounded
// dimension in Sentry the same way labels are in Prometheus, and captures
// routinely carry identifiers like guild_id and discord_id that have no
// business becoming one.
func promoteCommandTag(scope *sentry.Scope, kv []any) {
	for i := 0; i+1 < len(kv); i += 2 {
		if key, ok := kv[i].(string); !ok || key != commandTagKey {
			continue
		}
		if command, ok := kv[i+1].(string); ok && command != "" {
			scope.SetTag(commandTagKey, command)
		}
	}
}

func CaptureError(msg string, err error, kv ...any) {
	Logger.Error(msg, append([]any{"error", err}, kv...)...)

	if sentry.CurrentHub().Client() == nil {
		return
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("message", msg)

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
		promoteCommandTag(scope, kv)

		sentry.CaptureException(err)
	})
}

// RecoverPanic swallows a panic, logs it, and forwards it to Sentry. The
// variadic kv carries attribution for the event — currently the "command" pair
// the slash-command decorator supplies, which promoteCommandTag lifts to a tag.
func RecoverPanic(ctx string, kv ...any) {
	r := recover()
	if r == nil {
		return
	}

	Error("panic recovered", append([]any{"context", ctx, "panic", r, "stack", string(debug.Stack())}, kv...)...)

	if sentry.CurrentHub().Client() == nil {
		return
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("context", ctx)
		promoteCommandTag(scope, kv)
		sentry.CurrentHub().RecoverWithContext(context.Background(), r)
	})
	sentry.Flush(2 * time.Second)
}
