package commands

import (
	"errors"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// editFailInteraction builds a deferred-ephemeral interaction carrying a guild
// id and the warden `command` option, so the edit-failure path can read its
// command/guild context straight off the interaction.
func editFailInteraction(guildID, subcommand string) *discordgo.InteractionCreate {
	return wardenInteraction(guildID, stringOption("command", subcommand))
}

// countMethod returns how many recorded calls used the given responder method.
func countMethod(calls []recordedCall, method string) int {
	n := 0
	for _, c := range calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// A failed post-defer edit must be captured to Sentry exactly once, with no
// retry loop: the helper must not re-issue InteractionRespond or a second
// InteractionResponseEdit, since the interaction is already acknowledged and
// the identical edit just failed.
func TestEditEphemeral_FailedEditCapturesOnceNoRetry(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errors.New("503 service unavailable")}}
	i := editFailInteraction("guild-42", "add")

	editEphemeral(f, i, "✅ done")

	calls := f.Calls()
	if got := countMethod(calls, "Edit"); got != 1 {
		t.Fatalf("expected exactly 1 Edit (the failing call), got %d: %v", got, calls)
	}
	if got := countMethod(calls, "Respond"); got != 0 {
		t.Fatalf("expected no Respond on the post-defer failure path, got %d: %v", got, calls)
	}
	if rec.count != 1 {
		t.Fatalf("expected exactly 1 Sentry capture, got %d", rec.count)
	}
}

// editEphemeralWithEmbed must funnel its edit-failure handling through the same
// capture-with-context seam: one capture, no retry.
func TestEditEphemeralWithEmbed_FailedEditCapturesOnceNoRetry(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{errors.New("503 service unavailable")}}
	i := editFailInteraction("guild-42", "purge")
	embed := &discordgo.MessageEmbed{Title: "Added 0 user(s)"}

	editEphemeralWithEmbed(f, i, "summary", embed)

	calls := f.Calls()
	if got := countMethod(calls, "Edit"); got != 1 {
		t.Fatalf("expected exactly 1 Edit (the failing call), got %d: %v", got, calls)
	}
	if got := countMethod(calls, "Respond"); got != 0 {
		t.Fatalf("expected no Respond on the post-defer failure path, got %d: %v", got, calls)
	}
	if rec.count != 1 {
		t.Fatalf("expected exactly 1 Sentry capture, got %d", rec.count)
	}
}

// kvToMap folds a captured kv slice (key, value, key, value, ...) into a map for
// readable assertions on individual context fields.
func kvToMap(kv []any) map[string]any {
	m := map[string]any{}
	for idx := 0; idx+1 < len(kv); idx += 2 {
		if key, ok := kv[idx].(string); ok {
			m[key] = kv[idx+1]
		}
	}
	return m
}

// captureCommandAndGuild swaps the captureError seam to record the kv passed on
// the next capture, returning a pointer the test reads after the call.
func captureCommandAndGuild(t *testing.T) *[]any {
	t.Helper()
	var gotKV []any
	prev := captureError
	captureError = func(_ string, _ error, kv ...any) { gotKV = kv }
	t.Cleanup(func() { captureError = prev })
	return &gotKV
}

// The capture must carry command and guild context, both read off the
// interaction, so on-call can attribute a lost reply to its subcommand and
// guild. This is the single seam future fallback-delivery handling extends.
func TestEditEphemeral_CaptureCarriesCommandAndGuildContext(t *testing.T) {
	gotKV := captureCommandAndGuild(t)

	f := &fakeResponder{EditErrs: []error{errors.New("boom")}}
	i := editFailInteraction("guild-42", "remove")

	editEphemeral(f, i, "content")

	kvMap := kvToMap(*gotKV)
	if kvMap["command"] != "warden" {
		t.Fatalf("expected command=warden in capture context, got %v", kvMap["command"])
	}
	if kvMap["subcommand"] != "remove" {
		t.Fatalf("expected subcommand=remove in capture context, got %v", kvMap["subcommand"])
	}
	if kvMap["guild_id"] != "guild-42" {
		t.Fatalf("expected guild_id=guild-42 in capture context, got %v", kvMap["guild_id"])
	}
}

// When the interaction carries no `command` option (a malformed interaction),
// the capture context must fall back to "unknown" rather than an empty string,
// so the lost reply still has an attributable command field.
func TestEditEphemeral_CaptureFallsBackToUnknownCommand(t *testing.T) {
	gotKV := captureCommandAndGuild(t)

	f := &fakeResponder{EditErrs: []error{errors.New("boom")}}
	// wardenInteraction sets the guild id but passes no command option.
	i := wardenInteraction("guild-42")

	editEphemeral(f, i, "content")

	kvMap := kvToMap(*gotKV)
	if kvMap["subcommand"] != "unknown" {
		t.Fatalf("expected subcommand=unknown when no command option is present, got %v", kvMap["subcommand"])
	}
	if kvMap["command"] != "warden" {
		t.Fatalf("expected command=warden even when the subcommand is unresolvable, got %v", kvMap["command"])
	}
}

// A successful edit must never page Sentry: the capture seam stays untouched on
// the happy path. This pins the guarantee locally instead of leaving it only
// transitively implied by the 5xx-failure tests.
func TestEditEphemeral_SuccessfulEditDoesNotCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{} // no EditErrs: the edit succeeds.
	i := editFailInteraction("guild-42", "add")

	editEphemeral(f, i, "✅ done")

	if got := countMethod(f.Calls(), "Edit"); got != 1 {
		t.Fatalf("expected exactly 1 Edit, got %d", got)
	}
	if rec.count != 0 {
		t.Fatalf("a successful edit must not capture to Sentry, got %d captures", rec.count)
	}
}
