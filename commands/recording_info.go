package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/7cav/cavbot2/store"
)

// A recording's info file (spec #381, #389): a text file beside its tracks,
// and in its zip, that documents the recording. It names the title, the
// channel, the starter, the start and stop times in Zulu, how the recording
// ended, and each speaker's display name and Discord ID.

// infoFileName is the info file's name, in the recording's directory and in
// its zip.
const infoFileName = "info.txt"

// infoPath is the path of a recording's info file.
func infoPath(dir string, id int64) string {
	return filepath.Join(recordingDir(dir, id), infoFileName)
}

// zuluLayout is how the info file writes a time, in UTC.
const zuluLayout = "2006-01-02 15:04:05Z"

// writeRecordingInfo writes a stopped recording's info file. starterName is
// the starter's display name.
func writeRecordingInfo(dir string, rec store.Recording, starterName string) error {
	if err := os.MkdirAll(recordingDir(dir, rec.ID), 0o750); err != nil {
		return err
	}
	return os.WriteFile(infoPath(dir, rec.ID), []byte(recordingInfo(rec, starterName)), 0o600)
}

// recordingInfo is the info file's text.
func recordingInfo(rec store.Recording, starterName string) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("Recording %d", rec.ID)
	line("Title: %s", orNone(rec.Title))
	line("Channel: %s (%s)", orNone(rec.ChannelName), rec.ChannelID)
	line("Started by: %s (%s)", starterName, rec.StarterID)
	line("Started: %s", rec.StartedAt.UTC().Format(zuluLayout))
	line("Stopped: %s", rec.StoppedAt.UTC().Format(zuluLayout))
	line("Ended: %s", recordingEndPhrase(rec.Ended))
	line("Speakers:")
	if len(rec.Speakers) == 0 {
		line("  (none)")
	}
	for _, sp := range rec.Speakers {
		line("  %s (%s)", sp.DisplayName, sp.ID)
	}
	return b.String()
}

// orNone is s, or "(none)" when it is empty.
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// recordingEndPhrase says how a recording ended, for the info file.
func recordingEndPhrase(end store.RecordingEnd) string {
	switch end {
	case store.RecordingEndStopped:
		return "stopped by a member"
	case store.RecordingEndCap:
		return fmt.Sprintf("reached the %g-hour cap", recordingCap.Hours())
	case store.RecordingEndEmpty:
		return "nobody was left in the channel"
	case store.RecordingEndDisk:
		return "disk space ran low"
	case store.RecordingEndRecorderLeft:
		return "the recorder left the channel"
	default:
		return string(end)
	}
}
