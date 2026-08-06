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

// awolRefDate pins "now" for /awol tests at 2026-05-15 12:00 UTC. With the
// accountable-day model, a member is flagged when accountable dates exceed 7,
// so LastForumPostDate values more than 7 UTC dates before this (and not covered
// by LOA) are AWOL.
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
// case-folding) and hold the FULL retained window history per user — /awol
// applies its own injected `now` to that history for the accountable-day calc and
// active-window selection. healthy/lastRefresh drive the IsHealthy staleness
// guard; the zero value is unhealthy, so existing tests that want the original
// behavior should construct via healthyCache.
type fakeLOACache struct {
	entries     map[string][]utils.LOAEntry
	healthy     bool
	lastRefresh time.Time
}

func (f *fakeLOACache) GetEntries(username string) []utils.LOAEntry {
	return f.entries[strings.ToLower(username)]
}

func (f *fakeLOACache) IsHealthy(_ time.Duration) (bool, time.Time) {
	return f.healthy, f.lastRefresh
}

// healthyCache builds a healthy fakeLOACache from the given window history (keyed
// by lowercased username), with lastRefresh pinned at awolRefDate.
func healthyCache(entries map[string][]utils.LOAEntry) *fakeLOACache {
	return &fakeLOACache{entries: entries, healthy: true, lastRefresh: awolRefDate}
}

// oneWindow is a convenience for the common single-window-per-user case.
func oneWindow(e utils.LOAEntry) []utils.LOAEntry { return []utils.LOAEntry{e} }

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
	// Two AWOL members (last post well over 7 dates before awolRefDate); no LOA.
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
	embed := (*edit.Embeds)[0]
	desc := embed.Description
	for _, want := range []string{"Trooper.A", "Trooper.B"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("embed description missing %q.\nGot:\n%s", want, desc)
		}
	}
	// Title and footer match the agreed layout.
	if embed.Title != "AWOL — 1-7" {
		t.Fatalf("title = %q, want %q", embed.Title, "AWOL — 1-7")
	}
	if !strings.Contains(desc, "2 flagged") {
		t.Fatalf("summary line missing.\nGot:\n%s", desc)
	}
	if embed.Footer == nil || embed.Footer.Text != awolReportFooter {
		got := "<nil>"
		if embed.Footer != nil {
			got = embed.Footer.Text
		}
		t.Fatalf("footer = %q, want %q", got, awolReportFooter)
	}
	// Days-AWOL secondary context present.
	if !strings.Contains(desc, "d AWOL · last post") {
		t.Fatalf("rows should show 'Nd AWOL · last post Md'.\nGot:\n%s", desc)
	}
}

// TestRunAwol_NoLOARegressionAndSortOrder pins the common-case regression guard:
// with no LOA, days AWOL == raw overage (accountable == raw), and rows sort
// worst-first by days AWOL.
func TestRunAwol_NoLOARegressionAndSortOrder(t *testing.T) {
	// awolRefDate = 2026-05-15. UTC dates: last post 2026-05-01 → (05-01,05-15] =
	// 14 dates → 7 AWOL. last post 2026-03-01 → many → larger.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Closer.C", "100", "2026-05-01 12:00:00"),  // 14 dates → 7 AWOL
			"200": awolMember("Farther.F", "200", "2026-03-01 12:00:00"), // big AWOL
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, healthyCache(nil), awolRefDate, i)

	desc := (*f.Calls()[1].Edit.Embeds)[0].Description
	// Farther.F (worse) must appear before Closer.C.
	iF := strings.Index(desc, "Farther.F")
	iC := strings.Index(desc, "Closer.C")
	if iF == -1 || iC == -1 || iF > iC {
		t.Fatalf("expected Farther.F before Closer.C (worst-first).\nGot:\n%s", desc)
	}
	// Closer.C: 14 candidate dates, no LOA → 7d AWOL. Raw last post also 14d.
	if !strings.Contains(desc, "Closer.C](") || !strings.Contains(desc, "— 7d AWOL · last post 14d") {
		t.Fatalf("Closer.C should read '7d AWOL · last post 14d' (raw==accountable, no LOA).\nGot:\n%s", desc)
	}
}

// TestRunAwol_SeverityGlyphs pins the glyph tiers: 🔴 >14, 🟠 >7, 🟡 >0.
func TestRunAwol_SeverityGlyphs(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			// last post 2026-04-20 → (04-20,05-15] = 25 dates → 18 AWOL → 🔴 (>14)
			"100": awolMember("Red.R", "100", "2026-04-20 12:00:00"),
			// last post 2026-04-28 → (04-28,05-15] = 17 dates → 10 AWOL → 🟠 (>7)
			"200": awolMember("Orange.O", "200", "2026-04-28 12:00:00"),
			// last post 2026-05-06 → (05-06,05-15]=9 → 2 AWOL → 🟡 (>0)
			"300": awolMember("Yellow.Y", "300", "2026-05-06 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, healthyCache(nil), awolRefDate, i)

	desc := (*f.Calls()[1].Edit.Embeds)[0].Description
	checks := []struct{ glyph, name string }{
		{"🔴", "Red.R"},
		{"🟠", "Orange.O"},
		{"🟡", "Yellow.Y"},
	}
	for _, c := range checks {
		line := lineContaining(desc, c.name)
		if line == "" {
			t.Fatalf("missing row for %s.\nGot:\n%s", c.name, desc)
		}
		if !strings.HasPrefix(line, c.glyph) {
			t.Fatalf("row for %s should start with %s.\nGot line: %q", c.name, c.glyph, line)
		}
	}
}

// TestSeverityGlyph_TierBoundaries pins the EXACT strict-> tier edges that the
// inside-tier handler test (18/10/2) can't catch: 15→🔴, 14→🟠 (not 🔴), 8→🟠,
// 7→🟡 (not 🟠), 1→🟡. An active LOA always overrides to ⚪ regardless of days.
func TestSeverityGlyph_TierBoundaries(t *testing.T) {
	cases := []struct {
		days int
		want string
	}{
		{15, "🔴"},
		{14, "🟠"}, // exactly 14 is NOT >14
		{8, "🟠"},
		{7, "🟡"}, // exactly 7 is NOT >7
		{1, "🟡"},
	}
	for _, c := range cases {
		if got := (AwolUser{DaysAWOL: c.days}).severityGlyph(); got != c.want {
			t.Fatalf("severityGlyph(%d) = %q, want %q", c.days, got, c.want)
		}
	}
	// Active LOA overrides the tier.
	w := utils.LOAEntry{}
	if got := (AwolUser{DaysAWOL: 99, loaWindow: &w}).severityGlyph(); got != "⚪" {
		t.Fatalf("active-LOA severityGlyph = %q, want ⚪", got)
	}
}

// lineContaining returns the first line of s containing sub, or "".
func lineContaining(s, sub string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return ""
}

// TestRunAwol_EmbedDescriptionWithinDiscordLimit pins item 1: the per-chunk
// budget must leave room for the summary-line prefix + separator, so the FINAL
// rendered description (summaryLine + "\n\n" + chunk) never exceeds Discord's
// 4096-byte cap. Production budgets on len(...) (bytes), so this test asserts on
// bytes — the conservative measure — not runes.
//
// This is a genuine regression guard for the prefix-aware budget
// (`chunkBudget := discordEmbedDescriptionLimit - len(descPrefix)`). To trip the
// pre-fix bug a chunk must land in the danger band (4096 − prefixLen, 4096], where
// the raw chunk fits the bare 4096 budget but overflows once the prefix is
// prepended. The summary prefix is now "N flagged\n\n"; for N=45 that is
// "45 flagged\n\n" = 12 bytes, so the danger band is (4084, 4096]. Sizing targets
// that band exactly so the test still exercises the prefix reservation.
//
// Sizing math (all bytes):
//   - Each no-LOA row renders as "🔴 [U%02d_<12x>](https://7cav.us/rosters/profile/<id>) — 68d AWOL · last post 75d\n".
//     With a 12-char pad, 2-digit user index, 3-digit milpac id, and the 68/75 day
//     figures this calc produces for a 2026-03-01 post at awolRefDate
//     (DaysAWOL=68, raw=75 — both 2 digits), every row is exactly 91 bytes.
//   - Summary prefix "45 flagged\n\n" = 12 bytes → danger band (4084, 4096].
//   - 45 rows = 4095 raw bytes. The buggy budget (4096) packs all 45 into one
//     chunk; description = 12 + 4095 = 4107 > 4096 → FAILS pre-fix.
//   - The fixed budget (4096 − 12 = 4084) flushes after 44 rows = 4004 bytes;
//     description = 12 + 4004 = 4016 ≤ 4096 → PASSES. (2 chunks ≤ maxEmbedsPerMsg
//     so we stay on the embed path, not the file fallback.)
const (
	embedLimitTestUsers = 45
	embedLimitTestPad   = 12 // → 91-byte rows; see sizing math above
)

func TestRunAwol_EmbedDescriptionWithinDiscordLimit(t *testing.T) {
	pad := strings.Repeat("x", embedLimitTestPad)
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	for n := range embedLimitTestUsers {
		id := fmt.Sprintf("%d", 100+n)
		roster.LiteProfiles[id] = awolMember(fmt.Sprintf("U%02d_%s", n, pad), id, "2026-03-01 12:00:00")
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, healthyCache(nil), awolRefDate, i)

	edit := f.Calls()[1].Edit
	if edit.Embeds == nil {
		t.Fatalf("expected embeds on Edit (stay on embed path); got %+v", edit)
	}
	// Assert on bytes across EVERY emitted embed — the overflow can land in any
	// chunk, and production budgets on bytes (len), not runes.
	for idx, e := range *edit.Embeds {
		if n := len(e.Description); n > discordEmbedDescriptionLimit {
			t.Fatalf("embed[%d] description = %d bytes, exceeds Discord %d-byte limit",
				idx, n, discordEmbedDescriptionLimit)
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
	raw, _ := io.ReadAll(edit.Files[0].Reader)
	if !strings.Contains(string(raw), "d AWOL · last post") {
		t.Fatalf("file should carry days-AWOL rows.\nGot:\n%s", string(raw))
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

// TestRunAwol_ActiveLOAStillAWOL pins the "on LOA, still AWOL" case: a trooper
// with a pre-LOA unexcused gap is still flagged, rendered with the ⚪ glyph and a
// [LOA] thread link (active LOA overrides the severity tier regardless of days).
func TestRunAwol_ActiveLOAStillAWOL(t *testing.T) {
	// awolRefDate = 2026-05-15. Last post 2026-04-20. Candidate (04-20,05-15] =
	// 04-21..05-15 = 25 dates. Active LOA 2026-05-10..2026-05-31 covers 05-10..05-15
	// (6 candidate dates). Accountable = 19 → 12 AWOL. Active window → ⚪ + link.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Reyes.J", "100", "2026-04-20 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyCache(map[string][]utils.LOAEntry{
		"reyes.j": oneWindow(utils.LOAEntry{
			Username:  "Reyes.J",
			StartDate: mustParseAwolDate("2026-05-10 00:00:00"),
			EndDate:   mustParseAwolDate("2026-05-31 00:00:00"),
			ThreadID:  4242,
		}),
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, cache, awolRefDate, i)

	embed := (*f.Calls()[1].Edit.Embeds)[0]
	desc := embed.Description
	line := lineContaining(desc, "Reyes.J")
	if !strings.HasPrefix(line, "⚪") {
		t.Fatalf("active-LOA row must use ⚪ glyph regardless of days.\nGot line: %q", line)
	}
	if !strings.Contains(line, "[[LOA]](https://7cav.us/threads/4242/)") {
		t.Fatalf("active-LOA row must link the thread.\nGot line: %q", line)
	}
	if !strings.Contains(line, "12d AWOL · last post 25d") {
		t.Fatalf("expected '12d AWOL · last post 25d'.\nGot line: %q", line)
	}
	if !strings.Contains(desc, "1 flagged") {
		t.Fatalf("summary should report 1 flagged.\nGot:\n%s", desc)
	}
}

// TestRunAwol_ExpiredLOASubtractedNotTagged pins that an expired LOA still
// subtracts its covered dates (lowering days AWOL) but does NOT mark the trooper
// on LOA (no ⚪, no thread link) since the window isn't active now.
func TestRunAwol_ExpiredLOASubtractedNotTagged(t *testing.T) {
	// Last post 2026-01-01. awolRefDate 2026-05-15. Candidate huge. Expired LOA
	// 2026-01-03..2026-05-08 subtracts a big chunk but isn't active at now.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Tanner.K", "100", "2026-01-01 12:00:00"),
			"200": awolMember("Vasquez.A", "200", "2026-01-01 12:00:00"), // no LOA → much worse
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyCache(map[string][]utils.LOAEntry{
		"tanner.k": oneWindow(utils.LOAEntry{
			Username:  "Tanner.K",
			StartDate: mustParseAwolDate("2026-01-03 00:00:00"),
			EndDate:   mustParseAwolDate("2026-05-08 00:00:00"),
			ThreadID:  9001,
		}),
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, cache, awolRefDate, i)

	desc := (*f.Calls()[1].Edit.Embeds)[0].Description
	tanner := lineContaining(desc, "Tanner.K")
	if strings.Contains(tanner, "⚪") || strings.Contains(tanner, "LOA]") {
		t.Fatalf("expired LOA must not tag the row on-LOA.\nGot line: %q", tanner)
	}
	// Expired LOA subtracted → Tanner.K has fewer days AWOL than the no-LOA Vasquez.A,
	// so Vasquez.A sorts first.
	if strings.Index(desc, "Vasquez.A") > strings.Index(desc, "Tanner.K") {
		t.Fatalf("no-LOA Vasquez.A should outrank LOA-subtracted Tanner.K.\nGot:\n%s", desc)
	}
	if !strings.Contains(desc, "2 flagged") {
		t.Fatalf("summary line missing.\nGot:\n%s", desc)
	}
}

// TestRunAwol_FullyCoveredMemberNotListed pins that a trooper whose entire gap is
// covered by an active LOA (0 days AWOL) is not listed at all.
func TestRunAwol_FullyCoveredMemberNotListed(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Covered.C", "100", "2026-05-01 12:00:00"),
			"200": awolMember("Bare.B", "200", "2026-03-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyCache(map[string][]utils.LOAEntry{
		// Covers (05-01, 05-15] entirely → 0 accountable → not listed.
		"covered.c": oneWindow(utils.LOAEntry{
			Username:  "Covered.C",
			StartDate: mustParseAwolDate("2026-05-02 00:00:00"),
			EndDate:   mustParseAwolDate("2026-05-31 00:00:00"),
			ThreadID:  555,
		}),
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, cache, awolRefDate, i)

	desc := (*f.Calls()[1].Edit.Embeds)[0].Description
	if strings.Contains(desc, "Covered.C") {
		t.Fatalf("fully-covered member must not be listed.\nGot:\n%s", desc)
	}
	if !strings.Contains(desc, "Bare.B") {
		t.Fatalf("uncovered member must be listed.\nGot:\n%s", desc)
	}
	if !strings.Contains(desc, "1 flagged") {
		t.Fatalf("summary should count only listed members.\nGot:\n%s", desc)
	}
}

// unhealthyCache builds an unhealthy fakeLOACache: windows may exist but the
// health probe reports stale, so the handler must NOT subtract LOA and must warn
// loudly that the accountable-day adjustment was skipped.
func unhealthyCache(entries map[string][]utils.LOAEntry, lastRefresh time.Time) *fakeLOACache {
	return &fakeLOACache{entries: entries, healthy: false, lastRefresh: lastRefresh}
}

func TestRunAwol_UnhealthyCacheRendersRawFallback(t *testing.T) {
	// Two members. Trooper.A even has a (stale) window that would fully cover its
	// gap — but because the cache is unhealthy the handler computes RAW inactivity
	// (no subtraction), still flags, and warns that adjustment was skipped.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-05-01 12:00:00"),
			"200": awolMember("Trooper.B", "200", "2026-04-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := unhealthyCache(map[string][]utils.LOAEntry{
		"trooper.a": oneWindow(utils.LOAEntry{
			Username:  "Trooper.A",
			StartDate: mustParseAwolDate("2026-05-02 00:00:00"),
			EndDate:   mustParseAwolDate("2026-06-01 00:00:00"),
			ThreadID:  4242,
		}),
	}, awolRefDate.Add(-45*time.Minute))
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

	embed := (*f.Calls()[1].Edit.Embeds)[0]
	desc := embed.Description
	// Trooper.A would be fully covered if LOA were applied; raw fallback still
	// lists it with raw days (14 dates → 7 AWOL).
	if !strings.Contains(desc, "Trooper.A") {
		t.Fatalf("raw fallback must still flag Trooper.A.\nGot:\n%s", desc)
	}
	// No LOA decoration leaks through.
	if strings.Contains(desc, "LOA]") || strings.Contains(desc, "threads/4242") || strings.Contains(desc, "⚪") {
		t.Fatalf("no LOA decoration on degraded path.\nGot:\n%s", desc)
	}
	// Summary is just the flagged count — no LOA token on either path.
	if !strings.Contains(desc, "2 flagged") {
		t.Fatalf("degraded summary should report the flagged count.\nGot:\n%s", desc)
	}
	if strings.Contains(desc, "LOA unknown") || strings.Contains(desc, " LOA\n") || strings.Contains(desc, "· 0 LOA") || strings.Contains(desc, "· 1 LOA") {
		t.Fatalf("degraded summary must not carry an LOA count/unknown token.\nGot:\n%s", desc)
	}
	// Footer must say the adjustment was SKIPPED, not merely "stale column".
	if embed.Footer == nil || !strings.Contains(embed.Footer.Text, "SKIPPED") {
		got := "<nil>"
		if embed.Footer != nil {
			got = embed.Footer.Text
		}
		t.Fatalf("degraded footer must say adjustment SKIPPED.\nGot: %q", got)
	}
	if embed.Footer == nil || !strings.Contains(embed.Footer.Text, "LOA NOT subtracted") {
		t.Fatalf("degraded footer must say LOA NOT subtracted.\nGot: %q", embed.Footer.Text)
	}
}

// TestRunAwol_UnhealthyCacheEmptyListWarns pins item 2: when the LOA cache is
// unhealthy AND no member is raw-AWOL, the empty-result reply must still surface
// the degraded/SKIPPED warning rather than a silent all-clear, so staff know the
// cache was down (every other terminal degraded path warns; this one must too).
func TestRunAwol_UnhealthyCacheEmptyListWarns(t *testing.T) {
	// All members posted recently → no raw-AWOL, empty result. Cache unhealthy.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Fresh.A", "100", "2026-05-14 12:00:00"),
			"200": awolMember("Fresh.B", "200", "2026-05-13 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := unhealthyCache(nil, awolRefDate.Add(-30*time.Minute))
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

	edit := f.Calls()[1].Edit
	got := "<nil>"
	if edit.Content != nil {
		got = *edit.Content
	}
	if !strings.Contains(got, "SKIPPED") {
		t.Fatalf("unhealthy-cache empty result must warn the adjustment was SKIPPED, not a silent all-clear.\nGot: %q", got)
	}
}

func TestRunAwol_HealthyCacheEmptyListAllClear(t *testing.T) {
	// Healthy cache + empty result → the plain all-clear (no degraded warning).
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Fresh.A", "100", "2026-05-14 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, healthyCache(nil), awolRefDate, i)

	got := "<nil>"
	if edit := f.Calls()[1].Edit; edit.Content != nil {
		got = *edit.Content
	}
	if !strings.Contains(got, "no users matching") || strings.Contains(got, "SKIPPED") {
		t.Fatalf("healthy empty result should be the plain all-clear without a degraded warning.\nGot: %q", got)
	}
}

func TestRunAwol_UnhealthyCacheFileOutputCarriesWarning(t *testing.T) {
	// Force the file path; the unhealthy cache must surface the skipped-adjustment
	// warning and raw figures in the generated report rather than silent all-clear.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := unhealthyCache(map[string][]utils.LOAEntry{
		"trooper.a": oneWindow(utils.LOAEntry{Username: "Trooper.A", ThreadID: 4242}),
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
		t.Fatalf("file must not carry [LOA] tag on degraded path.\nGot:\n%s", body)
	}
	if !strings.Contains(body, "SKIPPED") {
		t.Fatalf("file should carry skipped-adjustment warning.\nGot:\n%s", body)
	}
	if !strings.Contains(body, "d AWOL · last post") {
		t.Fatalf("file should carry raw days-AWOL rows.\nGot:\n%s", body)
	}
}

// countingLOACache wraps fakeLOACache to count GetEntries calls per username,
// proving /awol reads each member's LOA history exactly ONCE — deriving the
// accountable-day figure AND the active-window link from that single read against
// one injected `now` (PR #161 clock-skew item).
type countingLOACache struct {
	*fakeLOACache
	getEntriesCalls map[string]int
}

func (c *countingLOACache) GetEntries(username string) []utils.LOAEntry {
	if c.getEntriesCalls == nil {
		c.getEntriesCalls = map[string]int{}
	}
	c.getEntriesCalls[strings.ToLower(username)]++
	return c.fakeLOACache.GetEntries(username)
}

func TestRunAwol_SingleHistoryReadPerUser(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-04-20 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := &countingLOACache{
		fakeLOACache: healthyCache(map[string][]utils.LOAEntry{
			"trooper.a": oneWindow(utils.LOAEntry{
				Username:  "Trooper.A",
				StartDate: mustParseAwolDate("2026-05-01 00:00:00"),
				EndDate:   mustParseAwolDate("2026-06-01 00:00:00"), // active at awolRefDate
				ThreadID:  4242,
			}),
		}),
	}
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, cache, awolRefDate, i)

	if n := cache.getEntriesCalls["trooper.a"]; n != 1 {
		t.Fatalf("GetEntries must be called exactly once per member, got %d", n)
	}
	line := lineContaining((*f.Calls()[1].Edit.Embeds)[0].Description, "Trooper.A")
	if !strings.Contains(line, "[[LOA]](https://7cav.us/threads/4242/)") {
		t.Fatalf("active member must render the [[LOA]] link from the single history read.\nGot:\n%s", line)
	}
}

// TestRunAwol_MultipleWindowsMergedNoDoubleCount pins that overlapping windows in
// a user's history don't double-subtract.
func TestRunAwol_MultipleWindowsMergedNoDoubleCount(t *testing.T) {
	// Last post 2026-04-15. awolRefDate 2026-05-15. Candidate (04-15,05-15] = 30.
	// Two overlapping windows 04-20..05-01 and 04-28..05-05 → union 04-20..05-05
	// (16 dates). Accountable = 14 → 7 AWOL. Neither active now → 🟠 not ⚪.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Multi.M", "100", "2026-04-15 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	cache := healthyCache(map[string][]utils.LOAEntry{
		"multi.m": {
			{Username: "Multi.M", StartDate: mustParseAwolDate("2026-04-20 00:00:00"), EndDate: mustParseAwolDate("2026-05-01 00:00:00"), ThreadID: 1},
			{Username: "Multi.M", StartDate: mustParseAwolDate("2026-04-28 00:00:00"), EndDate: mustParseAwolDate("2026-05-05 00:00:00"), ThreadID: 2},
		},
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, cache, awolRefDate, i)

	line := lineContaining((*f.Calls()[1].Edit.Embeds)[0].Description, "Multi.M")
	if !strings.Contains(line, "7d AWOL · last post 30d") {
		t.Fatalf("overlapping windows must merge (7d AWOL, raw 30d).\nGot line: %q", line)
	}
}

// TestRunAwol_MalformedDateSkipsMemberNotReport pins issue #163: one member with
// an unparseable LastForumPostDate is skipped (and reported), the rest of the
// roster still renders, and the report carries a visible skipped-count note.
func TestRunAwol_MalformedDateSkipsMemberNotReport(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": awolMember("Broken.B", "200", "not-a-date"),
			"300": awolMember("Trooper.C", "300", "2026-04-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, healthyCache(nil), awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder + Edit), got %d: %+v", len(calls), calls)
	}
	edit := calls[1].Edit
	if edit.Embeds == nil || len(*edit.Embeds) == 0 {
		t.Fatalf("report must still render as embeds; got Embeds=%v Content=%v", edit.Embeds, edit.Content)
	}
	desc := (*edit.Embeds)[0].Description
	for _, want := range []string{"Trooper.A", "Trooper.C"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("remaining member %q must still render.\nGot:\n%s", want, desc)
		}
	}
	if strings.Contains(desc, "Broken.B") {
		t.Fatalf("skipped member must not render.\nGot:\n%s", desc)
	}
	if !strings.Contains(desc, "⚠️ 1 record skipped due to errors (reported)") {
		t.Fatalf("report must carry the skipped-count note.\nGot:\n%s", desc)
	}
}

// TestRunAwol_MalformedUniformURLSkipsMemberNotReport pins the second issue #163
// call site: an AWOL member whose uniform URL doesn't match the milpac-ID pattern
// is skipped (and reported); the rest still render with the skipped note.
func TestRunAwol_MalformedUniformURLSkipsMemberNotReport(t *testing.T) {
	badUniform := awolMember("Badjpg.B", "999", "2026-03-01 12:00:00")
	badUniform.UniformUrl = "https://7cav.us/data/roster_uniforms/no-milpac-id-here.png"
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": badUniform,
			"300": awolMember("Trooper.C", "300", "2026-04-01 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, healthyCache(nil), awolRefDate, i)

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (placeholder + Edit), got %d: %+v", len(calls), calls)
	}
	edit := calls[1].Edit
	if edit.Embeds == nil || len(*edit.Embeds) == 0 {
		t.Fatalf("report must still render as embeds; got Embeds=%v Content=%v", edit.Embeds, edit.Content)
	}
	desc := (*edit.Embeds)[0].Description
	for _, want := range []string{"Trooper.A", "Trooper.C"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("remaining member %q must still render.\nGot:\n%s", want, desc)
		}
	}
	if strings.Contains(desc, "Badjpg.B") {
		t.Fatalf("skipped member must not render.\nGot:\n%s", desc)
	}
	if !strings.Contains(desc, "⚠️ 1 record skipped due to errors (reported)") {
		t.Fatalf("report must carry the skipped-count note.\nGot:\n%s", desc)
	}
}

// TestRunAwol_BothMalformedRecordsSkippedTogether pins the issue #163 acceptance
// scenario verbatim: one malformed date record AND one malformed uniform URL in
// the same roster — both skipped, the rest render, plural note shows the count.
func TestRunAwol_BothMalformedRecordsSkippedTogether(t *testing.T) {
	badUniform := awolMember("Badjpg.B", "999", "2026-03-01 12:00:00")
	badUniform.UniformUrl = "https://7cav.us/data/roster_uniforms/no-milpac-id-here.png"
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": awolMember("Baddate.D", "200", "not-a-date"),
			"300": badUniform,
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, healthyCache(nil), awolRefDate, i)

	desc := (*f.Calls()[1].Edit.Embeds)[0].Description
	if !strings.Contains(desc, "Trooper.A") {
		t.Fatalf("remaining member must still render.\nGot:\n%s", desc)
	}
	for _, skipped := range []string{"Baddate.D", "Badjpg.B"} {
		if strings.Contains(desc, skipped) {
			t.Fatalf("skipped member %q must not render.\nGot:\n%s", skipped, desc)
		}
	}
	if !strings.Contains(desc, "⚠️ 2 records skipped due to errors (reported)") {
		t.Fatalf("report must carry the plural skipped-count note.\nGot:\n%s", desc)
	}
}

// TestRunAwol_FileOutputCarriesSkippedNote pins that the file rendering path
// (force_file_output) surfaces the same skipped-records note as the embed path —
// the omission must not become silent just because the report went to a file.
func TestRunAwol_FileOutputCarriesSkippedNote(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Trooper.A", "100", "2026-03-01 12:00:00"),
			"200": awolMember("Baddate.D", "200", "not-a-date"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(
		stringOption("position", "1-7"),
		boolOption("force_file_output", true),
	)
	runAwol(f, healthyCache(nil), awolRefDate, i)

	edit := f.Calls()[1].Edit
	if len(edit.Files) != 1 {
		t.Fatalf("expected 1 file attachment, got %d", len(edit.Files))
	}
	raw, _ := io.ReadAll(edit.Files[0].Reader)
	body := string(raw)
	if !strings.Contains(body, "Trooper.A") {
		t.Fatalf("remaining member must still render in file.\nGot:\n%s", body)
	}
	if strings.Contains(body, "Baddate.D") {
		t.Fatalf("skipped member must not render in file.\nGot:\n%s", body)
	}
	if !strings.Contains(body, "⚠️ 1 record skipped due to errors (reported)") {
		t.Fatalf("file report must carry the skipped-count note.\nGot:\n%s", body)
	}
}

// TestRunAwol_SkippedNoteOnNoAwolPath pins that a "no users AWOL" outcome is not
// a silent all-clear when records were skipped: the only flaggable member failed
// to parse, so the response must carry the skipped-count note.
func TestRunAwol_SkippedNoteOnNoAwolPath(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			// Recent post → not AWOL.
			"100": awolMember("Active.A", "100", "2026-05-14 12:00:00"),
			// Unparseable date → skipped.
			"200": awolMember("Baddate.D", "200", "not-a-date"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, healthyCache(nil), awolRefDate, i)

	edit := f.Calls()[1].Edit
	if edit.Content == nil {
		t.Fatalf("expected text response on no-AWOL path")
	}
	if !strings.Contains(*edit.Content, "no users matching") {
		t.Fatalf("expected no-AWOL message, got %q", *edit.Content)
	}
	if !strings.Contains(*edit.Content, "⚠️ 1 record skipped due to errors (reported)") {
		t.Fatalf("no-AWOL response must carry the skipped-count note, got %q", *edit.Content)
	}
}
