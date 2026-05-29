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
)

// s6RefDate is the pinned "now" for all S6-IT unit tests; chosen so the
// 6-month cutoff lands at 2025-11-15.
var s6RefDate = mustParseS6Date("2026-05-15")

func mustParseS6Date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic("mustParseS6Date: " + err.Error())
	}
	return t
}

// serveS6Profile is the per-profile httptest stub for evaluateS6Member unit
// tests. Same shape as serveProfile in afsm_test.go, renamed to avoid the
// same-package symbol collision once both PRs merge.
func serveS6Profile(t *testing.T, profile utils.ProfileResponse) {
	t.Helper()
	body, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("serveS6Profile marshal: %v", err)
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

// serveS6RosterAndProfiles is the multiplexed httptest server for runS6ITCheck
// integration tests.
func serveS6RosterAndProfiles(
	t *testing.T,
	roster utils.LiteRosterResponse,
	rosterStatus int,
	profilesByUsername map[string]utils.ProfileResponse,
) {
	t.Helper()
	rosterBody, err := json.Marshal(roster)
	if err != nil {
		t.Fatalf("serveS6RosterAndProfiles marshal roster: %v", err)
	}
	encodedProfiles := make(map[string][]byte, len(profilesByUsername))
	for name, p := range profilesByUsername {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("serveS6RosterAndProfiles marshal profile %q: %v", name, err)
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

func TestNormalizePositionWords(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plural to singular", "S6 IT Specialists", "s6 it specialist"},
		{"already singular", "S6 IT Specialist", "s6 it specialist"},
		{"extra whitespace collapses", " IT  SPECIALIST ", "it specialist"},
		{"single word", "IT", "it"},
		{"empty input", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizePositionWords(tc.in)
			if got != tc.want {
				t.Fatalf("normalizePositionWords(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEvaluateS6Member_EligiblePaths(t *testing.T) {
	ctx := context.Background()

	// Case 1: Eligible — primary is IT+S6, assignment ≥6mo ago.
	t.Run("eligible via primary IT+S6", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.A"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/144.jpg",
		}
		profile := utils.ProfileResponse{
			User:       member.User,
			UniformUrl: member.UniformUrl,
			Primary:    utils.Position{PositionTitle: "S6 Operations Staff IT"},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-06-01", RecordDetails: "Assigned S6 Operations Staff IT"},
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 row, got %d: %+v", len(got), got)
		}
		if got[0].Position != "S6 Operations Staff IT" {
			t.Fatalf("Position = %q, want %q", got[0].Position, "S6 Operations Staff IT")
		}
		if !got[0].PositionDate.Equal(mustParseS6Date("2024-06-01")) {
			t.Fatalf("PositionDate = %v, want 2024-06-01", got[0].PositionDate)
		}
		if got[0].MilpacUrl != "https://7cav.us/rosters/profile/144" {
			t.Fatalf("MilpacUrl = %q, want .../144", got[0].MilpacUrl)
		}
	})

	// Case 2: Eligible — secondary is IT+S6.
	t.Run("eligible via secondary IT+S6", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.B"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/200.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary: utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{
				{PositionTitle: "S6 Forums Staff IT"},
			},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-03-01", RecordDetails: "Assigned S6 Forums Staff IT"},
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 row, got %d: %+v", len(got), got)
		}
	})

	// Case 3: Both primary AND secondary match → 2 rows.
	t.Run("primary and secondary both match", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.C"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/300.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary: utils.Position{PositionTitle: "S6 Game Staff IT"},
			Secondary: []utils.Position{
				{PositionTitle: "S6 Development Staff IT"},
			},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-06-01", RecordDetails: "Assigned S6 Game Staff IT"},
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-08-01", RecordDetails: "Assigned S6 Development Staff IT"},
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 rows, got %d: %+v", len(got), got)
		}
		positions := []string{got[0].Position, got[1].Position}
		want := map[string]bool{"S6 Game Staff IT": true, "S6 Development Staff IT": true}
		for _, p := range positions {
			if !want[p] {
				t.Fatalf("unexpected Position %q (positions=%v)", p, positions)
			}
		}
	})

	// Case 5: Multiple assignments to the same position → uses most recent.
	t.Run("multiple assignments uses most recent", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.E"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/500.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 Game Staff IT"}},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-01-01", RecordDetails: "Assigned S6 Game Staff IT"},
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2025-04-01", RecordDetails: "Assigned S6 Game Staff IT"},
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 row, got %d", len(got))
		}
		if !got[0].PositionDate.Equal(mustParseS6Date("2025-04-01")) {
			t.Fatalf("PositionDate = %v, want 2025-04-01 (most recent)", got[0].PositionDate)
		}
	})
}

func TestEvaluateS6Member_NonMatchAndWarning(t *testing.T) {
	ctx := context.Background()

	// Case 4: Position <6mo old → no row.
	t.Run("position too recent", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.F"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/600.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 Forums Staff IT"}},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2026-02-01", RecordDetails: "Assigned S6 Forums Staff IT"},
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected 0 rows (only 3mo in position), got %d: %+v", len(got), got)
		}
	})

	// Case 6: No assignment record matches the position string → warning row.
	t.Run("no matching assignment record yields warning row", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.G"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/700.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
			Records: []utils.Record{
				// Unrelated assignment — does NOT mention "S6 Operations Staff IT".
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-06-01", RecordDetails: "Assigned RRD Hell Let Loose Recruiter"},
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 warning row, got %d: %+v", len(got), got)
		}
		if got[0].TimeSince != "⚠️ No matching assignment record found" {
			t.Fatalf("TimeSince = %q, want warning string", got[0].TimeSince)
		}
		if !got[0].PositionDate.IsZero() {
			t.Fatalf("PositionDate should be zero, got %v", got[0].PositionDate)
		}
	})

	// Case 7: IT-but-not-S6 positions should not match.
	t.Run("IT without S6 does not match", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.H"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/800.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary: utils.Position{PositionTitle: "S2 Investigator IT"},
			Secondary: []utils.Position{
				{PositionTitle: "RRD Processing Clerk IT"},
				{PositionTitle: "MP Administrator IT"},
			},
			// Records irrelevant — we should never reach them.
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected 0 rows (no S6), got %d: %+v", len(got), got)
		}
	})

	// Case 8: S6-but-not-IT positions should not match.
	t.Run("S6 without IT does not match", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.I"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/900.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary: utils.Position{PositionTitle: "S6 Forums Staff"}, // no IT
			Secondary: []utils.Position{
				{PositionTitle: "S6 Senior Game Staff"}, // no IT
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected 0 rows (no IT), got %d: %+v", len(got), got)
		}
	})

	// Case 11: Plural normalization — record details use singular even though
	// position title is plural.
	t.Run("plural position matches singular record details", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.K"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/1100.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 IT Specialists"}}, // plural
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-06-01", RecordDetails: "Assigned S6 IT Specialist"}, // singular
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 row (normalization should match), got %d: %+v", len(got), got)
		}
	})

	// Case 12: Case-insensitive matching.
	t.Run("case-insensitive matching", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.L"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/1200.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 IT SPECIALIST"}}, // upper-case
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-06-01", RecordDetails: "assigned s6 it specialist"}, // lower-case
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 row (case-insensitive match), got %d: %+v", len(got), got)
		}
	})
}

func TestEvaluateS6Member_ErrorPaths(t *testing.T) {
	ctx := context.Background()

	// Case 9: malformed RecordDate on a matching assignment → error.
	t.Run("malformed RecordDate", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.M"},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/1300.jpg",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 Forums Staff IT"}},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "bad", RecordDetails: "Assigned S6 Forums Staff IT"},
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if got != nil {
			t.Fatalf("expected nil result with error, got %+v", got)
		}
		if err == nil || !strings.Contains(err.Error(), "failed to parse record date") {
			t.Fatalf("expected error containing 'failed to parse record date', got %v", err)
		}
	})

	// Case 10: malformed UniformUrl on matching member → error.
	t.Run("malformed UniformUrl", func(t *testing.T) {
		member := utils.LiteProfileResponse{
			User:       utils.User{Username: "Test.N"},
			UniformUrl: "garbage",
		}
		profile := utils.ProfileResponse{
			User: member.User, UniformUrl: member.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 Game Staff IT"}},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-06-01", RecordDetails: "Assigned S6 Game Staff IT"},
			},
		}
		serveS6Profile(t, profile)

		got, err := evaluateS6Member(ctx, member, s6RefDate)
		if got != nil {
			t.Fatalf("expected nil result with error, got %+v", got)
		}
		if err == nil || !strings.Contains(err.Error(), "uniform URL") {
			t.Fatalf("expected error containing 'uniform URL', got %v", err)
		}
	})
}

func TestRunS6ITCheck_HappyPathWithSkipped(t *testing.T) {
	// 3-member roster:
	//   - A is eligible (S6 + IT secondary, >6mo)
	//   - B has no S6+IT match (still fetched by S6ITCheck since it fetches ALL)
	//   - C has a malformed RecordDate on a matching position → skipped
	memberA := utils.LiteProfileResponse{
		User: utils.User{Username: "Test.A"}, UniformUrl: "https://7cav.us/data/roster_uniforms/0/144.jpg",
	}
	memberB := utils.LiteProfileResponse{
		User: utils.User{Username: "Test.B"}, UniformUrl: "https://7cav.us/data/roster_uniforms/0/200.jpg",
	}
	memberC := utils.LiteProfileResponse{
		User: utils.User{Username: "Test.C"}, UniformUrl: "https://7cav.us/data/roster_uniforms/0/300.jpg",
	}

	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"144": memberA, "200": memberB, "300": memberC,
		},
	}
	profiles := map[string]utils.ProfileResponse{
		"Test.A": {
			User: memberA.User, UniformUrl: memberA.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 Operations Staff IT"}},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "2024-01-01", RecordDetails: "Assigned S6 Operations Staff IT"},
			},
		},
		"Test.B": {
			User: memberB.User, UniformUrl: memberB.UniformUrl,
			Primary:   utils.Position{PositionTitle: "S6 Forums Staff"}, // S6 but no IT
			Secondary: []utils.Position{{PositionTitle: "S1 Analytics Senior"}},
		},
		"Test.C": {
			User: memberC.User, UniformUrl: memberC.UniformUrl,
			Primary:   utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
			Secondary: []utils.Position{{PositionTitle: "S6 Game Staff IT"}},
			Records: []utils.Record{
				{RecordType: "RECORD_TYPE_ASSIGNMENT", RecordDate: "bad-date", RecordDetails: "Assigned S6 Game Staff IT"},
			},
		},
	}
	serveS6RosterAndProfiles(t, roster, http.StatusOK, profiles)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction()

	runS6ITCheck(f, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder + final Edit), got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "Respond" || calls[0].Response.Data.Content != "Fetching S6 IT Check data..." {
		t.Fatalf("calls[0]: expected placeholder Respond, got %+v", calls[0])
	}
	if calls[1].Method != "Edit" || calls[1].Edit.Content == nil {
		t.Fatalf("calls[1]: expected Edit with non-nil Content, got %+v", calls[1])
	}
	got := *calls[1].Edit.Content
	for _, want := range []string{
		"The following S6 members are eligible for Full Status:",
		"[Test.A](<https://7cav.us/rosters/profile/144>)",
		"(S6 Operations Staff IT)",
		"1 member skipped due to errors (reported)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Edit content missing %q.\nGot:\n%s", want, got)
		}
	}
	// /s6-it-check, unlike /afsm, has no disclaimer.
	if strings.Contains(got, "This command cannot be made completely accurate") {
		t.Fatalf("Edit content should NOT contain AFSM disclaimer; got: %s", got)
	}
}

// serveS6RosterTrackingProfileConcurrency mirrors serveS6RosterAndProfiles but
// instruments the per-profile endpoint to record peak concurrent fetches. A
// serial loop peaks at 1; a fanned-out implementation peaks above 1.
func serveS6RosterTrackingProfileConcurrency(
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

func TestRunS6ITCheck_FetchesConcurrently(t *testing.T) {
	// evaluateS6Member fetches the milpac for every roster entry up front, so
	// four members force four fetches. A serial loop peaks at one in-flight
	// fetch; the fanned-out implementation peaks above one.
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	profiles := map[string]utils.ProfileResponse{}
	for _, id := range []string{"1", "2", "3", "4"} {
		name := "Member." + id
		m := utils.LiteProfileResponse{
			User:       utils.User{Username: name},
			UniformUrl: "https://7cav.us/data/roster_uniforms/0/" + id + ".jpg",
		}
		roster.LiteProfiles[id] = m
		profiles[name] = utils.ProfileResponse{
			User: m.User, UniformUrl: m.UniformUrl,
			Primary: utils.Position{PositionTitle: "Trooper 3/2/A/1-7"},
		}
	}

	var peak int32
	serveS6RosterTrackingProfileConcurrency(t, roster, profiles, &peak)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction()

	runS6ITCheck(f, i)

	if got := atomic.LoadInt32(&peak); got < 2 {
		t.Fatalf("expected concurrent milpac fetches (peak >= 2), got peak %d — fetches ran serially", got)
	}
}

func TestRunS6ITCheck_EmptyRoster(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	serveS6RosterAndProfiles(t, roster, http.StatusOK, nil)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction()

	runS6ITCheck(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls (placeholder + HandleError Respond + Edit fallback), got %d: %+v", len(calls), calls)
	}
	if calls[2].Method != "Edit" {
		t.Fatalf("calls[2]: expected Edit (HandleError fallback), got %q", calls[2].Method)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "S6 roster came back empty") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("expected 'S6 roster came back empty' in Edit fallback, got %q", got)
	}
}

func TestRunS6ITCheck_RosterFetch500(t *testing.T) {
	serveS6RosterAndProfiles(t, utils.LiteRosterResponse{}, http.StatusInternalServerError, nil)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	i := fakeAppCommandInteraction()

	runS6ITCheck(f, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(calls), calls)
	}
	if calls[2].Edit.Content == nil || !strings.Contains(*calls[2].Edit.Content, "Failed to fetch S6 Members") {
		got := "<nil>"
		if calls[2].Edit.Content != nil {
			got = *calls[2].Edit.Content
		}
		t.Fatalf("expected 'Failed to fetch S6 Members' in Edit fallback, got %q", got)
	}
}
