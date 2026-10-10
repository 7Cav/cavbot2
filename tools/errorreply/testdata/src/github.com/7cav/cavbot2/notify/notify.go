// Package notify stands in for a package of the module whose functions send
// a member a message they're given, so the message reaches Discord through a
// call into another package.
package notify

import (
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Reply sends message to the member.
func Reply(message string) {
	utils.HandleError(nil, nil, message)
}

// Post posts message to the bot's channel.
func Post(s *discordgo.Session, message string) {
	_, _ = s.ChannelMessageSend("1", message)
}

// Notifier sends members messages.
type Notifier struct{}

// Reply sends message to the member.
func (Notifier) Reply(message string) {
	utils.HandleError(nil, nil, message)
}
