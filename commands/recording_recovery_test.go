package commands

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// Cut-short recordings (#393). A recording the bot was in the middle of when
// it went down is closed as cut short by the next process: what reached the
// disk is kept, and the notice, the mix and the recorder are seen to as at a
// stop. Every fixture comes from the runtime itself. A crash is a fresh fake
// clock at the same instant, so the old runtime's timers never fire again and
// what its tracks hold in memory never reaches the disk, and a second runtime
// over the same store and recordings directory is the next process. Its
// startup recovery runs at the clock's next advance.

// crash ends the scene's process the way a crash does: nothing it scheduled
// runs again.
func (sc *recordScene) crash(t *testing.T) {
	t.Helper()
	now := sc.clock.read()
	sc.clock = installFakeClock(t)
	sc.clock.mu.Lock()
	sc.clock.now = now
	sc.clock.mu.Unlock()
}

// restart crashes the scene's process and starts the next one over the same
// store and recordings directory, with the recorders given. Its recovery
// runs at the clock's next advance.
func (sc *recordScene) restart(t *testing.T, recorders ...string) {
	t.Helper()
	sc.crash(t)
	sc.voice = &fakeRecorderVoice{recorders: recorders}
	sc.rt = NewRecordingRuntime(sc.fake, sc.faults, sc.tv, testTempVCGuild, sc.voice, sc.mixer, sc.dir, testPanelURL)
}

// assertCutShort checks that the store's one recording is closed as cut
// short.
func (sc *recordScene) assertCutShort(t *testing.T) {
	t.Helper()
	recs := sc.recordings(t)
	if len(recs) != 1 || recs[0].StoppedAt.IsZero() || recs[0].Ended != store.RecordingEndCutShort {
		t.Errorf("recordings after recovery = %+v, want one stopped, ended %q", recs, store.RecordingEndCutShort)
	}
}

// startCrashFixture starts a recording in which one speaker is heard before
// a flush, and another only after it, so a crash then leaves the first on
// disk and the second not.
func (sc *recordScene) startCrashFixture(t *testing.T) {
	t.Helper()
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(trackFlushInterval)
	sc.hear(t, recordChannel, frame(speakerB, 22, 90000, "b1"))
}

// A crash keeps what reached the disk: the next process closes the
// recording as cut short, and the track of a speaker heard before the last
// flush holds what they said. A speaker first heard after it has nothing on
// disk, and every track recovery keeps holds audio, since ffmpeg won't mix
// a track that holds none.
func TestRecordingCutShortByACrashKeepsWhatReachedTheDisk(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startCrashFixture(t)

	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	sc.assertCutShort(t)
	tracks := sc.tracks(t)
	for _, tr := range tracks {
		if len(tr.packets) == 0 {
			t.Errorf("%s holds no audio", tr.path)
		}
	}
	trackWith(t, tracks, "a1")
}

// A shutdown keeps everything captured up to it: a frame heard after the
// last flush is in its speaker's track once the next process recovers the
// recording.
func TestRecordingShutdownKeepsFramesHeardAfterTheLastFlush(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(trackFlushInterval)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000+opusFrameSamples, "a2"))

	sc.rt.Shutdown()
	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	trackWith(t, sc.tracks(t), "a2")
}

// A recording cut short by a shutdown stopped when the shutdown came, not
// when the next process got round to it: its stop time is where its
// captured audio ends.
func TestRecordingCutShortStopsWhenTheShutdownCame(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(10 * time.Second)
	shutdownAt := sc.clock.read()
	sc.rt.Shutdown()

	sc.crash(t)
	sc.clock.advance(5 * time.Minute)
	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	recs := sc.recordings(t)
	if len(recs) != 1 {
		t.Fatalf("recordings = %+v, want one", recs)
	}
	frameLen := 20 * time.Millisecond
	if got := recs[0].StoppedAt; got.Before(shutdownAt.Add(-frameLen)) || got.After(shutdownAt.Add(frameLen)) {
		t.Errorf("stopped at %v, want %v, when the shutdown came, within one frame", got, shutdownAt)
	}
}

// recordFlushedFrame starts a recording that hears one frame, and lets a
// flush write it to disk.
func (sc *recordScene) recordFlushedFrame(t *testing.T) {
	t.Helper()
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(trackFlushInterval)
}

// Recovery tells the channel the recording was cut short the way a stop
// tells it the recording stopped: the notice is edited to say so, with the
// link to the recording in the panel, and nobody gets a DM. In a channel
// that isn't spawned, only a notice that is gone, here with its channel
// deleted (10003), sends the starter that by DM instead: one DM, to the
// starter, carrying both.
func TestRecordingCutShortEditsTheNoticeOrSendsTheLink(t *testing.T) {
	t.Run("notice edited", func(t *testing.T) {
		sc := newRecordScene(t, testRecorder)
		sc.recordFlushedFrame(t)

		sc.restart(t, testRecorder)
		sc.clock.advance(0)

		edit := noticeEdit(t, sc.fake, recordingNotice(t, sc.fake, recordChannel))
		if content := deref(edit.Content); !strings.Contains(content, testPanelURL) || !strings.Contains(content, recordingCutShort) {
			t.Errorf("notice edit content = %q, want it to say it %s, with a link into the panel at %s", content, recordingCutShort, testPanelURL)
		}
		if dms := sc.fake.sentDMs(); len(dms) != 0 {
			t.Errorf("DMs after the notice edit went through = %+v, want none", dms)
		}
	})
	t.Run("notice gone", func(t *testing.T) {
		sc := newRecordScene(t, testRecorder)
		sc.recordFlushedFrame(t)
		sc.fake.messageEditErr = restError(http.StatusNotFound, discordgo.ErrCodeUnknownChannel, rawBodyMarker)

		sc.restart(t, testRecorder)
		sc.clock.advance(0)

		dms := sc.fake.sentDMs()
		assertOneLinkDM(t, dms)
		if !strings.Contains(dms[0].data.Content, recordingCutShort) {
			t.Errorf("DM = %q, want it to say the recording %s", dms[0].data.Content, recordingCutShort)
		}
	})
}

// Recovery DMs the starter of a recording cut short in a spawned channel as
// a stop there does, edit or no edit (#620): after the restart the channel
// may still be waiting out its hub's delete delay. One DM, saying the
// recording was cut short, with the link.
func TestRecordingCutShortInASpawnedChannelSendsTheStarterOneDM(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startInSpawned(t)
	sc.clock.advance(time.Second)
	before := len(sc.fake.sentDMs())

	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	dms := sc.fake.sentDMs()[before:]
	assertOneLinkDM(t, dms)
	if !strings.Contains(dms[0].data.Content, recordingCutShort) {
		t.Errorf("DM = %q, want it to say the recording %s", dms[0].data.Content, recordingCutShort)
	}
}

// A recording cut short is mixed like one that stopped: its mix reaches
// ready once the next process has recovered it.
func TestRecordingCutShortIsMixed(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordFlushedFrame(t)

	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	if rec := sc.onlyRecording(t); rec.Mix != store.MixReady {
		t.Errorf("mix = %q after recovery, want %q", rec.Mix, store.MixReady)
	}
}

// A mix the last process was building when it went down is built by the
// next process (#622): the recording stopped, and its mix never ran before
// the crash. Once the next process has recovered, the library lists the mix
// ready and serves the mixer's bytes. The process is down a minute between
// the two.
func TestRecordingMixCutOffByARestartIsBuiltByTheNextProcess(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordOneSpeaker(t)
	sc.crash(t)
	sc.clock.advance(time.Minute)

	want := []byte("the next process's mix")
	sc.mixer.set(want, nil)
	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	rec := sc.onlyRecording(t)
	if rec.Mix != store.MixReady {
		t.Fatalf("mix after the next process recovered = %q, stopped at %v, want %q", rec.Mix, rec.StoppedAt, store.MixReady)
	}
	if got := sc.servedMix(t, rec); !bytes.Equal(got, want) {
		t.Errorf("the library served %q as the mix, want the mixer's %q", got, want)
	}
}

// A mix the next process builds again and fails is marked failed, and
// Sentry hears of it once, as a failed mix after a stop is.
func TestRecordingMixCutOffByARestartThatFailsReachesSentryOnce(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	captures := countCaptures(t)
	sc.recordOneSpeaker(t)
	sc.crash(t)
	sc.clock.advance(time.Minute)

	sc.mixer.set(nil, errors.New("ffmpeg exited with status 1"))
	before := *captures
	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	if rec := sc.onlyRecording(t); rec.Mix != store.MixFailed {
		t.Errorf("mix after the next process failed to build it = %q, want %q", rec.Mix, store.MixFailed)
	}
	if n := *captures - before; n != 1 {
		t.Errorf("the failed mix sent %d events to Sentry, want one", n)
	}
}

// A mix this process is building is never taken for one the last process
// left: a recording stopped before recovery runs, its mix still
// processing when recovery reads it, is mixed once. The scene's runtime
// has just been built, so its recovery runs at the clock's first advance,
// ahead of the stop's mix.
func TestRecordingStoppedBeforeRecoveryRunsIsMixedOnce(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.stopAsStarter(t)

	sc.clock.advance(0)

	if n := sc.mixer.mixes(); n != 1 {
		t.Errorf("the mixer was asked for %d mixes of the recording, want one", n)
	}
}

// A recording cut short before anything its one speaker said reached the
// disk has nothing to mix: recovery drops the empty track, and the
// recording gets no mix without the mixer being asked for one. The crash
// comes before the first flush, which is what leaves the track empty.
func TestRecordingCutShortWithNoAudioOnDiskHasNoMix(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))

	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	sc.assertCutShort(t)
	if rec := sc.onlyRecording(t); rec.Mix != store.MixNone {
		t.Errorf("mix of a recording cut short with no audio on disk = %q, want %q", rec.Mix, store.MixNone)
	}
	if n := sc.mixer.mixes(); n != 0 {
		t.Errorf("the mixer was asked for %d mixes, want none", n)
	}
}

// A recovery that fails reaches Sentry: here the store refuses to close the
// recording's row.
func TestRecordingRecoveryFailureReachesSentry(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordFlushedFrame(t)
	sc.faults.stopRecordingErr = errors.New("connection refused")
	captures := countCaptures(t)

	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	if *captures == 0 {
		t.Error("the failed recovery sent nothing to Sentry")
	}
}

// A recording left running is closed as cut short on a host whose restart
// turned recording off, so the deploy's status check stops reporting it.
// The crash fixture once more, recovered by a runtime with no recorder.
// A regression pin: it goes red if recovery ever waits on a recorder to
// check.
func TestRecordingCutShortIsClosedWithRecordingOff(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startCrashFixture(t)

	sc.restart(t)
	sc.clock.advance(0)

	sc.assertCutShort(t)
}

// No recorder is left sitting in voice after recovery: a recorder the
// cache shows in the channel it recorded when the last process went down,
// with no recording of this process holding it, is disconnected.
func TestRecordingRecoveryLeavesNoRecorderInVoice(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordFlushedFrame(t)
	sc.fake.setVoice(testRecorder, recordChannel)

	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	if ch := sc.fake.VoiceStates(testTempVCGuild).channelOf(testRecorder); ch != "" {
		t.Errorf("the recorder is in %s after recovery, want it in no channel", ch)
	}
}

// A recording a member starts after the restart, before recovery has run,
// is never taken for one the last process left: it keeps running and its
// recorder stays in its channel, while the one the crash cut short is
// closed.
func TestRecordingStartedBeforeRecoveryRunsKeepsRunning(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordFlushedFrame(t)
	sc.restart(t, testRecorder)
	sc.fake.setVoice(testRecorder, recordOtherChannel)
	sc.startIn(t, recHolder, recordOtherChannel)

	sc.clock.advance(0)

	for _, rec := range sc.recordings(t) {
		switch rec.ChannelID {
		case recordChannel:
			if rec.Ended != store.RecordingEndCutShort {
				t.Errorf("the crashed recording ended %q, want %q", rec.Ended, store.RecordingEndCutShort)
			}
		case recordOtherChannel:
			if !rec.StoppedAt.IsZero() {
				t.Errorf("the recording started after the restart stopped at %v, ended %q, want it running", rec.StoppedAt, rec.Ended)
			}
		}
	}
	if ch := sc.fake.VoiceStates(testTempVCGuild).channelOf(testRecorder); ch != recordOtherChannel {
		t.Errorf("the recorder is in %q after recovery, want it still in %s, recording it", ch, recordOtherChannel)
	}
}

// A recording cut short names its speakers by their display names, so
// recovery waits for the member list, which a restart has to fetch again.
// The zip's info file, which names every speaker, shows it.
func TestRecordingCutShortNamesItsSpeakersOnceTheMemberListArrives(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordFlushedFrame(t)
	sc.fake.setListArriving(true)
	sc.restart(t, testRecorder)
	sc.clock.advance(0)

	sc.nameSpeakers(map[string]string{speakerA: "doej"})
	sc.fake.setListArriving(false)
	sc.clock.advance(recordingRecoveryPoll)

	info, ok := sc.zipEntries(t, sc.onlyRecording(t))[infoFileName]
	if !ok {
		t.Fatalf("the zip holds no %s", infoFileName)
	}
	if !bytes.Contains(info, []byte("doej")) {
		t.Errorf("the info file doesn't name the speaker doej:\n%s", info)
	}
}

// A member list that never arrives holds recovery up for only so long: once
// the wait runs out, the recording is closed as cut short anyway, so the
// deploy's status check stops reporting it.
func TestRecordingCutShortIsClosedWhenTheMemberListNeverArrives(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordFlushedFrame(t)
	sc.fake.setListArriving(true)
	sc.restart(t, testRecorder)

	sc.clock.advance(recordingRecoveryWait)

	sc.assertCutShort(t)
}
