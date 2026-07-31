package utils

import (
	"fmt"
	"os"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// parseDiscordgoLogLevel maps DISCORDGO_LOG_LEVEL onto a discordgo log level.
//
// An unset or unrecognised value means LogError, which is discordgo's own
// default — so leaving the variable alone keeps gateway logging exactly as
// quiet as it was before this knob existed. Raising it is opt-in, and worth
// knowing before you do: at LogWarning discordgo logs every gateway event it
// does not recognise, with the event's full raw payload. That is one line per
// event on a busy guild, so it is a diagnostic setting, not a default.
func parseDiscordgoLogLevel(value string) int {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "WARN", "WARNING":
		return discordgo.LogWarning
	case "INFO":
		return discordgo.LogInformational
	case "DEBUG":
		return discordgo.LogDebug
	default:
		return discordgo.LogError
	}
}

// InitDiscordgoLogging routes discordgo's internal logging through the same
// slog wrappers as the rest of the bot and returns the level to apply to a
// session.
//
// Without this, discordgo writes straight to the stdlib log package: its
// heartbeat and reconnect errors already bypass LOG_LEVEL and arrive in a
// different format from every other line the bot emits, which matters because
// the logs are scraped off-box (see ADR 0011).
//
// discordgo's levels are mapped faithfully onto ours rather than flattened, so
// LOG_LEVEL still applies on top: seeing discordgo's debug output needs both
// DISCORDGO_LOG_LEVEL=DEBUG and LOG_LEVEL=DEBUG.
func InitDiscordgoLogging() int {
	discordgo.Logger = func(msgL, _ int, format string, a ...interface{}) {
		msg := fmt.Sprintf(format, a...)
		switch msgL {
		case discordgo.LogError:
			Error("discordgo", "message", msg)
		case discordgo.LogWarning:
			Warn("discordgo", "message", msg)
		case discordgo.LogInformational:
			Info("discordgo", "message", msg)
		default:
			Debug("discordgo", "message", msg)
		}
	}

	return parseDiscordgoLogLevel(os.Getenv("DISCORDGO_LOG_LEVEL"))
}
