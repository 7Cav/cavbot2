package utils

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// TestMain initializes the package-level Logger so calls to Info/Warn/Debug
// inside the code under test don't panic on a nil *slog.Logger.
func TestMain(m *testing.M) {
	Logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	os.Exit(m.Run())
}

// withTestAPIServer redirects makeAPIRequest at the given httptest.Server for
// the duration of the test, then restores the production URL on cleanup.
func withTestAPIServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Cleanup(SetAPIBaseURLForTest(srv.URL))
}

type recordedCall struct {
	Method   string                         // "Respond", "Edit", "Followup"
	Response *discordgo.InteractionResponse // populated for "Respond"
	Edit     *discordgo.WebhookEdit         // populated for "Edit"
	Wait     bool                           // populated for "Followup"
	Params   *discordgo.WebhookParams       // populated for "Followup"
}

// fakeResponder is the in-utils test double. It is *also* defined in
// commands/responder_test.go because Go does not allow cross-package
// access to test helpers. The duplication is acceptable while the shape
// is small; revisit if either copy grows past ~50 lines.
type fakeResponder struct {
	mu    sync.Mutex
	calls []recordedCall

	// Per-call error queues. Head is consumed on each invocation; empty queue = always nil.
	RespondErrs  []error
	EditErrs     []error
	FollowupErrs []error
}

func (f *fakeResponder) InteractionRespond(_ *discordgo.Interaction, resp *discordgo.InteractionResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{Method: "Respond", Response: resp})
	return popErr(&f.RespondErrs)
}

func (f *fakeResponder) InteractionResponseEdit(_ *discordgo.Interaction, edit *discordgo.WebhookEdit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{Method: "Edit", Edit: edit})
	return popErr(&f.EditErrs)
}

func (f *fakeResponder) FollowupMessageCreate(_ *discordgo.Interaction, wait bool, params *discordgo.WebhookParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{Method: "Followup", Wait: wait, Params: params})
	return popErr(&f.FollowupErrs)
}

func (f *fakeResponder) Calls() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedCall(nil), f.calls...)
}

func popErr(queue *[]error) error {
	if len(*queue) == 0 {
		return nil
	}
	err := (*queue)[0]
	*queue = (*queue)[1:]
	return err
}
