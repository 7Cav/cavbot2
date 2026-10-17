package voice

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
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

// The channel every case records.
const testChannel = "200"

// fakeReceiveConn is a voice connection as the receiver reads it, its DAVE
// session the fake one unless a case gives it another. reconnects is when
// the receiver asked for each fresh connection.
type fakeReceiveConn struct {
	udp  *fakeVoiceUDP
	dave godave.Session

	mu         sync.Mutex
	users      map[uint32]snowflake.ID
	reconnects []time.Time
}

func newFakeReceiveConn() *fakeReceiveConn {
	return &fakeReceiveConn{
		udp:   &fakeVoiceUDP{reads: make(chan udpRead, 64)},
		dave:  &fakeDAVE{},
		users: make(map[uint32]snowflake.ID),
	}
}

func (c *fakeReceiveConn) UDP() disgovoice.UDPConn { return c.udp }
func (c *fakeReceiveConn) DAVE() godave.Session    { return c.dave }

// holdFrames has the fake DAVE session hold frames, as one whose setup
// hasn't finished does, or stop holding them.
func (c *fakeReceiveConn) holdFrames(hold bool) { c.dave.(*fakeDAVE).holding.Store(hold) }

// reconnectTimes is when the receiver asked for each fresh connection.
func (c *fakeReceiveConn) reconnectTimes() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.reconnects)
}

func (c *fakeReceiveConn) ChannelID() *snowflake.ID {
	id := snowflake.MustParse(testChannel)
	return &id
}

// connection is always connected here: the call harness covers the
// connection coming and going.
func (c *fakeReceiveConn) connection() (bool, time.Time) { return true, time.Time{} }
func (c *fakeReceiveConn) server() string                { return "c-dfw-1.discord.media:443" }

func (c *fakeReceiveConn) reconnect(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnects = append(c.reconnects, time.Now())
	return nil
}

func (c *fakeReceiveConn) UserIDBySSRC(ssrc uint32) snowflake.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.users[ssrc]
}

// mapSSRC maps an SSRC to a user, as op 5 does.
func (c *fakeReceiveConn) mapSSRC(ssrc uint32, userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users[ssrc] = snowflake.MustParse(userID)
}

// send queues a packet on the SSRC, its payload the DAVE frame given.
func (c *fakeReceiveConn) send(ssrc, timestamp uint32, payload []byte) {
	c.udp.reads <- udpRead{p: &disgovoice.Packet{SSRC: ssrc, Timestamp: timestamp, Opus: payload}}
}

// sendUnreadable queues a datagram the UDP connection can't read.
func (c *fakeReceiveConn) sendUnreadable() {
	c.udp.reads <- udpRead{err: errors.New("transport decrypt: cipher: message authentication failed")}
}

// udpRead is what one read of the UDP connection returns.
type udpRead struct {
	p   *disgovoice.Packet
	err error
}

// fakeVoiceUDP is the UDP connection: each read returns the next queued
// result, or a read timeout when none is waiting, as the adapter's own
// connection does. taken counts the queued results read.
type fakeVoiceUDP struct {
	disgovoice.UDPConn
	reads chan udpRead
	taken atomic.Int32
}

func (u *fakeVoiceUDP) ReadPacket() (*disgovoice.Packet, error) {
	select {
	case r := <-u.reads:
		u.taken.Add(1)
		return r.p, r.err
	case <-time.After(5 * time.Millisecond):
		return nil, os.ErrDeadlineExceeded
	}
}

// fakeDAVE is the DAVE session. A frame sealed for a user opens only as
// that user, and as no one else, the unmapped user 0 included. It holds
// frames while holding is set.
type fakeDAVE struct {
	godave.Session
	holding atomic.Bool
}

func (d *fakeDAVE) Ready() bool                                      { return !d.holding.Load() }
func (d *fakeDAVE) ShouldHoldFrames() bool                           { return d.holding.Load() }
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

// listener is a Listener that hands each frame to frame, when set, and keeps
// the gap events it hears in order, and when each gap started.
type listener struct {
	frame func(Frame)

	mu     sync.Mutex
	gaps   []string
	starts []time.Time
}

// The gap events a listener keeps.
const (
	gapStarted = "started"
	gapEnded   = "ended"
)

func listen(frame func(Frame)) *listener { return &listener{frame: frame} }

func (l *listener) Frame(f Frame) {
	if l.frame != nil {
		l.frame(f)
	}
}

func (l *listener) GapStarted(since time.Time) {
	l.mu.Lock()
	l.starts = append(l.starts, since)
	l.mu.Unlock()
	l.note(gapStarted)
}

func (l *listener) GapEnded() { l.note(gapEnded) }

// gapStarts is when each gap heard so far started, in order.
func (l *listener) gapStarts() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.starts)
}

func (l *listener) note(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gaps = append(l.gaps, event)
}

// gapEvents is the gap events heard so far, in order.
func (l *listener) gapEvents() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.gaps)
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

// expectSpeaker waits for the frames handed over next to be words, in
// order, each from testSpeaker on their SSRC.
func (h *heard) expectSpeaker(t *testing.T, words ...string) {
	t.Helper()
	for _, want := range words {
		f := h.next(t)
		if f.UserID != testSpeaker || string(f.Opus) != want || f.SSRC != testSpeakerSSRC {
			t.Errorf("frame handed over = %+v (%q), want %q from %s on SSRC %d",
				f, f.Opus, want, testSpeaker, testSpeakerSSRC)
		}
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

// startReceiver runs a receiver over conn until the test ends, handing
// each frame to handle.
func startReceiver(t *testing.T, conn *fakeReceiveConn, handle func(Frame)) *receiver {
	t.Helper()
	return startListening(t, conn, listen(handle))
}

// startListening runs a receiver over conn until the test ends, handing
// what it hears to l.
func startListening(t *testing.T, conn *fakeReceiveConn, l Listener) *receiver {
	t.Helper()
	r := newReceiver(conn, l, "recorder-1")
	r.open()
	t.Cleanup(r.close)
	return r
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
	// The receiver takes the second packet only once it's done with the
	// first, which it read with no user mapped.
	eventually(t, "both early frames read", func() bool { return conn.udp.taken.Load() == 2 })
	conn.mapSSRC(testSpeakerSSRC, testSpeaker)
	conn.send(testSpeakerSSRC, 2920, sealFor(testSpeaker, "third"))

	h.expectSpeaker(t, "first", "second", "third")
}

// A speaker who says something as they join, before op 5 names them, and
// then stays quiet still has those words in their track: the held frames
// are handed over once their SSRC maps, with nothing more from that speaker
// to carry them out.
func TestReceiverHandsOverHeldFramesOnceTheSSRCMapsWithNothingAfter(t *testing.T) {
	const otherSpeaker, otherSSRC = "301", 8
	conn := newFakeReceiveConn()
	conn.mapSSRC(otherSSRC, otherSpeaker)
	h := newHeard()
	startReceiver(t, conn, h.handle)

	conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "radio"))
	conn.send(testSpeakerSSRC, 1960, sealFor(testSpeaker, "check"))
	// The receiver reads one packet at a time, so once another speaker's
	// later frame is handed over, both early frames are held.
	conn.send(otherSSRC, 5000, sealFor(otherSpeaker, "copy"))
	if f := h.next(t); f.UserID != otherSpeaker {
		t.Fatalf("first frame handed over = %+v (%q), want %s's, the only speaker mapped", f, f.Opus, otherSpeaker)
	}
	conn.mapSSRC(testSpeakerSSRC, testSpeaker)

	h.expectSpeaker(t, "radio", "check")
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

	for i := range 2 * failureBurst {
		conn.send(testSpeakerSSRC, uint32(i*960), sealFor("999", "unreadable"))
	}
	conn.send(testSpeakerSSRC, uint32(2*failureBurst*960), sealFor(testSpeaker, "words"))
	h.next(t)

	if got := len(events.Events()); got != 1 {
		t.Errorf("Sentry events = %d, want 1 for the burst", got)
	}
}

// A run of datagrams the UDP connection can't read, such as a transport key
// that no longer matches, is reported to Sentry once, not once a datagram:
// left at debug level, it would leave every track empty with no word to the
// maintainer.
func TestReceiverReportsABurstOfUnreadablePacketsOnce(t *testing.T) {
	events := recordSentry(t)
	conn := newFakeReceiveConn()
	conn.mapSSRC(testSpeakerSSRC, testSpeaker)
	h := newHeard()
	startReceiver(t, conn, h.handle)

	for range 2 * failureBurst {
		conn.sendUnreadable()
	}
	conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "words"))
	h.next(t)

	if got := len(events.Events()); got != 1 {
		t.Errorf("Sentry events = %d, want 1 for the burst", got)
	}
}

// Once DAVE sets up after a stall, the speaker's frames reach the recording
// as theirs again, and the log says how long the recorder couldn't hear.
func TestReceiverHearsTheSpeakerAgainOnceDAVESetsUpAfterAStall(t *testing.T) {
	recordSentry(t)
	logs := captureLogs(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		conn.holdFrames(true)
		h := newHeard()
		unheardFrom := time.Now()
		startReceiver(t, conn, h.handle)

		time.Sleep(3500 * time.Millisecond)
		synctest.Wait()
		if len(conn.reconnectTimes()) == 0 {
			t.Fatal("no fresh connection asked for 3.5 s into the stall")
		}
		// The fresh connection's DAVE setup finishes.
		conn.holdFrames(false)
		heardFrom := time.Now()
		conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "back"))

		if f := h.next(t); f.UserID != testSpeaker || string(f.Opus) != "back" {
			t.Errorf("frame handed over = %+v (%q), want %q from %s", f, f.Opus, "back", testSpeaker)
		}
		want := heardFrom.Sub(unheardFrom)
		if d, ok := logs.duration(slog.LevelInfo, daveRecoveredMessage); !ok || d < want || d >= want+time.Second {
			t.Errorf("Info line %q carries %v (found %v), want how long the recorder couldn't hear, %v", daveRecoveredMessage, d, ok, want)
		}
	})
}

// DAVE holding every frame is a gap: the recorder hears nobody. The gap
// ends once DAVE passes frames again, and the listener hears of it once
// each way, however long DAVE held them.
func TestReceiverToldOfAGapWhileDAVEHoldsFrames(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		l := listen(nil)
		startListening(t, conn, l)
		time.Sleep(time.Second)

		conn.holdFrames(true)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted}) {
			t.Errorf("gap events while DAVE holds frames = %v, want one gap started", got)
		}
		conn.holdFrames(false)
		time.Sleep(time.Second)
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Errorf("gap events once DAVE passes frames = %v, want the gap started and ended", got)
		}
	})
}

// DAVE that never sets up is one stall, however long it lasts: Sentry hears
// of it once, and the recorder keeps asking for a fresh connection, at most
// once every 3 s, until the recording stops.
func TestReceiverRetriesAStallAtMostEvery3sAndReportsItOnce(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.holdFrames(true)
		r := startReceiver(t, conn, func(Frame) {})

		time.Sleep(10 * time.Second)
		r.close()
		atStop := conn.reconnectTimes()
		time.Sleep(10 * time.Second)
		synctest.Wait()

		if len(atStop) < 2 {
			t.Errorf("fresh connections asked for in 10 s of a stall = %d, want the recorder to keep trying", len(atStop))
		}
		got := conn.reconnectTimes()
		for i := 1; i < len(got); i++ {
			if gap := got[i].Sub(got[i-1]); gap < 3*time.Second {
				t.Errorf("fresh connections asked for %v apart, want at least 3 s", gap)
			}
		}
		if after := len(got) - len(atStop); after != 0 {
			t.Errorf("fresh connections asked for after the stop = %d, want none", after)
		}
		if n := len(events.Events()); n != 1 {
			t.Errorf("Sentry events = %d, want 1 for the whole stall", n)
		}
	})
}

// A DAVE setup that holds frames for under 2 s, as a join or a move's
// does, is no stall: Sentry hears nothing and nothing starts over.
func TestReceiverLeavesAShortDAVESetupAlone(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.holdFrames(true)
		startReceiver(t, conn, func(Frame) {})

		time.Sleep(1900 * time.Millisecond)
		conn.holdFrames(false)
		time.Sleep(10 * time.Second)
		synctest.Wait()

		if n := len(conn.reconnectTimes()); n != 0 {
			t.Errorf("fresh connections asked for = %d, want none for a setup that finished", n)
		}
		if n := len(events.Events()); n != 0 {
			t.Errorf("Sentry events = %d, want none for a setup that finished", n)
		}
	})
}

// A transport-only call, DAVE protocol version 0, never sets DAVE up, and
// that's no stall: dave-go's session is never ready there, and holds no
// frame.
func TestReceiverNeverCountsATransportOnlyCallAStall(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		dave := session.New(godave.UserID(testSpeaker), nil)
		dave.OnSelectProtocolAck(0)
		conn.dave = dave
		startReceiver(t, conn, func(Frame) {})

		time.Sleep(10 * time.Second)
		synctest.Wait()

		if n := len(conn.reconnectTimes()); n != 0 {
			t.Errorf("fresh connections asked for = %d, want none in a transport-only call", n)
		}
		if n := len(events.Events()); n != 0 {
			t.Errorf("Sentry events = %d, want none in a transport-only call", n)
		}
	})
}
