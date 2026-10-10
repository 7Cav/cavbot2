package voice

import (
	"context"
	"fmt"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
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

// OpenGateway is the production Opener: a discordgo session for the recorder
// account, open once READY has named it. A session that opened without
// reaching READY is closed again, so a failed recorder holds no connection.
func OpenGateway(token string) (GatewaySession, error) {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	dg.LogLevel = utils.DiscordgoLogLevel()
	dg.Identify.Intents = recorderIntents
	if err := utils.OpenSession(dg); err != nil {
		_ = dg.Close()
		return nil, err
	}
	userID, err := snowflake.Parse(dg.State.User.ID)
	if err != nil {
		_ = dg.Close()
		return nil, err
	}
	s := &discordgoSession{dg: dg, userID: dg.State.User.ID}
	// Closures rather than method values here and below: the gate's error
	// data check follows a closure, and not a method value.
	updateVoiceState := func(ctx context.Context, guildID snowflake.ID, channelID *snowflake.ID, selfMute, selfDeaf bool) error {
		return s.updateVoiceState(ctx, guildID, channelID, selfMute, selfDeaf)
	}
	s.voice = disgovoice.NewManager(updateVoiceState, userID,
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
}

func (s *discordgoSession) UserID() string { return s.userID }

// Close takes the recorder out of any voice channel it is still in, then
// ends its session.
func (s *discordgoSession) Close() error {
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
	return disgoConn{conn}, nil
}

// disgoConn is a recorder's disgo voice connection.
type disgoConn struct {
	conn disgovoice.Conn
}

// Leave sends the voice state update that takes the recorder out, and closes
// the voice gateway, UDP and DAVE session.
func (c disgoConn) Leave(ctx context.Context) { c.conn.Close(ctx) }

// updateVoiceState is disgo's StateUpdateFunc: the voice state update on the
// recorder's own gateway. discordgo's ChannelVoiceJoinManual sends exactly
// that without starting discordgo's own voice code. No channel leaves.
// discordgo writes to its websocket without checking that one is open, and
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
