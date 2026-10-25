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
// the receiver asked for each fresh connection, and quietDrops counts the
// drops it asked for because nothing came from the voice server. down is
// whether the connection is lost, as lost says, until it comes back, and
// losses counts the drops.
type fakeReceiveConn struct {
	udp  *fakeVoiceUDP
	dave godave.Session

	mu         sync.Mutex
	users      map[uint32]snowflake.ID
	reconnects []time.Time
	quietDrops int
	down       bool
	lost       gapStart
	losses     int
	steps      recoveryLog
}

func newFakeReceiveConn() *fakeReceiveConn {
	udp := &fakeVoiceUDP{reads: make(chan udpRead, 64)}
	udp.socket.Store(1)
	return &fakeReceiveConn{
		udp:   udp,
		dave:  &fakeDAVE{},
		users: make(map[uint32]snowflake.ID),
	}
}

func (c *fakeReceiveConn) datagrams() datagramReader { return c.udp }
func (c *fakeReceiveConn) DAVE() godave.Session      { return c.dave }

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

// connection is connected until drop: the call harness covers what drops a
// connection and brings it back.
func (c *fakeReceiveConn) connection() (bool, gapStart, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.down, c.lost, c.losses
}

// drop has the connection lost now, for cause, until restore.
func (c *fakeReceiveConn) drop(cause GapCause) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down, c.lost = true, gapStart{at: time.Now(), cause: cause}
	c.losses++
}

// restore has the connection connected again.
func (c *fakeReceiveConn) restore() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down = false
}

// nothingCame loses the connection, as the recorder's gateway does when the
// receiver asks for a drop because nothing came from the voice server since
// last, unless it is lost already. The fake counts the asks.
func (c *fakeReceiveConn) nothingCame(last time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quietDrops++
	if !c.down {
		c.down, c.lost = true, gapStart{at: last, cause: GapVoiceServerSilent}
		c.losses++
	}
}

// quietDropsAsked counts the drops the receiver asked for because nothing
// came from the voice server.
func (c *fakeReceiveConn) quietDropsAsked() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quietDrops
}

func (c *fakeReceiveConn) server() string { return "c-dfw-1.discord.media:443" }

func (c *fakeReceiveConn) sessionOnline() bool { return true }

func (c *fakeReceiveConn) recovery() *recoveryLog { return &c.steps }

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

// sendReport queues an RTCP datagram of rtcpType, such as the receiver
// report the voice server sends every second.
func (c *fakeReceiveConn) sendReport(rtcpType byte) {
	c.udp.reads <- udpRead{rtcp: rtcpType}
}

// rtcpSenderReport is the RTCP packet type of a sender report (RFC 3550),
// which the voice server sends for a member's audio.
const rtcpSenderReport = 200

// newSocket has the UDP connection open a new socket, as every reconnect
// does.
func (c *fakeReceiveConn) newSocket() { c.udp.socket.Add(1) }

// udpRead is the datagram one read of the UDP connection returns: a voice
// packet, an RTCP datagram of type rtcp, or one it couldn't read.
type udpRead struct {
	p    *disgovoice.Packet
	rtcp byte
	err  error
}

// fakeVoiceUDP is the UDP connection: each read returns the next queued
// datagram, arriving as it is read, or a read timeout when none is waiting,
// as the adapter's own connection does. socket is the socket open now, last
// when the last datagram arrived, in Unix nanoseconds, and taken counts the
// queued datagrams read.
type fakeVoiceUDP struct {
	reads  chan udpRead
	socket atomic.Int32
	last   atomic.Int64
	taken  atomic.Int32
}

func (u *fakeVoiceUDP) readDatagram() (datagram, error) {
	socket := int(u.socket.Load())
	select {
	case r := <-u.reads:
		u.taken.Add(1)
		at := time.Now()
		u.last.Store(at.UnixNano())
		return datagram{socket: socket, at: at, rtcp: r.rtcp, packet: r.p, err: r.err}, nil
	case <-time.After(5 * time.Millisecond):
		return datagram{socket: socket}, os.ErrDeadlineExceeded
	}
}

func (u *fakeVoiceUDP) lastDatagram() time.Time {
	if last := u.last.Load(); last != 0 {
		return time.Unix(0, last)
	}
	return time.Time{}
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
// the gap events it hears in order, and when and why each gap started.
type listener struct {
	frame func(Frame)

	mu     sync.Mutex
	gaps   []string
	starts []time.Time
	causes []GapCause
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

func (l *listener) GapStarted(since time.Time, cause GapCause) {
	l.mu.Lock()
	l.starts = append(l.starts, since)
	l.causes = append(l.causes, cause)
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

// gapCauses is what started each gap heard so far, in order.
func (l *listener) gapCauses() []GapCause {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.causes)
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

// One speaker's frames DAVE can't decrypt, while another speaker's frames
// decrypt, are a speaker missing from an otherwise good recording, which
// nothing else would tell the maintainer of. Sentry hears of that speaker's
// run once, naming them, when another speaker's frame decrypts. Before
// that, with nobody else decrypting, the run is the recorder hearing
// nobody, which a decrypt gap covers, and Sentry hears nothing of it.
func TestReceiverReportsASpeakersDAVEFailuresOnceAnotherSpeakerIsHeard(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		const otherSpeaker, otherSSRC = "301", 8
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		conn.mapSSRC(otherSSRC, otherSpeaker)
		h := newHeard()
		startReceiver(t, conn, h.handle)

		for i := range 2 * failureBurst {
			conn.send(testSpeakerSSRC, uint32(i*960), sealFor("999", "words"))
			time.Sleep(20 * time.Millisecond)
		}
		synctest.Wait()
		if n := conn.udp.taken.Load(); n != 2*failureBurst {
			t.Fatalf("datagrams read = %d, want all %d", n, 2*failureBurst)
		}
		if n := len(events.Events()); n != 0 {
			t.Fatalf("Sentry events with nobody else decrypting = %d, want none", n)
		}
		conn.send(otherSSRC, 1000, sealFor(otherSpeaker, "copy"))
		conn.send(otherSSRC, 1960, sealFor(otherSpeaker, "that"))
		h.next(t)
		h.next(t)

		got := events.Events()
		if len(got) != 1 {
			t.Fatalf("Sentry events once another speaker is heard = %d, want 1", len(got))
		}
		if msg := got[0].Tags["message"]; msg != daveDecryptFailuresMessage {
			t.Errorf("Sentry event's message = %q, want %q", msg, daveDecryptFailuresMessage)
		}
		for key, want := range map[string]string{"user_id": testSpeaker, "recorder": "recorder-1"} {
			if v := got[0].Contexts["extra"][key]; v != want {
				t.Errorf("Sentry event's %s = %v, want %s", key, v, want)
			}
		}
	})
}

// A recorder whose frames won't decrypt hears nobody. A run of
// decryptRunLimit failures with no frame decrypted, frames DAVE refuses and
// datagrams the UDP connection can't read alike, starts a gap named for it,
// and the receiver asks for one fresh voice connection, which starts DAVE
// over. Here two speakers' frames fail on the first socket and go on failing
// on the fresh connection's, and no second fresh connection is asked for.
// The gap's report counts every failure from the run's first, on both
// sockets, and the speakers whose frames failed.
func TestReceiverStartsAGapWhenNothingDecrypts(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		const otherSpeaker, otherSSRC = "301", 8
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		conn.mapSSRC(otherSSRC, otherSpeaker)
		l := listen(nil)
		r := startListening(t, conn, l)

		// Each 20 ms, a frame from each speaker that DAVE refuses, and
		// every fourth time a datagram the UDP connection can't read.
		frames, unreadable := 0, 0
		fail := func(rounds int) {
			for i := range rounds {
				conn.send(testSpeakerSSRC, uint32(i*960), sealFor("999", "words"))
				conn.send(otherSSRC, uint32(i*960), sealFor("999", "words"))
				frames += 2
				if i%4 == 0 {
					conn.sendUnreadable()
					unreadable++
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		fail(decryptRunLimit)
		conn.newSocket()
		fail(decryptRunLimit)
		synctest.Wait()

		if got := l.gapCauses(); !slices.Equal(got, []GapCause{GapNothingDecrypted}) {
			t.Fatalf("gap causes = %v, want one %s", got, GapNothingDecrypted)
		}
		if n := len(conn.reconnectTimes()); n != 1 {
			t.Errorf("fresh connections asked for = %d, want 1", n)
		}
		rep := r.report()
		for key, want := range map[string]int{"speakers_failing": 2, "frames_failed": frames, "unreadable_datagrams": unreadable} {
			if got, _ := reportField(rep, key); got != want {
				t.Errorf("gap report's %s = %v, want %d", key, got, want)
			}
		}
	})
}

// A decrypt run's gap starts where the failures stopped letting up: at the
// first failure of the run's latest unbroken stretch. Here two lone failures
// come 20 s apart, then a second later failures every 20 ms. The gap starts
// at the first of those, so the lone ones don't use up the 10 s a gap has.
func TestReceiverDatesADecryptGapAtTheRunsLatestStretch(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		l := listen(nil)
		startListening(t, conn, l)

		refused := sealFor("999", "words")
		conn.send(testSpeakerSSRC, 0, refused)
		time.Sleep(20 * time.Second)
		conn.send(testSpeakerSSRC, 960, refused)
		time.Sleep(time.Second)
		stretch := time.Now()
		for i := range decryptRunLimit {
			conn.send(testSpeakerSSRC, uint32((i+2)*960), refused)
			time.Sleep(20 * time.Millisecond)
		}
		synctest.Wait()

		starts := l.gapStarts()
		if len(starts) != 1 {
			t.Fatalf("gaps started = %d, want 1", len(starts))
		}
		if !starts[0].Equal(stretch) {
			t.Errorf("gap started %v after the latest stretch's first failure, want at it", starts[0].Sub(stretch))
		}
	})
}

// A decrypt run's gap ends at the first frame that decrypts, the first
// proof the recorder hears the channel again, and not once the fresh
// connection is ready. Here the fresh connection is connected with DAVE
// passing frames at once, and a second more of frames DAVE refuses leaves
// the gap running. The next frame decrypts, reaches the recording, and ends
// the gap.
func TestReceiverEndsADecryptGapAtTheFirstFrameThatDecrypts(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		h := newHeard()
		l := listen(h.handle)
		startListening(t, conn, l)

		sent := decryptRunLimit + 50
		for i := range sent {
			conn.send(testSpeakerSSRC, uint32(i*960), sealFor("999", "words"))
			time.Sleep(20 * time.Millisecond)
		}
		synctest.Wait()
		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted}) {
			t.Fatalf("gap events with frames still refused = %v, want the gap started and running", got)
		}
		conn.send(testSpeakerSSRC, uint32(sent*960), sealFor(testSpeaker, "heard"))
		h.expectSpeaker(t, "heard")
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Errorf("gap events once a frame decrypts = %v, want the gap started and ended", got)
		}
	})
}

// A datagram the transport decryption refuses is a failure to decrypt too.
// A recorder getting nothing but those hears nobody, and a run of them
// starts a decrypt gap like refused frames do.
func TestReceiverCountsUnreadableDatagramsTowardTheDecryptRun(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		l := listen(nil)
		startListening(t, conn, l)

		for range decryptRunLimit {
			conn.sendUnreadable()
			time.Sleep(20 * time.Millisecond)
		}
		synctest.Wait()

		if got := l.gapCauses(); !slices.Equal(got, []GapCause{GapNothingDecrypted}) {
			t.Errorf("gap causes = %v, want one %s", got, GapNothingDecrypted)
		}
	})
}

// The decrypt run starts no gap while another runs, so the gap keeps the
// cause that started it, and asks for no fresh connection on top of the
// recovery under way. Here the voice gateway closes, and frames DAVE
// refuses go on arriving on the old socket, twice the run's limit.
func TestReceiverDecryptRunStartsNoGapWhileAnotherRuns(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		l := listen(nil)
		startListening(t, conn, l)
		time.Sleep(time.Second)

		conn.drop(GapVoiceGatewayClosed)
		for i := range 2 * decryptRunLimit {
			conn.send(testSpeakerSSRC, uint32(i*960), sealFor("999", "words"))
			time.Sleep(20 * time.Millisecond)
		}
		synctest.Wait()

		if got := l.gapCauses(); !slices.Equal(got, []GapCause{GapVoiceGatewayClosed}) {
			t.Errorf("gap causes = %v, want one %s", got, GapVoiceGatewayClosed)
		}
		if n := len(conn.reconnectTimes()); n != 0 {
			t.Errorf("fresh connections asked for = %d, want none while a gap runs", n)
		}
	})
}

// A run of datagrams the UDP connection can't read, such as a transport key
// that no longer matches, logs one WARN, not one a datagram, and sends
// Sentry nothing: a run long enough to cost the recording is a decrypt gap,
// which reaches Sentry if it cuts the recording short.
func TestReceiverWarnsOfABurstOfUnreadablePacketsOnce(t *testing.T) {
	events := recordSentry(t)
	logs := captureLogs(t)
	conn := newFakeReceiveConn()
	conn.mapSSRC(testSpeakerSSRC, testSpeaker)
	h := newHeard()
	startReceiver(t, conn, h.handle)

	for range 2 * failureBurst {
		conn.sendUnreadable()
	}
	conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "words"))
	h.next(t)

	if n := logs.warnings(unreadableMessage); n != 1 {
		t.Errorf("%q WARNs = %d, want 1 for the burst", unreadableMessage, n)
	}
	if n := len(events.Events()); n != 0 {
		t.Errorf("Sentry events = %d, want none", n)
	}
}

// Once DAVE sets up after a stall, the speaker's frames reach the recording
// as theirs again, and the log says how long the recorder couldn't hear. A
// stall that recovers costs a few seconds of silence, as a short drop does,
// so it stays in the log as a WARN and sends Sentry nothing.
func TestReceiverHearsTheSpeakerAgainOnceDAVESetsUpAfterAStall(t *testing.T) {
	events := recordSentry(t)
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
		if n := logs.warnings(stallMessage); n != 1 {
			t.Errorf("%q WARNs = %d, want 1", stallMessage, n)
		}
		if n := len(events.Events()); n != 0 {
			t.Errorf("Sentry events = %d, want none for a stall that recovered", n)
		}
	})
}

// DAVE holding every frame past its stall limit is a gap: the recorder
// hears nobody. The gap ends once DAVE passes frames again, and the
// listener hears of it once each way, however long DAVE held them.
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

// DAVE holding every frame past its stall limit starts a gap named for the
// stall. The recorder last heard the channel when DAVE started holding, so
// the gap starts then, not at the limit.
func TestReceiverNamesTheGapOfAStalledDAVESetupFromWhenItStartedHolding(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		l := listen(nil)
		startListening(t, conn, l)
		time.Sleep(time.Second)

		held := time.Now()
		conn.holdFrames(true)
		time.Sleep(stallAfter + time.Second)
		synctest.Wait()

		causes, starts := l.gapCauses(), l.gapStarts()
		if len(causes) != 1 || causes[0] != GapDAVEStalled {
			t.Fatalf("gap causes = %v, want one %s", causes, GapDAVEStalled)
		}
		if starts[0].Before(held) || !starts[0].Before(held.Add(stallAfter)) {
			t.Errorf("gap started %v after DAVE started holding, want at it, inside the %v stall limit", starts[0].Sub(held), stallAfter)
		}
	})
}

// A stalled DAVE setup's gap report gives how long DAVE has held frames by
// the time the report is read, here 7 s after DAVE started holding them.
// The receiver notices the hold within one of its pauses.
func TestReceiverReportsHowLongDAVEHasHeldFramesInAStall(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		r := startListening(t, conn, listen(nil))
		time.Sleep(time.Second)

		conn.holdFrames(true)
		time.Sleep(7 * time.Second)
		synctest.Wait()

		got, _ := reportField(r.report(), "held_for")
		if d, ok := got.(time.Duration); !ok || d > 7*time.Second || d < 7*time.Second-notReadyPause {
			t.Errorf("gap report's held_for = %v, want 7s, how long DAVE has held frames", got)
		}
	})
}

// A gap keeps the cause that started it, whatever trouble follows while
// the recorder recovers. Here the voice server's heartbeat answer is
// overdue, the rejoin's DAVE setup stalls, and the connection drops again
// before DAVE sets up: all one gap, named for the overdue answer. It ends
// once the recorder is connected with DAVE passing frames.
func TestReceiverKeepsTheCauseThatStartedAGapThroughItsRecovery(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		l := listen(nil)
		startListening(t, conn, l)
		time.Sleep(time.Second)

		conn.drop(GapHeartbeatOverdue)
		time.Sleep(time.Second)
		conn.holdFrames(true)
		conn.restore()
		time.Sleep(stallAfter + time.Second)
		conn.drop(GapVoiceGatewayClosed)
		time.Sleep(time.Second)
		conn.restore()
		conn.holdFrames(false)
		time.Sleep(time.Second)
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Fatalf("gap events = %v, want one gap started and ended", got)
		}
		if got := l.gapCauses(); got[0] != GapHeartbeatOverdue {
			t.Errorf("gap cause = %s, want %s, the trouble that started it", got[0], GapHeartbeatOverdue)
		}
	})
}

// A lost connection is a gap however briefly it lasts. Here it drops and
// comes back before the receiver looks again, and the listener still hears
// the gap start, dated at the drop, and end.
func TestReceiverToldOfALossThatEndedBeforeItLooked(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		l := listen(nil)
		startListening(t, conn, l)
		time.Sleep(time.Second)

		dropped := time.Now()
		conn.drop(GapVoiceGatewayClosed)
		conn.restore()
		time.Sleep(time.Second)
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Fatalf("gap events = %v, want one gap started and ended", got)
		}
		if got := l.gapStarts(); !got[0].Equal(dropped) {
			t.Errorf("gap started %v after the drop, want at it", got[0].Sub(dropped))
		}
	})
}

// DAVE that never sets up is one stall, however long it lasts: the log
// warns of it once, and the recorder keeps asking for a fresh connection, at
// most once every 3 s, until the recording stops.
func TestReceiverRetriesAStallAtMostEvery3sAndWarnsOnce(t *testing.T) {
	recordSentry(t)
	logs := captureLogs(t)
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
		if n := logs.warnings(stallMessage); n != 1 {
			t.Errorf("%q WARNs = %d, want 1 for the whole stall", stallMessage, n)
		}
	})
}

// A DAVE setup that holds frames inside its stall limit, as a join or a
// move's does, is no stall: Sentry hears nothing, nothing starts over, and
// the recording hears of no gap.
func TestReceiverLeavesAShortDAVESetupAlone(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.holdFrames(true)
		l := listen(nil)
		startListening(t, conn, l)

		time.Sleep(stallAfter / 2)
		conn.holdFrames(false)
		time.Sleep(10 * time.Second)
		synctest.Wait()

		if n := len(conn.reconnectTimes()); n != 0 {
			t.Errorf("fresh connections asked for = %d, want none for a setup that finished", n)
		}
		if n := len(events.Events()); n != 0 {
			t.Errorf("Sentry events = %d, want none for a setup that finished", n)
		}
		if got := l.gapEvents(); len(got) != 0 {
			t.Errorf("gap events = %v, want none for a setup that finished", got)
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

// A voice server whose UDP path dies while its websocket stays up sends the
// recorder nothing more. Once a receiver report has armed the media watch,
// silentAfter with no datagram at all starts a gap named for the quiet
// voice server, dated at the last datagram, and the receiver asks for a
// drop, which rejoins the call. The gap's report gives how long before it
// was read the last datagram came. A watch that armed never warns it didn't,
// however long its reports came before the voice server went quiet.
func TestReceiverStartsAGapWhenNothingComesFromTheVoiceServer(t *testing.T) {
	recordSentry(t)
	logs := captureLogs(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		l := listen(nil)
		r := startListening(t, conn, l)

		// The voice server's first receiver report comes up to a second
		// after the recorder connects.
		time.Sleep(500 * time.Millisecond)
		for range 6 {
			conn.sendReport(rtcpReceiverReport)
			time.Sleep(time.Second)
		}
		last := time.Now()
		conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "last words"))
		time.Sleep(silentAfter + time.Second)
		synctest.Wait()

		causes, starts := l.gapCauses(), l.gapStarts()
		if len(causes) != 1 || causes[0] != GapVoiceServerSilent {
			t.Fatalf("gap causes = %v, want one %s", causes, GapVoiceServerSilent)
		}
		if !starts[0].Equal(last) {
			t.Errorf("gap started %v after the last datagram, want at it", starts[0].Sub(last))
		}
		if conn.quietDropsAsked() == 0 {
			t.Error("no drop asked for, want the receiver to ask for one")
		}
		got, _ := reportField(r.report(), "last_datagram")
		if want := time.Since(last); got != want {
			t.Errorf("gap report's last_datagram = %v, want %v, how long ago the last datagram came", got, want)
		}
		if n := logs.warnings(mediaNeverArmedMessage); n != 0 {
			t.Errorf("%q WARNs = %d, want none from a watch that armed", mediaNeverArmedMessage, n)
		}
	})
}

// Only a receiver report arms the media watch. The voice server stops
// sending a sender report, and a member's audio stops, once members go quiet
// or leave, so neither says the path stays up. Here a sender report and a
// member's audio arrive, then 10 s of nothing: the watch never armed, so no
// gap starts and no drop is asked for.
func TestReceiverMediaWatchArmsOnlyAtAReceiverReport(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		h := newHeard()
		l := listen(h.handle)
		startListening(t, conn, l)

		conn.sendReport(rtcpSenderReport)
		conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "words"))
		h.expectSpeaker(t, "words")
		time.Sleep(10 * time.Second)
		synctest.Wait()

		if got := l.gapEvents(); len(got) != 0 {
			t.Errorf("gap events = %v, want none from a watch no receiver report armed", got)
		}
		if n := conn.quietDropsAsked(); n != 0 {
			t.Errorf("drops asked for = %d, want none", n)
		}
	})
}

// Every reconnect opens a new UDP socket, and the media watch starts over
// unarmed on it: a receiver report on the old socket says nothing of the
// new one's path. Here a receiver report arms the watch on one socket, the
// next socket gets only a member's audio, then 10 s of nothing: no gap
// starts and no drop is asked for.
func TestReceiverMediaWatchStartsUnarmedOnEachNewSocket(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		h := newHeard()
		l := listen(h.handle)
		startListening(t, conn, l)

		conn.sendReport(rtcpReceiverReport)
		time.Sleep(time.Second)
		conn.newSocket()
		conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "words"))
		h.expectSpeaker(t, "words")
		time.Sleep(10 * time.Second)
		synctest.Wait()

		if got := l.gapEvents(); len(got) != 0 {
			t.Errorf("gap events = %v, want none from a new socket no receiver report armed", got)
		}
		if n := conn.quietDropsAsked(); n != 0 {
			t.Errorf("drops asked for = %d, want none", n)
		}
	})
}

// A quiet channel is no quiet voice server: with nobody talking, the voice
// server still sends a receiver report every second, which keeps the media
// watch fed. 30 s of reports and nothing else start no gap and ask for no
// drop.
func TestReceiverMediaWatchLeavesAQuietChannelAlone(t *testing.T) {
	recordSentry(t)
	logs := captureLogs(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		l := listen(nil)
		startListening(t, conn, l)

		for range 30 {
			conn.sendReport(rtcpReceiverReport)
			time.Sleep(time.Second)
		}
		synctest.Wait()

		if got := l.gapEvents(); len(got) != 0 {
			t.Errorf("gap events = %v, want none in a quiet channel", got)
		}
		if n := conn.quietDropsAsked(); n != 0 {
			t.Errorf("drops asked for = %d, want none", n)
		}
		if n := logs.warnings(mediaNeverArmedMessage); n != 0 {
			t.Errorf("%q WARNs = %d, want none once a receiver report armed the watch", mediaNeverArmedMessage, n)
		}
	})
}

// While DAVE holds frames the receiver leaves the socket unread, and what
// arrives waits there, so the media watch stands still and starts its clock
// over once reading resumes. Here DAVE holds frames for 5 s, long enough to
// stall, from 1 s after the last receiver report. The frame that comes just
// after the hold reaches the recording, the stall's gap ends, and no gap
// starts for a quiet voice server.
func TestReceiverMediaWatchStandsStillWhileDAVEHoldsFrames(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		h := newHeard()
		l := listen(h.handle)
		startListening(t, conn, l)

		conn.sendReport(rtcpReceiverReport)
		time.Sleep(time.Second)
		conn.holdFrames(true)
		time.Sleep(5 * time.Second)
		conn.holdFrames(false)
		time.Sleep(500 * time.Millisecond)
		conn.send(testSpeakerSSRC, 1000, sealFor(testSpeaker, "after the hold"))
		h.expectSpeaker(t, "after the hold")
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Errorf("gap events = %v, want the stall's gap started and ended", got)
		}
		if got := l.gapCauses(); slices.Contains(got, GapVoiceServerSilent) {
			t.Errorf("gap causes = %v, want no %s", got, GapVoiceServerSilent)
		}
		if n := conn.quietDropsAsked(); n != 0 {
			t.Errorf("drops asked for = %d, want none", n)
		}
	})
}

// The media watch starts no gap while another runs, so the gap keeps the
// cause that started it. Here the voice gateway closes, and while the
// recorder rejoins, the new socket gets a receiver report before the
// connection completes, then nothing for 10 s: one gap, named for the
// close, and no drop asked for.
func TestReceiverMediaWatchStartsNoGapWhileAnotherRuns(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		l := listen(nil)
		startListening(t, conn, l)
		conn.sendReport(rtcpReceiverReport)
		time.Sleep(time.Second)

		conn.drop(GapVoiceGatewayClosed)
		conn.newSocket()
		conn.sendReport(rtcpReceiverReport)
		time.Sleep(10 * time.Second)
		synctest.Wait()

		if got := l.gapCauses(); !slices.Equal(got, []GapCause{GapVoiceGatewayClosed}) {
			t.Errorf("gap causes = %v, want one %s", got, GapVoiceGatewayClosed)
		}
		if n := conn.quietDropsAsked(); n != 0 {
			t.Errorf("drops asked for = %d, want none while a gap runs", n)
		}
	})
}

// Discord could stop sending receiver reports, and the media watch would go
// blind. A connection still unarmed 5 s after it is connected with DAVE
// ready logs one WARN naming the recorder, the channel and the voice
// server. Here a member talks for 11 s with no receiver report.
func TestReceiverWarnsOnceWhenTheMediaWatchNeverArms(t *testing.T) {
	recordSentry(t)
	logs := captureLogs(t)
	synctest.Test(t, func(t *testing.T) {
		conn := newFakeReceiveConn()
		conn.mapSSRC(testSpeakerSSRC, testSpeaker)
		startListening(t, conn, listen(nil))

		for i := range 11 {
			conn.send(testSpeakerSSRC, uint32(i*48000), sealFor(testSpeaker, "words"))
			time.Sleep(time.Second)
		}
		synctest.Wait()

		if n := logs.warnings(mediaNeverArmedMessage); n != 1 {
			t.Fatalf("%q WARNs = %d, want 1", mediaNeverArmedMessage, n)
		}
		for key, want := range map[string]string{"recorder": "recorder-1", "channel_id": testChannel, "voice_server": conn.server()} {
			if n := logs.linesSaying(slog.LevelWarn, mediaNeverArmedMessage, key, want); n != 1 {
				t.Errorf("%q WARN with %s=%s: %d, want 1", mediaNeverArmedMessage, key, want, n)
			}
		}
	})
}
