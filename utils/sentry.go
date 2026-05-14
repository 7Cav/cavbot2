package utils

import (
	"context"
	"os"
	"runtime/debug"
	"time"

	"github.com/getsentry/sentry-go"
)

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

func CaptureError(msg string, err error, kv ...any) {
	Logger.Error(msg, append([]any{"error", err}, kv...)...)

	if sentry.CurrentHub().Client() == nil {
		return
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("message", msg)

		extras := make(map[string]any)
		for i := 0; i+1 < len(kv); i += 2 {
			if key, ok := kv[i].(string); ok {
				extras[key] = kv[i+1]
			}
		}
		if len(extras) > 0 {
			scope.SetContext("extra", extras)
		}

		sentry.CaptureException(err)
	})
}

func RecoverPanic(ctx string) {
	r := recover()
	if r == nil {
		return
	}

	Error("panic recovered", "context", ctx, "panic", r, "stack", string(debug.Stack()))

	if sentry.CurrentHub().Client() == nil {
		return
	}

	sentry.CurrentHub().RecoverWithContext(context.TODO(), r)
	sentry.Flush(2 * time.Second)
}
