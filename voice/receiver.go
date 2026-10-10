package voice

import (
	"errors"
	"net"
	"os"
	"sync"
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
// frame whose SSRC isn't mapped yet until it is.

// receiveConn is a disgo voice connection as the receiver reads it.
type receiveConn interface {
	UDP() disgovoice.UDPConn
	DAVE() godave.Session
	UserIDBySSRC(ssrc uint32) snowflake.ID
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
	// daveFailureBurst is how many of one speaker's frames in a row DAVE
	// fails to decrypt before Sentry hears of it: half a second of audio.
	// A join or leave costs a frame or two at most.
	daveFailureBurst = 25
)

// receiver reads one voice connection's packets on a goroutine of its own
// and hands each speaker's frames over, in the order they arrived.
type receiver struct {
	conn     receiveConn
	handle   func(Frame)
	recorder string

	stop chan struct{}
	done chan struct{}
	once sync.Once

	// held is the frames read on each SSRC no user is mapped to yet, still
	// DAVE-encrypted, since DAVE decrypts as a user. Only the receiver's
	// goroutine touches it.
	held map[uint32][]*disgovoice.Packet
	// failing counts each speaker's frames DAVE has failed to decrypt
	// since their last that decrypted. Only the receiver's goroutine
	// touches it.
	failing map[snowflake.ID]int
}

// newReceiver builds the receiver of a recorder's connection. It hands
// frames to handle once open starts it.
func newReceiver(conn receiveConn, handle func(Frame), recorderID string) *receiver {
	return &receiver{
		conn:     conn,
		handle:   handle,
		recorder: recorderID,
		stop:     make(chan struct{}),
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
	r.once.Do(func() { close(r.stop) })
	<-r.done
}

func (r *receiver) run() {
	defer close(r.done)
	for {
		select {
		case <-r.stop:
			return
		default:
		}
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
	if holdFrames(r.conn.DAVE()) {
		r.pause(notReadyPause)
		return
	}
	r.release()
	p, err := r.conn.UDP().ReadPacket()
	switch {
	case err == nil:
		r.receive(p)
	case errors.Is(err, os.ErrDeadlineExceeded):
		// Nothing arrived. The next step looks at the held frames again.
	case errors.Is(err, net.ErrClosed), errors.Is(err, errUDPNotOpen):
		// A voice reconnect closes the socket, and disgo opens a new one.
		r.pause(closedPause)
	default:
		utils.Debug("Voice packet dropped", "recorder", r.recorder, "error", err)
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

// pause sleeps for d, or until close.
func (r *receiver) pause(d time.Duration) {
	select {
	case <-r.stop:
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
		if r.failing[userID] == daveFailureBurst {
			utils.CaptureError("DAVE decrypt failures", err, "recorder", r.recorder,
				"user_id", userID.String(), "frames", daveFailureBurst)
		}
		return
	}
	if failed := r.failing[userID]; failed >= daveFailureBurst {
		utils.Info("DAVE decrypt recovered", "recorder", r.recorder, "user_id", userID.String(), "frames_lost", failed)
	}
	delete(r.failing, userID)
	r.handle(Frame{UserID: userID.String(), SSRC: p.SSRC, Timestamp: p.Timestamp, Opus: out[:n]})
}
