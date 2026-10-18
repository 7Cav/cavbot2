package commands

import (
	"strconv"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// loasAnswer is the 7Cav API's answer to an LOA call.
type loasAnswer struct {
	loas *utils.LOAsResponse
	err  error
}

// callLOAsAlongside starts call and returns the channel its answer arrives
// on, so the LOA call runs alongside the roster call and adds no wait. The
// channel is buffered, so the call can finish after a run that ends early.
func callLOAsAlongside(call func() (*utils.LOAsResponse, error)) <-chan loasAnswer {
	answer := make(chan loasAnswer, 1)
	go func() {
		loas, err := call()
		answer <- loasAnswer{loas, err}
	}()
	return answer
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
	threadID, _ := strconv.ParseInt(l.ThreadID, 10, 64)
	return utils.LOAEntry{
		Username:  l.Subject.Username,
		StartDate: start,
		EndDate:   end,
		ThreadID:  threadID,
	}, true
}
