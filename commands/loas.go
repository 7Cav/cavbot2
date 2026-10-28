package commands

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// getLOAs and getCurrentLOAs are the 7Cav API's LOA calls /awol and /loa
// make. Tests wrap them to start an LOA call late.
var (
	getLOAs        = utils.GetLOAs
	getCurrentLOAs = utils.GetCurrentLOAs
)

// loasAnswer is the 7Cav API's answer to an LOA call.
type loasAnswer struct {
	loas *utils.LOAsResponse
	err  error
}

// loaCallAlongside is an LOA call running alongside the roster call.
type loaCallAlongside struct {
	cancel context.CancelFunc
	done   chan struct{}
	answer loasAnswer
}

// callLOAsAlongside starts call under a context drawn from ctx, so the LOA
// call runs alongside the roster call and adds no wait. The run that starts
// it defers stop.
func callLOAsAlongside(ctx context.Context, call func(context.Context) (*utils.LOAsResponse, error)) *loaCallAlongside {
	ctx, cancel := context.WithCancel(ctx)
	c := &loaCallAlongside{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		c.answer.loas, c.answer.err = call(ctx)
	}()
	return c
}

// wait returns the call's answer once the call has returned.
func (c *loaCallAlongside) wait() loasAnswer {
	<-c.done
	return c.answer
}

// stop cancels the call and returns once it has returned. A run that ends
// without the call's answer leaves no call behind, and a slow LOA endpoint
// doesn't hold it up.
func (c *loaCallAlongside) stop() {
	c.cancel()
	<-c.done
}

// loaThreadID reads an LOA's thread ID. One that doesn't parse reads as 0,
// so the LOA shows with no thread link.
func loaThreadID(l utils.LOA) int64 {
	threadID, _ := strconv.ParseInt(l.ThreadID, 10, 64)
	return threadID
}

// loaThreadURL is the forum address of an LOA's thread.
func loaThreadURL(threadID int64) string {
	return fmt.Sprintf("https://7cav.us/threads/%d/", threadID)
}

// loaEntryOf reads an LOA's dates and thread, reporting false with a
// warning when its dates don't read. A thread ID that doesn't parse leaves
// ThreadID 0, so the LOA shows with no thread link.
func loaEntryOf(l utils.LOA) (utils.LOAEntry, bool) {
	start, startErr := time.Parse(time.DateOnly, l.StartDate)
	end, endErr := time.Parse(time.DateOnly, l.EndDate)
	if startErr != nil || endErr != nil {
		utils.Warn("LOA skipped: dates unreadable",
			"thread_id", l.ThreadID, "start", l.StartDate, "end", l.EndDate)
		return utils.LOAEntry{}, false
	}
	return utils.LOAEntry{
		Username:  l.Subject.Username,
		StartDate: start,
		EndDate:   end,
		ThreadID:  loaThreadID(l),
	}, true
}
