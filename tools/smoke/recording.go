//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// recorderTally is what the bot's startup log says about its recorders:
// whether recording is off, how many connected, and the place in
// RECORDER_TOKENS of each one that failed. It holds no token, so a report
// built from it can't print one.
type recorderTally struct {
	Off       bool  `json:"off,omitempty"`
	Connected int   `json:"connected"`
	Failed    []int `json:"failed,omitempty"`
}

// recorderPlace finds a recorder's place in a log line. A failure logs the
// error first, so the last match is the bot's own.
var recorderPlace = regexp.MustCompile(`\brecorder=(\d+)\b`)

// count tallies line if it is one of the bot's recorder lines.
func (r *recorderTally) count(line string) {
	switch {
	case strings.Contains(line, "RECORDER_TOKENS not set, recording off"):
		r.Off = true
	case strings.Contains(line, "Recorder connected"):
		r.Connected++
	case strings.Contains(line, "Recorder failed to connect"):
		if m := recorderPlace.FindAllStringSubmatch(line, -1); len(m) > 0 {
			n, _ := strconv.Atoi(m[len(m)-1][1])
			r.Failed = append(r.Failed, n)
		}
	}
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
// bot connected at startup, and whether it can build a mix. up prints it in
// the ready block and keeps it in the run file for status. It never stops a
// run, since most passes never record.
type recordingReport struct {
	Recorders recorderTally `json:"recorders"`
	Mix       mixVerdict    `json:"mix"`
}

// line is the report's line on the recorders. It names a failed one by its
// place in RECORDER_TOKENS, as the bot's log does.
func (t recorderTally) line() string {
	if t.Off {
		return "none, and recording is off because RECORDER_TOKENS is empty"
	}
	text := fmt.Sprintf("%d connected", t.Connected)
	if len(t.Failed) > 0 {
		places := make([]string, len(t.Failed))
		for i, n := range t.Failed {
			places[i] = strconv.Itoa(n)
		}
		named := "recorder " + places[0]
		if len(places) > 1 {
			named = "recorders " + strings.Join(places[:len(places)-1], ", ") + " and " + places[len(places)-1]
		}
		text += fmt.Sprintf(", %d failed. The bot log says why %s in RECORDER_TOKENS failed", len(t.Failed), named)
	}
	if t.Connected == 0 {
		text += ". No recording check can run"
	}
	return text
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
