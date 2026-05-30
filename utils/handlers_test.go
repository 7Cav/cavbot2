package utils

import (
	"errors"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestHandleValidateBranchName(t *testing.T) {
	// Cases are written to the ACTUAL behavior of the current regex
	// `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` plus the explicit length and leading-dot
	// guards in HandleValidateBranchName. The regex does NOT reject git-forbidden
	// patterns like a `..` sequence or a `.lock` suffix mid/end of string, so
	// those "look-forbidden" names are accepted here — assert that real behavior,
	// not git's rules.
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"valid simple", "feature", false},
		{"slash rejected by regex", "feat/issue-108", true},
		{"valid dots hyphens underscores", "v1.2.3-rc_1", false},
		{"valid single alphanumeric", "a", false},
		{"valid leading digit", "9lives", false},
		{"empty input", "", true},
		{"too long 256 chars", strings.Repeat("a", 256), true},
		{"max length 255 chars ok", strings.Repeat("a", 255), false},
		{"leading dot", ".hidden", true},
		{"leading hyphen rejected by regex", "-branch", true},
		{"leading underscore rejected by regex", "_branch", true},
		{"invalid slash character", "feature/foo", true},
		{"invalid space character", "my branch", true},
		{"leading whitespace", " branch", true},
		{"trailing whitespace", "branch ", true},
		{"invalid at sign", "br@nch", true},
		{"double dot accepted by regex", "foo..bar", false},
		{"dot-lock suffix accepted by regex", "release.lock", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := HandleValidateBranchName(tc.branch)
			if tc.wantErr && err == nil {
				t.Fatalf("HandleValidateBranchName(%q) = nil, want error", tc.branch)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("HandleValidateBranchName(%q) = %v, want nil", tc.branch, err)
			}
		})
	}
}

func TestHandleError_AppCommand_RespondSucceeds(t *testing.T) {
	f := &fakeResponder{}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}}

	HandleError(f, i, "boom")

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "Respond" {
		t.Fatalf("expected Respond, got %q", calls[0].Method)
	}
	if calls[0].Response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("expected ChannelMessageWithSource, got %v", calls[0].Response.Type)
	}
	if calls[0].Response.Data.Content != "boom" {
		t.Fatalf("expected content %q, got %q", "boom", calls[0].Response.Data.Content)
	}
	if calls[0].Response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("expected Ephemeral flag set, got flags=%v", calls[0].Response.Data.Flags)
	}
}

func TestHandleError_AppCommand_AlreadyAcknowledgedString_FallsBackToEdit(t *testing.T) {
	// String-match fallback path: synthetic errors.New(...) without a RESTError.
	f := &fakeResponder{
		RespondErrs: []error{errors.New("HTTP 400 Bad Request: Interaction has already been acknowledged.")},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}}

	HandleError(f, i, "boom")

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (Respond + Edit fallback), got %d: %+v", len(calls), calls)
	}
	if calls[1].Method != "Edit" {
		t.Fatalf("expected fallback Edit, got %q", calls[1].Method)
	}
	if calls[1].Edit.Content == nil || *calls[1].Edit.Content != "boom" {
		got := "<nil>"
		if calls[1].Edit.Content != nil {
			got = *calls[1].Edit.Content
		}
		t.Fatalf("expected Edit Content=%q, got %q", "boom", got)
	}
}

func TestHandleError_AppCommand_AlreadyAcknowledgedRESTError_FallsBackToEdit(t *testing.T) {
	// Typed RESTError path: this is what real discordgo returns in prod.
	restErr := &discordgo.RESTError{
		Message: &discordgo.APIErrorMessage{Code: 40060, Message: "Interaction has already been acknowledged."},
	}
	f := &fakeResponder{
		RespondErrs: []error{restErr},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}}

	HandleError(f, i, "boom")

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[1].Method != "Edit" {
		t.Fatalf("expected fallback Edit, got %q", calls[1].Method)
	}
}

func TestHandleError_AppCommand_NonAckError_NoFallback(t *testing.T) {
	f := &fakeResponder{
		RespondErrs: []error{errors.New("HTTP 401 Unauthorized")},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}}

	HandleError(f, i, "boom")

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call (Respond only, no fallback), got %d: %+v", len(calls), calls)
	}
}

func TestHandleError_Component_RespondSucceeds(t *testing.T) {
	f := &fakeResponder{}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
	}}

	HandleError(f, i, "cancelled")

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Response.Type != discordgo.InteractionResponseUpdateMessage {
		t.Fatalf("expected UpdateMessage, got %v", calls[0].Response.Type)
	}
	if calls[0].Response.Data.Flags&discordgo.MessageFlagsEphemeral != 0 {
		t.Fatalf("expected Ephemeral flag NOT set on component path, got flags=%v", calls[0].Response.Data.Flags)
	}
}

func TestHandleError_Component_AlreadyAcknowledged_FallsBackToEdit(t *testing.T) {
	f := &fakeResponder{
		RespondErrs: []error{errors.New("already been acknowledged")},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
	}}

	HandleError(f, i, "cancelled")

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[1].Method != "Edit" {
		t.Fatalf("expected fallback Edit, got %q", calls[1].Method)
	}
}

func TestHandleError_Component_NonAckError_NoFallback(t *testing.T) {
	f := &fakeResponder{
		RespondErrs: []error{errors.New("HTTP 500")},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
	}}

	HandleError(f, i, "cancelled")

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call (no fallback on non-ack error), got %d", len(calls))
	}
}
