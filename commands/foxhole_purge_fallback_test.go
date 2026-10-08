package commands

import (
	"errors"
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// tokenExpiredRESTError builds the *discordgo.RESTError Discord returns on the
// deferred-interaction webhook edit once the 15-minute token window has passed:
// a 401 carrying the "Invalid Webhook Token" application error code (50027).
func tokenExpiredRESTError() *discordgo.RESTError {
	return restError(http.StatusUnauthorized, discordgo.ErrCodeInvalidWebhookTokenProvided, "Invalid Webhook Token")
}

// purgeInteractionWithChannel builds a deferred purge interaction carrying both
// a guild id and the invoking channel id, so the channel-message fallback has a
// destination to post to.
func purgeInteractionWithChannel(guildID, channelID string) *discordgo.InteractionCreate {
	i := foxholeInteraction(guildID, stringOption("command", "purge"))
	i.ChannelID = channelID
	return i
}

// purgeSummaryContext is what a capture of a purge summary nobody received
// names: the command, the subcommand and the guild.
var purgeSummaryContext = map[string]any{"command": "foxhole", "subcommand": "purge", "guild_id": "guild-1"}

// A purge re-applies channel overwrites one at a time, so a long one can
// outlive the interaction's 15-minute token and its summary can't go out as
// the deferred reply. When Discord says the token is gone, the summary goes
// to the channel the purge was run in instead, and nothing pages Sentry,
// since the manager got it. Any other failed reply is a fault: it pages
// Sentry once, naming the run, and the summary goes nowhere else. A reply
// delivered needs neither.
func TestRunFoxholePurge_SummaryReachesTheManagerPastTheTokenWindow(t *testing.T) {
	cases := []struct {
		name     string
		editErr  error
		fallback bool
	}{
		{"the reply is delivered", nil, false},
		{"Invalid Webhook Token", tokenExpiredRESTError(), true},
		{"Unknown Webhook", restError(http.StatusNotFound, discordgo.ErrCodeUnknownWebhook, "Unknown Webhook"), true},
		{"Unknown Interaction", restError(http.StatusNotFound, discordgo.ErrCodeUnknownInteraction, "Unknown Interaction"), true},
		{"server error", restError(http.StatusInternalServerError, 0, "boom"), false},
		{"transport error", errors.New("dial tcp: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noOverwriteDelay(t)
			rec := &captureRecorder{}
			rec.install(t)
			gm := oneOverwriteGuild()
			f := &fakeResponder{EditErrs: []error{tc.editErr}}

			runFoxholePurge(f, gm, purgeInteractionWithChannel("guild-1", "chan-9"), "guild-1", "internal")

			if got := countMethod(f.Calls(), "Edit"); got != 1 {
				t.Errorf("the purge edited its reply %d times, want once", got)
			}
			messages := gm.channelMessages
			switch {
			case tc.fallback:
				if len(messages) != 1 || messages[0].channelID != "chan-9" {
					t.Fatalf("channel messages = %+v, want the summary in chan-9, where the purge ran", messages)
				}
				assertVerdict(t, messages[0].content, defaultInternalRoleName, verdictDone)
				if rec.count != 0 {
					t.Errorf("a summary delivered to the channel captured %d events, want none", rec.count)
				}
			case tc.editErr == nil:
				if len(messages) != 0 || rec.count != 0 {
					t.Errorf("a delivered reply sent channel messages %+v and captured %d events, want neither", messages, rec.count)
				}
			default:
				if len(messages) != 0 {
					t.Errorf("channel messages = %+v, want none for a failed reply that isn't the token's end", messages)
				}
				if rec.count != 1 {
					t.Fatalf("Sentry got %d events, want 1", rec.count)
				}
				got := kvToMap(rec.lastKV)
				for key, want := range purgeSummaryContext {
					if got[key] != want {
						t.Errorf("capture context %s = %v, want %v", key, got[key], want)
					}
				}
			}
		})
	}
}

// When the token is gone and the channel message fails too, nothing reached
// the manager: that pages Sentry once, naming the run and carrying the
// failed reply's error as well as the channel's, so on-call sees both.
func TestRunFoxholePurge_SummaryReachingNobodyIsCapturedWithBothFailures(t *testing.T) {
	noOverwriteDelay(t)
	rec := &captureRecorder{}
	rec.install(t)
	editErr := tokenExpiredRESTError()
	gm := oneOverwriteGuild()
	gm.ChannelMessageErrs = []error{errors.New("missing access")}
	f := &fakeResponder{EditErrs: []error{editErr}}

	runFoxholePurge(f, gm, purgeInteractionWithChannel("guild-1", "chan-9"), "guild-1", "internal")

	if got := gm.countCalls("ChannelMessageSend"); got != 1 {
		t.Fatalf("expected the fallback to be attempted once, got %d", got)
	}
	if rec.count != 1 {
		t.Fatalf("expected exactly 1 Sentry capture when both surfaces fail, got %d", rec.count)
	}
	got := kvToMap(rec.lastKV)
	for key, want := range purgeSummaryContext {
		if got[key] != want {
			t.Errorf("capture context %s = %v, want %v", key, got[key], want)
		}
	}
	if got["edit_error"] != error(editErr) {
		t.Errorf("expected the original edit error in capture context, got %v", got["edit_error"])
	}
}
