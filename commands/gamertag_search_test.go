package commands

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// gamertagProfileJSON is a minimal valid ProfileResponse body — only the
// fields runGamertagSearch reads matter.
const gamertagProfileJSON = `{
	"user": {"userId": "42", "username": "Spec.Ops"},
	"gamertag": "SpecOps",
	"rank": {"rankShort": "SPC", "rankFull": "Specialist"},
	"realName": "Test User",
	"uniformUrl": "https://example.com/cdn/123/456.jpg",
	"discordId": "111"
}`

func TestRunGamertagSearch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, gamertagProfileJSON)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("gamertag", "SpecOps"))

	runGamertagSearch(f, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder Respond + final Edit), got %d: %+v", len(calls), calls)
	}

	// Placeholder Respond.
	if calls[0].Method != "Respond" {
		t.Fatalf("calls[0]: expected Respond, got %q", calls[0].Method)
	}
	if calls[0].Response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("calls[0]: expected ChannelMessageWithSource, got %v", calls[0].Response.Type)
	}
	if !strings.Contains(calls[0].Response.Data.Content, "SpecOps") {
		t.Fatalf("calls[0]: expected placeholder content to mention gamertag, got %q", calls[0].Response.Data.Content)
	}

	// Final Edit with the milpac link.
	if calls[1].Method != "Edit" {
		t.Fatalf("calls[1]: expected Edit, got %q", calls[1].Method)
	}
	if calls[1].Edit.Content == nil {
		t.Fatalf("calls[1]: expected Edit Content to be non-nil")
	}
	if !strings.Contains(*calls[1].Edit.Content, "https://7cav.us/rosters/profile/456") {
		t.Fatalf("calls[1]: expected milpac URL with id 456, got %q", *calls[1].Edit.Content)
	}
}

func TestRunGamertagSearch_API404_FallsThroughHandleError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))

	// Placeholder Respond succeeds; HandleError's Respond hits "already
	// acknowledged" (simulated) and falls back to Edit.
	f := &fakeResponder{
		RespondErrs: []error{nil, errAlreadyAcked},
	}
	i := fakeAppCommandInteraction(stringOption("gamertag", "Unknown"))

	runGamertagSearch(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls (placeholder + HandleError Respond + Edit fallback), got %d: %+v", len(calls), calls)
	}
	// calls[0] = placeholder Respond; calls[1] = HandleError's Respond (fails);
	// calls[2] = HandleError's Edit fallback with the error message.
	if calls[2].Method != "Edit" {
		t.Fatalf("calls[2]: expected Edit (HandleError fallback), got %q", calls[2].Method)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "no milpac found") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("calls[2]: expected error content containing 'no milpac found', got %q", got)
	}
}

func TestRunGamertagSearch_API401_FallsThroughHandleError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))

	f := &fakeResponder{
		RespondErrs: []error{nil, errAlreadyAcked},
	}
	i := fakeAppCommandInteraction(stringOption("gamertag", "Anyone"))

	runGamertagSearch(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "milpac API returned 401") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("expected '401' surfaced in user-visible error, got %q", got)
	}
}

func TestRunGamertagSearch_BadUniformURL_FallsThroughHandleError(t *testing.T) {
	// Profile is valid JSON but UniformUrl doesn't match the regex.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"user": {"username": "X"},
			"gamertag": "X",
			"rank": {"rankShort": "PVT"},
			"uniformUrl": "not-a-valid-url"
		}`)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))

	f := &fakeResponder{
		RespondErrs: []error{nil, errAlreadyAcked},
	}
	i := fakeAppCommandInteraction(stringOption("gamertag", "X"))

	runGamertagSearch(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "uniform URL") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("expected error mentioning 'uniform URL', got %q", got)
	}
}

// errAlreadyAcked simulates the SDK error string HandleError matches on.
var errAlreadyAcked = stubError("HTTP 400 Bad Request, {\"message\": \"Interaction has already been acknowledged.\", \"code\": 40060}")

// errFirstRespond is a plain (non-already-acknowledged) failure used to fail the
// FIRST placeholder InteractionRespond. Because it does NOT match
// isAlreadyAcknowledged, HandleError's own Respond succeeds and it does not fall
// back to Edit — so the placeholder-fails path produces exactly 2 Respond calls.
var errFirstRespond = stubError("HTTP 503 Service Unavailable: gateway temporarily down")

type stubError string

func (e stubError) Error() string { return string(e) }

// tripwireAPIServer stands up an httptest.Server that fails the test if it
// receives ANY request, and points makeAPIRequest at it via
// SetAPIBaseURLForTest. Used by the placeholder-fails tests to prove the command
// bails before touching the upstream milpac API. The t.Errorf on any request is
// the assertion; the server and URL override are torn down via t.Cleanup.
func tripwireAPIServer(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream API must not be called after placeholder respond fails; got request to %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

// assertPlaceholderFailedBailout is the shared assertion for the
// "first InteractionRespond fails" path: the command must call HandleError
// exactly once (which itself emits a single Respond), yielding exactly two
// Respond calls total and no Edit. errFirstRespond is non-acked, so HandleError
// does not fall back to Edit.
func assertPlaceholderFailedBailout(t *testing.T, calls []recordedCall) {
	t.Helper()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder Respond fails + single HandleError Respond), got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "Respond" {
		t.Fatalf("calls[0]: expected placeholder Respond, got %q", calls[0].Method)
	}
	if calls[1].Method != "Respond" {
		t.Fatalf("calls[1]: expected HandleError Respond (no Edit fallback for non-acked error), got %q", calls[1].Method)
	}
}

func TestRunGamertagSearch_FirstRespondFails_BailsBeforeAPI(t *testing.T) {
	tripwireAPIServer(t)

	f := &fakeResponder{RespondErrs: []error{errFirstRespond}}
	i := fakeAppCommandInteraction(stringOption("gamertag", "SpecOps"))

	runGamertagSearch(f, i)

	assertPlaceholderFailedBailout(t, f.Calls())
}
