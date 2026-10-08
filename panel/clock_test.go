package panel

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// testClock stands in for the panel's clock in every test world. It moves
// only when the panel waits on it, by the length of the wait, or when a
// test moves it. So a page that waits out its time budget answers at once,
// with the clock moved on by the budget, and a test reads from the clock
// how long the panel waited.
type testClock struct {
	mu        sync.Mutex
	now       time.Time
	deadlines []*clockDeadline
}

// installClock puts a test clock under the panel for the rest of the test,
// or returns the one already there.
func installClock(t *testing.T) *testClock {
	t.Helper()
	if c, ok := panelClock.(*testClock); ok {
		return c
	}
	c := &testClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	prev := panelClock
	panelClock = c
	t.Cleanup(func() { panelClock = prev })
	return c
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// WithTimeout gives ctx a deadline d from now on this clock. It ends with
// context.DeadlineExceeded once the clock reaches it, or with ctx's error
// when ctx ends first.
func (c *testClock) WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	c.mu.Lock()
	dl := &clockDeadline{Context: ctx, deadline: c.now.Add(d), done: make(chan struct{})}
	if parent, ok := ctx.Deadline(); ok && parent.Before(dl.deadline) {
		dl.deadline = parent
	}
	due := !dl.deadline.After(c.now)
	if !due {
		c.deadlines = append(c.deadlines, dl)
	}
	c.mu.Unlock()
	if due {
		dl.end(context.DeadlineExceeded)
	}
	stop := context.AfterFunc(ctx, func() { dl.end(ctx.Err()) })
	return dl, func() {
		stop()
		dl.end(context.Canceled)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.deadlines = slices.DeleteFunc(c.deadlines, func(other *clockDeadline) bool { return other == dl })
	}
}

// After moves the clock on by d, ending every deadline it reaches, and
// returns a channel that already holds the time it moved to.
func (c *testClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.advance(d)
	return ch
}

// advance moves the clock on by d, ends every deadline it reaches, and
// returns the time it moved to.
func (c *testClock) advance(d time.Duration) time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	at := c.now
	var due []*clockDeadline
	c.deadlines = slices.DeleteFunc(c.deadlines, func(dl *clockDeadline) bool {
		if dl.deadline.After(at) {
			return false
		}
		due = append(due, dl)
		return true
	})
	c.mu.Unlock()
	for _, dl := range due {
		dl.end(context.DeadlineExceeded)
	}
	return at
}

// advanceTo moves the clock on to at, or leaves it where it is when it is
// already there or past it.
func (c *testClock) advanceTo(at time.Time) {
	c.advance(max(0, at.Sub(c.Now())))
}

// since is how far the clock has moved on from start.
func (c *testClock) since(start time.Time) time.Duration {
	return c.Now().Sub(start)
}

// clockDeadline is a context whose deadline falls on a test clock.
type clockDeadline struct {
	context.Context
	deadline time.Time
	done     chan struct{}
	mu       sync.Mutex
	err      error
}

func (c *clockDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func (c *clockDeadline) Done() <-chan struct{} { return c.done }

func (c *clockDeadline) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// end ends the context with err, once, whoever calls it first.
func (c *clockDeadline) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		close(c.done)
	}
}
