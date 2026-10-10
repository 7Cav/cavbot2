package commands

import (
	"bufio"
	"errors"
	"hash/fnv"
	"maps"
	"os"
	"slices"
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
	// dir is the recordings directory, which holds the recording's own.
	dir   string
	start time.Time

	mu        sync.Mutex
	bySpeaker map[string]*track
	closed    bool
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
// which started at start, in its own directory under dir. The tracks are
// flushed to disk every trackFlushInterval from now until close.
func newRecordingTracks(dir string, recordingID int64, start time.Time) *recordingTracks {
	tracks := &recordingTracks{
		recordingID: recordingID,
		dir:         dir,
		start:       start,
		bySpeaker:   make(map[string]*track),
	}
	tracks.mu.Lock()
	tracks.stopFlush = recordingAfterFunc(trackFlushInterval, tracks.flush)
	tracks.mu.Unlock()
	return tracks
}

// flush writes every track's pages out to disk. The next flush is set
// first, so a panic here costs this flush alone.
func (tracks *recordingTracks) flush() {
	defer utils.RecoverPanic("recording-flush", "recording_id", tracks.recordingID)
	tracks.mu.Lock()
	defer tracks.mu.Unlock()
	if tracks.closed {
		return
	}
	tracks.stopFlush = recordingAfterFunc(trackFlushInterval, tracks.flush)
	for userID, t := range tracks.bySpeaker {
		if t.err != nil {
			continue
		}
		if err := t.flush(); err != nil {
			tracks.fail(userID, t, err)
		}
	}
}

// write adds a frame that arrived at arrival to its speaker's track. A
// frame after close is dropped: the recorder has left by then.
func (tracks *recordingTracks) write(f voice.Frame, arrival time.Time) {
	tracks.mu.Lock()
	defer tracks.mu.Unlock()
	if tracks.closed {
		return
	}
	t, ok := tracks.bySpeaker[f.UserID]
	if !ok {
		var err error
		t, err = tracks.open(f.UserID)
		tracks.bySpeaker[f.UserID] = t
		if err != nil {
			tracks.fail(f.UserID, t, err)
		}
	}
	if t.err != nil {
		return
	}
	if err := t.place(f, tracks.samplesAt(arrival)); err != nil {
		tracks.fail(f.UserID, t, err)
	}
}

// samplesAt is how far into the recording a time is, in 48 kHz samples.
func (tracks *recordingTracks) samplesAt(at time.Time) int64 {
	return int64(at.Sub(tracks.start).Seconds() * opusSampleRate)
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

// end fills the track with silence to the recording's end, so it lasts the
// whole recording, and writes its last page, which ends the track there.
// The last page needs a packet, so a track already at the end gets one more
// silence frame, trimmed off again.
func (t *track) end(end int64) error {
	for int64(t.ogg.granule) < end || !t.ogg.pending() {
		if err := t.ogg.writePacket(opusSilence, opusSilenceSamples); err != nil {
			return err
		}
	}
	return t.ogg.closeAt(uint64(max(end, 0)))
}

// open creates a speaker's track, and the recording's directory with the
// first one. A track that can't be created comes back with the error.
func (tracks *recordingTracks) open(userID string) (*track, error) {
	t := &track{}
	if err := os.MkdirAll(recordingDir(tracks.dir, tracks.recordingID), 0o750); err != nil {
		return t, err
	}
	f, err := os.Create(trackPath(tracks.dir, tracks.recordingID, userID))
	if err != nil {
		return t, err
	}
	t.f, t.buf = f, bufio.NewWriter(f)
	serial := fnv.New32a()
	_, _ = serial.Write([]byte(userID))
	t.ogg, err = newOggWriter(t.buf, serial.Sum32())
	return t, err
}

// fail marks a speaker's track failed and closes its file, and Sentry
// hears of it once: the track takes nothing after.
func (tracks *recordingTracks) fail(userID string, t *track, err error) {
	t.err = err
	if t.f != nil {
		_ = t.f.Close()
	}
	captureError("Track not written", err, "recording_id", tracks.recordingID, "user_id", userID)
}

// flush writes the track's pages out to disk.
func (t *track) flush() error {
	if err := t.ogg.flush(); err != nil {
		return err
	}
	return t.buf.Flush()
}

// close ends the track at end and closes its file.
func (t *track) close(end int64) error {
	err := t.end(end)
	if ferr := t.buf.Flush(); err == nil {
		err = ferr
	}
	if cerr := t.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// speakerIDs lists the user ID of everyone the tracks heard, in order.
func (tracks *recordingTracks) speakerIDs() []string {
	tracks.mu.Lock()
	defer tracks.mu.Unlock()
	return slices.Sorted(maps.Keys(tracks.bySpeaker))
}

// close ends every track at stop, the recording's end, and closes its
// file. Frames that arrive after are dropped.
func (tracks *recordingTracks) close(stop time.Time) error {
	tracks.mu.Lock()
	defer tracks.mu.Unlock()
	tracks.closed = true
	tracks.stopFlush()
	end := tracks.samplesAt(stop)
	var errs []error
	for _, t := range tracks.bySpeaker {
		if t.err == nil {
			errs = append(errs, t.close(end))
		}
	}
	return errors.Join(errs...)
}
