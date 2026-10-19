package utils

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

// gatewayLatency is how long the scripted gateway takes to answer a frame,
// as a network round trip would. discordgo hands Disconnect and READY to
// their handlers on goroutines of their own, and against Discord a login
// takes long enough that they arrive in the order they happen.
const gatewayLatency = 20 * time.Millisecond

// gatewayHold is how long an answered login stays open before the scripted
// gateway closes it.
const gatewayHold = 200 * time.Millisecond

// gatewayLogin is how the scripted gateway answers one login.
type gatewayLogin struct {
	// refuse answers the upgrade with 503, so the session can't reach the
	// gateway.
	refuse bool
	// invalid answers the identify or resume with Op 9 Invalid Session
	// first. discordgo sends an identify in reply.
	invalid bool
	// answer answers the identify or resume: "READY", "RESUMED", or "" for
	// neither.
	answer string
	// close is the code the gateway closes the connection with, gatewayHold
	// after its answer. 0 keeps the connection open.
	close int
	// heard, when set, runs when a connection kept open reads its first
	// frame after the answer: the session's first heartbeat.
	heard func()
}

// scriptedGateway is a fake Discord gateway whose test scripts its answer
// to each login.
type scriptedGateway struct {
	mu      sync.Mutex
	script  func(login int) gatewayLogin
	logins  int
	settled chan struct{}
	// sent is what the session sent on each login that got the upgrade, in
	// the order they came.
	sent []*sessionSent
}

// sessionSent is what the session sent on one login.
type sessionSent struct {
	// at is when the login's upgrade came.
	at time.Time
	// op is the opcode of the login's first frame: 2 for an identify, 6
	// for a resume.
	op int
	// sessionID is the session a resume names.
	sessionID string
	// afterInvalid is the opcode of the frame the session sent in answer to
	// Op 9, on a login scripted invalid.
	afterInvalid int
	// beat is set once the session sends a frame on a held connection after
	// its answer: its first heartbeat.
	beat bool
	// closeCode is the code of the close frame the session sent on a held
	// connection, and closedAt when it came. Both stay zero while it sends
	// none.
	closeCode int
	closedAt  time.Time
}

// newScriptedGateway stands up a gateway that answers each login with what
// script returns for its number, from 1, and points discordgo at it for the
// length of the test. Every upgrade request is a login, refused or not.
func newScriptedGateway(t *testing.T, script func(login int) gatewayLogin) *scriptedGateway {
	t.Helper()
	g := &scriptedGateway{script: script}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// Registered after srv.Close so it runs first (Cleanup is LIFO): a held
	// connection must let go before the server shuts down.
	testOver := make(chan struct{})
	t.Cleanup(func() { close(testOver) })

	mux.HandleFunc("/gateway", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"})
	})
	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		login := g.login()
		if login.refuse {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		g.run(c, login, g.newSent(), testOver)
	})

	prev := discordgo.EndpointGateway
	discordgo.EndpointGateway = srv.URL + "/gateway"
	t.Cleanup(func() { discordgo.EndpointGateway = prev })
	return g
}

// login numbers a login and reads its answer from the script, or from
// settle once the test has called it.
func (g *scriptedGateway) login() gatewayLogin {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logins++
	if settled := g.settled; settled != nil {
		g.settled = nil
		return gatewayLogin{answer: "RESUMED", heard: func() { close(settled) }}
	}
	return g.script(g.logins)
}

// newSent starts the record of what the session sends on a login.
func (g *scriptedGateway) newSent() *sessionSent {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := &sessionSent{at: time.Now()}
	g.sent = append(g.sent, s)
	return s
}

// record updates s, a record newSent started, under the gateway's lock.
func (g *scriptedGateway) record(s *sessionSent, update func(*sessionSent)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	update(s)
}

// sentOn is what the session sent on the nth login that got the upgrade,
// from 1, and false before that login came.
func (g *scriptedGateway) sentOn(n int) (sessionSent, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n < 1 || n > len(g.sent) {
		return sessionSent{}, false
	}
	return *g.sent[n-1], true
}

// loginCount is how many logins the gateway has seen.
func (g *scriptedGateway) loginCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.logins
}

// settle answers the next login with RESUMED and keeps it open, and fails
// the test unless the session sends its first heartbeat on it within
// timeout. A session that keeps logging in ends its test connected, so
// closing it ends its reconnects. discordgo's heartbeat reconnects a
// session whose connection closed before its first heartbeat went out, so
// the close waits for that heartbeat.
func (g *scriptedGateway) settle(t *testing.T, timeout time.Duration) {
	t.Helper()
	settled := make(chan struct{})
	g.mu.Lock()
	g.settled = settled
	g.mu.Unlock()
	select {
	case <-settled:
	case <-time.After(timeout):
		t.Fatal("the session didn't log in again to settle")
	}
}

// run answers one login on c: Hello, then what login scripts, then the
// close or the hold.
func (g *scriptedGateway) run(c *websocket.Conn, login gatewayLogin, sent *sessionSent, testOver <-chan struct{}) {
	if c.WriteMessage(websocket.TextMessage, []byte(helloFrame)) != nil {
		return
	}
	_, first, err := c.ReadMessage() // the identify or resume
	if err != nil {
		return
	}
	var frame struct {
		Op int `json:"op"`
		D  struct {
			SessionID string `json:"session_id"`
		} `json:"d"`
	}
	if json.Unmarshal(first, &frame) == nil {
		g.record(sent, func(s *sessionSent) { s.op, s.sessionID = frame.Op, frame.D.SessionID })
	}
	if login.invalid {
		if !sendFrame(c, invalidSessionFrame) {
			return
		}
		_, answer, err := c.ReadMessage() // the identify that answers it
		if err != nil {
			return
		}
		var frame struct {
			Op int `json:"op"`
		}
		if json.Unmarshal(answer, &frame) == nil {
			g.record(sent, func(s *sessionSent) { s.afterInvalid = frame.Op })
		}
	}
	switch login.answer {
	case "READY":
		if !sendFrame(c, paddedReadyFrame) || !sendFrame(c, paddedTypingFrame) {
			return
		}
	case "RESUMED":
		if !sendFrame(c, resumedFrame) {
			return
		}
	}
	if login.close != 0 {
		time.Sleep(gatewayHold)
		_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(login.close, ""), time.Now().Add(time.Second))
		return
	}
	// Held open: read until the session closes the connection or the test
	// ends.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for heard := login.heard; ; heard = nil {
			if _, _, err := c.ReadMessage(); err != nil {
				var closeErr *websocket.CloseError
				if errors.As(err, &closeErr) {
					g.record(sent, func(s *sessionSent) { s.closeCode, s.closedAt = closeErr.Code, time.Now() })
				}
				return
			}
			g.record(sent, func(s *sessionSent) { s.beat = true })
			if heard != nil {
				heard()
			}
		}
	}()
	select {
	case <-done:
	case <-testOver:
	}
}

// sendFrame writes frame to c a round trip after what it answers.
func sendFrame(c *websocket.Conn, frame string) bool {
	time.Sleep(gatewayLatency)
	return c.WriteMessage(websocket.TextMessage, []byte(frame)) == nil
}

// helloFrame is the Op 10 packet discordgo requires as the first frame of
// the handshake; Open() rejects anything else outright. Its heartbeat
// interval outlasts any test, so discordgo sends one heartbeat and waits.
const helloFrame = `{"op":10,"d":{"heartbeat_interval":45000},"s":null,"t":null}`

// invalidSessionFrame is Op 9, which Discord sends in response to IDENTIFY
// when it cannot start a session — an identify that is rate limited or over
// the session-start limit, or a resume it will not honour, as for a token it
// has refused.
const invalidSessionFrame = `{"op":9,"d":false,"s":null,"t":null}`

const resumedFrame = `{"op":0,"s":2,"t":"RESUMED","d":{}}`

// paddedReadyFrame is a READY padded past 64 KiB, as a live one can run, and
// paddedTypingFrame an event padded past 125 bytes, so a session reads
// frames of every websocket length form before any close. The padding is a
// field discordgo ignores, of DEL bytes: a reader out of step with the
// frames reads a DEL as a header claiming a payload of exabytes, and never
// gets back in step to see the close.
//
// The READY carries only what State.onReady needs to populate State.User.
// It is hand-built, not captured from a live gateway, so it pins discordgo's
// handling of a READY-shaped frame, not Discord's actual READY schema.
var (
	paddedReadyFrame  = `{"op":0,"s":1,"t":"READY","d":{"v":10,"session_id":"sess","user":{"id":"999","username":"cavbot","bot":true},"guilds":[],"padding":"` + strings.Repeat("\x7f", 70_000) + `"}}`
	paddedTypingFrame = `{"op":0,"s":2,"t":"TYPING_START","d":{"channel_id":"1","user_id":"2","padding":"` + strings.Repeat("\x7f", 1_000) + `"}}`
)
