package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// afsmRefDate is the pinned "now" for all AFSM unit tests. Choosing a fixed
// reference date makes test fixtures reproducible — relative dates would
// drift as the wall clock advances and risk flipping the >1yr / <1yr boundaries.
var afsmRefDate = mustParseDate("2026-05-15")

func mustParseDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic("mustParseDate: " + err.Error())
	}
	return t
}

// serveProfile spins up an httptest.Server that responds with the marshaled
// profile for any /milpacs/profile/username/<name> request, 404 otherwise.
// It applies SetAPIBaseURLForTest and registers cleanups via t.Cleanup.
//
// This narrow seam is what makes evaluateAFSMMember unit-testable: the helper
// calls utils.GetMilpacByUsername internally, which hits apiBaseURL via resty.
func serveProfile(t *testing.T, profile utils.ProfileResponse) {
	t.Helper()
	body, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("serveProfile marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/milpacs/profile/username/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

// serveRosterAndProfiles handles both endpoints runAFSM hits. The roster body
// is returned for /milpacs/position/search/<dept>. Per-username requests look
// up the username from the URL tail in profilesByUsername; 404 if absent.
//
// rosterStatus lets a test simulate transport errors (e.g. 500).
func serveRosterAndProfiles(
	t *testing.T,
	roster utils.LiteRosterResponse,
	rosterStatus int,
	profilesByUsername map[string]utils.ProfileResponse,
) {
	t.Helper()
	rosterBody, err := json.Marshal(roster)
	if err != nil {
		t.Fatalf("serveRosterAndProfiles marshal roster: %v", err)
	}
	encodedProfiles := make(map[string][]byte, len(profilesByUsername))
	for name, p := range profilesByUsername {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("serveRosterAndProfiles marshal profile %q: %v", name, err)
		}
		encodedProfiles[name] = b
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/milpacs/position/search/"):
			if rosterStatus != 0 && rosterStatus != http.StatusOK {
				w.WriteHeader(rosterStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(rosterBody)
		case strings.HasPrefix(r.URL.Path, "/milpacs/profile/username/"):
			name := strings.TrimPrefix(r.URL.Path, "/milpacs/profile/username/")
			body, ok := encodedProfiles[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

func TestDetermineRecordType(t *testing.T) {
	cases := []struct {
		name    string
		details string
		want    string
	}{
		{"relieved is leave", "Relieved of Duties as S6 Lead", "leave"},
		{"eloa is leave", "Placed on ELOA", "leave"},
		{"discharge is leave", "Discharged from regiment", "leave"},
		{"retired is leave", "Retired from regiment", "leave"},
		{"assigned is join", "Assigned S6 Operations Staff IT", "join"},
		{"relieved-then-assigned is leave (Relieved wins)",
			"Relieved of duties as RRD Squad Recruiter; Assigned RRD Hell Let Loose Recruiter as Additional Duty",
			"leave"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := determineRecordType(tc.details)
			if got != tc.want {
				t.Fatalf("determineRecordType(%q) = %q, want %q", tc.details, got, tc.want)
			}
		})
	}
}

func TestEvaluateAFSMMember_EligiblePaths(t *testing.T) {
	const dept = "S6"
	ctx := context.Background()

	// Case 1: Eligible via secondary, no AFSM award. Date should equal startDate.
	t.Run("eligible via secondary, no award", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.A"},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/144.jpg",
		}
		profile := utils.ProfileResponse{
			User:       member.User,
			Primary:    member.Primary,
			Secondary:  member.Secondary,
			UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-05-15", RecordDetails: "Assigned S6 Operations Staff IT"},
			},
			// No awards.
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatalf("expected eligible member, got nil")
		}
		want := mustParseDate("2024-05-15")
		if !got.Date.Equal(want) {
			t.Fatalf("Date = %v, want %v (startDate)", got.Date, want)
		}
		if got.Username != "Test.A" {
			t.Fatalf("Username = %q, want %q", got.Username, "Test.A")
		}
		if got.MilpacUrl != "https://7cav.us/rosters/profile/144" {
			t.Fatalf("MilpacUrl = %q, want %q", got.MilpacUrl, "https://7cav.us/rosters/profile/144")
		}
	})

	// Case 6: Old AFSM award doesn't disqualify; Date should equal latestAward, not startDate.
	t.Run("old award eligible, refDate=latestAward", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.B"},
			Primary:    utils.Position{PositionTitle: "Aide to the XO"},
			Secondary:  []utils.Position{{PositionTitle: "S6 1IC"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/200.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2023-05-15", RecordDetails: "Assigned S6 Game Staff"},
			},
			Awards: []utils.Award{
				{AwardName: "Armed Forces Service Medal", AwardDate: "2025-03-15", AwardDetails: "For S6 Dept. 23SEP22 - 23SEP23"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatalf("expected eligible member, got nil")
		}
		want := mustParseDate("2025-03-15")
		if !got.Date.Equal(want) {
			t.Fatalf("Date = %v, want %v (latestAward, not startDate)", got.Date, want)
		}
	})

	// Case 7: Multiple AFSM/S6 awards → uses the most recent.
	t.Run("multi-award picks latest", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.C"},
			Primary:    utils.Position{PositionTitle: "Section Leader 1/2/A/ACD"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Senior Game Staff"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/300.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2023-05-15", RecordDetails: "Assigned S6 Senior Game Staff"},
			},
			Awards: []utils.Award{
				{AwardName: "Armed Forces Service Medal", AwardDate: "2023-05-15", AwardDetails: "For S6 Dept. 23SEP21 - 23SEP22"},
				{AwardName: "Armed Forces Service Medal", AwardDate: "2025-03-15", AwardDetails: "For S6 Dept. 23SEP22 - 23SEP23"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatalf("expected eligible member, got nil")
		}
		want := mustParseDate("2025-03-15") // the newer of the two
		if !got.Date.Equal(want) {
			t.Fatalf("Date = %v, want %v (newer of two awards)", got.Date, want)
		}
	})

	// Case 10: Expeditionary Medal is NOT the Service Medal — must be ignored.
	t.Run("expeditionary medal is not AFSM", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.D"},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Forums Staff"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/400.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-05-15", RecordDetails: "Assigned S6 Forums Staff"},
			},
			Awards: []utils.Award{
				// Looks AFSM-ish but isn't the exact AwardName the code matches on.
				{AwardName: "Armed Forces Expeditionary Medal", AwardDate: "2025-11-15", AwardDetails: "For S6 Dept. 2025"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatalf("expected eligible (Expeditionary Medal ignored), got nil")
		}
		want := mustParseDate("2024-05-15") // startDate, since no real AFSM
		if !got.Date.Equal(want) {
			t.Fatalf("Date = %v, want %v", got.Date, want)
		}
	})
}

func TestEvaluateAFSMMember_IneligiblePaths(t *testing.T) {
	const dept = "S6"
	ctx := context.Background()

	// Case 2: Primary contains dept → disqualified even though secondary also matches.
	t.Run("primary disqualifier", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.E"},
			Primary:    utils.Position{PositionTitle: "S6 2IC"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/500.jpg",
		}
		// profile body is never reached when ineligibility is decided pre-fetch,
		// but the helper still tries — serve a minimal one.
		serveProfile(t, utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
		})

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil (primary disqualifier), got %+v", got)
		}
	})

	// Case 3: No secondary contains dept → ineligible without any milpac fetch.
	t.Run("no secondary match", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:      utils.User{Username: "Test.F"},
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S1 Analytics Senior"}},
		}
		// Profile NOT served — should never be requested. If it is, the test
		// will surface that as a fetch error from the per-member call.
		serveProfile(t, utils.ProfileResponse{User: member.User})

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil (no S6 secondary), got %+v", got)
		}
	})

	// Case 4: Eligible by position but only served 6mo.
	t.Run("served too short", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.G"},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Forums Staff"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/600.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2025-11-15", RecordDetails: "Assigned S6 Forums Staff"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil (served <1yr), got %+v", got)
		}
	})

	// Case 5: Recent AFSM/S6 award disqualifies.
	t.Run("recent AFSM award disqualifies", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.H"},
			Primary:    utils.Position{PositionTitle: "Section Leader 1/2/A/ACD"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Game Staff"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/700.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-05-15", RecordDetails: "Assigned S6 Game Staff"},
			},
			Awards: []utils.Award{
				{AwardName: "Armed Forces Service Medal", AwardDate: "2025-11-15", AwardDetails: "For S6 Dept. 23SEP24 - 23SEP25"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil (recent S6 AFSM), got %+v", got)
		}
	})
}

func TestEvaluateAFSMMember_DischargeChain(t *testing.T) {
	const dept = "S6"
	ctx := context.Background()

	// Case 8: Join → discharge mid-chain → re-join. startDate must use the
	// re-join date (after the reset from the discharge record), not the original.
	t.Run("discharge then rejoin uses rejoin date", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.I"},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/800.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2021-05-15", RecordDetails: "Assigned S6 Operations Staff IT"},
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2022-05-15", RecordDetails: "Relieved of Duties as S6 Operations Staff IT"},
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-11-15", RecordDetails: "Assigned S6 Operations Staff IT"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatalf("expected eligible (rejoin >1yr ago), got nil")
		}
		want := mustParseDate("2024-11-15")
		if !got.Date.Equal(want) {
			t.Fatalf("Date = %v, want %v (rejoin, not original join)", got.Date, want)
		}
	})

	// Case 9: Same shape as Case 8 but rejoin too recent → ineligible.
	t.Run("discharge then rejoin too recent", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.J"},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/900.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2021-05-15", RecordDetails: "Assigned S6 Operations Staff IT"},
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2022-05-15", RecordDetails: "Relieved of Duties as S6 Operations Staff IT"},
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2025-09-15", RecordDetails: "Assigned S6 Operations Staff IT"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil (rejoin only 8mo ago), got %+v", got)
		}
	})
}

func TestEvaluateAFSMMember_ErrorPaths(t *testing.T) {
	const dept = "S6"
	ctx := context.Background()

	// Case 11: malformed RecordDate on a tracked record type → error.
	t.Run("malformed RecordDate", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.K"},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Forums Staff"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/1000.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "not-a-date", RecordDetails: "Assigned S6 Forums Staff"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if got != nil {
			t.Fatalf("expected nil result with error, got %+v", got)
		}
		if err == nil || !strings.Contains(err.Error(), "record date parse failed") {
			t.Fatalf("expected error containing 'record date parse failed', got %v", err)
		}
	})

	// Case 12: malformed AwardDate on a matching AFSM/S6 award → error.
	t.Run("malformed AwardDate", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.L"},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Lead"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/1100.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2023-05-15", RecordDetails: "Assigned S6 Lead"},
			},
			Awards: []utils.Award{
				{AwardName: "Armed Forces Service Medal", AwardDate: "bad", AwardDetails: "For S6 Dept. 2024"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if got != nil {
			t.Fatalf("expected nil result with error, got %+v", got)
		}
		if err == nil || !strings.Contains(err.Error(), "award date parse failed") {
			t.Fatalf("expected error containing 'award date parse failed', got %v", err)
		}
	})

	// Case 13: malformed UniformUrl on otherwise-eligible member → error.
	t.Run("malformed UniformUrl", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.M"},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Forums Staff"}},
			UniformUrl: "garbage",
		}
		profile := utils.ProfileResponse{
			User: member.User, Primary: member.Primary, Secondary: member.Secondary, UniformUrl: member.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-05-15", RecordDetails: "Assigned S6 Forums Staff"},
			},
		}
		serveProfile(t, profile)

		got, err := evaluateAFSMMember(ctx, member, dept, afsmRefDate)
		if got != nil {
			t.Fatalf("expected nil result with error, got %+v", got)
		}
		if err == nil || !strings.Contains(err.Error(), "uniform URL") {
			t.Fatalf("expected error containing 'uniform URL', got %v", err)
		}
	})
}

func TestRunAFSM_HappyPathWithSkipped(t *testing.T) {
	// 3-member roster:
	//   - A is eligible (S6 secondary, served >1yr, no recent AFSM)
	//   - B has no S6 secondary (ineligible)
	//   - C has a malformed RecordDate (triggers per-member error → skipped)
	memberA := utils.LiteProfileResponse{
		User: utils.User{Username: "Test.A"}, Primary: utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
		Secondary:  []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/144.jpg",
	}
	memberB := utils.LiteProfileResponse{
		User: utils.User{Username: "Test.B"}, Primary: utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
		Secondary:  []utils.Position{{PositionTitle: "S1 Analytics Senior"}},
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/200.jpg",
	}
	memberC := utils.LiteProfileResponse{
		User: utils.User{Username: "Test.C"}, Primary: utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
		Secondary:  []utils.Position{{PositionTitle: "S6 Forums Staff"}},
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/300.jpg",
	}

	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"144": memberA, "200": memberB, "300": memberC,
		},
	}
	profiles := map[string]utils.ProfileResponse{
		"Test.A": {
			User: memberA.User, Primary: memberA.Primary, Secondary: memberA.Secondary, UniformUrl: memberA.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-01-01", RecordDetails: "Assigned S6 Operations Staff IT"},
			},
		},
		// Test.B is never fetched because its secondary doesn't contain S6;
		// include a stub so a stray fetch surfaces visibly.
		"Test.B": {User: memberB.User, UniformUrl: memberB.UniformUrl},
		"Test.C": {
			User: memberC.User, Primary: memberC.Primary, Secondary: memberC.Secondary, UniformUrl: memberC.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "not-a-date", RecordDetails: "Assigned S6 Forums Staff"},
			},
		},
	}
	serveRosterAndProfiles(t, roster, http.StatusOK, profiles)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("department", "S6"))

	runAFSM(f, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 responder calls (placeholder Respond + final Edit), got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "Respond" {
		t.Fatalf("calls[0]: expected Respond, got %q", calls[0].Method)
	}
	if calls[0].Response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("calls[0]: expected placeholder Type ChannelMessageWithSource, got %v", calls[0].Response.Type)
	}
	if !strings.Contains(calls[0].Response.Data.Content, "S6") {
		t.Fatalf("placeholder content should mention S6, got %q", calls[0].Response.Data.Content)
	}
	if calls[1].Method != "Edit" {
		t.Fatalf("calls[1]: expected Edit, got %q", calls[1].Method)
	}
	if calls[1].Edit.Content == nil {
		t.Fatalf("Edit.Content is nil")
	}
	got := *calls[1].Edit.Content
	for _, want := range []string{
		"⚠️ This command cannot be made completely accurate",   // disclaimer
		"[Test.A](<https://7cav.us/rosters/profile/144>)",         // eligible row
		"1 member skipped due to errors (reported)",               // skipped count
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Edit content missing %q.\nGot:\n%s", want, got)
		}
	}
	// Ensure ineligible Test.B is NOT mentioned.
	if strings.Contains(got, "Test.B") {
		t.Fatalf("Edit content should not mention ineligible Test.B; got: %s", got)
	}
}

// serveRosterTrackingProfileConcurrency stands up a multiplexed server like
// serveRosterAndProfiles, but instruments the per-profile endpoint so the test
// can observe how many milpac fetches are in flight at once. Each profile
// handler blocks on a barrier long enough that any concurrent fetches overlap,
// then records the peak observed concurrency. A serial loop peaks at 1; a
// fanned-out implementation peaks at >1.
func serveRosterTrackingProfileConcurrency(
	t *testing.T,
	roster utils.LiteRosterResponse,
	profilesByUsername map[string]utils.ProfileResponse,
	peak *int32,
) {
	t.Helper()
	rosterBody, err := json.Marshal(roster)
	if err != nil {
		t.Fatalf("marshal roster: %v", err)
	}
	encodedProfiles := make(map[string][]byte, len(profilesByUsername))
	for name, p := range profilesByUsername {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal profile %q: %v", name, err)
		}
		encodedProfiles[name] = b
	}
	var inFlight int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/milpacs/position/search/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(rosterBody)
		case strings.HasPrefix(r.URL.Path, "/milpacs/profile/username/"):
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(peak)
				if cur <= old || atomic.CompareAndSwapInt32(peak, old, cur) {
					break
				}
			}
			// Hold the request open briefly so concurrent fetches genuinely
			// overlap before any returns — makes the peak observation reliable.
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)

			name := strings.TrimPrefix(r.URL.Path, "/milpacs/profile/username/")
			body, ok := encodedProfiles[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

func TestRunAFSM_FetchesConcurrently(t *testing.T) {
	// Four members all eligible by position (S6 secondary, no primary
	// disqualifier) so each forces a milpac fetch. A serial loop peaks at one
	// in-flight fetch; the fanned-out implementation peaks above one.
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	profiles := map[string]utils.ProfileResponse{}
	for _, id := range []string{"1", "2", "3", "4"} {
		name := "Member." + id
		m := utils.LiteProfileResponse{
			User:       utils.User{Username: name},
			Primary:    utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary:  []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/" + id + ".jpg",
		}
		roster.LiteProfiles[id] = m
		profiles[name] = utils.ProfileResponse{
			User: m.User, Primary: m.Primary, Secondary: m.Secondary, UniformUrl: m.UniformUrl,
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-01-01", RecordDetails: "Assigned S6 Operations Staff IT"},
			},
		}
	}

	var peak int32
	serveRosterTrackingProfileConcurrency(t, roster, profiles, &peak)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("department", "S6"))

	runAFSM(f, i)

	if got := atomic.LoadInt32(&peak); got < 2 {
		t.Fatalf("expected concurrent milpac fetches (peak >= 2), got peak %d — fetches ran serially", got)
	}
}

func TestRunAFSM_EmptyRoster(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	serveRosterAndProfiles(t, roster, http.StatusOK, nil)

	// First Respond is the placeholder (succeeds).
	// HandleError then tries Respond again (for the empty-roster error); we
	// simulate the "already acknowledged" SDK response so HandleError's Edit
	// fallback exercises.
	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction(stringOption("department", "S6"))

	runAFSM(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls (placeholder + HandleError Respond + Edit fallback), got %d: %+v", len(calls), calls)
	}
	if calls[2].Method != "Edit" {
		t.Fatalf("calls[2]: expected Edit (HandleError fallback), got %q", calls[2].Method)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "came back empty") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("expected empty-roster message in Edit fallback, got %q", got)
	}
}

func TestRunAFSM_RosterFetch500(t *testing.T) {
	serveRosterAndProfiles(t, utils.LiteRosterResponse{}, http.StatusInternalServerError, nil)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction(stringOption("department", "S6"))

	runAFSM(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(calls), calls)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "Failed to fetch Members") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("expected 'Failed to fetch Members' in Edit fallback, got %q", got)
	}
}

// TestRunAFSM_FirstRespondFails_BailsBeforeAPI covers the early-bail branch when
// the placeholder InteractionRespond fails: runAFSM must call HandleError once
// and return before fetching the roster (tripwire server fails the test if hit).
func TestRunAFSM_FirstRespondFails_BailsBeforeAPI(t *testing.T) {
	tripwireAPIServer(t)

	f := &fakeResponder{RespondErrs: []error{errFirstRespond}}
	i := fakeAppCommandInteraction(stringOption("department", "S6"))

	runAFSM(f, i)

	assertPlaceholderFailedBailout(t, f.Calls())
}
