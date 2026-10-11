package commands

import (
	"context"
	"os"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// Retention (spec #381, #392). A recording is deleted, its files and its
// row, once RecordingRetention has passed since it stopped, as its notice
// promises. The sweep runs when the runtime is built and every
// retentionSweepInterval after, on every host with a bot store, so a host
// with recording off still keeps the promise. It never touches a running
// recording.

// retentionSweepInterval is how often the retention sweep runs. A recording
// goes within one interval of its deletion date.
const retentionSweepInterval = time.Hour

// recordingRemoveAll removes a recording's directory and everything in it,
// a package var so tests can make it fail.
var recordingRemoveAll = os.RemoveAll

// sweepRetention deletes every recording whose deletion date has passed,
// then schedules the next pass, even after a panic.
func (r *RecordingRuntime) sweepRetention() {
	defer func() { recordingAfterFunc(retentionSweepInterval, r.sweepRetention) }()
	defer utils.RecoverPanic("recording-retention")
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	recs, err := r.st.ListRecordings(ctx, r.guildID)
	cancel()
	if err != nil {
		captureError("Recordings unreadable for retention", err)
		return
	}
	now := recordingNow()
	for _, rec := range recs {
		if rec.StoppedAt.IsZero() || now.Before(RecordingDeletesAt(rec)) {
			continue
		}
		r.expireRecording(rec)
	}
}

// expireRecording deletes a recording past its deletion date: its files,
// then its row. A failure goes to Sentry and leaves the row, so the next
// pass tries again.
func (r *RecordingRuntime) expireRecording(rec store.Recording) {
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancel()
	if err := removeRecording(ctx, r.st, r.dir, rec.ID); err != nil {
		captureError("Recording not deleted after retention", err, "recording_id", rec.ID)
		return
	}
	utils.Info("Recording deleted after its retention", "recording_id", rec.ID, "stopped_at", rec.StoppedAt)
}
