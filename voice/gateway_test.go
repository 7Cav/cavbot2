package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/getsentry/sentry-go"
	"github.com/gorilla/websocket"
	"github.com/thomas-vilte/dave-go/session"
)

// The production recorder session, over a fake disgo voice manager. Only
// OpenGateway needs Discord itself, and the smoke test covers it.

// fakeManager stands in for disgo's voice manager: it records the events
// handed to it and whether it was closed, and creates conn for every join.
// The methods it doesn't override belong to the nil Manager it embeds, so a
// call to one fails the test.
type fakeManager struct {
	disgovoice.Manager
	states  []gateway.EventVoiceStateUpdate
	servers []gateway.EventVoiceServerUpdate
	conn    *fakeDisgoConn
	closed  bool
}

func (m *fakeManager) HandleVoiceStateUpdate(e gateway.EventVoiceStateUpdate) {
	m.states = append(m.states, e)
}

func (m *fakeManager) HandleVoiceServerUpdate(e gateway.EventVoiceServerUpdate) {
	m.servers = append(m.servers, e)
}

func (m *fakeManager) CreateConn(snowflake.ID) disgovoice.Conn { return m.conn }
func (m *fakeManager) Close(context.Context)                   { m.closed = true }

// fakeDisgoConn stands in for one disgo voice connection. openErr, when set,
// is what Open returns: Discord not completing the join in time.
type fakeDisgoConn struct {
	disgovoice.Conn
	openErr  error
	opened   bool
	channel  snowflake.ID
	selfDeaf bool
	closed   bool
}

func (c *fakeDisgoConn) Open(_ context.Context, channelID snowflake.ID, _, selfDeaf bool) error {
	c.opened, c.channel, c.selfDeaf = true, channelID, selfDeaf
	return c.openErr
}

func (c *fakeDisgoConn) Close(context.Context) { c.closed = true }

// recorderSession is the production session over a fake manager, its
// discordgo session built and never opened.
func recorderSession(t *testing.T, m *fakeManager) *discordgoSession {
	t.Helper()
	dg, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	return &discordgoSession{dg: dg, userID: "300", voice: m}
}

// The recorder's voice state and voice server, as discordgo delivers them,
// reach disgo, which needs both to connect.
func TestRecorderVoiceEventsReachDisgo(t *testing.T) {
	m := &fakeManager{}
	s := recorderSession(t, m)

	s.onVoiceStateUpdate(&discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: "100", ChannelID: "200", UserID: "300", SessionID: "sess-1",
	}})
	s.onVoiceServerUpdate(&discordgo.VoiceServerUpdate{Token: "tok", GuildID: "100", Endpoint: "c-dfw.discord.media:443"})

	if len(m.states) != 1 || m.states[0].UserID != snowflake.ID(300) || m.states[0].SessionID != "sess-1" {
		t.Errorf("voice states handed to disgo = %+v, want the recorder's", m.states)
	}
	if len(m.servers) != 1 || m.servers[0].Token != "tok" {
		t.Errorf("voice servers handed to disgo = %+v, want the one Discord sent", m.servers)
	}
}

// A join opens the connection to the channel given without deafening the
// recorder, which Discord would then send no audio, and leaving closes it.
func TestRecorderJoinOpensUndeafenedAndLeaveCloses(t *testing.T) {
	m := &fakeManager{conn: &fakeDisgoConn{}}
	s := recorderSession(t, m)

	conn, err := s.Join(context.Background(), "100", "200")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}

	if !m.conn.opened || m.conn.channel != snowflake.ID(200) {
		t.Errorf("connection opened = %v to %d, want opened to 200", m.conn.opened, m.conn.channel)
	}
	if m.conn.selfDeaf {
		t.Error("the recorder joined deafened")
	}
	conn.Leave(context.Background())
	if !m.conn.closed {
		t.Error("the connection is still open after Leave")
	}
}

// A join Discord doesn't complete in time returns the error, and the
// connection is closed, since disgo leaves the join it sent standing.
func TestRecorderJoinThatTimesOutLeavesAgain(t *testing.T) {
	m := &fakeManager{conn: &fakeDisgoConn{openErr: context.DeadlineExceeded}}
	s := recorderSession(t, m)

	_, err := s.Join(context.Background(), "100", "200")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Join error = %v, want the deadline", err)
	}
	if !m.conn.closed {
		t.Error("the connection is still open after the failed join")
	}
}

// A voice state update sent while the recorder's gateway connection is down
// comes back as an error. disgo sends one from its own goroutine when it
// reconnects, where a panic would end the process.
func TestRecorderVoiceStateUpdateWithTheGatewayDownIsAnError(t *testing.T) {
	s := recorderSession(t, &fakeManager{})
	channelID := snowflake.ID(200)

	if err := s.updateVoiceState(context.Background(), snowflake.ID(100), &channelID, true, false); err == nil {
		t.Error("updateVoiceState with no gateway connection = nil, want an error")
	}
}

// A recorder the bot stopped logging in, as after Discord refused its
// token, has no gateway connection. A join through it fails, as a join
// Discord never completes does, so /record start gets the reply a failed
// join gets, and nothing panics.
func TestRecorderJoinWithItsGatewayStoppedFails(t *testing.T) {
	dg, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	s := &discordgoSession{dg: dg, userID: "300"}
	updateVoiceState := func(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
		return s.updateVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
	}
	s.voice = disgovoice.NewManager(updateVoiceState, snowflake.ID(300))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := s.Join(ctx, "100", "200"); err == nil {
		t.Error("Join through a recorder with no gateway connection = nil error, want one")
	}
}

// Closing the session at shutdown takes the recorder out of its voice
// channels.
func TestRecorderCloseLeavesVoice(t *testing.T) {
	m := &fakeManager{}
	s := recorderSession(t, m)

	_ = s.Close()

	if !m.closed {
		t.Error("the recorder's voice connections are still open after Close")
	}
}

// Discord moving a recorded call to another voice server, over disgo's own
// voice connection built with the recorder session's connection options.
// The test plays Discord around it: the voice gateways, the answer to each
// voice state update the recorder sends, and the API that says whether the
// channel is still there.

const (
	callGuild    = snowflake.ID(100)
	callChannel  = snowflake.ID(200)
	callRecorder = snowflake.ID(300)
	oldEndpoint  = "c-dfw-1.discord.media:443"
	newEndpoint  = "c-rtm-1.discord.media:443"
	// callDAVEVersion is the DAVE protocol version every voice server
	// announces: the call is end-to-end encrypted.
	callDAVEVersion = 1
)

// voiceUpdate is a voice state update the recorder session was asked to
// send. No channel takes the recorder out.
type voiceUpdate struct {
	channelID          *snowflake.ID
	selfMute, selfDeaf bool
}

// leaves reports whether the update takes the recorder out.
func (u voiceUpdate) leaves() bool { return u.channelID == nil }

func (u voiceUpdate) String() string {
	if u.leaves() {
		return "leave"
	}
	return fmt.Sprintf("join %d mute=%v deaf=%v", *u.channelID, u.selfMute, u.selfDeaf)
}

// channelAPI stands in for Discord's API on the recorder's session. It
// answers a read of the recorded channel, there or gone, and fails the test
// on anything else. during, when set, runs while it answers.
type channelAPI struct {
	t      *testing.T
	gone   atomic.Bool
	during func()
}

func (a *channelAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	if a.during != nil {
		a.during()
	}
	status, body := http.StatusOK, fmt.Sprintf(`{"id":"%d","guild_id":"%d","type":2,"name":"Ops"}`, callChannel, callGuild)
	switch {
	case r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, fmt.Sprintf("/channels/%d", callChannel)):
		a.t.Errorf("sent %s %s to Discord's API, want only a read of the recorded channel", r.Method, r.URL.Path)
		status, body = http.StatusInternalServerError, `{"message":"unexpected request"}`
	case a.gone.Load():
		status, body = http.StatusNotFound, `{"message":"Unknown Channel","code":10003}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// answeringGateway stands in for one disgo voice gateway, which a test can't
// open without a voice server. Open answers as the voice server does, with
// the session description that completes disgo's connection, and reports the
// open. Its DAVE protocol version goes to the DAVE session first, as disgo's
// gateway hands it over. It keeps the one rule of disgo's gateway a rejoin
// turns on: a gateway that has opened before resumes its voice session when
// it opens again, since a 4022 leaves disgo's gateway holding what it
// resumes with and a rejoin keeps the session ID, while one that never
// opened identifies. Revisit the rule if a disgo bump adds a way to drop a
// session. Like disgo's, it refuses an open while it holds a connection,
// from the start of an open until the open fails or the gateway closes.
// sent hears every op sent on the gateway. A voice server that answers
// rejects never answers: Open tells pending, then waits out its context.
type answeringGateway struct {
	disgovoice.Gateway
	dave      godave.Session
	onEvent   disgovoice.EventHandlerFunc
	onClose   disgovoice.CloseHandlerFunc
	opened    chan<- gatewayOpen
	sent      func(disgovoice.Opcode)
	answers   func(endpoint string) bool
	pending   chan<- struct{}
	opens     atomic.Int32
	holdsConn atomic.Bool
}

// gatewayOpen is one gateway's Open: the state it opened with, and whether
// it identified rather than resumed.
type gatewayOpen struct {
	gateway    *answeringGateway
	state      disgovoice.State
	identified bool
}

func (g *answeringGateway) Open(ctx context.Context, state disgovoice.State) error {
	if !g.holdsConn.CompareAndSwap(false, true) {
		return discord.ErrGatewayAlreadyConnected
	}
	identified := g.opens.Add(1) == 1
	if !g.answers(state.Endpoint) {
		g.pending <- struct{}{}
		<-ctx.Done()
		g.holdsConn.Store(false)
		return ctx.Err()
	}
	g.dave.OnSelectProtocolAck(callDAVEVersion)
	g.onEvent(g, disgovoice.OpcodeSessionDescription, 0, disgovoice.GatewayMessageDataSessionDescription{
		Mode:                disgovoice.EncryptionModeAEADAES256GCMRTPSize,
		SecretKey:           make([]byte, 32),
		DaveProtocolVersion: callDAVEVersion,
	})
	g.opened <- gatewayOpen{gateway: g, state: state, identified: identified}
	return nil
}

func (g *answeringGateway) Send(_ context.Context, op disgovoice.Opcode, _ disgovoice.GatewayMessageData) error {
	g.sent(op)
	return nil
}

func (g *answeringGateway) Close() { g.holdsConn.Store(false) }

func (g *answeringGateway) CloseWithCode(int, string) { g.Close() }

// callHarness is a recorder's disgo voice connection over answering
// gateways, with Discord played around it, and a dave-go DAVE session as
// the recorder's session gives it. The call starts at oldEndpoint.
type callHarness struct {
	t    *testing.T
	s    *discordgoSession
	conn disgovoice.Conn
	api  *channelAPI

	// down fails every voice state update, as the recorder's gateway being
	// down does.
	down atomic.Bool
	// silent has no voice server answer an open from then on. pending
	// hears each open a gateway leaves waiting.
	silent  atomic.Bool
	pending chan struct{}
	// answerDelay is how long Discord takes to answer a join.
	answerDelay atomic.Int64

	opened chan gatewayOpen
	// connected is the gateway the recorder is connected through.
	connected *answeringGateway

	mu      sync.Mutex
	updates []voiceUpdate
	// ops is every op sent on any gateway, in order.
	ops []disgovoice.Opcode
	// serving is the voice server serving the call, which Discord names
	// in its answer to a join, and left the one a 4022 moved it from,
	// which answers no open since.
	serving, left string
}

func newCallHarness(t *testing.T) *callHarness {
	t.Helper()
	h := &callHarness{t: t, s: recorderSession(t, &fakeManager{}), api: &channelAPI{t: t},
		opened: make(chan gatewayOpen, 64), pending: make(chan struct{}, 64), serving: oldEndpoint}
	h.s.dg.Client = &http.Client{Transport: h.api}
	opts := append(h.s.connOpts(h.newGateway),
		disgovoice.WithConnDaveSessionCreateFunc(session.CreateFunc()),
		disgovoice.WithConnDaveSessionLogger(slog.New(slog.DiscardHandler)))
	h.conn = disgovoice.NewConn(callGuild, callRecorder, h.updateVoiceState, func() {}, opts...)
	h.s.voice = callManager{conn: h.conn}
	return h
}

func (h *callHarness) newGateway(dave godave.Session, onEvent disgovoice.EventHandlerFunc, onClose disgovoice.CloseHandlerFunc, _ ...disgovoice.GatewayConfigOpt) disgovoice.Gateway {
	return &answeringGateway{dave: dave, onEvent: onEvent, onClose: onClose, opened: h.opened, sent: h.sentOp,
		answers: h.answersAt, pending: h.pending}
}

// callManager is the recorder session's voice manager over the harness's
// connection, which every join opens.
type callManager struct {
	disgovoice.Manager
	conn disgovoice.Conn
}

func (m callManager) CreateConn(snowflake.ID) disgovoice.Conn { return m.conn }

// answersAt reports whether the voice server at endpoint answers an open:
// any but the one the call left, unless none answers.
func (h *callHarness) answersAt(endpoint string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.silent.Load() && endpoint != h.left
}

func (h *callHarness) sentOp(op disgovoice.Opcode) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, op)
}

// keyPackagesSent counts the DAVE key packages, op 26, sent on any gateway.
func (h *callHarness) keyPackagesSent() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, op := range h.ops {
		if op == disgovoice.OpcodeDaveMLSKeyPackage {
			n++
		}
	}
	return n
}

// record starts a recording of the call, as /record start does: the
// recorder session joins the channel, and the connection's receiver hands
// frames to handle.
func (h *callHarness) record(handle func(Frame)) Conn {
	h.t.Helper()
	conn, err := h.s.Join(context.Background(), callGuild.String(), callChannel.String())
	if err != nil {
		h.t.Fatalf("Join: %v", err)
	}
	h.waitOpen()
	conn.Receive(handle)
	return conn
}

// updateVoiceState is the connection's voice state update: the recorder
// session's own, which Discord receives while the recorder's gateway is up
// and answers when it names a channel.
func (h *callHarness) updateVoiceState(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
	h.mu.Lock()
	h.updates = append(h.updates, voiceUpdate{channelID: channelID, selfMute: selfMute, selfDeaf: selfDeaf})
	h.mu.Unlock()
	// The session is never opened, so the update fails: Discord standing in
	// for it receives it unless the gateway is down.
	err := h.s.updateVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
	if h.down.Load() {
		return err
	}
	if channelID != nil {
		go h.answer(*channelID)
	}
	return nil
}

// answer is Discord's answer to the recorder joining a channel, after
// answerDelay: its voice state, and the voice server now serving the call.
func (h *callHarness) answer(channelID snowflake.ID) {
	time.Sleep(time.Duration(h.answerDelay.Load()))
	h.mu.Lock()
	serving := h.serving
	h.mu.Unlock()
	h.conn.HandleVoiceStateUpdate(recorderVoiceState(channelID))
	h.conn.HandleVoiceServerUpdate(voiceServer("token-2", serving))
}

// join sends the events Discord sends once it has let the recorder into the
// channel at the old voice server, and the connection opens its gateway
// there.
func (h *callHarness) join() {
	h.conn.HandleVoiceStateUpdate(recorderVoiceState(callChannel))
	h.conn.HandleVoiceServerUpdate(voiceServer("token-1", oldEndpoint))
}

// waitOpen waits for a gateway to open and returns the open. The recorder
// is connected through that gateway from then on.
func (h *callHarness) waitOpen() gatewayOpen {
	h.t.Helper()
	select {
	case o := <-h.opened:
		h.connected = o.gateway
		return o
	case <-time.After(waitFor):
		h.t.Fatal("no voice gateway opened")
		return gatewayOpen{}
	}
}

// closeCall closes the gateway the recorder is connected through with code,
// as disgo's gateway hands its close handler the close Discord sent. It
// returns once disgo has dealt with the close: rejoined, or left. A 4022
// ends the call at its voice server, and Discord moves it to newEndpoint.
func (h *callHarness) closeCall(code int) {
	if code == disgovoice.GatewayCloseEventCodeCallTerminated.Code {
		h.mu.Lock()
		h.left, h.serving = h.serving, newEndpoint
		h.mu.Unlock()
	}
	// disgo's gateway drops its connection before its close handler runs.
	h.connected.holdsConn.Store(false)
	h.connected.onClose(h.connected, &websocket.CloseError{Code: code})
}

// sent is the voice state updates the recorder session was asked to send.
func (h *callHarness) sent() []voiceUpdate {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.updates)
}

// recorderVoiceState is the recorder's voice state in a channel as Discord
// sends it: self-muted and not self-deafened, as the recorder joins.
func recorderVoiceState(channelID snowflake.ID) gateway.EventVoiceStateUpdate {
	return gateway.EventVoiceStateUpdate{VoiceState: discord.VoiceState{
		GuildID: callGuild, ChannelID: &channelID, UserID: callRecorder, SessionID: "sess-1", SelfMute: true,
	}}
}

// voiceServer is a VOICE_SERVER_UPDATE naming endpoint.
func voiceServer(token, endpoint string) gateway.EventVoiceServerUpdate {
	return gateway.EventVoiceServerUpdate{Token: token, GuildID: callGuild, Endpoint: &endpoint}
}

// A voice gateway that closes in a way disgo won't reconnect from, so that
// disgo takes the recorder out of the channel itself, is a voice connection
// lost mid-recording, and Sentry hears of it.
func TestRecorderReportsAVoiceConnectionDisgoGivesUpOn(t *testing.T) {
	events := recordSentry(t)
	h := newCallHarness(t)
	h.join()
	h.waitOpen()

	h.closeCall(4004)

	if got := len(events.Events()); got != 1 {
		t.Errorf("Sentry events = %d, want 1 for the lost connection", got)
	}
}

// A call Discord moves to another voice server, as a region change makes
// it do, closes the recorder's voice gateway with 4022, "Call terminated".
// The channel is still there, so the recorder rejoins it as it joined,
// self-muted and never self-deafened, and nothing reaches Sentry.
func TestRecorderRejoinsWhenItsCallMovesToAnotherVoiceServer(t *testing.T) {
	events := recordSentry(t)
	h := newCallHarness(t)
	h.join()
	h.waitOpen()

	h.closeCall(4022)

	if got := h.sent(); len(got) != 1 || got[0].String() != fmt.Sprintf("join %d mute=true deaf=false", callChannel) {
		t.Errorf("voice state updates after the close = %v, want one joining %d self-muted and not self-deafened", got, callChannel)
	}
	if got := len(events.Events()); got != 0 {
		t.Errorf("Sentry events = %d, want none for a call that moved", got)
	}
}

// A call that moves takes the recorder to a voice server that never had its
// voice session, so the rejoin identifies there. The gateway the call left
// would resume, and a resume gets no answer that opens audio. That holds
// whether Discord named the new server before the 4022 or only in its
// answer to the rejoin.
func TestRecorderIdentifiesAtTheVoiceServerItsCallMovedTo(t *testing.T) {
	for _, tc := range []struct {
		name        string
		namedBefore bool
	}{
		{"new server named before the close", true},
		{"new server named in the answer to the rejoin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCallHarness(t)
			h.join()
			h.waitOpen()
			if tc.namedBefore {
				h.conn.HandleVoiceServerUpdate(voiceServer("token-2", newEndpoint))
			}

			h.closeCall(4022)

			if o := h.waitOpen(); o.state.Endpoint != newEndpoint || !o.identified {
				t.Errorf("gateway opened at %s with identified=%v, want an identify at %s", o.state.Endpoint, o.identified, newEndpoint)
			}
		})
	}
}

// A 4022 also ends the call in a channel someone deleted. Discord's API says
// the channel is gone, so the recorder is taken out without a rejoin, though
// the delete may not have reached the recorder's session yet.
func TestRecorderLeavesWhenItsChannelIsDeleted(t *testing.T) {
	h := newCallHarness(t)
	h.join()
	h.waitOpen()
	h.api.gone.Store(true)

	h.closeCall(4022)

	if got := h.sent(); len(got) != 1 || got[0].String() != "leave" {
		t.Errorf("voice state updates after the close = %v, want the recorder taken out and no rejoin", got)
	}
}

// Discord can take the recorder out of the channel while the recorder asks
// whether the channel is still there after a 4022. disgo then starts no
// fresh connection, and the recorder is out as surely as after a rejoin
// that fails, so Sentry hears of it once.
func TestRecorderReportsAMovedCallItCouldNotRejoin(t *testing.T) {
	events := recordSentry(t)
	h := newCallHarness(t)
	h.join()
	h.waitOpen()
	h.api.during = func() {
		h.conn.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{VoiceState: discord.VoiceState{
			GuildID: callGuild, UserID: callRecorder, SessionID: "sess-1",
		}})
	}

	h.closeCall(4022)

	got := events.Events()
	if len(got) != 1 {
		t.Fatalf("Sentry events = %d, want 1 for the lost connection", len(got))
	}
	if msg := got[0].Tags["message"]; msg != lostConnectionMessage {
		t.Errorf("Sentry event = %q, want %q", msg, lostConnectionMessage)
	}
}

// A rejoin that fails, here because the recorder's own gateway is down
// when the rejoin goes out, ends as a lost connection does: the recorder is
// taken out, and Sentry hears of it once.
func TestRecorderReportsARejoinThatFails(t *testing.T) {
	events := recordSentry(t)
	h := newCallHarness(t)
	h.join()
	h.waitOpen()
	h.down.Store(true)

	h.closeCall(4022)

	if got := h.sent(); len(got) == 0 || got[len(got)-1].String() != "leave" {
		t.Errorf("voice state updates after the close = %v, want the recorder taken out last", got)
	}
	got := events.Events()
	if len(got) != 1 {
		t.Fatalf("Sentry events = %d, want 1 for the lost connection", len(got))
	}
	if msg := got[0].Tags["message"]; msg != lostConnectionMessage {
		t.Errorf("Sentry event = %q, want %q", msg, lostConnectionMessage)
	}
}

// Discord can leave the recorder's DAVE setup unanswered, as it did once
// when the recorder and a speaker raced to commit after a move: dave-go
// then holds every frame, and the recorder hears nobody. Past 3 s that's a
// stall. Sentry hears of it once, by recorder and channel, and the recorder
// starts DAVE over with a new key package without leaving the channel, so
// the recording carries on. The voice server here answers the join with
// DAVE and then says nothing more, as in the stall seen on the test guild.
func TestRecorderStartsDAVEOverWhenItsSetupStalls(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.record(func(Frame) {})
		defer conn.Leave(context.Background())

		time.Sleep(3500 * time.Millisecond)
		synctest.Wait()

		got := events.Events()
		if len(got) != 1 {
			t.Fatalf("Sentry events = %d, want 1 for the stall", len(got))
		}
		if !eventMentions(got[0], callRecorder.String()) || !eventMentions(got[0], callChannel.String()) {
			t.Errorf("Sentry event %v names neither or one of recorder %d and channel %d, want both",
				eventTexts(got[0]), callRecorder, callChannel)
		}
		if n := h.keyPackagesSent(); n < 2 {
			t.Errorf("DAVE key packages sent = %d, want a new one after the stall beside the join's", n)
		}
		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Errorf("voice state updates = %v, want the recorder never to leave", h.sent())
		}
	})
}

// Stalls follow moves, so Discord can move the call again while the
// recorder's DAVE setup is stalled, and answer the rejoin only once the
// stall's 3 s have passed. The recorder follows the call there, and the
// recording carries on: the recorder never leaves the channel, though
// disgo gives up on a rejoin after 30 s.
func TestRecorderFollowsACallThatMovesDuringAStall(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.record(func(Frame) {})
		defer conn.Leave(context.Background())

		time.Sleep(2500 * time.Millisecond)
		h.answerDelay.Store(int64(2 * time.Second))
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(32500 * time.Millisecond)
		synctest.Wait()

		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Errorf("voice state updates = %v, want the recorder never to leave", h.sent())
		}
	})
}

// A member can stop the recording while a fresh connection is still
// opening, here at a voice server that never answers. The recorder leaves
// at once, as any stop has it do, opens nothing more, and Sentry hears
// nothing more of the stall.
func TestRecorderStoppedDuringARecoveryLeavesAtOnceAndReportsNothingMore(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.record(func(Frame) {})
		h.silent.Store(true)
		select {
		case <-h.pending:
		case <-time.After(5 * time.Second):
			t.Fatal("no fresh connection opening 5 s into the stall")
		}
		atStop := len(events.Events())

		stopped := time.Now()
		conn.Leave(context.Background())
		leaving := time.Since(stopped)
		time.Sleep(60 * time.Second)
		synctest.Wait()

		if leaving >= time.Second {
			t.Errorf("the stop took %v to take the recorder out, want under 1 s", leaving)
		}
		sent := h.sent()
		if firstLeave := slices.IndexFunc(sent, voiceUpdate.leaves); firstLeave != len(sent)-1 {
			t.Errorf("voice state updates = %v, want the stop's leave last and alone", sent)
		}
		if opens := len(h.opened) + len(h.pending); opens != 0 {
			t.Errorf("gateways opened after the stop = %d, want none", opens)
		}
		if n := len(events.Events()); n != atStop {
			t.Errorf("Sentry events after the stop = %d, want none", n-atStop)
		}
	})
}

// eventMentions reports whether some text the Sentry event carries contains
// want.
func eventMentions(e *sentry.Event, want string) bool {
	return slices.ContainsFunc(eventTexts(e), func(text string) bool { return strings.Contains(text, want) })
}

// recorderWatchAfter is how long the tests' recorder watches wait before
// they report, short enough to wait out.
const recorderWatchAfter = 50 * time.Millisecond

// waitForEvents waits for Sentry to have at least n events, then waits out
// several more delays, so a report sent after them is counted too.
func waitForEvents(t *testing.T, events *sentryTransport, n int) []*sentry.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(events.Events()) < n && time.Now().Before(deadline) {
		time.Sleep(recorderWatchAfter / 5)
	}
	time.Sleep(4 * recorderWatchAfter)
	return events.Events()
}

// eventTexts is the text a Sentry event carries where a capture's pairs
// and error land: its extra context, its tags, its message and its
// exceptions.
func eventTexts(e *sentry.Event) []string {
	texts := []string{e.Message}
	for _, v := range e.Contexts["extra"] {
		texts = append(texts, fmt.Sprint(v))
	}
	for _, v := range e.Tags {
		texts = append(texts, v)
	}
	for _, x := range e.Exception {
		texts = append(texts, x.Value)
	}
	return texts
}

// A recorder whose gateway session drops and stays down reaches Sentry by
// its place in RECORDER_TOKENS, as "Recorder failed to connect" names it,
// and by its user ID. The token is the recorder account, so the event
// carries it nowhere.
func TestARecorderOfflineIsNamedByItsPlaceAndUserIDNeverItsToken(t *testing.T) {
	events := recordSentry(t)
	const token = "recorder-token-7f3a"
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	s := &discordgoSession{dg: dg, voice: &fakeManager{}}
	// Watched before it opens, and named once READY has told its user ID.
	s.watchGateway(2, recorderWatchAfter)
	s.named("300")

	// discordgo closes a session whose gateway connection drops, and the
	// close emits its Disconnect.
	_ = dg.Close()

	got := waitForEvents(t, events, 1)
	if len(got) != 1 {
		t.Fatalf("Sentry got %d events, want 1", len(got))
	}
	extra := got[0].Contexts["extra"]
	if r, id := fmt.Sprint(extra["recorder"]), fmt.Sprint(extra["user_id"]); r != "2" || id != "300" {
		t.Errorf("event names recorder %s with user ID %s, want recorder 2 with user ID 300", r, id)
	}
	for _, text := range eventTexts(got[0]) {
		if strings.Contains(text, token) {
			t.Errorf("event carries the recorder's token in %q", text)
		}
	}
}

// Shutdown closes the recorder's session on purpose, and Sentry hears
// nothing of the Disconnect that close emits.
func TestRecorderCloseSendsNoOutage(t *testing.T) {
	events := recordSentry(t)
	s := recorderSession(t, &fakeManager{})
	s.watchGateway(1, recorderWatchAfter)

	_ = s.Close()

	if got := waitForEvents(t, events, 0); len(got) != 0 {
		t.Errorf("Sentry got %d events after the recorder closed, want none", len(got))
	}
}
