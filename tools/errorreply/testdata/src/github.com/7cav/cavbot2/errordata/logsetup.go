package errordata

import (
	"bytes"
	"context"
	"log/slog"
	"os"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"github.com/getsentry/sentry-go"
)

// What a log call returns carries what it was handed.
func LogValueReplied(err error) {
	utils.HandleError(nil, nil, slog.AnyValue(err).String()) // want ""
}

// discordHandler posts each record it is handed to a Discord channel, so a
// logger built on it reaches more than the log.
type discordHandler struct {
	s *discordgo.Session
}

func (h *discordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *discordHandler) Handle(_ context.Context, r slog.Record) error {
	_, err := h.s.ChannelMessageSend("1", r.Message)
	return err
}

func (h *discordHandler) WithAttrs([]slog.Attr) slog.Handler { return h } // want ""

func (h *discordHandler) WithGroup(string) slog.Handler { return h } // want ""

func DiscordLogger(s *discordgo.Session) *slog.Logger {
	return slog.New(&discordHandler{s: s}) // want ""
}

// errorsAsWarnings has the name of the handler the check accepts in the
// voice package, and posts each record to a Discord channel.
type errorsAsWarnings struct {
	s *discordgo.Session
}

func (h errorsAsWarnings) Enabled(context.Context, slog.Level) bool { return true }

func (h errorsAsWarnings) Handle(_ context.Context, r slog.Record) error {
	_, err := h.s.ChannelMessageSend("1", r.Message)
	return err
}

func (h errorsAsWarnings) WithAttrs([]slog.Attr) slog.Handler { return h } // want ""

func (h errorsAsWarnings) WithGroup(string) slog.Handler { return h } // want ""

func PostingLogger(s *discordgo.Session) *slog.Logger {
	return slog.New(errorsAsWarnings{s: s}) // want ""
}

func BufferLogger(err error) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil)) // want ""
	logger.Error("lookup failed", "error", err)
	utils.HandleError(nil, nil, buf.String())
}

var lastAttr string

func ReplacingLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{ // want ""
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			lastAttr = a.Value.String()
			return a
		},
	}))
}

// The bot's own logger writes to the process's output.
func ProcessLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// A scope configurator only sets Sentry's own scope.
func CapturedInScope(err error) {
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("command", "lookup")
		sentry.CaptureException(err)
	})
}
