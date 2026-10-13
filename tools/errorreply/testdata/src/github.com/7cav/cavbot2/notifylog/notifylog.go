// Package notifylog logs a line and also posts it to a Discord channel, so
// what a caller hands it reaches more than the log.
package notifylog

import (
	"fmt"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

func Warn(s *discordgo.Session, msg string, args ...any) {
	utils.Info(msg, args...)
	_, _ = s.ChannelMessageSend("1", fmt.Sprint(args...))
}
