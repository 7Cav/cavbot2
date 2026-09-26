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
// in, on a hub with "Renaming allowed" on (#360). One required string option, the new name. Every reply is ephemeral,
// through the deferred-ephemeral pattern /warden uses.
//
// No code limits the command to Cav members: like every other command, that
// is a Server Settings restriction (docs/temp-vc-decisions.md, #279).

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
		replyAckFailed(r, interaction, err)
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

	result, err := tv.Rename(Invoker{UserID: discordID, Username: username, Roles: interactionRoles(interaction)}, name)
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

// voiceAction names what a voice command does, for the refusals the voice
// commands share (voiceChannelRefusal).
type voiceAction struct {
	// command is the registered command name.
	command string
	// verb and past name the action: "rename" and "renamed".
	verb, past string
}

// renameAction is /voice-rename's action.
var renameAction = voiceAction{command: voiceRenameCommandName, verb: "rename", past: "renamed"}

// channelGoneRefusal answers a voice command whose channel no longer
// exists.
const channelGoneRefusal = "❌ This channel no longer exists."

// voiceChannelRefusal renders the refusals /voice-rename, /voice-lock and
// /voice-unlock share, in the words /voice-rename has always used, so the
// voice commands refuse alike (#347). ok is false for any other error, which
// the command renders itself. The no-owner refusal is also a WARN line: the
// invoker reached the command inside a spawned channel nobody owns, so the
// Server Settings gate or the rank-role assumption has failed.
func voiceChannelRefusal(err error, a voiceAction, discordID string) (reply string, ok bool) {
	var (
		notSpawned *notSpawnedChannelError
		notOwner   *notOwnerError
	)
	switch {
	case errors.Is(err, errNotInVoice):
		return fmt.Sprintf("❌ Join the voice channel you want to %s, then run /%s again.", a.verb, a.command), true
	case errors.As(err, &notSpawned):
		return fmt.Sprintf("❌ Only channels created by joining a hub can be %s. <#%s> is not one.", a.past, notSpawned.ChannelID), true
	case errors.As(err, &notOwner) && notOwner.Owner != "":
		return fmt.Sprintf("❌ Only the owner can %s this channel. Ask <@%s>.", a.verb, notOwner.Owner), true
	case errors.As(err, &notOwner):
		utils.Warn("Temp VC "+a.verb+" refused, channel has no owner",
			"command", a.command, "discord_id", discordID)
		return fmt.Sprintf("❌ This channel has no owner, so it cannot be %s.", a.past), true
	case errors.Is(err, errChannelGone):
		return channelGoneRefusal, true
	default:
		return "", false
	}
}

// renameRefusal renders a failed Rename as the invoker's ephemeral reply,
// the shared refusals first. No reply carries a raw Discord body.
func renameRefusal(err error, discordID string) string {
	if reply, ok := voiceChannelRefusal(err, renameAction, discordID); ok {
		return reply
	}
	var window *renameWindowError
	switch {
	case errors.Is(err, errRenamingNotAllowed):
		return "❌ Renaming isn't turned on for this hub."
	case errors.As(err, &window):
		// Discord renders <t:UNIX:R> as "in 4 minutes". The runtime returns
		// this for its own refusal and for a 429 that carries retry_after.
		return fmt.Sprintf("❌ This channel was renamed twice in the last ten minutes. Try again <t:%d:R>.", window.OpensAt.Unix())
	case classifySpawnedChannelError(err).rateLimited:
		// A 429 with no retry_after. The runtime's own count did not see a
		// rename Discord did (one made in Discord's UI, or before a restart),
		// so Discord refused, and gave no wait to render.
		return "❌ Discord is rate limiting renames of this channel. Try again in a few minutes."
	default:
		// The warden classifier's phrase, never the raw body. Unknown Channel
		// never reaches it: the runtime classifies that code first.
		return fmt.Sprintf("❌ Could not rename the channel: %s.", classifyDiscordError(err).UserDetail)
	}
}
