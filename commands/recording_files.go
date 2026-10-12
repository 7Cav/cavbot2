package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// A recording's files (spec #381, #389). Each recording has a directory of
// its own under the recordings directory, named by its ID. It holds one Ogg
// Opus track per speaker, named by their Discord ID, the info file, and the
// mix once it is built. The tracks' writer, the info file's, the mixer and
// the library all find them here.

// recordingDir is a recording's directory under the recordings directory.
func recordingDir(dir string, id int64) string {
	return filepath.Join(dir, strconv.FormatInt(id, 10))
}

// trackPath is the path of a speaker's track in a recording.
func trackPath(dir string, id int64, userID string) string {
	return filepath.Join(recordingDir(dir, id), userID+".ogg")
}

// infoFileName is the info file's name, in the recording's directory and in
// its zip.
const infoFileName = "info.txt"

// infoPath is the path of a recording's info file.
func infoPath(dir string, id int64) string {
	return filepath.Join(recordingDir(dir, id), infoFileName)
}

// mixPath is the path of a recording's mix. RFC 7845 gives .opus as an Ogg
// Opus file's extension.
func mixPath(dir string, id int64) string {
	return filepath.Join(recordingDir(dir, id), "mix.opus")
}

// removeRecording deletes a recording whole: its directory, then its row.
// The files go first, because the row is what finds them: a failure part
// way leaves the row, and the recording listed for another try, never files
// no row leads to. Retention and an early delete from the panel both delete
// through it.
func removeRecording(ctx context.Context, st store.Store, dir string, id int64) error {
	if err := recordingRemoveAll(recordingDir(dir, id)); err != nil {
		return fmt.Errorf("delete the files of recording %d: %w", id, err)
	}
	if err := st.DeleteRecording(ctx, id); err != nil {
		return fmt.Errorf("delete the row of recording %d: %w", id, err)
	}
	return nil
}

// noteTrackNotOnDisk logs a speaker's track left out of the zip or the mix
// because it isn't on disk. A track the recording counts as lost logs
// nothing: the recording already says so beside its speaker's name. Any
// other, such as one removed by hand, is a WARN line.
func noteTrackNotOnDisk(rec store.Recording, sp store.Speaker) {
	if !sp.TrackLost {
		utils.Warn("Track not on disk", "recording_id", rec.ID, "user_id", sp.ID)
	}
}
