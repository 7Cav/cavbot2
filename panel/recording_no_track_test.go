package panel

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/net/html"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
)

// A recording with no track on disk (#648): nobody spoke in it, a crash cut
// it short before any audio reached the disk, or its tracks were removed by
// hand. Its zip holds the info file alone, so the Recordings page offers
// the info file in place of the tracks.

// noTrackRecording is one way a stopped recording ends up with no track on
// disk. record makes one, with the world's browser signed in as its
// starter. mixReady is set when its mix is ready.
type noTrackRecording struct {
	name     string
	record   recordingMaker
	mixReady bool
}

// noTrackRecordings is each way a recording ends up with no track on disk.
var noTrackRecordings = []noTrackRecording{
	{"nobody spoke", (*testWorld).recordSilence, false},
	{"cut short before any audio reached the disk", (*testWorld).recordCutShortWithNothingOnDisk, false},
	// Its mix was built before its tracks were removed.
	{"tracks removed by hand", (*testWorld).recordAndRemoveTracks, true},
}

// recordCutShortWithNothingOnDisk has the member with the Discord ID given
// start a recording, then leaves its disk as a crash before the first
// flush does: their track is there, created when they were first heard,
// with none of their audio in it yet. The restarted runtime's recovery
// closes the recording. It returns the recording's ID, and fails the test
// unless recovery left what it leaves after such a crash: the one speaker
// listed with their track lost, ended cut short, and a failed mix.
func (w *testWorld) recordCutShortWithNothingOnDisk(t *testing.T, discordID string) int64 {
	t.Helper()
	id := w.startSilentRecording(t, discordID)
	w.putTrack(t, id, discordID, nil)
	w.restartRecorder(t)
	rec := newestRecordingOf(t, w.store, discordID)
	if rec.ID != id || rec.Ended != store.RecordingEndCutShort || rec.Mix != store.MixFailed ||
		len(rec.Speakers) != 1 || !rec.Speakers[0].TrackLost {
		t.Fatalf("recording %d after recovery = %+v, want it cut short with its one speaker's track lost and its mix failed", id, rec)
	}
	return id
}

// recordAndRemoveTracks has the member with the Discord ID given record a
// recording they speak in, then removes its tracks from the disk the way
// someone clearing files by hand would. It returns the recording's ID, and
// fails the test unless the recording's zip then holds its info file alone
// and its mix still downloads.
func (w *testWorld) recordAndRemoveTracks(t *testing.T, discordID string) int64 {
	t.Helper()
	id := w.record(t, discordID)
	tracks, err := filepath.Glob(w.trackPath(id, "*"))
	if err != nil || len(tracks) == 0 {
		t.Fatalf("recording %d's tracks on disk = %v, %v, want at least one", id, tracks, err)
	}
	for _, path := range tracks {
		if err := os.Remove(path); err != nil {
			t.Fatalf("removing %s: %v", path, err)
		}
	}
	if names := zipEntryNames(t, w.b.get(commands.RecordingPath(id)+"/zip")); !slices.Equal(names, []string{"info.txt"}) {
		t.Fatalf("recording %d's zip with its tracks removed holds %v, want info.txt alone", id, names)
	}
	page := parseHTML(t, w.b.get(recordingsPath))
	if body := readBody(t, w.b.get(linkHref("mix-download")(t, page, id))); !bytes.Equal(body, testMix) {
		t.Fatalf("recording %d's mix with its tracks removed downloads as %q, want the mixer's %q", id, body, testMix)
	}
	return id
}

// readZip reads a zip download.
func readZip(t *testing.T, res *http.Response) *zip.Reader {
	t.Helper()
	body := readBody(t, res)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("reading the zip (status %d): %v", res.StatusCode, err)
	}
	return zr
}

// zipEntryNames reads a zip download and returns its entries' names.
func zipEntryNames(t *testing.T, res *http.Response) []string {
	t.Helper()
	zr := readZip(t, res)
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// zipEntry reads a zip download and returns the content of its entry with
// the name given, failing the test when it has none.
func zipEntry(t *testing.T, res *http.Response, name string) []byte {
	t.Helper()
	f, err := readZip(t, res).Open(name)
	if err != nil {
		t.Fatalf("the zip has no %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading the zip's %s: %v", name, err)
	}
	return content
}

// recordingViews is the recording with the ID given as the world's browser
// sees it: its block on the Recordings page, and its delete preview.
func recordingViews(t *testing.T, w *testWorld, id int64) (block, preview *html.Node) {
	t.Helper()
	page := parseHTML(t, w.b.get(recordingsPath))
	block, ok := listedRecordings(t, page)[id]
	if !ok {
		t.Fatalf("the Recordings page doesn't list recording %d", id)
	}
	preview, ok = listedRecordings(t, parseHTML(t, w.b.get(linkHref("delete")(t, page, id))))[id]
	if !ok {
		t.Fatalf("recording %d's delete preview doesn't show it", id)
	}
	return block, preview
}

// A recording with no track offers no track download on its block or its
// delete preview, and offers its info file instead.
func TestRecordingWithNoTrackOffersItsInfoFileInPlaceOfTheZip(t *testing.T) {
	for _, fx := range noTrackRecordings {
		t.Run(fx.name, func(t *testing.T) {
			w := newTestWorld(t)
			a := w.addStarter(5001, "Able.A", "discord-a")
			signInAs(t, w.forum, w.b, a.account)
			id := fx.record(w, t, a.discordID)

			block, preview := recordingViews(t, w, id)

			for where, view := range map[string]*html.Node{"its block": block, "its delete preview": preview} {
				if findElement(view, "a", "data-field", "zip-download") != nil {
					t.Errorf("%s offers each speaker's track", where)
				}
				if findElement(view, "a", "data-field", "info-download") == nil {
					t.Errorf("%s doesn't offer the info file", where)
				}
			}
		})
	}
}

// A recording with no track's delete preview lists no speaker's track
// among what goes for good. It lists the mix only when the mix is ready.
func TestDeletePreviewOfARecordingWithNoTrackListsNoTracks(t *testing.T) {
	for _, fx := range noTrackRecordings {
		t.Run(fx.name, func(t *testing.T) {
			w := newTestWorld(t)
			a := w.addStarter(5001, "Able.A", "discord-a")
			signInAs(t, w.forum, w.b, a.account)
			id := fx.record(w, t, a.discordID)

			_, preview := recordingViews(t, w, id)
			goes := findElement(preview, "", "data-list", "goes")

			if goes == nil {
				t.Fatal("the delete preview lists nothing that goes")
			}
			if findElement(goes, "", "data-field", "goes-tracks") != nil {
				t.Error("the delete preview lists each speaker's track among what goes")
			}
			if listed := findElement(goes, "", "data-field", "goes-mix") != nil; listed != fx.mixReady {
				t.Errorf("the delete preview lists the mix among what goes = %t, want %t", listed, fx.mixReady)
			}
		})
	}
}

// A recording with no track whose mix failed doesn't promise tracks on its
// Mix line. It says no speaker's audio was kept, so there was nothing to
// mix.
func TestFailedMixOfARecordingWithNoTrackPromisesNoTracks(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	signInAs(t, w.forum, w.b, a.account)
	id := w.recordCutShortWithNothingOnDisk(t, a.discordID)

	block, ok := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[id]

	if !ok {
		t.Fatalf("the Recordings page doesn't list recording %d", id)
	}
	mix := findElement(block, "", "data-field", "mix")
	if mix == nil {
		t.Fatal("the recording's block shows no Mix line")
	}
	if findElement(mix, "", "data-field", "tracks-downloadable") != nil {
		t.Error("its Mix line says the tracks can still be downloaded")
	}
	if findElement(mix, "", "data-field", "no-audio-kept") == nil {
		t.Error("its Mix line doesn't say no speaker's audio was kept")
	}
}

// A recording with a track on disk whose mix failed still offers its
// tracks: the zip and not the info file, its Mix line saying the tracks can
// still be downloaded, and "Each speaker's track" among what its delete
// preview says goes.
func TestRecordingWithATrackWhoseMixFailedStillOffersItsTracks(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	signInAs(t, w.forum, w.b, a.account)
	w.recorder(t).mixer.fail(errors.New("ffmpeg exited with status 1"))
	id := w.record(t, a.discordID)
	if rec := newestRecordingOf(t, w.store, a.discordID); rec.Mix != store.MixFailed {
		t.Fatalf("recording %d's mix = %q, want %q", id, rec.Mix, store.MixFailed)
	}

	block, preview := recordingViews(t, w, id)

	if findElement(block, "a", "data-field", "zip-download") == nil {
		t.Error("its block doesn't offer each speaker's track")
	}
	if findElement(block, "a", "data-field", "info-download") != nil {
		t.Error("its block offers the info file in the zip's place")
	}
	if mix := findElement(block, "", "data-field", "mix"); mix == nil || findElement(mix, "", "data-field", "tracks-downloadable") == nil {
		t.Error("its Mix line doesn't say the tracks can still be downloaded")
	}
	if goes := findElement(preview, "", "data-list", "goes"); goes == nil || findElement(goes, "", "data-field", "goes-tracks") == nil {
		t.Error("its delete preview doesn't list each speaker's track among what goes")
	}
}
