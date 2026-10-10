package voice

import (
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// recorderIntents are the only intents a recorder's session asks for (ADR
// 0014): the guilds it is in and who is in which voice channel. Neither is
// privileged, so a recorder application needs no privileged intent turned
// on, and Discord would close a session asking for one it lacks.
const recorderIntents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates

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
	return discordgoSession{dg}, nil
}

// discordgoSession is a recorder's discordgo session.
type discordgoSession struct {
	dg *discordgo.Session
}

func (s discordgoSession) UserID() string { return s.dg.State.User.ID }
func (s discordgoSession) Close() error   { return s.dg.Close() }
