package commands

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// loasAnswer is the 7Cav API's answer to an LOA call.
type loasAnswer struct {
	loas *utils.LOAsResponse
	err  error
}

// loasCall is an LOA call running alongside the roster call.
type loasCall struct {
	cancel context.CancelFunc
	done   chan struct{}
	answer loasAnswer
}

// callLOAsAlongside starts call under a context of its own beneath ctx, so
// the LOA call runs alongside the roster call and adds no wait. The run
// that starts it defers stop.
func callLOAsAlongside(ctx context.Context, call func(context.Context) (*utils.LOAsResponse, error)) *loasCall {
	ctx, cancel := context.WithCancel(ctx)
	c := &loasCall{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		c.answer.loas, c.answer.err = call(ctx)
	}()
	return c
}

// wait returns the call's answer once it arrives.
func (c *loasCall) wait() loasAnswer {
	<-c.done
	return c.answer
}

// stop cancels the call and returns once it has ended, so a run that ends
// before it reads the answer leaves no call running past it.
func (c *loasCall) stop() {
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
