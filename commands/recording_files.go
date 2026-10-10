package commands

import (
	"path/filepath"
	"strconv"
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
