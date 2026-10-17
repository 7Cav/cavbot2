package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/7cav/cavbot2/store"
)

// A recording's info file (spec #381, #389): a text file beside its tracks,
// and in its zip, that documents the recording. It names the title, the
// channel, the starter, the start and stop times in Zulu, how the recording
// ended, and each speaker's display name and Discord ID, marking a lost
// track. Each gap past 10 s gets a line too (GLOSSARY.md, Gap), but only in
// the file written at the stop: one rendered from the row lists none.

// TrackLostMarker follows the name of a speaker whose track was lost
// (GLOSSARY.md, Lost track), wherever a recording names its speakers: its
// info file and the Recordings page.
const TrackLostMarker = "(track lost)"

// infoGapLabel opens the info file's line for each gap past 10 s
// (GLOSSARY.md, Gap).
const infoGapLabel = "Gap:"

// zuluLayout is how the info file writes a time: a UTC date, then the time
// with the regiment's z suffix (GLOSSARY.md, Zulu).
const zuluLayout = "2006-01-02 1504z"

// gapSpan is when a gap past 10 s started and ended, as the info file
// lists it.
type gapSpan struct {
	start, end time.Time
}

// writeRecordingInfo writes a stopped recording's info file. starterName is
// the starter's display name, and gaps the recording's gaps past 10 s, in
// order.
func writeRecordingInfo(dir string, rec store.Recording, starterName string, gaps []gapSpan) error {
	if err := os.MkdirAll(recordingDir(dir, rec.ID), 0o750); err != nil {
		return err
	}
	return os.WriteFile(infoPath(dir, rec.ID), []byte(recordingInfo(rec, starterName, gaps)), 0o600)
}

// recordingInfo is the info file's text. starterName is the starter's
// display name, or empty to name them by Discord ID alone, and gaps the
// recording's gaps past 10 s, in order.
func recordingInfo(rec store.Recording, starterName string, gaps []gapSpan) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("Recording %d", rec.ID)
	line("Title: %s", orNone(rec.Title))
	line("Channel: %s (%s)", orNone(rec.ChannelName), rec.ChannelID)
	if starterName == "" {
		line("Started by: %s", rec.StarterID)
	} else {
		line("Started by: %s (%s)", starterName, rec.StarterID)
	}
	line("Started: %s", rec.StartedAt.UTC().Format(zuluLayout))
	line("Stopped: %s", rec.StoppedAt.UTC().Format(zuluLayout))
	line("Ended: %s", recordingEndPhrase(rec.Ended))
	for _, g := range gaps {
		line("%s %s to %s (%s)", infoGapLabel, g.start.UTC().Format(zuluLayout), g.end.UTC().Format(zuluLayout),
			g.end.Sub(g.start).Round(time.Second))
	}
	line("Speakers:")
	if len(rec.Speakers) == 0 {
		line("  (none)")
	}
	for _, sp := range rec.Speakers {
		if sp.TrackLost {
			line("  %s (%s) %s", sp.DisplayName, sp.ID, TrackLostMarker)
		} else {
			line("  %s (%s)", sp.DisplayName, sp.ID)
		}
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
	case store.RecordingEndCutShort:
		return "cut short when the bot restarted; what reached the disk is kept"
	default:
		return string(end)
	}
}
