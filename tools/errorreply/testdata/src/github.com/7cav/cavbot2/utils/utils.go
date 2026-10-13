// Package utils stands in for cavbot2's utils: the error reply, the log
// wrappers and the Sentry capture, in the shapes the real ones have.
package utils

import (
	"log/slog"

	"github.com/bwmarrin/discordgo"
	"github.com/getsentry/sentry-go"
)

type InteractionResponder interface {
	InteractionRespond(i *discordgo.Interaction, resp *discordgo.InteractionResponse) error
}

// HandleError sends message to the member.
func HandleError(r InteractionResponder, i *discordgo.InteractionCreate, message string) {
	r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Data: &discordgo.InteractionResponseData{Content: message}})
}

var Logger = slog.Default()

func Info(msg string, args ...any) {
	Logger.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	Logger.Warn(msg, args...)
}

func CaptureError(msg string, err error, kv ...any) {
	Logger.Error(msg, append([]any{"error", err}, kv...)...)

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("message", msg)
		setExtras(scope, kv)
		sentry.CaptureException(err)
	})
}

func setExtras(scope *sentry.Scope, kv []any) {
	extras := make(map[string]any)
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		extras[key] = kv[i+1]
	}
	if len(extras) > 0 {
		scope.SetContext("extra", extras)
	}
}
