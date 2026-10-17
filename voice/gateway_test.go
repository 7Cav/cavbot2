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
// voice manager built as the recorder session builds it. The test plays
// Discord around it: the voice gateways, the answer to each voice state
// update the recorder sends, and the API that says whether the channel is
// still there.

const (
	callGuild    = snowflake.ID(100)
	callChannel  = snowflake.ID(200)
	callRecorder = snowflake.ID(300)
	oldEndpoint  = "c-dfw-1.discord.media:443"
	newEndpoint  = "c-rtm-1.discord.media:443"
	// earlyEndpoint is a voice server Discord names for a moved call
	// before it names newEndpoint, and laterEndpoint one it names after.
	earlyEndpoint = "c-ams-1.discord.media:443"
	laterEndpoint = "c-hel-1.discord.media:443"
	// callDAVEVersion is the DAVE protocol version every voice server
	// announces, unless the call is transport-only: the call is end-to-end
	// encrypted.
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
// rejects never answers: Open tells pending, then waits out its context. One
// that reachable rejects can't be dialed, as when the host's resolver answers
// "no such host" for its hostname: Open dials again as disgo's gateway does.
type answeringGateway struct {
	disgovoice.Gateway
	dave      godave.Session
	onEvent   disgovoice.EventHandlerFunc
	onClose   disgovoice.CloseHandlerFunc
	opened    chan<- gatewayOpen
	sent      func(disgovoice.Opcode)
	answers   func(endpoint string) bool
	reachable func(endpoint string) bool
	// daveVersion is the DAVE protocol version the voice server announces.
	daveVersion uint16
	pending     chan<- struct{}
	opens       atomic.Int32
	holdsConn   atomic.Bool
}

// gatewayOpen is one gateway's Open: the state it opened with, and whether
// it identified rather than resumed.
type gatewayOpen struct {
	gateway    *answeringGateway
	state      disgovoice.State
	identified bool
}

func (g *answeringGateway) Open(ctx context.Context, state disgovoice.State) error {
	// disgo's gateway dials the same state again after a dial fails, 1 s
	// after the first and twice as long each time after, up to 10 s, until
	// ctx ends. It holds no connection meanwhile.
	for delay := time.Duration(0); ; delay = nextDialDelay(delay) {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		if g.reachable(state.Endpoint) {
			break
		}
	}
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
	g.dave.OnSelectProtocolAck(g.daveVersion)
	g.onEvent(g, disgovoice.OpcodeSessionDescription, 0, disgovoice.GatewayMessageDataSessionDescription{
		Mode:                disgovoice.EncryptionModeAEADAES256GCMRTPSize,
		SecretKey:           make([]byte, 32),
		DaveProtocolVersion: int(g.daveVersion),
	})
	g.opened <- gatewayOpen{gateway: g, state: state, identified: identified}
	return nil
}

// nextDialDelay is disgo's wait before its gateway dials again, after it
// waited delay before the dial that just failed.
func nextDialDelay(delay time.Duration) time.Duration {
	if delay == 0 {
		return time.Second
	}
	return min(2*delay, 10*time.Second)
}

func (g *answeringGateway) Send(_ context.Context, op disgovoice.Opcode, _ disgovoice.GatewayMessageData) error {
	g.sent(op)
	return nil
}

func (g *answeringGateway) Close() { g.holdsConn.Store(false) }

func (g *answeringGateway) CloseWithCode(int, string) { g.Close() }

// callHarness is a recorder session whose disgo voice connections run over
// answering gateways, with Discord played around them, and a dave-go DAVE
// session as the recorder's session gives each. The call starts at
// oldEndpoint.
type callHarness struct {
	t   *testing.T
	s   *discordgoSession
	api *channelAPI

	// down fails every voice state update, as the recorder's gateway being
	// down does.
	down atomic.Bool
	// silent has no voice server answer an open from then on. pending
	// hears each open a gateway leaves waiting.
	silent  atomic.Bool
	pending chan struct{}
	// transportOnly makes the call one without DAVE, as a stage channel's
	// is, for gateways built from then on. DAVE then holds no frame, where
	// in an end-to-end encrypted call it holds every frame here: no voice
	// server answers its setup.
	transportOnly atomic.Bool
	// answerDelay is how long Discord takes to answer a join, and
	// stateOnly has the answer carry the recorder's voice state alone, with
	// no voice server.
	answerDelay atomic.Int64
	stateOnly   atomic.Bool

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
	// unreachable is the voice servers the recorder can't dial.
	unreachable map[string]bool
}

func newCallHarness(t *testing.T) *callHarness {
	t.Helper()
	h := &callHarness{t: t, s: recorderSession(t, &fakeManager{}), api: &channelAPI{t: t},
		opened: make(chan gatewayOpen, 64), pending: make(chan struct{}, 64), serving: oldEndpoint,
		unreachable: make(map[string]bool)}
	h.s.dg.Client = &http.Client{Transport: h.api}
	h.s.voice = h.s.voiceManager(h.updateVoiceState, callRecorder, h.newGateway,
		disgovoice.WithDaveSessionCreateFunc(session.CreateFunc()),
		disgovoice.WithDaveSessionLogger(slog.New(slog.DiscardHandler)))
	return h
}

func (h *callHarness) newGateway(dave godave.Session, onEvent disgovoice.EventHandlerFunc, onClose disgovoice.CloseHandlerFunc, _ ...disgovoice.GatewayConfigOpt) disgovoice.Gateway {
	version := uint16(callDAVEVersion)
	if h.transportOnly.Load() {
		version = 0
	}
	return &answeringGateway{dave: dave, onEvent: onEvent, onClose: onClose, opened: h.opened, sent: h.sentOp,
		answers: h.answersAt, reachable: h.reachable, daveVersion: version, pending: h.pending}
}

// answersAt reports whether the voice server at endpoint answers an open:
// any but the one the call left, unless none answers.
func (h *callHarness) answersAt(endpoint string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.silent.Load() && endpoint != h.left
}

// reachable reports whether the recorder can dial the voice server at
// endpoint.
func (h *callHarness) reachable(endpoint string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.unreachable[endpoint]
}

// setReachable makes the voice server at endpoint one the recorder can
// dial, or one it can't.
func (h *callHarness) setReachable(endpoint string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unreachable[endpoint] = !ok
}

// leftChannel is Discord taking the recorder out of the channel: its voice
// state with no channel.
func (h *callHarness) leftChannel() {
	h.s.voice.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{VoiceState: discord.VoiceState{
		GuildID: callGuild, UserID: callRecorder, SessionID: "sess-1",
	}})
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
// what it hears to l.
func (h *callHarness) record(l Listener) Conn {
	h.t.Helper()
	conn := h.start()
	conn.Receive(l)
	return conn
}

// start has the recorder session join the channel, as a recording's start
// does, and returns once the connection's gateway has opened. The voice
// state updates sent count from then.
func (h *callHarness) start() Conn {
	h.t.Helper()
	conn, err := h.s.Join(context.Background(), callGuild.String(), callChannel.String())
	if err != nil {
		h.t.Fatalf("Join: %v", err)
	}
	h.waitOpen()
	h.mu.Lock()
	h.updates = nil
	h.mu.Unlock()
	return conn
}

// hangUp stops the recording on conn, as a member's stop does, and waits a
// minute out, so whatever the recorder still had in flight ends inside the
// test's bubble. A test under synctest defers it, failing or not.
func (h *callHarness) hangUp(conn Conn) {
	conn.Leave(context.Background())
	time.Sleep(time.Minute)
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
// answerDelay: its voice state, then the voice server now serving the call
// unless stateOnly.
func (h *callHarness) answer(channelID snowflake.ID) {
	time.Sleep(time.Duration(h.answerDelay.Load()))
	h.mu.Lock()
	serving := h.serving
	h.mu.Unlock()
	h.s.voice.HandleVoiceStateUpdate(recorderVoiceState(channelID))
	if !h.stateOnly.Load() {
		h.name("token-2", serving)
	}
}

// name is Discord naming the voice server at endpoint for the call, with
// token.
func (h *callHarness) name(token, endpoint string) {
	h.s.voice.HandleVoiceServerUpdate(voiceServer(token, endpoint))
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
	h.endConnection(&websocket.CloseError{Code: code})
}

// endConnection hands err to the close handler of the gateway the recorder
// is connected through, as disgo's gateway does when it gives up on its
// connection. It returns once disgo has dealt with err.
func (h *callHarness) endConnection(err error) {
	// disgo's gateway drops its connection before its close handler runs.
	h.connected.holdsConn.Store(false)
	h.connected.onClose(h.connected, err)
}

// resumesRanOut is what disgo's gateway hands its close handler once every
// try to resume after a close has failed, the last with err.
func resumesRanOut(err error) error {
	return fmt.Errorf("failed to reconnect voice gateway after 5 attempts: %w", err)
}

// sent is the voice state updates the recorder session was asked to send
// since the last start.
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
	h.start()

	h.closeCall(4004)

	if got := h.sent(); len(got) != 1 || got[0].String() != "leave" {
		t.Errorf("voice state updates after the close = %v, want the recorder taken out and no rejoin", got)
	}
	if got := len(events.Events()); got != 1 {
		t.Errorf("Sentry events = %d, want 1 for the lost connection", got)
	}
}

// disgo answers some closes with a fresh connection in the same channel.
// 4006, 4009 and 4011 get one at once. A close disgo resumes from gets one
// once every try to resume has failed, whether the last try failed on a
// close, such as 4015 or a code disgo doesn't know, or on the dial. The
// recorder rejoins the channel as it joined, self-muted and never
// self-deafened, and identifies at the voice server, since a resume gets no
// answer that opens audio. It never leaves, and nothing reaches Sentry.
func TestRecorderKeepsRecordingThroughACloseThatGetsAFreshConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"4006", &websocket.CloseError{Code: 4006}},
		{"4009", &websocket.CloseError{Code: 4009}},
		{"4011", &websocket.CloseError{Code: 4011}},
		{"resumes ran out on 4015", resumesRanOut(&websocket.CloseError{Code: 4015})},
		{"resumes ran out on a code disgo doesn't know", resumesRanOut(&websocket.CloseError{Code: 4100})},
		{"resumes ran out on the dial", resumesRanOut(errors.New("dial tcp: lookup c-dfw-1.discord.media: no such host"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := recordSentry(t)
			h := newCallHarness(t)
			h.start()

			h.endConnection(tc.err)

			if o := h.waitOpen(); !o.identified {
				t.Errorf("gateway opened at %s with identified=false, want an identify", o.state.Endpoint)
			}
			if got := h.sent(); len(got) != 1 || got[0].String() != fmt.Sprintf("join %d mute=true deaf=false", callChannel) {
				t.Errorf("voice state updates after the close = %v, want one joining %d self-muted and not self-deafened", got, callChannel)
			}
			if got := len(events.Events()); got != 0 {
				t.Errorf("Sentry events = %d, want none for a fresh connection that opened", got)
			}
		})
	}
}

// A fresh connection that fails, here because the recorder's own gateway is
// down when its rejoin goes out, ends as a lost connection does. disgo takes
// the recorder out, and Sentry hears of it once.
func TestRecorderReportsAFreshConnectionThatFails(t *testing.T) {
	events := recordSentry(t)
	h := newCallHarness(t)
	h.start()
	h.down.Store(true)

	h.closeCall(4006)

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

// A call Discord moves to another voice server, as a region change makes
// it do, closes the recorder's voice gateway with 4022, "Call terminated".
// The channel is still there, so the recorder rejoins it as it joined,
// self-muted and never self-deafened, and nothing reaches Sentry.
func TestRecorderRejoinsWhenItsCallMovesToAnotherVoiceServer(t *testing.T) {
	events := recordSentry(t)
	h := newCallHarness(t)
	h.start()

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
			h.start()
			if tc.namedBefore {
				h.name("token-2", newEndpoint)
			}

			h.closeCall(4022)

			if o := h.waitOpen(); o.state.Endpoint != newEndpoint || !o.identified {
				t.Errorf("gateway opened at %s with identified=%v, want an identify at %s", o.state.Endpoint, o.identified, newEndpoint)
			}
		})
	}
}

// Discord can name the voice server a call moved to before the rejoin
// starts, and then answer the rejoin with the recorder's voice state alone,
// as it did once in 8 moves on the test guild. The recorder still
// identifies at the server Discord named, with the token it gave, and
// nothing reaches Sentry. That holds whether Discord named it while the old
// gateway was still open or while the recorder asked whether the channel is
// still there.
func TestRecorderFollowsAMoveWhoseServerDiscordNamedBeforeTheRejoin(t *testing.T) {
	for _, tc := range []struct {
		name        string
		namedBefore bool
	}{
		{"named before the close", true},
		{"named during the channel lookup", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := recordSentry(t)
			synctest.Test(t, func(t *testing.T) {
				h := newCallHarness(t)
				h.start()
				if tc.namedBefore {
					h.name("token-3", newEndpoint)
				} else {
					h.api.during = func() { h.name("token-3", newEndpoint) }
				}
				h.stateOnly.Store(true)

				h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)

				if o := h.waitOpen(); o.state.Endpoint != newEndpoint || o.state.Token != "token-3" || !o.identified {
					t.Errorf("gateway opened at %s with token %s and identified=%v, want an identify at %s with token-3",
						o.state.Endpoint, o.state.Token, o.identified, newEndpoint)
				}
				if got := len(events.Events()); got != 0 {
					t.Errorf("Sentry events = %d, want none for a call the recorder followed", got)
				}
			})
		})
	}
}

// Discord can name one voice server before the rejoin starts and another
// in its answer, the voice state then the server, when it moves the call
// twice. The server named last counts: the recorder opens one gateway, and
// opens it there.
func TestRecorderFollowsAMoveToTheServerDiscordNamedLast(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.start()
		h.api.during = func() { h.name("token-3", earlyEndpoint) }

		h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		o := h.waitOpen()
		time.Sleep(time.Minute)
		synctest.Wait()

		if o.state.Endpoint != newEndpoint || o.state.Token != "token-2" {
			t.Errorf("gateway opened at %s with token %s, want %s with token-2, the server named last",
				o.state.Endpoint, o.state.Token, newEndpoint)
		}
		if more := len(h.opened) + len(h.pending); more != 0 {
			t.Errorf("gateways opened after the first = %d, want none", more)
		}
	})
}

// A call can move to a voice server the recorder can't reach yet, such as
// one whose hostname its resolver doesn't know, and then move again while
// the recorder still tries it. Discord names the next server with a voice
// server update alone. The recorder connects there next, and identifies
// with the token Discord gave for it.
func TestRecorderConnectsWhereDiscordNamesWhileItTriesAnEarlierServer(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.setReachable(newEndpoint, false)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(13 * time.Second)

		h.name("token-3", laterEndpoint)
		synctest.Wait()

		if o := h.waitOpen(); o.state.Endpoint != laterEndpoint || o.state.Token != "token-3" || !o.identified {
			t.Errorf("gateway opened at %s with token %s and identified=%v, want an identify at %s with token-3",
				o.state.Endpoint, o.state.Token, o.identified, laterEndpoint)
		}
	})
}

// A recorder that can't reach the voice server its call moved to keeps
// trying past the 30 s disgo gives a rejoin. It stays in the channel, so the
// recording goes on, and its connection isn't lost.
func TestRecorderStaysInWhileItCantReachTheServerItsCallMovedTo(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.setReachable(newEndpoint, false)

		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(time.Minute)
		synctest.Wait()

		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Errorf("voice state updates = %v, want the recorder never to leave", h.sent())
		}
		if lost := slices.Contains(eventMessages(events.Events()), lostConnectionMessage); lost {
			t.Errorf("Sentry events = %v, want no %q", eventMessages(events.Events()), lostConnectionMessage)
		}
	})
}

// Once the voice server a call moved to becomes reachable, past the 30 s
// disgo gives a rejoin, the recorder connects there and hears the channel
// again: the gap that started when the call closed ends. It never leaves
// the channel.
func TestRecorderConnectsOnceTheServerItsCallMovedToIsReachable(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		l := listen(nil)
		conn := h.record(l)
		defer h.hangUp(conn)
		h.setReachable(newEndpoint, false)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(time.Second)
		synctest.Wait()
		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted}) {
			t.Fatalf("gap events 1 s after the close = %v, want a gap started", got)
		}

		time.Sleep(time.Minute)
		h.setReachable(newEndpoint, true)
		time.Sleep(11 * time.Second)
		synctest.Wait()

		if o := h.waitOpen(); o.state.Endpoint != newEndpoint {
			t.Errorf("gateway opened at %s, want %s", o.state.Endpoint, newEndpoint)
		}
		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Errorf("gap events once connected = %v, want the gap started and ended", got)
		}
		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Errorf("voice state updates = %v, want the recorder never to leave", h.sent())
		}
	})
}

// A gap that reaches 10 s reaches Sentry once, naming the recorder, the
// channel and the voice server the recorder is trying to reach, while the
// recording still runs.
func TestRecorderReportsAGapThatReaches10s(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		conn := h.record(listen(nil))
		defer h.hangUp(conn)
		h.setReachable(newEndpoint, false)

		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(15 * time.Second)
		synctest.Wait()

		got := events.Events()
		if len(got) != 1 || got[0].Tags["message"] != gapMessage {
			t.Fatalf("Sentry events = %v, want one %q", eventMessages(got), gapMessage)
		}
		for _, want := range []string{callRecorder.String(), callChannel.String(), newEndpoint} {
			if !eventMentions(got[0], want) {
				t.Errorf("Sentry event %v doesn't name %s", eventTexts(got[0]), want)
			}
		}
	})
}

// A gap that ends before 10 s is no news for Sentry, however long the
// recording goes on after it.
func TestRecorderReportsNothingOfAGapUnder10s(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		conn := h.record(listen(nil))
		defer h.hangUp(conn)
		h.setReachable(newEndpoint, false)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(9 * time.Second)

		h.name("token-3", laterEndpoint)
		time.Sleep(time.Minute)
		synctest.Wait()

		if got := events.Events(); len(got) != 0 {
			t.Errorf("Sentry events = %v, want none for a gap under 10 s", eventMessages(got))
		}
	})
}

// A stalled DAVE setup is a gap that has already reached Sentry, as a
// stall. Past 10 s Sentry hears nothing more of it.
func TestRecorderReportsAStallPast10sOnlyAsAStall(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.record(listen(nil))
		defer h.hangUp(conn)

		time.Sleep(15 * time.Second)
		synctest.Wait()

		if got := events.Events(); len(got) != 1 || got[0].Tags["message"] != stallMessage {
			t.Errorf("Sentry events = %v, want one %q", eventMessages(got), stallMessage)
		}
	})
}

// Once Discord has named a later voice server for the call, the recorder
// never connects to an earlier one, even when the earlier one becomes
// reachable while the later one still isn't: the call isn't there anymore.
func TestRecorderNeverGoesBackToAServerDiscordNamedBefore(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.setReachable(newEndpoint, false)
		h.setReachable(laterEndpoint, false)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(13 * time.Second)
		h.name("token-3", laterEndpoint)
		time.Sleep(time.Second)

		h.setReachable(newEndpoint, true)
		time.Sleep(time.Minute)
		synctest.Wait()

		for len(h.opened) > 0 {
			if o := <-h.opened; o.state.Endpoint == newEndpoint {
				t.Errorf("gateway opened at %s, the server Discord named before %s", newEndpoint, laterEndpoint)
			}
		}
	})
}

// A 4022 also ends the call in a channel someone deleted. Discord's API says
// the channel is gone, so the recorder is taken out without a rejoin, though
// the delete may not have reached the recorder's session yet.
func TestRecorderLeavesWhenItsChannelIsDeleted(t *testing.T) {
	h := newCallHarness(t)
	h.start()
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
	h.start()
	h.api.during = func() {
		h.s.voice.HandleVoiceStateUpdate(gateway.EventVoiceStateUpdate{VoiceState: discord.VoiceState{
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
	h.start()
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

// Discord can take the recorder out of the channel while it is still trying
// to reach the voice server its call moved to. The recorder is out, so
// Sentry hears of the lost connection once.
func TestRecorderReportsBeingTakenOutWhileItTriesToReachTheCall(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.setReachable(newEndpoint, false)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(20 * time.Second)

		h.leftChannel()
		time.Sleep(time.Minute)
		synctest.Wait()

		got := events.Events()
		if len(got) != 1 || got[0].Tags["message"] != lostConnectionMessage {
			t.Errorf("Sentry events = %v, want one %q", eventMessages(got), lostConnectionMessage)
		}
	})
}

// eventMessages is the message tag of each Sentry event.
func eventMessages(events []*sentry.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Tags["message"])
	}
	return out
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
		conn := h.record(listen(nil))
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

// Discord can name the voice server a call is moving to while the recorder
// is still connected where it was, and the recorder's DAVE setup can stall
// meanwhile. The fresh connection that starts DAVE over opens at the server
// Discord named last, with its token.
func TestRecorderStartsDAVEOverAtTheServerDiscordNamedLast(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.record(listen(nil))
		defer conn.Leave(context.Background())

		h.name("token-3", laterEndpoint)
		time.Sleep(3500 * time.Millisecond)
		synctest.Wait()

		if o := h.waitOpen(); o.state.Endpoint != laterEndpoint || o.state.Token != "token-3" {
			t.Errorf("fresh connection opened at %s with token %s, want %s with token-3",
				o.state.Endpoint, o.state.Token, laterEndpoint)
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
		conn := h.record(listen(nil))
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
		conn := h.record(listen(nil))
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

// A member can stop the recording while a rejoin after a move waits, here
// one Discord answers with the voice state alone and never names a server
// for. The member ended the recording, so Sentry hears nothing, and the
// stop's leave is the last voice state update the recorder sends, also once
// disgo's 30 s wait has run out.
func TestRecorderStoppedWhileARejoinWaitsSendsNothingMore(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		h.stateOnly.Store(true)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if got := h.sent(); len(got) != 1 || got[0].leaves() {
			t.Fatalf("voice state updates 5 s after the close = %v, want the rejoin waiting", got)
		}

		conn.Leave(context.Background())
		time.Sleep(time.Minute)
		synctest.Wait()

		assertStoppedQuietly(t, h, events)
	})
}

// A member can stop the recording while the recorder asks whether the
// channel is still there after a 4022, before the rejoin starts. The
// recorder doesn't rejoin, Sentry hears nothing, and the stop's leave is
// the last voice state update the recorder sends.
func TestRecorderStoppedDuringTheChannelLookupSendsNoRejoin(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		h.api.during = func() { conn.Leave(context.Background()) }

		h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(time.Minute)
		synctest.Wait()

		assertStoppedQuietly(t, h, events)
	})
}

// assertStoppedQuietly fails unless the stop's leave is the last voice
// state update the recorder sent, and Sentry heard nothing.
func assertStoppedQuietly(t *testing.T, h *callHarness, events *sentryTransport) {
	t.Helper()
	sent := h.sent()
	if stop := slices.IndexFunc(sent, voiceUpdate.leaves); stop == -1 || stop != len(sent)-1 {
		t.Errorf("voice state updates = %v, want the stop's leave last", sent)
	}
	if n := len(events.Events()); n != 0 {
		t.Errorf("Sentry events = %d, want none for a recording a member stopped", n)
	}
}

// A member can stop the recording just after Discord answers a rejoin with
// the voice state alone, having named the server before the rejoin. The
// recorder opens no voice gateway after the stop, at that server or
// anywhere else.
func TestRecorderStoppedRightAfterAStateOnlyAnswerOpensNothing(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		h.api.during = func() { h.name("token-3", newEndpoint) }
		h.stateOnly.Store(true)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		synctest.Wait()
		if got := h.sent(); len(got) != 1 || got[0].leaves() {
			t.Fatalf("voice state updates after the close = %v, want the rejoin answered", got)
		}
		before := len(h.opened)

		conn.Leave(context.Background())
		time.Sleep(time.Minute)
		synctest.Wait()

		if opens := len(h.opened) - before + len(h.pending); opens != 0 {
			t.Errorf("gateways opened after the stop = %d, want none", opens)
		}
	})
}

// A stop releases the recorder at once, so a recording can start on it in
// the same guild while disgo still waits on the rejoin the stop abandoned.
// A leave names no channel, so one sent when that wait runs out would take
// the recorder out of the new recording's channel. The recorder stays in.
func TestRecorderStartedWhileAnAbandonedRejoinWaitsStaysIn(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		h.stateOnly.Store(true)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		conn.Leave(context.Background())
		h.stateOnly.Store(false)

		h.start()
		time.Sleep(time.Minute)
		synctest.Wait()

		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Errorf("voice state updates after the new start = %v, want the recorder left in the channel", h.sent())
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
