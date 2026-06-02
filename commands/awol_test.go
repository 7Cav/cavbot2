package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// awolRefDate pins "now" for /awol tests. With awolThresholdDays=8 the AWOL
// cutoff lands at 2026-05-07; LastForumPostDate values older than that are AWOL.
var awolRefDate = mustParseAwolDate("2026-05-15 12:00:00")

func mustParseAwolDate(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		panic("mustParseAwolDate: " + err.Error())
	}
	return t
}

// serveAwolRoster spins up an httptest server that returns the supplied roster
// for /milpacs/position/search/* requests; rosterStatus lets a test simulate
// upstream errors (e.g. 500). Other paths 404.
func serveAwolRoster(t *testing.T, roster utils.LiteRosterResponse, rosterStatus int) {
	t.Helper()
	body, err := json.Marshal(roster)
	if err != nil {
		t.Fatalf("serveAwolRoster marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/milpacs/position/search/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if rosterStatus != 0 && rosterStatus != http.StatusOK {
			w.WriteHeader(rosterStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

// fakeLOACache is a deterministic loaCacheReader for /awol integration tests.
// entries are keyed by lowercased username (matches the production cache's
// case-folding); IsOnLOA returns true iff an entry exists, decoupling the test
// from time.Now since the handler's "now" is already injected separately.
// healthy/lastRefresh drive the IsHealthy staleness guard; the zero value is
// unhealthy, so existing tests that want the original behavior should construct
// via healthyCache.
type fakeLOACache struct {
	entries     map[string]utils.LOAEntry
	healthy     bool
	lastRefresh time.Time
}

func (f *fakeLOACache) GetEntry(username string) (utils.LOAEntry, bool) {
	e, ok := f.entries[strings.ToLower(username)]
	return e, ok
}

func (f *fakeLOACache) IsOnLOA(username string) bool {
	_, ok := f.entries[strings.ToLower(username)]
	return ok
}

func (f *fakeLOACache) IsHealthy(_ time.Duration) (bool, time.Time) {
	return f.healthy, f.lastRefresh
}

// healthyCache builds a healthy fakeLOACache from the given entries (keyed by
// lowercased username), with lastRefresh pinned at awolRefDate.
func healthyCache(entries map[string]utils.LOAEntry) *fakeLOACache {
	return &fakeLOACache{entries: entries, healthy: true, lastRefresh: awolRefDate}
}

// dateAwareLOACache is a loaCacheReader whose IsOnLOA respects each entry's
// StartDate/EndDate window evaluated at a FIXED `at` instant — unlike fakeLOACache,
// whose IsOnLOA returns true for any existing entry regardless of dates. This lets
// an /awol test exercise the "entry exists but is not active today" path (upcoming
// or expired), proving such a member is excluded from the (N on LOA) tally and the
// [LOA] decoration on the healthy path. This fake substitutes its own fixed clock
// `at` for the wall clock that production's utils.LOACache.IsOnLOA reads internally
// (the real method takes no time argument). The two are fidelity-equivalent only
// because the test windows are weeks wide relative to the `at`-vs-wall-now skew.
type dateAwareLOACache struct {
	entries map[string]utils.LOAEntry
	at      time.Time
}

func (f *dateAwareLOACache) GetEntry(username string) (utils.LOAEntry, bool) {
	e, ok := f.entries[strings.ToLower(username)]
	return e, ok
}

func (f *dateAwareLOACache) IsOnLOA(username string) bool {
	e, ok := f.entries[strings.ToLower(username)]
	if !ok {
		return false
	}
	// Mirror utils.LOACache's inclusive active window evaluated at `at`.
	return !f.at.Before(e.StartDate) && !f.at.After(e.EndDate)
}

func (f *dateAwareLOACache) IsHealthy(_ time.Duration) (bool, time.Time) {
	return true, f.at // always healthy: this fake exercises the date-window path
}

func boolOption(name string, value bool) *discordgo.ApplicationCommandInteractionDataOption {
	return &discordgo.ApplicationCommandInteractionDataOption{
		Name:  name,
		Type:  discordgo.ApplicationCommandOptionBoolean,
		Value: value,
	}
}

// awolMember builds a roster-shape LiteProfileResponse for /awol tests. The
// uniformUrl pattern matches the /<n>/<milpacID>.jpg regex the handler parses.
func awolMember(username, milpacID, lastForumPost string) utils.LiteProfileResponse {
	return utils.LiteProfileResponse{
		User:              utils.User{Username: username},
		Rank:              utils.Rank{RankFull: "Specialist"},
		UniformUrl:        "https://7cav.us/data/roster_uniforms/0/" + milpacID + ".jpg",
		LastForumPostDate: lastForumPost,
	}
}

func TestRunAwol_SmallResultRendersEmbedChunks(t *testing.T) {
	// Two AWOL members (last post >8d before awolRefDate); no LOA; no force_file.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": awolMember("Trooper.B", "200", "2026-04-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	cache := healthyCache(nil)
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder Respond + Edit), got %d: %+v", len(calls), calls)
	}
	if calls[0].Method != "Respond" {
		t.Fatalf("calls[0]: expected Respond, got %q", calls[0].Method)
	}
	if !strings.Contains(calls[0].Response.Data.Content, "1-7") {
		t.Fatalf("placeholder should mention position; got %q", calls[0].Response.Data.Content)
	}
	if calls[1].Method != "Edit" {
		t.Fatalf("calls[1]: expected Edit, got %q", calls[1].Method)
	}
	edit := calls[1].Edit
	if edit.Embeds == nil || len(*edit.Embeds) == 0 {
		t.Fatalf("expected embeds set on Edit; got Embeds=%v Files=%v Content=%v", edit.Embeds, edit.Files, edit.Content)
	}
	if len(edit.Files) > 0 {
		t.Fatalf("expected no files for small-result path; got %d files", len(edit.Files))
	}
	desc := (*edit.Embeds)[0].Description
	for _, want := range []string{"Trooper.A", "Trooper.B"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("embed description missing %q.\nGot:\n%s", want, desc)
		}
	}
}

func TestRunAwol_LargeResultFallsBackToFile(t *testing.T) {
	// Inflate per-user line length with long padded usernames so a single user
	// line saturates a chunk (~2.5KB each, threshold 4096). 11 such users yield
	// 11 chunks, exceeding maxEmbedsPerMsg=10 and forcing the file fallback.
	const padded = 2500
	pad := strings.Repeat("x", padded)
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{},
	}
	for n := range 11 {
		id := fmt.Sprintf("%d", 1000+n)
		name := fmt.Sprintf("U%d_%s", n, pad)
		roster.LiteProfiles[id] = awolMember(name, id, "2026-03-01 12:00:00")
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	cache := healthyCache(nil)
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %+v", len(calls), calls)
	}
	edit := calls[1].Edit
	if edit == nil {
		t.Fatalf("calls[1].Edit is nil")
	}
	if len(edit.Files) != 1 {
		t.Fatalf("expected exactly 1 file attachment, got %d", len(edit.Files))
	}
	if edit.Embeds != nil && len(*edit.Embeds) > 0 {
		t.Fatalf("expected no embeds on file-fallback path, got %d", len(*edit.Embeds))
	}
	if edit.Content == nil || !strings.Contains(*edit.Content, "Large AWOL report") {
		got := "<nil>"
		if edit.Content != nil {
			got = *edit.Content
		}
		t.Fatalf("expected 'Large AWOL report' prefix on file path, got %q", got)
	}
	if edit.Files[0].Name != "awol_report_1-7.txt" {
		t.Fatalf("file name = %q, want awol_report_1-7.txt", edit.Files[0].Name)
	}
}

func TestRunAwol_ForceFileOutputWithSmallResult(t *testing.T) {
	// Two AWOL members — small enough to fit in embeds — but force_file_output
	// flips the rendering to the file path with the "Force File Set True" prefix.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": awolMember("Trooper.B", "200", "2026-04-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	cache := healthyCache(nil)
	i := fakeAppCommandInteraction(
		stringOption("position", "1-7"),
		boolOption("force_file_output", true),
	)

	runAwol(f, cache, awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %+v", len(calls), calls)
	}
	edit := calls[1].Edit
	if len(edit.Files) != 1 {
		t.Fatalf("expected exactly 1 file attachment, got %d", len(edit.Files))
	}
	if edit.Content == nil || !strings.Contains(*edit.Content, "Force File Set True") {
		got := "<nil>"
		if edit.Content != nil {
			got = *edit.Content
		}
		t.Fatalf("expected 'Force File Set True' prefix, got %q", got)
	}
}

func TestRunAwol_EmptyRosterSurfacesFormatHint(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	serveAwolRoster(t, roster, http.StatusOK)

	// First Respond is the placeholder (succeeds); HandleError's Respond
	// simulates "already acknowledged" so its Edit fallback fires with the
	// empty-roster format-hint message.
	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	cache := healthyCache(nil)
	i := fakeAppCommandInteraction(stringOption("position", "Q/Z/9-9"))

	runAwol(f, cache, awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls (placeholder + HandleError Respond + Edit fallback), got %d: %+v", len(calls), calls)
	}
	if calls[2].Method != "Edit" {
		t.Fatalf("calls[2]: expected Edit, got %q", calls[2].Method)
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

func TestRunAwol_RosterFetch500SurfacesError(t *testing.T) {
	serveAwolRoster(t, utils.LiteRosterResponse{}, http.StatusInternalServerError)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	cache := healthyCache(nil)
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

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

func TestRunAwol_OnLOAAnnotationRenders(t *testing.T) {
	// Two AWOL members; only Trooper.A has a matching LOA cache entry, with a
	// non-zero ThreadID — so the row must render with **[[LOA]](...)** linked
	// to that thread. Trooper.B has no entry → no LOA tag.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": awolMember("Trooper.B", "200", "2026-04-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyCache(map[string]utils.LOAEntry{
		"trooper.a": {
			Username:  "Trooper.A",
			StartDate: mustParseAwolDate("2026-04-01 00:00:00"),
			EndDate:   mustParseAwolDate("2026-06-01 00:00:00"),
			ThreadID:  4242,
		},
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %+v", len(calls), calls)
	}
	edit := calls[1].Edit
	if edit.Embeds == nil || len(*edit.Embeds) == 0 {
		t.Fatalf("expected embeds; got Embeds=%v", edit.Embeds)
	}
	desc := (*edit.Embeds)[0].Description
	wantTag := "**[[LOA]](https://7cav.us/threads/4242/)**"
	if !strings.Contains(desc, wantTag) {
		t.Fatalf("expected LOA tag %q in description.\nGot:\n%s", wantTag, desc)
	}
	// Footer must reflect 1 of 2 on LOA.
	footer := (*edit.Embeds)[0].Footer
	if footer == nil || !strings.Contains(footer.Text, "Total AWOL: 2 (1 on LOA)") {
		got := "<nil>"
		if footer != nil {
			got = footer.Text
		}
		t.Fatalf("footer should report 1 on LOA; got %q", got)
	}
}

func TestRunAwol_InactiveLOAEntryNotCountedOrTagged(t *testing.T) {
	// Three AWOL members on the healthy path, each with a cache entry, but only
	// Trooper.B's window covers awolRefDate (2026-05-15):
	//   - Trooper.A: UPCOMING (starts after now) → not active → no tag, not counted.
	//   - Trooper.B: ACTIVE (window straddles now) → tagged + counted.
	//   - Trooper.C: EXPIRED (ended before now) → not active → no tag, not counted.
	// Asserts the (N on LOA) tally is exactly 1 and only the active member is tagged.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": awolMember("Trooper.B", "200", "2026-03-15 12:00:00"),
			"300": awolMember("Trooper.C", "300", "2026-04-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := &dateAwareLOACache{
		at: awolRefDate,
		entries: map[string]utils.LOAEntry{
			"trooper.a": { // upcoming: starts AFTER awolRefDate
				Username:  "Trooper.A",
				StartDate: mustParseAwolDate("2026-06-01 00:00:00"),
				EndDate:   mustParseAwolDate("2026-06-30 00:00:00"),
				ThreadID:  1111,
			},
			"trooper.b": { // active: window straddles awolRefDate
				Username:  "Trooper.B",
				StartDate: mustParseAwolDate("2026-05-01 00:00:00"),
				EndDate:   mustParseAwolDate("2026-05-31 00:00:00"),
				ThreadID:  2222,
			},
			"trooper.c": { // expired: ended BEFORE awolRefDate
				Username:  "Trooper.C",
				StartDate: mustParseAwolDate("2026-04-01 00:00:00"),
				EndDate:   mustParseAwolDate("2026-04-30 00:00:00"),
				ThreadID:  3333,
			},
		},
	}
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %+v", len(calls), calls)
	}
	edit := calls[1].Edit
	if edit.Embeds == nil || len(*edit.Embeds) == 0 {
		t.Fatalf("expected embeds; got Embeds=%v", edit.Embeds)
	}
	desc := (*edit.Embeds)[0].Description
	// All three members still listed (AWOL is independent of LOA state).
	for _, want := range []string{"Trooper.A", "Trooper.B", "Trooper.C"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("embed description missing %q.\nGot:\n%s", want, desc)
		}
	}
	// Only the ACTIVE member is decorated; the upcoming/expired threads must not appear.
	if !strings.Contains(desc, "https://7cav.us/threads/2222/") {
		t.Fatalf("active member Trooper.B should be LOA-tagged to thread 2222.\nGot:\n%s", desc)
	}
	for _, badThread := range []string{"threads/1111", "threads/3333"} {
		if strings.Contains(desc, badThread) {
			t.Fatalf("inactive (upcoming/expired) entry must NOT be LOA-tagged (%s leaked).\nGot:\n%s", badThread, desc)
		}
	}
	// Footer tally counts ONLY the currently-active LOA: exactly 1 of 3.
	footer := (*edit.Embeds)[0].Footer
	if footer == nil || !strings.Contains(footer.Text, "Total AWOL: 3 (1 on LOA)") {
		got := "<nil>"
		if footer != nil {
			got = footer.Text
		}
		t.Fatalf("footer should report exactly 1 on LOA (active only); got %q", got)
	}
}

// unhealthyCache builds an unhealthy fakeLOACache: entries may exist (and the
// roster member may even have a "real" LOA) but the health probe reports stale,
// so the handler must NOT trust IsOnLOA/GetEntry and must render "unknown".
func unhealthyCache(entries map[string]utils.LOAEntry, lastRefresh time.Time) *fakeLOACache {
	return &fakeLOACache{entries: entries, healthy: false, lastRefresh: lastRefresh}
}

func TestRunAwol_UnhealthyCacheRendersUnknownColumn(t *testing.T) {
	// Two AWOL members. Trooper.A even has a (stale) cache entry with a thread,
	// but because the cache is unhealthy the handler must treat On LOA as unknown
	// for EVERY row: no [LOA]/[[LOA]] decoration, no loaCount, plus a footer
	// warning line.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": awolMember("Trooper.B", "200", "2026-04-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := unhealthyCache(map[string]utils.LOAEntry{
		"trooper.a": {
			Username:  "Trooper.A",
			StartDate: mustParseAwolDate("2026-04-01 00:00:00"),
			EndDate:   mustParseAwolDate("2026-06-01 00:00:00"),
			ThreadID:  4242,
		},
	}, awolRefDate.Add(-45*time.Minute))
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder + Edit), got %d: %+v", len(calls), calls)
	}
	edit := calls[1].Edit
	if edit.Embeds == nil || len(*edit.Embeds) == 0 {
		t.Fatalf("expected embeds even on unhealthy path; got Embeds=%v", edit.Embeds)
	}
	desc := (*edit.Embeds)[0].Description
	// Members still listed.
	for _, want := range []string{"Trooper.A", "Trooper.B"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("embed description missing %q.\nGot:\n%s", want, desc)
		}
	}
	// No LOA decoration leaks through on the unhealthy path.
	if strings.Contains(desc, "[LOA]") || strings.Contains(desc, "threads/4242") {
		t.Fatalf("expected no LOA decoration on unhealthy path.\nGot:\n%s", desc)
	}
	// An "unknown" marker is present.
	if !strings.Contains(desc, "On LOA: unknown") {
		t.Fatalf("expected per-row 'On LOA: unknown' marker.\nGot:\n%s", desc)
	}
	footer := (*edit.Embeds)[0].Footer
	if footer == nil {
		t.Fatalf("expected footer on unhealthy path")
	}
	// loaCount aggregate must NOT report a concrete number.
	if strings.Contains(footer.Text, "on LOA)") {
		t.Fatalf("footer must not report a concrete loaCount on unhealthy path; got %q", footer.Text)
	}
	if !strings.Contains(footer.Text, "LOA cache unavailable") {
		t.Fatalf("footer should carry cache-unavailable warning; got %q", footer.Text)
	}
}

func TestRunAwol_UnhealthyCacheFileOutputCarriesWarning(t *testing.T) {
	// Force the file path; the unhealthy cache must surface the warning and an
	// unknown marker in the generated report rather than silent [LOA]/all-clear.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := unhealthyCache(map[string]utils.LOAEntry{
		"trooper.a": {Username: "Trooper.A", ThreadID: 4242},
	}, time.Time{})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(
		stringOption("position", "1-7"),
		boolOption("force_file_output", true),
	)

	runAwol(f, cache, awolRefDate, i)

	edit := f.Calls()[1].Edit
	if len(edit.Files) != 1 {
		t.Fatalf("expected 1 file attachment, got %d", len(edit.Files))
	}
	raw, err := io.ReadAll(edit.Files[0].Reader)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "[LOA]") {
		t.Fatalf("file must not carry [LOA] tag on unhealthy path.\nGot:\n%s", body)
	}
	if !strings.Contains(body, "LOA cache unavailable") {
		t.Fatalf("file should carry cache-unavailable warning.\nGot:\n%s", body)
	}
	if !strings.Contains(body, "unknown") {
		t.Fatalf("file should mark On LOA unknown.\nGot:\n%s", body)
	}
}
