package voice

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/snowflake/v2"
)

// The recorder's voice state, as discordgo hands it over, reaches disgo with
// what disgo connects and reconnects on: the guild, the recorder, the session,
// whether it muted or deafened itself, and its channel.
func TestVoiceStateEventCarriesWhatDisgoConnectsOn(t *testing.T) {
	ev, ok := voiceStateEvent(&discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: "100", ChannelID: "200", UserID: "300", SessionID: "sess-1",
		SelfMute: true, SelfDeaf: false,
	}})
	if !ok {
		t.Fatal("voiceStateEvent dropped a well-formed event")
	}
	vs := ev.VoiceState
	if vs.GuildID != snowflake.ID(100) || vs.UserID != snowflake.ID(300) || vs.SessionID != "sess-1" {
		t.Errorf("event = %+v, want guild 100, user 300, session sess-1", vs)
	}
	if vs.ChannelID == nil || *vs.ChannelID != snowflake.ID(200) {
		t.Errorf("channel = %v, want 200", vs.ChannelID)
	}
	if !vs.SelfMute || vs.SelfDeaf {
		t.Errorf("self mute, deaf = %v, %v, want true, false", vs.SelfMute, vs.SelfDeaf)
	}

	ev, ok = voiceStateEvent(&discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: "100", UserID: "300", SelfDeaf: true,
	}})
	if !ok {
		t.Fatal("voiceStateEvent dropped a leave")
	}
	if ev.ChannelID != nil {
		t.Errorf("channel of a leave = %v, want none", *ev.ChannelID)
	}
	if !ev.SelfDeaf {
		t.Error("self deaf = false, want true")
	}
}

// The voice server reaches disgo with its token, guild and endpoint, and an
// endpoint Discord left out while it moves the voice server reaches it as
// none, so disgo waits for the next one.
func TestVoiceServerEventCarriesTheEndpointOrNone(t *testing.T) {
	ev, ok := voiceServerEvent(&discordgo.VoiceServerUpdate{Token: "tok", GuildID: "100", Endpoint: "c-dfw.discord.media:443"})
	if !ok {
		t.Fatal("voiceServerEvent dropped a well-formed event")
	}
	if ev.Token != "tok" || ev.GuildID != snowflake.ID(100) || ev.Endpoint == nil || *ev.Endpoint != "c-dfw.discord.media:443" {
		t.Errorf("event = %+v, want token tok, guild 100, the endpoint", ev)
	}

	ev, ok = voiceServerEvent(&discordgo.VoiceServerUpdate{Token: "tok", GuildID: "100"})
	if !ok {
		t.Fatal("voiceServerEvent dropped an event with no endpoint")
	}
	if ev.Endpoint != nil {
		t.Errorf("endpoint = %q, want none", *ev.Endpoint)
	}
}
