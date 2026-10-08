package commands

import (
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// lookupFailedReply answers a command whose 7Cav API lookup failed.
const lookupFailedReply = "❌ Couldn't get that from the 7Cav API. Try again in a few minutes."

// replyLookupFailed answers a command whose 7Cav API lookup failed. The
// error goes to Sentry under the command's registered name, and the member
// gets lookupFailedReply, never the error's text.
func replyLookupFailed(r utils.InteractionResponder, i *discordgo.InteractionCreate, command string, err error) {
	captureError("7Cav API lookup failed", err, "command", command, "guild_id", i.GuildID)
	utils.HandleError(r, i, lookupFailedReply)
}
