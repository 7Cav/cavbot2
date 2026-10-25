package commands

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// A stop's leave (#854). A recorder's leave can hang in the voice adapter,
// which never reads the leave's context. The stop gives up on it after
// recordingLeaveTimeout. Every case runs the /record scene in a synctest
// bubble with the runtime on the bubble's clock. The leave's deadline and
// the runtime's own timers then pass on one clock, and a test reads the time
// a stop takes off it.

// hungLeave is how long a hung leave takes to return: far past the bound.
const hungLeave = time.Hour

// The recording runtime's production clock, kept before any test swaps in
// the fake one.
var productionRecordingNow, productionRecordingAfterFunc = recordingNow, recordingAfterFunc

// newBubbleRecordScene is newRecordScene, called inside a synctest bubble,
// with the runtime on the bubble's clock from here on. The runtime was built
// on the fake clock, which nothing advances, so its startup retention sweep
// and recovery never run.
func newBubbleRecordScene(t *testing.T, recorders ...string) *recordScene {
	t.Helper()
	sc := newRecordScene(t, recorders...)
	prevNow, prevAfter := recordingNow, recordingAfterFunc
	recordingNow, recordingAfterFunc = productionRecordingNow, productionRecordingAfterFunc
	t.Cleanup(func() { recordingNow, recordingAfterFunc = prevNow, prevAfter })
	return sc
}

// leaveTimeouts is the WARN records of a leave that ran past its bound.
func leaveTimeouts(t *testing.T, logs *syncBuffer) []map[string]string {
	t.Helper()
	return logRecordsWith(t, logs, "WARN", map[string]string{"msg": leaveTimedOutMessage})
}

// A member running /record stop gets an answer within about 5 s when the
// recorder's leave hangs, and the leave logs one WARN naming it, counted
// once the hung leave has returned.
func TestRecordStopAnswersWithinTheLeaveBoundWhenTheLeaveHangs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		logs := captureLogs(t)
		sc.startIn(t, recStarter, recordChannel)
		sc.voice.setLeaveTakes(hungLeave)

		asked := time.Now()
		reply := recordAs(t, sc.rt, recStarter, "stop")
		answeredAfter := time.Since(asked)

		if !strings.HasPrefix(reply, "✅") {
			t.Errorf("reply = %q, want a ✅ verdict", reply)
		}
		if answeredAfter > recordingLeaveTimeout {
			t.Errorf("/record stop answered after %v, want within %v", answeredAfter, recordingLeaveTimeout)
		}
		time.Sleep(hungLeave)
		synctest.Wait()
		if got := leaveTimeouts(t, logs); len(got) != 1 {
			t.Errorf("leave timeout WARNs = %v, want one", got)
		}
	})
}

// A leave that returns inside its bound logs no leave timeout, read once
// /record stop has answered and the clock has passed the bound.
func TestRecordStopLeaveInTimeLogsNoTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		logs := captureLogs(t)
		sc.startIn(t, recStarter, recordChannel)
		sc.voice.setLeaveTakes(recordingLeaveTimeout - time.Millisecond)

		reply := recordAs(t, sc.rt, recStarter, "stop")

		if !strings.HasPrefix(reply, "✅") {
			t.Fatalf("reply = %q, want a ✅ verdict", reply)
		}
		time.Sleep(recordingLeaveTimeout)
		synctest.Wait()
		if got := leaveTimeouts(t, logs); len(got) != 0 {
			t.Errorf("leave timeout WARNs = %v, want none", got)
		}
	})
}

// A stop by the Stop button, the cap, low disk or the last person leaving
// finishes within about 5 s when the recorder's leave hangs: by then the
// mixer has been asked for the recording's mix, the stop's last step. A
// member speaks one frame first, so the recording has a track to mix. A
// cause runs on a goroutine of its own, so a stop held up by the leave can't
// hold up the test's look at the mixer. The stop falls due stopsAfter from
// the start.
func TestRecordingStopsFinishWithinTheLeaveBoundWhenTheLeaveHangs(t *testing.T) {
	for _, tc := range []struct {
		name string
		// cause stops the recording, or sets up what the clock stops it
		// for, and is nil when the clock alone does. button is the
		// recording notice's Stop button.
		cause      func(sc *recordScene, button string)
		stopsAfter time.Duration
	}{
		{
			name: "the Stop button",
			cause: func(sc *recordScene, button string) {
				runRecord(&fakeResponder{}, sc.rt, pressInteraction(button, recStarter))
			},
		},
		{name: "the cap", stopsAfter: recordingCap},
		{
			name:       "free disk space below the threshold",
			cause:      func(sc *recordScene, _ string) { sc.freeDisk.Store(recordingMinFreeDisk - 1) },
			stopsAfter: recordingDiskCheckInterval,
		},
		{
			name:  "the last person leaves",
			cause: func(sc *recordScene, _ string) { sc.deliver(recStarter.id, "") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sc := newBubbleRecordScene(t, testRecorder)
				sc.startIn(t, recStarter, recordChannel)
				button := stopButton(t, recordingNotice(t, sc.fake, recordChannel))
				sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
				sc.voice.setLeaveTakes(hungLeave)

				if tc.cause != nil {
					go tc.cause(sc, button)
				}
				time.Sleep(tc.stopsAfter + recordingLeaveTimeout)
				synctest.Wait()

				if got := sc.mixer.mixes(); got != 1 {
					t.Errorf("mixes asked for %v after the stop fell due = %d, want 1", recordingLeaveTimeout, got)
				}
				time.Sleep(hungLeave)
			})
		})
	}
}

// A stop whose leave hangs keeps its recorder busy until the leave returns,
// since the voice adapter can't join a recorder whose connection it is
// still closing. A start meanwhile takes the other recorder, and once the
// leave returns the first one records again.
func TestRecordStopWhoseLeaveHangsKeepsItsRecorderUntilTheLeaveReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder, testSecondRecorder)
		sc.startIn(t, recStarter, recordChannel)
		sc.voice.setLeaveTakes(hungLeave)
		recordAs(t, sc.rt, recStarter, "stop")
		sc.voice.setLeaveTakes(0)

		sc.fake.setVoice(recHolder.id, recordOtherChannel)
		if reply := recordAs(t, sc.rt, recHolder, "start"); !strings.HasPrefix(reply, "✅") {
			t.Errorf("start in %s while the leave hangs = %q, want a ✅ verdict", recordOtherChannel, reply)
		}
		time.Sleep(hungLeave)
		synctest.Wait()
		if reply := recordAs(t, sc.rt, recStarter, "start"); !strings.HasPrefix(reply, "✅") {
			t.Errorf("start in %s once the leave returned = %q, want a ✅ verdict", recordChannel, reply)
		}
	})
}
