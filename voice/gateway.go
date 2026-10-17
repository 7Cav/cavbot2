package voice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgo/gateway"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"github.com/thomas-vilte/dave-go/session"
)

// recorderIntents are the only intents a recorder's session asks for (ADR
// 0014): the guilds it is in and who is in which voice channel. Neither is
// privileged, so a recorder application needs no privileged intent turned
// on, and Discord would close a session asking for one it lacks. The voice
// states carry the recorder's own, which disgo needs to connect.
const recorderIntents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates

// closeTimeout bounds the leave a failed join or a shutdown sends.
const closeTimeout = 5 * time.Second

// lookupTimeout bounds the channel lookup a 4022 makes before the rejoin.
const lookupTimeout = 5 * time.Second

// answerWait is how long a join's answer has to name a voice server after
// the recorder's voice state. Discord sends the two together when it names
// one, and discordgo hands each event to a goroutine of its own.
const answerWait = 250 * time.Millisecond

// OpenGateway is the production Opener: a discordgo session for the recorder
// account, open once READY has named it. A session that opened without
// reaching READY is closed again for good, so a failed recorder holds no
// connection and its token logs in no more until the bot restarts.
// The session is watched, so a recorder that drops off the gateway and stays
// off reaches Sentry while the bot runs, and one whose token Discord refuses
// stops logging in.
func OpenGateway(recorder int, token string) (GatewaySession, error) {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	dg.LogLevel = utils.DiscordgoLogLevel()
	dg.Identify.Intents = recorderIntents
	s := &discordgoSession{dg: dg}
	// Watched from before it opens, so the watch reads every close Discord
	// sends the session. A failed open is a close on purpose, and sends
	// nothing beyond its own report.
	s.watchGateway(recorder, utils.GatewayOfflineAfter)
	if err := utils.OpenSession(dg); err != nil {
		_ = s.watch.CloseSession()
		return nil, err
	}
	userID, err := snowflake.Parse(dg.State.User.ID)
	if err != nil {
		_ = s.watch.CloseSession()
		return nil, err
	}
	s.named(dg.State.User.ID)
	// Closures rather than method values here and below: the gate's error
	// data check follows a closure, and not a method value.
	updateVoiceState := func(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
		return s.updateVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
	}
	s.voice = s.voiceManager(updateVoiceState, userID, disgovoice.NewGateway,
		disgovoice.WithLogger(utils.Logger),
		disgovoice.WithDaveSessionCreateFunc(session.CreateFunc()),
		disgovoice.WithDaveSessionLogger(utils.Logger),
	)
	// Added after READY, once the manager exists: no voice event reaches a
	// recorder before its first join.
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.VoiceStateUpdate) { s.onVoiceStateUpdate(e) })
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.VoiceServerUpdate) { s.onVoiceServerUpdate(e) })
	return s, nil
}

// discordgoSession is a recorder's discordgo session, with the disgo voice
// manager its joins go through. discordgo keeps the gateway and sends the
// voice state updates, and disgo runs the voice gateway, UDP and DAVE.
type discordgoSession struct {
	dg     *discordgo.Session
	userID string
	voice  disgovoice.Manager

	// watch reports a gateway outage that lasts.
	watch *utils.GatewayWatch
}

// voiceManager builds the recorder's disgo voice manager, which every join
// goes through. update sends the recorder's voice state updates, newGateway
// builds each voice gateway, disgo's NewGateway outside tests, and opts
// configure the rest.
func (s *discordgoSession) voiceManager(update disgovoice.StateUpdateFunc, userID snowflake.ID, newGateway disgovoice.GatewayCreateFunc, opts ...disgovoice.ManagerConfigOpt) disgovoice.Manager {
	return disgovoice.NewManager(update, userID, append(opts, disgovoice.WithConnCreateFunc(s.connCreate(newGateway)))...)
}

// connCreate builds each of the recorder's voice connections. Each is
// disgo's own, held by a recorderConn.
func (s *discordgoSession) connCreate(newGateway disgovoice.GatewayCreateFunc) disgovoice.ConnCreateFunc {
	return func(guildID, userID snowflake.ID, update disgovoice.StateUpdateFunc, remove func(), opts ...disgovoice.ConnConfigOpt) disgovoice.Conn {
		c := &recorderConn{s: s, guildID: guildID, userID: userID, update: update}
		updateVoiceState := func(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
			return c.updateVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
		}
		c.Conn = disgovoice.NewConn(guildID, userID, updateVoiceState, remove, append(opts, c.connOpts(newGateway)...)...)
		return c
	}
}

// connOpts configures the voice connection c holds: the recorder's own UDP
// connection, and a voice gateway whose close handler is watched for a
// connection lost mid-recording or a call that moved. newGateway builds the
// gateway, disgo's NewGateway outside tests.
func (c *recorderConn) connOpts(newGateway disgovoice.GatewayCreateFunc) []disgovoice.ConnConfigOpt {
	return []disgovoice.ConnConfigOpt{
		disgovoice.WithUDPConnCreateFunc(newUDPConn),
		disgovoice.WithConnGatewayCreateFunc(func(dave godave.Session, onEvent disgovoice.EventHandlerFunc, onClose disgovoice.CloseHandlerFunc, opts ...disgovoice.GatewayConfigOpt) disgovoice.Gateway {
			g := &renewableGateway{}
			g.build = func() disgovoice.Gateway {
				return newGateway(dave, onEvent, c.watchClose(g, onClose), opts...)
			}
			g.gw = g.build()
			c.gateway = g
			return g
		}),
	}
}

// recorderConn is disgo's voice connection to one guild's calls, as the
// recorder's voice manager holds it. disgo connects only once it has both
// the recorder's voice state and a voice server Discord named after the
// join started, and throws away any server named before. Discord can name
// the server a call moved to before the rejoin starts, then answer the
// rejoin with the voice state alone. So the connection keeps the last
// server Discord named, and hands it to disgo again when disgo threw it
// away. Once the recorder closes the connection, a rejoin disgo started
// before sends nothing more.
type recorderConn struct {
	disgovoice.Conn
	s               *discordgoSession
	guildID, userID snowflake.ID
	// update is the recorder session's voice state update.
	update  disgovoice.StateUpdateFunc
	gateway *renewableGateway

	// closed is set once the recorder closes the connection, for a stop, a
	// failed join or a shutdown. leaveSent is set once that close's leave
	// has gone out. gaveUp is set when disgo takes the recorder out on its
	// own, because it couldn't keep the connection.
	closed    atomic.Bool
	leaveSent atomic.Bool
	gaveUp    atomic.Bool

	mu sync.Mutex
	// lastServer is the last voice server Discord named for the call.
	// droppedServer is a server disgo threw away and still needs. Discord
	// named it after the gateway last opened, but before the join under
	// way started.
	lastServer, droppedServer *gateway.EventVoiceServerUpdate
}

// errConnClosed is what disgo gets for a voice state update on a connection
// the recorder closed.
var errConnClosed = errors.New("voice: the recorder closed the connection")

// Close takes the recorder out of the call and closes the connection. disgo
// may still be waiting on a rejoin. That rejoin sends nothing after this
// leave.
func (c *recorderConn) Close(ctx context.Context) {
	c.closed.Store(true)
	c.Conn.Close(ctx)
}

// updateVoiceState is the voice state update disgo sends for the
// connection. A join is disgo starting to connect, and a leave the
// recorder didn't ask for is disgo giving up. Once the recorder closes the
// connection, only the leave of that close goes out. A leave takes the
// recorder out of whichever voice channel of the guild it is in, so a later
// one could take it out of a recording started since.
func (c *recorderConn) updateVoiceState(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
	switch {
	case c.closed.Load():
		if channelID != nil || c.leaveSent.Swap(true) {
			return errConnClosed
		}
	case channelID == nil:
		c.gaveUp.Store(true)
	default:
		c.joinStarted()
	}
	return c.update(ctx, guildID, channelID, selfMute, selfDeaf)
}

// joinStarted notes disgo starting a join, which throws away the voice
// server disgo held. A server Discord named since the gateway last opened
// is the one the call moved to, so disgo dropped it.
func (c *recorderConn) joinStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.droppedServer = nil
	if c.lastServer != nil && !c.gateway.openedAt(*c.lastServer) {
		c.droppedServer = c.lastServer
	}
}

// HandleVoiceServerUpdate hands disgo a voice server Discord named, and
// keeps it. disgo keeps it too, unless a join starts after.
func (c *recorderConn) HandleVoiceServerUpdate(e gateway.EventVoiceServerUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.GuildID == c.guildID && e.Endpoint != nil {
		c.lastServer, c.droppedServer = &e, nil
	}
	c.Conn.HandleVoiceServerUpdate(e)
}

// HandleVoiceStateUpdate hands disgo a voice state. When it is Discord's
// answer to a join and disgo dropped the voice server, the answer may still
// name another. If it names none within answerWait, disgo gets the dropped
// server again and connects there.
func (c *recorderConn) HandleVoiceStateUpdate(e gateway.EventVoiceStateUpdate) {
	c.Conn.HandleVoiceStateUpdate(e)
	if e.GuildID != c.guildID || e.UserID != c.userID || e.ChannelID == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if dropped := c.droppedServer; dropped != nil {
		time.AfterFunc(answerWait, func() { c.redeliver(dropped) })
	}
}

// redeliver hands disgo the voice server it dropped, unless Discord has
// named another since or the recorder has closed the connection.
func (c *recorderConn) redeliver(dropped *gateway.EventVoiceServerUpdate) {
	defer utils.RecoverPanic("recorder-voice-redeliver", "recorder", c.s.userID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.droppedServer != dropped || c.closed.Load() {
		return
	}
	c.droppedServer = nil
	c.Conn.HandleVoiceServerUpdate(*dropped)
}

// renewableGateway is the voice gateway disgo's connection holds. It hands
// every call to a disgo gateway, which renew swaps for a new one. disgo's
// gateway keeps what it resumes with through a close, and resumes when it
// next opens, since disgo's fresh connection keeps the voice session ID. A
// voice server answers that resume with nothing that opens audio, and one
// the call moved to never had the session. A new gateway identifies instead.
type renewableGateway struct {
	build func() disgovoice.Gateway

	mu sync.Mutex
	gw disgovoice.Gateway
	// state is the voice state the gateway last opened with: the call's
	// channel, and the voice server and token it was connected with.
	state disgovoice.State
	// opened is whether the gateway has opened since its last renew.
	opened bool
}

func (g *renewableGateway) current() disgovoice.Gateway {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gw
}

// renew swaps the gateway, closed by now, for a new one to open next.
func (g *renewableGateway) renew() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gw = g.build()
	g.opened = false
}

// openedAt reports whether the gateway last opened at the voice server e
// names, with its token.
func (g *renewableGateway) openedAt(e gateway.EventVoiceServerUpdate) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state.Endpoint == *e.Endpoint && g.state.Token == e.Token
}

// openedSinceRenew reports whether the new gateway has opened.
func (g *renewableGateway) openedSinceRenew() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.opened
}

func (g *renewableGateway) SSRC() uint32 { return g.current().SSRC() }

func (g *renewableGateway) Open(ctx context.Context, state disgovoice.State) error {
	g.mu.Lock()
	g.state = state
	g.opened = true
	gw := g.gw
	g.mu.Unlock()
	return gw.Open(ctx, state)
}

// channel is the channel of the call the gateway last opened for.
func (g *renewableGateway) channel() snowflake.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state.ChannelID
}

// reopen closes the gateway and opens a new one with the voice state it
// last opened with. The new gateway identifies, so the voice server starts
// the connection afresh: a new UDP socket, and a session description that
// starts DAVE over. The close is on purpose, so disgo's close handler hears
// nothing of it. It carries the code disgo's gateway closes with before it
// reconnects, not 1000: on Discord's main gateway, a 1000 ends the session
// the new connection identifies with.
func (g *renewableGateway) reopen(ctx context.Context) error {
	g.mu.Lock()
	old, state := g.gw, g.state
	g.gw = g.build()
	g.mu.Unlock()
	old.CloseWithCode(websocket.CloseServiceRestart, "starting DAVE over")
	return g.Open(ctx, state)
}

func (g *renewableGateway) Close() { g.current().Close() }

func (g *renewableGateway) CloseWithCode(code int, message string) {
	g.current().CloseWithCode(code, message)
}

func (g *renewableGateway) Status() disgovoice.Status { return g.current().Status() }

func (g *renewableGateway) Send(ctx context.Context, op disgovoice.Opcode, d disgovoice.GatewayMessageData) error {
	return g.current().Send(ctx, op, d)
}

func (g *renewableGateway) Latency() time.Duration { return g.current().Latency() }

// errCallMoved is what disgo's close handler gets in place of a 4022. disgo
// answers a close that isn't a websocket close with a fresh connection in
// the same channel, where a 4022 would take the recorder out.
var errCallMoved = errors.New("voice: the call moved to another voice server")

// lostConnectionMessage is what Sentry hears when disgo takes the recorder
// out of the channel on its own.
const lostConnectionMessage = "Voice connection lost mid-recording"

// watchClose wraps disgo's handler for a voice gateway that closed and
// couldn't resume. disgo opens a fresh connection when the close allows it,
// and a voice reconnect ends there. That connection goes through a new
// gateway, which identifies. The old one would resume the voice session,
// which the rejoin keeps, and a resume gets no answer that opens audio.
// Discord ends a call with 4022, "Call terminated", when its channel
// is deleted or the call moves to another voice server, as a region change
// makes it do. A call that moved gets a fresh connection too, unless its
// channel is gone. Otherwise, or when the fresh connection fails, disgo
// takes the recorder out of the channel itself: the voice connection is
// lost mid-recording, and Sentry hears of it with the close that started it.
// A connection the recorder closed meanwhile ended with its recording, and
// lost nothing.
func (c *recorderConn) watchClose(gw *renewableGateway, onClose disgovoice.CloseHandlerFunc) disgovoice.CloseHandlerFunc {
	return func(g disgovoice.Gateway, err error) {
		defer utils.RecoverPanic("recorder-voice-close", "recorder", c.s.userID)
		c.gaveUp.Store(false)
		forDisgo := err
		moved := closedWith(err, disgovoice.GatewayCloseEventCodeCallTerminated.Code) && c.s.channelExists(gw.channel())
		if moved {
			forDisgo = errCallMoved
		}
		if freshConnection(forDisgo) {
			gw.renew()
		}
		onClose(g, forDisgo)
		if c.closed.Load() {
			return
		}
		// disgo starts no fresh connection once Discord has taken the
		// recorder out of the channel, and sends no leave either. The call
		// moved, and the recorder couldn't follow it.
		if c.gaveUp.Load() || moved && !gw.openedSinceRenew() {
			utils.CaptureError(lostConnectionMessage, err, "recorder", c.s.userID)
		}
	}
}

// channelExists reports whether Discord's API still has a channel. Its
// delete can reach the recorder's session after the 4022 that ended its
// call, so the session's state can't say. Only Discord answering that the
// channel is unknown counts as gone. A lookup that fails otherwise leaves
// the rejoin to find out.
func (s *discordgoSession) channelExists(channelID snowflake.ID) bool {
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	_, err := s.dg.Channel(channelID.String(), discordgo.WithContext(ctx))
	var restErr *discordgo.RESTError
	return !errors.As(err, &restErr) || restErr.Message == nil || restErr.Message.Code != discordgo.ErrCodeUnknownChannel
}

// freshConnection reports whether disgo's close handler answers err with a
// fresh connection in the same channel. It does for anything but a voice
// gateway close that disgo's close table says to leave on. disgo hands
// on a close it resumes from only once every try to resume has failed,
// wrapping the last close or dial error.
func freshConnection(err error) bool {
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		return true
	}
	// disgo's GatewayCloseEventCodeByCode, written out as a comparison. The
	// error data check (ADR 0018) fails a close code handed to a call that
	// returns text, as that one does.
	for _, cc := range disgovoice.GatewayCloseEventCodes {
		if cc.Code == closeErr.Code {
			return cc.NewConnection
		}
	}
	return disgovoice.GatewayCloseEventCodeUnknown.NewConnection
}

// closedWith reports whether err is a voice gateway close with code.
func closedWith(err error, code int) bool {
	var closeErr *websocket.CloseError
	return errors.As(err, &closeErr) && closeErr.Code == code
}

func (s *discordgoSession) UserID() string { return s.userID }

// Close takes the recorder out of any voice channel it is still in, then
// ends its session. The close is on purpose, so its watch stands down
// first.
func (s *discordgoSession) Close() error {
	if s.watch != nil {
		s.watch.StandDown()
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	s.voice.Close(ctx)
	return s.dg.Close()
}

// Join opens a disgo voice connection with a dave-go DAVE session, so
// Discord lets the recorder into a channel that requires end-to-end
// encryption. The recorder mutes itself, since it never speaks, and never
// deafens itself, or Discord sends it no audio. A join that fails leaves
// again, so the recorder isn't left half in the channel.
func (s *discordgoSession) Join(ctx context.Context, guildID, channelID string) (Conn, error) {
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return nil, fmt.Errorf("guild ID %q: %w", guildID, err)
	}
	cid, err := snowflake.Parse(channelID)
	if err != nil {
		return nil, fmt.Errorf("channel ID %q: %w", channelID, err)
	}
	conn := s.voice.CreateConn(gid)
	if err := conn.Open(ctx, cid, true, false); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		conn.Close(closeCtx)
		return nil, err
	}
	return &disgoConn{conn: conn, recorder: s.userID}, nil
}

// disgoConn is a recorder's disgo voice connection.
type disgoConn struct {
	conn     disgovoice.Conn
	recorder string

	mu  sync.Mutex
	rcv *receiver
}

// Receive starts the recorder's audio receiver on the connection, which
// hands handle every frame from now until Leave. Frames that arrived before
// wait in the socket.
func (c *disgoConn) Receive(handle func(Frame)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rcv != nil {
		return
	}
	c.rcv = newReceiver(callConn{c.conn}, handle, c.recorder)
	c.rcv.open()
}

// callConn is a recorder's disgo voice connection as its receiver reads it.
type callConn struct {
	disgovoice.Conn
}

// errNotRenewable is reconnect's answer on a connection whose gateway the
// recorder's connection options didn't build.
var errNotRenewable = errors.New("voice: the connection's gateway can't reopen")

// reconnect opens a fresh voice connection where the call is, through the
// gateway the recorder's connection options built.
func (c callConn) reconnect(ctx context.Context) error {
	g, ok := c.Gateway().(*renewableGateway)
	if !ok {
		return errNotRenewable
	}
	return g.reopen(ctx)
}

// Leave stops the receiver, so no frame is handed over after it returns,
// then sends the voice state update that takes the recorder out, and closes
// the voice gateway, UDP and DAVE session.
func (c *disgoConn) Leave(ctx context.Context) {
	c.mu.Lock()
	rcv := c.rcv
	c.mu.Unlock()
	if rcv != nil {
		rcv.close()
	}
	c.conn.Close(ctx)
}

// updateVoiceState is disgo's StateUpdateFunc: the voice state update on the
// recorder's own gateway. discordgo's ChannelVoiceJoinManual sends exactly
// that without starting discordgo's own voice code. No channel leaves.
// discordgo writes to its websocket without checking that one is open, and
// a write to none panics holding discordgo's write lock, which would hold up
// every later write on the session, its heartbeats included. So an update
// goes out only while the session is connected, and the session's lock
// keeps it from closing meanwhile, as discordgo's own status update does.
// disgo calls this from its own goroutines when it reconnects, where a panic
// would end the process, so a panic comes back as an error.
func (s *discordgoSession) updateVoiceState(_ context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("voice state update panicked: %v", r)
		}
	}()
	cid := ""
	if channelID != nil {
		cid = channelID.String()
	}
	s.dg.RLock()
	defer s.dg.RUnlock()
	if !s.dg.DataReady {
		return discordgo.ErrWSNotFound
	}
	return s.dg.ChannelVoiceJoinManual(guildID.String(), cid, selfMute, selfDeaf)
}

// onVoiceStateUpdate hands a voice state to disgo. The session sees every
// member's, and disgo acts on the recorder's own alone.
func (s *discordgoSession) onVoiceStateUpdate(e *discordgo.VoiceStateUpdate) {
	defer utils.RecoverPanic("recorder-voice-state", "recorder", s.userID)
	if ev, ok := voiceStateEvent(e); ok {
		s.voice.HandleVoiceStateUpdate(ev)
	}
}

// onVoiceServerUpdate hands the voice server Discord assigned to disgo.
func (s *discordgoSession) onVoiceServerUpdate(e *discordgo.VoiceServerUpdate) {
	defer utils.RecoverPanic("recorder-voice-server", "recorder", s.userID)
	if ev, ok := voiceServerEvent(e); ok {
		s.voice.HandleVoiceServerUpdate(ev)
	}
}

// watchGateway watches the recorder's gateway session, so an outage that
// lasts reaches Sentry by the recorder's place in RECORDER_TOKENS and,
// once named, its user ID, never by its token.
func (s *discordgoSession) watchGateway(recorder int, after time.Duration) {
	s.watch = utils.WatchGateway(s.dg, after, "session", "recorder", "recorder", recorder)
}

// named records the recorder's user ID, which READY tells, and has the
// watch name the recorder by it.
func (s *discordgoSession) named(userID string) {
	s.userID = userID
	s.watch.AddPairs("user_id", userID)
}
