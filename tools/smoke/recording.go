//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// recorderTally is what the bot's log says about its recorders: whether
// recording is off, how many are connected, and the place in RECORDER_TOKENS
// of each one that failed and each one that is out. It holds no token, so a
// report built from it can't print one.
type recorderTally struct {
	Off       bool  `json:"off,omitempty"`
	Connected int   `json:"connected"`
	Failed    []int `json:"failed,omitempty"`
	Out       []int `json:"out,omitempty"`
}

// The bot's recorder lines, as its voice adapter logs them.
const (
	recordingOffLine      = "RECORDER_TOKENS not set, recording off"
	recorderConnectedLine = "Recorder connected"
	recorderFailedLine    = "Recorder failed to connect"
	recorderRefusedLine   = "Discord refused the recorder's token, recorder out until a restart"
)

// recorderPlace finds a recorder's place in a log line. A failure logs the
// error first, so the last match is the bot's own.
var recorderPlace = regexp.MustCompile(`\brecorder=(\d+)\b`)

// recorderLines reads the bot's recorder lines into a tally. A recorder
// logs that it connected at each login, as when it logs back in after a cut
// short, so the tally counts it once, by its place in RECORDER_TOKENS. One
// Discord refuses as it logs in again is out until the bot restarts, so the
// tally stops counting it.
type recorderLines struct {
	tally     recorderTally
	connected map[int]bool
}

// read tallies line if it is one of the bot's recorder lines, and reports
// whether the tally changed.
func (r *recorderLines) read(line string) bool {
	switch {
	case strings.Contains(line, recordingOffLine):
		r.tally.Off = true
		return true
	case strings.Contains(line, recorderConnectedLine):
		if n, ok := placeIn(line); ok && !r.connected[n] {
			if r.connected == nil {
				r.connected = map[int]bool{}
			}
			r.connected[n] = true
			r.tally.Connected++
			return true
		}
	case strings.Contains(line, recorderFailedLine):
		if n, ok := placeIn(line); ok {
			r.tally.Failed = append(r.tally.Failed, n)
			return true
		}
	case strings.Contains(line, recorderRefusedLine):
		if n, ok := placeIn(line); ok {
			if r.connected[n] {
				delete(r.connected, n)
				r.tally.Connected--
			}
			r.tally.Out = append(r.tally.Out, n)
			return true
		}
	}
	return false
}

// snapshot is the tally so far, which later lines leave as it is.
func (r *recorderLines) snapshot() recorderTally {
	t := r.tally
	t.Failed = slices.Clone(t.Failed)
	t.Out = slices.Clone(t.Out)
	return t
}

// placeIn is the place of the recorder a log line names.
func placeIn(line string) (int, bool) {
	m := recorderPlace.FindAllStringSubmatch(line, -1)
	if len(m) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	return n, err == nil
}

// mixVerdict is whether the bot can build a mix.
type mixVerdict string

const (
	mixReady       mixVerdict = "ready"
	mixNoFFmpeg    mixVerdict = "no-ffmpeg"
	mixNoLibopus   mixVerdict = "no-libopus"
	mixFFmpegFails mixVerdict = "ffmpeg-fails"
)

// checkMix judges whether a bot started with env can build a mix.
func checkMix(ctx context.Context, env []string) mixVerdict {
	path, _ := envValue(env, "PATH")
	ffmpeg, ok := lookPathIn("ffmpeg", path)
	if !ok {
		return mixNoFFmpeg
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return mixFFmpegFails
	}
	// The mix encodes with libopus, not ffmpeg's native opus encoder.
	if !listsEncoder(string(out), "libopus") {
		return mixNoLibopus
	}
	return mixReady
}

// listsEncoder reports whether name is an encoder in out, ffmpeg's
// -encoders list, where each encoder's line gives its flags, then its name.
func listsEncoder(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}

// lookPathIn finds file in the directories of path, a PATH value, the way
// the bot's exec.Command does. That refuses a relative directory, so only
// absolute ones count.
func lookPathIn(file, path string) (string, bool) {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, file)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// recordingReport says whether recording checks can run: the recorders the
// bot has connected, and whether it can build a mix. up prints it in
// the ready block and keeps it in the run file for status. It never stops a
// run, since most passes never record.
type recordingReport struct {
	Recorders recorderTally `json:"recorders"`
	Mix       mixVerdict    `json:"mix"`
}

// The phrases of the report's line on the recorders that tell its cases
// apart.
const (
	noRecordingCheck  = "No recording check can run"
	outBecauseRefused = "out because Discord refused"
)

// line is the report's line on the recorders. It names a failed or out one
// by its place in RECORDER_TOKENS, as the bot's log does.
func (t recorderTally) line() string {
	if t.Off {
		return "none, and recording is off because RECORDER_TOKENS is empty"
	}
	text := fmt.Sprintf("%d connected", t.Connected)
	if len(t.Out) > 0 {
		tokens := "its token"
		if len(t.Out) > 1 {
			tokens = "their tokens"
		}
		text += fmt.Sprintf(", %s %s %s", recorderNames(t.Out), outBecauseRefused, tokens)
	}
	if len(t.Failed) > 0 {
		text += fmt.Sprintf(", %d failed. The bot log says why %s in RECORDER_TOKENS failed", len(t.Failed), recorderNames(t.Failed))
	}
	if t.Connected == 0 {
		text += ". " + noRecordingCheck
	}
	return text
}

// recorderNames names recorders by their places in RECORDER_TOKENS.
func recorderNames(places []int) string {
	named := make([]string, len(places))
	for i, n := range places {
		named[i] = strconv.Itoa(n)
	}
	if len(named) == 1 {
		return "recorder " + named[0]
	}
	return "recorders " + strings.Join(named[:len(named)-1], ", ") + " and " + named[len(named)-1]
}

// line is the report's line on building a mix.
func (v mixVerdict) line() string {
	switch v {
	case mixReady:
		return "will work"
	case mixNoFFmpeg:
		return "will fail because ffmpeg is not on the bot's PATH"
	case mixNoLibopus:
		return "will fail because the bot's ffmpeg has no libopus encoder"
	case mixFFmpegFails:
		return "will fail because the bot's ffmpeg exits with an error when it lists its encoders"
	}
	return "not checked"
}
