package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	disgovoice "github.com/disgoorg/disgo/voice"
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
// first connection it accepts, as Discord's does, then closes it with 4015
// once the recording listens. Its Hello carries a sequence number, so
// disgo's gateway would resume after the close rather than identify. A
// later connection it answers with Resumed when it resumes, and refuses
// with 4004 when it identifies. The first it refuses holds the token the
// recorder held, so the recorder leaves, joins again and identifies once
// more, and nothing dials after that one's 4004. dropped hears when it sent
// the 4015.
type droppingVoiceServer struct {
	*localVoiceServer
	udp       *fakeVoiceServer
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
	s := &droppingVoiceServer{udp: newFakeVoiceServer(t), listening: make(chan struct{}), dropped: make(chan time.Time, 1)}
	s.localVoiceServer = newLocalVoiceServer(t, func(n int32, conn *websocket.Conn) {
		defer s.locked(func() { s.played, s.changed = s.played+1, time.Now() })
		s.play(t, n, conn)
	})
	return s
}

// locked runs f holding the server's lock.
func (s *droppingVoiceServer) locked(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

// play plays the voice server for the nth connection.
func (s *droppingVoiceServer) play(t *testing.T, n int32, conn *websocket.Conn) {
	send := func(op disgovoice.Opcode, d disgovoice.GatewayMessageData, seq int) {
		if err := conn.WriteJSON(disgovoice.GatewayMessage{Op: op, D: d, Seq: seq}); err != nil {
			t.Errorf("voice server sending op %d: %v", op, err)
		}
	}
	closeWith := func(code int) {
		if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, "")); err != nil {
			t.Errorf("voice server sending %d: %v", code, err)
		}
	}
	s.locked(func() { s.greeted, s.changed = s.greeted+1, time.Now() })
	greeted := time.Now()
	send(disgovoice.OpcodeHello, disgovoice.GatewayMessageDataHello{HeartbeatInterval: float64(dropHeartbeat.Milliseconds())}, 1)
	op, ok := awaitOneOf(conn, disgovoice.OpcodeIdentify, disgovoice.OpcodeResume)
	if !ok {
		return
	}
	s.locked(func() { s.opened = append(s.opened, op) })
	time.Sleep(time.Until(greeted.Add(dropReadyAfter)))
	switch {
	case op == disgovoice.OpcodeResume:
		send(disgovoice.OpcodeResumed, disgovoice.GatewayMessageDataResumed{}, 0)
		return
	case n > 1:
		closeWith(disgovoice.GatewayCloseEventCodeAuthenticationFailed.Code)
		return
	}
	go s.udp.answerDiscovery(t)
	send(disgovoice.OpcodeReady, disgovoice.GatewayMessageDataReady{SSRC: 42, IP: "127.0.0.1", Port: s.udp.port(),
		Modes: []disgovoice.EncryptionMode{disgovoice.EncryptionModeAEADAES256GCMRTPSize}}, 0)
	awaitOp(t, conn, disgovoice.OpcodeSelectProtocol)
	send(disgovoice.OpcodeSessionDescription, disgovoice.GatewayMessageDataSessionDescription{
		Mode: disgovoice.EncryptionModeAEADAES256GCMRTPSize, SecretKey: make([]byte, 32)}, 0)
	select {
	case <-s.listening:
	case <-time.After(waitFor):
		t.Error("the recording never listened")
		return
	}
	closeWith(disgovoice.GatewayCloseEventCodeVoiceServerCrash.Code)
	s.dropped <- time.Now()
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
