package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgo/discord"
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
	s.voice = s.voiceManager(updateVoiceState, userID, disgovoice.NewGateway, utils.NewLogger, newVoiceDialer().websocket(),
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

// connLoggerFunc builds the logger one voice connection logs through, and the
// LevelVar that holds its level, which starts at the bot log's.
type connLoggerFunc func() (*slog.Logger, *slog.LevelVar)

// voiceManager builds the recorder's disgo voice manager, which every join
// goes through. update sends the recorder's voice state updates, newGateway
// builds each voice gateway, disgo's NewGateway outside tests, newLogger
// builds the log of each voice connection, utils.NewLogger outside tests,
// dialer dials the voice servers, and opts configure the rest. A voice
// gateway dials only when the recorder opens it (ownDialsOnly).
func (s *discordgoSession) voiceManager(update disgovoice.StateUpdateFunc, userID snowflake.ID, newGateway disgovoice.GatewayCreateFunc, newLogger connLoggerFunc, dialer *websocket.Dialer, opts ...disgovoice.ManagerConfigOpt) disgovoice.Manager {
	return disgovoice.NewManager(update, userID, append(opts,
		disgovoice.WithConnConfigOpts(disgovoice.WithConnGatewayConfigOpts(disgovoice.WithGatewayDialer(ownDialsOnly(dialer)))),
		disgovoice.WithConnCreateFunc(s.connCreate(newGateway, newLogger)))...)
}

// The recorder rejoins a call whose voice connection dropped, and never lets
// disgo resume it. disgo's gateway resumes after a close it can come back
// from, or a read that fails, and tells the recorder nothing until its
// fifth try has failed, 15 s on. The recorder can't hear the call that
// whole time, and gets no sound back from a resume that works: nobody has
// seen one bring audio back. disgo sends the Resume's sequence number as
// seq where Discord's v8 docs say seq_ack, and DAVE removes a member cut
// off from the voice gateway from the call's encryption group, so the SFU
// stops forwarding them its media. A rejoin brought sound back within 2.7 s
// in #728's smoke pass. So every dial a voice gateway makes outside the
// recorder's own open is refused. disgo's resume then fails at its first
// try and hands the close to the recorder at once, which starts the gap and
// rejoins through a new gateway that identifies.

// ownDial marks the context of a dial the recorder's own open makes.
type ownDial struct{}

// errResumeRefused is what disgo's resume gets for its dial. disgo's gateway
// stops trying at once on an already-connected error, and hands the close
// handler this one: the recorder connects again on its own.
var errResumeRefused = fmt.Errorf("voice: the recorder rejoins rather than resumes: %w", discord.ErrGatewayAlreadyConnected)

// ownDialsOnly is dialer, nil for gorilla's default, refusing every dial but
// those the recorder's own open of a voice gateway makes.
func ownDialsOnly(dialer *websocket.Dialer) *websocket.Dialer {
	d := websocket.Dialer{}
	if dialer != nil {
		d = *dialer
	}
	dial := d.NetDialContext
	if dial == nil {
		var nd net.Dialer
		dial = nd.DialContext
	}
	d.NetDialContext = refuseOthers(dial)
	if d.NetDialTLSContext != nil {
		d.NetDialTLSContext = refuseOthers(d.NetDialTLSContext)
	}
	return &d
}

// refuseOthers is dial, refusing a dial whose context the recorder's own
// open didn't mark.
func refuseOthers(dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if ctx.Value(ownDial{}) == nil {
			return nil, errResumeRefused
		}
		return dial(ctx, network, addr)
	}
}

// connCreate builds each of the recorder's voice connections. Each is
// disgo's own, held by a recorderConn, and logs through a logger of its own
// from newLogger.
func (s *discordgoSession) connCreate(newGateway disgovoice.GatewayCreateFunc, newLogger connLoggerFunc) disgovoice.ConnCreateFunc {
	return func(guildID, userID snowflake.ID, update disgovoice.StateUpdateFunc, remove func(), opts ...disgovoice.ConnConfigOpt) disgovoice.Conn {
		c := &recorderConn{s: s, guildID: guildID, userID: userID, update: update}
		logger, level := newLogger()
		c.logLevel = level
		updateVoiceState := func(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
			return c.updateVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
		}
		c.Conn = disgovoice.NewConn(guildID, userID, updateVoiceState, remove, append(opts, c.connOpts(newGateway, logger)...)...)
		return c
	}
}

// connOpts configures the voice connection c holds: its logger, the
// recorder's own UDP connection, and a voice gateway whose close handler is
// watched for a connection lost mid-recording or a call that moved.
// newGateway builds the gateway, disgo's NewGateway outside tests. disgo
// gives the gateway and UDP connection loggers of their own from logger, so
// one level holds for all three, and disgo's ERRORs reach logger at WARN
// from all three. The manager hands every connection its own logger first,
// and this one takes its place.
func (c *recorderConn) connOpts(newGateway disgovoice.GatewayCreateFunc, logger *slog.Logger) []disgovoice.ConnConfigOpt {
	return []disgovoice.ConnConfigOpt{
		disgovoice.WithConnLogger(slog.New(errorsAsWarnings{next: logger.Handler()})),
		disgovoice.WithUDPConnCreateFunc(newUDPConn),
		disgovoice.WithConnGatewayCreateFunc(func(dave godave.Session, onEvent disgovoice.EventHandlerFunc, onClose disgovoice.CloseHandlerFunc, opts ...disgovoice.GatewayConfigOpt) disgovoice.Gateway {
			g := &renewableGateway{named: c.namedLast}
			g.dropped = c.watchClose(g, onClose)
			g.build = func() disgovoice.Gateway {
				return newGateway(dave, g.watchEvents(onEvent), g.dropped, opts...)
			}
			g.gw = g.build()
			c.gateway = g
			return g
		}),
	}
}

// errorsAsWarnings hands each record on to next, at WARN when it was logged
// at ERROR or above. disgo logs at ERROR for every dial, open or close of a
// voice connection that fails, while the recorder goes on trying and the
// recording runs. A loss gets an ERROR of the bot's own, with its Sentry
// event, so an ERROR in the bot log means the recorder lost something. The
// gate's error data check accepts this handler by name without reading it
// (ADR 0018), so keep it to moving a record's level and handing the record
// on.
type errorsAsWarnings struct {
	next slog.Handler
}

func (h errorsAsWarnings) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, asWarning(level))
}

func (h errorsAsWarnings) Handle(ctx context.Context, r slog.Record) error {
	r.Level = asWarning(r.Level)
	return h.next.Handle(ctx, r)
}

func (h errorsAsWarnings) WithAttrs(attrs []slog.Attr) slog.Handler {
	return errorsAsWarnings{next: h.next.WithAttrs(attrs)}
}

func (h errorsAsWarnings) WithGroup(name string) slog.Handler {
	return errorsAsWarnings{next: h.next.WithGroup(name)}
}

// asWarning moves level down to WARN when it is ERROR or above, and leaves
// it as it is below.
func asWarning(level slog.Level) slog.Level {
	if level >= slog.LevelError {
		return slog.LevelWarn
	}
	return level
}

// recorderConn is disgo's voice connection to one guild's calls, as the
// recorder's voice manager holds it. disgo connects only once it has both
// the recorder's voice state and a voice server Discord named after the
// join started, and throws away any server named before. Discord can name
// the server a call moved to before the rejoin starts, then answer the
// rejoin with the voice state alone. So the connection keeps the last
// server Discord named, and hands it to disgo again when disgo threw it
// away. Once the recorder closes the connection, a rejoin disgo started
// before sends nothing more. A rejoin after a close the recorder can come
// back from is the connection's own (rejoin), with no time limit, so the
// recording goes on while the call's new server can't be reached.
type recorderConn struct {
	disgovoice.Conn
	s               *discordgoSession
	guildID, userID snowflake.ID
	// update is the recorder session's voice state update.
	update  disgovoice.StateUpdateFunc
	gateway *renewableGateway
	// logLevel is the level of the connection's log, the bot log's until
	// the recorder closes the connection.
	logLevel *slog.LevelVar

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
	// stopRejoin ends the rejoin under way, nil while there is none.
	stopRejoin context.CancelFunc
}

// errConnClosed is what disgo gets for a voice state update on a connection
// the recorder closed.
var errConnClosed = errors.New("voice: the recorder closed the connection")

// closedLogLevel is the level of a connection's log once the recorder has
// closed the connection. It is above every level disgo logs at.
const closedLogLevel = slog.LevelError + 1

// Close takes the recorder out of the call and closes the connection. A
// rejoin may still be waiting. It ends, and sends nothing after this leave.
// What disgo logs for the connection from now on is about work the close
// abandoned, such as a dial it cut, so none of it reaches the bot log.
func (c *recorderConn) Close(ctx context.Context) {
	c.closed.Store(true)
	c.logLevel.Set(closedLogLevel)
	c.cancelRejoin()
	c.Conn.Close(ctx)
}

// The recorder joins self-muted, since it never speaks, and never
// self-deafened, or Discord sends it no audio.
const selfMute, selfDeaf = true, false

// rejoinRetry is how often a rejoin whose voice state update couldn't go
// out tries it again.
const rejoinRetry = time.Second

// rejoin opens a fresh voice connection in the call's channel, as disgo's
// close handler does, and reports whether the recorder is connected there
// again. disgo's gives up after 30 s and takes the recorder out, where a call
// that moved to a voice server the recorder can't reach yet would end the
// recording. This one waits until the recorder connects, closes the
// connection, or Discord takes it out. A voice state update that can't go
// out, as with the recorder's own gateway down, it sends again every
// rejoinRetry, so the recorder rejoins once its gateway is back.
func (c *recorderConn) rejoin() bool {
	_ = c.UDP().Close()
	channelID := c.ChannelID()
	if channelID == nil {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return false
	}
	c.stopRejoin = cancel
	c.mu.Unlock()
	defer c.cancelRejoin()
	for {
		// Open fails before ctx ends only when its voice state update
		// couldn't go out.
		err := c.Open(ctx, *channelID, selfMute, selfDeaf)
		if err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(rejoinRetry):
		}
	}
}

// cancelRejoin ends the rejoin under way, if any.
func (c *recorderConn) cancelRejoin() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopRejoin != nil {
		c.stopRejoin()
		c.stopRejoin = nil
	}
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

// namedLast is the last voice server Discord named for the call, nil before
// it names one.
func (c *recorderConn) namedLast() *gateway.EventVoiceServerUpdate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastServer
}

// HandleVoiceServerUpdate hands disgo a voice server Discord named, and
// keeps it. disgo keeps it too, unless a join starts after.
func (c *recorderConn) HandleVoiceServerUpdate(e gateway.EventVoiceServerUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.GuildID == c.guildID && e.Endpoint != nil {
		c.lastServer, c.droppedServer = &e, nil
		c.gateway.redialFor(e)
	}
	c.Conn.HandleVoiceServerUpdate(e)
}

// HandleVoiceStateUpdate hands disgo a voice state. When it is Discord's
// answer to a join and disgo dropped the voice server, the answer may still
// name another. If it names none within answerWait, disgo gets the dropped
// server again and connects there.
func (c *recorderConn) HandleVoiceStateUpdate(e gateway.EventVoiceStateUpdate) {
	c.Conn.HandleVoiceStateUpdate(e)
	if e.GuildID != c.guildID || e.UserID != c.userID {
		return
	}
	if e.ChannelID == nil {
		// Discord took the recorder out, so no rejoin can bring it back.
		c.cancelRejoin()
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
// next opens with the same voice session, which a rejoin keeps. A voice
// server answers that resume with nothing that opens audio, and one the
// call moved to never had the session. A new gateway identifies instead.
type renewableGateway struct {
	build func() disgovoice.Gateway
	// named is the last voice server Discord named for the call, nil
	// before it names one.
	named func() *gateway.EventVoiceServerUpdate

	mu sync.Mutex
	gw disgovoice.Gateway
	// state is the voice state the gateway last opened with: the call's
	// channel, and the voice server and token it was connected with.
	state disgovoice.State
	// stopDialing ends the dialing under way, and dialed closes once it
	// has. Both are nil before the first Open.
	stopDialing context.CancelFunc
	dialed      chan struct{}
	// redial ends the dial in flight, so the next one dials the voice
	// server Discord named since, and describedDial closes once the
	// session description that completes its connection arrives. Both are
	// nil while no dial is in flight.
	redial        context.CancelFunc
	describedDial chan struct{}
	// connected is whether the gateway is connected in the call, at the
	// voice server Discord named last: the session description that
	// completes a connection arrived after its last dial started. lostAt is
	// when it last stopped being connected, zero before it ever was.
	connected bool
	lostAt    time.Time

	// dropped is the close handler of every gateway built, which a voice
	// server gone silent reaches as a close.
	dropped disgovoice.CloseHandlerFunc
	// interval is the heartbeat interval the voice server named in its
	// Hello, answered when the gateway last heard that Hello or a heartbeat
	// answer, and heard when it last heard anything from the server. silence
	// fires once the server's heartbeat answer is overdue, while the gateway
	// is connected, and watches counts the times it was set. Each new
	// gateway starts them over.
	interval        time.Duration
	answered, heard time.Time
	silence         *time.Timer
	watches         int
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
	g.disconnectLocked(time.Now())
	g.unwatchLocked()
}

// lose notes that the recorder lost its connection to the call at at.
func (g *renewableGateway) lose(at time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.disconnectLocked(at)
}

// disconnectLocked notes the gateway not connected, from at when it was.
// Caller holds mu.
func (g *renewableGateway) disconnectLocked(at time.Time) {
	if g.connected {
		g.connected, g.lostAt = false, at
	}
}

// watchEvents wraps disgo's handler for the gateway's events, to note the
// session description that completes a connection, and to watch for a
// voice server that goes silent. disgo's gateway hands it every message the
// voice server sends, as it arrives.
func (g *renewableGateway) watchEvents(onEvent disgovoice.EventHandlerFunc) disgovoice.EventHandlerFunc {
	return func(gw disgovoice.Gateway, op disgovoice.Opcode, seq int, data disgovoice.GatewayMessageData) {
		if op == disgovoice.OpcodeSessionDescription {
			g.described(gw)
		}
		g.heardFrom(gw, data)
		onEvent(gw, op, seq, data)
	}
}

// heartbeatMargin is how long past its heartbeat interval since its last
// answer a voice server may go without answering a heartbeat before the
// recorder counts the connection dropped. disgo's gateway notices only at
// the heartbeat after the one left unanswered, up to two intervals on, and
// Discord names an interval of over 13 s.
const heartbeatMargin = 3 * time.Second

// errVoiceServerSilent is the close a voice server that stopped answering
// heartbeats reaches the gateway's close handler with.
var errVoiceServerSilent = errors.New("voice: the voice server stopped answering heartbeats")

// heardFrom notes a message from the voice server on gw. Its Hello names
// the heartbeat interval, and it and each heartbeat answer start the
// silence watch over.
func (g *renewableGateway) heardFrom(gw disgovoice.Gateway, data disgovoice.GatewayMessageData) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gw != g.gw {
		return
	}
	now := time.Now()
	g.heard = now
	switch d := data.(type) {
	case disgovoice.GatewayMessageDataHello:
		g.interval, g.answered = time.Duration(d.HeartbeatInterval*float64(time.Millisecond)), now
	case disgovoice.GatewayMessageDataHeartbeatACK:
		g.answered = now
	default:
		return
	}
	g.watchLocked(gw)
}

// watchLocked starts the silence watch over gw afresh from the voice
// server's last answer, while gw is connected and its server has named a
// heartbeat interval. Caller holds mu.
func (g *renewableGateway) watchLocked(gw disgovoice.Gateway) {
	if g.silence != nil {
		g.silence.Stop()
		g.silence = nil
	}
	if !g.connected || g.interval <= 0 {
		return
	}
	g.watches++
	watch := g.watches
	g.silence = time.AfterFunc(time.Until(g.answered.Add(g.interval+heartbeatMargin)), func() { g.wentSilent(gw, watch) })
}

// unwatch ends the silence watch, for a gateway that closes.
func (g *renewableGateway) unwatch() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unwatchLocked()
}

// unwatchLocked ends the silence watch and forgets what it knew, for a
// gateway that closed or gave way to a new one. Caller holds mu.
func (g *renewableGateway) unwatchLocked() {
	if g.silence != nil {
		g.silence.Stop()
	}
	g.silence, g.interval, g.answered, g.heard = nil, 0, time.Time{}, time.Time{}
}

// wentSilent drops gw, whose voice server's heartbeat answer is overdue,
// unless watch has given way to a later one or the gateway to a new one.
// The recorder couldn't hear the call from the server's last message on.
// The close is on purpose, so disgo's close handler hears nothing of it,
// and the gateway's own takes the drop as it takes a close.
func (g *renewableGateway) wentSilent(gw disgovoice.Gateway, watch int) {
	defer utils.RecoverPanic("recorder-voice-silence")
	g.mu.Lock()
	if gw != g.gw || watch != g.watches || !g.connected {
		g.mu.Unlock()
		return
	}
	g.disconnectLocked(g.heard)
	g.unwatchLocked()
	g.mu.Unlock()
	gw.CloseWithCode(websocket.CloseServiceRestart, "heartbeat answer overdue")
	g.dropped(gw, errVoiceServerSilent)
}

// described notes a session description on gw, which completes the dial in
// flight when gw is the gateway it dialed. It connects the gateway when that
// dial went to the server Discord named last.
func (g *renewableGateway) described(gw disgovoice.Gateway) {
	e := g.named()
	g.mu.Lock()
	defer g.mu.Unlock()
	if gw != g.gw {
		return
	}
	if e == nil || dialedAt(g.state, *e) {
		g.connected = true
		g.watchLocked(gw)
	} else {
		g.disconnectLocked(time.Now())
	}
	if g.describedDial != nil {
		close(g.describedDial)
		g.describedDial = nil
	}
}

// dialedAt reports whether state is at the voice server e names, with its
// token.
func dialedAt(state disgovoice.State, e gateway.EventVoiceServerUpdate) bool {
	return state.Endpoint == *e.Endpoint && state.Token == e.Token
}

// server is the voice server Discord named last for the call, which every
// dial goes to, empty before Discord names one.
func (g *renewableGateway) server() string {
	if e := g.named(); e != nil {
		return *e.Endpoint
	}
	return ""
}

// connection reports whether the gateway is connected in the call, at the
// voice server Discord named last, and when it last stopped being
// connected, zero before it ever was.
func (g *renewableGateway) connection() (connected bool, lostAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.connected, g.lostAt
}

// openedAt reports whether the gateway last opened at the voice server e
// names, with its token.
func (g *renewableGateway) openedAt(e gateway.EventVoiceServerUpdate) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return dialedAt(g.state, e)
}

func (g *renewableGateway) SSRC() uint32 { return g.current().SSRC() }

// Open opens the gateway in the call state names, at the voice server
// Discord named last, with its token. The state disgo hands over can name
// an earlier one, and so can the state a fresh connection after a DAVE
// stall reopens with. Open returns once the gateway has opened, or with
// ctx's error. The dialing goes on past ctx, so a voice server the recorder
// can't reach yet doesn't end the recording at the 30 s disgo gives an
// open. It ends once the gateway opens or closes, or a later Open takes its
// place.
func (g *renewableGateway) Open(ctx context.Context, state disgovoice.State) error {
	result := g.dial(state)
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// dial starts dialing state in place of the dialing under way, once that
// has ended, and returns where its result arrives.
func (g *renewableGateway) dial(state disgovoice.State) <-chan error {
	ctx, stop := context.WithCancel(context.Background())
	dialed := make(chan struct{})
	g.mu.Lock()
	if g.stopDialing != nil {
		g.stopDialing()
	}
	prev := g.dialed
	g.stopDialing, g.dialed = stop, dialed
	g.mu.Unlock()
	result := make(chan error, 1)
	go func() {
		defer close(dialed)
		defer stop()
		err := errDialPanicked
		defer func() { result <- err }()
		defer utils.RecoverPanic("recorder-voice-dial")
		if prev != nil {
			<-prev
		}
		err = g.dialUntilOpen(ctx, state)
	}()
	return result
}

// errDialPanicked is Open's answer when disgo's gateway panicked opening.
var errDialPanicked = errors.New("voice: opening the voice gateway panicked")

// dialUntilOpen opens the gateway, dialing again at each voice server
// Discord names meanwhile, until it opens at the one named last or ctx ends.
// disgo's gateway dials the state it was given again and again while it
// can't reach its server, so a server Discord names ends that dial, and the
// next one dials the new server. A dial ends once the session description
// completes its connection, which comes after disgo's open returns. Discord
// can name another server before then. The call isn't there anymore, so
// that gateway closes, and a new one dials the server named last.
func (g *renewableGateway) dialUntilOpen(ctx context.Context, state disgovoice.State) error {
	for {
		dial, redial := context.WithCancel(ctx)
		gw, at, described := g.startDial(state, redial)
		err := gw.Open(context.WithValue(dial, ownDial{}, true), at)
		if err == nil {
			select {
			case <-described:
			case <-dial.Done():
			}
		}
		redialed := dial.Err() != nil && ctx.Err() == nil
		g.dialEnded()
		redial()
		switch {
		case err == nil && ctx.Err() != nil:
			return ctx.Err()
		case err == nil && g.namedLastIs(at):
			return nil
		case err == nil:
			g.replace(gw)
		case !redialed:
			return err
		}
	}
}

// namedLastIs reports whether state is at the voice server Discord named
// last, with its token.
func (g *renewableGateway) namedLastIs(state disgovoice.State) bool {
	e := g.named()
	return e == nil || dialedAt(state, *e)
}

// replace closes gw, which opened at a voice server Discord has replaced
// since, and puts a new gateway in its place, one that identifies. The close
// is on purpose, so disgo's close handler hears nothing of it.
func (g *renewableGateway) replace(gw disgovoice.Gateway) {
	g.mu.Lock()
	if g.gw == gw {
		g.gw = g.build()
		g.disconnectLocked(time.Now())
		g.unwatchLocked()
	}
	g.mu.Unlock()
	gw.CloseWithCode(websocket.CloseServiceRestart, "Discord named a later voice server")
}

// cancelDialing ends the dialing under way, if any.
func (g *renewableGateway) cancelDialing() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopDialing != nil {
		g.stopDialing()
	}
}

// startDial notes a dial of state at the voice server Discord named last,
// which redial ends. It returns the gateway to dial, the state to dial with,
// and a channel that closes once the session description completes the
// dial's connection.
func (g *renewableGateway) startDial(state disgovoice.State, redial context.CancelFunc) (disgovoice.Gateway, disgovoice.State, <-chan struct{}) {
	if e := g.named(); e != nil {
		state.Endpoint, state.Token = *e.Endpoint, e.Token
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state, g.redial = state, redial
	g.disconnectLocked(time.Now())
	g.describedDial = make(chan struct{})
	return g.gw, state, g.describedDial
}

// dialEnded notes that the dial in flight has ended.
func (g *renewableGateway) dialEnded() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.redial, g.describedDial = nil, nil
}

// redialFor ends the dial in flight when it dials a voice server other than
// the one e names.
func (g *renewableGateway) redialFor(e gateway.EventVoiceServerUpdate) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.redial != nil && !dialedAt(g.state, e) {
		g.redial()
	}
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
	g.disconnectLocked(time.Now())
	g.unwatchLocked()
	g.mu.Unlock()
	old.CloseWithCode(websocket.CloseServiceRestart, "starting DAVE over")
	return g.Open(ctx, state)
}

// Close ends any dialing, then closes the gateway: disgo closes it when the
// recorder leaves or Discord takes it out.
func (g *renewableGateway) Close() {
	g.cancelDialing()
	g.unwatch()
	g.current().Close()
}

func (g *renewableGateway) CloseWithCode(code int, message string) {
	g.cancelDialing()
	g.unwatch()
	g.current().CloseWithCode(code, message)
}

func (g *renewableGateway) Status() disgovoice.Status { return g.current().Status() }

func (g *renewableGateway) Send(ctx context.Context, op disgovoice.Opcode, d disgovoice.GatewayMessageData) error {
	return g.current().Send(ctx, op, d)
}

func (g *renewableGateway) Latency() time.Duration { return g.current().Latency() }

// lostConnectionMessage is what Sentry hears when the recorder is taken out
// of the channel on its own.
const lostConnectionMessage = "Voice connection lost mid-recording"

// watchClose wraps disgo's handler for a voice gateway that closed and
// couldn't resume. disgo opens a fresh connection when the close allows it,
// and a voice reconnect ends there. Here the recorder's rejoin takes that
// over, through a new gateway, which identifies. The old one would resume
// the voice session, which the rejoin keeps, and a resume gets no answer
// that opens audio. Discord ends a call with 4022, "Call terminated", when
// its channel is deleted or the call moves to another voice server, as a
// region change makes it do. A call that moved gets a fresh connection too,
// unless its channel is gone. Otherwise disgo takes the recorder out of the
// channel itself, and so does a rejoin that can't go out: the voice
// connection is lost mid-recording, and Sentry hears of it with the close
// that started it. So does Discord taking the recorder out before it is
// back. A connection the recorder closed meanwhile ended with its
// recording, and lost nothing.
func (c *recorderConn) watchClose(gw *renewableGateway, onClose disgovoice.CloseHandlerFunc) disgovoice.CloseHandlerFunc {
	return func(g disgovoice.Gateway, err error) {
		defer utils.RecoverPanic("recorder-voice-close", "recorder", c.s.userID)
		// The recorder can't hear the call from the close on, whatever the
		// lookup below costs.
		gw.lose(time.Now())
		moved := closedWith(err, disgovoice.GatewayCloseEventCodeCallTerminated.Code) && c.s.channelExists(gw.channel())
		if !moved && !getsFreshConnection(err) {
			c.gaveUp.Store(false)
			onClose(g, err)
			if c.gaveUp.Load() && !c.closed.Load() {
				utils.CaptureError(lostConnectionMessage, err, "recorder", c.s.userID)
			}
			return
		}
		gw.renew()
		if !c.rejoin() && !c.closed.Load() {
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

// getsFreshConnection reports whether disgo's close handler answers err with
// a fresh connection in the same channel. It does for anything but a voice
// gateway close that disgo's close table says to leave on. disgo hands on a
// close it would resume from as the refusal of its resume's dial
// (errResumeRefused).
func getsFreshConnection(err error) bool {
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		return true
	}
	// disgo's GatewayCloseEventCodeByCode, written out. The error data check
	// (ADR 0018) fails a close code handed to a call that returns text, as
	// that one does, and passes a lookup in a table.
	code, ok := disgovoice.GatewayCloseEventCodes[closeErr.Code]
	if !ok {
		code = disgovoice.GatewayCloseEventCodeUnknown
	}
	return code.NewConnection
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
	if err := conn.Open(ctx, cid, selfMute, selfDeaf); err != nil {
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
// hands l every frame from now until Leave, and each gap. Frames that
// arrived before wait in the socket.
func (c *disgoConn) Receive(l Listener) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rcv != nil {
		return
	}
	c.rcv = newReceiver(callConn{c.conn}, l, c.recorder)
	c.rcv.open()
}

// callConn is a recorder's disgo voice connection as its receiver reads it.
type callConn struct {
	disgovoice.Conn
}

// errNotRenewable is reconnect's answer on a connection whose gateway the
// recorder's connection options didn't build.
var errNotRenewable = errors.New("voice: the connection's gateway can't reopen")

// renewable is the gateway the recorder's connection options built, false
// on a connection they didn't build.
func (c callConn) renewable() (*renewableGateway, bool) {
	g, ok := c.Gateway().(*renewableGateway)
	return g, ok
}

// connection reports whether the recorder's gateway is connected in the
// call, at the voice server Discord named last, and when it last stopped
// being connected, zero before it ever was.
func (c callConn) connection() (connected bool, lostAt time.Time) {
	g, ok := c.renewable()
	if !ok {
		return false, time.Time{}
	}
	return g.connection()
}

// server is the voice server the recorder's gateway dials: the one Discord
// named last for the call, empty before Discord names one.
func (c callConn) server() string {
	g, ok := c.renewable()
	if !ok {
		return ""
	}
	return g.server()
}

// reconnect opens a fresh voice connection where the call is, through the
// gateway the recorder's connection options built.
func (c callConn) reconnect(ctx context.Context) error {
	g, ok := c.renewable()
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
