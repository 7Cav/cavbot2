package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// promoInteraction builds an InteractionCreate carrying the /promo options.
func promoInteraction(position, asOf string) *discordgo.InteractionCreate {
	opts := []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "position", Type: discordgo.ApplicationCommandOptionString, Value: position},
	}
	if asOf != "" {
		opts = append(opts, &discordgo.ApplicationCommandInteractionDataOption{
			Name: "as_of", Type: discordgo.ApplicationCommandOptionString, Value: asOf,
		})
	}
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionApplicationCommand,
			Data: discordgo.ApplicationCommandInteractionData{
				Name:    "promo",
				Options: opts,
			},
			Member: &discordgo.Member{User: &discordgo.User{ID: "42", Username: "tester"}},
		},
	}
}

func promoLiteProfile(username, rankShort, uniformID string) utils.LiteProfileResponse {
	return utils.LiteProfileResponse{
		User:       utils.User{UserID: "u-" + username, Username: username},
		Rank:       utils.Rank{RankShort: rankShort},
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/" + uniformID + ".jpg",
	}
}

func promoFullProfile(username, rankShort, promotionDate, joinDate, position string, courseRecords ...string) utils.ProfileResponse {
	records := make([]utils.Record, 0, len(courseRecords))
	for _, d := range courseRecords {
		records = append(records, utils.Record{RecordDetails: d, RecordType: "RECORD_TYPE_GRADUATION", RecordDate: "2025-01-01"})
	}
	return utils.ProfileResponse{
		User:          utils.User{UserID: "u-" + username, Username: username},
		Rank:          utils.Rank{RankShort: rankShort},
		Primary:       utils.Position{PositionTitle: position},
		Records:       records,
		JoinDate:      joinDate,
		PromotionDate: promotionDate,
	}
}

func TestRunPromoHappyPath(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		// Eligible: PVT past 30d TIG.
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
		// Not eligible: PVT short on TIG.
		"2": promoLiteProfile("Fresh.F", "PVT", "102"),
		// Skipped cheaply: no ladder for SGM, no profile fetch attempted.
		"3": promoLiteProfile("Topout.T", "SGM", "103"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman 1/1/1/A"),
		"Fresh.F": promoFullProfile("Fresh.F", "PVT", "2026-05-10", "2026-05-10", "Rifleman 1/1/1/A"),
		// Deliberately no profile for Topout.T; a fetch would 404 and turn
		// into a skip, so a clean output also proves the cheap pre-filter (SGM
		// has no ladder and §VII is unavailable this run).
	}
	serveRosterAndProfiles(t, roster, 200, profiles)

	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "Ready.R") {
		t.Errorf("output missing eligible member: %q", content)
	}
	if !strings.Contains(content, "PVT → PFC") {
		t.Errorf("output missing rank transition: %q", content)
	}
	if strings.Contains(content, "Fresh.F") {
		t.Errorf("output includes ineligible member: %q", content)
	}
	if strings.Contains(content, "Topout.T") {
		t.Errorf("output includes ladderless member: %q", content)
	}
	if strings.Contains(content, "skipped") {
		t.Errorf("unexpected skip warning (COL should be pre-filtered): %q", content)
	}
	if !strings.Contains(content, "⚠️") {
		t.Errorf("disclaimer missing: %q", content)
	}
}

func TestRunPromoAsOfDateExpandsEligibility(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Fresh.F", "PVT", "102"),
	}}
	profiles := map[string]utils.ProfileResponse{
		// 5d TIG at ref date; 30d gate clears 2026-06-09.
		"Fresh.F": promoFullProfile("Fresh.F", "PVT", "2026-05-10", "2026-05-10", "Rifleman"),
	}
	serveRosterAndProfiles(t, roster, 200, profiles)

	f := &fakeResponder{}
	// as_of in the org's DDMMMYY format, lowercase to prove case-insensitivity.
	runPromo(f, promoInteraction("ACD", "01jul26"), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "Fresh.F") {
		t.Errorf("member should be eligible as of 01JUL26: %q", content)
	}
	if !strings.Contains(content, "01JUL26") {
		t.Errorf("as-of date missing from header (want DDMMMYY): %q", content)
	}
}

func TestParsePromoDate(t *testing.T) {
	for _, ok := range []string{"30JUL26", "30jul26", "30Jul26", "2026-07-30"} {
		got, err := parsePromoDate(ok)
		if err != nil || got.Format("2006-01-02") != "2026-07-30" {
			t.Errorf("parsePromoDate(%q) = %v, %v; want 2026-07-30", ok, got, err)
		}
	}
	for _, bad := range []string{"julyish", "30JULY26", "2026/07/30", ""} {
		if _, err := parsePromoDate(bad); err == nil {
			t.Errorf("parsePromoDate(%q) should fail", bad)
		}
	}
}

// §VII is always discretionary; a candidate eligible via standard-automatic
// AND §VII reads "automatic & discretionary" with paths "standard, §VII".
func TestPromoCandidateTypesAndPaths(t *testing.T) {
	rk := &viiRank{Short: "SGT"}
	cases := []struct {
		name      string
		c         promoCandidate
		wantTypes string
		wantPaths string
	}{
		{
			"automatic standard only",
			promoCandidate{Verdict: promoEligibility{Eligible: true, Type: "automatic"}},
			"automatic", "standard",
		},
		{
			"standard automatic + §VII → automatic & discretionary",
			promoCandidate{Verdict: promoEligibility{Eligible: true, Type: "automatic"}, ViaVII: true, Vii: &viiResult{Target: rk}},
			"automatic & discretionary", "standard, §VII",
		},
		{
			"§VII only is discretionary",
			promoCandidate{Verdict: promoEligibility{Eligible: false}, ViaVII: true, Vii: &viiResult{Target: rk}},
			"discretionary", "§VII",
		},
		{
			"discretionary standard only",
			promoCandidate{Verdict: promoEligibility{Eligible: true, Type: "discretionary"}},
			"discretionary", "standard",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(promoCandidateTypes(tc.c), " & "); got != tc.wantTypes {
				t.Errorf("types = %q, want %q", got, tc.wantTypes)
			}
			if got := strings.Join(promoCandidatePaths(tc.c), ", "); got != tc.wantPaths {
				t.Errorf("paths = %q, want %q", got, tc.wantPaths)
			}
		})
	}
}

// A position/unit scope displays upper-cased regardless of input casing;
// activeduty stays the readable phrase.
func TestScopeDisplayCasing(t *testing.T) {
	for in, want := range map[string]string{
		"acd": "ACD", "Acd": "ACD", "ACD": "ACD", "s1": "S1",
		"activeduty": "active duty", "active-duty": "active duty",
	} {
		if got := scopeDisplay(in); got != want {
			t.Errorf("scopeDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunPromoInvalidAsOfDate(t *testing.T) {
	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", "julyish"), afsmRefDate)

	found := false
	for _, call := range f.Calls() {
		if call.Method == "Respond" && call.Response != nil && call.Response.Data != nil &&
			strings.Contains(call.Response.Data.Content, "Invalid as_of") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected invalid-date error response, calls: %+v", f.Calls())
	}
}

func TestRunPromoEmptyRoster(t *testing.T) {
	// User-supplied position: empty roster is a message-only outcome routed
	// through HandleError with the shared search-format hint (ADR 0002).
	serveRosterAndProfiles(t, utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}, 200, nil)

	f := &fakeResponder{}
	runPromo(f, promoInteraction("XYZZY", ""), afsmRefDate)

	found := false
	for _, call := range f.Calls() {
		if call.Method == "Respond" && call.Response != nil && call.Response.Data != nil &&
			strings.Contains(call.Response.Data.Content, "No troopers found") &&
			strings.Contains(call.Response.Data.Content, "XYZZY") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected shared empty-roster message, calls: %+v", f.Calls())
	}
}

func TestRunPromoRosterFetchError(t *testing.T) {
	serveRosterAndProfiles(t, utils.LiteRosterResponse{}, 500, nil)

	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	// HandleError delivers errors via Respond (ephemeral), not Edit.
	found := false
	for _, call := range f.Calls() {
		if call.Method == "Respond" && call.Response != nil && call.Response.Data != nil &&
			strings.Contains(call.Response.Data.Content, "Failed to fetch roster") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected roster fetch error, calls: %+v", f.Calls())
	}
}

func TestRunPromoMemberFetchFailureSkipsAndReports(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
		"2": promoLiteProfile("Ghost.G", "PVT", "102"), // no profile → 404 → skip
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
	}
	serveRosterAndProfiles(t, roster, 200, profiles)

	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "Ready.R") {
		t.Errorf("eligible member missing despite unrelated skip: %q", content)
	}
	if !strings.Contains(content, "1 member skipped") {
		t.Errorf("skip warning missing: %q", content)
	}
}

// A long list fills one message and reports the remainder for the attached
// report: the message never exceeds Discord's cap.
func TestFormatPromoMessageOverflows(t *testing.T) {
	candidates := make([]promoCandidate, 80)
	for idx := range candidates {
		candidates[idx] = promoCandidate{
			Username:  fmt.Sprintf("Member.%03d", idx),
			MilpacURL: "https://7cav.us/rosters/profile/1",
			RankShort: "PVT",
			Verdict:   promoEligibility{Eligible: true, NextRank: "PFC", Type: "automatic"},
		}
	}
	msg, omitted := formatPromoMessage("ACD", promoFilter{}, mustParseDate("2026-05-15"), candidates, 1, false)
	if omitted <= 0 {
		t.Fatalf("80 candidates should overflow one message, omitted = %d", omitted)
	}
	if len(msg) >= 2000 {
		t.Errorf("message length %d exceeds Discord limit", len(msg))
	}
	if !strings.Contains(msg, fmt.Sprintf("…and %d more", omitted)) {
		t.Errorf("overflow notice missing or miscounted: %q", msg)
	}
	if !strings.Contains(msg, "members eligible for promotion") {
		t.Errorf("header missing: %q", msg)
	}
	// Footer notes survive alongside the overflow notice.
	if !strings.Contains(msg, "standard ladder only") || !strings.Contains(msg, "1 member skipped") {
		t.Errorf("footer notes missing: %q", msg)
	}
	// The listed names are a prefix of the candidate order.
	if !strings.Contains(msg, "Member.000") {
		t.Errorf("first candidate missing: %q", msg)
	}
}

// A list that fits needs no attachment.
func TestFormatPromoMessageFitsWithoutOverflow(t *testing.T) {
	candidates := []promoCandidate{{
		Username:  "Ready.R",
		MilpacURL: "https://7cav.us/rosters/profile/1",
		RankShort: "PVT",
		Verdict:   promoEligibility{Eligible: true, NextRank: "PFC", Type: "automatic"},
	}}
	msg, omitted := formatPromoMessage("ACD", promoFilter{}, mustParseDate("2026-05-15"), candidates, 0, true)
	if omitted != 0 {
		t.Errorf("omitted = %d, want 0", omitted)
	}
	if strings.Contains(msg, "attached report") {
		t.Errorf("overflow notice should not render: %q", msg)
	}
}

// manyEligiblePVTs builds a roster and profile set of n eligible PVTs; long
// enough scopes force multi-message output.
func manyEligiblePVTs(n int) (utils.LiteRosterResponse, map[string]utils.ProfileResponse) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	profiles := make(map[string]utils.ProfileResponse, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Member.%03d", i)
		roster.LiteProfiles[name] = promoLiteProfile(name, "PVT", "101")
		profiles[name] = promoFullProfile(name, "PVT", "2026-04-01", "2026-04-01", "Rifleman")
	}
	return roster, profiles
}

// A scope whose list exceeds one message posts what fits and attaches the
// full report: every candidate still reaches the reader.
func TestRunPromoLongListAttachesReport(t *testing.T) {
	roster, profiles := manyEligiblePVTs(60)
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if len(content) >= 2000 {
		t.Errorf("message length %d exceeds Discord limit", len(content))
	}
	// The message keeps the inline list and the "…and N more" overflow notice.
	if !strings.Contains(content, "more. Full list in the attached report") {
		t.Errorf("overflow notice missing: %q", content)
	}
	if !strings.Contains(content, "Member.000") {
		t.Errorf("inline list should show the first candidates: %q", content)
	}
	// The full detail is still attached as both CSV and HTML.
	csvBody, htmlBody := lastAttachments(t, f)
	for i := 0; i < 60; i++ {
		who := fmt.Sprintf("Member.%03d", i)
		if !strings.Contains(htmlBody, who) {
			t.Fatalf("candidate %s missing from HTML report", who)
		}
		if !strings.Contains(csvBody, who) {
			t.Fatalf("candidate %s missing from CSV report", who)
		}
	}
	if !strings.Contains(csvBody, "username,current_rank,primary_billet") {
		t.Errorf("CSV header missing billet column: %q", csvBody[:min(120, len(csvBody))])
	}
}

func TestFormatPromoMessageNoCandidates(t *testing.T) {
	msg, omitted := formatPromoMessage("ACD", promoFilter{}, mustParseDate("2026-05-15"), nil, 0, true)
	if omitted != 0 {
		t.Errorf("omitted = %d, want 0", omitted)
	}
	if !strings.Contains(msg, "No ACD members eligible") {
		t.Errorf("expected no-candidates message, got %q", msg)
	}
}

func TestPromoDefinition(t *testing.T) {
	cmd := Promo()
	if cmd.Definition.Name != "promo" {
		t.Errorf("command name = %q, want promo", cmd.Definition.Name)
	}
	if len(cmd.Definition.Options) != 7 {
		t.Fatalf("options = %d, want 7 (position, user, rank, type, exclude, as_of, force_file_output)", len(cmd.Definition.Options))
	}
	for _, opt := range cmd.Definition.Options {
		if opt.Required {
			t.Errorf("option %q should be optional (mode validated at runtime)", opt.Name)
		}
	}
	if cmd.Handler == nil {
		t.Error("handler is nil")
	}
	// Registry wiring.
	if _, ok := NewRegistry().GetHandler("promo"); !ok {
		t.Error("promo not registered in NewRegistry")
	}
}

// Guard against clock drift in default as-of handling: runPromo must use the
// injected now, not the wall clock.
func TestRunPromoUsesInjectedNow(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
	}
	serveRosterAndProfiles(t, roster, 200, profiles)

	f := &fakeResponder{}
	injected := mustParseDate("2030-01-01")
	runPromo(f, promoInteraction("ACD", ""), injected)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "01JAN30") {
		t.Errorf("header should show injected date, got %q", content)
	}
}

// servePromoAPIWithRanks extends the AFSM test-server pattern with the
// /milpacs/ranks endpoint the §VII path needs.
func servePromoAPIWithRanks(
	t *testing.T,
	roster utils.LiteRosterResponse,
	profilesByUsername map[string]utils.ProfileResponse,
	ranks *utils.RanksResponse,
) {
	t.Helper()
	rosterBody, err := json.Marshal(roster)
	if err != nil {
		t.Fatalf("marshal roster: %v", err)
	}
	ranksBody, err := json.Marshal(ranks)
	if err != nil {
		t.Fatalf("marshal ranks: %v", err)
	}
	encodedProfiles := make(map[string][]byte, len(profilesByUsername))
	for name, p := range profilesByUsername {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal profile %q: %v", name, err)
		}
		encodedProfiles[name] = b
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/milpacs/ranks"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(ranksBody)
		case strings.HasPrefix(r.URL.Path, "/milpacs/position/search/"),
			strings.HasPrefix(r.URL.Path, "/roster/"):
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

func TestRunPromoViiPath(t *testing.T) {
	vet := viiVetProfile() // CPL, previously held SSG as Section Leader
	vet.UniformUrl = "https://7cav.us/data/roster_uniforms/0/301.jpg"

	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": {
			User:       vet.User,
			Rank:       vet.Rank,
			UniformUrl: vet.UniformUrl,
		},
	}}
	servePromoAPIWithRanks(t, roster, map[string]utils.ProfileResponse{"Vet.V": *vet}, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "Vet.V") {
		t.Fatalf("§VII veteran missing from output: %q", content)
	}
	if !strings.Contains(content, "§VII veteran retention") {
		t.Errorf("§VII path not labeled: %q", content)
	}
	if !strings.Contains(content, "CPL → SSG") {
		t.Errorf("restoration target missing: %q", content)
	}
	if !strings.Contains(content, "held 2021-01-01") {
		t.Errorf("held-date evidence missing: %q", content)
	}
	if strings.Contains(content, "§VII veteran-retention check unavailable") {
		t.Errorf("degradation notice should not render when ranks fetch succeeds: %q", content)
	}
}

func TestRunPromoViiDegradationNotice(t *testing.T) {
	// The plain server (no /milpacs/ranks route) 404s the ranks fetch -
	// output must carry the standard-only notice.
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
	}
	serveRosterAndProfiles(t, roster, 200, profiles)

	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "standard ladder only") {
		t.Errorf("degradation notice missing: %q", content)
	}
	if !strings.Contains(content, "Ready.R") {
		t.Errorf("standard path should still work: %q", content)
	}
}

// --- rank ordering -----------------------------------------------------------

func TestPromoRankIndex(t *testing.T) {
	// Most-senior-first: officer < warrant < enlisted; alphabetic ties broken
	// elsewhere. Unknown ranks sort after every known one.
	ordered := []string{"COL", "2LT", "CW5", "WO1", "CSM", "SGT", "PVT"}
	for i := 1; i < len(ordered); i++ {
		if promoRankIndex(ordered[i-1]) >= promoRankIndex(ordered[i]) {
			t.Errorf("%s should sort before %s", ordered[i-1], ordered[i])
		}
	}
	if promoRankIndex("pfc") != promoRankIndex("PFC") {
		t.Error("rank index must be case-insensitive")
	}
	if promoRankIndex("XYZ") <= promoRankIndex("PVT") {
		t.Error("unknown rank must sort after every known rank")
	}
}

// The HTML "Next" column sorts by seniority; a compound target (SPC's
// "CPL/WO1") sorts as its first rank.
func TestPromoNextRankIndex(t *testing.T) {
	if promoNextRankIndex("CPL/WO1") != promoRankIndex("CPL") {
		t.Error("compound next rank should sort as its first rank")
	}
	if promoNextRankIndex("BG") >= promoNextRankIndex("SGT") {
		t.Error("BG (senior) should sort before SGT")
	}
}

func TestPromoCandidatesSortedByRankThenName(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Bravo.B", "PVT", "101"),
		"2": promoLiteProfile("Alpha.A", "PVT", "102"),
		"3": promoLiteProfile("Sarge.S", "SGT", "103"),
		"4": promoLiteProfile("Fresh.F", "PFC", "104"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Bravo.B": promoFullProfile("Bravo.B", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
		"Alpha.A": promoFullProfile("Alpha.A", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
		"Sarge.S": promoFullProfile("Sarge.S", "SGT", "2025-01-01", "2024-01-01", "Platoon Sergeant 1/1/A"),
		"Fresh.F": promoFullProfile("Fresh.F", "PFC", "2026-01-01", "2026-01-01", "Rifleman"),
	}
	serveRosterAndProfiles(t, roster, 200, profiles)

	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	iS, iF := strings.Index(content, "Sarge.S"), strings.Index(content, "Fresh.F")
	iA, iB := strings.Index(content, "Alpha.A"), strings.Index(content, "Bravo.B")
	for name, idx := range map[string]int{"Sarge.S": iS, "Fresh.F": iF, "Alpha.A": iA, "Bravo.B": iB} {
		if idx < 0 {
			t.Fatalf("%s missing from output: %q", name, content)
		}
	}
	if iS >= iF || iF >= iA || iA >= iB {
		t.Errorf("want SGT before PFC before PVTs (alphabetical), got order S=%d F=%d A=%d B=%d in %q", iS, iF, iA, iB, content)
	}
}

// --- rank mode ---------------------------------------------------------------

func promoRankInteraction(rank string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{Name: "promo", Options: []*discordgo.ApplicationCommandInteractionDataOption{
			{Name: "rank", Type: discordgo.ApplicationCommandOptionString, Value: rank},
		}},
		Member: &discordgo.Member{User: &discordgo.User{ID: "42", Username: "tester"}},
	}}
}

// DEVCOM scope merges the HQ search and the D/DEVCOM sub-unit search,
// deduplicating a member returned by both.
func TestResolvePositionRosterDevcomMerge(t *testing.T) {
	hq := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Hq.H", "SGT", "101"),
		"2": promoLiteProfile("Both.B", "SSG", "102"), // also in the sub-unit search
	}}
	sub := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Sub.S", "CPL", "201"),
		"2": promoLiteProfile("Both.B", "SSG", "102"),
	}}
	hqBody, _ := json.Marshal(hq)
	subBody, _ := json.Marshal(sub)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/D/DEVCOM"):
			_, _ = w.Write(subBody)
		case strings.HasSuffix(r.URL.Path, "/DEVCOM"):
			_, _ = w.Write(hqBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))

	roster, err := resolvePositionRoster(t.Context(), "devcom")
	if err != nil {
		t.Fatalf("resolvePositionRoster: %v", err)
	}
	names := map[string]bool{}
	for _, p := range roster.LiteProfiles {
		names[p.User.Username] = true
	}
	for _, want := range []string{"Hq.H", "Sub.S", "Both.B"} {
		if !names[want] {
			t.Errorf("DEVCOM merge missing %s; got %v", want, names)
		}
	}
	if len(roster.LiteProfiles) != 3 {
		t.Errorf("expected 3 deduplicated members, got %d (%v)", len(roster.LiteProfiles), names)
	}
}

func TestRunPromoRankMode(t *testing.T) {
	// The active-duty roster carries a PVT (eligible), a PVT (not eligible),
	// and an eligible PFC that the rank filter must exclude.
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
		"2": promoLiteProfile("Fresh.F", "PVT", "102"),
		"3": promoLiteProfile("Other.O", "PFC", "103"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
		"Fresh.F": promoFullProfile("Fresh.F", "PVT", "2026-05-10", "2026-05-10", "Rifleman"),
		"Other.O": promoFullProfile("Other.O", "PFC", "2026-01-01", "2026-01-01", "Rifleman"),
	}
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoRankInteraction("pvt"), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "PVT members eligible") {
		t.Errorf("header should carry the canonical rank scope: %q", content)
	}
	if !strings.Contains(content, "Ready.R") {
		t.Errorf("eligible PVT missing: %q", content)
	}
	if strings.Contains(content, "Fresh.F") {
		t.Errorf("ineligible PVT should not render: %q", content)
	}
	if strings.Contains(content, "Other.O") {
		t.Errorf("PFC must be excluded by the rank filter: %q", content)
	}
}

func TestRunPromoRankModeNoHolders(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
	}}
	servePromoAPIWithRanks(t, roster, nil, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoRankInteraction("XYZ"), afsmRefDate)

	found := false
	for _, call := range f.Calls() {
		if call.Method == "Respond" && call.Response != nil && call.Response.Data != nil &&
			strings.Contains(call.Response.Data.Content, "No active-duty troopers hold rank") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected no-holders message, calls: %+v", f.Calls())
	}
}

// --- type filter -------------------------------------------------------------

// wingedCPL is a lateral-only candidate: CPL, too fresh for the standard
// ladder, no prior higher rank for §VII, but flight-qualified (Aviator Badge
// + 15-series MOS) → lateral WO1.
func wingedCPL(username string) utils.ProfileResponse {
	p := promoFullProfile(username, "CPL", "2026-05-01", "2026-05-01", "Pilot 1/C/1-7")
	p.Mos = "15A"
	p.Awards = []utils.Award{{AwardName: "Army Aviator Badge"}}
	return p
}

// promoTypeInteraction builds /promo position:ACD type:<promoType>.
func promoTypeInteraction(promoType string) *discordgo.InteractionCreate {
	i := promoInteraction("ACD", "")
	data := i.Data.(discordgo.ApplicationCommandInteractionData)
	data.Options = append(data.Options, &discordgo.ApplicationCommandInteractionDataOption{
		Name: "type", Type: discordgo.ApplicationCommandOptionString, Value: promoType,
	})
	i.Data = data
	return i
}

// servePromoTypeFixture serves a roster carrying one candidate per path:
// automatic (PVT), discretionary (SGT), §VII-only (vet CPL), lateral-only
// (winged CPL).
func servePromoTypeFixture(t *testing.T) {
	t.Helper()
	vet := viiVetProfile() // §VII-only: CPL, previously held SSG
	vet.UniformUrl = "https://7cav.us/data/roster_uniforms/0/301.jpg"
	wings := wingedCPL("Wings.W")

	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
		"2": promoLiteProfile("Sarge.S", "SGT", "102"),
		"3": {User: vet.User, Rank: vet.Rank, UniformUrl: vet.UniformUrl},
		"4": promoLiteProfile("Wings.W", "CPL", "302"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
		"Sarge.S": promoFullProfile("Sarge.S", "SGT", "2025-01-01", "2024-01-01", "Platoon Sergeant 1/1/A"),
		"Vet.V":   *vet,
		"Wings.W": wings,
	}
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())
}

func TestRunPromoTypeFilters(t *testing.T) {
	// For each path: the one candidate that must render and, implicitly, the
	// three that must not.
	cases := []struct {
		promoType string
		want      string
	}{
		{"automatic", "Ready.R"},
		{"discretionary", "Sarge.S"},
		{"vii", "Vet.V"},
		{"lateral", "Wings.W"},
	}
	all := []string{"Ready.R", "Sarge.S", "Vet.V", "Wings.W"}
	for _, tc := range cases {
		t.Run(tc.promoType, func(t *testing.T) {
			servePromoTypeFixture(t)
			f := &fakeResponder{}
			runPromo(f, promoTypeInteraction(tc.promoType), afsmRefDate)

			content := lastEditContent(f.Calls())
			if !strings.Contains(content, tc.want) {
				t.Errorf("%s candidate missing: %q", tc.promoType, content)
			}
			for _, other := range all {
				if other != tc.want && strings.Contains(content, other) {
					t.Errorf("%s must be filtered out of type:%s: %q", other, tc.promoType, content)
				}
			}
			if !strings.Contains(content, "eligible for "+promoPhrase(tc.promoType)) {
				t.Errorf("header should carry the type phrase: %q", content)
			}
		})
	}
}

func promoExcludeInteraction(excludeType string) *discordgo.InteractionCreate {
	i := promoInteraction("ACD", "")
	data := i.Data.(discordgo.ApplicationCommandInteractionData)
	data.Options = append(data.Options, &discordgo.ApplicationCommandInteractionDataOption{
		Name: "exclude", Type: discordgo.ApplicationCommandOptionString, Value: excludeType,
	})
	i.Data = data
	return i
}

// exclude:X hides the candidate whose only path is X and keeps the other three.
func TestRunPromoExcludeFilters(t *testing.T) {
	cases := []struct {
		excludeType string
		gone        string // the candidate that must disappear
	}{
		{"automatic", "Ready.R"},
		{"discretionary", "Sarge.S"},
		{"vii", "Vet.V"},
		{"lateral", "Wings.W"},
	}
	all := []string{"Ready.R", "Sarge.S", "Vet.V", "Wings.W"}
	for _, tc := range cases {
		t.Run(tc.excludeType, func(t *testing.T) {
			servePromoTypeFixture(t)
			f := &fakeResponder{}
			runPromo(f, promoExcludeInteraction(tc.excludeType), afsmRefDate)

			content := lastEditContent(f.Calls())
			if strings.Contains(content, tc.gone) {
				t.Errorf("%s should be excluded by exclude:%s: %q", tc.gone, tc.excludeType, content)
			}
			for _, other := range all {
				if other != tc.gone && !strings.Contains(content, other) {
					t.Errorf("%s should survive exclude:%s: %q", other, tc.excludeType, content)
				}
			}
			if !strings.Contains(content, "excluding "+promoTypeWord(tc.excludeType)) {
				t.Errorf("header should note the exclusion: %q", content)
			}
		})
	}
}

// A candidate eligible via BOTH standard and §VII survives exclude:vii,
// because §VII is not their only path.
func TestFilterPromoExcludingKeepsMultiPath(t *testing.T) {
	both := promoCandidate{Verdict: promoEligibility{Eligible: true, Type: "automatic"}, ViaVII: true, Vii: &viiResult{Target: &viiRank{Short: "SGT"}}}
	viiOnly := promoCandidate{Verdict: promoEligibility{Eligible: false}, ViaVII: true, Vii: &viiResult{Target: &viiRank{Short: "SGT"}}}
	got := filterPromoExcluding([]promoCandidate{both, viiOnly}, promoTypeVii)
	if len(got) != 1 || !got[0].Verdict.Eligible {
		t.Errorf("exclude:vii should keep only the standard+§VII candidate, got %+v", got)
	}
}

// type and exclude cannot be combined.
func TestRunPromoTypeAndExcludeConflict(t *testing.T) {
	i := promoTypeInteraction("automatic")
	data := i.Data.(discordgo.ApplicationCommandInteractionData)
	data.Options = append(data.Options, &discordgo.ApplicationCommandInteractionDataOption{
		Name: "exclude", Type: discordgo.ApplicationCommandOptionString, Value: "vii",
	})
	i.Data = data

	f := &fakeResponder{}
	runPromo(f, i, afsmRefDate)
	found := false
	for _, call := range f.Calls() {
		if call.Method == "Respond" && call.Response != nil && call.Response.Data != nil &&
			strings.Contains(call.Response.Data.Content, "not both") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected type+exclude conflict error, calls: %+v", f.Calls())
	}
}

// Unfiltered lists must include the lateral-only candidate with the lateral
// rendering.
func TestRunPromoLateralInUnfilteredList(t *testing.T) {
	servePromoTypeFixture(t)
	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "Wings.W") {
		t.Fatalf("lateral candidate missing from unfiltered list: %q", content)
	}
	if !strings.Contains(content, "CPL → WO1 (lateral, flight wings)") {
		t.Errorf("lateral line rendering wrong: %q", content)
	}
}

// Wings without an aviation MOS (or the reverse) is NOT lateral-eligible.
func TestLateralRequiresWingsAndAviationMos(t *testing.T) {
	badgeOnly := promoFullProfile("Badge.B", "CPL", "2026-05-01", "2026-05-01", "Rifleman")
	badgeOnly.Awards = []utils.Award{{AwardName: "Army Aviator Badge"}}

	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Badge.B", "CPL", "101"),
	}}
	servePromoAPIWithRanks(t, roster, map[string]utils.ProfileResponse{"Badge.B": badgeOnly}, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoTypeInteraction("lateral"), afsmRefDate)

	content := lastEditContent(f.Calls())
	if strings.Contains(content, "Badge.B") {
		t.Errorf("badge without aviation MOS must not be lateral-eligible: %q", content)
	}
}

// --- single-user mode --------------------------------------------------------

func TestRunPromoUserModeVerdict(t *testing.T) {
	// Not-yet-eligible PVT: verdict must show the failing TIG gate and the
	// countdown, and §VII inapplicability.
	profiles := map[string]utils.ProfileResponse{
		"Fresh.F": promoFullProfile("Fresh.F", "PVT", "2026-05-10", "2026-05-10", "Rifleman"),
	}
	servePromoAPIWithRanks(t, utils.LiteRosterResponse{}, profiles, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoUserInteraction("Fresh.F", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "not yet eligible for PFC") {
		t.Errorf("verdict header wrong: %q", content)
	}
	if !strings.Contains(content, "❌ TIG") {
		t.Errorf("failing TIG gate not shown: %q", content)
	}
	if !strings.Contains(content, "day(s) until time requirements met") {
		t.Errorf("countdown missing: %q", content)
	}
	if !strings.Contains(content, "§VII): not applicable") {
		t.Errorf("§VII verdict missing: %q", content)
	}
}

// A recognized rank with no TIG/TIS ladder entry (1SG) reads as billet-based
// advancement, NOT "topped out" (1SG can still make SGM/CSM) and not the
// bug-like "no ladder defined", and carries the shared S6 disclaimer.
func TestRunPromoUserModeNoLadderRank(t *testing.T) {
	profiles := map[string]utils.ProfileResponse{
		"Top.T": promoFullProfile("Top.T", "1SG", "2024-01-01", "2022-01-01", "First Sergeant A/ACD"),
	}
	servePromoAPIWithRanks(t, utils.LiteRosterResponse{}, profiles, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoUserInteraction("Top.T", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "billet-based") {
		t.Errorf("billet-based advancement message missing: %q", content)
	}
	if strings.Contains(content, "top of the standard ladder") {
		t.Errorf("must not claim 1SG is topped out: %q", content)
	}
	if !strings.Contains(content, "report inaccurate outputs to S6") {
		t.Errorf("shared disclaimer missing from verdict: %q", content)
	}
}

func TestRunPromoUserModeViiVeteran(t *testing.T) {
	vet := viiVetProfile()
	profiles := map[string]utils.ProfileResponse{"Vet.V": *vet}
	servePromoAPIWithRanks(t, utils.LiteRosterResponse{}, profiles, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoUserInteraction("Vet.V", ""), afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "eligible for SSG") {
		t.Errorf("§VII eligibility missing: %q", content)
	}
	if !strings.Contains(content, "held 2021-01-01 as Section Leader") {
		t.Errorf("held evidence missing: %q", content)
	}
}

func TestRunPromoUserModeUnknownUser(t *testing.T) {
	servePromoAPIWithRanks(t, utils.LiteRosterResponse{}, nil, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoUserInteraction("Nobody.N", ""), afsmRefDate)

	found := false
	for _, call := range f.Calls() {
		if call.Method == "Respond" && call.Response != nil && call.Response.Data != nil &&
			strings.Contains(call.Response.Data.Content, "No milpac found") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected not-found message, calls: %+v", f.Calls())
	}
}

// No mode option at all → the whole Active Duty roster, not an error.
func TestRunPromoDefaultsToActiveDuty(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
	}
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:   discordgo.InteractionApplicationCommand,
		Data:   discordgo.ApplicationCommandInteractionData{Name: "promo"},
		Member: &discordgo.Member{User: &discordgo.User{ID: "42", Username: "tester"}},
	}}, afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "Active duty members eligible") {
		t.Errorf("bare /promo should default to the active duty scope: %q", content)
	}
	if !strings.Contains(content, "Ready.R") {
		t.Errorf("default scan missing candidate: %q", content)
	}
}

// user is a single-trooper check and can't be combined with position/rank.
func TestRunPromoUserExclusive(t *testing.T) {
	for _, combineWith := range []string{"position", "rank"} {
		f := &fakeResponder{}
		i := promoInteraction("", "")
		data := i.Data.(discordgo.ApplicationCommandInteractionData)
		data.Options = []*discordgo.ApplicationCommandInteractionDataOption{
			{Name: "user", Type: discordgo.ApplicationCommandOptionString, Value: "Someone.S"},
			{Name: combineWith, Type: discordgo.ApplicationCommandOptionString, Value: "ACD"},
		}
		i.Data = data
		runPromo(f, i, afsmRefDate)

		found := false
		for _, call := range f.Calls() {
			if call.Method == "Respond" && call.Response != nil && call.Response.Data != nil &&
				strings.Contains(call.Response.Data.Content, "don't combine it with") {
				found = true
			}
		}
		if !found {
			t.Errorf("user + %s should error, calls: %+v", combineWith, f.Calls())
		}
	}
}

// position and rank combine: position scopes the roster, rank narrows it.
func TestRunPromoPositionAndRankCombine(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
		"2": promoLiteProfile("Other.O", "PFC", "103"),
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
		"Other.O": promoFullProfile("Other.O", "PFC", "2026-01-01", "2026-01-01", "Rifleman"),
	}
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())

	i := promoInteraction("ACD", "")
	data := i.Data.(discordgo.ApplicationCommandInteractionData)
	data.Options = append(data.Options, &discordgo.ApplicationCommandInteractionDataOption{
		Name: "rank", Type: discordgo.ApplicationCommandOptionString, Value: "PVT",
	})
	i.Data = data

	f := &fakeResponder{}
	runPromo(f, i, afsmRefDate)

	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "ACD PVT members eligible") {
		t.Errorf("header should combine position and rank: %q", content)
	}
	if !strings.Contains(content, "Ready.R") {
		t.Errorf("PVT in ACD should be listed: %q", content)
	}
	if strings.Contains(content, "Other.O") {
		t.Errorf("PFC must be filtered out by rank:PVT: %q", content)
	}
}

func promoUserInteraction(username, asOf string) *discordgo.InteractionCreate {
	opts := []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "user", Type: discordgo.ApplicationCommandOptionString, Value: username},
	}
	if asOf != "" {
		opts = append(opts, &discordgo.ApplicationCommandInteractionDataOption{
			Name: "as_of", Type: discordgo.ApplicationCommandOptionString, Value: asOf,
		})
	}
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:   discordgo.InteractionApplicationCommand,
		Data:   discordgo.ApplicationCommandInteractionData{Name: "promo", Options: opts},
		Member: &discordgo.Member{User: &discordgo.User{ID: "42", Username: "tester"}},
	}}
}

// --- file report -------------------------------------------------------------

func TestRunPromoForceFileOutput(t *testing.T) {
	vet := viiVetProfile()
	vet.UniformUrl = "https://7cav.us/data/roster_uniforms/0/301.jpg"
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"1": promoLiteProfile("Ready.R", "PVT", "101"),
		"2": {User: vet.User, Rank: vet.Rank, UniformUrl: vet.UniformUrl},
	}}
	profiles := map[string]utils.ProfileResponse{
		"Ready.R": promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman"),
		"Vet.V":   *vet,
	}
	servePromoAPIWithRanks(t, roster, profiles, viiTestRanks())

	i := promoInteraction("ACD", "")
	data := i.Data.(discordgo.ApplicationCommandInteractionData)
	data.Options = append(data.Options, &discordgo.ApplicationCommandInteractionDataOption{
		Name: "force_file_output", Type: discordgo.ApplicationCommandOptionBoolean, Value: true,
	})
	i.Data = data

	f := &fakeResponder{}
	runPromo(f, i, afsmRefDate)

	csvBody, htmlBody := lastAttachments(t, f)
	if !strings.Contains(htmlBody, "<table") || !strings.Contains(htmlBody, "eligibility") {
		t.Errorf("HTML report structure missing: %q", htmlBody)
	}
	if !strings.Contains(htmlBody, "Ready.R") || !strings.Contains(htmlBody, "Vet.V") {
		t.Errorf("HTML report missing candidates: %q", htmlBody)
	}
	if !strings.Contains(htmlBody, "§VII") {
		t.Errorf("HTML report missing §VII path data: %q", htmlBody)
	}
	if !strings.Contains(csvBody, "Ready.R") || !strings.Contains(csvBody, "Vet.V") {
		t.Errorf("CSV report missing candidates: %q", csvBody)
	}
	// A short list forced to file still renders the inline list too.
	content := lastEditContent(f.Calls())
	if !strings.Contains(content, "Ready.R") {
		t.Errorf("forced file should still show the inline list: %q", content)
	}
	// Primary billet now appears in both reports.
	if !strings.Contains(csvBody, "primary_billet") || !strings.Contains(htmlBody, "Billet") {
		t.Errorf("billet column missing from reports")
	}
}

// Milpac text is user-entered and must not break out of the report markup.
func TestPromoReportEscapesMilpacData(t *testing.T) {
	scan := promoScan{ViiActive: true, Candidates: []promoCandidate{{
		Username:  `Evil<script>alert("x")</script>`,
		MilpacURL: "https://7cav.us/rosters/profile/1",
		RankShort: "PVT",
		Verdict:   promoEligibility{Eligible: true, NextRank: "PFC", Type: "automatic"},
	}}}
	file := promoReportFile("ACD", promoFilter{}, mustParseDate("2026-05-15"), scan)
	var sb strings.Builder
	if _, err := io.Copy(&sb, file.Reader); err != nil {
		t.Fatalf("read report: %v", err)
	}
	// The report carries its own sort/filter <script> block; what must never
	// appear is the injected payload unescaped.
	if strings.Contains(sb.String(), "<script>alert") {
		t.Errorf("unescaped injected markup reached the report: %q", sb.String())
	}
	if !strings.Contains(sb.String(), "&lt;script&gt;") {
		t.Errorf("expected escaped markup: %q", sb.String())
	}
}

// lastAttachments returns the CSV and HTML report bodies from the last Edit
// call: /promo attaches both when a list ships as files.
func lastAttachments(t *testing.T, f *fakeResponder) (csvBody, htmlBody string) {
	t.Helper()
	var edit *discordgo.WebhookEdit
	for _, call := range f.Calls() {
		if call.Method == "Edit" && call.Edit != nil {
			edit = call.Edit
		}
	}
	if edit == nil || len(edit.Files) != 2 {
		t.Fatalf("expected CSV + HTML attachments, calls: %+v", f.Calls())
	}
	for _, fl := range edit.Files {
		var sb strings.Builder
		if _, err := io.Copy(&sb, fl.Reader); err != nil {
			t.Fatalf("read attachment %s: %v", fl.Name, err)
		}
		switch {
		case strings.HasSuffix(fl.Name, ".csv"):
			csvBody = sb.String()
		case strings.HasSuffix(fl.Name, ".html"):
			htmlBody = sb.String()
		default:
			t.Fatalf("unexpected attachment %q", fl.Name)
		}
	}
	if csvBody == "" || htmlBody == "" {
		t.Fatalf("missing csv or html attachment; files: %+v", edit.Files)
	}
	return csvBody, htmlBody
}
