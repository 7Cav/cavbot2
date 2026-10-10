package commands

import (
	"bytes"
	"context"
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
// stay downloadable either way.

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

// mix builds a stopped recording's mix and records how it went.
func (r *RecordingRuntime) mix(rec store.Recording) {
	defer utils.RecoverPanic("recording-mix", "recording_id", rec.ID)
	tracks := make([]string, 0, len(rec.Speakers))
	for _, sp := range rec.Speakers {
		tracks = append(tracks, trackPath(r.dir, rec.ID, sp.ID))
	}
	mixCtx, cancelMix := context.WithTimeout(context.Background(), recordingMixTimeout)
	defer cancelMix()
	state := store.MixReady
	if err := r.mixer.Mix(mixCtx, tracks, mixPath(r.dir, rec.ID)); err != nil {
		state = store.MixFailed
		captureError("Mix failed", err, "recording_id", rec.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancel()
	if err := r.st.SetRecordingMix(ctx, rec.ID, state); err != nil {
		captureError("Mix state not written", err, "recording_id", rec.ID, "mix", string(state))
		return
	}
	utils.Info("Recording mixed", "recording_id", rec.ID, "mix", string(state))
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
