package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// A recording's mix (spec #381, #389): one Ogg Opus file of every track
// played together, built once the recording stops. Its state on the row goes
// from processing to ready, or to failed, which Sentry hears of. The tracks
// stay downloadable either way. A recording nobody spoke in goes to none:
// it has nothing to mix, and the mixer never runs.

// Mixer builds a recording's mix from its tracks.
type Mixer interface {
	// Mix writes to out one Ogg Opus file of every track played together,
	// as long as the longest.
	Mix(ctx context.Context, tracks []string, out string) error
}

// recordingMixTimeout bounds one mix. ffmpeg mixes a 6-hour recording in
// minutes.
const recordingMixTimeout = time.Hour

// startMix hands a stopped recording's tracks to the mixer, on a goroutine
// of its own: a mix takes minutes, and the stop doesn't wait for it.
func (r *RecordingRuntime) startMix(rec store.Recording) {
	recordingAfterFunc(0, func() { r.mix(rec) })
}

// mix builds a stopped recording's mix and records how it went. A
// recording nobody spoke in has no track, and ffmpeg won't mix none, so it
// gets no mix.
func (r *RecordingRuntime) mix(rec store.Recording) {
	defer utils.RecoverPanic("recording-mix", "recording_id", rec.ID)
	if len(rec.Speakers) == 0 {
		r.setMix(rec.ID, store.MixNone, nil)
		return
	}
	tracks := make([]string, 0, len(rec.Speakers))
	for _, sp := range rec.Speakers {
		tracks = append(tracks, trackPath(r.dir, rec.ID, sp.ID))
	}
	mixCtx, cancelMix := context.WithTimeout(context.Background(), recordingMixTimeout)
	defer cancelMix()
	state := store.MixReady
	mixErr := r.mixer.Mix(mixCtx, tracks, mixPath(r.dir, rec.ID))
	if mixErr != nil {
		state = store.MixFailed
	}
	if r.setMix(rec.ID, state, mixErr) {
		utils.Info("Recording mixed", "recording_id", rec.ID, "mix", string(state))
	}
}

// setMix records how far a recording's mix got, and sends mixErr, the
// mixer's failure when it failed, to Sentry. It reports whether the state
// was written.
func (r *RecordingRuntime) setMix(id int64, state store.MixState, mixErr error) bool {
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancel()
	err := r.st.SetRecordingMix(ctx, id, state)
	if errors.Is(err, store.ErrNotFound) {
		// Its starter or a panel admin deleted it since the stop, files
		// and all, which is what failed the mixer if it failed.
		utils.Info("Recording deleted while it was mixed", "recording_id", id)
		return false
	}
	if mixErr != nil {
		captureError("Mix failed", mixErr, "recording_id", id)
	}
	if err != nil {
		captureError("Mix state not written", err, "recording_id", id, "mix", string(state))
		return false
	}
	return true
}

// FFmpegMixer builds a mix with ffmpeg, run as a subprocess: every track
// mixed by amix, as long as the longest, encoded to Ogg Opus. Each track
// keeps its level, since amix's default divides every voice by the number
// of tracks. The mix is written under a temporary name and renamed into
// place, so a mix cut short never sits at its path.
type FFmpegMixer struct{}

// ffmpegErrTail is the most of ffmpeg's error output a failure keeps.
const ffmpegErrTail = 1024

// Mix implements Mixer.
func (FFmpegMixer) Mix(ctx context.Context, tracks []string, out string) error {
	part := out + ".part"
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}
	for _, track := range tracks {
		args = append(args, "-i", track)
	}
	args = append(args,
		"-filter_complex", "amix=inputs="+strconv.Itoa(len(tracks))+":duration=longest:normalize=0",
		"-c:a", "libopus", "-b:a", "64k", "-f", "ogg", part)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(part)
		msg := stderr.String()
		if len(msg) > ffmpegErrTail {
			msg = msg[len(msg)-ffmpegErrTail:]
		}
		return fmt.Errorf("ffmpeg mixing %d tracks: %w: %s", len(tracks), err, strings.TrimSpace(msg))
	}
	return os.Rename(part, out)
}
