// Package voice stands in for cavbot2's voice. It holds the handler that
// moves disgo's ERRORs down to WARN on a recorder's voice connection, which
// the check accepts, and a handler of the same shape that it doesn't.
package voice

import (
	"context"
	"log/slog"
	"os"
)

type errorsAsWarnings struct {
	next slog.Handler
}

func (h errorsAsWarnings) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, asWarning(level))
}

func (h errorsAsWarnings) Handle(ctx context.Context, r slog.Record) error {
	r.Level = asWarning(r.Level)
	return h.next.Handle(ctx, r)
}

func (h errorsAsWarnings) WithAttrs(attrs []slog.Attr) slog.Handler {
	return errorsAsWarnings{next: h.next.WithAttrs(attrs)}
}

func (h errorsAsWarnings) WithGroup(name string) slog.Handler {
	return errorsAsWarnings{next: h.next.WithGroup(name)}
}

func asWarning(level slog.Level) slog.Level {
	if level >= slog.LevelError {
		return slog.LevelWarn
	}
	return level
}

func ConnLogger() *slog.Logger {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	return slog.New(errorsAsWarnings{next: logger.Handler()})
}

// warningsOnly has the accepted handler's shape under another name.
type warningsOnly struct {
	next slog.Handler
}

func (h warningsOnly) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, asWarning(level))
}

func (h warningsOnly) Handle(ctx context.Context, r slog.Record) error {
	r.Level = asWarning(r.Level)
	return h.next.Handle(ctx, r)
}

func (h warningsOnly) WithAttrs(attrs []slog.Attr) slog.Handler {
	return warningsOnly{next: h.next.WithAttrs(attrs)} // want ""
}

func (h warningsOnly) WithGroup(name string) slog.Handler {
	return warningsOnly{next: h.next.WithGroup(name)} // want ""
}

func WarningsLogger() *slog.Logger {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	return slog.New(warningsOnly{next: logger.Handler()}) // want ""
}
