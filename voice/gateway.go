package voice

import (
	"cmp"
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

// Discord is the production Opener: Discord itself, through discordgo.
type Discord struct {
	// offlineAfter is how long a recorder stays offline before Sentry
	// hears of it, utils.GatewayOfflineAfter when zero.
	offlineAfter time.Duration

	mu sync.Mutex
	// recorders is each recorder the opener has logged in, by its place in
	// RECORDER_TOKENS.
	recorders map[int]*discordRecorder
}

// discordRecorder is a recorder as the opener knows it, across its logins.
// The opener's mutex guards named.
type discordRecorder struct {
	// watch is the recorder's gateway outage watch, which reads each of
	// its logins in turn.
	watch *utils.GatewayWatch
	// named is set once the watch names the recorder by its user ID.
	named bool
}

// recorder is the recorder at its place in RECORDER_TOKENS. Its first login
// makes it, with a watch whose outage starts then, which a login's READY
// ends, so a first login that fails is an outage from its try.
func (d *Discord) recorder(place int) *discordRecorder {
	d.mu.Lock()
	defer d.mu.Unlock()
	if rec := d.recorders[place]; rec != nil {
		return rec
	}
	after := cmp.Or(d.offlineAfter, utils.GatewayOfflineAfter)
	rec := &discordRecorder{watch: utils.NewGatewayWatch(after, "session", "recorder", "recorder", place)}
	rec.watch.Offline()
	if d.recorders == nil {
		d.recorders = map[int]*discordRecorder{}
	}
	d.recorders[place] = rec
	return rec
}

// name has the recorder's watch name it by its user ID, which READY tells,
// once for all of its logins, since the ID is the account's.
func (d *Discord) name(rec *discordRecorder, userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !rec.named {
		rec.named = true
		rec.watch.AddPairs("user_id", userID)
	}
}

// FailedToConnect stands the recorder's watch down. The recorder is out
// until a restart, and Sentry has heard of it as "Recorder failed to
// connect", which the outage of its first login would only repeat.
func (d *Discord) FailedToConnect(place int) {
	d.recorder(place).watch.StandDown()
}

// UserID reads the recorder account's user ID from GET /users/@me with its
// token, which needs no gateway session.
func (d *Discord) UserID(ctx context.Context, token string) (string, error) {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return "", err
	}
	u, err := dg.User("@me", discordgo.WithContext(ctx))
	if err != nil {
		return "", err
	}
	return u.ID, nil
}

// Open is one login of a recorder: a new discordgo session for the
// recorder account, open once READY has named it. A session that opened
// without reaching READY is closed again for good, so a failed login holds
// no connection, and the next login opens a session of its own.
// The recorder's watch reads the session, so a recorder that drops off the
// gateway, or stays offline, reaches Sentry while the bot runs, and one
// whose token Discord refuses stops logging in.
func (d *Discord) Open(recorder int, token string) (GatewaySession, error) {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	dg.LogLevel = utils.DiscordgoLogLevel()
	dg.Identify.Intents = recorderIntents
	dialer := newVoiceDialer()
	s := &discordgoSession{dg: dg, drops: dialer.drops}
	// Watched from before it opens, so the watch reads every close Discord
	// sends the session. A failed open is a close on purpose, and the
	// recorder stays offline.
	rec := d.recorder(recorder)
	s.watch = rec.watch
	s.watch.Watch(dg)
	if err := utils.OpenSession(dg); err != nil {
		return nil, s.failedOpen(err)
	}
	userID, err := snowflake.Parse(dg.State.User.ID)
	if err != nil {
		return nil, s.failedOpen(err)
	}
	s.userID = dg.State.User.ID
	d.name(rec, s.userID)
	// Closures rather than method values here and below: the gate's error
	// data check follows a closure, and not a method value.
	updateVoiceState := func(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
		return s.updateVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
	}
	s.voice = s.voiceManager(updateVoiceState, userID, disgovoice.NewGateway, utils.NewLogger, dialer.websocket(),
		disgovoice.WithLogger(utils.Logger),
		disgovoice.WithDaveSessionCreateFunc(session.CreateFunc()),
		disgovoice.WithDaveSessionLogger(utils.Logger),
	)
	// Added after READY, once the manager exists: no voice event reaches a
	// recorder before its first join.
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.VoiceStateUpdate) { s.onVoiceStateUpdate(e) })
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.VoiceServerUpdate) { s.onVoiceServerUpdate(e) })
	// discordgo may be reconnecting the login when the recorder logs out of
	// it, and the logout doesn't stop that.
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Connect) { s.cameBack() })
	return s, nil
}

// failedOpen closes a session that failed to open, and returns err, the
// open's failure. When Discord refused the recorder's token, that failure
// is ErrTokenRefused, whether Discord closed the connection with 4004 during
// the open or after the open had returned.
func (s *discordgoSession) failedOpen(err error) error {
	if refused, _ := s.watch.CloseSession(); refused {
		return fmt.Errorf("%w: %w", ErrTokenRefused, err)
	}
	return err
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
	// drops, when set, can drop the recorder's voice connections, as the
	// smoke hook does.
	drops *connDrops
	// loggedOut is set once the recorder has logged out of this login.
	loggedOut atomic.Bool
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
		disgovoice.WithUDPConnCreateFunc(droppableUDPConns(c.s.drops)),
		disgovoice.WithConnGatewayCreateFunc(func(dave godave.Session, onEvent disgovoice.EventHandlerFunc, onClose disgovoice.CloseHandlerFunc, opts ...disgovoice.GatewayConfigOpt) disgovoice.Gateway {
			g := &renewableGateway{
				named:   c.namedLast,
				refused: func(state disgovoice.State, err error) { c.refused(state, err) },
				steps:   &c.steps,
			}
			g.onDrop = c.watchClose(g, onClose)
			g.build = func() disgovoice.Gateway {
				var gw disgovoice.Gateway
				observe := disgovoice.WithGatewayCloseObserver(func(err error) { g.closeObserved(gw, err) })
				gw = newGateway(dave, g.watchEvents(onEvent), g.onDrop, append(opts[:len(opts):len(opts)], observe)...)
				return gw
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
// before sends nothing more. A rejoin after a drop the recorder can come
// back from is the connection's own (rejoin), with no time limit, so the
// recording goes on while the call's server or the recorder's own gateway
// can't be reached. A voice server can refuse the token the recorder held,
// and then the rejoin leaves the channel and joins it again: Discord answers
// a fresh join with a voice server and a new token.
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
	// ownLeaves counts the rejoin's own leaves, each from just before it
	// goes out.
	ownLeaves atomic.Int32
	// steps is the recovery steps the connection has taken.
	steps recoveryLog

	mu sync.Mutex
	// lastServer is the last voice server Discord named for the call.
	// droppedServer is a server disgo threw away and still needs: Discord
	// named it before the join under way started. held is the one disgo got
	// again, until Discord names another. shunned is one disgo never gets
	// again, since the call left it or it refused the recorder's token.
	lastServer, droppedServer, held, shunned *gateway.EventVoiceServerUpdate
	// stopRejoin ends the rejoin under way, nil while there is none, and
	// refuseAttempt ends its wait for a connection when the voice server
	// refuses the token the recorder held.
	stopRejoin    context.CancelFunc
	refuseAttempt context.CancelCauseFunc
	// leaveAnswered closes once Discord answers the rejoin's own leave, nil
	// while no such leave is out.
	leaveAnswered chan struct{}
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

// errHeldTokenRefused ends a rejoin's wait for a connection when the voice
// server refuses the token the recorder held.
var errHeldTokenRefused = errors.New("voice: the voice server refused the token the recorder held")

// rejoin opens a fresh voice connection in the call's channel, as disgo's
// close handler does, and reports whether the recorder is connected there
// again. disgo's gives up after 30 s and takes the recorder out, where a call
// that moved to a voice server the recorder can't reach yet would end the
// recording. This one waits until the recorder connects, closes the
// connection, or Discord takes it out. A voice state update that can't go
// out, as with the recorder's own gateway down, it sends again every
// rejoinRetry, so the recorder rejoins once its gateway is back. When the
// voice server refuses the token the recorder held, the rejoin leaves the
// channel and joins it again.
func (c *recorderConn) rejoin() (rejoined bool) {
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
	step := c.steps.start(StepRejoin, "")
	defer func() { c.steps.end(step, outcomeOf(rejoined), nil) }()
	for {
		attempt, refuse := context.WithCancelCause(ctx)
		c.mu.Lock()
		c.refuseAttempt = refuse
		c.mu.Unlock()
		// Open fails before ctx ends only when its voice state update
		// couldn't go out, or the voice server refused the held token.
		err := c.Open(attempt, *channelID, selfMute, selfDeaf)
		refused := errors.Is(context.Cause(attempt), errHeldTokenRefused)
		c.mu.Lock()
		c.refuseAttempt = nil
		c.mu.Unlock()
		refuse(nil)
		switch {
		case err == nil:
			return true
		case ctx.Err() != nil:
			return false
		case refused:
			if !c.leaveToJoinAgain(ctx) {
				return false
			}
			continue
		}
		if !waitToRetry(ctx) {
			return false
		}
	}
}

// waitToRetry waits rejoinRetry, and reports false when ctx ends first.
func waitToRetry(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(rejoinRetry):
		return true
	}
}

// leaveToJoinAgain takes the recorder out of the call's channel on its own,
// through a fresh gateway, so the rejoin's next join is a fresh one, which
// Discord answers with a voice server and a new token. It returns once
// Discord has answered the leave, false when ctx ends first. A leave that
// can't go out it sends again every rejoinRetry.
func (c *recorderConn) leaveToJoinAgain(ctx context.Context) (left bool) {
	step := c.steps.start(StepLeaveToJoinAgain, "")
	defer func() { c.steps.end(step, outcomeOf(left), nil) }()
	c.gateway.renew(GapVoiceGatewayClosed)
	answered := make(chan struct{})
	c.mu.Lock()
	c.leaveAnswered = answered
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.leaveAnswered == answered {
			c.leaveAnswered = nil
		}
	}()
	c.ownLeaves.Add(1)
	for c.leave(ctx) != nil {
		if !waitToRetry(ctx) {
			return false
		}
	}
	select {
	case <-answered:
		return true
	case <-ctx.Done():
		return false
	}
}

// leave sends the recorder's own leave, unless the recorder has closed the
// connection, whose own leave goes out instead.
func (c *recorderConn) leave(ctx context.Context) error {
	if c.closed.Load() {
		return errConnClosed
	}
	return c.sendVoiceState(ctx, c.guildID, nil, false, false)
}

// sendVoiceState sends a voice state update on the recorder's own session.
// One that couldn't go out, as while the session is offline, is a recovery
// step that failed.
func (c *recorderConn) sendVoiceState(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
	err := c.update(ctx, guildID, channelID, selfMute, selfDeaf)
	if err != nil {
		c.steps.failed(StepVoiceStateUpdate, err)
	}
	return err
}

// refused notes that a voice server closed the connection before its
// Ready, refusing the token or session the identify state carried. When that was the token the
// recorder held, during a rejoin, the recorder shuns the server, and the
// rejoin leaves and joins again. A token Discord named since is refused for
// some other reason, and a fresh join would get no better one.
func (c *recorderConn) refused(state disgovoice.State, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held == nil || !dialedAt(state, *c.held) || c.refuseAttempt == nil {
		return
	}
	utils.Warn(refusedTokenMessage, "recorder", c.s.userID, "voice_server", state.Endpoint, "error", err)
	c.shunned, c.held = c.held, nil
	c.refuseAttempt(errHeldTokenRefused)
}

// cancelRejoin ends the rejoin under way, if any.
func (c *recorderConn) cancelRejoin() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelRejoinLocked()
}

// cancelRejoinLocked is cancelRejoin with mu held.
func (c *recorderConn) cancelRejoinLocked() {
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
	return c.sendVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
}

// joinStarted notes disgo starting a join, which throws away the voice
// server disgo held. Discord answers a rejoin of the channel the recorder
// is in with its voice state alone, unless the call moved, so disgo dropped
// the server Discord named last, unless the recorder shuns it.
func (c *recorderConn) joinStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.droppedServer = nil
	if c.lastServer != c.shunned {
		c.droppedServer = c.lastServer
	}
}

// callMoved notes that the call left the voice server the gateway last
// opened at, which the recorder shuns from then on. Discord can have named
// the server the call moved to already.
func (c *recorderConn) callMoved() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastServer != nil && c.gateway.openedAt(*c.lastServer) {
		c.shunned = c.lastServer
	}
}

// heldTokenMessage is the bot log's INFO line when the recorder reconnects
// with the voice server token it held, since Discord's answer to its rejoin
// named no server.
const heldTokenMessage = "Recorder reconnecting with the voice server token it held"

// refusedTokenMessage is the bot log's WARN line when the voice server
// refuses the token the recorder held, and the recorder leaves the channel
// and joins it again for a new one.
const refusedTokenMessage = "Voice server refused the token the recorder held, so it leaves and joins again"

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
		c.lastServer, c.droppedServer, c.held = &e, nil, nil
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.ChannelID == nil {
		if c.leaveAnswered != nil {
			// Discord's answer to the rejoin's own leave, which a join
			// follows.
			close(c.leaveAnswered)
			c.leaveAnswered = nil
			return
		}
		// Discord took the recorder out, so no rejoin can bring it back.
		c.cancelRejoinLocked()
		return
	}
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
	c.droppedServer, c.held = nil, dropped
	utils.Info(heldTokenMessage, "recorder", c.s.userID, "voice_server", *dropped.Endpoint)
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
	// before it names one. refused hears of a voice server that closed a
	// connection before its Ready, refusing the token or session the
	// identify state carried.
	named   func() *gateway.EventVoiceServerUpdate
	refused func(state disgovoice.State, err error)

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
	// completes a connection arrived after its last dial started. loss is
	// when and why it last stopped being connected, zero before it ever
	// was, and losses counts the times it has.
	connected bool
	loss      gapStart
	losses    int
	// observed is the close or failed read a disgo gateway last saw, which
	// disgo hands the close handler only as its refused resume when it
	// would resume from it.
	observed observedClose
	// steps is the log each dial is noted in. connectedWith is the token
	// the gateway last connected with: a dial with it dials with the token
	// the recorder held.
	steps         *recoveryLog
	connectedWith string

	// onDrop is the close handler of every gateway built, which a voice
	// server gone silent reaches as a close.
	onDrop disgovoice.CloseHandlerFunc
	// heartbeat watches the voice server's heartbeat while the gateway is
	// connected, and each new gateway starts it over. watches counts the
	// times its timer was set.
	heartbeat heartbeatWatch
	watches   int
}

// observedClose is the close or failed read err that disgo gateway gw saw.
type observedClose struct {
	gw  disgovoice.Gateway
	err error
}

// closeObserved notes the close or failed read disgo's gateway gw saw.
// disgo starts its observer before the reconnect whose refused dial reaches
// the close handler, and either can run first. A loss gw's close handler
// handed over already as the refused resume takes err in its place.
func (g *renewableGateway) closeObserved(gw disgovoice.Gateway, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observed = observedClose{gw: gw, err: err}
	if g.loss.closedBy == gw && errors.Is(g.loss.close, errResumeRefused) {
		g.loss.close = err
	}
}

// heartbeatWatch is what the recorder knows of a voice server's heartbeat on
// one gateway: the interval the server's Hello named, when the gateway last
// heard that Hello or a heartbeat answer, and when it last heard anything
// from the server. timer fires once the server's heartbeat answer is
// overdue.
type heartbeatWatch struct {
	interval        time.Duration
	answered, heard time.Time
	timer           *time.Timer
}

// overdue is when the voice server's heartbeat answer is overdue:
// heartbeatMargin past the interval since its last answer.
func (w *heartbeatWatch) overdue() time.Time { return w.answered.Add(w.interval + heartbeatMargin) }

// stop stops the timer, if it is set.
func (w *heartbeatWatch) stop() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (g *renewableGateway) current() disgovoice.Gateway {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gw
}

// renew swaps the gateway, closed by now for cause, for a new one to open
// next.
func (g *renewableGateway) renew(cause GapCause) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.renewLocked(cause)
}

// renewAfter swaps the gateway for a new one, as renew does, after it lost
// the call to loss for cause. Connected again since, as disgo can connect it
// while a 4022's lookup is out, it counts as lost to loss still, so the gap
// starts at that loss and not at the renew, unless it has lost the call
// again since.
func (g *renewableGateway) renewAfter(loss int, cause GapCause) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if loss == g.losses {
		g.connected = false
	}
	g.renewLocked(cause)
}

// renewLocked swaps the gateway for a new one, not connected yet, which
// loses the call for cause when the old one was connected. Caller holds mu.
func (g *renewableGateway) renewLocked(cause GapCause) {
	g.gw = g.build()
	g.disconnectLocked(gapStart{at: time.Now(), cause: cause})
	g.unwatchLocked()
}

// lost notes that the recorder lost its connection to the call at at, for
// cause, by close, the close or failed read gw handed over, when gw is the
// gateway it connects through, and reports whether it is. When close is
// disgo's refused resume, the loss keeps the close behind it that gw saw.
// It returns the loss's number too, which lostFor takes to name a cause not
// known yet.
func (g *renewableGateway) lost(gw disgovoice.Gateway, at time.Time, cause GapCause, close error) (loss int, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gw != g.gw {
		return 0, false
	}
	if errors.Is(close, errResumeRefused) && g.observed.gw == gw {
		close = g.observed.err
	}
	g.disconnectLocked(gapStart{at: at, cause: cause, close: close, closedBy: gw})
	return g.losses, true
}

// lostFor names cause as the cause of loss, unless loss has one already or
// the gateway has lost the call again since. The gateway may have given way
// to a new one meanwhile, and the cause still names the loss.
func (g *renewableGateway) lostFor(loss int, cause GapCause) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if loss == g.losses && g.loss.cause == causeNotKnown {
		g.loss.cause = cause
	}
}

// disconnectLocked notes the gateway not connected, as start says, when it
// was. Its cause can be causeNotKnown until it is known. A gateway that
// never connected loses the call too: its join completed at a voice server
// Discord has replaced since. The loss keeps the voice server the gateway
// last opened at. Caller holds mu.
func (g *renewableGateway) disconnectLocked(start gapStart) {
	if g.connected || g.loss.at.IsZero() {
		start.server = g.state.Endpoint
		g.connected, g.loss = false, start
		g.losses++
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
// the heartbeat after the one left unanswered, two intervals on, so the
// recorder notices first whenever the interval is longer than this.
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
	g.heartbeat.heard = now
	switch d := data.(type) {
	case disgovoice.GatewayMessageDataHello:
		g.heartbeat.interval, g.heartbeat.answered = time.Duration(d.HeartbeatInterval*float64(time.Millisecond)), now
	case disgovoice.GatewayMessageDataHeartbeatACK:
		g.heartbeat.answered = now
	default:
		return
	}
	g.watchLocked(gw)
}

// watchLocked starts the silence watch over gw afresh from the voice
// server's last answer, while gw is connected and its server has named a
// heartbeat interval. Caller holds mu.
func (g *renewableGateway) watchLocked(gw disgovoice.Gateway) {
	g.heartbeat.stop()
	if !g.connected || g.heartbeat.interval <= 0 {
		return
	}
	g.watches++
	watch := g.watches
	g.heartbeat.timer = time.AfterFunc(time.Until(g.heartbeat.overdue()), func() { g.wentSilent(gw, watch) })
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
	g.heartbeat.stop()
	g.heartbeat = heartbeatWatch{}
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
	w := g.heartbeat
	g.disconnectLocked(gapStart{at: w.heard, cause: GapHeartbeatOverdue, interval: w.interval, sinceAnswer: w.heard.Sub(w.answered)})
	g.unwatchLocked()
	g.mu.Unlock()
	gw.CloseWithCode(websocket.CloseServiceRestart, "heartbeat answer overdue")
	g.onDrop(gw, errVoiceServerSilent)
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
		g.connected, g.connectedWith = true, g.state.Token
		g.watchLocked(gw)
	} else {
		g.disconnectLocked(gapStart{at: time.Now(), cause: GapCallMoved})
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
// voice server Discord named last, when and why it last stopped being
// connected, zero before it ever was, and how many times it has.
func (g *renewableGateway) connection() (connected bool, lost gapStart, losses int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.connected, g.loss, g.losses
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
// that gateway closes, and a new one dials the server named last. A voice
// server that closes the connection before its Ready ends the dialing, and
// one that closes it refusing the identify's token is heard of (refused).
// Each dial is a recovery step, which only a connection at the server
// Discord named last makes succeed.
func (g *renewableGateway) dialUntilOpen(ctx context.Context, state disgovoice.State) error {
	for {
		dial, redial := context.WithCancel(ctx)
		gw, at, described := g.startDial(state, redial)
		step := g.steps.start(StepDial, g.dialTokenOf(at))
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
		ended := func(outcome StepOutcome) { g.steps.end(step, outcome, err) }
		switch {
		case err == nil && ctx.Err() != nil:
			ended(StepFailed)
			return ctx.Err()
		case err == nil && g.namedLastIs(at):
			ended(StepSucceeded)
			return nil
		case err == nil:
			ended(StepFailed)
			g.replace(gw)
		case !redialed:
			if ctx.Err() == nil && refusesIdentify(err) {
				ended(StepRefused)
				g.refused(at, err)
			} else {
				ended(StepFailed)
			}
			return err
		default:
			ended(StepFailed)
		}
	}
}

// dialTokenOf says whether a dial of state dials with the token the gateway
// last connected with.
func (g *renewableGateway) dialTokenOf(state disgovoice.State) DialToken {
	g.mu.Lock()
	defer g.mu.Unlock()
	if state.Token == g.connectedWith {
		return TokenHeld
	}
	return TokenNew
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
		g.renewLocked(GapCallMoved)
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
// dial's connection. A dial while connected follows Discord naming the
// call's voice server again, as when the call moved.
func (g *renewableGateway) startDial(state disgovoice.State, redial context.CancelFunc) (disgovoice.Gateway, disgovoice.State, <-chan struct{}) {
	if e := g.named(); e != nil {
		state.Endpoint, state.Token = *e.Endpoint, e.Token
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// A dial before the gateway ever connected is the join's own, and
	// loses nothing. The loss keeps the server the call moved from.
	if g.connected {
		g.disconnectLocked(gapStart{at: time.Now(), cause: GapCallMoved})
	}
	g.state, g.redial = state, redial
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
// the new connection identifies with. The fresh connection is a recovery
// step.
func (g *renewableGateway) reopen(ctx context.Context) error {
	step := g.steps.start(StepFreshConnection, "")
	g.mu.Lock()
	old, state := g.gw, g.state
	g.renewLocked(GapDAVEStalled)
	g.mu.Unlock()
	old.CloseWithCode(websocket.CloseServiceRestart, "starting DAVE over")
	err := g.Open(ctx, state)
	g.steps.end(step, outcomeOf(err == nil), err)
	return err
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

// watchClose wraps disgo's handler for a voice gateway that dropped: it
// closed, its read failed, or its voice server went silent. The recorder
// can't hear the call from then on. disgo opens a fresh connection when the
// close allows it, and a voice reconnect ends there. Here the recorder's
// rejoin takes that over, through a new gateway, which identifies. The old
// one would resume the voice session, which the rejoin keeps, and a resume
// gets no answer that opens audio (ownDialsOnly). Discord ends a call with
// 4022, "Call terminated", when its channel is deleted or the call moves to
// another voice server, as a region change makes it do. A call that moved
// gets a fresh connection too, unless its channel is gone. Otherwise disgo
// takes the recorder out of the channel itself: the voice connection is
// lost mid-recording, and Sentry hears of it with the close that started
// it. So does Discord taking the recorder out before it is back. A
// connection the recorder closed meanwhile ended with its recording, and
// lost nothing.
func (c *recorderConn) watchClose(gw *renewableGateway, onClose disgovoice.CloseHandlerFunc) disgovoice.CloseHandlerFunc {
	return func(g disgovoice.Gateway, err error) {
		defer utils.RecoverPanic("recorder-voice-close", "recorder", c.s.userID)
		// The recorder can't hear the call from the close on, whatever the
		// lookup below costs. A 4022's lookup says whether the call moved,
		// which names the gap, and any other close names it at once. A
		// gateway the recorder left behind can still hand over a close, as
		// when disgo's heartbeat check fires on one the recorder is closing,
		// and it changes nothing.
		terminated := closedWith(err, disgovoice.GatewayCloseEventCodeCallTerminated.Code)
		cause := GapVoiceGatewayClosed
		if terminated {
			cause = causeNotKnown
		}
		loss, ok := gw.lost(g, time.Now(), cause, err)
		if !ok {
			return
		}
		moved := terminated && c.s.channelExists(gw.channel())
		if terminated {
			cause = GapVoiceGatewayClosed
			if moved {
				cause = GapCallMoved
			}
			gw.lostFor(loss, cause)
		}
		if !moved && !getsFreshConnection(err) {
			c.gaveUp.Store(false)
			onClose(g, err)
			if c.gaveUp.Load() && !c.closed.Load() {
				utils.CaptureError(lostConnectionMessage, err, "recorder", c.s.userID)
			}
			return
		}
		if moved {
			c.callMoved()
		}
		gw.renewAfter(loss, cause)
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

// refusesIdentify reports whether err is a voice gateway close that refuses
// the identify's token or session: 4004, 4006 or 4009. Nobody has seen
// which one Discord sends for a token it no longer takes. Another close
// before Ready, such as 4014 when a moderator kicks the recorder, isn't one
// a fresh join answers.
func refusesIdentify(err error) bool {
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		return false
	}
	switch closeErr.Code {
	case disgovoice.GatewayCloseEventCodeAuthenticationFailed.Code,
		disgovoice.GatewayCloseEventCodeSessionNoLongerValid.Code,
		disgovoice.GatewayCloseEventCodeSessionTimeout.Code:
		return true
	}
	return false
}

// closedWith reports whether err is a voice gateway close with code.
func closedWith(err error, code int) bool {
	var closeErr *websocket.CloseError
	return errors.As(err, &closeErr) && closeErr.Code == code
}

func (s *discordgoSession) UserID() string { return s.userID }

// LogOut logs the recorder out of this login, for a relogin. Its leave has
// already taken it out of the call, so the logout waits on no voice
// connection. The recorder is offline from now until a login's READY, and
// in a gateway outage all that time, though the bot chose it. A login
// discordgo brings back after the logout is logged out again at once.
func (s *discordgoSession) LogOut() error {
	s.loggedOut.Store(true)
	s.watch.Offline()
	_, err := s.watch.CloseSession()
	return err
}

// cameBack logs a login that came back after its logout out again, with
// 1000. discordgo had been reconnecting it, and its reconnects end once one
// works, so this close ends them, and takes the recorder out of the call
// the login was in. The logout turned discordgo's reconnects off, so none
// follows this close.
func (s *discordgoSession) cameBack() {
	defer utils.RecoverPanic("recorder-logout", "recorder", s.userID)
	if !s.loggedOut.Load() {
		return
	}
	if err := s.dg.Close(); err != nil {
		utils.Warn("Recorder did not close cleanly", "user_id", s.userID, "error", err)
	}
}

// Close takes the recorder out of any voice channel it is still in, then
// ends its session, at shutdown. The close is on purpose, so its watch
// stands down first.
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
// call, at the voice server Discord named last, when and why it last
// stopped being connected, zero before it ever was, and how many times it
// has.
func (c callConn) connection() (connected bool, lost gapStart, losses int) {
	g, ok := c.renewable()
	if !ok {
		return false, gapStart{}, 0
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

// sessionOnline reports whether the recorder's own gateway session is
// online, so that a voice state update could go out.
func (c callConn) sessionOnline() bool {
	rc, ok := c.Conn.(*recorderConn)
	return ok && rc.s.online()
}

// recovery is the log of the recorder connection's recovery steps, and an
// empty one on a connection the recorder's connection options didn't
// build.
func (c callConn) recovery() *recoveryLog {
	if rc, ok := c.Conn.(*recorderConn); ok {
		return &rc.steps
	}
	return new(recoveryLog)
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

// OwnLeaves is how many times the recorder has left the channel on its own
// to join it again.
func (c *disgoConn) OwnLeaves() int {
	rc, ok := c.conn.(*recorderConn)
	if !ok {
		return 0
	}
	return int(rc.ownLeaves.Load())
}

// GapReport is what the recorder knows now of the gap it last told of,
// empty before the receiver starts.
func (c *disgoConn) GapReport() GapReport {
	c.mu.Lock()
	rcv := c.rcv
	c.mu.Unlock()
	if rcv == nil {
		return GapReport{}
	}
	return rcv.report()
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

// online reports whether the session's gateway connection is up, which a
// voice state update needs.
func (s *discordgoSession) online() bool {
	s.dg.RLock()
	defer s.dg.RUnlock()
	return s.dg.DataReady
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
