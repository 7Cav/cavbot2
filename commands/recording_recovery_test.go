package commands

import (
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

// A crash keeps what reached the disk: the next process closes the
// recording as cut short, and the track of a speaker heard before the last
// flush holds what they said. A speaker first heard after it has nothing on
// disk, and every track recovery keeps holds audio, since ffmpeg won't mix
// a track that holds none.
func TestRecordingCutShortByACrashKeepsWhatReachedTheDisk(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(trackFlushInterval)
	sc.hear(t, recordChannel, frame(speakerB, 22, 90000, "b1"))

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

// crashMidRecording starts a recording that hears one frame, and crashes
// once the frame is on disk.
func (sc *recordScene) crashMidRecording(t *testing.T) {
	t.Helper()
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(trackFlushInterval)
}

// Recovery tells the channel the recording was cut short the way a stop
// does: the notice is edited with the link to the recording in the panel,
// and nobody gets a DM. Only when the notice is gone, here with its channel
// deleted (10003), does the starter get the link by DM instead: one DM,
// to the starter, carrying it.
func TestRecordingCutShortEditsTheNoticeOrSendsTheLink(t *testing.T) {
	t.Run("notice edited", func(t *testing.T) {
		sc := newRecordScene(t, testRecorder)
		sc.crashMidRecording(t)

		sc.restart(t, testRecorder)
		sc.clock.advance(0)

		edit := noticeEdit(t, sc.fake, recordingNotice(t, sc.fake, recordChannel))
		if !strings.Contains(deref(edit.Content), testPanelURL) {
			t.Errorf("notice edit content = %q, want a link into the panel at %s", deref(edit.Content), testPanelURL)
		}
		if dms := sc.fake.sentDMs(); len(dms) != 0 {
			t.Errorf("DMs after the notice edit went through = %+v, want none", dms)
		}
	})
	t.Run("notice gone", func(t *testing.T) {
		sc := newRecordScene(t, testRecorder)
		sc.crashMidRecording(t)
		sc.fake.messageEditErr = restError(http.StatusNotFound, discordgo.ErrCodeUnknownChannel, rawBodyMarker)

		sc.restart(t, testRecorder)
		sc.clock.advance(0)

		dms := sc.fake.sentDMs()
		if len(dms) != 1 {
			t.Fatalf("DMs after the notice edit failed with 10003 = %+v, want one", dms)
		}
		if dms[0].to != recStarter.id || !strings.Contains(dms[0].data.Content, testPanelURL) {
			t.Errorf("DM to %s = %q, want one to the starter %s with a link into the panel at %s",
				dms[0].to, dms[0].data.Content, recStarter.id, testPanelURL)
		}
	})
}
