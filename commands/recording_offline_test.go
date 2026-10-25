package commands

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/voice"
)

// Offline recorders (#861, GLOSSARY.md Offline recorder): a recorder a gap
// cut short logs out and back in, and a start can take one that is still
// logging back in and wait for it. Every case runs the runtime over the
// /record scene in a synctest bubble, so a start that waits on the fake
// voice adapter waits on the bubble's clock.

// A start takes a free online recorder before an offline one, though the
// offline one comes first in the configured order, so the starter doesn't
// wait when they don't have to.
func TestRecordStartTakesAnOnlineRecorderBeforeAnOfflineOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder, testSecondRecorder)
		sc.voice.setState(testRecorder, voice.RecorderOffline)
		sc.fake.setVoice(recStarter.id, recordChannel)

		reply := recordAs(t, sc.rt, recStarter, "start")

		if !strings.HasPrefix(reply, "✅") {
			t.Errorf("reply = %q, want a ✅ verdict", reply)
		}
		if got := sc.voice.inChannel(recordChannel); !slices.Equal(got, []string{testSecondRecorder}) {
			t.Errorf("recorders in %s = %v, want [%s]", recordChannel, got, testSecondRecorder)
		}
	})
}

// A start that finds no free online recorder takes a free offline one and
// waits for it to log back in, then records.
func TestRecordStartTakesAnOfflineRecorderWhenNoOnlineOneIsFree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.voice.setState(testRecorder, voice.RecorderOffline)
		sc.fake.setVoice(recStarter.id, recordChannel)
		go func() {
			time.Sleep(3 * time.Second)
			sc.voice.setState(testRecorder, voice.RecorderOnline)
		}()

		reply := recordAs(t, sc.rt, recStarter, "start")

		if !strings.HasPrefix(reply, "✅") {
			t.Errorf("reply = %q, want a ✅ verdict", reply)
		}
		if got := sc.voice.inChannel(recordChannel); !slices.Equal(got, []string{testRecorder}) {
			t.Errorf("recorders in %s = %v, want [%s]", recordChannel, got, testRecorder)
		}
	})
}

// A start whose offline recorder doesn't log back in within the start's
// 30 s is refused as the recorder coming back online, which tells the
// starter to try again in a minute. Sentry hears nothing, since a recorder
// logging back in isn't a fault, no row is written, and the recorder is
// free for the next start.
func TestRecordStartThatWaitsOutAnOfflineRecorderIsRefused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		captures := countCaptures(t)
		sc.voice.setState(testRecorder, voice.RecorderOffline)
		sc.fake.setVoice(recStarter.id, recordChannel)

		asked := time.Now()
		reply := recordAs(t, sc.rt, recStarter, "start")

		if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, recorderComingBackRefusal) {
			t.Errorf("reply = %q, want a ❌ verdict carrying %q", reply, recorderComingBackRefusal)
		}
		if waited := time.Since(asked); waited != recordingJoinTimeout {
			t.Errorf("start answered after %v, want after its %v", waited, recordingJoinTimeout)
		}
		if *captures != 0 {
			t.Errorf("Sentry captures = %d, want none", *captures)
		}
		if recs := sc.recordings(t); len(recs) != 0 {
			t.Errorf("recordings after the refused start = %+v, want none", recs)
		}
		sc.voice.setState(testRecorder, voice.RecorderOnline)
		if reply := recordAs(t, sc.rt, recStarter, "start"); !strings.HasPrefix(reply, "✅") {
			t.Errorf("next start once the recorder is online = %q, want a ✅ verdict", reply)
		}
	})
}

// A starter who answers the ping at the 10 s mark, while the cut-short
// recording's leave hangs, isn't refused as already recorded: the start
// takes the one recorder, waits for it to log back in, and gets ✅ within
// its 30 s. Once the hung leave returns, the cut-short recording's cleanup
// has left the new recording alone: its starter stops it, and its row
// closes.
func TestRecordStartAtTheMarkWaitsForTheSameRecorder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.startIn(t, recStarter, recordChannel)
		sc.voice.setLeaveTakes(hungLeave)
		// A failure before the end still lets the hung leave return, so the
		// bubble can exit.
		t.Cleanup(func() { time.Sleep(hungLeave) })
		sc.listener(t, recordChannel).GapStarted(time.Now(), voice.GapVoiceGatewayClosed)
		time.Sleep(gapCutsShortAfter)
		synctest.Wait()

		if reply := recordAs(t, sc.rt, recStarter, "start"); !strings.HasPrefix(reply, "✅") {
			t.Fatalf("start at the mark = %q, want a ✅ verdict", reply)
		}

		time.Sleep(hungLeave)
		synctest.Wait()
		sc.voice.setLeaveTakes(0)

		if reply := recordAs(t, sc.rt, recStarter, "stop"); !strings.HasPrefix(reply, "✅") {
			t.Errorf("stop of the new recording once the hung leave returned = %q, want a ✅ verdict", reply)
		}
		var open []store.Recording
		for _, rec := range sc.recordings(t) {
			if rec.StoppedAt.IsZero() {
				open = append(open, rec)
			}
		}
		if len(open) != 0 {
			t.Errorf("recordings still open after the new one's stop = %+v, want none", open)
		}
	})
}

// The runtime has a cut-short recorder log back in only once its leave of
// the recording's channel has returned, here 2 s after the 10 s mark, so
// the logout never lands while the recorder is still in the call.
func TestRecordingCutShortLogsTheRecorderBackInAfterItsLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.startIn(t, recStarter, recordChannel)
		sc.voice.setLeaveTakes(2 * time.Second)
		sc.listener(t, recordChannel).GapStarted(time.Now(), voice.GapVoiceGatewayClosed)

		time.Sleep(gapCutsShortAfter + recordingLeaveTimeout)
		synctest.Wait()

		relogins := sc.voice.reloginsOf(testRecorder)
		if len(relogins) == 0 {
			t.Fatal("the cut-short recorder was never logged back in")
		}
		for _, rl := range relogins {
			if !rl.afterLeave {
				t.Error("the cut-short recorder was logged back in before its leave returned")
			}
		}
	})
}
