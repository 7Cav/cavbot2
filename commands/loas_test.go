package commands

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// startLOACallsLate makes each LOA call start d late, as when the scheduler
// starts the call's goroutine late. Call the returned outlived right after
// the run returns. It watches for 10×d and reports whether an LOA call
// returned after the run did.
func startLOACallsLate(t *testing.T, d time.Duration) (outlived func() bool) {
	t.Helper()
	var runReturned atomic.Bool
	late := make(chan struct{}, 1)
	callLate := func(call func() (*utils.LOAsResponse, error)) (*utils.LOAsResponse, error) {
		time.Sleep(d)
		defer func() {
			if runReturned.Load() {
				select {
				case late <- struct{}{}:
				default:
				}
			}
		}()
		return call()
	}

	prevLOAs, prevCurrentLOAs := getLOAs, getCurrentLOAs
	t.Cleanup(func() { getLOAs, getCurrentLOAs = prevLOAs, prevCurrentLOAs })
	getLOAs = func(ctx context.Context, endedSince time.Time) (*utils.LOAsResponse, error) {
		return callLate(func() (*utils.LOAsResponse, error) { return prevLOAs(ctx, endedSince) })
	}
	getCurrentLOAs = func(ctx context.Context) (*utils.LOAsResponse, error) {
		return callLate(func() (*utils.LOAsResponse, error) { return prevCurrentLOAs(ctx) })
	}

	return func() bool {
		runReturned.Store(true)
		select {
		case <-late:
			return true
		case <-time.After(10 * d):
			return false
		}
	}
}
