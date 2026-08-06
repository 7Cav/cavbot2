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
//
// The capture is intentionally unconditional on the failure's status class:
// HandleError runs synchronously inside the interaction window, so any delivery
// fault here is genuine. This differs from warden's post-window edit helpers,
// which gate capture on a token-expiry predicate because their edits race the
// interaction-token lifetime.
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
				append(interactionCaptureContext(i), "guild_id", i.GuildID)...)
		}
		return
	}

	captureError("Failed to deliver interaction error reply", err,
		append(interactionCaptureContext(i), "guild_id", i.GuildID)...)
}

// interactionCaptureContext builds the command attribution for a capture: the
// invoked application-command name, or — for a message component — the
// command-name prefix its CustomID routes on, with the full CustomID carried
// alongside under its own key.
//
// The prefix, not the whole CustomID, is what goes under "command" because that
// value is promoted to a Sentry tag (see promoteCommandTag), and tags are a
// bounded dimension. A CustomID can embed free user input — /apps_beta_deploy
// builds "apps_beta_deploy::confirm::<branch>" from a typed branch name — so
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
