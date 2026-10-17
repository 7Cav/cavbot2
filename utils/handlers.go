package utils

import (
	"errors"
	"github.com/bwmarrin/discordgo"
	"log/slog"
	"os"
	"strings"
)

var Logger *slog.Logger

// captureError is the Sentry-capture seam used by HandleError's delivery-failure
// paths. It points at CaptureError in production; tests swap it to assert that a
// genuine failure to deliver an error reply pages Sentry while the expected
// already-acknowledged case that succeeds on the edit fallback does not (ADR
// 0001). This mirrors the swappable seam the Foxhole error helpers use.
var captureError = CaptureError

// ParseLogLevel is the level a LOG_LEVEL value sets. It reads only uppercase
// DEBUG, INFO, WARN and ERROR, and anything else means INFO.
func ParseLogLevel(levelStr string) slog.Level {
	switch levelStr {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// logLevel is the bot log's level, as InitLogger set it.
var logLevel slog.Level

func InitLogger(levelStr string) {
	logLevel = ParseLogLevel(levelStr)
	opts := &slog.HandlerOptions{Level: logLevel}
	Logger = slog.New(slog.NewTextHandler(os.Stdout, opts))
	Info("Logger initialized", "level", logLevel)
}

// NewLogger returns a logger that writes to stdout as the bot log does, and
// the LevelVar that holds its level. The level starts at the bot log's. A
// library keeps the logger it is handed, so setting the LevelVar is how the
// owner changes what that library logs, and the bot log stays as it is.
func NewLogger() (*slog.Logger, *slog.LevelVar) {
	level := new(slog.LevelVar)
	level.Set(logLevel)
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})), level
}

func Info(msg string, args ...any) {
	Logger.Info(msg, args...)
}

func Error(msg string, args ...any) {
	Logger.Error(msg, args...)
}

func Warn(msg string, args ...any) {
	Logger.Warn(msg, args...)
}
func Debug(msg string, args ...any) {
	Logger.Debug(msg, args...)
}

// HandleError sends message to the member as their error reply. message is
// fixed text plus any member input it names. An error's text goes to the log
// or to Sentry instead. tools/errorreply, which the gate runs, reports code
// that takes an error's data anywhere else, a message for this included
// (ADR 0016, ADR 0018).
func HandleError(r InteractionResponder, i *discordgo.InteractionCreate, message string) {
	Info("Error handling interaction", "message", message)

	if i.Type == discordgo.InteractionApplicationCommand {
		deliverErrorReply(r, i, message, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: message,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	deliverErrorReply(r, i, message, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: message,
		},
	})
}

// deliverErrorReply sends the prepared error response and handles delivery
// failure. The normal path — respond succeeds, or the already-acknowledged
// respond falls back to an edit that succeeds — captures nothing. Neither
// does an edit of a message someone deleted while the command ran, such as a
// slash command's placeholder or the message a button sits on: the reply goes
// out as an ephemeral follow-up instead (#651). A genuine delivery fault does
// page Sentry via captureError (ADR 0001):
//
//   - a respond failure that is NOT "already acknowledged" is a genuine system
//     fault (the reply never reached Discord),
//   - a fallback-edit failure after an already-acknowledged respond, other
//     than a deleted message, means the user's error reply was lost, and
//   - so does a failed follow-up for a deleted message.
//
// Only the sanitized `message` is ever shown to the user; the raw delivery error
// goes to captureError, never into the user-facing content.
//
// The capture is intentionally unconditional on the failure's status class:
// HandleError runs synchronously inside the interaction window, so any delivery
// fault here is genuine. This differs from the Foxhole commands' post-window
// edit helpers, which gate capture on a token-expiry predicate because their
// edits race the interaction-token lifetime.
func deliverErrorReply(r InteractionResponder, i *discordgo.InteractionCreate, message string, resp *discordgo.InteractionResponse) {
	err := r.InteractionRespond(i.Interaction, resp)
	if err == nil {
		return
	}

	if isAlreadyAcknowledged(err) {
		Debug("Retrying error response as edit", "error", err)
		editErr := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &message,
		})
		switch {
		case editErr == nil:
		case isUnknownMessage(editErr):
			followUpErrorReply(r, i, message)
		default:
			captureLostReply("Failed to deliver interaction error reply via edit fallback", editErr, i)
		}
		return
	}

	captureLostReply("Failed to deliver interaction error reply", err, i)
}

// captureLostReply sends a failure to deliver i's error reply to Sentry, with
// the command and guild it came from.
func captureLostReply(msg string, err error, i *discordgo.InteractionCreate) {
	captureError(msg, err, append(interactionCaptureContext(i), "guild_id", i.GuildID)...)
}

// followUpErrorReply sends message as an ephemeral follow-up only the member
// sees, for an error reply whose message was deleted before the edit could
// reach it. The bot deletes no interaction reply, so the deletion was a
// member's or another bot's doing, and a follow-up that lands is an INFO
// line. One that fails means the reply never reached the member, and goes to
// Sentry.
func followUpErrorReply(r InteractionResponder, i *discordgo.InteractionCreate, message string) {
	if err := r.FollowupMessageCreate(i.Interaction, false, &discordgo.WebhookParams{
		Content: message,
		Flags:   discordgo.MessageFlagsEphemeral,
	}); err != nil {
		captureLostReply("Failed to deliver interaction error reply via follow-up", err, i)
		return
	}
	Info("Error reply sent as a follow-up, its message was deleted", interactionCaptureContext(i)...)
}

// interactionCaptureContext builds the command attribution for a capture: the
// invoked application-command name, or — for a message component — the
// command-name prefix its CustomID routes on, with the full CustomID carried
// alongside under its own key.
//
// The prefix, not the whole CustomID, is what goes under "command" because that
// value is promoted to a Sentry tag (see promoteCommandTag), and tags are a
// bounded dimension. A CustomID's payload segment can embed free user input, so
// tagging the whole thing would give the tag an unbounded value space and
// scatter one command's failures across a new group per input. The separator
// matches the one main.go's dispatcher splits on.
//
// It returns a "command" of "" when nothing is resolvable. The type assertions
// and nil checks are purely defensive (capture context must never become a new
// failure mode); they are not a nil-interaction guard for HandleError, which
// dereferences i.Type before this helper ever runs.
func interactionCaptureContext(i *discordgo.InteractionCreate) []any {
	if i == nil || i.Interaction == nil {
		return []any{"command", ""}
	}
	if data, ok := i.Data.(discordgo.ApplicationCommandInteractionData); ok {
		return []any{"command", data.Name}
	}
	if data, ok := i.Data.(discordgo.MessageComponentInteractionData); ok {
		name, _, _ := strings.Cut(data.CustomID, customIDSeparator)
		return []any{"command", name, "custom_id", data.CustomID}
	}
	return []any{"command", ""}
}

// customIDSeparator delimits a component CustomID's routing prefix from its
// payload. Kept in step with the split in main.go's dispatcher; see ADR 0007.
const customIDSeparator = "::"

// alreadyAcknowledgedCode is Discord's error code for "Interaction has already
// been acknowledged" (40060). discordgo wraps real API failures as *RESTError;
// the string-match fallback catches synthetic errors (e.g. in tests) and any
// future wrapping where the typed error isn't reachable via errors.As.
const alreadyAcknowledgedCode = 40060

func isAlreadyAcknowledged(err error) bool {
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Message != nil && restErr.Message.Code == alreadyAcknowledgedCode {
		return true
	}
	return strings.Contains(err.Error(), "already been acknowledged")
}

// isUnknownMessage reports whether Discord answered with 10008 Unknown
// Message: the message the call names is gone.
func isUnknownMessage(err error) bool {
	var restErr *discordgo.RESTError
	return errors.As(err, &restErr) && restErr.Message != nil && restErr.Message.Code == discordgo.ErrCodeUnknownMessage
}
