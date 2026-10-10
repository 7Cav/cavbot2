package voice

import (
	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
)

// voiceStateEvent translates a recorder's own VOICE_STATE_UPDATE from
// discordgo to disgo, which needs one to connect to the voice gateway. An
// empty channel is the recorder leaving. disgo reopens a dropped voice
// connection with the self mute and deafen the last one carried, so both go
// across. false when an ID doesn't parse.
func voiceStateEvent(e *discordgo.VoiceStateUpdate) (gateway.EventVoiceStateUpdate, bool) {
	guildID, err := snowflake.Parse(e.GuildID)
	if err != nil {
		return gateway.EventVoiceStateUpdate{}, false
	}
	userID, err := snowflake.Parse(e.UserID)
	if err != nil {
		return gateway.EventVoiceStateUpdate{}, false
	}
	var channelID *snowflake.ID
	if e.ChannelID != "" {
		id, err := snowflake.Parse(e.ChannelID)
		if err != nil {
			return gateway.EventVoiceStateUpdate{}, false
		}
		channelID = &id
	}
	return gateway.EventVoiceStateUpdate{VoiceState: discord.VoiceState{
		GuildID:   guildID,
		ChannelID: channelID,
		UserID:    userID,
		SessionID: e.SessionID,
		SelfMute:  e.SelfMute,
		SelfDeaf:  e.SelfDeaf,
	}}, true
}

// voiceServerEvent translates a VOICE_SERVER_UPDATE from discordgo to disgo,
// the other event disgo needs to connect. Discord sends no endpoint while it
// moves the voice server, and disgo waits for one then. false when the guild
// ID doesn't parse.
func voiceServerEvent(e *discordgo.VoiceServerUpdate) (gateway.EventVoiceServerUpdate, bool) {
	guildID, err := snowflake.Parse(e.GuildID)
	if err != nil {
		return gateway.EventVoiceServerUpdate{}, false
	}
	var endpoint *string
	if e.Endpoint != "" {
		endpoint = &e.Endpoint
	}
	return gateway.EventVoiceServerUpdate{Token: e.Token, GuildID: guildID, Endpoint: endpoint}, true
}
