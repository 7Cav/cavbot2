package commands

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
)

// The mix and the downloads (#389). Every case records through /record with
// the fake voice connection, stops, and reads the recording back through the
// library the panel uses. The mixer is a fake that writes the bytes a test
// gives it, or fails when told to. The runtime hands it the tracks on a
// goroutine of its own, which the fake clock runs at its next advance.

// fakeMixer stands in for ffmpeg. It writes out to the mix's path, or fails
// with err when a test sets one.
type fakeMixer struct {
	mu  sync.Mutex
	out []byte
	err error
}

func (m *fakeMixer) Mix(_ context.Context, _ []string, out string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	return os.WriteFile(out, m.out, 0o600)
}

// set makes the next mix write out, or fail with err when it isn't nil.
func (m *fakeMixer) set(out []byte, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out, m.err = out, err
}

// onlyRecording reads the one recording the library lists, failing the
// test unless it lists exactly one.
func (sc *recordScene) onlyRecording(t *testing.T) store.Recording {
	t.Helper()
	recs, err := sc.lib.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("the library lists %d recordings, want one", len(recs))
	}
	return recs[0]
}

// recordOneSpeaker starts a recording, has one speaker say one frame, and
// stops it.
func (sc *recordScene) recordOneSpeaker(t *testing.T) {
	t.Helper()
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	recordAs(t, sc.rt, recStarter, "stop")
}

// recordSpeakers starts a recording, has each speaker say one frame whose
// marker is their user ID, and stops it.
func (sc *recordScene) recordSpeakers(t *testing.T, userIDs ...string) {
	t.Helper()
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	for i, id := range userIDs {
		sc.hear(t, recordChannel, frame(id, uint32(100+i), 5000, id))
	}
	recordAs(t, sc.rt, recStarter, "stop")
}

// zipEntries downloads a recording's zip through the library and returns
// each entry's name and content.
func (sc *recordScene) zipEntries(t *testing.T, rec store.Recording) map[string][]byte {
	t.Helper()
	var buf bytes.Buffer
	if err := sc.lib.WriteZip(&buf, rec); err != nil {
		t.Fatalf("WriteZip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading the zip: %v", err)
	}
	entries := make(map[string][]byte)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s in the zip: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("reading %s in the zip: %v", f.Name, err)
		}
		if _, dup := entries[f.Name]; dup {
			t.Fatalf("the zip holds %s twice", f.Name)
		}
		entries[f.Name] = data
	}
	return entries
}

// entriesHolding lists the zip entries that are Ogg tracks holding marker.
func entriesHolding(t *testing.T, entries map[string][]byte, marker string) []string {
	t.Helper()
	var names []string
	for name, data := range entries {
		if !bytes.HasPrefix(data, []byte("OggS")) {
			continue
		}
		if _, ok := parseOgg(t, name, data).at(marker); ok {
			names = append(names, name)
		}
	}
	return names
}

// A stopped recording's mix is processing, and the library won't serve it,
// until the mixer has built it. Then the library lists it ready and serves
// the mixer's bytes.
func TestRecordingMixIsProcessingUntilTheMixerCompletes(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordOneSpeaker(t)

	rec := sc.onlyRecording(t)
	if rec.Mix != store.MixProcessing {
		t.Errorf("mix after the stop = %q, want %q", rec.Mix, store.MixProcessing)
	}
	if f, err := sc.lib.OpenMix(rec); err == nil {
		_ = f.Close()
		t.Error("the library served the mix while it was processing")
	}

	want := []byte("the mixer's mix")
	sc.mixer.set(want, nil)
	sc.clock.advance(0)

	rec = sc.onlyRecording(t)
	if rec.Mix != store.MixReady {
		t.Fatalf("mix after the mixer completed = %q, want %q", rec.Mix, store.MixReady)
	}
	f, err := sc.lib.OpenMix(rec)
	if err != nil {
		t.Fatalf("OpenMix: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading the mix: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the library served %q as the mix, want the mixer's %q", got, want)
	}
}

// A mix that fails is marked failed and reaches Sentry, and the tracks still
// download: the zip holds each speaker's track.
func TestRecordingMixFailureLeavesTheTracksDownloadable(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	captures := countCaptures(t)
	sc.recordSpeakers(t, speakerA, speakerB)

	sc.mixer.set(nil, errors.New("ffmpeg exited with status 1"))
	before := *captures
	sc.clock.advance(0)

	rec := sc.onlyRecording(t)
	if rec.Mix != store.MixFailed {
		t.Errorf("mix after the mixer failed = %q, want %q", rec.Mix, store.MixFailed)
	}
	if *captures == before {
		t.Error("the mix failure didn't reach Sentry")
	}
	entries := sc.zipEntries(t, rec)
	for _, speaker := range []string{speakerA, speakerB} {
		if got := entriesHolding(t, entries, speaker); len(got) != 1 {
			t.Errorf("zip entries holding %s's track = %v, want one", speaker, got)
		}
	}
}

// nameSpeakers gives each speaker a display name in the guild's member list,
// as their server nickname.
func (sc *recordScene) nameSpeakers(names map[string]string) {
	for id, name := range names {
		sc.fake.setMember(ListedMember{ID: id, Username: "user", Nick: name})
	}
}

// entriesNamed lists the zip entries whose name contains name.
func entriesNamed(entries map[string][]byte, name string) []string {
	var out []string
	for entry := range entries {
		if strings.Contains(entry, name) {
			out = append(out, entry)
		}
	}
	return out
}

// The zip holds each speaker's track, under a name that carries their
// display name, and one more entry, the info file, which mentions every
// speaker by display name.
func TestRecordingZipNamesEachTrackForItsSpeakerAndHoldsTheInfoFile(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	names := map[string]string{speakerA: "doej", speakerB: "smitha"}
	sc.nameSpeakers(names)
	sc.recordSpeakers(t, speakerA, speakerB)

	entries := sc.zipEntries(t, sc.onlyRecording(t))

	if len(entries) != len(names)+1 {
		t.Errorf("the zip holds %d entries, want %d: a track per speaker and the info file", len(entries), len(names)+1)
	}
	tracks := make(map[string]bool)
	for id, name := range names {
		named := entriesNamed(entries, name)
		if len(named) != 1 {
			t.Errorf("zip entries named for %s = %v, want one", name, named)
			continue
		}
		if !slices.Contains(entriesHolding(t, entries, id), named[0]) {
			t.Errorf("zip entry %s doesn't hold %s's track", named[0], name)
		}
		tracks[named[0]] = true
	}
	for entry, data := range entries {
		if tracks[entry] {
			continue
		}
		for _, name := range names {
			if !bytes.Contains(data, []byte(name)) {
				t.Errorf("the info file %s doesn't mention %s", entry, name)
			}
		}
	}
}

// Two speakers who share a display name get an entry each in the zip, both
// named for them, each holding that speaker's own track.
func TestRecordingZipKeepsSpeakersWhoShareADisplayNameApart(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.nameSpeakers(map[string]string{speakerA: "doej", speakerB: "doej"})
	sc.recordSpeakers(t, speakerA, speakerB)

	entries := sc.zipEntries(t, sc.onlyRecording(t))

	named := entriesNamed(entries, "doej")
	if len(named) != 2 {
		t.Fatalf("zip entries named for doej = %v, want two", named)
	}
	for _, id := range []string{speakerA, speakerB} {
		if holding := entriesHolding(t, entries, id); len(holding) != 1 || !slices.Contains(named, holding[0]) {
			t.Errorf("zip entries holding %s's track = %v, want one of %v", id, holding, named)
		}
	}
}

// starters lists the starter of each recording, in the order given.
func starters(recs []store.Recording) []string {
	var out []string
	for _, rec := range recs {
		out = append(out, rec.StarterID)
	}
	return out
}

// A member's list holds the recording they started and not one another
// member started. The list of all recordings holds both.
func TestRecordingLibraryListsAStartersOwnRecordings(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	for _, m := range []permMember{recStarter, recHolder} {
		sc.fake.setVoice(m.id, recordChannel)
		recordAs(t, sc.rt, m, "start")
		recordAs(t, sc.rt, m, "stop")
	}
	ctx := context.Background()

	own, err := sc.lib.StartedBy(ctx, recStarter.id)
	if err != nil {
		t.Fatalf("StartedBy: %v", err)
	}
	all, err := sc.lib.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	if got := starters(own); !slices.Equal(got, []string{recStarter.id}) {
		t.Errorf("%s's recordings were started by %v, want %s's alone", recStarter.id, got, recStarter.id)
	}
	if got := starters(all); len(got) != 2 || !slices.Contains(got, recStarter.id) || !slices.Contains(got, recHolder.id) {
		t.Errorf("all recordings were started by %v, want %s's and %s's", got, recStarter.id, recHolder.id)
	}
}

// A display name that climbs out of a folder, "../doej", names its track's
// zip entry without a "/", so extracting the zip puts every track in the
// one folder.
func TestRecordingZipEntryNamesStayInTheirFolder(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.nameSpeakers(map[string]string{speakerA: "../doej"})
	sc.recordSpeakers(t, speakerA)

	entries := sc.zipEntries(t, sc.onlyRecording(t))

	holding := entriesHolding(t, entries, speakerA)
	if len(holding) != 1 {
		t.Fatalf("zip entries holding the speaker's track = %v, want one", holding)
	}
	if name := holding[0]; strings.Contains(name, "/") || !strings.Contains(name, "doej") {
		t.Errorf("the track's zip entry is %q, want a name with no / that still carries doej", name)
	}
}
