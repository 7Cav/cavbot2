package replies

import "github.com/bwmarrin/discordgo"

// poster posts to channels the way the guild manager and the temp VC manager
// do.
type poster interface {
	ChannelMessageSend(channelID, content string) error
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error)
	ChannelMessageEditComplex(edit *discordgo.MessageEdit) (*discordgo.Message, error)
}

func postsTheError(p poster, err error) {
	_ = p.ChannelMessageSend("1", "❌ Failed: "+err.Error()) // want "."
}

func postsTheErrorComplex(p poster, err error) {
	_, _ = p.ChannelMessageSendComplex("1", &discordgo.MessageSend{Content: err.Error()}) // want "."
}

func editsAPostToTheError(p poster, err error) {
	content := "❌ Failed: " + err.Error()
	_, _ = p.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: "2", Channel: "1", Content: &content}) // want "."
}

func postsTheErrorThroughTheSession(s *discordgo.Session, err error) {
	_, _ = s.ChannelMessageSend("1", "❌ Failed: "+err.Error()) // want "."
}

func renamesAChannelToTheError(s *discordgo.Session, err error) {
	_, _ = s.ChannelEdit("1", &discordgo.ChannelEdit{Name: "vc " + err.Error()}) // want "Discord"
}

// renamer renames channels the way the temp VC manager does, with the audit
// log reason as its own argument.
type renamer interface {
	ChannelEdit(channelID string, data *discordgo.ChannelEdit, auditReason string) (*discordgo.Channel, error)
}

func renamesAChannelToTheErrorThroughAManager(m renamer, err error) {
	_, _ = m.ChannelEdit("1", &discordgo.ChannelEdit{Name: "vc " + err.Error()}, "renamed by /voice-rename") // want "Discord"
}

// channelMaker creates channels the way the temp VC manager does, with the
// audit log reason as its own argument.
type channelMaker interface {
	GuildChannelCreateComplex(guildID string, data discordgo.GuildChannelCreateData, auditReason string) (*discordgo.Channel, error)
}

func givesTheErrorAsAChannelsAuditLogReason(m channelMaker, err error) {
	_, _ = m.GuildChannelCreateComplex("1", discordgo.GuildChannelCreateData{Name: "vc"}, "spawn failed: "+err.Error()) // want "Discord"
}

func renamesAChannelToTheErrorThroughAMethodValue(m renamer, err error) {
	edit := m.ChannelEdit
	_, _ = edit("1", &discordgo.ChannelEdit{Name: "vc " + err.Error()}, "renamed by /voice-rename") // want "Discord"
}
