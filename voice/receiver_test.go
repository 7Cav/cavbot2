package voice

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
)

// The audio receiver, over a fake voice connection: the test queues the
// packets the UDP connection reads, maps SSRCs to users as Discord's op 5
// would, and seals each frame for one user's DAVE key. It judges the
// receiver by the frames it hands over and the Sentry events it sends.

// The speaker every case hears, and the SSRC they send on.
const (
	testSpeaker     = "300"
	testSpeakerSSRC = 7
)

// waitFor is how long a case waits on the receiver's goroutine.
const waitFor = 2 * time.Second

// fakeReceiveConn is a voice connection as the receiver reads it.
type fakeReceiveConn struct {
	udp  *fakeVoiceUDP
	dave *fakeDAVE

	mu    sync.Mutex
	users map[uint32]snowflake.ID
	// misses counts lookups of an SSRC no user is mapped to yet.
	misses int
}

func newFakeReceiveConn() *fakeReceiveConn {
	return &fakeReceiveConn{
		udp:   &fakeVoiceUDP{packets: make(chan *disgovoice.Packet, 64)},
		dave:  &fakeDAVE{},
		users: make(map[uint32]snowflake.ID),
	}
}

func (c *fakeReceiveConn) UDP() disgovoice.UDPConn { return c.udp }
func (c *fakeReceiveConn) DAVE() godave.Session    { return c.dave }

func (c *fakeReceiveConn) UserIDBySSRC(ssrc uint32) snowflake.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.users[ssrc]
	if id == 0 {
		c.misses++
	}
	return id
}

// mapSSRC maps an SSRC to a user, as op 5 does.
func (c *fakeReceiveConn) mapSSRC(ssrc uint32, userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users[ssrc] = snowflake.MustParse(userID)
}

// missCount is how many lookups found no user so far.
func (c *fakeReceiveConn) missCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.misses
}

// send queues a packet on the SSRC, its payload the DAVE frame given.
func (c *fakeReceiveConn) send(ssrc, timestamp uint32, payload []byte) {
	c.udp.packets <- &disgovoice.Packet{SSRC: ssrc, Timestamp: timestamp, Opus: payload}
}

// fakeVoiceUDP is the UDP connection: each read returns the next queued
// packet, or a read timeout when none is waiting, as the adapter's own
// connection does.
type fakeVoiceUDP struct {
	disgovoice.UDPConn
	packets chan *disgovoice.Packet
}

func (u *fakeVoiceUDP) ReadPacket() (*disgovoice.Packet, error) {
	select {
	case p := <-u.packets:
		return p, nil
	case <-time.After(5 * time.Millisecond):
		return nil, os.ErrDeadlineExceeded
	}
}

// fakeDAVE is the DAVE session. A frame sealed for a user opens only as
// that user, and as no one else, the unmapped user 0 included.
type fakeDAVE struct {
	godave.Session
}

func (d *fakeDAVE) Ready() bool                                      { return true }
func (d *fakeDAVE) MaxDecryptedFrameSize(_ godave.UserID, n int) int { return n }

func (d *fakeDAVE) Decrypt(userID godave.UserID, frame, out []byte) (int, error) {
	prefix := sealedPrefix(string(userID))
	if !bytes.HasPrefix(frame, prefix) {
		return 0, errors.New("fake DAVE: frame not sealed for this user")
	}
	return copy(out, frame[len(prefix):]), nil
}

func sealedPrefix(userID string) []byte { return []byte("dave(" + userID + "):") }

// sealFor is plaintext as a DAVE frame only userID's key opens.
func sealFor(userID, plaintext string) []byte {
	return append(sealedPrefix(userID), plaintext...)
}

// heard collects the frames a receiver hands over.
type heard struct {
	frames chan Frame
}

func newHeard() *heard { return &heard{frames: make(chan Frame, 64)} }

func (h *heard) handle(f Frame) { h.frames <- f }

// next waits for the next frame handed over.
func (h *heard) next(t *testing.T) Frame {
	t.Helper()
	select {
	case f := <-h.frames:
		return f
	case <-time.After(waitFor):
		t.Fatal("no frame handed over")
		return Frame{}
	}
}

// eventually waits until cond holds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// startReceiver runs a receiver over conn until the test ends.
func startReceiver(t *testing.T, conn *fakeReceiveConn, handle func(Frame)) {
	t.Helper()
	r := newReceiver(conn, handle, "recorder-1")
	r.open()
	t.Cleanup(r.close)
}

// A speaker's first frames can arrive before op 5 says whose SSRC they came
// on. They're held, and handed over as that speaker, decrypted with their
// key and in the order they came, once the SSRC maps, ahead of what comes
// after.
func TestReceiverHoldsFramesUntilTheirSSRCMaps(t *testing.T) {
	conn := newFakeReceiveConn()
	h := newHeard()
	startReceiver(t, conn, h.handle)

	conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "first"))
	conn.send(testSpeakerSSRC, 1960, sealFor(testSpeaker, "second"))
	eventually(t, "both early frames read with no user mapped", func() bool {
		return len(conn.udp.packets) == 0 && conn.missCount() >= 2
	})
	conn.mapSSRC(testSpeakerSSRC, testSpeaker)
	conn.send(testSpeakerSSRC, 2920, sealFor(testSpeaker, "third"))

	for _, want := range []string{"first", "second", "third"} {
		f := h.next(t)
		if f.UserID != testSpeaker || string(f.Opus) != want || f.SSRC != testSpeakerSSRC {
			t.Errorf("frame handed over = %+v (%q), want %q from %s on SSRC %d",
				f, f.Opus, want, testSpeaker, testSpeakerSSRC)
		}
	}
}

// A panic while a frame is handed over, a bug in the code writing tracks,
// is recovered and reported to Sentry, so it can't take the bot down with
// temp VC and every command.
func TestReceiverRecoversAPanicInTheFrameHandler(t *testing.T) {
	events := recordSentry(t)
	conn := newFakeReceiveConn()
	conn.mapSSRC(testSpeakerSSRC, testSpeaker)
	startReceiver(t, conn, func(Frame) { panic("track writer bug") })

	conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "words"))

	eventually(t, "the panic reported to Sentry", func() bool { return len(events.Events()) == 1 })
}

// A run of frames from one speaker that DAVE can't decrypt is reported to
// Sentry once, not once a frame: here twice the burst size fails in a row,
// then a frame decrypts.
func TestReceiverReportsABurstOfDAVEFailuresOnce(t *testing.T) {
	events := recordSentry(t)
	conn := newFakeReceiveConn()
	conn.mapSSRC(testSpeakerSSRC, testSpeaker)
	h := newHeard()
	startReceiver(t, conn, h.handle)

	for i := range 2 * daveFailureBurst {
		conn.send(testSpeakerSSRC, uint32(i*960), sealFor("999", "unreadable"))
	}
	conn.send(testSpeakerSSRC, uint32(2*daveFailureBurst*960), sealFor(testSpeaker, "words"))
	h.next(t)

	if got := len(events.Events()); got != 1 {
		t.Errorf("Sentry events = %d, want 1 for the burst", got)
	}
}
