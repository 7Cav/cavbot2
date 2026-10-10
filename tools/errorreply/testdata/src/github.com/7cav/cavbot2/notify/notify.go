// Package notify stands in for a package of the module whose functions send
// a member a message they're given, so the message reaches Discord through a
// call into another package.
package notify

import (
	"github.com/7cav/cavbot2/state"
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

// Remember keeps reason as the last refusal's.
func Remember(reason string) {
	state.LastRefusal = reason
}

// Refusal is a refusal the bot explains to a member.
type Refusal struct {
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

// Refuse returns a refusal for reason.
func Refuse(reason string) *Refusal {
	return &Refusal{Reason: reason}
}

// Explain sends the member the refusal's reason.
func Explain(r *Refusal) {
	utils.HandleError(nil, nil, r.Error())
}
