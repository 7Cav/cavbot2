package commands

import (
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// S3AARDisabled stands in for S3AAR in the registry while /s3aar is disabled
// due to disuse, ahead of its removal. It keeps the name: the startup sync
// deletes any guild command the registry lacks, and Discord keys command
// permissions by command ID, so a deleted and re-created /s3aar would shed its
// Server Settings restriction. S3AAR stays in the tree, so restoring it is one
// registry line.
func S3AARDisabled() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "s3aar",
			Description: "Disabled due to disuse. Removal on " + s3aarRemovalDate + ". Open an S6 ticket if you still need it.",
		},
		Handler: handleS3aarDisabled,
	}
}

func handleS3aarDisabled(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runS3aarDisabled(utils.NewSessionResponder(s), i)
}

// s3aarRemovalDate is the earliest date /s3aar is removed, written the
// regiment's way. The picker and the reply both name it, so a postponement is
// one edit here.
const s3aarRemovalDate = "01DEC26"

// s3aarDisabledNotice is what a member who runs /s3aar sees.
const s3aarDisabledNotice = "/s3aar has been disabled due to disuse and will be removed on " + s3aarRemovalDate +
	". If you still need it, open an S6 ticket."

func runS3aarDisabled(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	username, discordID := interactionUsernameAndID(i)
	utils.Info("🚀 Starting S3 AAR (disabled)", "command", "S3AAR", "username", username, "discord_id", discordID)

	// Deferred like the other ephemeral commands (ADR 0004), so a missed
	// acknowledgement is reported with its timings.
	if err := deferEphemeral(r, i); err != nil {
		replyAckFailed(r, i, err)
		return
	}
	editEphemeral(r, i, s3aarDisabledNotice)
	utils.Info("✨ Done!", "command", "S3AAR")
}
