package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestDetermineEnlistmentRecordType mirrors the afsm TestDetermineRecordType
// table test. determineEnlistmentRecordType classifies a milpac record's detail
// string as "leave" (Retired / Placed on ELOA / Discharge) or "join"
// (everything else: Enlisted, Reinstated, Returned, Assigned, ...). The "join"
// default is what feeds the start of a service period in calculateTotalService.
func TestDetermineEnlistmentRecordType(t *testing.T) {
	cases := []struct {
		name    string
		details string
		want    string
	}{
		{"enlisted is join", "Enlisted into the 7th Cavalry", "join"},
		{"reinstated is join", "Reinstated to active duty", "join"},
		{"returned is join", "Returned from ELOA", "join"},
		{"retired is leave", "Retired from the regiment", "leave"},
		{"placed on eloa is leave", "Placed on ELOA", "leave"},
		{"discharge is leave", "Discharged from the regiment", "leave"},
		// "Returned from ELOA" contains the substring "ELOA" but NOT "Placed on
		// ELOA" — it must classify as join, guarding the exact-phrase check.
		{"eloa substring without 'Placed on' is join", "ELOA ended, member returned", "join"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := determineEnlistmentRecordType(tc.details); got != tc.want {
				t.Fatalf("determineEnlistmentRecordType(%q) = %q, want %q", tc.details, got, tc.want)
			}
		})
	}
}

// TestCalculateTotalService_MultiPeriod drives the multi-period accumulation:
// join -> leave -> rejoin. The first closed period (2020-01-01 .. 2021-01-01)
// contributes a fixed 366-day span (2020 is a leap year). The trailing rejoin
// has no closing leave, so it accumulates time.Since(rejoin) — wall-clock
// dependent — which is why only the closed-period contribution is asserted
// exactly, and the trailing period is asserted as a lower bound.
func TestCalculateTotalService_MultiPeriod(t *testing.T) {
	date := func(s string) time.Time { return mustParseDate(s) }
	rejoin := date("2022-06-01")

	assignments := []map[string]interface{}{
		{"record_date": date("2020-01-01"), "record_type": "join", "record_details": "Enlisted"},
		{"record_date": date("2021-01-01"), "record_type": "leave", "record_details": "Discharged"},
		{"record_date": rejoin, "record_type": "join", "record_details": "Reinstated"},
	}

	got := calculateTotalService(assignments)

	closedPeriod := date("2021-01-01").Sub(date("2020-01-01")) // 366 days (leap)
	trailing := time.Since(rejoin)
	wantMin := closedPeriod + trailing - time.Minute // allow tiny exec slack
	if got < wantMin {
		t.Fatalf("calculateTotalService = %s, want >= %s (closed %s + trailing %s)",
			got, wantMin, closedPeriod, trailing)
	}
	// Upper sanity bound: must not exceed closed + trailing + slack.
	if got > closedPeriod+trailing+time.Minute {
		t.Fatalf("calculateTotalService = %s, want <= %s", got, closedPeriod+trailing+time.Minute)
	}
}

// TestCalculateTotalService_ConsecutiveJoinsAndTrailing covers the
// "two consecutive joins" branch (second join does not reset the period start)
// and the no-active-period branch (a leave with no open period is a no-op).
func TestCalculateTotalService_ConsecutiveJoinsAndTrailing(t *testing.T) {
	date := func(s string) time.Time { return mustParseDate(s) }

	// leave-before-any-join is a no-op; two joins in a row keep the earlier
	// start; final leave closes the single accumulated period.
	assignments := []map[string]interface{}{
		{"record_date": date("2019-01-01"), "record_type": "leave", "record_details": "Discharged"},
		{"record_date": date("2020-01-01"), "record_type": "join", "record_details": "Enlisted"},
		{"record_date": date("2020-03-01"), "record_type": "join", "record_details": "Reinstated"},
		{"record_date": date("2020-06-01"), "record_type": "leave", "record_details": "Retired"},
	}

	got := calculateTotalService(assignments)
	// Period start is the FIRST join (2020-01-01), not the second, because a
	// second consecutive join must not overwrite currentPeriodStart.
	want := date("2020-06-01").Sub(date("2020-01-01"))
	if got != want {
		t.Fatalf("calculateTotalService = %s, want %s (start at first join, single closed period)", got, want)
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
	if calls[0].Response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("calls[0]: expected placeholder Type ChannelMessageWithSource, got %v", calls[0].Response.Type)
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

// TestRunMilpac_SuccessNoSecondaries pins the unified field list for a member
// with NO secondary positions: the Secondary Positions field is omitted, but the
// Gamertag field (non-empty here) and the "Initial Enlist Date:" label still
// render — these used to be dropped because the no-secondaries branch was a
// drifted hand-copy that never included them.
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
	fieldByName := make(map[string]string, len(embed.Fields))
	for _, fld := range embed.Fields {
		fieldByName[fld.Name] = fld.Value
	}
	if _, ok := fieldByName["Secondary Positions"]; ok {
		t.Fatalf("no-secondaries member must omit the Secondary Positions field; fields: %+v", embed.Fields)
	}
	if got := fieldByName["Gamertag"]; got != "TestGamer" {
		t.Fatalf("Gamertag field = %q, want %q to render even without secondaries", got, "TestGamer")
	}
	if tis := fieldByName["Time in Service"]; !strings.Contains(tis, "Initial Enlist Date:") {
		t.Fatalf("Time in Service = %q, want it to carry the 'Initial Enlist Date:' label", tis)
	}
}

// TestRunMilpac_EmptyGamertagOmitsField mirrors the PC-majority case (e.g.
// West.R): a member with secondary positions but no console gamertag. The empty
// value must omit the Gamertag field entirely rather than render a blank line —
// the original symptom that prompted the fix.
func TestRunMilpac_EmptyGamertagOmitsField(t *testing.T) {
	profile := milpacProfileWithSecondaries()
	profile.Gamertag = "" // PC member with no console gamertag
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
		if fld.Name == "Gamertag" {
			t.Fatalf("empty gamertag must omit the Gamertag field, got value %q", fld.Value)
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

// TestRunMilpac_EmptyPromotionDate_FallsBackToJoinDate exercises the
// PromotionDate == "" branch: promotionDate is set to joinDate, so the embed's
// "Promoted:" line renders the (uppercased) join date. The fixture also carries
// a join -> leave -> rejoin record set so the full-handler service-math path
// (assignment filtering -> sort -> calculateTotalService) runs end-to-end.
func TestRunMilpac_EmptyPromotionDate_FallsBackToJoinDate(t *testing.T) {
	profile := milpacProfileWithSecondaries()
	profile.PromotionDate = "" // trigger fallback to JoinDate
	profile.Records = []utils.Record{
		{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2023-01-15", RecordDetails: "Enlisted into the 7th Cavalry"},
		{RecordType: "RECORD_TYPE_DISCHARGE", RecordDate: "2023-06-15", RecordDetails: "Discharged from the regiment"},
		{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-01-15", RecordDetails: "Reinstated to active duty"},
	}
	serveMilpacByDiscordID(t, profile)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runMilpac(f, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder + Edit), got %d: %+v", len(calls), calls)
	}
	embed := (*calls[1].Edit.Embeds)[0]
	var rankField string
	for _, fld := range embed.Fields {
		if fld.Name == "Rank" {
			rankField = fld.Value
		}
	}
	if rankField == "" {
		t.Fatalf("expected a Rank field, got fields %+v", embed.Fields)
	}
	// JoinDate 2023-01-15 -> "15JAN2023" (uppercased 02Jan2006). Fallback means
	// the promotion line equals the join date.
	if !strings.Contains(rankField, "15JAN2023") {
		t.Fatalf("Rank field = %q, want promotion date to fall back to join date 15JAN2023", rankField)
	}
}

// TestRunMilpac_MalformedPromotionDate_FallsThroughHandleError exercises the
// PromotionDate parse-error path: a non-empty but unparseable value makes
// time.Parse fail, surfacing "Failed to parse promotion date" via HandleError.
func TestRunMilpac_MalformedPromotionDate_FallsThroughHandleError(t *testing.T) {
	profile := milpacProfileWithSecondaries()
	profile.PromotionDate = "not-a-date" // non-empty, unparseable
	serveMilpacByDiscordID(t, profile)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runMilpac(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls (placeholder + HandleError Respond + Edit fallback), got %d: %+v", len(calls), calls)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "Failed to parse promotion date") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("calls[2]: expected 'Failed to parse promotion date', got %q", got)
	}
}

// TestRunMilpac_FirstRespondFails_BailsBeforeAPI covers the early-bail branch
// when the placeholder InteractionRespond fails: runMilpac must call HandleError
// once and return before fetching the milpac (tripwire server fails the test if
// hit).
func TestRunMilpac_FirstRespondFails_BailsBeforeAPI(t *testing.T) {
	tripwireAPIServer(t)

	f := &fakeResponder{RespondErrs: []error{errFirstRespond}}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runMilpac(f, i)

	assertPlaceholderFailedBailout(t, f.Calls())
}
