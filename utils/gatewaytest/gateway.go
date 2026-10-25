// Package gatewaytest is a scripted Discord gateway: a fake gateway that
// answers each login of a real discordgo session as its test scripts it.
// Only tests import it.
package gatewaytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

// Latency is how long the gateway takes to answer a frame, as a network
// round trip would. discordgo hands Disconnect and READY to their handlers
// on goroutines of their own, and against Discord a login takes long enough
// that they arrive in the order they happen.
const Latency = 20 * time.Millisecond

// Hold is how long an answered login stays open before the gateway closes
// it.
const Hold = 200 * time.Millisecond

// UserID is the user ID the gateway's READY names.
const UserID = "999"

// Login is how the gateway answers one login.
type Login struct {
	// Refuse answers the upgrade with 503, so the session can't reach the
	// gateway.
	Refuse bool
	// Invalid answers the identify or resume with Op 9 Invalid Session
	// first. discordgo sends an identify in reply.
	Invalid bool
	// Answer answers the identify or resume: "READY", "RESUMED", or "" for
	// neither.
	Answer string
	// Close is the code the gateway closes the connection with, Hold after
	// its answer. 0 keeps the connection open.
	Close int
	// Heard, when set, runs when a connection kept open reads its first
	// frame after the answer: the session's first heartbeat.
	Heard func()
}

// Gateway is a fake Discord gateway whose test scripts its answer to each
// login.
type Gateway struct {
	mu      sync.Mutex
	script  func(login int) Login
	logins  int
	settled chan struct{}
}

// New stands up a gateway that answers each login with what script returns
// for its number, from 1, and points discordgo at it for the length of the
// test. Every upgrade request is a login, refused or not.
func New(t testing.TB, script func(login int) Login) *Gateway {
	t.Helper()
	g := &Gateway{script: script}

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
		if login.Refuse {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		g.run(c, login, testOver)
	})

	prev := discordgo.EndpointGateway
	discordgo.EndpointGateway = srv.URL + "/gateway"
	t.Cleanup(func() { discordgo.EndpointGateway = prev })
	return g
}

// login numbers a login and reads its answer from the script, or from
// Settle once the test has called it.
func (g *Gateway) login() Login {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logins++
	if settled := g.settled; settled != nil {
		g.settled = nil
		return Login{Answer: "RESUMED", Heard: func() { close(settled) }}
	}
	return g.script(g.logins)
}

// Logins is how many logins the gateway has seen.
func (g *Gateway) Logins() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.logins
}

// Settle answers the next login with RESUMED and keeps it open, and fails
// the test unless the session sends its first heartbeat on it within
// timeout. A session that keeps logging in ends its test connected, so
// closing it ends its reconnects. discordgo's heartbeat reconnects a
// session whose connection closed before its first heartbeat went out, so
// the close waits for that heartbeat.
func (g *Gateway) Settle(t testing.TB, timeout time.Duration) {
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
func (g *Gateway) run(c *websocket.Conn, login Login, testOver <-chan struct{}) {
	if c.WriteMessage(websocket.TextMessage, []byte(helloFrame)) != nil {
		return
	}
	if _, _, err := c.ReadMessage(); err != nil { // the identify or resume
		return
	}
	if login.Invalid {
		if !sendFrame(c, invalidSessionFrame) {
			return
		}
		if _, _, err := c.ReadMessage(); err != nil { // the identify that answers it
			return
		}
	}
	switch login.Answer {
	case "READY":
		if !sendFrame(c, paddedReadyFrame) || !sendFrame(c, paddedTypingFrame) {
			return
		}
	case "RESUMED":
		if !sendFrame(c, resumedFrame) {
			return
		}
	}
	if login.Close != 0 {
		time.Sleep(Hold)
		_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(login.Close, ""), time.Now().Add(time.Second))
		return
	}
	// Held open: read until the session closes the connection or the test
	// ends.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for heard := login.Heard; ; heard = nil {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
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
	time.Sleep(Latency)
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
	paddedReadyFrame  = `{"op":0,"s":1,"t":"READY","d":{"v":10,"session_id":"sess","user":{"id":"` + UserID + `","username":"cavbot","bot":true},"guilds":[],"padding":"` + strings.Repeat("\x7f", 70_000) + `"}}`
	paddedTypingFrame = `{"op":0,"s":2,"t":"TYPING_START","d":{"channel_id":"1","user_id":"2","padding":"` + strings.Repeat("\x7f", 1_000) + `"}}`
)
