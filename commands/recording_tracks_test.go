package commands

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/voice"
)

// Tracks (#387). Every case starts a recording through /record, has the
// fake voice connection hand the runtime frames at fake-clock times, and
// reads the Ogg files the runtime left under the recordings directory. A
// speaker's audio is found by a marker the test put in their frame, at the
// sum of the durations of every packet before it, never by a file's name or
// path.

// The speakers. Neither is a member of the fixture: a speaker needn't be.
const (
	speakerA = "user-speaker-a"
	speakerB = "user-speaker-b"
)

// opusFrameSamples is one 20 ms Opus frame at 48 kHz, the tolerance a
// marker's position is read to.
const opusFrameSamples = 960

// markerFrame is an Opus packet of one 20 ms frame (TOC 0xF8: CELT fullband,
// 20 ms, one frame) carrying marker, so a test finds it in a track. Nothing
// decodes it, and the runtime copies it as Discord sent it.
func markerFrame(marker string) []byte {
	return append([]byte{0xF8}, marker...)
}

// frame is the voice frame a speaker sends with a marker.
func frame(userID string, ssrc, timestamp uint32, marker string) voice.Frame {
	return voice.Frame{UserID: userID, SSRC: ssrc, Timestamp: timestamp, Opus: markerFrame(marker)}
}

// hear hands f to the runtime through the recorder in the channel, at the
// clock's now, the way the voice adapter delivers a frame.
func (sc *recordScene) hear(t *testing.T, channelID string, f voice.Frame) {
	t.Helper()
	sc.voice.mu.Lock()
	var handle func(voice.Frame)
	for _, c := range sc.voice.conns {
		if c.channelID == channelID && !c.left {
			handle = c.handle
		}
	}
	sc.voice.mu.Unlock()
	if handle == nil {
		t.Fatalf("no recorder in %s is receiving", channelID)
	}
	handle(f)
}

// oggTrack is one track as read from disk: its audio packets, past the two
// Opus header packets, and its last page's granule position.
type oggTrack struct {
	path    string
	packets [][]byte
	granule uint64
}

// tracks reads every .ogg file under the recordings directory.
func (sc *recordScene) tracks(t *testing.T) []oggTrack {
	t.Helper()
	var out []oggTrack
	err := filepath.WalkDir(sc.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".ogg") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, parseOgg(t, path, data))
		return nil
	})
	if err != nil {
		t.Fatalf("reading the recordings directory: %v", err)
	}
	return out
}

// parseOgg reads an Ogg stream's pages (RFC 3533) back into packets, and
// stops at a page cut short.
func parseOgg(t *testing.T, path string, data []byte) oggTrack {
	t.Helper()
	tr := oggTrack{path: path}
	var packets [][]byte
	var partial []byte
	for len(data) >= 27 {
		if string(data[:4]) != "OggS" {
			t.Fatalf("%s: no page capture pattern where a page should start", path)
		}
		granule := binary.LittleEndian.Uint64(data[6:14])
		segments := int(data[26])
		if len(data) < 27+segments {
			break
		}
		lacing := data[27 : 27+segments]
		size := 0
		for _, l := range lacing {
			size += int(l)
		}
		body := data[27+segments:]
		if len(body) < size {
			break
		}
		for _, l := range lacing {
			partial = append(partial, body[:l]...)
			body = body[l:]
			if l < 255 {
				packets = append(packets, partial)
				partial = nil
			}
		}
		tr.granule = granule
		data = data[27+segments+size:]
	}
	if len(packets) < 2 || !bytes.HasPrefix(packets[0], []byte("OpusHead")) || !bytes.HasPrefix(packets[1], []byte("OpusTags")) {
		t.Fatalf("%s: no Opus header packets", path)
	}
	tr.packets = packets[2:]
	return tr
}

// opusSamples is a packet's duration in 48 kHz samples, read from its TOC
// byte (RFC 6716, section 3.1).
func opusSamples(pkt []byte) int {
	if len(pkt) == 0 {
		return 0
	}
	config := pkt[0] >> 3
	var size int
	switch {
	case config < 12: // SILK: 10, 20, 40, 60 ms
		size = []int{480, 960, 1920, 2880}[config%4]
	case config < 16: // Hybrid: 10, 20 ms
		size = []int{480, 960}[config%2]
	default: // CELT: 2.5, 5, 10, 20 ms
		size = []int{120, 240, 480, 960}[config%4]
	}
	switch pkt[0] & 3 {
	case 0:
		return size
	case 1, 2:
		return 2 * size
	default:
		if len(pkt) < 2 {
			return 0
		}
		return int(pkt[1]&0x3F) * size
	}
}

// at is where marker sits in the track, in samples from the track's start,
// and false when the track doesn't hold it.
func (tr oggTrack) at(marker string) (int, bool) {
	want := markerFrame(marker)
	pos := 0
	for _, p := range tr.packets {
		if bytes.Equal(p, want) {
			return pos, true
		}
		pos += opusSamples(p)
	}
	return 0, false
}

// trackWith returns the one track holding marker, failing the test unless
// exactly one does.
func trackWith(t *testing.T, tracks []oggTrack, marker string) oggTrack {
	t.Helper()
	var found []oggTrack
	for _, tr := range tracks {
		if _, ok := tr.at(marker); ok {
			found = append(found, tr)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d tracks hold %q, want one", len(found), marker)
	}
	return found[0]
}

// Two speakers each get a track of their own: exactly two tracks, one with
// every one of A's words and none of B's, the other the reverse.
func TestRecordingKeepsOneTrackPerSpeaker(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)

	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.hear(t, recordChannel, frame(speakerB, 22, 90000, "b1"))
	sc.clock.advance(20 * time.Millisecond)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5960, "a2"))
	sc.hear(t, recordChannel, frame(speakerB, 22, 90960, "b2"))
	recordAs(t, sc.rt, recStarter, "stop")

	tracks := sc.tracks(t)
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2, one per speaker", len(tracks))
	}
	for _, sp := range []struct{ own, other []string }{
		{own: []string{"a1", "a2"}, other: []string{"b1", "b2"}},
		{own: []string{"b1", "b2"}, other: []string{"a1", "a2"}},
	} {
		tr := trackWith(t, tracks, sp.own[0])
		for _, m := range sp.own[1:] {
			if _, ok := tr.at(m); !ok {
				t.Errorf("the track holding %q lacks %q, from the same speaker", sp.own[0], m)
			}
		}
		for _, m := range sp.other {
			if _, ok := tr.at(m); ok {
				t.Errorf("the track holding %q also holds %q, from the other speaker", sp.own[0], m)
			}
		}
	}
}

// placed is where marker sits in its one track, failing the test unless
// exactly one track holds it.
func placed(t *testing.T, tracks []oggTrack, marker string) int {
	t.Helper()
	pos, _ := trackWith(t, tracks, marker).at(marker)
	return pos
}

// assertAt checks that marker sits at want samples into its track, within
// one 20 ms frame.
func assertAt(t *testing.T, tracks []oggTrack, marker string, want int) {
	t.Helper()
	if got := placed(t, tracks, marker); got < want-opusFrameSamples || got > want+opusFrameSamples {
		t.Errorf("%q at sample %d (%.3f s), want %d (%.3f s) within one frame",
			marker, got, float64(got)/48000, want, float64(want)/48000)
	}
}

// samplesIn is a time from the recording's start in 48 kHz samples.
func samplesIn(d time.Duration) int {
	return int(d.Seconds() * 48000)
}

// A frame that arrives 300 ms late, whose RTP timestamp says it follows the
// one before directly, lands directly after it, not at the time it arrived:
// jitter doesn't chop up continuous speech.
func TestRecordingPlacesALateFrameByItsRTPTimestamp(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)

	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(20*time.Millisecond + 300*time.Millisecond)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000+opusFrameSamples, "a2"))
	recordAs(t, sc.rt, recStarter, "stop")

	tracks := sc.tracks(t)
	assertAt(t, tracks, "a2", placed(t, tracks, "a1")+opusFrameSamples)
}

// A speaker who rejoins comes back on a new SSRC, whose RTP timestamps
// start from a base of their own. Their first frame on it lands at the time
// it arrived, in the track they already have. Here the new SSRC's timestamp
// sits 1.5 s past where the old one's next frame would have gone, close
// enough to the clock that only the new SSRC, not a disagreement with the
// clock, can put it at clock time.
func TestRecordingPlacesARejoiningSpeakerByTheClockInTheirTrack(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)

	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(3 * time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 12, uint32(5000+opusFrameSamples+samplesIn(1500*time.Millisecond)), "a2"))
	recordAs(t, sc.rt, recStarter, "stop")

	tracks := sc.tracks(t)
	if a1, a2 := trackWith(t, tracks, "a1"), trackWith(t, tracks, "a2"); a1.path != a2.path {
		t.Errorf("a1 is in %s and a2 in %s, want one track for the speaker", a1.path, a2.path)
	}
	assertAt(t, tracks, "a2", samplesIn(4*time.Second))
}

// When a frame's RTP timestamp disagrees with the clock by more than 2 s,
// the clock wins: here the timestamp jumps 5 s while the clock moves 1 s,
// and the frame lands at the time it arrived.
func TestRecordingPlacesAnRTPJumpPastTwoSecondsByTheClock(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)

	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, uint32(5000+opusFrameSamples+samplesIn(5*time.Second)), "a2"))
	recordAs(t, sc.rt, recStarter, "stop")

	assertAt(t, sc.tracks(t), "a2", samplesIn(2*time.Second))
}

// A track runs to the end of the recording and no further: its final
// granule position is the recording's length, within one frame, whether its
// speaker fell silent long before the stop, was talking as it came, or had
// RTP timestamps that put their words ahead of the clock.
func TestRecordingTrackRunsToTheRecordingsLength(t *testing.T) {
	type heardAt struct {
		at time.Duration
		ts uint32
	}
	for _, tc := range []struct {
		name   string
		frames []heardAt
	}{
		{name: "silent at the stop", frames: []heardAt{{at: time.Second, ts: 5000}}},
		{name: "talking at the stop", frames: []heardAt{{at: 30 * time.Second, ts: 5000}}},
		{name: "ahead of the clock at the stop", frames: []heardAt{
			{at: 27 * time.Second, ts: 5000},
			// RTP puts this frame 3 s after the first, at 30 s, while it
			// arrives at 29.5 s: within 2 s of the clock, so RTP places it.
			{at: 29500 * time.Millisecond, ts: uint32(5000 + samplesIn(3*time.Second))},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, testRecorder)
			sc.startIn(t, recStarter, recordChannel)

			start := sc.clock.read()
			for i, f := range tc.frames {
				sc.clock.advance(start.Add(f.at).Sub(sc.clock.read()))
				sc.hear(t, recordChannel, frame(speakerA, 11, f.ts, "a"+strconv.Itoa(i)))
			}
			sc.clock.advance(start.Add(30 * time.Second).Sub(sc.clock.read()))
			recordAs(t, sc.rt, recStarter, "stop")

			tracks := sc.tracks(t)
			if len(tracks) != 1 {
				t.Fatalf("tracks = %d, want 1", len(tracks))
			}
			want := uint64(samplesIn(30 * time.Second))
			if got := tracks[0].granule; got+opusFrameSamples < want || got > want+opusFrameSamples {
				t.Errorf("track length = %d samples (%.3f s), want %d (30 s) within one frame",
					got, float64(got)/48000, want)
			}
		})
	}
}

// While a recording runs, what a speaker said is on disk within one flush
// interval, so a crash loses at most that much.
func TestRecordingTrackReachesDiskWithinAFlushInterval(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)

	sc.clock.advance(time.Second)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(trackFlushInterval)

	trackWith(t, sc.tracks(t), "a1")
}
