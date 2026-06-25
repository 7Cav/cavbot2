package commands

import (
	"errors"
	"net/http"
	"strings"
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
	i := wardenInteraction(guildID, stringOption("command", "purge"))
	i.ChannelID = channelID
	return i
}

// installCountingCapture swaps the captureError seam to both count how many
// times it fires and record the kv from the most recent call, so a test can
// assert exactly-one-capture and the context fields on the same install.
func installCountingCapture(t *testing.T) (*int, *[]any) {
	t.Helper()
	var count int
	var gotKV []any
	prev := captureError
	captureError = func(_ string, _ error, kv ...any) {
		count++
		gotKV = kv
	}
	t.Cleanup(func() { captureError = prev })
	return &count, &gotKV
}

// lastChannelMessage returns the content of the last ChannelMessageSend
// recorded by the fake, or "<none>".
func (g *fakeGuildManager) lastChannelMessage() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.channelMessages) == 0 {
		return "<none>"
	}
	return g.channelMessages[len(g.channelMessages)-1].content
}

// --- token-expiry classifier ---

func TestIsInteractionTokenExpired_50027IsExpiry(t *testing.T) {
	if !isInteractionTokenExpired(tokenExpiredRESTError()) {
		t.Fatal("an Invalid Webhook Token (50027) error must be treated as token expiry")
	}
}

func TestIsInteractionTokenExpired_UnknownWebhookIsExpiry(t *testing.T) {
	err := restError(http.StatusNotFound, discordgo.ErrCodeUnknownWebhook, "Unknown Webhook")
	if !isInteractionTokenExpired(err) {
		t.Fatal("an Unknown Webhook (10015) error must be treated as token expiry")
	}
}

func TestIsInteractionTokenExpired_UnknownInteractionIsExpiry(t *testing.T) {
	err := restError(http.StatusNotFound, discordgo.ErrCodeUnknownInteraction, "Unknown Interaction")
	if !isInteractionTokenExpired(err) {
		t.Fatal("an Unknown Interaction (10062) error must be treated as token expiry")
	}
}

func TestIsInteractionTokenExpired_5xxIsNotExpiry(t *testing.T) {
	err := restError(http.StatusInternalServerError, 0, "boom")
	if isInteractionTokenExpired(err) {
		t.Fatal("a 5xx must NOT be misclassified as token expiry — it is an unexpected fault")
	}
}

func TestIsInteractionTokenExpired_TransportErrorIsNotExpiry(t *testing.T) {
	if isInteractionTokenExpired(errors.New("dial tcp: connection refused")) {
		t.Fatal("a transport error must NOT be classified as token expiry")
	}
}

// --- deliverPurgeSummary: happy path ---

// On a successful edit, the summary is delivered ephemerally and the fallback
// channel surface is never touched, and nothing is captured to Sentry.
func TestDeliverPurgeSummary_HappyPathEditsNoFallback(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{} // edit succeeds
	gm := &fakeGuildManager{}
	i := purgeInteractionWithChannel("guild-1", "chan-9")

	deliverPurgeSummary(f, gm, i, "✅ Purge complete.")

	if got := countMethod(f.Calls(), "Edit"); got != 1 {
		t.Fatalf("expected exactly 1 Edit on the happy path, got %d", got)
	}
	if got := gm.countCalls("ChannelMessageSend"); got != 0 {
		t.Fatalf("happy path must NOT use the channel fallback; got %d ChannelMessageSend", got)
	}
	if rec.count != 0 {
		t.Fatalf("happy path must not capture to Sentry; got %d", rec.count)
	}
}

// --- deliverPurgeSummary: token-expiry fallback ---

// When the deferred edit fails with a token-expiry error, the summary must be
// delivered through the channel-message fallback carrying the same summary.
func TestDeliverPurgeSummary_TokenExpiryFallsBackToChannel(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)

	f := &fakeResponder{EditErrs: []error{tokenExpiredRESTError()}}
	gm := &fakeGuildManager{}
	i := purgeInteractionWithChannel("guild-1", "chan-9")
	summary := "✅ Recreated 'Verified Warden Internal'."

	deliverPurgeSummary(f, gm, i, summary)

	if got := gm.countCalls("ChannelMessageSend"); got != 1 {
		t.Fatalf("a token-expired edit must fall back to one ChannelMessageSend; got %d (%v)", got, gm.Calls())
	}
	if got := gm.lastChannelMessage(); !strings.Contains(got, "Recreated 'Verified Warden Internal'") {
		t.Fatalf("fallback channel message must carry the purge summary, got %q", got)
	}
	// Token expiry is an expected end-of-window condition, not a system fault:
	// the operator was reached via the fallback, so it must NOT page Sentry.
	if rec.count != 0 {
		t.Fatalf("a token-expiry fallback delivery must NOT capture to Sentry; got %d", rec.count)
	}
}

// The fallback channel message must be addressed to the invoking channel.
func TestDeliverPurgeSummary_TokenExpiryFallbackTargetsInvokingChannel(t *testing.T) {
	f := &fakeResponder{EditErrs: []error{tokenExpiredRESTError()}}
	gm := &fakeGuildManager{}
	i := purgeInteractionWithChannel("guild-1", "chan-target")

	deliverPurgeSummary(f, gm, i, "summary")

	gm.mu.Lock()
	defer gm.mu.Unlock()
	if len(gm.channelMessages) != 1 {
		t.Fatalf("expected exactly 1 channel message, got %d", len(gm.channelMessages))
	}
	if gm.channelMessages[0].channelID != "chan-target" {
		t.Fatalf("fallback must target the invoking channel id, got %q", gm.channelMessages[0].channelID)
	}
}

// --- deliverPurgeSummary: unexpected (non-expiry) edit failure ---

// A non-expiry edit failure (e.g. a 5xx) is an unexpected fault: it must be
// captured to Sentry with context and must NOT be delivered as if expiry.
func TestDeliverPurgeSummary_UnexpectedFailureCapturedNotFallback(t *testing.T) {
	captures, gotKV := installCountingCapture(t)

	f := &fakeResponder{EditErrs: []error{restError(http.StatusInternalServerError, 0, "boom")}}
	gm := &fakeGuildManager{}
	i := purgeInteractionWithChannel("guild-7", "chan-9")

	deliverPurgeSummary(f, gm, i, "summary")

	// The edit is attempted exactly once: an unexpected failure captures, it does
	// not retry the already-acknowledged edit.
	if got := countMethod(f.Calls(), "Edit"); got != 1 {
		t.Fatalf("expected exactly 1 Edit (the failing call, no retry), got %d", got)
	}
	if got := gm.countCalls("ChannelMessageSend"); got != 0 {
		t.Fatalf("an unexpected (non-expiry) edit failure must NOT use the channel fallback; got %d", got)
	}
	if *captures != 1 {
		t.Fatalf("expected exactly 1 Sentry capture on an unexpected failure, got %d", *captures)
	}
	kvMap := kvToMap(*gotKV)
	if kvMap["command"] != "purge" {
		t.Fatalf("expected command=purge in capture context, got %v", kvMap["command"])
	}
	if kvMap["guild_id"] != "guild-7" {
		t.Fatalf("expected guild_id=guild-7 in capture context, got %v", kvMap["guild_id"])
	}
}

// When the edit expires AND the channel fallback also fails, the operator
// cannot be reached at all — that is a genuine delivery fault, so it must be
// captured to Sentry with command + guild context.
func TestDeliverPurgeSummary_BothSurfacesFailCaptures(t *testing.T) {
	captures, gotKV := installCountingCapture(t)

	editErr := tokenExpiredRESTError()
	f := &fakeResponder{EditErrs: []error{editErr}}
	gm := &fakeGuildManager{ChannelMessageErrs: []error{errors.New("missing access")}}
	i := purgeInteractionWithChannel("guild-3", "chan-9")

	deliverPurgeSummary(f, gm, i, "summary")

	if got := gm.countCalls("ChannelMessageSend"); got != 1 {
		t.Fatalf("expected the fallback to be attempted once, got %d", got)
	}
	if *captures != 1 {
		t.Fatalf("expected exactly 1 Sentry capture when both surfaces fail, got %d", *captures)
	}
	kvMap := kvToMap(*gotKV)
	if kvMap["command"] != "purge" {
		t.Fatalf("expected command=purge in capture context, got %v", kvMap["command"])
	}
	if kvMap["guild_id"] != "guild-3" {
		t.Fatalf("expected guild_id=guild-3 in capture context, got %v", kvMap["guild_id"])
	}
	// The capture must carry the original token-expiry edit error too, so on-call
	// sees the full failure chain (edit expired AND channel send failed).
	if kvMap["edit_error"] != error(editErr) {
		t.Fatalf("expected the original edit error in capture context, got %v", kvMap["edit_error"])
	}
}

// --- runWardenPurge wires the summary through deliverPurgeSummary ---

// The end-to-end purge path must reach the channel fallback when its deferred
// edit expires, so a long purge that finished server-side still reaches the
// operator. Drives runWardenPurge directly (synchronous, no goroutine).
func TestRunWardenPurge_TokenExpiryReachesChannelFallback(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("old-int", wardenRoleBaseName+" Internal")},
	}
	f := &fakeResponder{EditErrs: []error{tokenExpiredRESTError()}}
	i := purgeInteractionWithChannel("guild-1", "chan-9")

	runWardenPurge(f, gm, i, "guild-1", "internal")

	if got := gm.countCalls("ChannelMessageSend"); got != 1 {
		t.Fatalf("a token-expired purge edit must reach the channel fallback once; got %d (%v)", got, gm.Calls())
	}
	if got := gm.lastChannelMessage(); !strings.Contains(got, "Recreated 'Verified Warden Internal'") {
		t.Fatalf("fallback message must carry the recreated-role summary, got %q", got)
	}
}
