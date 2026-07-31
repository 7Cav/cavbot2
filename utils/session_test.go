package utils

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

// helloFrame is the Op 10 packet discordgo requires as the first frame of the
// handshake; Open() rejects anything else outright.
const helloFrame = `{"op":10,"d":{"heartbeat_interval":45000},"s":null,"t":null}`

// readyFrame is a minimal READY carrying only what State.onReady needs to
// populate State.User. It is hand-built, not captured from a live gateway —
// so it pins discordgo's handling of a READY-shaped frame, not Discord's
// actual READY schema. If Discord ever relocates `user`, this fixture keeps
// passing while production breaks.
const readyFrame = `{"op":0,"s":1,"t":"READY","d":{"v":10,"session_id":"sess","user":{"id":"999","username":"cavbot","bot":true},"guilds":[]}}`

// invalidSessionFrame is Op 9, which Discord sends in response to IDENTIFY
// when it cannot start a session — an identify that is rate limited or over
// the session-start limit, or a resume it will not honour.
const invalidSessionFrame = `{"op":9,"d":false,"s":null,"t":null}`

// newFakeGateway stands up a server speaking enough of the Discord gateway
// protocol to get discordgo through Open(), and points discordgo at it for the
// duration of the test. secondFrame is written after the client's IDENTIFY —
// that is the frame Open() inspects for READY, and the one this bug turns on.
func newFakeGateway(t *testing.T, secondFrame string) {
	t.Helper()

	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Registered after srv.Close so it runs first (Cleanup is LIFO): the
	// handler must let go of the connection before the server will shut down.
	testOver := make(chan struct{})
	t.Cleanup(func() { close(testOver) })

	mux.HandleFunc("/gateway", func(w http.ResponseWriter, _ *http.Request) {
		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
		_ = json.NewEncoder(w).Encode(map[string]string{"url": wsURL})
	})

	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		if err := c.WriteMessage(websocket.TextMessage, []byte(helloFrame)); err != nil {
			return
		}
		if _, _, err := c.ReadMessage(); err != nil { // the client's IDENTIFY
			return
		}
		if err := c.WriteMessage(websocket.TextMessage, []byte(secondFrame)); err != nil {
			return
		}
		<-testOver
	})

	prev := discordgo.EndpointGateway
	discordgo.EndpointGateway = srv.URL + "/gateway"
	t.Cleanup(func() { discordgo.EndpointGateway = prev })
}

// newFakeSession returns a session pointed at the fake gateway.
func newFakeSession(t *testing.T) *discordgo.Session {
	t.Helper()

	dg, err := discordgo.New("Bot fake-token-for-test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	// Teardown hygiene only: without this, discordgo's listen goroutine spins
	// in reconnect() against the torn-down fake gateway for the rest of the run.
	dg.ShouldReconnectOnError = false
	t.Cleanup(func() { _ = dg.Close() })

	return dg
}

func TestOpenSessionReadySessionIsUsable(t *testing.T) {
	newFakeGateway(t, readyFrame)
	dg := newFakeSession(t)

	if err := OpenSession(dg); err != nil {
		t.Fatalf("OpenSession returned %v, want nil", err)
	}
	if dg.State.User == nil {
		t.Fatal("State.User is nil after OpenSession reported success")
	}
}

// TestOpenSessionRejectsSessionThatNeverBecameReady pins the invariant the
// whole bug turns on: a nil error must mean the session is usable. discordgo
// treats a non-READY second frame as non-fatal (wsapi.go:178-182) and Open()
// returns nil, leaving State.User unpopulated for the caller to walk into.
func TestOpenSessionRejectsSessionThatNeverBecameReady(t *testing.T) {
	newFakeGateway(t, invalidSessionFrame)
	dg := newFakeSession(t)

	err := OpenSession(dg)

	if err == nil && dg.State.User == nil {
		t.Fatal("OpenSession reported success but State.User is nil; the caller would panic dereferencing it")
	}
}
