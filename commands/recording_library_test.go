package commands

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
// with err when a test sets one. Handed no tracks, or a track that isn't on
// disk, it fails as ffmpeg does. calls counts every mix it was asked for.
type fakeMixer struct {
	mu    sync.Mutex
	out   []byte
	err   error
	calls int
}

func (m *fakeMixer) Mix(_ context.Context, tracks []string, out string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if len(tracks) == 0 {
		return errors.New("ffmpeg: amix needs at least one input")
	}
	for _, track := range tracks {
		if _, err := os.Stat(track); err != nil {
			return fmt.Errorf("ffmpeg: %s: %w", track, err)
		}
	}
	if m.err != nil {
		return m.err
	}
	return os.WriteFile(out, m.out, 0o600)
}

// mixes is how many mixes the mixer was asked for.
func (m *fakeMixer) mixes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
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
	if got := sc.servedMix(t, rec); !bytes.Equal(got, want) {
		t.Errorf("the library served %q as the mix, want the mixer's %q", got, want)
	}
}

// servedMix downloads a recording's mix through the library and returns
// its bytes.
func (sc *recordScene) servedMix(t *testing.T, rec store.Recording) []byte {
	t.Helper()
	f, err := sc.lib.OpenMix(rec)
	if err != nil {
		t.Fatalf("OpenMix: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading the mix: %v", err)
	}
	return got
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

// A recording nobody spoke in has no track, so there is nothing to mix: it
// stops with no mix, the mixer is never asked for one, and Sentry hears
// nothing of it.
func TestRecordingNobodySpokeInHasNoMix(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	captures := countCaptures(t)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	recordAs(t, sc.rt, recStarter, "stop")
	sc.clock.advance(0)

	if rec := sc.onlyRecording(t); rec.Mix != store.MixNone {
		t.Errorf("mix of a recording nobody spoke in = %q, want %q", rec.Mix, store.MixNone)
	}
	if n := sc.mixer.mixes(); n != 0 {
		t.Errorf("the mixer was asked for %d mixes, want none", n)
	}
	if *captures != 0 {
		t.Errorf("a recording nobody spoke in sent %d events to Sentry, want none", *captures)
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

// removeTrack removes the track holding marker from the recordings
// directory, as someone clearing files by hand would.
func (sc *recordScene) removeTrack(t *testing.T, marker string) {
	t.Helper()
	if err := os.Remove(trackWith(t, sc.tracks(t), marker).path); err != nil {
		t.Fatalf("removing the track holding %q: %v", marker, err)
	}
}

// recordTwoAndRemoveOne records speakers A and B, lets the mix finish, and
// removes A's track from the disk.
func (sc *recordScene) recordTwoAndRemoveOne(t *testing.T) store.Recording {
	t.Helper()
	sc.recordSpeakers(t, speakerA, speakerB)
	sc.clock.advance(0)
	sc.removeTrack(t, speakerA)
	return sc.onlyRecording(t)
}

// A speaker's track gone from the disk leaves the zip whole (#623): it
// holds the other speaker's track and the info file, and writing it fails
// nothing.
func TestRecordingZipSkipsATrackNotOnDisk(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	rec := sc.recordTwoAndRemoveOne(t)

	entries := sc.zipEntries(t, rec)

	if got := entriesHolding(t, entries, speakerB); len(got) != 1 {
		t.Errorf("zip entries holding the kept track = %v, want one", got)
	}
	if got := entriesHolding(t, entries, speakerA); len(got) != 0 {
		t.Errorf("zip entries holding the removed track = %v, want none", got)
	}
	if _, ok := entries[infoFileName]; !ok {
		t.Errorf("the zip holds no %s", infoFileName)
	}
}

// A track gone from the disk that the recording doesn't count as lost, such
// as one removed by hand, is one WARN line naming the recording and the
// speaker when the zip skips it, and nothing reaches Sentry.
func TestRecordingZipWarnsOfATrackGoneThatWasNotLost(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	rec := sc.recordTwoAndRemoveOne(t)
	logs := captureLogs(t)
	captures := countCaptures(t)

	sc.zipEntries(t, rec)

	fields := map[string]string{"recording_id": strconv.FormatInt(rec.ID, 10), "user_id": speakerA}
	if warns := logRecordsWith(t, logs, "WARN", fields); len(warns) != 1 {
		t.Errorf("WARN lines naming the recording and the removed track's speaker = %v, want one", warns)
	}
	if *captures != 0 {
		t.Errorf("the zip sent %d events to Sentry, want none", *captures)
	}
}

// removeFilesNamed removes every file under the recordings directory whose
// name is name, failing the test when there is none.
func (sc *recordScene) removeFilesNamed(t *testing.T, name string) {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(sc.dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		t.Fatalf("reading the recordings directory: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no %s under the recordings directory", name)
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatalf("removing %s: %v", path, err)
		}
	}
}

// removeEverything empties the recordings directory.
func (sc *recordScene) removeEverything(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(sc.dir)
	if err != nil {
		t.Fatalf("reading the recordings directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the recordings directory is already empty")
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(sc.dir, e.Name())); err != nil {
			t.Fatalf("removing %s: %v", e.Name(), err)
		}
	}
}

// An info file gone from the disk is rendered from the recording's row for
// the zip (#623): the zip holds every track, and an info file that names
// the recording's title, each speaker, and the starter by Discord ID.
func TestRecordingZipRendersAnInfoFileNotOnDisk(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	names := map[string]string{speakerA: "doej", speakerB: "smitha"}
	sc.nameSpeakers(names)
	const title = "S2 interview"
	sc.fake.setVoice(recStarter.id, recordChannel)
	recordAs(t, sc.rt, recStarter, "start", stringOption("title", title))
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, speakerA))
	sc.hear(t, recordChannel, frame(speakerB, 22, 5000, speakerB))
	recordAs(t, sc.rt, recStarter, "stop")
	sc.clock.advance(0)
	sc.removeFilesNamed(t, infoFileName)

	entries := sc.zipEntries(t, sc.onlyRecording(t))

	for _, id := range []string{speakerA, speakerB} {
		if got := entriesHolding(t, entries, id); len(got) != 1 {
			t.Errorf("zip entries holding %s's track = %v, want one", id, got)
		}
	}
	info, ok := entries[infoFileName]
	if !ok {
		t.Fatalf("the zip holds no %s", infoFileName)
	}
	for _, want := range []string{title, names[speakerA], names[speakerB], recStarter.id} {
		if !bytes.Contains(info, []byte(want)) {
			t.Errorf("the rendered info file doesn't name %q:\n%s", want, info)
		}
	}
}

// A recording with none of its files left on disk still downloads: the zip
// holds the info file rendered from its row, and nothing else.
func TestRecordingZipOfARecordingWithNoFilesHoldsOnlyTheInfoFile(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordSpeakers(t, speakerA, speakerB)
	sc.clock.advance(0)
	sc.removeEverything(t)

	entries := sc.zipEntries(t, sc.onlyRecording(t))

	if _, ok := entries[infoFileName]; len(entries) != 1 || !ok {
		names := slices.Sorted(maps.Keys(entries))
		t.Errorf("the zip holds %v, want %s alone", names, infoFileName)
	}
}

// trackLostVerbatim is what marks a lost track's speaker wherever a recording
// names its speakers. The brief fixes it verbatim, so tests quote it.
const trackLostVerbatim = "(track lost)"

// createTracksWith makes create create every track from now on, until the
// test ends.
func createTracksWith(t *testing.T, create func(string) (*os.File, error)) {
	t.Helper()
	prev := recordingCreateTrack
	recordingCreateTrack = create
	t.Cleanup(func() { recordingCreateTrack = prev })
}

// failTrackCreates makes every track created from now on fail, as a full or
// read-only disk does.
func failTrackCreates(t *testing.T) {
	t.Helper()
	createTracksWith(t, func(string) (*os.File, error) { return nil, errors.New("no space left on device") })
}

// tracksLost maps each of a recording's speakers to whether their track
// was lost.
func tracksLost(rec store.Recording) map[string]bool {
	lost := map[string]bool{}
	for _, sp := range rec.Speakers {
		lost[sp.ID] = sp.TrackLost
	}
	return lost
}

// recordWithALostTrack records doej (speaker A) and smitha (speaker B),
// whose track can't be created when they first speak, and stops.
func (sc *recordScene) recordWithALostTrack(t *testing.T) store.Recording {
	t.Helper()
	sc.nameSpeakers(map[string]string{speakerA: "doej", speakerB: "smitha"})
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, speakerA))
	failTrackCreates(t)
	sc.hear(t, recordChannel, frame(speakerB, 22, 5000, speakerB))
	recordAs(t, sc.rt, recStarter, "stop")
	return sc.onlyRecording(t)
}

// A speaker whose track couldn't be created stays a speaker after the stop,
// with their track counted as lost. The other speaker's isn't.
func TestRecordingStopListsASpeakerWhoseTrackFailedAsLost(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	rec := sc.recordWithALostTrack(t)

	if lost, want := tracksLost(rec), map[string]bool{speakerA: false, speakerB: true}; !maps.Equal(lost, want) {
		t.Errorf("speakers' tracks lost = %v, want %v", lost, want)
	}
}

// infoLine is the line of an info file that names name, failing the test
// unless exactly one does.
func infoLine(t *testing.T, info []byte, name string) string {
	t.Helper()
	var found []string
	for line := range strings.Lines(string(info)) {
		if strings.Contains(line, name) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("info file lines naming %s = %q, want one:\n%s", name, found, info)
	}
	return found[0]
}

// The info file marks the speaker whose track was lost, after their name,
// and leaves the other speaker unmarked.
func TestRecordingInfoFileMarksALostTrack(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	rec := sc.recordWithALostTrack(t)

	info := sc.zipEntries(t, rec)[infoFileName]

	lostLine := infoLine(t, info, "smitha")
	if i := strings.Index(lostLine, trackLostVerbatim); i < strings.Index(lostLine, "smitha") {
		t.Errorf("the lost track's speaker's line is %q, want %s after their name", lostLine, trackLostVerbatim)
	}
	if keptLine := infoLine(t, info, "doej"); strings.Contains(keptLine, trackLostVerbatim) {
		t.Errorf("the kept track's speaker's line is %q, want no %s", keptLine, trackLostVerbatim)
	}
}

// A recording with a lost track still downloads whole: the zip holds the
// other speaker's track.
func TestRecordingZipOfARecordingWithALostTrackHoldsTheOthers(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	rec := sc.recordWithALostTrack(t)

	entries := sc.zipEntries(t, rec)

	if got := entriesHolding(t, entries, speakerA); len(got) != 1 {
		t.Errorf("zip entries holding the kept track = %v, want one", got)
	}
}

// A lost track the zip leaves out was reported when it was lost, so the
// zip logs no WARN line for it and sends nothing to Sentry.
func TestRecordingZipIsQuietAboutALostTrack(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	rec := sc.recordWithALostTrack(t)
	logs := captureLogs(t)
	captures := countCaptures(t)

	sc.zipEntries(t, rec)

	if warns := logRecordsWith(t, logs, "WARN", map[string]string{"user_id": speakerB}); len(warns) != 0 {
		t.Errorf("WARN lines naming the lost track's speaker = %v, want none", warns)
	}
	if *captures != 0 {
		t.Errorf("the zip sent %d events to Sentry, want none", *captures)
	}
}

// fillTrackWrites makes every track created from now on refuse its writes
// with ENOSPC, as a full disk does: the file opens, but none of the audio
// reaches it. It hands back Linux's /dev/full, and skips the test where
// there is none.
func fillTrackWrites(t *testing.T) {
	t.Helper()
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("no /dev/full to stand in for a full disk: %v", err)
	}
	_ = full.Close()
	createTracksWith(t, func(string) (*os.File, error) { return os.OpenFile("/dev/full", os.O_WRONLY, 0) })
}

// A speaker whose track's audio first meets a full disk at the stop, when
// the stop writes it out, is listed with their track lost. The other
// speaker isn't.
func TestRecordingStopListsASpeakerWhoseTrackFailedAtTheStopAsLost(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, speakerA))
	fillTrackWrites(t)
	sc.hear(t, recordChannel, frame(speakerB, 22, 5000, speakerB))
	recordAs(t, sc.rt, recStarter, "stop")

	if lost, want := tracksLost(sc.onlyRecording(t)), map[string]bool{speakerA: false, speakerB: true}; !maps.Equal(lost, want) {
		t.Errorf("speakers' tracks lost = %v, want %v", lost, want)
	}
}

// A track gone from the disk before the mix is built leaves the mix to the
// tracks that are: it ends ready.
func TestRecordingMixIsBuiltFromTheTracksOnDisk(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.recordSpeakers(t, speakerA, speakerB)
	sc.removeTrack(t, speakerA)

	sc.clock.advance(0)

	if rec := sc.onlyRecording(t); rec.Mix != store.MixReady {
		t.Errorf("mix with a track gone from the disk = %q, want %q", rec.Mix, store.MixReady)
	}
}

// A speaker whose track failed partway is listed with their track lost, but
// what reached the disk before the failure is still their track: the
// recording has a track, which the Recordings page reads to offer the zip
// (#648).
func TestRecordingWhoseOnlyTrackFailedPartwayStillHasATrack(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	var mu sync.Mutex
	var track *os.File
	createTracksWith(t, func(path string) (*os.File, error) {
		f, err := os.Create(filepath.Clean(path))
		mu.Lock()
		track = f
		mu.Unlock()
		return f, err
	})
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(trackFlushInterval)
	// The file closes under the track, so its next flush fails.
	mu.Lock()
	_ = track.Close()
	mu.Unlock()
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000+opusFrameSamples, "a2"))
	sc.clock.advance(trackFlushInterval)
	recordAs(t, sc.rt, recStarter, "stop")
	rec := sc.onlyRecording(t)
	if lost := tracksLost(rec); !maps.Equal(lost, map[string]bool{speakerA: true}) {
		t.Fatalf("speakers' tracks lost = %v, want %s's alone", lost, speakerA)
	}
	if got := entriesHolding(t, sc.zipEntries(t, rec), "a1"); len(got) != 1 {
		t.Fatalf("zip entries holding what reached the disk = %v, want one", got)
	}

	if !sc.lib.HasTrack(rec) {
		t.Error("a recording whose only track failed partway has no track, want one")
	}
}

// A crash before the first flush leaves nothing its speaker said on the
// disk: recovery keeps them listed with their track lost, and the recording
// has no track (#648).
func TestRecordingCutShortBeforeAnyAudioReachedTheDiskHasNoTrack(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))

	sc.restart(t, testRecorder)
	sc.clock.advance(0)
	rec := sc.onlyRecording(t)
	if lost := tracksLost(rec); !maps.Equal(lost, map[string]bool{speakerA: true}) {
		t.Fatalf("speakers' tracks lost after recovery = %v, want %s's alone", lost, speakerA)
	}

	if sc.lib.HasTrack(rec) {
		t.Error("a recording cut short before any audio reached the disk has a track, want none")
	}
}
