package utils

import (
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
	Info("Logger initialized", "level", levelStr)
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

func HandleError(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	Info("Error handling interaction", "message", message)

	if i.Type == discordgo.InteractionApplicationCommand {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: message,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			if strings.Contains(err.Error(), "already been acknowledged") {
				Info("Retrying error response as edit", "error", err)
				_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &message,
				})
				if err != nil {
					Error("Failed to send error message", "error", err)
				}
			} else {
				Error("Failed to send error message", "error", err)
			}
		}
	} else {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: message,
			},
		})
		if err != nil {
			if strings.Contains(err.Error(), "already been acknowledged") {
				Info("Retrying error response as edit", "error", err)
				_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &message,
				})
				if err != nil {
					Error("Failed to send error message", "error", err)
				}
			} else {
				Error("Failed to send error message", "error", err)
			}
		}
	}
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
