package voice

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/7cav/cavbot2/utils"
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
// One that refusal names a close code for closes the connection before its
// Ready with that code, and Open fails with the close, as disgo's gateway's
// does.
type answeringGateway struct {
	disgovoice.Gateway
	dave      godave.Session
	onEvent   disgovoice.EventHandlerFunc
	onClose   disgovoice.CloseHandlerFunc
	opened    chan<- gatewayOpen
	sent      func(disgovoice.Opcode)
	answers   func(endpoint string) bool
	reachable func(endpoint string) bool
	refusal   func(token string) int
	// daveVersion is the DAVE protocol version the voice server announces.
	daveVersion uint16
	// opening runs as an open at an endpoint completes, before Open
	// returns, or after it when describeLate.
	opening func(endpoint string)
	// describeLate has Open return at the voice server's Ready, as disgo's
	// gateway does, with the session description following it 100 ms
	// later.
	describeLate bool
	pending      chan<- struct{}
	opens        atomic.Int32
	holdsConn    atomic.Bool
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
	if code := g.refusal(state.Token); code != 0 {
		g.holdsConn.Store(false)
		return fmt.Errorf("failed to open voice gateway connection: %w", &websocket.CloseError{Code: code})
	}
	identified := g.opens.Add(1) == 1
	if !g.answers(state.Endpoint) {
		g.pending <- struct{}{}
		<-ctx.Done()
		g.holdsConn.Store(false)
		return ctx.Err()
	}
	if g.describeLate {
		go func() {
			time.Sleep(100 * time.Millisecond)
			g.opening(state.Endpoint)
			g.describe(state, identified)
		}()
		return nil
	}
	g.describe(state, identified)
	g.opening(state.Endpoint)
	return nil
}

// describe sends the session description that completes the connection
// Open opened, and reports the open.
func (g *answeringGateway) describe(state disgovoice.State, identified bool) {
	g.dave.OnSelectProtocolAck(g.daveVersion)
	g.onEvent(g, disgovoice.OpcodeSessionDescription, 0, disgovoice.GatewayMessageDataSessionDescription{
		Mode:                disgovoice.EncryptionModeAEADAES256GCMRTPSize,
		SecretKey:           make([]byte, 32),
		DaveProtocolVersion: int(g.daveVersion),
	})
	g.opened <- gatewayOpen{gateway: g, state: state, identified: identified}
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

// hello is the voice server's Hello, naming its heartbeat interval, as
// disgo's gateway hands it to its event handler.
func (g *answeringGateway) hello(interval time.Duration) {
	g.onEvent(g, disgovoice.OpcodeHello, 0, disgovoice.GatewayMessageDataHello{HeartbeatInterval: float64(interval.Milliseconds())})
}

// answerHeartbeat is the voice server's answer to a heartbeat.
func (g *answeringGateway) answerHeartbeat() {
	g.onEvent(g, disgovoice.OpcodeHeartbeatACK, 0, disgovoice.GatewayMessageDataHeartbeatACK{T: time.Now().UnixMilli()})
}

// speaking is the voice server naming a speaker's SSRC.
func (g *answeringGateway) speaking() {
	g.onEvent(g, disgovoice.OpcodeSpeaking, 0, disgovoice.GatewayMessageDataSpeaking{SSRC: 7, UserID: 400})
}

func (g *answeringGateway) CloseWithCode(int, string) { g.Close() }

// callHarness is a recorder session whose disgo voice connections run over
// answering gateways, with Discord played around them, and a dave-go DAVE
// session as the recorder's session gives each. The call starts at
// oldEndpoint.
type callHarness struct {
	t   *testing.T
	s   *discordgoSession
	api *channelAPI
	// logs is the bot log, empty when the harness is built. disgo logs to it
	// as the recorder's session wires it. logLevel is its level, which a
	// test sets before the start as LOG_LEVEL sets it, INFO otherwise.
	logs     *logRecorder
	logLevel slog.Level

	// down fails every voice state update, as the recorder's gateway being
	// down does.
	down atomic.Bool
	// silent has no voice server answer an open from then on. pending
	// hears each open a gateway leaves waiting.
	silent  atomic.Bool
	pending chan struct{}
	// describeLate has gateways built from then on send their session
	// description only after their open has returned.
	describeLate atomic.Bool
	// transportOnly makes the call one without DAVE, as a stage channel's
	// is, for gateways built from then on. DAVE then holds no frame, where
	// in an end-to-end encrypted call it holds every frame here: no voice
	// server answers its setup.
	transportOnly atomic.Bool
	// answerDelay is how long Discord takes to answer a join, nameDelay
	// how long after the voice state its answer names a voice server, and
	// stateOnly has the answer carry the recorder's voice state alone, with
	// no voice server.
	answerDelay, nameDelay atomic.Int64
	stateOnly              atomic.Bool

	opened chan gatewayOpen
	// connected is the gateway the recorder is connected through.
	connected *answeringGateway

	mu      sync.Mutex
	updates []voiceUpdate
	// ops is every op sent on any gateway, in order.
	ops []disgovoice.Opcode
	// serving is the voice server serving the call, which Discord names
	// in its answer to a join, and left the one a 4022 moved it from,
	// which answers no open since. named is the one Discord named last,
	// and answerToken the token its answer to a join names a server with.
	serving, left, named, answerToken string
	// inChannel is whether Discord has the recorder in the call's
	// channel: it answered a join and no leave has gone out since.
	inChannel bool
	// refused is the tokens every voice server refuses, with refusedWith,
	// 4004 when zero. refusals is how many voice state updates had gone
	// out at each refusal, and atRefusal, when set, runs at each.
	refused     []string
	refusedWith int
	refusals    []int
	atRefusal   func()
	// atLeave, when set, runs as Discord receives each leave.
	atLeave func()
	// unreachable is the voice servers the recorder can't dial, and dials
	// counts the dials of each voice server.
	unreachable map[string]bool
	dials       map[string]int
	// whileOpening maps an endpoint to what happens once as an open there
	// completes.
	whileOpening map[string]func()
}

func newCallHarness(t *testing.T) *callHarness {
	t.Helper()
	h := &callHarness{t: t, s: recorderSession(t, &fakeManager{}), api: &channelAPI{t: t}, logs: captureLogs(t),
		opened: make(chan gatewayOpen, 64), pending: make(chan struct{}, 64), serving: oldEndpoint, answerToken: "token-2",
		unreachable: make(map[string]bool), dials: make(map[string]int), whileOpening: make(map[string]func())}
	h.s.dg.Client = &http.Client{Transport: h.api}
	h.s.voice = h.voiceManager(h.newGateway, nil)
	return h
}

// voiceManager builds the recorder session's voice manager over newGateway,
// dialing voice servers through dialer, with disgo logging as the
// recorder's session wires it.
func (h *callHarness) voiceManager(newGateway disgovoice.GatewayCreateFunc, dialer *websocket.Dialer) disgovoice.Manager {
	return h.s.voiceManager(h.updateVoiceState, callRecorder, newGateway, h.newLogger, dialer,
		disgovoice.WithLogger(utils.Logger),
		disgovoice.WithDaveSessionCreateFunc(session.CreateFunc()),
		disgovoice.WithDaveSessionLogger(slog.New(slog.DiscardHandler)),
	)
}

func (h *callHarness) newGateway(dave godave.Session, onEvent disgovoice.EventHandlerFunc, onClose disgovoice.CloseHandlerFunc, _ ...disgovoice.GatewayConfigOpt) disgovoice.Gateway {
	version := uint16(callDAVEVersion)
	if h.transportOnly.Load() {
		version = 0
	}
	return &answeringGateway{dave: dave, onEvent: onEvent, onClose: onClose, opened: h.opened, sent: h.sentOp,
		answers: h.answersAt, reachable: h.reachable, refusal: h.refusal, daveVersion: version, opening: h.opening,
		describeLate: h.describeLate.Load(), pending: h.pending}
}

// newLogger stands in for utils.NewLogger, which the recorder's session hands
// its voice manager. It returns a logger in slog's text format at a level of
// its own, starting at the bot log's, whose lines go to the bot log.
func (h *callHarness) newLogger() (*slog.Logger, *slog.LevelVar) {
	level := new(slog.LevelVar)
	level.Set(h.logLevel)
	return slog.New(slog.NewTextHandler(logfmtWriter{h.logs}, &slog.HandlerOptions{Level: level})), level
}

// answersAt reports whether the voice server at endpoint answers an open:
// any but the one the call left, unless none answers.
func (h *callHarness) answersAt(endpoint string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.silent.Load() && endpoint != h.left
}

// reachable counts a dial of the voice server at endpoint, and reports
// whether it reaches the server.
func (h *callHarness) reachable(endpoint string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dials[endpoint]++
	return !h.unreachable[endpoint]
}

// refusal is the close code the voice servers refuse token with, 0 when
// they don't.
func (h *callHarness) refusal(token string) int {
	h.mu.Lock()
	if !slices.Contains(h.refused, token) {
		h.mu.Unlock()
		return 0
	}
	h.refusals = append(h.refusals, len(h.updates))
	code, atRefusal := h.refusedWith, h.atRefusal
	h.mu.Unlock()
	if atRefusal != nil {
		atRefusal()
	}
	if code == 0 {
		code = disgovoice.GatewayCloseEventCodeAuthenticationFailed.Code
	}
	return code
}

// refuse has every voice server refuse tokens from now on, with 4004.
func (h *callHarness) refuse(tokens ...string) {
	h.refuseWith(0, tokens...)
}

// refuseWith has every voice server refuse tokens from now on, closing
// with code.
func (h *callHarness) refuseWith(code int, tokens ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refused, h.refusedWith = tokens, code
}

// answerWith has Discord name a voice server with token in its answers to a
// join from now on.
func (h *callHarness) answerWith(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.answerToken = token
}

// dialsOf is how many times the recorder has dialed the voice server at
// endpoint.
func (h *callHarness) dialsOf(endpoint string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dials[endpoint]
}

// opening runs what was to happen as an open at endpoint completes, once.
func (h *callHarness) opening(endpoint string) {
	h.mu.Lock()
	f := h.whileOpening[endpoint]
	delete(h.whileOpening, endpoint)
	h.mu.Unlock()
	if f != nil {
		f()
	}
}

// onOpening has f happen as the next open at endpoint completes, before
// the recorder's gateway hears it opened.
func (h *callHarness) onOpening(endpoint string, f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.whileOpening[endpoint] = f
}

// setReachable makes the voice server at endpoint one the recorder can
// dial, or one it can't.
func (h *callHarness) setReachable(endpoint string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unreachable[endpoint] = !ok
}

// leftChannel is Discord taking the recorder out of the channel, or
// answering its leave: its voice state with no channel. A leave's answer
// comes before Discord answers any later join.
func (h *callHarness) leftChannel() {
	h.mu.Lock()
	h.inChannel = false
	h.mu.Unlock()
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
	h.countFromNow()
	return conn
}

// countFromNow has the voice state updates sent count from now.
func (h *callHarness) countFromNow() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.updates = nil
}

// hangUp stops the recording on conn, as a member's stop does, and waits a
// minute out, so whatever the recorder still had in flight ends inside the
// test's bubble. A test under synctest defers it, failing or not.
func (h *callHarness) hangUp(conn Conn) {
	conn.Leave(context.Background())
	time.Sleep(time.Minute)
}

// updateVoiceState is the connection's voice state update: the recorder
// session's own, which Discord receives and answers while the recorder's
// gateway is up.
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
	if channelID == nil {
		h.mu.Lock()
		atLeave := h.atLeave
		h.mu.Unlock()
		if atLeave != nil {
			atLeave()
		}
		h.leftChannel()
		return nil
	}
	go h.answer(*channelID)
	return nil
}

// answer is Discord's answer to the recorder joining a channel, after
// answerDelay: its voice state, then, nameDelay later, the voice server now
// serving the call, unless stateOnly. Discord has the recorder in the
// channel already when it rejoins after a drop, and then names no server
// unless the call moved off the one it named last, as #786's smoke pass saw.
func (h *callHarness) answer(channelID snowflake.ID) {
	time.Sleep(time.Duration(h.answerDelay.Load()))
	h.mu.Lock()
	serving, token := h.serving, h.answerToken
	fresh := !h.inChannel || serving != h.named
	h.inChannel = true
	h.mu.Unlock()
	h.s.voice.HandleVoiceStateUpdate(recorderVoiceState(channelID))
	if fresh && !h.stateOnly.Load() {
		time.Sleep(time.Duration(h.nameDelay.Load()))
		h.name(token, serving)
	}
}

// name is Discord naming the voice server at endpoint for the call, with
// token.
func (h *callHarness) name(token, endpoint string) {
	h.mu.Lock()
	h.named = endpoint
	h.mu.Unlock()
	h.s.voice.HandleVoiceServerUpdate(voiceServer(token, endpoint))
}

// opensSince is every gateway open since the last one a test waited for or
// read, in order.
func (h *callHarness) opensSince() []gatewayOpen {
	var out []gatewayOpen
	for len(h.opened) > 0 {
		out = append(out, <-h.opened)
	}
	return out
}

// leftAndJoinedAgain reports whether the recorder has left the channel
// since the voice state updates last counted from, and joined after.
func (h *callHarness) leftAndJoinedAgain() bool {
	sent := h.sent()
	leave := slices.IndexFunc(sent, voiceUpdate.leaves)
	return leave != -1 && slices.ContainsFunc(sent[leave+1:], func(u voiceUpdate) bool { return !u.leaves() })
}

// identifiedAt reports whether one of opens identified at endpoint with
// token.
func identifiedAt(opens []gatewayOpen, endpoint, token string) bool {
	return slices.ContainsFunc(opens, func(o gatewayOpen) bool {
		return o.identified && o.state.Endpoint == endpoint && o.state.Token == token
	})
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

// disgo answers some closes with a fresh connection in the same channel:
// 4006, 4009 and 4011. The recorder rejoins the channel as it joined,
// self-muted and never self-deafened, and identifies at the voice server,
// since a resume gets no answer that opens audio. It never leaves, and
// nothing reaches Sentry. A close disgo would resume from goes the same way,
// on disgo's own gateway (drop_test.go).
func TestRecorderKeepsRecordingThroughACloseThatGetsAFreshConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"4006", &websocket.CloseError{Code: 4006}},
		{"4009", &websocket.CloseError{Code: 4009}},
		{"4011", &websocket.CloseError{Code: 4011}},
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

// A drop that leaves the call on its voice server has Discord answer the
// rejoin with the recorder's voice state alone, naming no server. After an
// abrupt drop, whose refused resume hands the recorder the close, the
// recorder identifies at the voice server Discord named last, with the
// token Discord gave for it, and hears the channel again. It never leaves,
// and the bot log says it reconnected with the token it held.
func TestRecorderIdentifiesWithTheTokenItHeldAfterADropOnTheSameVoiceServer(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		l := listen(nil)
		conn := h.record(l)
		defer h.hangUp(conn)

		go h.endConnection(errResumeRefused)
		time.Sleep(time.Minute)
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Errorf("gap events a minute after the drop = %v, want the gap started and ended", got)
		}
		if opens := h.opensSince(); !identifiedAt(opens, oldEndpoint, "token-2") {
			t.Errorf("gateway opens after the drop = %+v, want an identify at %s with token-2", opens, oldEndpoint)
		}
		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Errorf("voice state updates = %v, want the recorder never to leave", h.sent())
		}
		if infos := h.logs.messagesAt(slog.LevelInfo); !slices.Contains(infos, heldTokenMessage) {
			t.Errorf("bot log INFOs = %q, want %q", infos, heldTokenMessage)
		}
	})
}

// A voice server the recorder can't reach after a drop, as when its
// hostname won't resolve, refuses nothing. The recorder keeps dialing it
// past the 30 s disgo gives an open and stays in the channel, and once the
// server is reachable it identifies there within 10 s, with the token it
// held.
func TestRecorderKeepsDialingAVoiceServerItCantReachAfterADrop(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.setReachable(oldEndpoint, false)

		go h.endConnection(errResumeRefused)
		time.Sleep(time.Minute)
		synctest.Wait()
		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Fatalf("voice state updates a minute after the drop = %v, want the recorder never to leave", h.sent())
		}

		h.setReachable(oldEndpoint, true)
		time.Sleep(10 * time.Second)
		synctest.Wait()

		if opens := h.opensSince(); !identifiedAt(opens, oldEndpoint, "token-2") {
			t.Errorf("gateway opens 10 s after the server is reachable = %+v, want an identify at %s with token-2", opens, oldEndpoint)
		}
	})
}

// A voice server can close the identify before its Ready for a reason other
// than the token, such as 4014 when a moderator kicks the recorder, and
// Discord's voice state taking the recorder out can reach it after that
// close. The recorder doesn't take the close for a refused token: it never
// joins again after it, so the kick holds.
func TestRecorderStaysOutWhenItsVoiceServerClosesForAKick(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.refuseWith(disgovoice.GatewayCloseEventCodeDisconnected.Code, "token-2")
		h.mu.Lock()
		h.atRefusal = func() { time.AfterFunc(100*time.Millisecond, h.leftChannel) }
		h.mu.Unlock()

		go h.endConnection(errResumeRefused)
		time.Sleep(time.Minute)
		synctest.Wait()

		h.mu.Lock()
		refusals := slices.Clone(h.refusals)
		h.mu.Unlock()
		if len(refusals) == 0 {
			t.Fatal("the voice server never closed the identify")
		}
		if after := h.sent()[refusals[0]:]; slices.ContainsFunc(after, func(u voiceUpdate) bool { return !u.leaves() }) {
			t.Errorf("voice state updates after the 4014 = %v, want no join", after)
		}
	})
}

// The voice server can refuse the token the recorder held after a drop,
// closing the connection before its Ready. The recorder then leaves the
// channel and joins it again, and identifies with the token Discord's
// answer to that fresh join names. It hears the channel again, Sentry hears
// of no lost connection, and the bot log says the recorder left and joined
// again.
func TestRecorderLeavesAndJoinsAgainWhenTheVoiceServerRefusesTheTokenItHeld(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		l := listen(nil)
		conn := h.record(l)
		defer h.hangUp(conn)
		h.refuse("token-2")
		h.answerWith("token-4")

		go h.endConnection(errResumeRefused)
		time.Sleep(time.Minute)
		synctest.Wait()

		if !h.leftAndJoinedAgain() {
			t.Errorf("voice state updates after the drop = %v, want a leave and a join after it", h.sent())
		}
		if opens := h.opensSince(); !identifiedAt(opens, oldEndpoint, "token-4") {
			t.Errorf("gateway opens after the drop = %+v, want an identify at %s with token-4", opens, oldEndpoint)
		}
		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Errorf("gap events a minute after the drop = %v, want the gap started and ended", got)
		}
		if lost := slices.Contains(eventMessages(events.Events()), lostConnectionMessage); lost {
			t.Errorf("Sentry events = %v, want no %q", eventMessages(events.Events()), lostConnectionMessage)
		}
		if warns := h.logs.messagesAt(slog.LevelWarn); !slices.Contains(warns, refusedTokenMessage) {
			t.Errorf("bot log WARNs = %q, want %q", warns, refusedTokenMessage)
		}
	})
}

// The recorder leaves and joins again once for the token it held, and no
// more, so the call hears no string of leaves and joins. That holds when
// Discord names the voice server for the fresh join 400 ms after its voice
// state, past the recorder's wait for one, and when the voice server
// refuses the new token too.
func TestRecorderLeavesOnlyOnceForARefusedToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		late    bool
		refused []string
	}{
		{"server named late", true, []string{"token-2"}},
		{"new token refused too", false, []string{"token-2", "token-4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordSentry(t)
			synctest.Test(t, func(t *testing.T) {
				h := newCallHarness(t)
				conn := h.start()
				defer h.hangUp(conn)
				h.refuse(tc.refused...)
				h.answerWith("token-4")
				if tc.late {
					h.nameDelay.Store(int64(400 * time.Millisecond))
				}

				go h.endConnection(errResumeRefused)
				time.Sleep(time.Minute)
				synctest.Wait()

				if leaves := slices.DeleteFunc(h.sent(), func(u voiceUpdate) bool { return !u.leaves() }); len(leaves) != 1 {
					t.Errorf("voice state updates in the minute after the drop = %v, want one leave", h.sent())
				}
			})
		})
	}
}

// The recording runs on while the recorder leaves the channel to join it
// again, so the recorder's connection has counted that leave as its own by
// the time Discord hears it.
func TestRecorderCountsItsOwnLeaveBeforeDiscordHearsIt(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.refuse("token-2")
		h.answerWith("token-4")
		var counted []int
		h.mu.Lock()
		h.atLeave = func() { counted = append(counted, conn.OwnLeaves()) }
		h.mu.Unlock()

		go h.endConnection(errResumeRefused)
		time.Sleep(time.Minute)
		synctest.Wait()

		h.mu.Lock()
		h.atLeave = nil
		h.mu.Unlock()
		if !slices.Equal(counted, []int{1}) {
			t.Errorf("own leaves counted as Discord received each leave = %v, want 1", counted)
		}
	})
}

// The recorder's own gateway can be down when its voice connection drops,
// so the rejoin can't go out. The recorder waits for the gateway and
// rejoins once it is back: the gap ends, and the recording keeps running,
// since the recorder never left.
func TestRecorderRejoinsOnceItsGatewayIsBack(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		l := listen(nil)
		conn := h.record(l)
		defer h.hangUp(conn)
		h.down.Store(true)
		go h.closeCall(4006)
		time.Sleep(2 * time.Minute)
		synctest.Wait()

		h.down.Store(false)
		time.Sleep(5 * time.Second)
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Errorf("gap events 5 s after the gateway is back = %v, want the gap started and ended", got)
		}
		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Errorf("voice state updates = %v, want the recorder never to leave", h.sent())
		}
	})
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

// Discord can answer the rejoin after a 4022 with the recorder's voice
// state alone and name the server the call moved to only after, as when its
// voice server update with no endpoint comes first. The call has left the
// server it was on, so the recorder never dials that one.
func TestRecorderNeverDialsTheServerAMovedCallLeft(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.stateOnly.Store(true)
		before := h.dialsOf(oldEndpoint)

		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(time.Second)
		h.name("token-3", newEndpoint)
		time.Sleep(time.Minute)
		synctest.Wait()

		if n := h.dialsOf(oldEndpoint) - before; n != 0 {
			t.Errorf("dials of %s, the server the call left, in the minute after the 4022 = %d, want none", oldEndpoint, n)
		}
	})
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

// A recorder still trying to reach the voice server its call moved to has
// lost nothing yet, so the bot log has no ERROR for it. disgo's ERROR, once
// its 30 s wait for the open runs out, reaches the bot log at WARN.
func TestRecorderLogsNoErrorWhileItTriesToReachTheServerItsCallMovedTo(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.setReachable(newEndpoint, false)

		closed := time.Now()
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(time.Minute)
		synctest.Wait()

		if errs := h.logs.errorsFrom(closed); len(errs) != 0 {
			t.Errorf("bot log ERRORs in the minute after the close = %q, want none", messages(errs))
		}
		const openFailed = "error opening voice gateway"
		if warns := h.logs.messagesAt(slog.LevelWarn); !slices.Contains(warns, openFailed) {
			t.Errorf("bot log WARNs = %q, want disgo's %q", warns, openFailed)
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

// Discord can name a later voice server for the call just as the
// recorder's connection to the one it named before completes: as the
// gateway opens, or between the voice server's Ready and its session
// description. The call isn't there anymore, so the recorder doesn't stay
// there: it connects at the server named last, with the token Discord gave
// for it, through a gateway that identifies.
func TestRecorderMovesOnFromAServerDiscordReplacedAsItConnected(t *testing.T) {
	for _, tc := range []struct {
		name         string
		describeLate bool
	}{
		{"named as the gateway opens", false},
		{"named before the session description", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordSentry(t)
			synctest.Test(t, func(t *testing.T) {
				h := newCallHarness(t)
				conn := h.start()
				defer h.hangUp(conn)
				h.describeLate.Store(tc.describeLate)
				h.onOpening(newEndpoint, func() { h.name("token-3", laterEndpoint) })

				go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
				time.Sleep(time.Second)
				synctest.Wait()

				var last gatewayOpen
				for len(h.opened) > 0 {
					last = <-h.opened
				}
				if last.state.Endpoint != laterEndpoint || last.state.Token != "token-3" || !last.identified {
					t.Errorf("last gateway open at %s with token %s and identified=%v, want an identify at %s with token-3",
						last.state.Endpoint, last.state.Token, last.identified, laterEndpoint)
				}
			})
		})
	}
}

// The recorder can close a gateway on purpose, as when Discord names a later
// voice server just as it connects, and the gateway can still hand its close
// handler a close: disgo's own heartbeat check can fire on a gateway closing
// under it. The recorder has left that gateway behind, so its close changes
// nothing, and the recording hears no new gap. Every voice server is out of
// reach by then, so a gap would last.
func TestRecorderIgnoresACloseFromAGatewayItLeftBehind(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		l := listen(nil)
		conn := h.record(l)
		defer h.hangUp(conn)
		h.onOpening(newEndpoint, func() { h.name("token-3", laterEndpoint) })
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(time.Second)
		synctest.Wait()
		var replaced *answeringGateway
		for len(h.opened) > 0 {
			if o := <-h.opened; o.state.Endpoint == newEndpoint {
				replaced = o.gateway
			}
		}
		before := l.gapEvents()
		if replaced == nil || len(before)%2 != 0 {
			t.Fatalf("after the move: gateway at %s = %v, gap events = %v, want one replaced and the recorder hearing", newEndpoint, replaced, before)
		}
		for _, endpoint := range []string{oldEndpoint, newEndpoint, laterEndpoint} {
			h.setReachable(endpoint, false)
		}

		go replaced.onClose(replaced, errResumeRefused)
		time.Sleep(time.Minute)
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, before) {
			t.Errorf("gap events after the left-behind gateway's close = %v, want them as before, %v", got, before)
		}
	})
}

// A gap that reaches 10 s reaches Sentry once, naming the recorder, the
// channel and the voice server the recorder is trying to reach, while the
// recording still runs. The bot log has it at ERROR.
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
		if errs := h.logs.messagesAt(slog.LevelError); !slices.Contains(errs, gapMessage) {
			t.Errorf("bot log ERRORs = %q, want %q", errs, gapMessage)
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

// A voice server can stop answering without closing the connection, as when
// the network between it and the recorder stops passing traffic. The
// recorder counts the connection dropped once the server's heartbeat answer
// is overdue, a few seconds past the heartbeat interval, and rejoins. The
// recording's gap starts at the last message the server sent, here one
// naming a speaker after the last heartbeat answer. The server is out of
// reach from then, so the gap lasts.
func TestRecorderStartsTheGapOfASilentVoiceServerAtItsLastMessage(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		l := listen(nil)
		conn := h.record(l)
		defer h.hangUp(conn)
		server := h.connected
		server.hello(time.Second)
		for range 30 {
			time.Sleep(time.Second)
			server.answerHeartbeat()
		}
		synctest.Wait()
		if got := l.gapEvents(); len(got) != 0 {
			t.Fatalf("gap events while the server answers = %v, want none", got)
		}
		time.Sleep(300 * time.Millisecond)
		server.speaking()
		last := time.Now()
		h.setReachable(oldEndpoint, false)

		time.Sleep(time.Second + 5*time.Second)
		synctest.Wait()

		starts := l.gapStarts()
		if len(starts) == 0 {
			t.Fatal("no gap 6 s after the server's last message, want one")
		}
		if !starts[0].Equal(last) {
			t.Errorf("gap started %v after the server's last message, want at it", starts[0].Sub(last))
		}
	})
}

// A 4022 has the recorder ask Discord's API whether the channel is still
// there, and the answer can take the lookup's full 5 s. The recorder could
// not hear the call from the close on, so the gap the recording hears
// starts at the close. The server the call moved to is out of reach for
// now, so the gap lasts.
func TestRecorderStartsTheGapOfAMovedCallAtTheClose(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		l := listen(nil)
		conn := h.record(l)
		defer h.hangUp(conn)
		h.api.during = func() { time.Sleep(5 * time.Second) }
		h.setReachable(newEndpoint, false)

		closed := time.Now()
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(time.Minute)
		synctest.Wait()

		if got := l.gapStarts(); len(got) == 0 || !got[0].Equal(closed) {
			t.Errorf("gap starts = %v, want the first at the close, %v", got, closed)
		}
	})
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

// Discord can take the recorder out of the channel while the recorder's own
// gateway is down after its voice connection dropped: a session that
// resumes hears what it missed before it counts as up again. The recorder
// stays out once its gateway is back, sending no join, so the recording
// ends as Discord taking the recorder out ends one.
func TestRecorderTakenOutWhileItsGatewayIsDownStaysOut(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		defer h.hangUp(conn)
		h.down.Store(true)
		go h.closeCall(4006)
		time.Sleep(time.Minute)
		synctest.Wait()
		h.leftChannel()
		taken := len(h.sent())

		h.down.Store(false)
		time.Sleep(time.Minute)
		synctest.Wait()

		if after := h.sent()[taken:]; slices.ContainsFunc(after, func(u voiceUpdate) bool { return !u.leaves() }) {
			t.Errorf("voice state updates after Discord took the recorder out = %v, want no join", after)
		}
	})
}

// With the recorder's own gateway down past the 10-minute gap limit, the
// recorder goes on waiting for it, and neither leaves nor reports a lost
// connection on its own. The recording ends at the limit, and its stop is
// the last voice state update the recorder sends.
func TestRecorderWaitsForItsGatewayUntilTheRecordingStops(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		h.down.Store(true)
		go h.closeCall(4022)
		time.Sleep(11 * time.Minute)
		synctest.Wait()
		if left := slices.ContainsFunc(h.sent(), voiceUpdate.leaves); left {
			t.Fatalf("voice state updates 11 min after the close = %v, want no leave", h.sent())
		}

		conn.Leave(context.Background())
		time.Sleep(time.Minute)
		synctest.Wait()

		sent := h.sent()
		if stop := slices.IndexFunc(sent, voiceUpdate.leaves); stop != len(sent)-1 {
			t.Errorf("voice state updates = %v, want the stop's leave last", sent)
		}
		if lost := slices.Contains(eventMessages(events.Events()), lostConnectionMessage); lost {
			t.Errorf("Sentry events = %v, want no %q", eventMessages(events.Events()), lostConnectionMessage)
		}
	})
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

		stopped := time.Now()
		conn.Leave(context.Background())
		time.Sleep(time.Minute)
		synctest.Wait()

		assertStoppedQuietly(t, h, events, stopped)
	})
}

// A member can stop the recording while a rejoin after a move tries a voice
// server the recorder can't reach yet. Here Discord named it while the
// recorder asked whether the channel is still there, then answered the
// rejoin with the voice state alone. The stop is as quiet as any other,
// also once disgo's 30 s wait has run out.
func TestRecorderStoppedWhileARejoinTriesAServerItCantReachLogsNoError(t *testing.T) {
	events := recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		conn := h.start()
		h.api.during = func() { h.name("token-3", newEndpoint) }
		h.stateOnly.Store(true)
		h.setReachable(newEndpoint, false)
		go h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(5 * time.Second)
		synctest.Wait()

		stopped := time.Now()
		conn.Leave(context.Background())
		time.Sleep(time.Minute)
		synctest.Wait()

		assertStoppedQuietly(t, h, events, stopped)
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
		var stopped time.Time
		h.api.during = func() {
			stopped = time.Now()
			conn.Leave(context.Background())
		}

		h.closeCall(disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		time.Sleep(time.Minute)
		synctest.Wait()

		assertStoppedQuietly(t, h, events, stopped)
	})
}

// assertStoppedQuietly fails unless the stop's leave is the last voice
// state update the recorder sent, Sentry heard nothing, and the bot log got
// no ERROR from the stop at stopped on.
func assertStoppedQuietly(t *testing.T, h *callHarness, events *sentryTransport, stopped time.Time) {
	t.Helper()
	sent := h.sent()
	if stop := slices.IndexFunc(sent, voiceUpdate.leaves); stop == -1 || stop != len(sent)-1 {
		t.Errorf("voice state updates = %v, want the stop's leave last", sent)
	}
	if n := len(events.Events()); n != 0 {
		t.Errorf("Sentry events = %d, want none for a recording a member stopped", n)
	}
	if errs := h.logs.errorsFrom(stopped); len(errs) != 0 {
		t.Errorf("bot log ERRORs after the stop = %q, want none for a recording a member stopped", messages(errs))
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

// localVoiceServer is a voice server on localhost, over TLS. play plays the
// nth connection it accepts, counting from 1. Once play returns, the server
// reads what the recorder sends until the connection closes.
type localVoiceServer struct {
	srv *httptest.Server
}

func newLocalVoiceServer(t *testing.T, play func(n int32, conn *websocket.Conn)) *localVoiceServer {
	t.Helper()
	var accepted atomic.Int32
	upgrader := websocket.Upgrader{}
	s := &localVoiceServer{srv: httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		play(accepted.Add(1), conn)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))}
	t.Cleanup(s.srv.Close)
	return s
}

// endpoint is the server's address, as Discord names a voice server.
func (s *localVoiceServer) endpoint() string { return s.srv.Listener.Addr().String() }

// dialer is a voice gateway dialer that trusts the server's certificate.
func (s *localVoiceServer) dialer() *websocket.Dialer {
	roots := x509.NewCertPool()
	roots.AddCert(s.srv.Certificate())
	return &websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: roots}}
}

// quietVoiceServer is a voice server that accepts each connection and says
// nothing on it, as one that hasn't greeted the recorder yet. connected
// closes once it has accepted one.
type quietVoiceServer struct {
	*localVoiceServer
	connected chan struct{}
}

func newQuietVoiceServer(t *testing.T) *quietVoiceServer {
	t.Helper()
	s := &quietVoiceServer{connected: make(chan struct{})}
	s.localVoiceServer = newLocalVoiceServer(t, func(n int32, _ *websocket.Conn) {
		if n == 1 {
			close(s.connected)
		}
	})
	return s
}

// useDisgoGateway has the recorder session's voice connections run over
// disgo's own voice gateway from now on, dialing voice servers through
// dialer.
func (h *callHarness) useDisgoGateway(dialer *websocket.Dialer) {
	h.s.voice = h.voiceManager(disgovoice.NewGateway, dialer)
}

// joinInBackground has the recorder session join the channel, and returns
// without waiting for the join to complete. The join ends with the test.
func (h *callHarness) joinInBackground() {
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, _ = h.s.Join(ctx, callGuild.String(), callChannel.String())
	}()
	h.t.Cleanup(func() { cancel(); <-joined })
}

// With the bot log at DEBUG, a voice connection the recorder hasn't closed
// logs there what #735's move smoke check reads, word for word: disgo
// opening the connection, and opening its voice gateway. disgo's gateway logs its identify through
// the same logger, but only after a voice server's Hello. Hello starts a
// heartbeat that disgo's close races under -race, so this server never
// greets.
func TestRecorderVoiceConnectionLogsReachTheBotLogAtDebug(t *testing.T) {
	server := newQuietVoiceServer(t)
	h := newCallHarness(t)
	h.logLevel = slog.LevelDebug
	h.serving = server.endpoint()
	h.useDisgoGateway(server.dialer())
	h.joinInBackground()

	select {
	case <-server.connected:
	case <-time.After(waitFor):
		t.Fatal("the voice gateway never connected")
	}

	debug := h.logs.messagesAt(slog.LevelDebug)
	for _, want := range []string{"opening voice conn", "opening voice gateway connection"} {
		if !slices.Contains(debug, want) {
			t.Errorf("bot log DEBUG records = %q, want %q", debug, want)
		}
	}
}

// unreachableDialer is a voice gateway dialer that can't reach any voice
// server, as when the host's resolver doesn't know its hostname. dials
// hears each dial.
func unreachableDialer(dials chan<- struct{}) *websocket.Dialer {
	return &websocket.Dialer{NetDialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
		dials <- struct{}{}
		return nil, fmt.Errorf("dial tcp: lookup %s: no such host", addr)
	}}
}

// joinUnreachable has the recorder session join the channel through disgo's
// own voice gateway, which can't reach the voice server, and returns once
// disgo has dialed it twice. disgo logs a failed dial before it dials again.
// The join is still under way, so the connection is the recorder's. The
// join ends with the test.
func (h *callHarness) joinUnreachable() {
	dials := make(chan struct{}, 64)
	h.useDisgoGateway(unreachableDialer(dials))
	h.joinInBackground()
	<-dials
	<-dials
}

// The recorder goes on dialing a voice server it can't reach yet, and has
// lost nothing while it does. disgo's ERRORs for each failed dial reach the
// bot log at WARN.
func TestRecorderLogsAFailedDialOfTheVoiceServerAtWarn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)

		h.joinUnreachable()

		if errs := h.logs.messagesAt(slog.LevelError); len(errs) != 0 {
			t.Errorf("bot log ERRORs = %q, want none", errs)
		}
		warns := h.logs.messagesAt(slog.LevelWarn)
		for _, want := range []string{"error connecting to the voice gateway", "failed to reconnect voice gateway"} {
			if !slices.Contains(warns, want) {
				t.Errorf("bot log WARNs = %q, want disgo's %q", warns, want)
			}
		}
	})
}

// terminatingVoiceServer is a voice server that connects the recorder on
// the first connection it accepts, as Discord's does, and then ends that
// call with 4022. Each later connection it accepts and says nothing on.
type terminatingVoiceServer struct {
	*localVoiceServer
	udp *fakeVoiceServer
}

func newTerminatingVoiceServer(t *testing.T) *terminatingVoiceServer {
	t.Helper()
	s := &terminatingVoiceServer{udp: newFakeVoiceServer(t)}
	s.localVoiceServer = newLocalVoiceServer(t, func(n int32, conn *websocket.Conn) {
		if n == 1 {
			s.connectThenTerminate(t, conn)
		}
	})
	return s
}

// connectThenTerminate plays Discord's voice server for one connection, from
// Hello to the session description that completes it, with no DAVE. Once
// the recorder's first heartbeat arrives, it ends the call with 4022. disgo
// starts its heartbeat at Hello, and its close races that start under -race
// until a heartbeat has gone out.
func (s *terminatingVoiceServer) connectThenTerminate(t *testing.T, conn *websocket.Conn) {
	send := func(op disgovoice.Opcode, d disgovoice.GatewayMessageData) {
		if err := conn.WriteJSON(disgovoice.GatewayMessage{Op: op, D: d}); err != nil {
			t.Errorf("voice server sending op %d: %v", op, err)
		}
	}
	send(disgovoice.OpcodeHello, disgovoice.GatewayMessageDataHello{HeartbeatInterval: 50})
	awaitOp(t, conn, disgovoice.OpcodeIdentify)
	go s.udp.answerDiscovery(t)
	send(disgovoice.OpcodeReady, disgovoice.GatewayMessageDataReady{SSRC: 42, IP: "127.0.0.1", Port: s.udp.port(),
		Modes: []disgovoice.EncryptionMode{disgovoice.EncryptionModeAEADAES256GCMRTPSize}})
	awaitOp(t, conn, disgovoice.OpcodeSelectProtocol)
	send(disgovoice.OpcodeSessionDescription, disgovoice.GatewayMessageDataSessionDescription{
		Mode: disgovoice.EncryptionModeAEADAES256GCMRTPSize, SecretKey: make([]byte, 32)})
	awaitOp(t, conn, disgovoice.OpcodeHeartbeat)
	terminated := websocket.FormatCloseMessage(disgovoice.GatewayCloseEventCodeCallTerminated.Code, "Call terminated")
	if err := conn.WriteMessage(websocket.CloseMessage, terminated); err != nil {
		t.Errorf("voice server sending 4022: %v", err)
	}
}

// awaitOp reads what the recorder sends on conn until a message with op.
func awaitOp(t *testing.T, conn *websocket.Conn, op disgovoice.Opcode) {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("voice server waiting for op %d: %v", op, err)
			return
		}
		var m struct {
			Op disgovoice.Opcode `json:"op"`
		}
		if mt == websocket.TextMessage && json.Unmarshal(data, &m) == nil && m.Op == op {
			return
		}
	}
}

// The recorder rejoins a call its voice server ended with 4022, and has lost
// nothing yet. disgo's ERROR for the close reaches the bot log at WARN.
func TestRecorderLogsACallItsVoiceServerEndedAtWarn(t *testing.T) {
	recordSentry(t)
	server := newTerminatingVoiceServer(t)
	h := newCallHarness(t)
	h.serving = server.endpoint()
	h.useDisgoGateway(server.dialer())
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	conn, err := h.s.Join(ctx, callGuild.String(), callChannel.String())
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	defer conn.Leave(context.Background())
	h.countFromNow()

	if !within(time.Now(), waitFor, h.rejoined) {
		t.Fatal("the recorder never rejoined after the 4022")
	}

	if errs := h.logs.messagesAt(slog.LevelError); len(errs) != 0 {
		t.Errorf("bot log ERRORs = %q, want none", errs)
	}
	const closeReceived = "voice gateway close received"
	if warns := h.logs.messagesAt(slog.LevelWarn); !slices.Contains(warns, closeReceived) {
		t.Errorf("bot log WARNs = %q, want disgo's %q", warns, closeReceived)
	}
}

// With the bot log at ERROR, as LOG_LEVEL=ERROR sets it, disgo's ERRORs are
// WARN lines it leaves out.
func TestRecorderLeavesAFailedDialOutOfABotLogAtError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.logLevel = slog.LevelError

		h.joinUnreachable()

		if warns := h.logs.messagesAt(slog.LevelWarn); len(warns) != 0 {
			t.Errorf("bot log WARNs = %q, want none in a bot log at ERROR", warns)
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
