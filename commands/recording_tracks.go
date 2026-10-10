package commands

import (
	"bufio"
	"errors"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/7cav/cavbot2/voice"
)

// A recording's tracks (spec #381, #387): one Ogg Opus file per speaker in
// the recording's directory, named by the speaker's Discord ID. Discord's
// packets go in as they arrived, never re-encoded.

// trackFlushInterval is how often a running recording's tracks are written
// out to disk, which bounds what a crash loses.
const trackFlushInterval = 3 * time.Second

// recordingTracks is one recording's tracks, written as the voice adapter
// delivers frames. A speaker's track opens at their first frame.
type recordingTracks struct {
	recordingID int64
	dir         string
	start       time.Time

	mu     sync.Mutex
	tracks map[string]*track
	closed bool
	// stopFlush cancels the next flush.
	stopFlush func()
}

// track is one speaker's file. err is the first write that failed: the
// track takes nothing after it, so a broken disk is reported once.
type track struct {
	f   *os.File
	buf *bufio.Writer
	ogg *oggWriter
	err error

	// started is set by the first packet. ssrc is the RTP stream the last
	// packet came on, and nextTS the RTP timestamp the packet after it
	// would carry if none were missing.
	started bool
	ssrc    uint32
	nextTS  uint32
}

// maxRTPDrift is how far a packet's RTP timestamp may disagree with the
// clock before the clock places it instead: 2 s, in 48 kHz samples.
const maxRTPDrift = 2 * opusSampleRate

// opusSilence is Discord's Opus silence frame: 20 ms of nothing. Gaps in a
// track are filled with it, so every track runs from the recording's start.
var opusSilence = []byte{0xF8, 0xFF, 0xFE}

// opusSilenceSamples is the length of opusSilence in 48 kHz samples.
const opusSilenceSamples = 960

// newRecordingTracks makes the tracks of the recording with the ID given,
// which started at start, in its own directory under dir.
// The tracks are flushed to disk every trackFlushInterval from now until
// close.
func newRecordingTracks(dir string, recordingID int64, start time.Time) *recordingTracks {
	rt := &recordingTracks{
		recordingID: recordingID,
		dir:         filepath.Join(dir, strconv.FormatInt(recordingID, 10)),
		start:       start,
		tracks:      make(map[string]*track),
	}
	rt.mu.Lock()
	rt.stopFlush = recordingAfterFunc(trackFlushInterval, rt.flush)
	rt.mu.Unlock()
	return rt
}

// flush writes every track's pages out to disk, then sets the next flush.
func (rt *recordingTracks) flush() {
	defer utils.RecoverPanic("recording-flush", "recording_id", rt.recordingID)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return
	}
	for userID, t := range rt.tracks {
		if t.err != nil {
			continue
		}
		err := t.ogg.flush()
		if err == nil {
			err = t.buf.Flush()
		}
		if err != nil {
			t.fail(err)
			captureError("Track not written", err, "recording_id", rt.recordingID, "user_id", userID)
		}
	}
	rt.stopFlush = recordingAfterFunc(trackFlushInterval, rt.flush)
}

// write adds a frame that arrived at arrival to its speaker's track. A
// frame after close is dropped: the recorder has left by then.
func (rt *recordingTracks) write(f voice.Frame, arrival time.Time) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return
	}
	t, ok := rt.tracks[f.UserID]
	if !ok {
		var err error
		t, err = rt.open(f.UserID)
		if err != nil {
			captureError("Track not written", err, "recording_id", rt.recordingID, "user_id", f.UserID)
		}
		rt.tracks[f.UserID] = t
	}
	if t.err != nil {
		return
	}
	if err := t.place(f, rt.samplesAt(arrival)); err != nil {
		t.fail(err)
		captureError("Track not written", err, "recording_id", rt.recordingID, "user_id", f.UserID)
	}
}

// samplesAt is how far into the recording a time is, in 48 kHz samples.
func (rt *recordingTracks) samplesAt(at time.Time) int64 {
	return int64(at.Sub(rt.start).Seconds() * opusSampleRate)
}

// place writes a packet where it belongs in the track, filling the gap
// before it with silence. wallClock is where the packet's arrival puts it.
//
// A new SSRC starts a new RTP clock at a random base: the speaker's first
// packet, or their first after rejoining. Those packets are placed by the
// wall clock. After that the RTP timestamps place each packet, because
// they're exact where arrival jitters, unless they disagree with the wall
// clock by more than 2 s. Then the wall clock wins. A packet whose timestamp
// is behind the track, reordered or duplicated, is dropped.
func (t *track) place(f voice.Frame, wallClock int64) error {
	wallGap := wallClock - int64(t.ogg.granule)
	gap := wallGap
	if t.started && f.SSRC == t.ssrc {
		gap = int64(int32(f.Timestamp - t.nextTS))
		if gap < 0 {
			return nil
		}
		if gap-wallGap > maxRTPDrift || wallGap-gap > maxRTPDrift {
			gap = wallGap
		}
	}
	t.started, t.ssrc = true, f.SSRC
	samples := opusPacketSamples(f.Opus)
	t.nextTS = f.Timestamp + uint32(samples)
	for ; gap >= opusSilenceSamples; gap -= opusSilenceSamples {
		if err := t.ogg.writePacket(opusSilence, opusSilenceSamples); err != nil {
			return err
		}
	}
	return t.ogg.writePacket(f.Opus, samples)
}

// end fills the track with silence to the recording's end, at least one
// frame, so it lasts the whole recording, and writes its last page.
func (t *track) end(end int64) error {
	for {
		if err := t.ogg.writePacket(opusSilence, opusSilenceSamples); err != nil {
			return err
		}
		if int64(t.ogg.granule) >= end {
			return t.ogg.close()
		}
	}
}

// open creates a speaker's track, and the recording's directory with the
// first one. A track that can't be created comes back failed.
func (rt *recordingTracks) open(userID string) (*track, error) {
	if err := os.MkdirAll(rt.dir, 0o750); err != nil {
		return &track{err: err}, err
	}
	f, err := os.Create(filepath.Join(rt.dir, userID+".ogg"))
	if err != nil {
		return &track{err: err}, err
	}
	t := &track{f: f, buf: bufio.NewWriter(f)}
	serial := fnv.New32a()
	_, _ = serial.Write([]byte(userID))
	if t.ogg, err = newOggWriter(t.buf, serial.Sum32()); err != nil {
		t.fail(err)
		return t, err
	}
	return t, nil
}

// fail marks the track failed and closes its file.
func (t *track) fail(err error) {
	t.err = err
	if t.f != nil {
		_ = t.f.Close()
	}
}

// close ends every track at stop, the recording's end, and closes its
// file. Frames that arrive after are dropped.
func (rt *recordingTracks) close(stop time.Time) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.closed = true
	rt.stopFlush()
	end := rt.samplesAt(stop)
	var errs []error
	for _, t := range rt.tracks {
		if t.err != nil {
			continue
		}
		err := t.end(end)
		if ferr := t.buf.Flush(); err == nil {
			err = ferr
		}
		if cerr := t.f.Close(); err == nil {
			err = cerr
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
