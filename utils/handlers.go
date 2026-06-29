package utils

import (
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

var Logger *slog.Logger

// captureError is the Sentry-capture seam used by HandleError's delivery-failure
// paths. It points at CaptureError in production; tests swap it to assert that a
// genuine failure to deliver an error reply pages Sentry while the expected
// already-acknowledged case that succeeds on the edit fallback does not (ADR
// 0001). This mirrors the swappable seam the warden error helpers use.
var captureError = CaptureError

func InitLogger(levelStr string) {
	var level slog.Level
	switch levelStr {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO":
		level = slog.LevelInfo
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	Logger = slog.New(slog.NewTextHandler(os.Stdout, opts))
	Info("Logger initialized", "level", level)
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
// respond falls back to an edit that succeeds — captures nothing. A genuine
// delivery fault does page Sentry via captureError (ADR 0001):
//
//   - a respond failure that is NOT "already acknowledged" is a genuine system
//     fault (the reply never reached Discord), and
//   - a fallback-edit failure after an already-acknowledged respond means the
//     user's error reply was lost.
//
// Only the sanitized `message` is ever shown to the user; the raw delivery error
// goes to captureError, never into the user-facing content.
func deliverErrorReply(r InteractionResponder, i *discordgo.InteractionCreate, message string, resp *discordgo.InteractionResponse) {
	err := r.InteractionRespond(i.Interaction, resp)
	if err == nil {
		return
	}

	if isAlreadyAcknowledged(err) {
		Debug("Retrying error response as edit", "error", err)
		if editErr := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &message,
		}); editErr != nil {
			captureError("Failed to deliver interaction error reply via edit fallback", editErr,
				"command", interactionCommandName(i), "guild_id", i.GuildID)
		}
		return
	}

	captureError("Failed to deliver interaction error reply", err,
		"command", interactionCommandName(i), "guild_id", i.GuildID)
}

// interactionCommandName returns the invoked application-command name for
// capture context, or "" when none is resolvable (a message component, or a
// malformed interaction). The type assertion is guarded so a nil or non-command
// Data never panics — capture context is best-effort, never a new failure mode.
func interactionCommandName(i *discordgo.InteractionCreate) string {
	if i == nil || i.Interaction == nil {
		return ""
	}
	data, ok := i.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok {
		return ""
	}
	return data.Name
}

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
func HandleValidateBranchName(branch string) error {
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

	if len(branch) == 0 || len(branch) > 255 {
		return fmt.Errorf("branch name must be between 1 and 255 characters")
	}

	if branch[0] == '.' {
		return fmt.Errorf("invalid branch name: must not start with a dot")
	}

	if !validPattern.MatchString(branch) {
		return fmt.Errorf("invalid branch name: must start with alphanumeric and contain only alphanumeric, dots, hyphens, or underscores")
	}

	return nil
}
