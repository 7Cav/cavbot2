package commands

import (
	"errors"
	"fmt"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Presses on the lock notice's buttons (spec #347, #351). main.go routes a
// press here through /voice-lock's handler by the CustomID's first segment
// (ADR 0007); temp_vc_lock_notice.go builds the notice and its CustomIDs.
//
// Every answer is a new ephemeral message: a deferred ephemeral response,
// then an edit of it. Never utils.HandleError, and never an update of the
// message the button sits on: either would overwrite the public notice for
// everyone.

// lockNoticeNotPosted is added to the invoker's /voice-lock reply when the
// lock stands but its notice did not post.
const lockNoticeNotPosted = " The lock notice couldn't be posted in the channel's chat, so unlock it with /" + voiceUnlockCommandName + "."

// runLockNoticeComponent answers a press on a lock notice button, on the
// channel its CustomID names.
func runLockNoticeComponent(r utils.InteractionResponder, tv *TempVC, interaction *discordgo.InteractionCreate) {
	customID := interaction.MessageComponentData().CustomID
	if err := deferEphemeral(r, interaction); err != nil {
		captureError("Failed to acknowledge a lock notice button", err,
			"command", voiceLockCommandName, "custom_id", customID, "guild_id", interaction.GuildID)
		return
	}

	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting LockNoticeButton", "command", voiceLockCommandName,
		"username", username, "discord_id", discordID, "custom_id", customID)

	if tv == nil {
		utils.Warn("Lock notice button refused, no bot store configured",
			"custom_id", customID, "discord_id", discordID)
		editNoticeReply(r, interaction, "❌ Temporary voice channels are not enabled on this bot.")
		return
	}

	var roles []string
	if interaction.Member != nil {
		roles = interaction.Member.Roles
	}
	action, channelID, ok := parseLockNoticeCustomID(customID)
	switch {
	case ok && action == lockNoticeUnlock:
		if _, err := tv.unlockFromNotice(channelID, discordID, roles); err != nil {
			editNoticeReply(r, interaction, lockNoticeRefusal(err, discordID))
			return
		}
		editNoticeReply(r, interaction, voiceUnlock.done)
	default:
		// A button from an older build whose action is gone.
		utils.Warn("Lock notice button not recognised", "custom_id", customID, "discord_id", discordID)
		editNoticeReply(r, interaction, "❌ This button no longer does anything. Use /"+voiceUnlockCommandName+" instead.")
		return
	}
	utils.Info("✨ Done!", "command", voiceLockCommandName)
}

// lockNoticeRefusal renders a refused press as the presser's reply. A
// presser outside the buttons' rule is pointed at /voice-unlock, which an
// owner may run. Every other refusal reads as /voice-unlock's.
func lockNoticeRefusal(err error, discordID string) string {
	if errors.Is(err, errNotLockerOrModerator) {
		return fmt.Sprintf("❌ Only whoever locked this channel or a moderator can use this button. The channel's owner can unlock it with /%s.", voiceUnlockCommandName)
	}
	return lockRefusal(err, voiceUnlock, discordID)
}

// editNoticeReply fills in the ephemeral reply to a press. A lost reply is
// captured. editEphemeral cannot serve here: its capture reads the
// interaction as a slash command's, which panics on a button's.
func editNoticeReply(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, content string) {
	if err := r.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{Content: &content}); err != nil {
		captureError("Failed to deliver deferred-ephemeral edit", err,
			"command", voiceLockCommandName,
			"custom_id", interaction.MessageComponentData().CustomID,
			"guild_id", interaction.GuildID)
	}
}
