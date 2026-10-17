package voice

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"github.com/7cav/cavbot2/utils"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
)

// The audio receiver replaces disgo's default one (ADR 0014), which spins a
// core while DAVE isn't ready, stops for good when a voice reconnect closes
// the UDP socket, never reads in a transport-only session such as a stage
// channel, and drops a speaker's frames that arrive before op 5 names them.
// This one reads packets with only the transport encryption off, takes the
// DAVE encryption off itself as the speaker the SSRC maps to, and holds a
// frame whose SSRC isn't mapped yet until it is. It also watches DAVE's
// setup, which Discord can leave unanswered: a setup that stalls reaches
// Sentry, and the recorder starts it over on a fresh voice connection.

// receiveConn is a disgo voice connection as the receiver reads it.
type receiveConn interface {
	UDP() disgovoice.UDPConn
	DAVE() godave.Session
	UserIDBySSRC(ssrc uint32) snowflake.ID
	// ChannelID is the call's channel, nil while there is none.
	ChannelID() *snowflake.ID
	// connection reports whether the recorder is connected in the call, at
	// the voice server Discord named last, and when it last stopped being
	// connected, zero before it ever was.
	connection() (connected bool, lostAt time.Time)
	// server is the voice server the recorder connects to: the one Discord
	// named last for the call.
	server() string
	// reconnect opens a fresh voice connection in the call, which starts
	// DAVE over, and returns once it has opened, or with ctx's error.
	reconnect(ctx context.Context) error
}

const (
	// notReadyPause is how long the receiver sleeps while DAVE holds
	// frames. Packets wait in the socket meanwhile.
	notReadyPause = 20 * time.Millisecond
	// closedPause is how long the receiver sleeps while the UDP socket is
	// closed or not yet open, waiting for disgo to open it.
	closedPause = 250 * time.Millisecond
	// heldPerSSRC caps the frames held for one SSRC while op 5 hasn't
	// mapped it: 5 s of audio. The oldest goes first.
	heldPerSSRC = 250
	// failureBurst is how many failures in a row Sentry hears of once:
	// datagrams the UDP connection can't read, or one speaker's frames DAVE
	// can't decrypt. Half a second of one speaker's audio. A join or leave
	// costs a frame or two at most.
	failureBurst = 25
	// stallAfter is how long one DAVE setup may hold frames before the
	// recorder counts it a stall. Every setup seen on the test guild
	// finished within 0.7 s of the session description.
	stallAfter = 3 * time.Second
	// reconnectTimeout bounds the open of one fresh voice connection.
	reconnectTimeout = 5 * time.Second
)

// stallMessage is what Sentry hears when DAVE stalls: the recorder hears
// nobody in the channel. daveRecoveredMessage is the Info line once it
// hears the channel again.
const (
	stallMessage         = "DAVE setup stalled"
	daveRecoveredMessage = "DAVE setup recovered"
)

// errDAVEStalled is the error a stall reaches Sentry with.
var errDAVEStalled = errors.New("voice: DAVE held every frame past the stall limit")

// gapMessage is what Sentry hears when a gap reaches GapReportedAfter, and
// errGap the error it comes with.
const gapMessage = "Recorder can't hear the channel"

var errGap = errors.New("voice: the recorder couldn't hear the channel for 10 s")

// receiver reads one voice connection's packets on a goroutine of its own
// and hands each speaker's frames over, in the order they arrived. It tells
// its listener when a gap starts and ends.
type receiver struct {
	conn     receiveConn
	listener Listener
	recorder string

	// ctx ends at close, and with it any fresh connection in flight.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	// held is the frames read on each SSRC no user is mapped to yet, still
	// DAVE-encrypted, since DAVE decrypts as a user. Only the receiver's
	// goroutine touches it.
	held map[uint32][]*disgovoice.Packet
	// failing counts each speaker's frames DAVE has failed to decrypt
	// since their last that decrypted, and unreadable the datagrams the
	// UDP connection has failed to read since its last good one. Only the
	// receiver's goroutine touches them.
	failing    map[snowflake.ID]int
	unreadable int
	// unheardSince is when DAVE started holding frames, zero while it
	// holds none, and setupSince when the DAVE setup holding them now
	// started: then, or at the last fresh connection. stalled is whether
	// Sentry has heard of the stall. Only the receiver's goroutine touches
	// them.
	unheardSince time.Time
	setupSince   time.Time
	stalled      bool
	// inGap is whether the listener was last told a gap started, at
	// gapSince. gapReported is whether Sentry has heard of that gap. Only
	// the receiver's goroutine touches them.
	inGap       bool
	gapSince    time.Time
	gapReported bool
}

// newReceiver builds the receiver of a recorder's connection. It hands
// what it hears to l once open starts it.
func newReceiver(conn receiveConn, l Listener, recorderID string) *receiver {
	ctx, cancel := context.WithCancel(context.Background())
	return &receiver{
		conn:     conn,
		listener: l,
		recorder: recorderID,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		held:     make(map[uint32][]*disgovoice.Packet),
		failing:  make(map[snowflake.ID]int),
	}
}

// open starts reading.
func (r *receiver) open() { go r.run() }

// close stops reading and returns once the receiver's goroutine has, so no
// frame is handed over after. The receiver must have been opened.
func (r *receiver) close() {
	r.cancel()
	<-r.done
}

func (r *receiver) run() {
	defer close(r.done)
	for r.ctx.Err() == nil {
		r.step()
	}
}

// step reads and hands over at most one packet, after any held frames
// whose SSRC has been mapped since. A panic, such as one from the code the
// frame is handed to, is recovered here and costs that step alone: an
// unrecovered one would take temp VC and every command down with the
// recording.
func (r *receiver) step() {
	defer utils.RecoverPanic("recorder-receive", "recorder", r.recorder)
	holding := holdFrames(r.conn.DAVE())
	connected, lostAt := r.conn.connection()
	since := time.Now()
	if !connected && !lostAt.IsZero() {
		since = lostAt
	}
	r.watchGap(connected && !holding, since)
	if holding {
		r.waitOrStartOver()
		return
	}
	r.endStall()
	r.release()
	p, err := r.conn.UDP().ReadPacket()
	switch {
	case err == nil:
		r.unreadable = 0
		r.receive(p)
	case errors.Is(err, os.ErrDeadlineExceeded):
		// Nothing arrived. The next step looks at the held frames again.
	case errors.Is(err, net.ErrClosed), errors.Is(err, errUDPNotOpen):
		// A voice reconnect closes the socket, and disgo opens a new one.
		r.pause(closedPause)
	default:
		r.unreadable++
		if r.unreadable == failureBurst {
			utils.CaptureError("Voice packets unreadable", err, "recorder", r.recorder, "packets", failureBurst)
		}
	}
}

// watchGap tells the listener when the recorder stops hearing the channel,
// and when it hears it again. A gap starts at since: when the connection to
// the call dropped, which the receiver can learn of only later, or now. A
// gap that reaches GapReportedAfter reaches Sentry once, with the voice
// server the recorder is trying.
func (r *receiver) watchGap(hearing bool, since time.Time) {
	switch {
	case !hearing && !r.inGap:
		r.inGap, r.gapSince, r.gapReported = true, since, false
		r.listener.GapStarted(r.gapSince)
	case !hearing && !r.gapReported && time.Since(r.gapSince) >= GapReportedAfter:
		r.gapReported = true
		utils.CaptureError(gapMessage, errGap, "recorder", r.recorder, "channel_id", channelOf(r.conn),
			"voice_server", r.conn.server())
	case hearing && r.inGap:
		r.inGap = false
		r.listener.GapEnded()
	}
}

// holdFrames reports whether the DAVE session wants frames left unread: a
// DAVE call whose encryption isn't set up yet. dave-go says so through
// ShouldHoldFrames, which stays false in a transport-only session such as a
// stage channel, where Ready never turns true.
func holdFrames(s godave.Session) bool {
	if h, ok := s.(interface{ ShouldHoldFrames() bool }); ok {
		return h.ShouldHoldFrames()
	}
	return !s.Ready()
}

// waitOrStartOver pauses while DAVE holds frames. A setup that holds them
// past stallAfter has stalled, and the recorder hears nobody: Sentry hears
// of the stall once, and the recorder opens a fresh voice connection, whose
// session description starts DAVE over. That can take up to
// reconnectTimeout.
func (r *receiver) waitOrStartOver() {
	now := time.Now()
	if r.unheardSince.IsZero() {
		r.unheardSince, r.setupSince = now, now
	}
	if now.Sub(r.setupSince) <= stallAfter {
		r.pause(notReadyPause)
		return
	}
	if !r.stalled {
		// The stall's report is the gap's too: Sentry hears no more of it.
		r.stalled, r.gapReported = true, true
		utils.CaptureError(stallMessage, errDAVEStalled, "recorder", r.recorder, "channel_id", channelOf(r.conn))
	}
	r.reconnect()
	r.setupSince = time.Now()
}

// endStall notes DAVE passing frames, which ends any stall. After one, the
// log says how long the recorder couldn't hear.
func (r *receiver) endStall() {
	if r.stalled {
		utils.Info(daveRecoveredMessage, "recorder", r.recorder, "channel_id", channelOf(r.conn),
			"unheard", time.Since(r.unheardSince))
	}
	r.unheardSince, r.stalled = time.Time{}, false
}

// reconnect opens a fresh voice connection. One that fails leaves DAVE
// holding frames, so the next stall tries again.
func (r *receiver) reconnect() {
	ctx, cancel := context.WithTimeout(r.ctx, reconnectTimeout)
	defer cancel()
	if err := r.conn.reconnect(ctx); err != nil && r.ctx.Err() == nil {
		utils.Warn("Fresh voice connection failed", "recorder", r.recorder, "error", err)
	}
}

// channelOf is the ID of conn's channel, empty while it has none.
func channelOf(conn receiveConn) string {
	if id := conn.ChannelID(); id != nil {
		return id.String()
	}
	return ""
}

// pause sleeps for d, or until close.
func (r *receiver) pause(d time.Duration) {
	select {
	case <-r.ctx.Done():
	case <-time.After(d):
	}
}

// receive hands a packet over as the user its SSRC maps to, after any
// frames held on that SSRC, or holds it while the SSRC maps to no one.
func (r *receiver) receive(p *disgovoice.Packet) {
	userID := r.conn.UserIDBySSRC(p.SSRC)
	if userID == 0 {
		held := append(r.held[p.SSRC], p)
		if len(held) > heldPerSSRC {
			held = held[1:]
		}
		r.held[p.SSRC] = held
		return
	}
	r.releaseSSRC(p.SSRC, userID)
	r.deliver(userID, p)
}

// release hands over the held frames of every SSRC mapped since they
// arrived.
func (r *receiver) release() {
	for ssrc := range r.held {
		if userID := r.conn.UserIDBySSRC(ssrc); userID != 0 {
			r.releaseSSRC(ssrc, userID)
		}
	}
}

// releaseSSRC hands over the frames held on an SSRC as the user it now
// maps to, in the order they arrived.
func (r *receiver) releaseSSRC(ssrc uint32, userID snowflake.ID) {
	held := r.held[ssrc]
	delete(r.held, ssrc)
	for _, p := range held {
		r.deliver(userID, p)
	}
}

// deliver takes the DAVE encryption off a packet as its speaker and hands
// the frame over.
func (r *receiver) deliver(userID snowflake.ID, p *disgovoice.Packet) {
	dave := r.conn.DAVE()
	user := godave.UserID(userID.String())
	out := make([]byte, dave.MaxDecryptedFrameSize(user, len(p.Opus)))
	n, err := dave.Decrypt(user, p.Opus, out)
	if err != nil {
		r.failing[userID]++
		if r.failing[userID] == failureBurst {
			utils.CaptureError("DAVE decrypt failures", err, "recorder", r.recorder,
				"user_id", userID.String(), "frames", failureBurst)
		}
		return
	}
	if failed := r.failing[userID]; failed >= failureBurst {
		utils.Info("DAVE decrypt recovered", "recorder", r.recorder, "user_id", userID.String(), "frames_lost", failed)
	}
	delete(r.failing, userID)
	r.listener.Frame(Frame{UserID: userID.String(), SSRC: p.SSRC, Timestamp: p.Timestamp, Opus: out[:n]})
}
