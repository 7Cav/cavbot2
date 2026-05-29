package commands

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// loaRefDate pins "now" for /loa tests. Active = StartDate ≤ now ≤ EndDate;
// Upcoming = StartDate > now. Cache entries below straddle this date.
var loaRefDate = time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)

// fakeLOAView is a deterministic loaCacheView for /loa integration tests.
// entries are keyed by lowercased username (matches the production cache's
// case-folding). healthy/lastRefresh drive the IsHealthy staleness guard.
type fakeLOAView struct {
	entries     map[string]utils.LOAEntry
	healthy     bool
	lastRefresh time.Time
}

func (f *fakeLOAView) GetEntry(username string) (utils.LOAEntry, bool) {
	e, ok := f.entries[strings.ToLower(username)]
	return e, ok
}

func (f *fakeLOAView) IsHealthy(_ time.Duration) (bool, time.Time) {
	return f.healthy, f.lastRefresh
}

// healthyView builds a healthy fakeLOAView from the given entries (keyed by
// lowercased username), with lastRefresh pinned at loaRefDate.
func healthyView(entries map[string]utils.LOAEntry) *fakeLOAView {
	return &fakeLOAView{entries: entries, healthy: true, lastRefresh: loaRefDate}
}

// loaMember builds a roster-shape LiteProfileResponse for /loa tests. The
// uniformUrl matches the /<n>/<milpacID>.jpg regex the handler parses to
// build the milpac profile link.
func loaMember(username, milpacID string) utils.LiteProfileResponse {
	return utils.LiteProfileResponse{
		User:       utils.User{Username: username},
		Rank:       utils.Rank{RankFull: "Specialist"},
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/" + milpacID + ".jpg",
	}
}

func TestRunLoa_RosterFetch500SurfacesError(t *testing.T) {
	serveAwolRoster(t, utils.LiteRosterResponse{}, http.StatusInternalServerError)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	cache := healthyView(map[string]utils.LOAEntry{})
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runLoa(f, cache, loaRefDate, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %+v", len(calls), calls)
	}
	got := "<nil>"
	if calls[2].Edit.Content != nil {
		got = *calls[2].Edit.Content
	}
	if !strings.Contains(got, "Failed to fetch roster") {
		t.Fatalf("expected 'Failed to fetch roster' in Edit fallback, got %q", got)
	}
}

func TestRunLoa_StaleCacheShortCircuitsWithUnavailable(t *testing.T) {
	// An empty roster is served as a tripwire: if the staleness guard failed to
	// short-circuit, the handler would reach the empty-roster path (3 calls,
	// format-hint content) instead of the 2-call unavailable edit below.
	serveAwolRoster(t, utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}, http.StatusOK)

	f := &fakeResponder{}
	cache := &fakeLOAView{
		entries:     map[string]utils.LOAEntry{},
		healthy:     false,
		lastRefresh: loaRefDate.Add(-45 * time.Minute),
	}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runLoa(f, cache, loaRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder + unavailable Edit), got %d: %+v", len(calls), calls)
	}
	got := "<nil>"
	if calls[1].Edit.Content != nil {
		got = *calls[1].Edit.Content
	}
	if !strings.Contains(got, "LOA cache unavailable") || !strings.Contains(got, "45 minutes ago") {
		t.Fatalf("expected unavailable message with staleness; got %q", got)
	}
}

func TestRunLoa_NoMatchingLOAsReportsNone(t *testing.T) {
	// Roster has a member, but the cache holds no entry for them → neither
	// active nor upcoming, so the handler edits with the "no LOAs" message.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.Present", "100"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyView(map[string]utils.LOAEntry{})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runLoa(f, cache, loaRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder + Edit), got %d: %+v", len(calls), calls)
	}
	edit := calls[1].Edit
	if edit.Embeds != nil && len(*edit.Embeds) > 0 {
		t.Fatalf("expected no embeds for no-LOA result, got %d", len(*edit.Embeds))
	}
	got := "<nil>"
	if edit.Content != nil {
		got = *edit.Content
	}
	if !strings.Contains(got, "No active or upcoming LOAs found") || !strings.Contains(got, "1-7") {
		t.Fatalf("expected no-LOA message mentioning position; got %q", got)
	}
}

func TestRunLoa_EmptyRosterSurfacesFormatHint(t *testing.T) {
	serveAwolRoster(t, utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}, http.StatusOK)

	// Placeholder Respond succeeds; HandleError's Respond simulates "already
	// acknowledged" so its Edit fallback fires with the empty-roster hint.
	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	cache := healthyView(map[string]utils.LOAEntry{})
	i := fakeAppCommandInteraction(stringOption("position", "Q/Z/9-9"))

	runLoa(f, cache, loaRefDate, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls (placeholder + HandleError Respond + Edit fallback), got %d: %+v", len(calls), calls)
	}
	want := emptyRosterSearchMessage("Q/Z/9-9")
	got := "<nil>"
	if calls[2].Edit.Content != nil {
		got = *calls[2].Edit.Content
	}
	if got != want {
		t.Fatalf("Edit fallback content mismatch.\nwant: %q\ngot:  %q", want, got)
	}
}

func TestRunLoa_BothSectionsRenderActiveBeforeUpcoming(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.Active", "100"),
			"200": loaMember("Trooper.Soon", "200"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyView(map[string]utils.LOAEntry{
		"trooper.active": {
			Username:  "Trooper.Active",
			StartDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			EndDate:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		},
		"trooper.soon": {
			Username:  "Trooper.Soon",
			StartDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			EndDate:   time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		},
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runLoa(f, cache, loaRefDate, i)

	edit := f.Calls()[1].Edit
	desc := (*edit.Embeds)[0].Description
	activeIdx := strings.Index(desc, "**Active LOAs**")
	upcomingIdx := strings.Index(desc, "**Upcoming LOAs**")
	if activeIdx == -1 || upcomingIdx == -1 {
		t.Fatalf("expected both sections.\nGot:\n%s", desc)
	}
	if activeIdx > upcomingIdx {
		t.Fatalf("Active section must precede Upcoming.\nGot:\n%s", desc)
	}
	footer := (*edit.Embeds)[0].Footer
	if footer == nil || !strings.Contains(footer.Text, "Active: 1 | Upcoming: 1") {
		got := "<nil>"
		if footer != nil {
			got = footer.Text
		}
		t.Fatalf("footer should report Active: 1 | Upcoming: 1; got %q", got)
	}
}

func TestRunLoa_UpcomingOnlyRendersUpcomingSection(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"200": loaMember("Trooper.Soon", "200"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyView(map[string]utils.LOAEntry{
		"trooper.soon": {
			Username:  "Trooper.Soon",
			StartDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			EndDate:   time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		},
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runLoa(f, cache, loaRefDate, i)

	edit := f.Calls()[1].Edit
	desc := (*edit.Embeds)[0].Description
	if !strings.Contains(desc, "**Upcoming LOAs**") {
		t.Fatalf("expected Upcoming LOAs section.\nGot:\n%s", desc)
	}
	if strings.Contains(desc, "**Active LOAs**") {
		t.Fatalf("did not expect Active section for upcoming-only.\nGot:\n%s", desc)
	}
	footer := (*edit.Embeds)[0].Footer
	if footer == nil || !strings.Contains(footer.Text, "Active: 0 | Upcoming: 1") {
		got := "<nil>"
		if footer != nil {
			got = footer.Text
		}
		t.Fatalf("footer should report Active: 0 | Upcoming: 1; got %q", got)
	}
}

func TestRunLoa_ActiveLOARendersActiveSection(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.Active", "100"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyView(map[string]utils.LOAEntry{
		"trooper.active": {
			Username:  "Trooper.Active",
			StartDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			EndDate:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			ThreadID:  4242,
		},
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runLoa(f, cache, loaRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder Respond + Edit), got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "Respond" {
		t.Fatalf("calls[0]: expected Respond, got %q", calls[0].Method)
	}
	edit := calls[1].Edit
	if edit.Embeds == nil || len(*edit.Embeds) == 0 {
		t.Fatalf("expected embeds on Edit; got %+v", edit)
	}
	desc := (*edit.Embeds)[0].Description
	if !strings.Contains(desc, "**Active LOAs**") {
		t.Fatalf("expected Active LOAs section.\nGot:\n%s", desc)
	}
	if strings.Contains(desc, "**Upcoming LOAs**") {
		t.Fatalf("did not expect Upcoming section for active-only.\nGot:\n%s", desc)
	}
	if !strings.Contains(desc, "Trooper.Active") {
		t.Fatalf("expected member name in description.\nGot:\n%s", desc)
	}
	if !strings.Contains(desc, "[[Thread]](https://7cav.us/threads/4242/)") {
		t.Fatalf("expected thread link for non-zero ThreadID.\nGot:\n%s", desc)
	}
	footer := (*edit.Embeds)[0].Footer
	if footer == nil || !strings.Contains(footer.Text, "Active: 1 | Upcoming: 0") {
		got := "<nil>"
		if footer != nil {
			got = footer.Text
		}
		t.Fatalf("footer should report Active: 1 | Upcoming: 0; got %q", got)
	}
}

func TestLOAUnavailableMessage(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		lastRefresh time.Time
		wantSubstr  string
	}{
		{
			name:        "never refreshed reports never",
			lastRefresh: time.Time{},
			wantSubstr:  "never successfully refreshed",
		},
		{
			name:        "31 minutes ago reports 31 minutes",
			lastRefresh: now.Add(-31 * time.Minute),
			wantSubstr:  "last refresh: 31 minutes ago",
		},
		{
			name:        "90 minutes ago reports 90 minutes",
			lastRefresh: now.Add(-90 * time.Minute),
			wantSubstr:  "last refresh: 90 minutes ago",
		},
		{
			name:        "exactly 30 minutes ago reports 30 minutes",
			lastRefresh: now.Add(-30 * time.Minute),
			wantSubstr:  "last refresh: 30 minutes ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loaUnavailableMessage(tt.lastRefresh, now)
			if !strings.Contains(got, tt.wantSubstr) {
				t.Fatalf("loaUnavailableMessage = %q, want substring %q", got, tt.wantSubstr)
			}
			if !strings.HasPrefix(got, "❌ LOA cache unavailable") {
				t.Fatalf("loaUnavailableMessage = %q, want prefix %q",
					got, "❌ LOA cache unavailable")
			}
			if !strings.HasSuffix(strings.TrimSpace(got), "Try again shortly.") {
				t.Fatalf("loaUnavailableMessage = %q, want suffix %q",
					got, "Try again shortly.")
			}
		})
	}
}
