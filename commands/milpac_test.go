package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// userOption builds a User-typed slash-command option. The Value of a User
// option is the Discord ID string; UserValue(nil) resolves it to a
// *discordgo.User carrying just that ID — which is all runMilpac consumes.
func userOption(name, discordID string) *discordgo.ApplicationCommandInteractionDataOption {
	return &discordgo.ApplicationCommandInteractionDataOption{
		Name:  name,
		Type:  discordgo.ApplicationCommandOptionUser,
		Value: discordID,
	}
}

// serveMilpacByDiscordID stands up an httptest.Server that returns the marshaled
// profile for any /milpac/discord/<id> request (the path GetMilpacByDiscordID
// hits), 404 otherwise. It redirects makeAPIRequest at the server via
// SetAPIBaseURLForTest and registers cleanups.
func serveMilpacByDiscordID(t *testing.T, profile utils.ProfileResponse) {
	t.Helper()
	body, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("serveMilpacByDiscordID marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/milpac/discord/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

// serveMilpacStatus stands up a server that always replies with the given
// status (and no body) for the discord-ID endpoint, to exercise upstream-error
// paths.
func serveMilpacStatus(t *testing.T, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

// milpacProfileWithSecondaries is a complete ProfileResponse exercising the
// "has secondary positions" embed branch. Dates are fixed so the embed renders
// deterministically; the uniform URL matches the /\d+/(\d+)\.jpg regex (id 456).
func milpacProfileWithSecondaries() utils.ProfileResponse {
	return utils.ProfileResponse{
		User:          utils.User{UserID: "42", Username: "Test.Trooper"},
		Gamertag:      "TestGamer",
		Rank:          utils.Rank{RankShort: "SPC", RankFull: "Specialist", RankImageUrl: "https://example.com/rank.png"},
		RealName:      "Test Trooper",
		UniformUrl:    "https://7cav.us/data/roster_uniforms/0/456.jpg",
		Roster:        "ROSTER_TYPE_COMBAT",
		Primary:       utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
		Secondary:     []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
		JoinDate:      "2023-01-15",
		PromotionDate: "2024-02-20",
		DiscordID:     "111",
		Records: []utils.Record{
			{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2023-01-15", RecordDetails: "Enlisted into the 7th Cavalry"},
		},
	}
}

func TestRunMilpac_SuccessByDiscordID(t *testing.T) {
	profile := milpacProfileWithSecondaries()
	serveMilpacByDiscordID(t, profile)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runMilpac(f, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder Respond + final Edit), got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "Respond" {
		t.Fatalf("calls[0]: expected Respond, got %q", calls[0].Method)
	}
	if !strings.Contains(calls[0].Response.Data.Content, "Fetching Milpac data") {
		t.Fatalf("calls[0]: expected placeholder content, got %q", calls[0].Response.Data.Content)
	}
	if calls[1].Method != "Edit" {
		t.Fatalf("calls[1]: expected Edit, got %q", calls[1].Method)
	}
	if calls[1].Edit.Embeds == nil || len(*calls[1].Edit.Embeds) != 1 {
		t.Fatalf("calls[1]: expected exactly one embed, got %+v", calls[1].Edit.Embeds)
	}
	embed := (*calls[1].Edit.Embeds)[0]
	if !strings.Contains(embed.Title, "Specialist Test Trooper") {
		t.Fatalf("embed title = %q, want it to contain rank+name", embed.Title)
	}
	if embed.URL != "https://7cav.us/rosters/profile/456" {
		t.Fatalf("embed URL = %q, want milpac profile URL with id 456", embed.URL)
	}
	// Secondary-positions branch renders a Gamertag field; assert it's present.
	var sawGamertag, sawSecondary bool
	for _, fld := range embed.Fields {
		switch fld.Name {
		case "Gamertag":
			sawGamertag = true
		case "Secondary Positions":
			sawSecondary = true
		}
	}
	if !sawGamertag || !sawSecondary {
		t.Fatalf("expected Gamertag and Secondary Positions fields; sawGamertag=%v sawSecondary=%v", sawGamertag, sawSecondary)
	}
}

func TestRunMilpac_SuccessNoSecondaries(t *testing.T) {
	profile := milpacProfileWithSecondaries()
	profile.Secondary = nil // exercise the no-secondaries embed branch
	serveMilpacByDiscordID(t, profile)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runMilpac(f, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %+v", len(calls), calls)
	}
	embed := (*calls[1].Edit.Embeds)[0]
	for _, fld := range embed.Fields {
		if fld.Name == "Gamertag" || fld.Name == "Secondary Positions" {
			t.Fatalf("no-secondaries branch should omit %q field", fld.Name)
		}
	}
}

func TestRunMilpac_NotFound_FallsThroughHandleError(t *testing.T) {
	serveMilpacStatus(t, http.StatusNotFound)

	// Placeholder Respond succeeds; HandleError's Respond hits "already
	// acknowledged" (simulated) and falls back to Edit.
	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction(userOption("user", "404"))

	runMilpac(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls (placeholder + HandleError Respond + Edit fallback), got %d: %+v", len(calls), calls)
	}
	if calls[2].Method != "Edit" {
		t.Fatalf("calls[2]: expected Edit (HandleError fallback), got %q", calls[2].Method)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "no milpac found") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("calls[2]: expected 'no milpac found' in error, got %q", got)
	}
}

func TestRunMilpac_UpstreamError_FallsThroughHandleError(t *testing.T) {
	serveMilpacStatus(t, http.StatusInternalServerError)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction(userOption("user", "500"))

	runMilpac(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(calls), calls)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "milpac API returned 500") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("calls[2]: expected '500' surfaced in user-visible error, got %q", got)
	}
}

func TestRunMilpac_BadJoinDate_FallsThroughHandleError(t *testing.T) {
	profile := milpacProfileWithSecondaries()
	profile.JoinDate = "not-a-date"
	serveMilpacByDiscordID(t, profile)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runMilpac(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(calls), calls)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "Failed to parse join date") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("calls[2]: expected 'Failed to parse join date', got %q", got)
	}
}

func TestRunMilpac_BadUniformURL_FallsThroughHandleError(t *testing.T) {
	profile := milpacProfileWithSecondaries()
	profile.UniformUrl = "garbage"
	serveMilpacByDiscordID(t, profile)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runMilpac(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(calls), calls)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "uniform URL") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("calls[2]: expected 'uniform URL' error, got %q", got)
	}
}
