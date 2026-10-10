package commands

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/7cav/cavbot2/store"
)

// The recording library (spec #381, #389): the recordings as the panel reads
// them. It lists them from the store and streams their files from the
// recordings directory, so it works whether recording is on or off.
//
// A recording's directory, named by its ID, holds one Ogg Opus track per
// speaker named by their Discord ID, the info file, and the mix once it is
// built.

// recordingDir is a recording's directory under the recordings directory.
func recordingDir(dir string, id int64) string {
	return filepath.Join(dir, strconv.FormatInt(id, 10))
}

// trackFileName is the file name of a speaker's track.
func trackFileName(userID string) string {
	return userID + ".ogg"
}

// trackPath is the path of a speaker's track in a recording.
func trackPath(dir string, id int64, userID string) string {
	return filepath.Join(recordingDir(dir, id), trackFileName(userID))
}

// mixPath is the path of a recording's mix. RFC 7845 gives .opus as an Ogg
// Opus file's extension.
func mixPath(dir string, id int64) string {
	return filepath.Join(recordingDir(dir, id), "mix.opus")
}

// errMixNotReady: the recording's mix is processing or failed, so there is
// nothing to download.
var errMixNotReady = errors.New("recording: mix not ready")

// RecordingLibrary reads the recordings of one guild.
type RecordingLibrary struct {
	st      store.Store
	guildID string
	dir     string
}

// NewRecordingLibrary builds the library over the store and the recordings
// directory.
func NewRecordingLibrary(st store.Store, guildID, dir string) *RecordingLibrary {
	return &RecordingLibrary{st: st, guildID: guildID, dir: dir}
}

// All lists every recording, the newest first.
func (l *RecordingLibrary) All(ctx context.Context) ([]store.Recording, error) {
	recs, err := l.st.ListRecordings(ctx, l.guildID)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(recs, func(a, b store.Recording) int {
		return cmp.Or(b.StartedAt.Compare(a.StartedAt), cmp.Compare(b.ID, a.ID))
	})
	return recs, nil
}

// OpenMix opens a recording's mix for reading. errMixNotReady unless the
// mix is ready.
func (l *RecordingLibrary) OpenMix(rec store.Recording) (*os.File, error) {
	if rec.Mix != store.MixReady {
		return nil, errMixNotReady
	}
	f, err := os.Open(mixPath(l.dir, rec.ID))
	if err != nil {
		return nil, fmt.Errorf("open the mix of recording %d: %w", rec.ID, err)
	}
	return f, nil
}
