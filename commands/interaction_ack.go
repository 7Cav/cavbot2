package commands

import (
	"errors"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// deferEphemeral acknowledges an interaction with a deferred ephemeral
// reply. A refusal comes back as an *ackFailure, which carries the timings
// ackTimings reports.
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

// ackTimings returns a refused acknowledgement's timings as capture
// key/values. interaction_age_ms compares Discord's timestamp in the
// interaction ID with the bot's clock, so clock skew moves it. It is left
// out when the ID does not parse.
func ackTimings(err error) []any {
	var failure *ackFailure
	if !errors.As(err, &failure) {
		return nil
	}
	kv := []any{"ack_ms", failure.took.Milliseconds()}
	if failure.ageKnown {
		kv = append(kv, "interaction_age_ms", failure.age.Milliseconds())
	}
	return kv
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
// answered with 10062. It is the one event for the miss, so the caller
// sends nothing more on the interaction.
func captureMissedAck(interaction *discordgo.InteractionCreate, err error) {
	captureError("Failed to acknowledge a slash command", err, append([]any{
		"command", interaction.ApplicationCommandData().Name,
		"guild_id", interaction.GuildID,
	}, ackTimings(err)...)...)
}

// ackFailedReply answers a slash command whose deferred acknowledgement
// Discord refused.
const ackFailedReply = "❌ Couldn't start the command. Try again in a moment."

// replyAckFailed handles a refused deferEphemeral. A 10062 goes to Sentry
// and gets no reply, since none could arrive. Any other refusal goes to the
// log and gets ackFailedReply, never the error, which would show the member
// a raw Discord body.
func replyAckFailed(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, err error) {
	if isUnknownInteraction(err) {
		captureMissedAck(interaction, err)
		return
	}
	username, discordID := interactionUsernameAndID(interaction)
	utils.Warn("Failed to acknowledge interaction",
		"command", interaction.ApplicationCommandData().Name, "username", username, "discord_id", discordID, "error", err)
	utils.HandleError(r, interaction, ackFailedReply)
}
