// Package relay stands in for a package of the bot that posts through an
// interface of its own and imports nothing of discordgo.
package relay

// poster posts to a channel the way a manager that wraps the session does.
type poster interface {
	ChannelMessageSend(channelID, content string) error
}

func postsTheError(p poster, err error) {
	_ = p.ChannelMessageSend("1", "❌ Failed: "+err.Error()) // want "Discord"
}
