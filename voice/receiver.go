package voice

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/7cav/cavbot2/utils"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

// The audio receiver replaces disgo's default one (ADR 0014), which spins a
// core while DAVE isn't ready, stops for good when a voice reconnect closes
// the UDP socket, never reads in a transport-only session such as a stage
// channel, and drops a speaker's frames that arrive before op 5 names them.
// This one reads packets with only the transport encryption off, takes the
// DAVE encryption off itself as the speaker the SSRC maps to, and holds a
// frame whose SSRC isn't mapped yet until it is. It also watches DAVE's
// setup, which Discord can leave unanswered: a setup that stalls reaches
// Sentry, and the recorder starts it over on a fresh voice connection. And
// it watches the UDP socket, whose path can die while the voice gateway
// still answers: once the voice server's receiver reports have started, a
// socket that goes quiet drops the connection, and the recorder rejoins.

// receiveConn is a disgo voice connection as the receiver reads it.
type receiveConn interface {
	// datagrams is the connection's UDP connection.
	datagrams() datagramReader
	DAVE() godave.Session
	UserIDBySSRC(ssrc uint32) snowflake.ID
	// ChannelID is the call's channel, nil while there is none.
	ChannelID() *snowflake.ID
	// connection reports whether the recorder is connected in the call, at
	// the voice server Discord named last, when and why it last stopped
	// being connected, zero before it ever was, and how many times it has.
	connection() (connected bool, lost gapStart, losses int)
	// server is the voice server the recorder connects to: the one Discord
	// named last for the call.
	server() string
	// sessionOnline reports whether the recorder's own gateway session is
	// online, so that a rejoin could go out.
	sessionOnline() bool
	// reconnect opens a fresh voice connection in the call, which starts
	// DAVE over, and returns once it has opened, or with ctx's error.
	reconnect(ctx context.Context) error
	// recovery is the log of the recovery steps the connection takes.
	recovery() *recoveryLog
	// nothingCame drops the connection, as the heartbeat check does for an
	// overdue answer, since nothing has come from the voice server since
	// last: the connection is lost from then, and the recorder rejoins the
	// call. It returns at once, and does nothing to a connection that is
	// lost already.
	nothingCame(last time.Time)
}

// datagramReader is a UDP connection that tells of every datagram it reads,
// as the recorder's own does (readDatagram), and of when the last arrived.
type datagramReader interface {
	readDatagram() (datagram, error)
	lastDatagram() time.Time
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
	// silentAfter is how long the media watch waits with no datagram at all
	// before it counts the voice server gone quiet. The voice server sends a
	// receiver report every second, so a quiet channel never waits this
	// long.
	silentAfter = 3500 * time.Millisecond
)

// mediaNeverArmedMessage is the WARN a connection logs once when its media
// watch is still unarmed neverArmedAfter after it connected with DAVE ready
// on its socket. Discord may have stopped sending receiver reports, and the
// watch can't see a voice server gone quiet.
const mediaNeverArmedMessage = "Voice media watch never armed"

// neverArmedAfter is how long a connection with DAVE ready may wait for its
// socket's first receiver report before it warns. The voice server sends
// one every second.
const neverArmedAfter = 5 * time.Second

// stallMessage is what Sentry hears when DAVE stalls: the recorder hears
// nobody in the channel. daveRecoveredMessage is the Info line once it
// hears the channel again.
const (
	stallMessage         = "DAVE setup stalled"
	daveRecoveredMessage = "DAVE setup recovered"
)

// errDAVEStalled is the error a stall reaches Sentry with.
var errDAVEStalled = errors.New("voice: DAVE held every frame past the stall limit")

// gapStart is when and why the recorder stopped hearing the channel, with
// what the recorder knew of the cause then.
type gapStart struct {
	at    time.Time
	cause GapCause
	// close is the voice gateway close or failed read that started the
	// gap, nil when neither did, and closedBy the disgo gateway whose close
	// handler handed it over. server is the voice server the gateway last
	// opened at.
	close    error
	closedBy disgovoice.Gateway
	server   string
	// interval is the heartbeat interval the voice server's Hello named,
	// and sinceAnswer how long before the gap's start the server last
	// answered a heartbeat, for a heartbeat answer overdue.
	interval, sinceAnswer time.Duration
}

// fields are the cause's own fields of the gap that started as s, with
// namedLast the voice server Discord has named last by now, held how long
// DAVE has held frames, or held them last, and sinceDatagram how long ago
// the last datagram came. A call that moved moved to namedLast, unless it is
// still the one the call left.
func (s gapStart) fields(namedLast string, held, sinceDatagram time.Duration) []GapField {
	switch s.cause {
	case GapVoiceGatewayClosed:
		return []GapField{{Key: "closed_server", Value: s.server}}
	case GapHeartbeatOverdue:
		return []GapField{{Key: "heartbeat_interval", Value: s.interval}, {Key: "last_answer", Value: s.sinceAnswer}}
	case GapCallMoved:
		movedTo := namedLast
		if movedTo == s.server {
			movedTo = ""
		}
		return []GapField{{Key: "moved_from", Value: s.server}, {Key: "moved_to", Value: movedTo}}
	case GapDAVEStalled:
		return []GapField{{Key: "held_for", Value: held}}
	case GapVoiceServerSilent:
		return []GapField{{Key: "last_datagram", Value: sinceDatagram}}
	}
	return nil
}

// causeNotKnown is the cause of a lost connection until it is known, as a
// 4022's is once its channel lookup answers. A gapStart with it starts no
// gap, and neither does the zero gapStart of a connection still connected.
const causeNotKnown GapCause = ""

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
	// inGap is whether the listener was last told a gap started. told is
	// how many of the connection's losses the receiver has told of, each as
	// a gap or within one. Only the receiver's goroutine touches them.
	inGap bool
	told  int
	// media is the media watch. Only the receiver's goroutine touches it.
	media mediaWatch

	// mu guards what the gap report reads: gap, the start of the gap the
	// listener was last told of, gapLoss, the number of the connection's
	// loss that started it, zero for a DAVE stall, and heldSince and
	// heldUntil, when DAVE last started holding frames and stopped, zero
	// while it holds them.
	mu                   sync.Mutex
	gap                  gapStart
	gapLoss              int
	heldSince, heldUntil time.Time
}

// mediaWatch is the receiver's watch of what arrives on one UDP socket,
// which catches a voice server gone quiet while the voice gateway still
// answers. Each reconnect opens a new socket, numbered socket, and the
// watch starts over on it. armed is whether it watches: from the socket's
// first receiver report on. A sender report or a member's audio stops when
// members go quiet or leave, while a receiver report comes every second
// regardless. fired is whether it has found the voice server quiet.
//
// heard is when its clock started: the socket's last datagram, or when
// reading resumed after DAVE held frames, as what arrived meanwhile waits in
// the socket unread. ready is when the recorder was first connected on the
// socket with DAVE ready, zero before, and warned whether the watch has
// warned it never armed.
type mediaWatch struct {
	socket       int
	armed, fired bool
	heard        time.Time
	ready        time.Time
	warned       bool
}

// on starts the watch over unarmed when socket is a new one.
func (w *mediaWatch) on(socket int) {
	if socket != w.socket {
		*w = mediaWatch{socket: socket}
	}
}

// arrived notes a datagram, whatever it is, which starts the clock over. A
// receiver report arms the watch.
func (w *mediaWatch) arrived(d datagram) {
	w.heard = d.at
	if d.rtcp == rtcpReceiverReport {
		w.armed = true
	}
}

// resumed starts the clock over at now, once reading resumes.
func (w *mediaWatch) resumed(now time.Time) { w.heard = now }

// quiet reports whether the armed watch has heard nothing for silentAfter
// by now, once: the voice server has gone quiet.
func (w *mediaWatch) quiet(now time.Time) bool {
	if !w.armed || w.fired || now.Sub(w.heard) < silentAfter {
		return false
	}
	w.fired = true
	return true
}

// neverArmed reports, once, whether the watch is still unarmed
// neverArmedAfter after the recorder was first connected on the socket with
// DAVE ready. connected is whether it is now.
func (w *mediaWatch) neverArmed(now time.Time, connected bool) bool {
	if w.armed || w.warned || !connected {
		return false
	}
	if w.ready.IsZero() {
		w.ready = now
	}
	if now.Sub(w.ready) < neverArmedAfter {
		return false
	}
	w.warned = true
	return true
}

// newReceiver builds the receiver of a recorder's connection. It hands
// what it hears to l once open starts it. Losses a connection that is
// connected now had before are none of the recording's.
func newReceiver(conn receiveConn, l Listener, recorderID string) *receiver {
	ctx, cancel := context.WithCancel(context.Background())
	told := 0
	if connected, _, losses := conn.connection(); connected {
		told = losses
	}
	return &receiver{
		told:     told,
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
	connected, lost, losses := r.conn.connection()
	if losses != r.told && lost.cause != causeNotKnown {
		// A loss the receiver hasn't told of, which can be over already.
		r.told = losses
		r.startGap(lost, losses)
	}
	r.watchGap(connected && !holding)
	if holding {
		r.waitOrStartOver()
		return
	}
	r.endStall()
	r.release()
	d, err := r.conn.datagrams().readDatagram()
	r.media.on(d.socket)
	switch {
	case err == nil:
		r.arrived(d)
	case errors.Is(err, os.ErrDeadlineExceeded):
		// Nothing arrived. The next step looks at the held frames again.
		r.watchMedia()
	case errors.Is(err, net.ErrClosed), errors.Is(err, errUDPNotOpen):
		// A voice reconnect closes the socket, and disgo opens a new one.
		r.pause(closedPause)
	default:
		r.unreadablePacket(err)
	}
	r.watchArming(connected)
}

// arrived takes in a datagram the UDP connection read: the media watch
// counts it, whatever it is, and a voice packet in it is handed over.
func (r *receiver) arrived(d datagram) {
	r.media.arrived(d)
	switch {
	case d.err != nil:
		r.unreadablePacket(d.err)
	case d.packet != nil:
		r.unreadable = 0
		r.receive(d.packet)
	}
}

// unreadablePacket counts a datagram the UDP connection couldn't read.
// Sentry hears of a burst of them once.
func (r *receiver) unreadablePacket(err error) {
	r.unreadable++
	if r.unreadable == failureBurst {
		utils.CaptureError("Voice packets unreadable", err, "recorder", r.recorder, "packets", failureBurst)
	}
}

// watchArming warns once when the socket's media watch is still unarmed
// neverArmedAfter after the recorder was first connected on it with DAVE
// ready, as it is whenever the receiver reads.
func (r *receiver) watchArming(connected bool) {
	if r.media.neverArmed(time.Now(), connected) {
		utils.Warn(mediaNeverArmedMessage, "recorder", r.recorder, "channel_id", channelOf(r.conn), "voice_server", r.conn.server())
	}
}

// watchMedia looks at the media watch after a read that came back empty.
// Once it finds nothing at all from the voice server for silentAfter, it
// asks for a drop: the voice server has gone quiet. The gap starts at the
// last datagram, once the connection says it is lost. While another gap
// runs, the watch asks for nothing, so that gap keeps its cause.
func (r *receiver) watchMedia() {
	if !r.inGap && r.media.quiet(time.Now()) {
		r.conn.nothingCame(r.conn.datagrams().lastDatagram())
	}
}

// watchGap tells the listener when the recorder hears the channel again,
// after a gap: one each loss of the connection starts, which the receiver
// can learn of only later, or a DAVE stall.
func (r *receiver) watchGap(hearing bool) {
	if hearing && r.inGap {
		r.inGap = false
		r.listener.GapEnded()
	}
}

// startGap tells the listener a gap started, as start says, unless one is
// running already or start names no cause yet, as a dropped connection's
// can take a lookup to. The gap keeps that cause until it ends, and its
// report lists the recovery steps from its start on. loss is the number of
// the connection's loss start is, zero for none.
func (r *receiver) startGap(start gapStart, loss int) {
	if r.inGap || start.cause == causeNotKnown {
		return
	}
	r.inGap = true
	r.mu.Lock()
	r.gap, r.gapLoss = start, loss
	r.mu.Unlock()
	r.conn.recovery().from(start.at)
	r.listener.GapStarted(start.at, start.cause)
}

// report is what the recorder knows now of the gap the listener was last
// told of, the cause's own fields measured now, and the recovery steps since
// its start. A loss's close can reach the connection after the receiver told
// of its gap (closeObserved), so a gap whose loss is still the connection's
// latest reads it afresh.
func (r *receiver) report() GapReport {
	r.mu.Lock()
	gap, loss, held := r.gap, r.gapLoss, r.heldUntil.Sub(r.heldSince)
	if r.heldUntil.IsZero() {
		held = time.Since(r.heldSince)
	}
	r.mu.Unlock()
	sinceDatagram := time.Since(r.conn.datagrams().lastDatagram())
	if _, lost, losses := r.conn.connection(); loss != 0 && losses == loss {
		gap = lost
	}
	namedLast := r.conn.server()
	rep := GapReport{
		CloseCode:            closeCode(gap.close),
		VoiceServer:          namedLast,
		GatewaySessionOnline: r.conn.sessionOnline(),
		Fields:               gap.fields(namedLast, held, sinceDatagram),
	}
	rep.Steps, rep.EarlierSteps = r.conn.recovery().report(gap.at)
	if gap.cause == GapVoiceGatewayClosed {
		rep.Close = gap.close
	}
	return rep
}

// closeCodes names each close code a voice gateway closes with by its
// number: websocket's own and Discord's voice gateway codes. A gap's report
// picks a close's code out of it, as the error data check passes (ADR
// 0018).
var closeCodes = func() map[int]string {
	codes := make(map[int]string)
	for code := websocket.CloseNormalClosure; code <= websocket.CloseTLSHandshake; code++ {
		codes[code] = strconv.Itoa(code)
	}
	for code := range disgovoice.GatewayCloseEventCodes {
		codes[code] = strconv.Itoa(code)
	}
	return codes
}()

// closeCodeOther is the close code of a close whose code closeCodes
// doesn't name. The close's text still carries it.
const closeCodeOther = "other"

// closeCode is the close code of a gap's close: its code for a websocket
// close, CloseCodeNone for a failed read, and empty with no close.
func closeCode(close error) string {
	var closeErr *websocket.CloseError
	switch {
	case close == nil:
		return ""
	case !errors.As(close, &closeErr):
		return CloseCodeNone
	}
	if code, ok := closeCodes[closeErr.Code]; ok {
		return code
	}
	return closeCodeOther
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
// past stallAfter has stalled, and the recorder hears nobody: a gap starts
// from when DAVE started holding them, unless one is running already,
// Sentry hears of the stall once, and the recorder opens a fresh voice
// connection, whose session description starts DAVE over. That can take up
// to reconnectTimeout.
func (r *receiver) waitOrStartOver() {
	now := time.Now()
	if r.unheardSince.IsZero() {
		r.unheardSince, r.setupSince = now, now
		r.mu.Lock()
		r.heldSince, r.heldUntil = now, time.Time{}
		r.mu.Unlock()
	}
	if now.Sub(r.setupSince) <= stallAfter {
		r.pause(notReadyPause)
		return
	}
	if !r.stalled {
		r.startGap(gapStart{at: r.unheardSince, cause: GapDAVEStalled}, 0)
		r.stalled = true
		utils.CaptureError(stallMessage, errDAVEStalled, "recorder", r.recorder, "channel_id", channelOf(r.conn))
	}
	r.reconnect()
	r.setupSince = time.Now()
}

// endStall notes DAVE passing frames, which ends any stall, and starts the
// media watch's clock over after a hold. After a stall, the log says how
// long the recorder couldn't hear.
func (r *receiver) endStall() {
	if r.stalled {
		utils.Info(daveRecoveredMessage, "recorder", r.recorder, "channel_id", channelOf(r.conn),
			"unheard", time.Since(r.unheardSince))
	}
	if !r.unheardSince.IsZero() {
		now := time.Now()
		r.mu.Lock()
		r.heldUntil = now
		r.mu.Unlock()
		r.media.resumed(now)
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
