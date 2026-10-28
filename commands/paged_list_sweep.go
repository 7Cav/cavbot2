package commands

import (
	"context"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The sweep of saved list pages (#824, ADR 0020). A long /loa or /awol
// list's pages last pagedListRetention from the run. The sweep deletes the
// older ones when the bot starts and every pagedListSweepInterval after, on
// every host with a bot store. A press reads a list 30 days old as expired
// whether or not the sweep has reached it yet.

// pagedListSweepInterval is how often the sweep runs.
const pagedListSweepInterval = time.Hour

// pagedListNow is the sweep's clock, and pagedListAfterFunc schedules its
// next pass, package vars as recordingNow and recordingAfterFunc are, so
// tests run the sweep on the fake clock.
var (
	pagedListNow       = time.Now
	pagedListAfterFunc = func(d time.Duration, f func()) (stop func()) {
		timer := time.AfterFunc(d, f)
		return func() { timer.Stop() }
	}
)

// StartPagedListSweep starts the sweep of st's saved list pages.
func StartPagedListSweep(st store.Store) {
	pagedListAfterFunc(0, func() { sweepPagedLists(st) })
}

// sweepPagedLists deletes every list run more than pagedListRetention ago,
// then schedules the next pass, even after a panic. A failed delete goes to
// Sentry, and the next pass tries again.
func sweepPagedLists(st store.Store) {
	defer func() { pagedListAfterFunc(pagedListSweepInterval, func() { sweepPagedLists(st) }) }()
	defer utils.RecoverPanic("paged-list-sweep")
	ctx, cancel := context.WithTimeout(context.Background(), pagedListStoreTimeout)
	defer cancel()
	if err := st.DeletePagedListsBefore(ctx, pagedListNow().Add(-pagedListRetention)); err != nil {
		captureError("Saved list pages not swept", err)
	}
}
