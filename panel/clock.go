package panel

import (
	"context"
	"time"
)

// clock is the time the panel reads and waits on: a session's lifetime, a
// page's time budget, a save's store deadlines and the waits for Discord's
// data. Lint holds the rest of the package to it, so the tests run the
// panel on a clock they move themselves and never wait out a budget.
type clock interface {
	// Now is the time.
	Now() time.Time
	// WithTimeout is context.WithTimeout on this clock.
	WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc)
	// After is time.After on this clock.
	After(d time.Duration) <-chan time.Time
}

// panelClock is the panel's clock, the wall clock outside the tests. A
// package var, the same arrangement as telemetryNow in the commands
// package.
var panelClock clock = wallClock{}

// wallClock is the clock the panel runs on outside the tests.
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() } //nolint:forbidigo // the wall clock itself

func (wallClock) WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d) //nolint:forbidigo // the wall clock itself
}

func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) } //nolint:forbidigo // the wall clock itself
