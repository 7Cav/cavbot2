package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Presses on the lock notice's buttons (spec #347, #351), and submissions
// of the member picker Let someone in opens (#352). main.go routes both
// here through /voice-lock's handler by the CustomID's first segment (ADR
// 0007); temp_vc_lock_notice.go builds the notice and its CustomIDs, and
// temp_vc_let_in.go the picker.
//
// Every answer is a new ephemeral message: a deferred ephemeral response,
// then an edit of it. Never utils.HandleError, and never an update of the
// message the component sits on: either would overwrite the public notice
// for everyone. The Let someone in press fills its answer with the picker.

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

	by := Invoker{UserID: discordID, Roles: interactionRoles(interaction)}
	action, channelID, ok := parseLockNoticeCustomID(customID)
	switch {
	case ok && action == lockNoticeUnlock:
		if _, err := tv.unlockFromNotice(channelID, by); err != nil {
			editNoticeReply(r, interaction, lockNoticeRefusal(err, discordID))
			return
		}
		editNoticeReply(r, interaction, voiceUnlock.done)
	case ok && action == lockNoticeLetIn:
		if err := tv.letInAllowed(channelID, by); err != nil {
			editNoticeReply(r, interaction, letInRefusal(err))
			return
		}
		picker, err := letInPicker(channelID)
		if err != nil {
			utils.Warn("Let someone in picker not built", "channel_id", channelID, "discord_id", discordID, "error", err)
			editNoticeReply(r, interaction, "❌ Couldn't open the member picker for this channel. Ask a moderator to drag the member in.")
			return
		}
		content := fmt.Sprintf("Pick up to %d members to let into <#%s>. Anyone who can't see the channel is skipped.", letInMaxPicks, channelID)
		sendNoticeReply(r, interaction, &discordgo.WebhookEdit{Content: &content, Components: &picker})
	case ok && action == lockNoticeLetInPick:
		res, err := tv.letIn(channelID, by, letInPicks(interaction.MessageComponentData()))
		if err != nil {
			editNoticeReply(r, interaction, letInRefusal(err))
			return
		}
		editNoticeReply(r, interaction, letInReply(res))
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

// letInRefusal renders a let-in refused before it reached anyone, at the
// button or at the picker. The not-locker refusal has no /voice-unlock
// hint: the command unlocks and lets nobody in.
func letInRefusal(err error) string {
	switch {
	case errors.Is(err, errNotLockerOrModerator):
		return "❌ Only whoever locked this channel or a moderator can let someone in."
	case errors.Is(err, errNotLocked):
		return notLockedRefusal
	case errors.Is(err, errChannelGone):
		return channelGoneRefusal
	default:
		return "❌ Couldn't let anyone in. Try again in a moment."
	}
}

// letInSkipText is how the presser's reply gives a skip reason.
var letInSkipText = map[letInSkipReason]string{
	skipBot:       "a bot",
	skipGuest:     "already a guest",
	skipCannotSee: "can't see this channel",
}

// letInReply renders a let-in's outcome for the presser: who got in, who
// was skipped and why, and who did not get in. The mentions render in an
// ephemeral message and ping nobody.
func letInReply(res letInResult) string {
	var lines []string
	if len(res.LetIn) > 0 {
		lines = append(lines, "✅ Let in "+mentionList(res.LetIn)+".")
	}
	for _, s := range res.Skipped {
		lines = append(lines, fmt.Sprintf("➖ Skipped <@%s>: %s.", s.UserID, letInSkipText[s.Reason]))
	}
	if len(res.Failed) > 0 {
		lines = append(lines, "❌ Couldn't let in "+mentionList(res.Failed)+". Try again in a moment.")
	}
	if res.PingFailed {
		lines = append(lines, "The ping couldn't be posted in the channel's chat, so let them know they can join.")
	}
	if len(lines) == 0 {
		return "Nobody was picked."
	}
	return strings.Join(lines, "\n")
}

// editNoticeReply fills in the ephemeral reply to a press with content
// alone (sendNoticeReply).
func editNoticeReply(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, content string) {
	sendNoticeReply(r, interaction, &discordgo.WebhookEdit{Content: &content})
}

// sendNoticeReply fills in the ephemeral reply to a press or a picker
// submission. A lost reply is captured. editEphemeral cannot serve here:
// its capture reads the interaction as a slash command's, which panics on
// a component's.
func sendNoticeReply(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, edit *discordgo.WebhookEdit) {
	if err := r.InteractionResponseEdit(interaction.Interaction, edit); err != nil {
		captureError("Failed to deliver deferred-ephemeral edit", err,
			"command", voiceLockCommandName,
			"custom_id", interaction.MessageComponentData().CustomID,
			"guild_id", interaction.GuildID)
	}
}
