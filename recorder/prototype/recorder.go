package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

const (
	sampleRate    = 48000
	silenceFrame  = 960 // samples in voice.SilenceAudioFrame, 20 ms
	maxGapSamples = sampleRate * 60 * 60
)

// recorder writes one Ogg Opus file per speaker. Every track starts at the
// moment recording started and fills gaps with Opus silence frames, so the
// files line up when opened side by side, the way Craig's do.
type recorder struct {
	dir   string
	start time.Time
	st    *stats

	mu       sync.Mutex
	tracks   map[snowflake.ID]*track
	receiver *receiver
	closed   bool
}

func newRecorder(outDir string, st *stats) (*recorder, error) {
	dir := filepath.Join(outDir, "PROTOTYPE-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &recorder{dir: dir, start: st.start, st: st, tracks: map[snowflake.ID]*track{}}, nil
}

// newReceiver is the disgo AudioReceiverCreateFunc. The recorder keeps the
// receiver so it can stop it on shutdown; disgo's Conn.Close does not.
func (r *recorder) newReceiver(_ *slog.Logger, handler voice.OpusFrameReceiver, conn voice.Conn) voice.AudioReceiver {
	rcv := &receiver{conn: conn, handler: handler, st: r.st, stop: make(chan struct{})}
	r.mu.Lock()
	r.receiver = rcv
	r.mu.Unlock()
	return rcv
}

func (r *recorder) ReceiveOpusFrame(userID snowflake.ID, p *voice.Packet) error {
	now := time.Now()
	r.st.received(userID, p, now)
	if userID == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	t, ok := r.tracks[userID]
	if !ok {
		var err error
		if t, err = newTrack(filepath.Join(r.dir, userID.String()+".ogg"), uint32(userID)); err != nil {
			return err
		}
		r.tracks[userID] = t
		r.st.printf("new track for %s", r.st.names.of(userID))
	}
	late, err := t.write(p, now.Sub(r.start))
	if late {
		r.st.late(userID)
	}
	r.st.trackAt(userID, t.seconds())
	return err
}

// CleanupUser is called when a user leaves the call. The track stays open:
// they may come back, and the gap is filled with silence when they do.
func (r *recorder) CleanupUser(snowflake.ID) {}

// Close satisfies voice.OpusFrameReceiver. main calls finish to see the error.
func (r *recorder) Close() { _ = r.finish() }

// finish closes every track, writing its end-of-stream page.
func (r *recorder) finish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var errs []error
	for _, t := range r.tracks {
		errs = append(errs, t.close())
	}
	return errors.Join(errs...)
}

func (r *recorder) stopReceiving() {
	r.mu.Lock()
	rcv := r.receiver
	r.mu.Unlock()
	if rcv != nil {
		rcv.Close()
	}
}

func (r *recorder) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tracks {
		_ = t.w.Flush()
	}
}

type track struct {
	f   *os.File
	w   *bufio.Writer
	ogg *oggWriter

	started     bool
	ssrc        uint32
	nextTS      uint32 // RTP timestamp the next packet should carry
	samplesDone uint64
}

func newTrack(path string, serial uint32) (*track, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriter(f)
	ogg, err := newOggWriter(w, serial)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &track{f: f, w: w, ogg: ogg}, nil
}

// write appends one packet and fills any gap before it with silence.
//
// A new SSRC starts a new RTP clock at a random base: the user's first packet,
// or their first after rejoining. Those packets are placed by wall-clock time.
// After that the RTP timestamps place each packet, because they are exact,
// unless they disagree with the wall clock by more than two seconds. Then the
// wall clock wins. A packet whose timestamp is behind the track (reordered or
// duplicated) is dropped.
func (t *track) write(p *voice.Packet, sinceStart time.Duration) (late bool, err error) {
	wallGap := int64(sinceStart.Seconds()*sampleRate) - int64(t.samplesDone)
	var gap int64
	if !t.started || p.SSRC != t.ssrc {
		gap = wallGap
		t.started, t.ssrc = true, p.SSRC
	} else {
		gap = int64(int32(p.Timestamp - t.nextTS))
		if gap < 0 {
			return true, nil
		}
		if d := gap - wallGap; d > 2*sampleRate || d < -2*sampleRate {
			gap = wallGap
		}
	}
	gap = max(0, min(gap, maxGapSamples))
	for ; gap >= silenceFrame; gap -= silenceFrame {
		if err := t.ogg.writePacket(voice.SilenceAudioFrame, silenceFrame); err != nil {
			return false, err
		}
		t.samplesDone += silenceFrame
	}
	n := opusSamples(p.Opus)
	if err := t.ogg.writePacket(p.Opus, n); err != nil {
		return false, err
	}
	t.samplesDone += uint64(n)
	t.nextTS = p.Timestamp + uint32(n)
	return false, nil
}

func (t *track) seconds() float64 { return float64(t.samplesDone) / sampleRate }

func (t *track) close() error {
	err := t.ogg.close()
	if ferr := t.w.Flush(); err == nil {
		err = ferr
	}
	if cerr := t.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// opusSamples reads the TOC byte (RFC 6716 section 3.1) for the packet's
// length in 48 kHz samples.
func opusSamples(pkt []byte) int {
	if len(pkt) == 0 {
		return 0
	}
	cfg := pkt[0] >> 3
	var frame int
	switch {
	case cfg < 12:
		frame = []int{480, 960, 1920, 2880}[cfg&3]
	case cfg < 16:
		frame = []int{480, 960}[cfg&1]
	default:
		frame = []int{120, 240, 480, 960}[cfg&3]
	}
	switch pkt[0] & 3 {
	case 0:
		return frame
	case 1, 2:
		return 2 * frame
	default:
		if len(pkt) < 2 {
			return 0
		}
		return int(pkt[1]&0x3f) * frame
	}
}

// oggWriter writes an Ogg Opus stream (RFC 7845), one packet per page. It
// holds the latest packet back so close can mark it end-of-stream.
type oggWriter struct {
	w       io.Writer
	serial  uint32
	pageSeq uint32
	granule uint64

	pending        []byte
	pendingGranule uint64
	hasPending     bool
}

func newOggWriter(w io.Writer, serial uint32) (*oggWriter, error) {
	o := &oggWriter{w: w, serial: serial}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1 // version
	head[9] = 2 // channels
	binary.LittleEndian.PutUint32(head[12:], sampleRate)
	if err := o.page(head, 0, 0x02); err != nil {
		return nil, err
	}
	vendor := "cavbot2 recorder prototype"
	tags := make([]byte, 0, 16+len(vendor))
	tags = append(tags, "OpusTags"...)
	tags = binary.LittleEndian.AppendUint32(tags, uint32(len(vendor)))
	tags = append(tags, vendor...)
	tags = binary.LittleEndian.AppendUint32(tags, 0)
	if err := o.page(tags, 0, 0); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *oggWriter) writePacket(pkt []byte, samples int) error {
	if o.hasPending {
		if err := o.page(o.pending, o.pendingGranule, 0); err != nil {
			return err
		}
	}
	o.granule += uint64(samples)
	o.pending = append(o.pending[:0], pkt...)
	o.pendingGranule = o.granule
	o.hasPending = true
	return nil
}

func (o *oggWriter) close() error {
	if !o.hasPending {
		return nil
	}
	o.hasPending = false
	return o.page(o.pending, o.pendingGranule, 0x04)
}

func (o *oggWriter) page(data []byte, granule uint64, flags byte) error {
	segments := len(data)/255 + 1
	if segments > 255 {
		return fmt.Errorf("ogg: %d byte packet does not fit one page", len(data))
	}
	hdr := make([]byte, 27+segments)
	copy(hdr, "OggS")
	hdr[5] = flags
	binary.LittleEndian.PutUint64(hdr[6:], granule)
	binary.LittleEndian.PutUint32(hdr[14:], o.serial)
	binary.LittleEndian.PutUint32(hdr[18:], o.pageSeq)
	hdr[26] = byte(segments)
	for i := range segments - 1 {
		hdr[27+i] = 255
	}
	hdr[27+segments-1] = byte(len(data) % 255)
	crc := oggCRC(oggCRC(0, hdr), data)
	binary.LittleEndian.PutUint32(hdr[22:], crc)
	o.pageSeq++
	if _, err := o.w.Write(hdr); err != nil {
		return err
	}
	_, err := o.w.Write(data)
	return err
}

var oggCRCTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(crc uint32, b []byte) uint32 {
	for _, c := range b {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^c]
	}
	return crc
}
