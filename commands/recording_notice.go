package commands

import (
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The recording notice (spec #381, #388): the message a start posts in the
// recorded channel's text chat. It names the starter and the title, says
// how long the recording is kept and who can download it, pings nobody, and
// carries a Stop button.
//
// The button's CustomID follows ADR 0007: record::stop::<channel ID>.
// main.go's dispatcher routes every press to /record's handler, which hands
// a component interaction to runRecordingNoticeComponent before it reads
// anything a slash command carries. The button answers to whoever /record
// stop does: Server Settings don't reach buttons, so that check is its only
// gate.

// recordingNoticeStop is the Stop button's action, the middle segment of
// its CustomID.
const recordingNoticeStop = "stop"

// recordingNoticeCustomID assembles the Stop button's CustomID for a
// channel, and refuses one longer than Discord's cap: a truncated ID would
// misroute.
func recordingNoticeCustomID(channelID string) (string, error) {
	id := strings.Join([]string{recordCommandName, recordingNoticeStop, channelID}, customIDSeparator)
	if len(id) > discordCustomIDLimit {
		return "", fmt.Errorf("recording notice CustomID for channel %s is %d characters, over Discord's %d", channelID, len(id), discordCustomIDLimit)
	}
	return id, nil
}

// parseRecordingNoticeCustomID reads a recording notice button's action and
// channel ID back out of its CustomID. ok is false for any CustomID
// recordingNoticeCustomID could not have built.
func parseRecordingNoticeCustomID(customID string) (action, channelID string, ok bool) {
	parts := strings.Split(customID, customIDSeparator)
	if len(parts) != 3 || parts[0] != recordCommandName || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// recordingNoticeKept is the notice's line on keeping the recording, word for
// word as the spec gives it.
const recordingNoticeKept = "Kept 30 days. Only the starter and panel admins can download it. File an S6 ticket to request deletion."

// recordingNoticeLine is the notice's content while the recording runs. The
// title is the starter's own, shown as they typed it.
func recordingNoticeLine(starterID, title string) string {
	line := fmt.Sprintf("🔴 <@%s> is recording this channel.", starterID)
	if title != "" {
		line = fmt.Sprintf("🔴 <@%s> is recording this channel: %s", starterID, title)
	}
	return line + "\n" + recordingNoticeKept
}

// postRecordingNotice posts the notice of a recording starting in a
// channel, and returns its message ID.
func (r *RecordingRuntime) postRecordingNotice(channelID, starterID, title string) (string, error) {
	customID, err := recordingNoticeCustomID(channelID)
	if err != nil {
		return "", err
	}
	msg, err := r.mgr.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: recordingNoticeLine(starterID, title),
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Stop recording", Style: discordgo.DangerButton, CustomID: customID},
		}}},
		AllowedMentions: noMentions(),
	})
	if err != nil {
		return "", err
	}
	return msg.ID, nil
}

// recordingNoticeStoppedLine is the notice's content once the recording
// has stopped.
const recordingNoticeStoppedLine = "⏹️ This channel is no longer being recorded."

// closeRecordingNotice edits a stopped recording's notice to say it stopped,
// and removes its button. A failed edit is a WARN line and never holds up
// the stop, which has already happened.
func (r *RecordingRuntime) closeRecordingNotice(channelID, messageID string, recordingID int64) {
	content := recordingNoticeStoppedLine
	_, err := r.mgr.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:              messageID,
		Channel:         channelID,
		Content:         &content,
		Components:      &[]discordgo.MessageComponent{},
		AllowedMentions: noMentions(),
	})
	if err != nil {
		utils.Warn("Recording notice not edited at stop",
			"recording_id", recordingID, "channel_id", channelID, "message_id", messageID, "error", err)
	}
}

// runRecordingNoticeComponent answers a press on a recording notice's
// button. Every answer is a new ephemeral message: a deferred ephemeral
// response, then an edit of it. Never an update of the message the button
// sits on, which would overwrite the public notice for everyone.
func runRecordingNoticeComponent(r utils.InteractionResponder, rt *RecordingRuntime, interaction *discordgo.InteractionCreate) {
	customID := interaction.MessageComponentData().CustomID
	if err := deferEphemeral(r, interaction); err != nil {
		captureAckFailure("Failed to acknowledge a recording notice button", err,
			"command", recordCommandName, "custom_id", customID, "guild_id", interaction.GuildID)
		return
	}

	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting RecordingNoticeButton", "command", recordCommandName,
		"username", username, "discord_id", discordID, "custom_id", customID)

	if rt == nil {
		utils.Warn("Recording notice button refused, no bot store configured",
			"custom_id", customID, "discord_id", discordID)
		editNoticeReply(r, interaction, recordingOffRefusal)
		return
	}

	action, channelID, ok := parseRecordingNoticeCustomID(customID)
	if !ok || action != recordingNoticeStop {
		// A button from an older build whose action is gone.
		utils.Warn("Recording notice button not recognised", "custom_id", customID, "discord_id", discordID)
		editNoticeReply(r, interaction, "❌ This button no longer does anything. Use /"+recordCommandName+" stop instead.")
		return
	}
	by := Invoker{UserID: discordID, Username: username, Roles: interactionRoles(interaction)}
	if err := rt.stopFromNotice(channelID, by); err != nil {
		reply, refused := stopRefusal(err)
		if !refused {
			reply = recordFailedReply
		}
		editNoticeReply(r, interaction, reply)
		return
	}
	editNoticeReply(r, interaction, fmt.Sprintf("✅ Stopped recording <#%s>.", channelID))
	utils.Info("✨ Done!", "command", recordCommandName)
}
