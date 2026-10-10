package commands

import (
	"bytes"
	"context"
	"io"
	"os"
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
