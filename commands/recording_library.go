package commands

import (
	"archive/zip"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/7cav/cavbot2/store"
)

// The recording library (spec #381, #389): the recordings as the panel reads
// them. It lists them from the store and streams their files from the
// recordings directory (recording_files.go), so it works whether recording
// is on or off.

// RecordingPath is the panel's path of a recording's page, which the link
// a stop gives leads to.
func RecordingPath(id int64) string {
	return "/recordings/" + strconv.FormatInt(id, 10)
}

// ErrMixNotReady: the recording's mix is processing or failed, so there is
// nothing to download.
var ErrMixNotReady = errors.New("recording: mix not ready")

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

// StartedBy lists the recordings the member with the Discord ID given
// started, the newest first.
func (l *RecordingLibrary) StartedBy(ctx context.Context, discordID string) ([]store.Recording, error) {
	recs, err := l.All(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(recs, func(rec store.Recording) bool { return rec.StarterID != discordID }), nil
}

// OpenMix opens a recording's mix for reading. ErrMixNotReady unless the
// mix is ready.
func (l *RecordingLibrary) OpenMix(rec store.Recording) (*os.File, error) {
	if rec.Mix != store.MixReady {
		return nil, ErrMixNotReady
	}
	f, err := os.Open(mixPath(l.dir, rec.ID))
	if err != nil {
		return nil, fmt.Errorf("open the mix of recording %d: %w", rec.ID, err)
	}
	return f, nil
}

// WriteZip writes a zip of a recording's tracks and its info file to w,
// each track named by its speaker's display name. Tracks are already
// compressed, so they go in stored as they are.
func (l *RecordingLibrary) WriteZip(w io.Writer, rec store.Recording) error {
	zw := zip.NewWriter(w)
	names := trackEntryNames(rec.Speakers)
	for i, sp := range rec.Speakers {
		if err := l.addTrack(zw, rec, sp, names[i]); err != nil {
			return err
		}
	}
	info, err := os.ReadFile(infoPath(l.dir, rec.ID))
	if err != nil {
		return fmt.Errorf("read the info file of recording %d: %w", rec.ID, err)
	}
	entry, err := zw.CreateHeader(&zip.FileHeader{Name: infoFileName, Method: zip.Deflate, Modified: rec.StoppedAt})
	if err != nil {
		return err
	}
	if _, err := entry.Write(info); err != nil {
		return err
	}
	return zw.Close()
}

// addTrack copies a speaker's track into the zip under name.
func (l *RecordingLibrary) addTrack(zw *zip.Writer, rec store.Recording, sp store.Speaker, name string) error {
	f, err := os.Open(trackPath(l.dir, rec.ID, sp.ID))
	if err != nil {
		return fmt.Errorf("open the track of %s in recording %d: %w", sp.ID, rec.ID, err)
	}
	defer func() { _ = f.Close() }()
	entry, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: rec.StoppedAt})
	if err != nil {
		return err
	}
	if _, err := io.Copy(entry, f); err != nil {
		return fmt.Errorf("zip the track of %s in recording %d: %w", sp.ID, rec.ID, err)
	}
	return nil
}

// trackEntryNames names each speaker's track in a zip, in the speakers'
// order: their display name made safe for a file name, with " (2)", " (3)"
// and so on after a name an earlier speaker took. Names are compared
// without case, since Windows and macOS extract "Doe" and "doe" to one file.
func trackEntryNames(speakers []store.Speaker) []string {
	names := make([]string, 0, len(speakers))
	taken := make(map[string]bool)
	for _, sp := range speakers {
		base := cmp.Or(safeFileName(sp.DisplayName), sp.ID)
		name := base
		for n := 2; taken[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		taken[strings.ToLower(name)] = true
		names = append(names, name+".ogg")
	}
	return names
}

// safeFileName makes a display name safe as a file name: every character
// Windows forbids in one, a path separator among them, and every control
// character becomes "_", and leading and trailing spaces and dots go, so
// no name climbs out of the folder the zip extracts to.
func safeFileName(name string) string {
	safe := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	return strings.Trim(safe, " .")
}
