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

// OpenGateway is the production Opener: a discordgo session for the recorder
// account, open once READY has named it. A session that opened without
// reaching READY is closed again, so a failed recorder holds no connection.
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
		s.watch.StandDown()
		_ = dg.Close()
		return nil, err
	}
	userID, err := snowflake.Parse(dg.State.User.ID)
	if err != nil {
		s.watch.StandDown()
		_ = dg.Close()
		return nil, err
	}
	s.named(dg.State.User.ID)
	// Closures rather than method values here and below: the gate's error
	// data check follows a closure, and not a method value.
	updateVoiceState := func(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
		return s.updateVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
	}
	s.voice = disgovoice.NewManager(updateVoiceState, userID,
		disgovoice.WithLogger(utils.Logger),
		disgovoice.WithDaveSessionCreateFunc(session.CreateFunc()),
		disgovoice.WithDaveSessionLogger(utils.Logger),
		disgovoice.WithConnConfigOpts(s.connOpts(disgovoice.NewGateway)...),
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

	// leaving is set while a Leave or Close takes the recorder out, so the
	// voice state update disgo sends for it is known to be ours. gaveUp is
	// set when disgo sends one on its own: it couldn't keep the connection.
	leaving atomic.Bool
	gaveUp  atomic.Bool

	// watch reports a gateway outage that lasts.
	watch *utils.GatewayWatch
}

// connOpts configures every voice connection the recorder opens: the
// recorder's own UDP connection, and a voice gateway whose close handler is
// watched for a connection lost mid-recording or a call that moved.
// newGateway builds the gateway, disgo's NewGateway outside tests.
func (s *discordgoSession) connOpts(newGateway disgovoice.GatewayCreateFunc) []disgovoice.ConnConfigOpt {
	return []disgovoice.ConnConfigOpt{
		disgovoice.WithUDPConnCreateFunc(newUDPConn),
		disgovoice.WithConnGatewayCreateFunc(func(dave godave.Session, onEvent disgovoice.EventHandlerFunc, onClose disgovoice.CloseHandlerFunc, opts ...disgovoice.GatewayConfigOpt) disgovoice.Gateway {
			g := &renewableGateway{}
			g.build = func() disgovoice.Gateway {
				return newGateway(dave, onEvent, s.watchClose(g, onClose), opts...)
			}
			g.gw = g.build()
			return g
		}),
	}
}

// renewableGateway is the voice gateway disgo's connection holds. It hands
// every call to a disgo gateway, which renew swaps for a new one. disgo's
// gateway keeps what it resumes with through a close, and resumes when it
// next opens, but a voice server the call moved to never had that session.
// A new gateway identifies there instead.
type renewableGateway struct {
	build func() disgovoice.Gateway

	mu sync.Mutex
	gw disgovoice.Gateway
	// channelID is the channel the gateway last opened in: the call's.
	channelID snowflake.ID
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

// openedSinceRenew reports whether the new gateway has opened.
func (g *renewableGateway) openedSinceRenew() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.opened
}

func (g *renewableGateway) SSRC() uint32 { return g.current().SSRC() }

func (g *renewableGateway) Open(ctx context.Context, state disgovoice.State) error {
	g.mu.Lock()
	g.channelID = state.ChannelID
	g.opened = true
	gw := g.gw
	g.mu.Unlock()
	return gw.Open(ctx, state)
}

// channel is the channel of the call the gateway last opened for.
func (g *renewableGateway) channel() snowflake.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.channelID
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
// and a voice reconnect ends there. Discord ends a call with 4022, "Call
// terminated", when its channel is deleted or the call moves to another
// voice server, as a region change makes it do. A call that moved gets a
// fresh connection too, through a new gateway, unless its channel is gone.
// Otherwise, or when the fresh connection fails, disgo takes the recorder
// out of the channel itself: the voice connection is lost mid-recording,
// and Sentry hears of it with the close that started it.
func (s *discordgoSession) watchClose(gateway *renewableGateway, onClose disgovoice.CloseHandlerFunc) disgovoice.CloseHandlerFunc {
	return func(g disgovoice.Gateway, err error) {
		defer utils.RecoverPanic("recorder-voice-close", "recorder", s.userID)
		s.gaveUp.Store(false)
		forDisgo := err
		moved := closedWith(err, disgovoice.GatewayCloseEventCodeCallTerminated.Code) && s.channelExists(gateway.channel())
		if moved {
			gateway.renew()
			forDisgo = errCallMoved
		}
		onClose(g, forDisgo)
		// disgo starts no fresh connection once Discord has taken the
		// recorder out of the channel, and sends no leave either. The call
		// moved, and the recorder couldn't follow it.
		if s.gaveUp.Load() || moved && !gateway.openedSinceRenew() {
			utils.CaptureError(lostConnectionMessage, err, "recorder", s.userID)
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
	s.leaving.Store(true)
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
	s.leaving.Store(false)
	conn := s.voice.CreateConn(gid)
	if err := conn.Open(ctx, cid, true, false); err != nil {
		s.leaving.Store(true)
		closeCtx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		conn.Close(closeCtx)
		return nil, err
	}
	return &disgoConn{conn: conn, recorder: s.userID, leaving: &s.leaving}, nil
}

// disgoConn is a recorder's disgo voice connection. leaving is its
// session's.
type disgoConn struct {
	conn     disgovoice.Conn
	recorder string
	leaving  *atomic.Bool

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
	c.rcv = newReceiver(c.conn, handle, c.recorder)
	c.rcv.open()
}

// Leave stops the receiver, so no frame is handed over after it returns,
// then sends the voice state update that takes the recorder out, and closes
// the voice gateway, UDP and DAVE session.
func (c *disgoConn) Leave(ctx context.Context) {
	c.leaving.Store(true)
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
	} else if !s.leaving.Load() {
		s.gaveUp.Store(true)
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
	s.watch.Name("user_id", userID)
}
