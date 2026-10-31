package voice

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/getsentry/sentry-go"
	"github.com/gorilla/websocket"
)

// A recorder's voice connection dropping mid-recording, on disgo's own voice
// gateway over real sockets: a voice server on localhost the test scripts,
// and a UDP socket that answers IP discovery.
//
// disgo's gateway starts a heartbeat goroutine at Hello, which writes fields
// a close and a reconnect read and write with no lock between them. Under
// -race, only a heartbeat that went out tells the race detector the two are
// ordered, and one that went out successfully writes a field disgo's own
// reconnect writes again. So the voice server here holds its Ready until
// disgo's first heartbeat has tried to go out and been refused for want of
// one, and ends each connection before the second.

// dropHeartbeat is the heartbeat interval the voice server names, and
// dropReadyAfter how long after its Hello it sends Ready: past the first
// heartbeat, and short of the second by more than a connection takes.
const (
	dropHeartbeat  = 500 * time.Millisecond
	dropReadyAfter = 3 * dropHeartbeat / 2
)

// droppingVoiceServer is a voice server that connects the recorder on the
// first connection it accepts, as Discord's does, then ends it as end says
// once the recording listens, by default closing it with 4015. Its Hello
// carries a sequence number, so disgo's gateway would resume after a 4015
// rather than identify. A later connection it answers with Resumed when it
// resumes, and refuses with 4004 when it identifies. The first it refuses
// holds the token the recorder held, so the recorder leaves, joins again
// and identifies once more, and nothing dials after that one's 4004. A
// later connection plays as later says instead, when it is set. dropped
// hears when it ended the first connection.
type droppingVoiceServer struct {
	*localVoiceServer
	udp       *fakeVoiceServer
	end       connEnd
	later     laterConn
	listening chan struct{}
	dropped   chan time.Time

	mu sync.Mutex
	// opened is the first op each connection sent after Hello, in the
	// order the connections arrived. played counts the connections whose
	// script has ended, and greeted those given a Hello. changed is when
	// either count last went up.
	opened          []disgovoice.Opcode
	greeted, played int
	changed         time.Time
}

func newDroppingVoiceServer(t *testing.T) *droppingVoiceServer {
	t.Helper()
	return newDroppingVoiceServerEnding(t, closingWith(disgovoice.GatewayCloseEventCodeVoiceServerCrash.Code))
}

// newDroppingVoiceServerEnding is a dropping voice server that ends the
// first connection as end does.
func newDroppingVoiceServerEnding(t *testing.T, end connEnd) *droppingVoiceServer {
	t.Helper()
	return newDroppingVoiceServerPlaying(t, end, nil)
}

// newDroppingVoiceServerPlaying is a dropping voice server that ends the
// first connection as end does, and plays each later one as later does.
func newDroppingVoiceServerPlaying(t *testing.T, end connEnd, later laterConn) *droppingVoiceServer {
	t.Helper()
	s := &droppingVoiceServer{udp: newFakeVoiceServer(t), end: end, later: later, listening: make(chan struct{}), dropped: make(chan time.Time, 1)}
	s.localVoiceServer = newLocalVoiceServer(t, func(n int32, conn *websocket.Conn) {
		defer s.locked(func() { s.played, s.changed = s.played+1, time.Now() })
		s.play(t, n, conn)
	})
	return s
}

// connEnd is how a voice server ends a connection.
type connEnd func(t *testing.T, conn *websocket.Conn)

// laterConn is how a dropping voice server s plays the nth connection it
// accepts, after the first.
type laterConn func(s *droppingVoiceServer, t *testing.T, n int32, conn *websocket.Conn)

// closingWith ends a connection with a close carrying code.
func closingWith(code int) connEnd {
	return func(t *testing.T, conn *websocket.Conn) {
		if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, "")); err != nil {
			t.Errorf("voice server sending %d: %v", code, err)
		}
	}
}

// resetting ends a connection with a TCP reset and no close, so the
// recorder's next read of it fails.
func resetting(t *testing.T, conn *websocket.Conn) {
	var tcp *net.TCPConn
	if tlsConn, ok := conn.NetConn().(*tls.Conn); ok {
		tcp, _ = tlsConn.NetConn().(*net.TCPConn)
	}
	if tcp == nil {
		t.Errorf("voice server's connection = %T, want TCP under TLS", conn.NetConn())
		return
	}
	if err := tcp.SetLinger(0); err != nil {
		t.Errorf("voice server setting no linger: %v", err)
	}
	if err := tcp.Close(); err != nil {
		t.Errorf("voice server resetting its connection: %v", err)
	}
}

// locked runs f holding the server's lock.
func (s *droppingVoiceServer) locked(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

// play plays the voice server for the nth connection.
func (s *droppingVoiceServer) play(t *testing.T, n int32, conn *websocket.Conn) {
	if n > 1 && s.later != nil {
		s.later(s, t, n, conn)
		return
	}
	op, ok := s.greet(t, conn)
	if !ok {
		return
	}
	switch {
	case op == disgovoice.OpcodeResume:
		sendOp(t, conn, disgovoice.OpcodeResumed, disgovoice.GatewayMessageDataResumed{}, 0)
		return
	case n > 1:
		closingWith(disgovoice.GatewayCloseEventCodeAuthenticationFailed.Code)(t, conn)
		return
	}
	ready(t, conn, s.udp)
	sendOp(t, conn, disgovoice.OpcodeSessionDescription, disgovoice.GatewayMessageDataSessionDescription{
		Mode: disgovoice.EncryptionModeAEADAES256GCMRTPSize, SecretKey: make([]byte, 32)}, 0)
	select {
	case <-s.listening:
	case <-time.After(waitFor):
		t.Error("the recording never listened")
		return
	}
	s.end(t, conn)
	s.dropped <- time.Now()
}

// greet sends conn the server's Hello, reads the recorder's identify or
// resume, and returns its op once Ready is due. false when the connection
// ended first.
func (s *droppingVoiceServer) greet(t *testing.T, conn *websocket.Conn) (disgovoice.Opcode, bool) {
	s.locked(func() { s.greeted, s.changed = s.greeted+1, time.Now() })
	greeted := time.Now()
	sendOp(t, conn, disgovoice.OpcodeHello, disgovoice.GatewayMessageDataHello{HeartbeatInterval: float64(dropHeartbeat.Milliseconds())}, 1)
	op, ok := awaitOneOf(conn, disgovoice.OpcodeIdentify, disgovoice.OpcodeResume)
	if !ok {
		return 0, false
	}
	s.locked(func() { s.opened = append(s.opened, op) })
	time.Sleep(time.Until(greeted.Add(dropReadyAfter)))
	return op, true
}

// ready sends conn the server's Ready, naming udp as its UDP socket, which
// answers the recorder's IP discovery, and waits for the recorder's Select
// Protocol.
func ready(t *testing.T, conn *websocket.Conn, udp *fakeVoiceServer) {
	go udp.answerDiscovery(t)
	sendOp(t, conn, disgovoice.OpcodeReady, disgovoice.GatewayMessageDataReady{SSRC: 42, IP: "127.0.0.1", Port: udp.port(),
		Modes: []disgovoice.EncryptionMode{disgovoice.EncryptionModeAEADAES256GCMRTPSize}}, 0)
	awaitOp(t, conn, disgovoice.OpcodeSelectProtocol)
}

// sendOp sends a voice gateway message on conn.
func sendOp(t *testing.T, conn *websocket.Conn, op disgovoice.Opcode, d disgovoice.GatewayMessageData, seq int) {
	if err := conn.WriteJSON(disgovoice.GatewayMessage{Op: op, D: d, Seq: seq}); err != nil {
		t.Errorf("voice server sending op %d: %v", op, err)
	}
}

// firstOps is the first op each connection sent after Hello.
func (s *droppingVoiceServer) firstOps() []disgovoice.Opcode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.opened)
}

// settled reports whether every connection given a Hello has played out,
// and none has come for longer than one takes to dial after the last.
func (s *droppingVoiceServer) settled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.played >= s.greeted && time.Since(s.changed) > dropReadyAfter
}

// awaitOneOf reads what the recorder sends on conn until a message with one
// of ops, and returns its op. false when the connection ended first.
func awaitOneOf(conn *websocket.Conn, ops ...disgovoice.Opcode) (disgovoice.Opcode, bool) {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return 0, false
		}
		var m struct {
			Op disgovoice.Opcode `json:"op"`
		}
		if mt == websocket.TextMessage && json.Unmarshal(data, &m) == nil && slices.Contains(ops, m.Op) {
			return m.Op, true
		}
	}
}

// dialerRefusingAfterFirst is the server's dialer, which refuses every dial
// after the first, as a voice server that went down does.
func (s *droppingVoiceServer) dialerRefusingAfterFirst() *websocket.Dialer {
	d := s.dialer()
	var dials atomic.Int32
	d.NetDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) > 1 {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		var nd net.Dialer
		return nd.DialContext(ctx, network, addr)
	}
	return d
}

// recordDropping starts a recording through disgo's own voice gateway at
// server, dialing through dialer, and returns its connection and what its
// listener hears. The voice state updates sent count from then. The
// recording ends with the test, once the server has settled.
func (h *callHarness) recordDropping(server *droppingVoiceServer, dialer *websocket.Dialer) (Conn, *listener) {
	h.t.Helper()
	h.serving = server.endpoint()
	h.useDisgoGateway(dialer)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	conn, err := h.s.Join(ctx, callGuild.String(), callChannel.String())
	if err != nil {
		h.t.Fatalf("Join: %v", err)
	}
	h.t.Cleanup(func() {
		within(time.Now(), 2*waitFor, server.settled)
		// The status takes the lock disgo's refused heartbeat took, which
		// orders the close after it for the race detector.
		_ = conn.(*disgoConn).conn.Gateway().Status()
		conn.Leave(context.Background())
	})
	l := listen(nil)
	conn.Receive(l)
	h.countFromNow()
	close(server.listening)
	return conn, l
}

// awaitDrop waits for the server to close the first connection, and returns
// when it did.
func (s *droppingVoiceServer) awaitDrop(t *testing.T) time.Time {
	t.Helper()
	select {
	case at := <-s.dropped:
		return at
	case <-time.After(waitFor):
		t.Fatal("the voice server never closed the recorder's connection")
		return time.Time{}
	}
}

// within waits until cond holds or d has passed since from, and reports
// whether it held.
func within(from time.Time, d time.Duration, cond func() bool) bool {
	for !cond() {
		if time.Since(from) > d {
			return cond()
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// rejoined reports whether the recorder has asked to join the call's
// channel since the voice state updates last counted from.
func (h *callHarness) rejoined() bool {
	join := fmt.Sprintf("join %d mute=true deaf=false", callChannel)
	return slices.ContainsFunc(h.sent(), func(u voiceUpdate) bool { return u.String() == join })
}

// A voice server can close the recorder's voice connection with a code
// disgo resumes from, such as 4015. The recorder can't hear the call from
// then on, so the recording's gap starts within 1 s, whether the server
// then refuses every connection or would take a resume. disgo would try to
// resume for 15 s before telling the recorder. A server that refuses every
// connection never answers again, and the recorder's rejoin goes out
// within 1 s all the same. disgo hands the recorder that close as its own
// refused resume, and the gap's report gives the close's code all the
// same.
func TestRecorderStartsAGapWithin1sOfACloseDisgoResumesFrom(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuses bool
	}{
		{"server refuses connections", true},
		{"server would take a resume", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordSentry(t)
			server := newDroppingVoiceServer(t)
			dialer := server.dialer()
			if tc.refuses {
				dialer = server.dialerRefusingAfterFirst()
			}
			h := newCallHarness(t)
			conn, l := h.recordDropping(server, dialer)

			dropped := server.awaitDrop(t)

			if !within(dropped, time.Second, func() bool { return len(l.gapStarts()) > 0 }) {
				t.Errorf("gap events 1 s after the 4015 = %v, want a gap started", l.gapEvents())
			}
			if got, want := conn.GapReport().CloseCode, "4015"; got != want {
				t.Errorf("gap report's close code = %q, want %q", got, want)
			}
			if tc.refuses && !within(dropped, time.Second, h.rejoined) {
				t.Errorf("voice state updates 1 s after the 4015 = %v, want the recorder's rejoin", h.sent())
			}
		})
	}
}

// Discord can take the recorder out of the channel while its rejoin after a
// drop is still trying, here at a voice server that refuses every dial. The
// recorder is out, and Sentry hears of the lost connection once, with the
// close that started the drop, not the resume the recorder refused.
func TestRecorderReportsTheCloseBehindAConnectionItCouldNotRejoin(t *testing.T) {
	crash := disgovoice.GatewayCloseEventCodeVoiceServerCrash.Code
	for _, tc := range []struct {
		name string
		end  connEnd
		want string
	}{
		{"4015", closingWith(crash), (&websocket.CloseError{Code: crash}).Error()},
		{"a failed read", resetting, syscall.ECONNRESET.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := recordSentry(t)
			server := newDroppingVoiceServerEnding(t, tc.end)
			h := newCallHarness(t)
			h.recordDropping(server, server.dialerRefusingAfterFirst())

			dropped := server.awaitDrop(t)
			if !within(dropped, waitFor, h.rejoined) {
				t.Fatalf("voice state updates after the drop = %v, want the recorder's rejoin", h.sent())
			}
			h.leftChannel()

			got := awaitLostConnection(t, events)
			if len(got) != 1 {
				t.Fatalf("Sentry got %d %q events, want 1", len(got), lostConnectionMessage)
			}
			values := exceptionValues(got[0])
			if !slices.Contains(values, tc.want) {
				t.Errorf("event's error = %q, want %q in it", values, tc.want)
			}
			if slices.ContainsFunc(values, func(v string) bool { return strings.Contains(v, errResumeRefused.Error()) }) {
				t.Errorf("event's error = %q, want no refused resume in it", values)
			}
		})
	}
}

// A voice server can close a recording's connection with a code disgo
// neither resumes from nor answers with a fresh connection, such as 4014
// when a moderator disconnects the recorder. disgo takes the recorder out
// itself, and Sentry hears of the lost connection once, with that close.
func TestRecorderReportsACloseDisgoTakesItOutFor(t *testing.T) {
	events := recordSentry(t)
	disconnected := disgovoice.GatewayCloseEventCodeDisconnected.Code
	server := newDroppingVoiceServerEnding(t, closingWith(disconnected))
	h := newCallHarness(t)
	h.recordDropping(server, server.dialer())

	server.awaitDrop(t)

	got := awaitLostConnection(t, events)
	if len(got) != 1 {
		t.Fatalf("Sentry got %d %q events, want 1", len(got), lostConnectionMessage)
	}
	want := (&websocket.CloseError{Code: disconnected}).Error()
	if values := exceptionValues(got[0]); !slices.Contains(values, want) {
		t.Errorf("event's error = %q, want %q in it", values, want)
	}
}

// A stop's leave can hang before it starts, as the smoke hook holds it, and
// the recorder then logs out for its relogin. Discord answers the logout by
// closing the recording's voice gateway with 4014, which disgo takes the
// recorder out for. The stop had ended the recording, so Sentry hears of no
// connection lost mid-recording.
func TestRecorderReportsNoLostConnectionForACloseItsLogoutCaused(t *testing.T) {
	events := recordSentry(t)
	loggedOut := make(chan struct{})
	disconnected := disgovoice.GatewayCloseEventCodeDisconnected.Code
	server := newDroppingVoiceServerEnding(t, func(t *testing.T, conn *websocket.Conn) {
		select {
		case <-loggedOut:
		case <-time.After(waitFor):
			t.Error("the recorder never logged out")
		}
		closingWith(disconnected)(t, conn)
	})
	h := newCallHarness(t)
	h.s.watch = utils.NewGatewayWatch(time.Hour)
	conn, _ := h.recordDropping(server, server.dialer())

	hangFile := filepath.Join(t.TempDir(), "leave-hang")
	if err := os.WriteFile(hangFile, nil, 0o600); err != nil {
		t.Fatalf("writing the leave hang file: %v", err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		(&hangingLeaveConn{Conn: conn, file: hangFile}).Leave(context.Background())
	}()
	t.Cleanup(func() {
		_ = os.Remove(hangFile)
		<-stopped
	})
	if err := h.s.LogOut(); err != nil {
		t.Fatalf("LogOut: %v", err)
	}
	close(loggedOut)
	server.awaitDrop(t)

	// The stop's leave is still held, so any leave now is disgo taking the
	// recorder out. A report of a lost connection would follow it at once.
	disgoLeft := func() bool { return slices.ContainsFunc(h.sent(), voiceUpdate.leaves) }
	if !within(time.Now(), waitFor, disgoLeft) {
		t.Fatalf("voice state updates after the 4014 = %v, want disgo's leave", h.sent())
	}
	time.Sleep(lostQuiet)
	if got := tagged(events.Events(), lostConnectionMessage); len(got) != 0 {
		t.Errorf("Sentry got %d %q events, want none", len(got), lostConnectionMessage)
	}
}

// lostQuiet is how long after Sentry first hears of a lost connection a test
// waits for any more such events.
const lostQuiet = 500 * time.Millisecond

// awaitLostConnection waits for Sentry to hear of a connection lost
// mid-recording, then lostQuiet more, and returns every such event it has
// by then.
func awaitLostConnection(t *testing.T, events *sentryTransport) []*sentry.Event {
	t.Helper()
	heard := func() bool { return len(tagged(events.Events(), lostConnectionMessage)) > 0 }
	if !within(time.Now(), waitFor, heard) {
		t.Fatalf("Sentry events = %v, want a %q", eventMessages(events.Events()), lostConnectionMessage)
	}
	time.Sleep(lostQuiet)
	return tagged(events.Events(), lostConnectionMessage)
}

// exceptionValues is the text of each error a Sentry event carries: the
// captured one and each it wraps.
func exceptionValues(e *sentry.Event) []string {
	out := make([]string, 0, len(e.Exception))
	for _, x := range e.Exception {
		out = append(out, x.Value)
	}
	return out
}

// After a 4015 the recorder's next open at a voice server identifies, and no
// Resume reaches any voice server: the gateway the close left behind dials
// nothing more. A resume gets no answer that opens audio.
func TestRecorderIdentifiesAfterACloseDisgoResumesFrom(t *testing.T) {
	recordSentry(t)
	server := newDroppingVoiceServer(t)
	h := newCallHarness(t)
	h.recordDropping(server, server.dialer())

	server.awaitDrop(t)
	within(time.Now(), waitFor, func() bool { return len(server.firstOps()) > 1 })

	ops := server.firstOps()
	if len(ops) < 2 || ops[1] != disgovoice.OpcodeIdentify {
		t.Errorf("first ops after Hello = %v, want the second connection to identify (op %d)", ops, disgovoice.OpcodeIdentify)
	}
	if slices.Contains(ops, disgovoice.OpcodeResume) {
		t.Errorf("first ops after Hello = %v, want no Resume (op %d)", ops, disgovoice.OpcodeResume)
	}
}

// After a drop, the voice server can refuse the token the recorder held by
// closing its identify with 4004 before its Ready, as this one does. disgo's
// gateway hands that close back from its open, and the recorder leaves the
// channel, then joins it again.
func TestRecorderLeavesAndJoinsAgainWhenAVoiceServerRefusesItsHeldToken(t *testing.T) {
	recordSentry(t)
	server := newDroppingVoiceServer(t)
	h := newCallHarness(t)
	h.recordDropping(server, server.dialer())

	dropped := server.awaitDrop(t)

	if !within(dropped, waitFor, h.leftAndJoinedAgain) {
		t.Errorf("voice state updates after the 4015 = %v, want a leave and a join after it", h.sent())
	}
}

// A voice server can close the recorder's dial after its Ready, before the
// session description that would complete the connection, as this one does
// on the rejoin's dial after a drop. The gap's report lists that dial with
// the close's code, whether disgo would resume from the close, as from
// 4015, or hands it straight over, as 4006. The drop itself is a failed
// read with no code, so the dial's code can only be its own close's.
func TestRecorderReportsTheCloseOfADialItsVoiceServerClosedAfterReady(t *testing.T) {
	for _, code := range []int{
		disgovoice.GatewayCloseEventCodeVoiceServerCrash.Code,
		disgovoice.GatewayCloseEventCodeSessionNoLongerValid.Code,
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			recordSentry(t)
			udp := newFakeVoiceServer(t)
			server := newDroppingVoiceServerPlaying(t, resetting, func(s *droppingVoiceServer, t *testing.T, n int32, conn *websocket.Conn) {
				if n > 2 {
					return
				}
				if _, ok := s.greet(t, conn); !ok {
					return
				}
				ready(t, conn, udp)
				closingWith(code)(t, conn)
			})
			h := newCallHarness(t)
			conn, _ := h.recordDropping(server, server.dialer())

			dropped := server.awaitDrop(t)

			want := RecoveryStep{Kind: StepDial, Token: TokenHeld, Outcome: StepFailed, CloseCode: strconv.Itoa(code)}
			hasDial := func() bool {
				return slices.ContainsFunc(conn.GapReport().Steps, func(s RecoveryStep) bool { return sameStep(s, want) })
			}
			if !within(dropped, waitFor, hasDial) {
				t.Errorf("recovery steps = %+v, want one %+v", conn.GapReport().Steps, want)
			}
		})
	}
}

// voicePeer is a voice server's UDP socket on localhost. It answers every
// IP discovery request, as a voice server answers each socket the recorder
// opens, and sends what a test asks to the socket that asked last.
type voicePeer struct {
	*fakeVoiceServer

	mu      sync.Mutex
	client  net.Addr
	counter uint32
}

func newVoicePeer(t *testing.T) *voicePeer {
	t.Helper()
	p := &voicePeer{fakeVoiceServer: newFakeVoiceServer(t)}
	go p.answerDiscoveries()
	return p
}

// answerDiscoveries answers each IP discovery request until the socket
// closes, and notes who asked. Anything else, such as the recorder's
// keepalive, it reads and drops.
func (p *voicePeer) answerDiscoveries() {
	buf := make([]byte, 1500)
	for {
		n, from, err := p.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if n != discoverySize || binary.BigEndian.Uint16(buf[0:2]) != 1 {
			continue
		}
		p.mu.Lock()
		p.client = from
		p.mu.Unlock()
		_, _ = p.conn.WriteTo(discoveryAnswer(buf[:n], from), from)
	}
}

// sendTo sends b to the socket that asked for IP discovery last.
func (p *voicePeer) sendTo(t *testing.T, b []byte) {
	t.Helper()
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()
	if client == nil {
		t.Fatal("no socket has asked the voice server for IP discovery")
	}
	if _, err := p.conn.WriteTo(b, client); err != nil {
		t.Fatalf("voice server sending a datagram: %v", err)
	}
}

// sendReport sends an RTCP datagram of rtcpType, such as the receiver
// report the voice server sends every second, its body left out.
func (p *voicePeer) sendReport(t *testing.T, rtcpType byte) {
	t.Helper()
	p.sendTo(t, []byte{0x80, rtcpType, 0x00, 0x01, 0x00, 0x00, 0x00, 0x2A})
}

// sendFrame sends a voice packet on ssrc carrying payload, encrypted with
// the all-zero key the scripted gateway's session description names.
func (p *voicePeer) sendFrame(t *testing.T, ssrc uint32, payload []byte) {
	t.Helper()
	p.mu.Lock()
	p.counter++
	counter := p.counter
	p.mu.Unlock()
	p.sendTo(t, serverPacket(t, make([]byte, 32), ssrc, counter*960, counter, payload))
}

// answerHeartbeats has the voice server behind gw answer a heartbeat every
// interval until the test ends, as it does while its voice gateway is up.
func answerHeartbeats(t *testing.T, gw *answeringGateway, interval time.Duration) {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				gw.answerHeartbeat()
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
}

// A voice server can stop sending anything on the recorder's UDP socket, as
// a NAT or a media node dropping the path does, while its voice gateway
// still answers every heartbeat. Once the server's receiver report has armed
// the media watch, the recorder notices, starts a gap named for the quiet
// voice server and rejoins the call. A frame the server sends on the
// rejoined connection reaches the recording, and the gap ends.
func TestRecorderRejoinsWhenNothingComesFromItsVoiceServer(t *testing.T) {
	recordSentry(t)
	peer := newVoicePeer(t)
	h := newCallHarness(t)
	h.transportOnly.Store(true)
	h.peer = peer
	frames := newHeard()
	l := listen(frames.handle)
	conn := h.record(l)
	defer conn.Leave(context.Background())
	h.connected.hello(time.Second)
	answerHeartbeats(t, h.connected, time.Second)

	peer.sendReport(t, rtcpReceiverReport)
	quiet := time.Now()

	if !within(quiet, silentAfter+waitFor, func() bool { return len(l.gapStarts()) > 0 }) {
		t.Fatalf("gap events after the voice server went quiet = %v, want a gap started", l.gapEvents())
	}
	if got := l.gapCauses(); got[0] != GapVoiceServerSilent {
		t.Errorf("gap cause = %s, want %s", got[0], GapVoiceServerSilent)
	}
	h.waitOpen().gateway.speaking()
	peer.sendFrame(t, callSpeakerSSRC, []byte("back"))
	if f := frames.next(t); f.UserID != callSpeaker.String() || string(f.Opus) != "back" {
		t.Errorf("frame handed over after the rejoin = %+v (%q), want %q from %s", f, f.Opus, "back", callSpeaker)
	}
	if !within(time.Now(), waitFor, func() bool { return slices.Equal(l.gapEvents(), []string{gapStarted, gapEnded}) }) {
		t.Errorf("gap events after the rejoin = %v, want the gap started and ended", l.gapEvents())
	}
}

// A voice gateway can go quiet, heartbeat answers and all, while datagrams
// still arrive on the recorder's UDP socket, and the recorder hears the call
// until the last of them. The heartbeat check finds the answer overdue
// later, and the gap starts at the last datagram, not at the last websocket
// message. Here the datagrams stop 1.5 s after the websocket goes quiet.
// They are sender reports, which leave the media watch unarmed.
func TestRecorderDatesAHeartbeatGapAtItsLastDatagram(t *testing.T) {
	recordSentry(t)
	peer := newVoicePeer(t)
	h := newCallHarness(t)
	h.transportOnly.Store(true)
	h.peer = peer
	l := listen(nil)
	conn := h.record(l)
	defer conn.Leave(context.Background())
	const interval = 500 * time.Millisecond
	h.connected.hello(interval)
	h.connected.answerHeartbeat()
	quiet := time.Now()

	var last time.Time
	for time.Since(quiet) < 1500*time.Millisecond {
		peer.sendReport(t, rtcpSenderReport)
		last = time.Now()
		time.Sleep(20 * time.Millisecond)
	}

	if !within(quiet, interval+heartbeatMargin+waitFor, func() bool { return len(l.gapStarts()) > 0 }) {
		t.Fatalf("gap events after the voice gateway went quiet = %v, want a gap started", l.gapEvents())
	}
	if got := l.gapCauses(); got[0] != GapHeartbeatOverdue {
		t.Errorf("gap cause = %s, want %s", got[0], GapHeartbeatOverdue)
	}
	if d := l.gapStarts()[0].Sub(last); d.Abs() > 250*time.Millisecond {
		t.Errorf("gap started %v after the last datagram, want at it", d)
	}
}
