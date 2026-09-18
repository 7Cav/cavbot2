package commands

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// /voice-rename (spec #285, #292): the owner of a spawned channel, or a member
// holding a moderator role of its hub, renames the channel they are sitting
// in. One required string option, the new name. Every reply is ephemeral,
// through the deferred-ephemeral pattern /warden uses.
//
// No code limits the command to Cav members. The maintainer applies a Server
// Settings command restriction naming the 29 rank roles at deploy, before the
// hubs are enabled (README, Commands).

// voiceRenameCommandName is the registered slash-command name, the value the
// telemetry line and every capture carry under "command".
const voiceRenameCommandName = "voice-rename"

// VoiceRename declares the command over a runtime, nil on a host with no bot
// store (see NewRegistry for why the command exists anyway).
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

	if tv == nil {
		utils.Warn("Temp VC rename refused, no bot store configured",
			"command", voiceRenameCommandName, "discord_id", discordID)
		editEphemeral(r, interaction, "❌ Temporary voice channels are not enabled on this bot.")
		return
	}

	// Trimmed, then bounded by Discord's channel name limit, counted in
	// characters. No other filter. The Code of Conduct polices abuse.
	name, _ := getOptionString(interaction.ApplicationCommandData(), "name")
	name = strings.TrimSpace(name)
	if name == "" {
		editEphemeral(r, interaction, "❌ `name` must not be empty.")
		return
	}
	if utf8.RuneCountInString(name) > discordChannelNameLimit {
		editEphemeral(r, interaction, fmt.Sprintf("❌ `name` must be at most %d characters.", discordChannelNameLimit))
		return
	}

	var roles []string
	if interaction.Member != nil {
		roles = interaction.Member.Roles
	}
	result, err := tv.Rename(discordID, roles, name)
	if err != nil {
		editEphemeral(r, interaction, renameRefusal(err, discordID))
		return
	}

	utils.Info("Temp VC renamed",
		"command", voiceRenameCommandName, "username", username, "discord_id", discordID,
		"channel_id", result.ChannelID, "before", result.Before, "after", result.After)
	editEphemeral(r, interaction, fmt.Sprintf("✅ Renamed to **%s**.", result.After))
	utils.Info("✨ Done!", "command", voiceRenameCommandName)
}

// renameRefusal renders a failed Rename as the invoker's ephemeral reply. The
// no-owner refusal is also a WARN line: the invoker reached the command inside
// a spawned channel nobody owns, so the Server Settings gate or the rank-role
// assumption has failed. No reply carries a raw Discord body.
func renameRefusal(err error, discordID string) string {
	var (
		notOwner *notOwnerError
		window   *renameWindowError
	)
	switch {
	case errors.Is(err, errNotInSpawnedChannel):
		return "❌ Join a spawned voice channel first. This command renames the channel you are in."
	case errors.As(err, &notOwner) && notOwner.Owner != "":
		return fmt.Sprintf("❌ Only the owner can rename this channel. Ask <@%s>.", notOwner.Owner)
	case errors.As(err, &notOwner):
		utils.Warn("Temp VC rename refused, channel has no owner",
			"command", voiceRenameCommandName, "discord_id", discordID)
		return "❌ This channel has no owner, so it cannot be renamed."
	case errors.As(err, &window):
		// Discord renders <t:UNIX:R> as "in 4 minutes".
		return fmt.Sprintf("❌ This channel was renamed twice in the last ten minutes. Try again <t:%d:R>.", window.OpensAt.Unix())
	case errors.Is(err, errChannelGone):
		return "❌ This channel no longer exists."
	case classifySpawnedChannelError(err).rateLimited:
		// The runtime's own count did not see a rename Discord did (one made
		// in Discord's UI, or before a restart), so Discord refused.
		return "❌ Discord is rate limiting renames of this channel. Try again in a few minutes."
	default:
		// The warden classifier's phrase, never the raw body. Unknown Channel
		// never reaches it: the runtime classifies that code first.
		return fmt.Sprintf("❌ Could not rename the channel: %s.", classifyDiscordError(err).UserDetail)
	}
}
