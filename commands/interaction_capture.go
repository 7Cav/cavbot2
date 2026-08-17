package commands

import "github.com/bwmarrin/discordgo"

// captureDeferredEditFailure handles a failed follow-up edit on an interaction
// that has ALREADY been acknowledged (an immediate Respond or a defer). Because
// the interaction is acknowledged, the identical InteractionResponseEdit cannot
// be retried and a fresh InteractionRespond would only be rejected as
// already-acknowledged — that is the dead-end loop utils.HandleError walks into
// on a post-ack edit failure (Respond → already-acknowledged → re-issue the same
// failing edit). So instead of retrying, we treat the lost reply as a genuine
// delivery failure and capture it to Sentry with the command name and the guild
// read off the interaction, so on-call can attribute it even though the command
// may already have done its work.
//
// This is the non-warden sibling of warden.go's captureEditFailure: warden reads
// its subcommand off the `command` option, whereas these commands each pass their
// own fixed command name. Both funnel through the same captureError seam and
// record the same {command, guild_id} context shape.
//
// `command` must be the REGISTERED slash-command name (`milpac`, `s6-it-check`),
// not a display name. utils.CaptureError promotes it to a Sentry tag, so a
// display name here would group the same command's failures separately from the
// ones the telemetry decorator attributes. See docs/command-telemetry.md.
func captureDeferredEditFailure(interaction *discordgo.InteractionCreate, command string, err error) {
	guildID := ""
	if interaction != nil {
		guildID = interaction.GuildID
	}
	captureError(
		"Failed to deliver post-acknowledgement interaction edit",
		err,
		"command", command,
		"guild_id", guildID,
	)
}
