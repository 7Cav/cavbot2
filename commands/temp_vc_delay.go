package commands

import (
	"time"

	"github.com/7cav/cavbot2/utils"
)

// The delete delay (#372, CONTEXT.md). On a hub with one, a spawned channel
// that empties is not deleted at once: it waits, and once it has stayed
// empty for the delay the guarded delete runs, with its fresh cache check
// (#319) and its failure classification, as for a channel deleted the
// moment it empties. A join cancels the wait. The delay is read as the hub
// stands now, so a panel save re-times the waits already running. A delay
// of 0 deletes a channel the moment it empties. Nothing about a wait is
// stored: the restart sweep starts every empty channel's wait afresh.

// deleteWait is one spawned channel waiting out its hub's delete delay:
// when it emptied, and the stop of the timer that ends the wait.
type deleteWait struct {
	emptiedAt time.Time
	stop      func() bool
}

// deleteDelayLocked is the delete delay of a spawned channel's hub as the
// hub is now. A channel whose hub row is gone reads the default, 0 (#372
// Q9). A disabled or broken hub keeps its delay. Caller holds mu.
func (t *TempVC) deleteDelayLocked(channelID string) time.Duration {
	hub, ok := t.hubByIDLocked(t.channelHub[channelID])
	if !ok || hub.DeleteDelayMinutes <= 0 {
		return 0
	}
	return time.Duration(hub.DeleteDelayMinutes) * time.Minute
}

// waitStart is a wait just started, kept for its log line, which the caller
// writes once it has released the mutex.
type waitStart struct {
	channelID string
	hubID     int64
	delay     time.Duration
}

// log writes the wait's INFO line. userID is the member whose leave emptied
// the channel; the restart sweep passes none.
func (w waitStart) log(userID string) {
	kv := []any{"channel_id", w.channelID, "hub_id", w.hubID, "delete_delay_minutes", int(w.delay / time.Minute)}
	if userID != "" {
		kv = append(kv, "user_id", userID)
	}
	utils.Info("Temp VC delete delay started", kv...)
}

// startDeleteWaitLocked starts a channel's wait when it has just emptied on
// a hub with a delete delay. A delay of 0 starts nothing and reports false,
// and the caller deletes the channel at once. Caller holds mu.
func (t *TempVC) startDeleteWaitLocked(channelID string) (waitStart, bool) {
	delay := t.deleteDelayLocked(channelID)
	if delay == 0 {
		return waitStart{}, false
	}
	t.scheduleDeleteLocked(channelID, tempVCNow(), delay)
	return waitStart{channelID: channelID, hubID: t.channelHub[channelID], delay: delay}, true
}

// scheduleDeleteLocked times a channel's wait to end once it has been empty
// for delay, counted from emptiedAt. Caller holds mu.
func (t *TempVC) scheduleDeleteLocked(channelID string, emptiedAt time.Time, delay time.Duration) {
	w := &deleteWait{emptiedAt: emptiedAt}
	w.stop = tempVCAfterFunc(emptiedAt.Add(delay).Sub(tempVCNow()), func() { t.deleteWaitOver(channelID, w) })
	t.waits[channelID] = w
}

// retimeDeleteWaitsLocked times every waiting channel of a hub again
// against its delay as it stands now, counted from when each emptied. A
// channel already empty that long, or a delay of 0, fires at once, on the
// timer's goroutine: the caller, a panel save, never waits for the delete.
// A save that raises the delay lengthens the waits already running. Caller
// holds mu.
func (t *TempVC) retimeDeleteWaitsLocked(hubID int64) {
	for channelID, w := range t.waits {
		if t.channelHub[channelID] != hubID {
			continue
		}
		w.stop()
		t.scheduleDeleteLocked(channelID, w.emptiedAt, t.deleteDelayLocked(channelID))
	}
}

// cancelDeleteWaitLocked ends a channel's wait without a delete, and reports
// whether it had one. A join cancels the wait this way. Caller holds mu.
func (t *TempVC) cancelDeleteWaitLocked(channelID string) bool {
	w, ok := t.waits[channelID]
	if !ok {
		return false
	}
	w.stop()
	delete(t.waits, channelID)
	return true
}

// deleteWaitOver ends a channel's wait on the timer's goroutine and runs the
// guarded delete. A wait that is no longer the channel's own does nothing:
// its stop came too late to keep this call from starting, and the wait was
// cancelled or replaced while the call waited for the mutex.
func (t *TempVC) deleteWaitOver(channelID string, w *deleteWait) {
	defer utils.RecoverPanic("tempvc-delete-delay", "channel_id", channelID)
	t.mu.Lock()
	if t.waits[channelID] != w {
		t.mu.Unlock()
		return
	}
	delete(t.waits, channelID)
	t.mu.Unlock()
	t.deleteIfStillEmpty(channelID, "")
}
