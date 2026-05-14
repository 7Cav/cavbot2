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
		err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: message,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			if isAlreadyAcknowledged(err) {
				Debug("Retrying error response as edit", "error", err)
				if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &message,
				}); err != nil {
					Error("Failed to send error message", "error", err)
				}
			} else {
				Error("Failed to send error message", "error", err)
			}
		}
		return
	}

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: message,
		},
	})
	if err != nil {
		if isAlreadyAcknowledged(err) {
			Debug("Retrying error response as edit", "error", err)
			if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &message,
			}); err != nil {
				Error("Failed to send error message", "error", err)
			}
		} else {
			Error("Failed to send error message", "error", err)
		}
	}
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
