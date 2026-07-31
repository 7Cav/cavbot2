package utils

import (
	"context"
	"log/slog"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// levelRecorder captures the slog level of each record and discards the
// message. The level is the routing decision the shim exists to make; the
// wording is not part of any contract.
type levelRecorder struct {
	levels []slog.Level
}

func (r *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *levelRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.levels = append(r.levels, rec.Level)
	return nil
}

func (r *levelRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *levelRecorder) WithGroup(string) slog.Handler { return r }

// TestInstallDiscordgoLoggerMapsLevelsFaithfully pins the mapping from
// discordgo's levels onto ours. Flattening them — routing everything through
// Error, say — would leave LOG_LEVEL unable to filter discordgo output, which
// is half the reason the shim exists.
func TestInstallDiscordgoLoggerMapsLevelsFaithfully(t *testing.T) {
	cases := []struct {
		name string
		from int
		want slog.Level
	}{
		{"error", discordgo.LogError, slog.LevelError},
		{"warning", discordgo.LogWarning, slog.LevelWarn},
		{"informational", discordgo.LogInformational, slog.LevelInfo},
		{"debug", discordgo.LogDebug, slog.LevelDebug},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &levelRecorder{}
			prevLogger, prevSink := Logger, discordgo.Logger
			t.Cleanup(func() { Logger, discordgo.Logger = prevLogger, prevSink })
			Logger = slog.New(rec)

			InstallDiscordgoLogger()
			discordgo.Logger(tc.from, 0, "gateway said %s", "something")

			if len(rec.levels) != 1 {
				t.Fatalf("got %d records, want 1", len(rec.levels))
			}
			if rec.levels[0] != tc.want {
				t.Errorf("discordgo level %d logged at %v, want %v", tc.from, rec.levels[0], tc.want)
			}
		})
	}
}

// TestDiscordgoLogLevelReadsConfiguredLevel covers the wiring the mapping test
// cannot reach: that the level actually comes from DISCORDGO_LOG_LEVEL. A
// misspelled variable name satisfies every other test in this file.
func TestDiscordgoLogLevelReadsConfiguredLevel(t *testing.T) {
	t.Setenv("DISCORDGO_LOG_LEVEL", "DEBUG")

	if got := DiscordgoLogLevel(); got != discordgo.LogDebug {
		t.Errorf("DiscordgoLogLevel() = %d, want %d", got, discordgo.LogDebug)
	}
}

func TestParseDiscordgoLogLevel(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int
	}{
		{"unset leaves discordgo as quiet as it is today", "", discordgo.LogError},
		// WARN is the level the startup failure tells operators to set, because
		// it is where discordgo reports the frame it got instead of READY.
		{"WARN surfaces the handshake diagnostic", "WARN", discordgo.LogWarning},
		{"DEBUG opts into the full gateway firehose", "DEBUG", discordgo.LogDebug},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDiscordgoLogLevel(tc.value); got != tc.want {
				t.Errorf("parseDiscordgoLogLevel(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}
