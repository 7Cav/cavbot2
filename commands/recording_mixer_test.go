package commands

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The mixer's contract (#389), run against the real ffmpeg. The inputs are
// Ogg Opus tracks ffmpeg generates, each with a tone in a time window of its
// own, and the mix is read back by decoding it with ffmpeg. Its bytes,
// pre-skip and padding are never pinned. CI installs ffmpeg; run anywhere
// else without it, the test skips.

// pcmRate is the rate the tests decode at, in samples a second.
const pcmRate = 48000

// The levels a window's RMS is judged by. The tones are generated at
// amplitude 0.5, an RMS of about 0.35.
const (
	// signalRMS: a window louder than this has signal (-40 dBFS).
	signalRMS = 0.01
	// silenceRMS: a window quieter than this is silent (-60 dBFS).
	silenceRMS = 0.001
)

// requireFFmpeg skips the test when ffmpeg isn't on PATH, except under
// GitHub Actions, whose Build job installs it: there a missing ffmpeg
// fails the test rather than letting the contract go unchecked.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if os.Getenv("GITHUB_ACTIONS") != "" {
			t.Fatal("ffmpeg is not on PATH, and CI installs it")
		}
		t.Skip("ffmpeg is not on PATH")
	}
}

// ffmpeg runs ffmpeg with args and returns what it wrote to stdout.
func ffmpeg(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-nostdin", "-hide_banner", "-loglevel", "error"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, stderr)
	}
	return out
}

// toneTrack writes a stereo Ogg Opus track length seconds long, silent but
// for a 440 Hz tone from from to to seconds, and returns its path.
func toneTrack(t *testing.T, dir, name string, length, from, to float64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	expr := fmt.Sprintf("aevalsrc='if(between(t,%g,%g),0.5*sin(2*PI*440*t),0)':s=%d:d=%g", from, to, pcmRate, length)
	ffmpeg(t, "-f", "lavfi", "-i", expr, "-ac", "2", "-c:a", "libopus", "-f", "ogg", path)
	return path
}

// decodeMono decodes an audio file to mono samples at pcmRate, each in
// [-1, 1).
func decodeMono(t *testing.T, path string) []float64 {
	t.Helper()
	raw := ffmpeg(t, "-i", path, "-f", "s16le", "-ac", "1", "-ar", fmt.Sprint(pcmRate), "-")
	samples := make([]float64, len(raw)/2)
	for i := range samples {
		samples[i] = float64(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / 32768
	}
	return samples
}

// rmsBetween is the RMS of the samples from from to to seconds.
func rmsBetween(t *testing.T, samples []float64, from, to float64) float64 {
	t.Helper()
	lo, hi := int(from*pcmRate), int(to*pcmRate)
	if hi > len(samples) {
		t.Fatalf("the mix is %.3f s long, too short to read %g-%g s", float64(len(samples))/pcmRate, from, to)
	}
	var sum float64
	for _, s := range samples[lo:hi] {
		sum += s * s
	}
	return math.Sqrt(sum / float64(hi-lo))
}

// The mix plays every track together: each speaker's window has signal in
// it, the gap between them is silent, and it lasts as long as the longest
// track. Each window and the gap are read in their middle, away from the
// codec's edges.
func TestFFmpegMixerPlaysEveryTrackTogether(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	a := toneTrack(t, dir, "a.ogg", 3, 0.5, 1.0)
	b := toneTrack(t, dir, "b.ogg", 4, 1.5, 2.0)
	out := filepath.Join(dir, "mix.opus")

	if err := (FFmpegMixer{}).Mix(context.Background(), []string{a, b}, out); err != nil {
		t.Fatalf("Mix: %v", err)
	}

	mix := decodeMono(t, out)
	for _, w := range []struct {
		name     string
		from, to float64
	}{{"a's window", 0.6, 0.9}, {"b's window", 1.6, 1.9}} {
		if got := rmsBetween(t, mix, w.from, w.to); got < signalRMS {
			t.Errorf("%s (%g-%g s) has RMS %.5f in the mix, want signal above %g", w.name, w.from, w.to, got, signalRMS)
		}
	}
	if got := rmsBetween(t, mix, 1.15, 1.35); got > silenceRMS {
		t.Errorf("the gap (1.15-1.35 s) has RMS %.5f in the mix, want silence below %g", got, silenceRMS)
	}
	longest := len(decodeMono(t, b))
	if diff := math.Abs(float64(len(mix)-longest)) / pcmRate; diff > 0.1 {
		t.Errorf("the mix is %.3f s long and the longest track %.3f s, want them within 100 ms",
			float64(len(mix))/pcmRate, float64(longest)/pcmRate)
	}
}
