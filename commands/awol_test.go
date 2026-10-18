package commands

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

// serveAwolRoster starts a stub 7Cav API that serves roster, or answers
// rosterStatus to every position search when it isn't 200, and serves no
// LOAs.
func serveAwolRoster(t *testing.T, roster utils.LiteRosterResponse, rosterStatus int) {
	t.Helper()
	api := serveCavAPI(t, awolRefDate, roster)
	if rosterStatus != http.StatusOK {
		api.failRoster(rosterStatus)
	}
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

// forumMember is awolMember with a forum user ID, the ID an LOA's Subject is
// matched on.
func forumMember(username, forumUserID, milpacID, lastForumPost string) utils.LiteProfileResponse {
	m := awolMember(username, milpacID, lastForumPost)
	m.User.UserID = forumUserID
	return m
}

// awolReport returns the description of every embed in the run's final
// edit, failing the test when the run sent no embeds.
func awolReport(t *testing.T, f *fakeResponder) string {
	t.Helper()
	calls := f.Calls()
	edit := calls[len(calls)-1].Edit
	if edit == nil || edit.Embeds == nil || len(*edit.Embeds) == 0 {
		t.Fatalf("expected a report in embeds; the run last showed %q", lastReply(t, calls))
	}
	var desc strings.Builder
	for _, e := range *edit.Embeds {
		desc.WriteString(e.Description)
		desc.WriteString("\n")
	}
	return desc.String()
}

// awolReplyText returns all the text the run's final edit shows a leader:
// its content, every embed's description and footer, and any file's body.
func awolReplyText(t *testing.T, f *fakeResponder) string {
	t.Helper()
	calls := f.Calls()
	edit := calls[len(calls)-1].Edit
	if edit == nil {
		t.Fatalf("the run never edited its reply; calls %+v", calls)
	}
	var text strings.Builder
	if edit.Content != nil {
		text.WriteString(*edit.Content + "\n")
	}
	if edit.Embeds != nil {
		for _, e := range *edit.Embeds {
			text.WriteString(e.Description + "\n")
			if e.Footer != nil {
				text.WriteString(e.Footer.Text + "\n")
			}
		}
	}
	for _, file := range edit.Files {
		body, err := io.ReadAll(file.Reader)
		if err != nil {
			t.Fatalf("reading file: %v", err)
		}
		text.Write(body)
	}
	return text.String()
}

// daysAWOLOn reads the days AWOL figure off a trooper's report line.
func daysAWOLOn(t *testing.T, line string) int {
	t.Helper()
	m := regexp.MustCompile(`(\d+)d AWOL`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("no days AWOL figure on line %q", line)
	}
	n, _ := strconv.Atoi(m[1])
	return n
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
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, awolRefDate, i)

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
	runAwol(f, awolRefDate, i)

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
	runAwol(f, awolRefDate, i)

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
	runAwol(f, awolRefDate, i)

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
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runAwol(f, awolRefDate, i)

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
	i := fakeAppCommandInteraction(
		stringOption("position", "1-7"),
		boolOption("force_file_output", true),
	)

	runAwol(f, awolRefDate, i)

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
	i := fakeAppCommandInteraction(stringOption("position", "Q/Z/9-9"))

	runAwol(f, awolRefDate, i)

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

// A roster fetch the 7Cav API fails tells the member to try again, and its
// error goes to Sentry, not to the member.
func TestRunAwolRosterFailureReachesSentryNotTheMember(t *testing.T) {
	serveAwolRoster(t, utils.LiteRosterResponse{}, http.StatusInternalServerError)
	rec := recordCaptures(t)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertLookupFailed(t, f.Calls(), rec, "awol")
}

// An LOA counts for the roster member whose forum user ID matches its
// Subject's, even when the member's username differs from the Subject's, as
// after a rename.
func TestRunAwolCreditsAnLOAToTheMemberWithItsSubjectsForumUserID(t *testing.T) {
	// Last post 2026-04-15: (04-15, 05-15] is 30 dates, 23d AWOL with no LOA.
	// The LOA covers 05-06..05-15, 10 of them, leaving 13d AWOL.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Renamed.R", "41", "100", "2026-04-15 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "7001", postDate: mustParseAwolDate("2026-05-05 18:00:00"),
		userID: "41", username: "OldName.R",
		start: "2026-05-06", end: "2026-05-20",
	})
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	line := lineContaining(awolReport(t, f), "Renamed.R")
	if got := daysAWOLOn(t, line); got != 13 {
		t.Fatalf("days AWOL = %d, want 13 with the LOA counted.\nGot line: %q", got, line)
	}
}

// An LOA never counts for a member who only shares its Subject's username.
func TestRunAwolNeverCreditsAnLOAToAMemberWhoOnlySharesItsSubjectsUsername(t *testing.T) {
	// Last post 2026-04-15: (04-15, 05-15] is 30 dates, 23d AWOL. The LOA
	// would cover all of them, but its Subject is another forum user.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Shared.S", "50", "100", "2026-04-15 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "7002", postDate: mustParseAwolDate("2026-04-15 18:00:00"),
		userID: "51", username: "Shared.S",
		start: "2026-04-16", end: "2026-05-20",
	})
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	report := awolReport(t, f)
	line := lineContaining(report, "Shared.S")
	if line == "" {
		t.Fatalf("Shared.S must stay listed.\nGot:\n%s", report)
	}
	if got := daysAWOLOn(t, line); got != 23 {
		t.Fatalf("days AWOL = %d, want the full 23.\nGot line: %q", got, line)
	}
}

// An LOA that ended exactly 364 days before the bot's UTC today still
// subtracts the days it covered.
func TestRunAwolSubtractsAnLOAThatEnded364DaysAgo(t *testing.T) {
	// 364 days before 2026-05-15 is 2025-05-16. Last post 2025-05-11:
	// (2025-05-11, 2026-05-15] is 369 dates, 362d AWOL with no LOA. The LOA
	// covered 2025-05-12..2025-05-16, 5 of them, leaving 357d AWOL.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Silent.S", "60", "100", "2025-05-11 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "7003", postDate: mustParseAwolDate("2025-05-11 18:00:00"),
		userID: "60", username: "Silent.S",
		start: "2025-05-12", end: "2025-05-16",
	})
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	line := lineContaining(awolReport(t, f), "Silent.S")
	if got := daysAWOLOn(t, line); got != 357 {
		t.Fatalf("days AWOL = %d, want 357 with the ended LOA subtracted.\nGot line: %q", got, line)
	}
}

// A run at 23:59 UTC, when the API's UTC date has already moved on to the
// next day, reports normally with no fallback warning.
func TestRunAwolJustBeforeUTCMidnightReportsWithoutTheFallbackWarning(t *testing.T) {
	now := mustParseAwolDate("2026-05-15 23:59:00")
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Trooper.A", "70", "100", "2026-04-15 12:00:00"),
		},
	}
	api := serveCavAPI(t, now, roster)
	api.setToday(mustParseAwolDate("2026-05-16 00:00:00"))
	f := &fakeResponder{}
	runAwol(f, now, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if reply := awolReplyText(t, f); strings.Contains(reply, awolDegradedFooter) || !strings.Contains(reply, "Trooper.A") {
		t.Fatalf("want Trooper.A reported with no fallback warning.\nGot:\n%s", reply)
	}
}

// A needs-review LOA subtracts none of its days, so its trooper keeps their
// full days AWOL.
func TestRunAwolNeedsReviewLOASubtractsNoneOfItsDays(t *testing.T) {
	// Last post 2026-04-15: (04-15, 05-15] is 30 dates, 23d AWOL. The LOA's
	// dates read and cover 04-16..05-05, but it runs more than 14 days.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Review.R", "80", "100", "2026-04-15 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "7005", postDate: mustParseAwolDate("2026-04-16 09:00:00"),
		userID: "80", username: "Review.R",
		start: "2026-04-16", end: "2026-05-05",
		needsReview: true, reviewReason: "LOA_REVIEW_REASON_TOO_LONG",
	})
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	report := awolReport(t, f)
	line := lineContaining(report, "Review.R")
	if line == "" {
		t.Fatalf("Review.R must stay listed.\nGot:\n%s", report)
	}
	if got := daysAWOLOn(t, line); got != 23 {
		t.Fatalf("days AWOL = %d, want the full 23.\nGot line: %q", got, line)
	}
}

// When the LOA call answers 404, /awol still reports, on raw figures with the
// fallback warning: the shared client's not-found answer is a failure here,
// never "no LOAs". The failure reaches Sentry once and the reply carries
// none of it.
func TestRunAwolLOACallAnswering404FallsBackToRawFiguresAndReachesSentry(t *testing.T) {
	// Trooper.A's LOA would cover the whole gap. Unread, the report shows raw
	// figures: (05-01, 05-15] is 14 dates, 7d AWOL.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Trooper.A", "90", "100", "2026-05-01 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "4242", postDate: mustParseAwolDate("2026-05-01 18:00:00"),
		userID: "90", username: "Trooper.A",
		start: "2026-05-02", end: "2026-06-01",
	}).failLOAs(http.StatusNotFound)
	rec := recordCaptures(t)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	line := lineContaining(awolReport(t, f), "Trooper.A")
	if got := daysAWOLOn(t, line); got != 7 {
		t.Fatalf("days AWOL = %d, want the raw 7.\nGot line: %q", got, line)
	}
	reply := awolReplyText(t, f)
	if !strings.Contains(reply, awolDegradedFooter) {
		t.Fatalf("reply must carry the fallback warning.\nGot:\n%s", reply)
	}
	if rec.count != 1 {
		t.Fatalf("captures = %d, want 1", rec.count)
	}
	if strings.Contains(reply, rec.errs[0].Error()) {
		t.Fatalf("reply carries the error's text %q.\nGot:\n%s", rec.errs[0].Error(), reply)
	}
}

// An LOA deleted a minute ago stops counting on the next run: /awol keeps no
// LOAs between runs.
func TestRunAwolStopsCountingADeletedLOAOnTheNextRun(t *testing.T) {
	// Last post 2026-04-15: 23d AWOL with no LOA, 13d with the LOA covering
	// 05-06..05-15.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Trooper.D", "95", "100", "2026-04-15 12:00:00"),
		},
	}
	api := serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "7008", postDate: mustParseAwolDate("2026-05-05 18:00:00"),
		userID: "95", username: "Trooper.D",
		start: "2026-05-06", end: "2026-05-20",
	})
	first := &fakeResponder{}
	runAwol(first, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))
	if got := daysAWOLOn(t, lineContaining(awolReport(t, first), "Trooper.D")); got != 13 {
		t.Fatalf("first run: days AWOL = %d, want 13 with the LOA counted", got)
	}

	api.setLOAs()
	second := &fakeResponder{}
	runAwol(second, awolRefDate.Add(time.Minute), fakeAppCommandInteraction(stringOption("position", "1-7")))
	if got := daysAWOLOn(t, lineContaining(awolReport(t, second), "Trooper.D")); got != 23 {
		t.Fatalf("second run: days AWOL = %d, want the full 23 once the LOA is deleted", got)
	}
}

// A leader waits no longer for /awol than before: it asks for the roster and
// the LOAs at the same time. The stub holds each call until the other comes.
func TestRunAwolAsksForTheRosterAndTheLOAsAtTheSameTime(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Trooper.A", "99", "100", "2026-04-15 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster).pairCalls()
	rec := recordCaptures(t)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	report := awolReport(t, f)
	if !strings.Contains(report, "Trooper.A") {
		t.Fatalf("Trooper.A must be reported.\nGot:\n%s", report)
	}
	if reply := awolReplyText(t, f); strings.Contains(reply, awolDegradedFooter) || rec.count != 0 {
		t.Fatalf("want both calls answered: %d captures.\nGot:\n%s", rec.count, reply)
	}
}

// TestRunAwol_ActiveLOAStillAWOL pins the "on LOA, still AWOL" case: a trooper
// with a pre-LOA unexcused gap is still flagged, rendered with the ⚪ glyph and a
// [LOA] thread link (active LOA overrides the severity tier regardless of days).
func TestRunAwol_ActiveLOAStillAWOL(t *testing.T) {
	// awolRefDate = 2026-05-15. Last post 2026-04-20. Candidate (04-20,05-15] =
	// 04-21..05-15 = 25 dates. Open LOA 2026-05-10..2026-05-31 covers 05-10..05-15
	// (6 candidate dates). Accountable = 19 → 12 AWOL. Open LOA → ⚪ + link.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Reyes.J", "7", "100", "2026-04-20 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "4242", postDate: mustParseAwolDate("2026-05-09 18:00:00"),
		userID: "7", username: "Reyes.J",
		start: "2026-05-10", end: "2026-05-31",
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, awolRefDate, i)

	line := lineContaining((*f.Calls()[1].Edit.Embeds)[0].Description, "Reyes.J")
	if !strings.HasPrefix(line, "⚪") {
		t.Fatalf("active-LOA row must use ⚪ glyph regardless of days.\nGot line: %q", line)
	}
	if !strings.Contains(line, awolLOATag) || !lineLinksThread(line, "4242") {
		t.Fatalf("active-LOA row must carry %s and link thread 4242.\nGot line: %q", awolLOATag, line)
	}
	if got := daysAWOLOn(t, line); got != 12 {
		t.Fatalf("days AWOL = %d, want 12.\nGot line: %q", got, line)
	}
}

// lineLinksThread reports whether some link target on line is a URL on
// 7cav.us whose path names threadID, with or without a trailing slash.
func lineLinksThread(line, threadID string) bool {
	for _, m := range regexp.MustCompile(`\]\(([^)\s]+)\)`).FindAllStringSubmatch(line, -1) {
		u, err := url.Parse(m[1])
		if err != nil || u.Host != "7cav.us" {
			continue
		}
		for _, segment := range strings.Split(u.Path, "/") {
			if segment == threadID {
				return true
			}
		}
	}
	return false
}

// TestRunAwol_ExpiredLOASubtractedNotTagged pins that an expired LOA still
// subtracts its covered dates (lowering days AWOL) but does NOT mark the trooper
// on LOA (no ⚪, no thread link) since the window isn't active now.
func TestRunAwol_ExpiredLOASubtractedNotTagged(t *testing.T) {
	// Last post 2026-01-01. awolRefDate 2026-05-15. Candidate huge. Expired LOA
	// 2026-01-03..2026-05-08 subtracts a big chunk but isn't active at now.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Tanner.K", "11", "100", "2026-01-01 12:00:00"),
			"200": forumMember("Vasquez.A", "12", "200", "2026-01-01 12:00:00"), // no LOA → much worse
		},
	}
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "9001", postDate: mustParseAwolDate("2026-01-02 18:00:00"),
		userID: "11", username: "Tanner.K",
		start: "2026-01-03", end: "2026-05-08",
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, awolRefDate, i)

	desc := (*f.Calls()[1].Edit.Embeds)[0].Description
	tanner := lineContaining(desc, "Tanner.K")
	if strings.Contains(tanner, "⚪") || strings.Contains(tanner, awolLOATag) {
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
			"100": forumMember("Covered.C", "21", "100", "2026-05-01 12:00:00"),
			"200": forumMember("Bare.B", "22", "200", "2026-03-01 12:00:00"),
		},
	}
	// Covers (05-01, 05-15] entirely → 0 accountable → not listed.
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "555", postDate: mustParseAwolDate("2026-05-01 12:00:00"),
		userID: "21", username: "Covered.C",
		start: "2026-05-02", end: "2026-05-31",
	})
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, awolRefDate, i)

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

// TestRunAwol_LOAsUnreadEmptyListWarns pins that when the LOA call fails AND
// no member is raw-AWOL, the empty-result reply still carries the fallback
// warning rather than a silent all-clear.
func TestRunAwol_LOAsUnreadEmptyListWarns(t *testing.T) {
	// All members posted recently → no raw-AWOL, empty result. LOA call fails.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Fresh.A", "100", "2026-05-14 12:00:00"),
			"200": awolMember("Fresh.B", "200", "2026-05-13 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster).failLOAs(http.StatusInternalServerError)
	recordCaptures(t)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, awolRefDate, i)

	edit := f.Calls()[1].Edit
	got := "<nil>"
	if edit.Content != nil {
		got = *edit.Content
	}
	if !strings.Contains(got, awolDegradedFooter) {
		t.Fatalf("empty result with LOAs unread must carry the fallback warning, not a silent all-clear.\nGot: %q", got)
	}
}

func TestRunAwol_HealthyCacheEmptyListAllClear(t *testing.T) {
	// LOAs read + empty result → the plain all-clear (no fallback warning).
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": awolMember("Fresh.A", "100", "2026-05-14 12:00:00"),
		},
	}
	serveAwolRoster(t, roster, http.StatusOK)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, awolRefDate, i)

	got := "<nil>"
	if edit := f.Calls()[1].Edit; edit.Content != nil {
		got = *edit.Content
	}
	if !strings.Contains(got, "no users matching") || strings.Contains(got, awolDegradedFooter) {
		t.Fatalf("empty result should be the plain all-clear without the fallback warning.\nGot: %q", got)
	}
}

func TestRunAwol_LOAsUnreadFileOutputCarriesWarning(t *testing.T) {
	// Force the file path; with the LOA call failing, the report must carry
	// the fallback warning and raw figures, and no [LOA] tag.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Trooper.A", "31", "100", "2026-03-01 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster, stubLOA{
		threadID: "4242", postDate: mustParseAwolDate("2026-05-01 12:00:00"),
		userID: "31", username: "Trooper.A",
		start: "2026-05-10", end: "2026-05-31",
	}).failLOAs(http.StatusInternalServerError)
	recordCaptures(t)

	f := &fakeResponder{}
	i := fakeAppCommandInteraction(
		stringOption("position", "1-7"),
		boolOption("force_file_output", true),
	)
	runAwol(f, awolRefDate, i)

	edit := f.Calls()[1].Edit
	if len(edit.Files) != 1 {
		t.Fatalf("expected 1 file attachment, got %d", len(edit.Files))
	}
	raw, err := io.ReadAll(edit.Files[0].Reader)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, awolLOATag) {
		t.Fatalf("file must not carry %s with LOAs unread.\nGot:\n%s", awolLOATag, body)
	}
	if !strings.Contains(body, awolDegradedFooter) {
		t.Fatalf("file should carry the fallback warning.\nGot:\n%s", body)
	}
	if !strings.Contains(body, "Trooper.A") {
		t.Fatalf("file should list Trooper.A on raw figures.\nGot:\n%s", body)
	}
}

// TestRunAwol_MultipleWindowsMergedNoDoubleCount pins that overlapping windows in
// a user's history don't double-subtract.
func TestRunAwol_MultipleWindowsMergedNoDoubleCount(t *testing.T) {
	// Last post 2026-04-15. awolRefDate 2026-05-15. Candidate (04-15,05-15] = 30.
	// Two overlapping LOAs 04-20..05-01 and 04-28..05-05 → union 04-20..05-05
	// (16 dates). Accountable = 14 → 7 AWOL. Neither active now → 🟠 not ⚪.
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": forumMember("Multi.M", "41", "100", "2026-04-15 12:00:00"),
		},
	}
	serveCavAPI(t, awolRefDate, roster,
		stubLOA{threadID: "1", postDate: mustParseAwolDate("2026-04-15 12:00:00"), userID: "41", username: "Multi.M", start: "2026-04-20", end: "2026-05-01"},
		stubLOA{threadID: "2", postDate: mustParseAwolDate("2026-04-15 12:00:00"), userID: "41", username: "Multi.M", start: "2026-04-28", end: "2026-05-05"},
	)
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))
	runAwol(f, awolRefDate, i)

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
	runAwol(f, awolRefDate, i)

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
	runAwol(f, awolRefDate, i)

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
	runAwol(f, awolRefDate, i)

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
	runAwol(f, awolRefDate, i)

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
	runAwol(f, awolRefDate, i)

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
