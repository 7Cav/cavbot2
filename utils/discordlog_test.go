package utils

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// TestInitDiscordgoLoggingReadsConfiguredLevel covers the wiring the mapping
// test cannot reach: that the level actually comes from DISCORDGO_LOG_LEVEL.
// A misspelled variable name satisfies every other test in this file.
func TestInitDiscordgoLoggingReadsConfiguredLevel(t *testing.T) {
	prev := discordgo.Logger
	t.Cleanup(func() { discordgo.Logger = prev })

	t.Setenv("DISCORDGO_LOG_LEVEL", "DEBUG")

	if got := InitDiscordgoLogging(); got != discordgo.LogDebug {
		t.Errorf("InitDiscordgoLogging() = %d, want %d", got, discordgo.LogDebug)
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
