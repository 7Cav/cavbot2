package commands

import (
	"errors"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// deferEphemeral acknowledges an interaction with a deferred ephemeral
// reply. A refusal comes back as an *ackFailure, which carries the timings
// captureAckFailure reports.
func deferEphemeral(r utils.InteractionResponder, interaction *discordgo.InteractionCreate) error {
	sent := time.Now()
	err := r.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
	if err == nil {
		return nil
	}
	failure := &ackFailure{err: err, took: time.Since(sent)}
	if created, parseErr := discordgo.SnowflakeTimestamp(interaction.ID); parseErr == nil {
		failure.age, failure.ageKnown = sent.Sub(created), true
	}
	return failure
}

// ackFailure is a refused acknowledgement. Discord drops an interaction
// that is not acknowledged within 3 seconds of creating it, so its two
// timings tell a late interaction from a slow answer: took is how long the
// acknowledgement call ran, and age is how old the interaction was when the
// bot sent it. The bot does no I/O before acknowledging, so age is also how
// old the interaction was when the bot received it.
type ackFailure struct {
	err      error
	took     time.Duration
	age      time.Duration
	ageKnown bool
}

func (f *ackFailure) Error() string { return f.err.Error() }
func (f *ackFailure) Unwrap() error { return f.err }

// captureAckFailure captures a refused acknowledgement with its timings:
// ack_ms, and interaction_age_ms when the interaction ID parses. The age
// compares Discord's timestamp in the ID with the bot's clock, so clock
// skew moves it. The event carries Discord's own error, not the
// *ackFailure around it, so it groups in Sentry by Discord's error type.
func captureAckFailure(msg string, err error, kv ...any) {
	var failure *ackFailure
	if !errors.As(err, &failure) {
		captureError(msg, err, kv...)
		return
	}
	kv = append(kv, "ack_ms", failure.took.Milliseconds())
	if failure.ageKnown {
		kv = append(kv, "interaction_age_ms", failure.age.Milliseconds())
	}
	captureError(msg, failure.err, kv...)
}

// isUnknownInteraction reports whether Discord refused an acknowledgement
// with 10062 Unknown interaction. The interaction is gone then: the member
// sees "The application did not respond", and Discord rejects anything
// more the bot sends on it.
func isUnknownInteraction(err error) bool {
	var restErr *discordgo.RESTError
	return errors.As(err, &restErr) && restErr.Message != nil &&
		restErr.Message.Code == discordgo.ErrCodeUnknownInteraction
}

// captureMissedAck reports a slash command whose acknowledgement Discord
// answered with 10062, with any further context the caller has in kv. It
// is the one event for the miss, so the caller sends nothing more on the
// interaction.
func captureMissedAck(interaction *discordgo.InteractionCreate, err error, kv ...any) {
	captureAckFailure("Failed to acknowledge a slash command", err, append([]any{
		"command", interaction.ApplicationCommandData().Name,
		"guild_id", interaction.GuildID,
	}, kv...)...)
}

// ackFailedReply answers a slash command whose deferred acknowledgement
// Discord refused.
const ackFailedReply = "❌ Couldn't start the command. Try again in a moment."

// replyAckFailed handles a refused deferEphemeral. A 10062 goes to Sentry,
// with any further context the caller has in kv, and gets no reply, since
// none could arrive. Any other refusal goes to the log and gets
// ackFailedReply, never the error, which would show the member a raw
// Discord body.
func replyAckFailed(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, err error, kv ...any) {
	if isUnknownInteraction(err) {
		captureMissedAck(interaction, err, kv...)
		return
	}
	username, discordID := interactionUsernameAndID(interaction)
	utils.Warn("Failed to acknowledge interaction",
		"command", interaction.ApplicationCommandData().Name, "username", username, "discord_id", discordID, "error", err)
	utils.HandleError(r, interaction, ackFailedReply)
}
