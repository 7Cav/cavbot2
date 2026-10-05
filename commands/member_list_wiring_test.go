package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The wiring of the member list (#440): a session built by
// NewSessionTempVCManager, opened against a stand-in for Discord's gateway,
// hands discordgo's own events to the tracker and sends the member request
// through the session itself. The other member list tests call the
// adapter's handler directly. This one is what goes red if the handler is
// never wired, or never gets the raw RATE_LIMITED dispatch.

// gatewayFrame is one gateway payload: an opcode and its data.
type gatewayFrame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

// fakeGateway stands in for Discord's gateway websocket. On connect it
// follows the documented handshake: Hello, the bot's Identify, then a
// READY with the test guild unavailable. From then on it passes each frame
// the bot sends, heartbeats left out, to the test.
type fakeGateway struct {
	t      *testing.T
	srv    *httptest.Server
	conns  chan *websocket.Conn
	frames chan gatewayFrame
	conn   *websocket.Conn
	seq    int
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	g := &fakeGateway{t: t, conns: make(chan *websocket.Conn, 1), frames: make(chan gatewayFrame, 16)}
	var upgrader websocket.Upgrader
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("gateway upgrade: %v", err)
			return
		}
		if err := c.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 45000}}); err != nil {
			t.Errorf("gateway Hello: %v", err)
			return
		}
		g.conns <- c
		for {
			var f gatewayFrame
			if err := c.ReadJSON(&f); err != nil {
				return
			}
			if f.Op != 1 {
				g.frames <- f
			}
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// url is the gateway's address, as Discord's GET /gateway answers it.
func (g *fakeGateway) url() string {
	return "ws" + strings.TrimPrefix(g.srv.URL, "http")
}

// readDeadline bounds each wait for the bot. Shorter than the stall limit,
// so no stall re-ask could ever answer one.
const readDeadline = memberListStallLimit / 3

// next returns the next frame the bot sends, failing the test past the
// deadline.
func (g *fakeGateway) next() gatewayFrame {
	g.t.Helper()
	select {
	case f := <-g.frames:
		return f
	case <-time.After(readDeadline):
		g.t.Fatal("the bot sent the gateway nothing")
		return gatewayFrame{}
	}
}

// dispatch sends the bot one dispatch, as Discord numbers them.
func (g *fakeGateway) dispatch(event, data string) {
	g.t.Helper()
	if g.conn == nil {
		select {
		case g.conn = <-g.conns:
		case <-time.After(readDeadline):
			g.t.Fatal("the bot never connected to the gateway")
		}
	}
	g.seq++
	frame := map[string]any{"op": 0, "s": g.seq, "t": event, "d": json.RawMessage(data)}
	if err := g.conn.WriteJSON(frame); err != nil {
		g.t.Fatalf("gateway %s: %v", event, err)
	}
}

// memberRequestFrame is opcode 8's data. discordgo sends guild_id as a
// one-element list, Discord's docs a single ID; either names the guild.
type memberRequestFrame struct {
	GuildID json.RawMessage `json:"guild_id"`
	Query   *string         `json:"query"`
	Limit   int             `json:"limit"`
	Nonce   string          `json:"nonce"`
}

// names reports whether the request's guild_id is the guild, in either
// form.
func (r memberRequestFrame) names(guildID string) bool {
	var one string
	if json.Unmarshal(r.GuildID, &one) == nil {
		return one == guildID
	}
	var list []string
	return json.Unmarshal(r.GuildID, &list) == nil && len(list) == 1 && list[0] == guildID
}

// memberRequest reads the next frame as the bot's member request.
func (g *fakeGateway) memberRequest() memberRequestFrame {
	g.t.Helper()
	f := g.next()
	if f.Op != opRequestGuildMembers {
		g.t.Fatalf("the bot sent op %d %s, want a member request", f.Op, f.D)
	}
	var r memberRequestFrame
	if err := json.Unmarshal(f.D, &r); err != nil {
		g.t.Fatalf("member request %s: %v", f.D, err)
	}
	return r
}

// Regression pin: green on arrival, no code was written for it. The
// session asks the gateway itself for the whole list after the guild's
// GUILD_CREATE, and a RATE_LIMITED dispatch for that request, which
// discordgo hands only to handlers of the raw event, brings the next
// request once its wait passes.
func TestMemberListIsWiredToTheSessionsGateway(t *testing.T) {
	clock := installFakeClock(t)
	// The refusal's wait runs on the fake clock, so the test learns when
	// the handler has scheduled it, and runs it then.
	scheduled := make(chan struct{}, 16)
	onClock := tempVCAfterFunc
	tempVCAfterFunc = func(d time.Duration, f func()) func() {
		stop := onClock(d, f)
		scheduled <- struct{}{}
		return stop
	}
	t.Cleanup(func() { tempVCAfterFunc = onClock })

	gw := newFakeGateway(t)
	api := &fakeDiscordAPI{answer: func(r *http.Request, _ []byte) (int, []byte) {
		if strings.HasSuffix(r.URL.Path, "/gateway") {
			return http.StatusOK, []byte(`{"url":"` + gw.url() + `"}`)
		}
		t.Errorf("sent %s %s to Discord's API, want the gateway lookup alone", r.Method, r.URL.Path)
		return http.StatusInternalServerError, []byte(`{"message":"no request expected"}`)
	}}
	dg := stateSession(t, api)
	mgr := NewSessionTempVCManager(dg, testTempVCGuild)

	ready := make(chan error, 1)
	go func() { ready <- dg.Open() }()
	if f := gw.next(); f.Op != 2 {
		t.Fatalf("the bot's first frame is op %d, want Identify", f.Op)
	}
	gw.dispatch("READY", `{"v":10,"session_id":"session-1","user":{"id":"user-bot","username":"cavbot"},`+
		`"guilds":[{"id":"`+testTempVCGuild+`","unavailable":true}]}`)
	if err := <-ready; err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = dg.Close() })

	gw.dispatch("GUILD_CREATE", `{"id":"`+testTempVCGuild+`","members":[{"user":{"id":"user-bot","username":"cavbot"},"roles":[]}]}`)

	first := gw.memberRequest()
	if !first.names(testTempVCGuild) || first.Query == nil || *first.Query != "" || first.Limit != 0 || first.Nonce == "" {
		t.Fatalf("member request = guild %s, query %v, limit %d, nonce %q, want the whole list of %s with a nonce",
			first.GuildID, first.Query, first.Limit, first.Nonce, testTempVCGuild)
	}
	if snap := mgr.MemberList(testTempVCGuild); snap.Status != MemberListArriving || !snap.Connected {
		t.Errorf("after the request, snapshot = status %v, connected %v, want arriving and connected", snap.Status, snap.Connected)
	}
	// The GUILD_CREATE's late and stall waits are on the clock before the
	// request goes out.
	for range 2 {
		<-scheduled
	}

	gw.dispatch("RATE_LIMITED", `{"retry_after":0,"opcode":8,"meta":{"nonce":"`+first.Nonce+`","guild_id":"`+testTempVCGuild+`"}}`)

	select {
	case <-scheduled:
	case <-time.After(readDeadline):
		t.Fatal("the refusal scheduled no retry: the handler never got the raw RATE_LIMITED dispatch")
	}
	clock.advance(0)

	second := gw.memberRequest()
	if second.Nonce == "" || second.Nonce == first.Nonce {
		t.Errorf("the retry's nonce = %q, want one of its own, not %q", second.Nonce, first.Nonce)
	}
}
