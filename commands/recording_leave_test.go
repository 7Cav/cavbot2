package commands

import (
	"bytes"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/7cav/cavbot2/voice"
	"github.com/bwmarrin/discordgo"
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

// slowLeave is how long a slow leave takes to return: inside the bound.
const slowLeave = 3 * time.Second

// A /record stop keeps the moment the member ran it as the recording's stop
// time, however long the recorder's leave takes (#889).
func TestRecordStopKeepsTheMomentTheMemberRanIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		leaveTakes time.Duration
	}{
		{"a slow leave", slowLeave},
		{"a hung leave", hungLeave},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sc := newBubbleRecordScene(t, testRecorder)
				sc.startIn(t, recStarter, recordChannel)
				time.Sleep(time.Minute)
				sc.voice.setLeaveTakes(tc.leaveTakes)
				t.Cleanup(func() { time.Sleep(tc.leaveTakes) })

				asked := time.Now()
				recordAs(t, sc.rt, recStarter, "stop")

				if rec := sc.onlyRecording(t); !rec.StoppedAt.Equal(asked) {
					t.Errorf("recording stopped at %v, want the moment the member ran /record stop, %v", rec.StoppedAt, asked)
				}
			})
		})
	}
}

// A /record stop ends every track at the stop, however long the recorder's
// leave takes: a frame the recorder hands over during the leave, after the
// stop, is in no track (#889).
func TestRecordStopKeepsNothingHeardDuringTheLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.startIn(t, recStarter, recordChannel)
		l := sc.listener(t, recordChannel)
		time.Sleep(30 * time.Second)
		l.Frame(frame(speakerA, 11, 5000, "a1"))
		time.Sleep(30 * time.Second)
		sc.voice.setLeaveTakes(slowLeave)

		go recordAs(t, sc.rt, recStarter, "stop")
		time.Sleep(2 * time.Second)
		l.Frame(frame(speakerA, 11, uint32(5000+samplesIn(32*time.Second)), "late"))
		time.Sleep(slowLeave)
		synctest.Wait()

		// The stop comes a minute in.
		stopSamples := uint64(samplesIn(time.Minute))
		tracks := sc.tracks(t)
		if len(tracks) != 1 {
			t.Fatalf("tracks = %d, want the speaker's one", len(tracks))
		}
		if tracks[0].granule != stopSamples {
			t.Errorf("track ends at sample %d, want the stop, %d", tracks[0].granule, stopSamples)
		}
		for _, p := range tracks[0].packets {
			if bytes.Equal(p, markerFrame("late")) {
				t.Errorf("track holds the frame handed over during the leave, after the stop")
			}
		}
	})
}

// A stop by the Stop button, the cap, low disk or the last person leaving
// keeps the moment it took the recording as the stop time, however long
// the recorder's leave takes (#889). The stop falls due stopsAfter from the
// start.
func TestRecordingStopsKeepTheirOwnStopTimeWhenTheLeaveIsSlow(t *testing.T) {
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
				stopsAt := time.Now().Add(tc.stopsAfter)
				button := stopButton(t, recordingNotice(t, sc.fake, recordChannel))
				sc.voice.setLeaveTakes(slowLeave)

				if tc.cause != nil {
					tc.cause(sc, button)
				}
				time.Sleep(tc.stopsAfter + slowLeave)
				synctest.Wait()

				if rec := sc.onlyRecording(t); !rec.StoppedAt.Equal(stopsAt) {
					t.Errorf("recording stopped at %v, want the moment the stop took it, %v", rec.StoppedAt, stopsAt)
				}
			})
		})
	}
}

// One channel check that stops two recordings stops both at the moment of
// the check, though the second stop waits on the first recorder's slow
// leave, and keeps in no track what either recorder hands over after it
// (#889). The check runs late on the first person's leave, by when the
// cache shows the second person gone too, as discordgo's cache can when two
// voice events come close together. Which recording leaves first is up to
// the check, so both recorders hand over a frame during the first leave.
func TestRecordingChannelCheckStopsEveryRecordingItTakesAtTheCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder, testSecondRecorder)
		sc.startIn(t, recStarter, recordChannel)
		sc.startIn(t, recHolder, recordOtherChannel)
		time.Sleep(time.Minute)
		listeners := []voice.Listener{sc.listener(t, recordChannel), sc.listener(t, recordOtherChannel)}
		sc.voice.setLeaveTakes(slowLeave)

		checkedAt := time.Now()
		sc.fake.setVoice(recHolder.id, "")
		go sc.deliver(recStarter.id, "")
		time.Sleep(2 * time.Second)
		for _, l := range listeners {
			l.Frame(frame(speakerA, 11, 5000, "late"))
		}
		time.Sleep(2 * slowLeave)
		synctest.Wait()

		for _, tr := range sc.tracks(t) {
			for _, p := range tr.packets {
				if bytes.Equal(p, markerFrame("late")) {
					t.Errorf("%s holds the frame handed over during the first leave, after the check", tr.path)
				}
			}
		}
		recs := sc.recordings(t)
		if len(recs) != 2 {
			t.Fatalf("recordings = %d, want the two", len(recs))
		}
		for _, rec := range recs {
			if !rec.StoppedAt.Equal(checkedAt) {
				t.Errorf("recording of %s stopped at %v, want the moment of the check, %v", rec.ChannelID, rec.StoppedAt, checkedAt)
			}
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

// hangLeaves makes every leave from now on hang. Before the bubble ends,
// the runtime shuts down, so no recording a test left running fires a timer
// once the scene is gone, and the hung leaves return.
func (sc *recordScene) hangLeaves(t *testing.T) {
	t.Helper()
	sc.voice.setLeaveTakes(hungLeave)
	t.Cleanup(func() {
		sc.rt.Shutdown()
		time.Sleep(hungLeave)
	})
}

// With two recorders, a stop whose leave hangs leaves its channel free for
// the other recorder while the first one is still logging back in (#888): a
// start in the stopped channel records with the second.
func TestRecordStartInAChannelWhoseStopsLeaveHangsTakesAFreeRecorder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder, testSecondRecorder)
		sc.startIn(t, recStarter, recordChannel)
		sc.hangLeaves(t)
		sc.voice.staysOffline = true
		recordAs(t, sc.rt, recStarter, "stop")
		sc.voice.setLeaveTakes(0)

		if reply := recordAs(t, sc.rt, recStarter, "start"); !strings.HasPrefix(reply, "✅") {
			t.Fatalf("start in %s while its stopped recording's leave hangs = %q, want a ✅ verdict", recordChannel, reply)
		}
		var open []string
		for _, rec := range sc.recordings(t) {
			if rec.StoppedAt.IsZero() {
				open = append(open, rec.RecorderID)
			}
		}
		if !slices.Equal(open, []string{testSecondRecorder}) {
			t.Errorf("recorders of the running recordings = %v, want [%s]", open, testSecondRecorder)
		}
	})
}

// With one recorder, a start after a stop whose leave hangs waits for the
// recorder's relogin, and when the relogin is still running at the end of
// the start's 30 s, the starter is told the recorder is coming back (#888).
func TestRecordStartAfterAStopWhoseLeaveHangsWaitsOutTheRelogin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.startIn(t, recStarter, recordChannel)
		sc.hangLeaves(t)
		sc.voice.staysOffline = true
		recordAs(t, sc.rt, recStarter, "stop")

		reply := recordAs(t, sc.rt, recStarter, "start")

		if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, recorderComingBackRefusal) {
			t.Errorf("reply = %q, want a ❌ verdict carrying %q", reply, recorderComingBackRefusal)
		}
	})
}

// A hung leave that returns once a new recording has its recorder leaves
// that recording running (#888): its starter stops it, and no row is left
// open.
func TestRecordStopsHungLeaveReturningLeavesTheNextRecordingRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.startIn(t, recStarter, recordChannel)
		sc.hangLeaves(t)
		recordAs(t, sc.rt, recStarter, "stop")
		sc.voice.setLeaveTakes(0)
		if reply := recordAs(t, sc.rt, recStarter, "start"); !strings.HasPrefix(reply, "✅") {
			t.Fatalf("start while the leave hangs = %q, want a ✅ verdict", reply)
		}

		time.Sleep(hungLeave)
		synctest.Wait()

		if reply := recordAs(t, sc.rt, recStarter, "stop"); !strings.HasPrefix(reply, "✅") {
			t.Errorf("stop of the new recording once the hung leave returned = %q, want a ✅ verdict", reply)
		}
		for _, rec := range sc.recordings(t) {
			if rec.StoppedAt.IsZero() {
				t.Errorf("recording %d still open after the new one's stop", rec.ID)
			}
		}
	})
}

// A stop whose leave is slow but in time frees its recorder with no relogin
// (#888), and the next start records.
func TestRecordStopWhoseLeaveIsInTimeAsksForNoRelogin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.startIn(t, recStarter, recordChannel)
		sc.voice.setLeaveTakes(slowLeave)
		recordAs(t, sc.rt, recStarter, "stop")

		if reply := recordAs(t, sc.rt, recStarter, "start"); !strings.HasPrefix(reply, "✅") {
			t.Errorf("start after the stop = %q, want a ✅ verdict", reply)
		}
		if got := sc.voice.reloginsOf(testRecorder); len(got) != 0 {
			t.Errorf("relogins of the recorder = %d, want none", len(got))
		}
	})
}

// A stop whose leave hangs frees its channel and its recorder once
// /record stop has answered (#888). The recorder goes offline and logs back
// in, as after a cut short, since the voice adapter would hand its next join
// on the old login the connection it is still closing. A start in the
// stopped channel, or in another, waits for the new login and records,
// while the leave still hangs.
func TestRecordStartAfterAStopWhoseLeaveHangsRecords(t *testing.T) {
	for _, channelID := range []string{recordChannel, recordOtherChannel} {
		t.Run(channelID, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sc := newBubbleRecordScene(t, testRecorder)
				sc.startIn(t, recStarter, recordChannel)
				sc.hangLeaves(t)
				if reply := recordAs(t, sc.rt, recStarter, "stop"); !strings.HasPrefix(reply, "✅") {
					t.Fatalf("stop = %q, want a ✅ verdict", reply)
				}
				sc.voice.setLeaveTakes(0)

				sc.fake.setVoice(recHolder.id, channelID)
				if reply := recordAs(t, sc.rt, recHolder, "start"); !strings.HasPrefix(reply, "✅") {
					t.Errorf("start in %s while the stopped recording's leave hangs = %q, want a ✅ verdict", channelID, reply)
				}
				if got := sc.voice.reloginsOf(testRecorder); len(got) != 1 {
					t.Errorf("relogins of the recorder = %d, want one once its leave gave up", len(got))
				}
			})
		})
	}
}

// A stop by the Stop button, the cap, low disk or the last person leaving
// frees its recorder when its leave hangs, as /record stop's does (#888):
// a start in the channel records while the leave still hangs. The stop
// falls due stopsAfter from the start.
func TestRecordingStopsWhoseLeaveHangsFreeTheirRecorder(t *testing.T) {
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
				sc.hangLeaves(t)

				if tc.cause != nil {
					go tc.cause(sc, button)
				}
				time.Sleep(tc.stopsAfter + recordingLeaveTimeout)
				synctest.Wait()
				sc.voice.setLeaveTakes(0)
				sc.freeDisk.Store(1 << 40)

				sc.fake.setVoice(recHolder.id, recordChannel)
				if reply := recordAs(t, sc.rt, recHolder, "start"); !strings.HasPrefix(reply, "✅") {
					t.Errorf("start while the stopped recording's leave hangs = %q, want a ✅ verdict", reply)
				}
			})
		})
	}
}

// A start that fails after its recorder joined, because the deafen wasn't
// lifted, the notice didn't post or the row wasn't written, frees its
// recorder when the leave hangs (#888): once the cause is gone, the next
// start records while the leave still hangs.
func TestRecordStartThatFailsAfterItsJoinFreesItsRecorderWhenTheLeaveHangs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fail, repair func(sc *recordScene)
	}{
		{
			name: "the deafen not lifted",
			fail: func(sc *recordScene) {
				sc.voice.joinDeafened()
				sc.fake.memberDeafenErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)
			},
			repair: func(sc *recordScene) { sc.voice.undeafen(testRecorder) },
		},
		{
			name: "the notice not posted",
			fail: func(sc *recordScene) {
				sc.fake.messageErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)
			},
			repair: func(sc *recordScene) { sc.fake.messageErr = nil },
		},
		{
			name:   "the row not written",
			fail:   func(sc *recordScene) { sc.faults.startRecordingErr = errors.New("connection refused") },
			repair: func(sc *recordScene) { sc.faults.startRecordingErr = nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sc := newBubbleRecordScene(t, testRecorder)
				sc.hangLeaves(t)
				sc.fake.setVoice(recStarter.id, recordChannel)
				tc.fail(sc)
				if reply := recordAs(t, sc.rt, recStarter, "start"); reply != recordFailedReply {
					t.Fatalf("failing start = %q, want %q", reply, recordFailedReply)
				}
				tc.repair(sc)
				sc.voice.setLeaveTakes(0)

				if reply := recordAs(t, sc.rt, recStarter, "start"); !strings.HasPrefix(reply, "✅") {
					t.Errorf("next start while the failed start's leave hangs = %q, want a ✅ verdict", reply)
				}
			})
		})
	}
}
