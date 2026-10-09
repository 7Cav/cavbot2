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
