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

// capturedError records one captureError seam invocation for assertions.
type capturedError struct {
	msg string
	err error
	kv  []any
}

// swapCaptureError replaces the package capture seam with a recorder for the
// duration of the test and returns a pointer to the slice of recorded calls, so
// a test can assert the capture / no-capture split without touching the global
// Sentry hub. Tests using this seam must stay non-parallel: it mutates the
// package-global captureError, so a t.Parallel() sibling would race the swap.
func swapCaptureError(t *testing.T) *[]capturedError {
	t.Helper()
	var recorded []capturedError
	orig := captureError
	captureError = func(msg string, err error, kv ...any) {
		recorded = append(recorded, capturedError{msg: msg, err: err, kv: kv})
	}
	t.Cleanup(func() { captureError = orig })
	return &recorded
}

// kvValue returns the value paired with key in a captureError kv slice.
func kvValue(kv []any, key string) (any, bool) {
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok && k == key {
			return kv[i+1], true
		}
	}
	return nil, false
}

func TestHandleError_AppCommand_NonAckError_Captures(t *testing.T) {
	captured := swapCaptureError(t)
	respondErr := errors.New("HTTP 503 Service Unavailable")
	f := &fakeResponder{RespondErrs: []error{respondErr}}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:    discordgo.InteractionApplicationCommand,
		GuildID: "guild-123",
	}}

	HandleError(f, i, "boom")

	if len(*captured) != 1 {
		t.Fatalf("expected 1 capture for a non-ack delivery failure, got %d: %+v", len(*captured), *captured)
	}
	if !errors.Is((*captured)[0].err, respondErr) {
		t.Fatalf("captured err = %v, want %v", (*captured)[0].err, respondErr)
	}
	if gid, ok := kvValue((*captured)[0].kv, "guild_id"); !ok || gid != "guild-123" {
		t.Fatalf("expected guild_id context %q in capture, got %v (ok=%v)", "guild-123", gid, ok)
	}
	// A non-ack failure must not attempt the edit fallback.
	if calls := f.Calls(); len(calls) != 1 {
		t.Fatalf("expected 1 call (Respond only), got %d", len(calls))
	}
}

func TestHandleError_AppCommand_FallbackEditFails_Captures(t *testing.T) {
	captured := swapCaptureError(t)
	editErr := errors.New("HTTP 500 Internal Server Error")
	f := &fakeResponder{
		RespondErrs: []error{errors.New("already been acknowledged")},
		EditErrs:    []error{editErr},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}}

	HandleError(f, i, "boom")

	if len(*captured) != 1 {
		t.Fatalf("expected 1 capture for a lost fallback edit, got %d: %+v", len(*captured), *captured)
	}
	if !errors.Is((*captured)[0].err, editErr) {
		t.Fatalf("captured err = %v, want the edit error %v", (*captured)[0].err, editErr)
	}
	calls := f.Calls()
	if len(calls) != 2 || calls[1].Method != "Edit" {
		t.Fatalf("expected Respond+Edit, got %+v", calls)
	}
}

func TestHandleError_AppCommand_RespondSucceeds_NoCapture(t *testing.T) {
	captured := swapCaptureError(t)
	f := &fakeResponder{}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}}

	HandleError(f, i, "boom")

	if len(*captured) != 0 {
		t.Fatalf("expected 0 captures on the normal success path, got %d: %+v", len(*captured), *captured)
	}
}

func TestHandleError_AppCommand_AlreadyAck_EditSucceeds_NoCapture(t *testing.T) {
	captured := swapCaptureError(t)
	f := &fakeResponder{
		RespondErrs: []error{errors.New("already been acknowledged")},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}}

	HandleError(f, i, "boom")

	if len(*captured) != 0 {
		t.Fatalf("expected 0 captures when the edit fallback succeeds, got %d: %+v", len(*captured), *captured)
	}
}

func TestHandleError_AppCommand_NoRawErrorBodyInUserMessage(t *testing.T) {
	swapCaptureError(t)
	// Respond fails as already-acknowledged while carrying a raw Discord body; the
	// edit fallback is the user-facing delivery and must carry only the sanitized
	// message, never the raw body.
	rawBody := `HTTP 400 Bad Request: {"message":"secret internal detail","code":40060} already been acknowledged`
	f := &fakeResponder{RespondErrs: []error{errors.New(rawBody)}}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
	}}

	HandleError(f, i, "❌ user-facing boom")

	calls := f.Calls()
	if len(calls) != 2 || calls[1].Method != "Edit" {
		t.Fatalf("expected Respond+Edit, got %+v", calls)
	}
	got := "<nil>"
	if calls[1].Edit.Content != nil {
		got = *calls[1].Edit.Content
	}
	if got != "❌ user-facing boom" {
		t.Fatalf("edit content = %q, want the sanitized message", got)
	}
	if strings.Contains(got, "secret internal detail") {
		t.Fatalf("raw Discord error body leaked into the user-facing message: %q", got)
	}
}

func TestHandleError_Component_NonAckError_Captures(t *testing.T) {
	captured := swapCaptureError(t)
	respondErr := errors.New("HTTP 502 Bad Gateway")
	f := &fakeResponder{RespondErrs: []error{respondErr}}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
	}}

	HandleError(f, i, "cancelled")

	if len(*captured) != 1 {
		t.Fatalf("expected 1 capture for a non-ack delivery failure, got %d: %+v", len(*captured), *captured)
	}
	if !errors.Is((*captured)[0].err, respondErr) {
		t.Fatalf("captured err = %v, want %v", (*captured)[0].err, respondErr)
	}
}

func TestHandleError_Component_FallbackEditFails_Captures(t *testing.T) {
	captured := swapCaptureError(t)
	editErr := errors.New("HTTP 500")
	f := &fakeResponder{
		RespondErrs: []error{errors.New("already been acknowledged")},
		EditErrs:    []error{editErr},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
	}}

	HandleError(f, i, "cancelled")

	if len(*captured) != 1 {
		t.Fatalf("expected 1 capture for a lost fallback edit, got %d: %+v", len(*captured), *captured)
	}
	if !errors.Is((*captured)[0].err, editErr) {
		t.Fatalf("captured err = %v, want the edit error %v", (*captured)[0].err, editErr)
	}
}

func TestHandleError_Component_RespondSucceeds_NoCapture(t *testing.T) {
	captured := swapCaptureError(t)
	f := &fakeResponder{}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
	}}

	HandleError(f, i, "cancelled")

	if len(*captured) != 0 {
		t.Fatalf("expected 0 captures on the normal success path, got %d: %+v", len(*captured), *captured)
	}
}

func TestHandleError_Component_AlreadyAck_EditSucceeds_NoCapture(t *testing.T) {
	captured := swapCaptureError(t)
	f := &fakeResponder{
		RespondErrs: []error{errors.New("already been acknowledged")},
	}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
	}}

	HandleError(f, i, "cancelled")

	if len(*captured) != 0 {
		t.Fatalf("expected 0 captures when the edit fallback succeeds, got %d: %+v", len(*captured), *captured)
	}
}

func TestHandleError_AppCommand_NonAckError_CapturesCommandName(t *testing.T) {
	// Exercises interactionCommandName's happy path: a real
	// ApplicationCommandInteractionData resolves to its command Name in the
	// capture context. Every other test leaves i.Data nil, so this closes the
	// gap on the type-assertion success branch.
	captured := swapCaptureError(t)
	f := &fakeResponder{RespondErrs: []error{errors.New("HTTP 503 Service Unavailable")}}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{Name: "milpac"},
	}}

	HandleError(f, i, "boom")

	if len(*captured) != 1 {
		t.Fatalf("expected 1 capture for a non-ack delivery failure, got %d: %+v", len(*captured), *captured)
	}
	if cmd, ok := kvValue((*captured)[0].kv, "command"); !ok || cmd != "milpac" {
		t.Fatalf("expected command context %q in capture, got %v (ok=%v)", "milpac", cmd, ok)
	}
}

func TestHandleError_Component_NonAckError_CapturesCustomID(t *testing.T) {
	// Component interactions carry no command Name, so the capture context falls
	// back to the component's CustomID — a blank command otherwise hurts triage.
	captured := swapCaptureError(t)
	f := &fakeResponder{RespondErrs: []error{errors.New("HTTP 502 Bad Gateway")}}
	i := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		Data: discordgo.MessageComponentInteractionData{CustomID: "warden_purge_confirm"},
	}}

	HandleError(f, i, "cancelled")

	if len(*captured) != 1 {
		t.Fatalf("expected 1 capture for a non-ack delivery failure, got %d: %+v", len(*captured), *captured)
	}
	if cmd, ok := kvValue((*captured)[0].kv, "command"); !ok || cmd != "warden_purge_confirm" {
		t.Fatalf("expected component CustomID %q as command context, got %v (ok=%v)", "warden_purge_confirm", cmd, ok)
	}
}
