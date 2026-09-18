package commands

import (
	"errors"
	"fmt"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// /voice-rename (spec #285, #292): the owner of a spawned channel, or a member
// holding a moderator role of its hub, renames the channel they are sitting
// in. One required string option, the new name. Every reply is ephemeral,
// through the deferred-ephemeral pattern /warden uses.
//
// The Cav-member gate is not in code: it is a Server Settings command
// restriction naming the 29 rank roles, applied by the maintainer at deploy
// before the hubs are enabled (README, Temporary voice channels).

// voiceRenameCommandName is the registered slash-command name, the value the
// telemetry line and every capture carry under "command".
const voiceRenameCommandName = "voice-rename"

// VoiceRename declares the command over a runtime. The registry adds it only
// when a runtime exists: with no bot store there are no spawned channels, and
// a command that could only refuse would be noise.
func VoiceRename(tv *TempVC) Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        voiceRenameCommandName,
			Description: "Rename the voice channel you are in",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "name",
					Description: "The new channel name",
					Required:    true,
				},
			},
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			runVoiceRename(utils.NewSessionResponder(s), tv, i)
		},
	}
}

// runVoiceRename is the handler behind the responder seam (ADR 0004). It
// defers an ephemeral reply first, so every outcome below is an edit of that
// one reply and nothing the invoker sees is public.
func runVoiceRename(r utils.InteractionResponder, tv *TempVC, interaction *discordgo.InteractionCreate) {
	if err := deferEphemeral(r, interaction); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to acknowledge: %v", err))
		return
	}

	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting VoiceRename", "command", voiceRenameCommandName, "username", username, "discord_id", discordID)

	name, _ := getOptionString(interaction.ApplicationCommandData(), "name")

	var roles []string
	if interaction.Member != nil {
		roles = interaction.Member.Roles
	}
	res, err := tv.Rename(discordID, roles, name)
	if err != nil {
		editEphemeral(r, interaction, renameRefusal(err, discordID))
		return
	}

	utils.Info("Temp VC renamed",
		"command", voiceRenameCommandName, "username", username, "discord_id", discordID,
		"channel_id", res.ChannelID, "before", res.Before, "after", res.After)
	editEphemeral(r, interaction, fmt.Sprintf("✅ Renamed to **%s**.", res.After))
	utils.Info("✨ Done!", "command", voiceRenameCommandName)
}

// renameRefusal renders a failed Rename as the invoker's ephemeral reply. The
// no-owner refusal is also a WARN line: the invoker reached the command inside
// a spawned channel nobody owns, so the Server Settings gate or the rank-role
// assumption has failed. No reply carries a raw Discord body.
func renameRefusal(err error, discordID string) string {
	var notOwner *notOwnerError
	switch {
	case errors.Is(err, errNotInSpawnedChannel):
		return "❌ Join a spawned voice channel first. This command renames the channel you are in."
	case errors.As(err, &notOwner) && notOwner.Owner != "":
		return fmt.Sprintf("❌ Only the owner can rename this channel. Ask <@%s>.", notOwner.Owner)
	case errors.As(err, &notOwner):
		utils.Warn("Temp VC rename refused, channel has no owner",
			"command", voiceRenameCommandName, "discord_id", discordID)
		return "❌ This channel has no owner, so it cannot be renamed."
	default:
		return "❌ Could not rename the channel."
	}
}
