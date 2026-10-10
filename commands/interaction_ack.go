package commands

import (
	"errors"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// deferEphemeral acknowledges an interaction with a deferred reply only
// the invoker sees.
func deferEphemeral(r utils.InteractionResponder, interaction *discordgo.InteractionCreate) error {
	return deferReply(r, interaction, &discordgo.InteractionResponseData{
		Flags: discordgo.MessageFlagsEphemeral,
	})
}

// deferPublic acknowledges an interaction with a deferred reply the whole
// channel sees.
func deferPublic(r utils.InteractionResponder, interaction *discordgo.InteractionCreate) error {
	return deferReply(r, interaction, nil)
}

// deferReply acknowledges an interaction with a deferred reply.
func deferReply(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, data *discordgo.InteractionResponseData) error {
	return acknowledge(r, interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: data,
	})
}

// acknowledge sends an interaction's first response, a deferral or the
// reply itself. A refusal comes back as an *ackFailure, which carries the
// timings captureAckFailure reports.
func acknowledge(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, resp *discordgo.InteractionResponse) error {
	sent := time.Now()
	err := r.InteractionRespond(interaction.Interaction, resp)
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

// missedAckMessage is the message of the Sentry event for a missed
// acknowledgement, which groups every miss as one issue.
const missedAckMessage = "Failed to acknowledge a slash command"

// captureMissedAck reports a slash command whose acknowledgement Discord
// answered with 10062, with any further context the caller has in kv. It
// is the one event for the miss, so the caller sends nothing more on the
// interaction.
func captureMissedAck(interaction *discordgo.InteractionCreate, err error, kv ...any) {
	captureAckFailure(missedAckMessage, err, append([]any{
		"command", interaction.ApplicationCommandData().Name,
		"guild_id", interaction.GuildID,
	}, kv...)...)
}

// ackFailedReply answers a slash command whose acknowledgement Discord
// refused.
const ackFailedReply = "❌ Couldn't start the command. Try again in a moment."

// replyAckFailed handles a refused acknowledgement. A 10062 goes to Sentry,
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

// refuse refuses a slash command run with message, a reply only its
// member sees, sent through utils.HandleError as the interaction's first
// response. That response acknowledges the interaction, so refuse sends it
// through acknowledge. A 10062 is then a missed acknowledgement: refuse
// reports it as captureMissedAck does, with any further context in kv, and
// returns true, so the caller sends nothing more on the interaction.
// HandleError handles any other error, which reaches it as Discord's own
// error.
func refuse(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, message string, kv ...any) (missedAck bool) {
	responder := &errorReplyResponder{InteractionResponder: r, interaction: interaction}
	utils.HandleError(responder, interaction, message)
	if responder.missed == nil {
		return false
	}
	captureMissedAck(interaction, responder.missed, kv...)
	return true
}

// replyError sends message as a slash command's error reply when the reply
// isn't a refusal, such as a lookup that failed after the command deferred.
// It sends the reply as refuse does, so an error reply sent as the
// interaction's first response reports a 10062 as a missed acknowledgement
// too. After a deferral, Discord answers the response with 40060 and
// HandleError edits the deferred reply instead.
func replyError(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, message string) {
	refuse(r, interaction, message)
}

// errorReplyResponder sends an error reply from refuse or replyError
// through acknowledge. It keeps a 10062 in missed rather than hand it to
// HandleError, which would report it as an error reply that never arrived.
type errorReplyResponder struct {
	utils.InteractionResponder
	interaction *discordgo.InteractionCreate
	missed      error
}

func (rr *errorReplyResponder) InteractionRespond(_ *discordgo.Interaction, resp *discordgo.InteractionResponse) error {
	err := acknowledge(rr.InteractionResponder, rr.interaction, resp)
	switch {
	case err == nil:
		return nil
	case isUnknownInteraction(err):
		rr.missed = err
		return nil
	default:
		return errors.Unwrap(err)
	}
}
