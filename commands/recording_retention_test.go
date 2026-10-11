package commands

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
)

// Retention (#392). A sweep deletes each recording, its files and its row,
// 30 days after it stopped. Every case records through /record with the fake
// voice connection, advances the fake clock, which runs each pass on the
// test goroutine, and reads the recordings back through the library the
// panel uses and from the recordings directory. The 30 days are written out,
// since the recording notice promises them; the sweep's interval is the
// production constant.

// thirtyDays is how long the recording notice promises a recording is kept.
const thirtyDays = 30 * 24 * time.Hour

// listed reads every recording the library lists.
func (sc *recordScene) listed(t *testing.T) []store.Recording {
	t.Helper()
	recs, err := sc.lib.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	return recs
}

// filesUnder lists every regular file under a directory.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return files
}

// A recording is deleted, its files and its row, 30 days after it stops,
// and is still there a day short of that. The 30 days run from the stop,
// not the start: a recording that ran longer than one sweep interval is
// still there one interval short of 30 days after its stop.
func TestRecordingIsDeleted30DaysAfterItStops(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(3 * time.Hour)
	recordAs(t, sc.rt, recStarter, "stop")

	sc.clock.advance(thirtyDays - 24*time.Hour)
	if recs := sc.listed(t); len(recs) != 1 {
		t.Fatalf("recordings listed a day short of 30 days after the stop = %d, want one", len(recs))
	}
	if files := filesUnder(t, sc.dir); len(files) == 0 {
		t.Error("no file of the recording is left a day short of 30 days after its stop")
	}

	sc.clock.advance(24*time.Hour - retentionSweepInterval)
	if recs := sc.listed(t); len(recs) != 1 {
		t.Fatalf("recordings listed one sweep interval short of 30 days after the stop = %d, want one", len(recs))
	}

	sc.clock.advance(2 * retentionSweepInterval)
	if recs := sc.listed(t); len(recs) != 0 {
		t.Errorf("recordings listed 30 days after the stop = %+v, want none", recs)
	}
	if files := filesUnder(t, sc.dir); len(files) != 0 {
		t.Errorf("files left 30 days after the stop = %v, want none", files)
	}
}

// A running recording is never swept: one held just short of the cap,
// across every pass that runs while it records, is still listed and still
// running.
func TestRecordingRunningIsNeverSwept(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))

	sc.clock.advance(recordingCap - 2*time.Second)

	if recs := sc.listed(t); len(recs) != 1 || !recs[0].StoppedAt.IsZero() {
		t.Errorf("recordings listed one second short of the cap = %+v, want one still running", recs)
	}
}

// With no recorder configured, a recording is still deleted 30 days after it
// stops, as its notice promises: recording off leaves the sweep running. A
// regression pin, with the recording written through the store, since a
// runtime with a recorder would sweep it itself.
func TestRecordingIsDeleted30DaysAfterItStopsWithNoRecorder(t *testing.T) {
	sc := newRecordScene(t)
	ctx := context.Background()
	rec, err := sc.st.StartRecording(ctx, store.Recording{GuildID: testTempVCGuild, ChannelID: recordChannel,
		StarterID: recStarter.id, RecorderID: testRecorder, StartedAt: sc.clock.read()})
	if err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	sc.clock.advance(time.Hour)
	if err := sc.st.StopRecording(ctx, rec.ID, sc.clock.read(), store.RecordingEndStopped, nil); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}

	sc.clock.advance(thirtyDays + retentionSweepInterval)

	if recs := sc.listed(t); len(recs) != 0 {
		t.Errorf("recordings listed 30 days after the stop with no recorder = %+v, want none", recs)
	}
}

// A deletion that fails reaches Sentry, and the next pass deletes the
// recording: when its files can't be removed, and when its row can't be
// deleted. A recording whose files outlast its row would be left with
// nothing to sweep it, so after a file failure the files go too.
func TestRecordingDeletionFailureIsReportedAndRetried(t *testing.T) {
	for _, tc := range []struct {
		name string
		// fail arms the failure with err, and disarms it when err is nil.
		fail func(sc *recordScene, err error)
		// filesChecked is set when the retry must also remove the files,
		// which the failure kept.
		filesChecked bool
	}{
		{
			name: "files",
			fail: func(_ *recordScene, err error) {
				recordingRemoveAll = func(path string) error {
					if err != nil {
						return err
					}
					return os.RemoveAll(path)
				}
			},
			filesChecked: true,
		},
		{
			name: "row",
			fail: func(sc *recordScene, err error) { sc.faults.deleteRecordingErr = err },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prevRemoveAll := recordingRemoveAll
			t.Cleanup(func() { recordingRemoveAll = prevRemoveAll })
			sc := newRecordScene(t, testRecorder)
			captures := countCaptures(t)
			sc.recordOneSpeaker(t)
			sc.clock.advance(thirtyDays - retentionSweepInterval)

			before := *captures
			tc.fail(sc, errors.New("input/output error"))
			sc.clock.advance(2 * retentionSweepInterval)
			if *captures == before {
				t.Error("the failed deletion didn't reach Sentry")
			}

			tc.fail(sc, nil)
			sc.clock.advance(retentionSweepInterval)
			if recs := sc.listed(t); len(recs) != 0 {
				t.Errorf("recordings listed after the next pass = %+v, want none", recs)
			}
			if files := filesUnder(t, sc.dir); tc.filesChecked && len(files) != 0 {
				t.Errorf("files left after the next pass = %v, want none", files)
			}
		})
	}
}
