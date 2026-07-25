package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// promoRefDate pins "as of" for engine tests; same rationale as afsmRefDate.
var promoRefDate = mustParseDate("2026-05-15")

func rec(details string) utils.Record {
	return utils.Record{RecordDetails: details, RecordType: "RECORD_TYPE_GRADUATION", RecordDate: "2025-01-01"}
}

func TestParseCourseCompletions(t *testing.T) {
	cases := []struct {
		name    string
		details []string
		want    courseCompletions
	}{
		{"ncoa phase 1 long form", []string{"Graduated NCOA Warrior Leadership Course Phase I"}, courseCompletions{NcoaPhase1: true}},
		{"ncoa wlc shorthand", []string{"Graduated NCOA WLC - 01*01-14"}, courseCompletions{NcoaPhase1: true}},
		{"ncoa phase ii not misread as phase i", []string{"Graduated NCOA Phase II"}, courseCompletions{NcoaPhase2: true}},
		{"ncoa phase 2 numeric", []string{"Graduated NCOA Phase 2"}, courseCompletions{NcoaPhase2: true}},
		{"sac", []string{"Attended the Server Administration Course"}, courseCompletions{Sac: true}},
		{"ods long form", []string{"Graduated Officer Development School"}, courseCompletions{Ods: true}},
		{"ods abbreviation with leading space", []string{"Graduated ODS class 24-01"}, courseCompletions{Ods: true}},
		{"rdptc", []string{"Graduated Regimental Disciplinary Process Training Course"}, courseCompletions{Rdptc: true}},
		{"empty records", nil, courseCompletions{}},
		{"unrelated record", []string{"Promoted to SGT"}, courseCompletions{}},
		{
			"all courses across records",
			[]string{
				"Graduated NCOA Phase I",
				"Graduated NCOA Phase II",
				"Attended the Server Administration Course",
				"Graduated ODS",
				"Graduated RDPTC",
			},
			courseCompletions{NcoaPhase1: true, NcoaPhase2: true, Sac: true, Ods: true, Rdptc: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := make([]utils.Record, 0, len(tc.details))
			for _, d := range tc.details {
				records = append(records, rec(d))
			}
			got := parseCourseCompletions(records)
			if got != tc.want {
				t.Errorf("parseCourseCompletions() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCourseCompletionsHas(t *testing.T) {
	c := courseCompletions{NcoaPhase1: true, Sac: true, Rdptc: true}
	for key, want := range map[string]bool{
		"ncoaPhase1": true, "ncoaPhase2": false, "sac": true,
		"ods": false, "rdptc": true, "bogus": false,
	} {
		if got := c.has(key); got != want {
			t.Errorf("has(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestDetectBillet(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"PSG 1/A/ACD", "PSG"},
		{"Platoon Sergeant 1/A/ACD", "PSG"},
		{"First Sergeant A/ACD", "1SG"},
		{"ASL 1/1/A/ACD", "ASL"},
		// substring overlap: "assistant section leader" must not fall
		// through to SL
		{"Assistant Section Leader 1/1/A", "ASL"},
		{"Section Leader 1/1/A", "SL"},
		{"SL 2/1/B", "SL"},
		{"Platoon Leader 1/A", "PL"},
		// "platoon commander" must resolve PL before the bare "commander"
		// check resolves CO
		{"Platoon Commander 1/A", "PL"},
		{"Executive Officer A/ACD", "XO"},
		{"XO A/ACD", "XO"},
		{"Commanding Officer ACD", "CO"},
		{"S1 Clerk", "DEPT"},
		{"NCOA Instructor", "DEPT"},
		{"RRD Recruiter", "DEPT"},
		{"Fire Team Leader 1/1/1/A", "FTL"},
		{"RTO 1/1/A", "RTO"},
		{"Rifleman 1/1/1/A", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			if got := detectBillet(tc.title); got != tc.want {
				t.Errorf("detectBillet(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestBilletMeetsRequirement(t *testing.T) {
	cases := []struct {
		name     string
		detected string
		required []string
		want     bool
	}{
		{"no requirement always passes", "", nil, true},
		{"no requirement passes with billet", "PSG", nil, true},
		{"required and missing fails", "", []string{"SL", "PSG"}, false},
		{"required and matching passes", "PSG", []string{"SL", "PSG"}, true},
		{"required and non-matching fails", "FTL", []string{"SL", "PSG"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := billetMeetsRequirement(tc.detected, tc.required); got != tc.want {
				t.Errorf("billetMeetsRequirement(%q, %v) = %v, want %v", tc.detected, tc.required, got, tc.want)
			}
		})
	}
}

func TestDaysBetween(t *testing.T) {
	cases := []struct {
		name string
		date string
		want int
	}{
		{"empty", "", -1},
		{"zero string", "0", -1},
		{"zero-value date sentinel", "0001-01-01", -1},
		{"garbage", "not-a-date", -1},
		{"pre-2000 treated as invalid", "1999-12-31", -1},
		{"one month back", "2026-04-15", 30},
		{"same day", "2026-05-15", 0},
		{"future date is negative", "2026-06-15", -31},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := daysBetween(tc.date, promoRefDate); got != tc.want {
				t.Errorf("daysBetween(%q) = %d, want %d", tc.date, got, tc.want)
			}
		})
	}
}

func TestCalculatePromotionEligibility(t *testing.T) {
	allCourses := courseCompletions{NcoaPhase1: true, NcoaPhase2: true, Sac: true, Ods: true, Rdptc: true}

	cases := []struct {
		name          string
		rank          string
		promotionDate string
		joinDate      string
		courses       courseCompletions
		position      string
		mos           string
		wantEligible  bool
		wantNoReq     bool
		wantNext      string
		check         func(t *testing.T, v promoEligibility)
	}{
		{
			name: "PVT with 31 days TIG eligible (automatic, no courses, no billet)",
			rank: "PVT", promotionDate: "2026-04-10", joinDate: "2026-04-10",
			position: "Rifleman 1/1/1/A", wantEligible: true, wantNext: "PFC",
			check: func(t *testing.T, v promoEligibility) {
				if v.Type != "automatic" {
					t.Errorf("Type = %q, want automatic", v.Type)
				}
			},
		},
		{
			name: "PVT with 20 days TIG not eligible, projects days until",
			rank: "PVT", promotionDate: "2026-04-25", joinDate: "2026-04-25",
			position: "Rifleman", wantEligible: false, wantNext: "PFC",
			check: func(t *testing.T, v promoEligibility) {
				if v.TigMet {
					t.Error("TigMet = true, want false")
				}
				if v.DaysUntilEligible != 10 {
					t.Errorf("DaysUntilEligible = %d, want 10", v.DaysUntilEligible)
				}
			},
		},
		{
			name: "SPC blocked by missing NCOA courses despite TIG",
			rank: "SPC", promotionDate: "2025-11-01", joinDate: "2025-06-01",
			courses:  courseCompletions{Sac: true},
			position: "Rifleman", wantEligible: false, wantNext: "CPL/WO1",
			check: func(t *testing.T, v promoEligibility) {
				if v.CoursesMet {
					t.Error("CoursesMet = true, want false")
				}
				if len(v.MissingCourses) != 2 {
					t.Errorf("MissingCourses = %v, want ncoaPhase1+ncoaPhase2", v.MissingCourses)
				}
			},
		},
		{
			name: "SPC with all courses and TIG eligible",
			rank: "SPC", promotionDate: "2025-11-01", joinDate: "2025-06-01",
			courses:  allCourses,
			position: "Rifleman", wantEligible: true, wantNext: "CPL/WO1",
		},
		{
			name: "CPL in SL billet with TIG+TIS eligible; no pending-course noise (ODS dropped per S1)",
			rank: "CPL", promotionDate: "2025-12-01", joinDate: "2025-05-01",
			position: "Section Leader 1/1/A", wantEligible: true, wantNext: "SGT",
			check: func(t *testing.T, v promoEligibility) {
				if len(v.PendingDisplayCourses) != 0 {
					t.Errorf("PendingDisplayCourses = %v, want none (ODS is a commissioning concern, not /promo's)", v.PendingDisplayCourses)
				}
			},
		},
		{
			name: "CPL without leadership billet blocked",
			rank: "CPL", promotionDate: "2025-12-01", joinDate: "2025-05-01",
			position: "Rifleman 1/1/1/A", wantEligible: false, wantNext: "SGT",
			check: func(t *testing.T, v promoEligibility) {
				if v.BilletMet {
					t.Error("BilletMet = true, want false")
				}
			},
		},
		{
			name: "CPL TIG met but TIS short blocks",
			rank: "CPL", promotionDate: "2025-12-01", joinDate: "2025-12-01",
			position: "Section Leader 1/1/A", wantEligible: false, wantNext: "SGT",
			check: func(t *testing.T, v promoEligibility) {
				if !v.TigMet {
					t.Error("TigMet = false, want true")
				}
				if v.TisMet {
					t.Error("TisMet = true, want false")
				}
				// TIS required 270d, ~165d served as of ref date → ~105 left,
				// and TIS is the binding constraint.
				if v.DaysUntilEligible <= 0 {
					t.Errorf("DaysUntilEligible = %d, want > 0", v.DaysUntilEligible)
				}
			},
		},
		{
			name: "1LT in PL billet blocked (PL caps at 1LT, CPT needs XO/CO/DEPT)",
			rank: "1LT", promotionDate: "2025-09-01", joinDate: "2023-01-01",
			position: "Platoon Leader 1/A", wantEligible: false, wantNext: "CPT",
		},
		{
			name: "2LT in PL billet eligible for 1LT",
			rank: "2LT", promotionDate: "2025-09-01", joinDate: "2024-01-01",
			position: "Platoon Leader 1/A", wantEligible: true, wantNext: "1LT",
		},
		{
			name: "CPT without RDPTC blocked despite billet",
			rank: "CPT", promotionDate: "2025-09-01", joinDate: "2022-01-01",
			position: "Commanding Officer ACD", wantEligible: false, wantNext: "MAJ",
		},
		{
			name: "CPT with RDPTC in CO billet eligible",
			rank: "CPT", promotionDate: "2025-09-01", joinDate: "2022-01-01",
			courses:  allCourses,
			position: "Commanding Officer ACD", wantEligible: true, wantNext: "MAJ",
		},
		{
			name: "COL without a regiment-HQ MOS is not eligible for BG",
			rank: "COL", promotionDate: "2020-01-01", joinDate: "2015-01-01",
			position: "Regimental Commander", mos: "11A", wantEligible: false, wantNext: "BG",
			check: func(t *testing.T, v promoEligibility) {
				if v.MosMet {
					t.Error("MosMet = true, want false (11A is not a regiment-HQ MOS)")
				}
			},
		},
		{
			name: "COL with a regiment-HQ MOS is eligible for BG",
			rank: "COL", promotionDate: "2020-01-01", joinDate: "2015-01-01",
			position: "Regimental Commander", mos: "00B", wantEligible: true, wantNext: "BG",
			check: func(t *testing.T, v promoEligibility) {
				if !v.MosMet {
					t.Error("MosMet = false, want true (00B is a regiment-HQ MOS)")
				}
			},
		},
		{
			name: "COL with the MOS but short of nine months TIG",
			rank: "COL", promotionDate: "2026-01-01", joinDate: "2015-01-01",
			position: "Regimental Commander", mos: "00B", wantEligible: false, wantNext: "BG",
			check: func(t *testing.T, v promoEligibility) {
				if !v.MosMet {
					t.Error("MosMet = false, want true")
				}
				if v.TigMet {
					t.Error("TigMet = true, want false (R-023 Ch.2 §III sets nine months at COL)")
				}
				if v.TigRequired != 9*30 {
					t.Errorf("TigRequired = %d, want %d", v.TigRequired, 9*30)
				}
			},
		},
		{
			// 00D is Officer Pool, not Regimental Staff: a pool officer is not
			// in a regiment-HQ billet, so 00D does NOT gate COL→BG.
			name: "COL on 00D (Officer Pool) is not BG-eligible",
			rank: "COL", promotionDate: "2015-01-01", joinDate: "2010-01-01",
			position: "Officer Pool", mos: "00D", wantEligible: false, wantNext: "BG",
			check: func(t *testing.T, v promoEligibility) {
				if v.MosMet {
					t.Error("MosMet = true, want false (00D is Officer Pool, not Regimental Staff)")
				}
			},
		},
		{
			name: "COL on 00Z (regimental staff) with 9mo TIG is BG-eligible",
			rank: "COL", promotionDate: "2015-01-01", joinDate: "2010-01-01",
			position: "Regimental Command Sergeant Major", mos: "00Z", wantEligible: true, wantNext: "BG",
			check: func(t *testing.T, v promoEligibility) {
				if !v.MosMet {
					t.Error("MosMet = false, want true (00Z is Regimental Staff)")
				}
			},
		},
		{
			name: "missing promotion date never eligible",
			rank: "PVT", promotionDate: "", joinDate: "2026-01-01",
			position: "Rifleman", wantEligible: false, wantNext: "PFC",
			check: func(t *testing.T, v promoEligibility) {
				if v.TigDays != -1 {
					t.Errorf("TigDays = %d, want -1", v.TigDays)
				}
				// Unknowable dates must not project a countdown.
				if v.DaysUntilEligible != 0 {
					t.Errorf("DaysUntilEligible = %d, want 0", v.DaysUntilEligible)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := calculatePromotionEligibility(tc.rank, tc.promotionDate, tc.joinDate, tc.courses, tc.position, tc.mos, promoRefDate)
			if v.Eligible != tc.wantEligible {
				t.Errorf("Eligible = %v, want %v (verdict %+v)", v.Eligible, tc.wantEligible, v)
			}
			if v.NoRequirements != tc.wantNoReq {
				t.Errorf("NoRequirements = %v, want %v", v.NoRequirements, tc.wantNoReq)
			}
			if v.NextRank != tc.wantNext {
				t.Errorf("NextRank = %q, want %q", v.NextRank, tc.wantNext)
			}
			if tc.check != nil {
				tc.check(t, v)
			}
		})
	}
}

// TestEligibilityAsOfDateShifts verifies the issue #4 date filter: a trooper
// short on TIG today becomes eligible when asOf moves past the gate.
func TestEligibilityAsOfDateShifts(t *testing.T) {
	// Promoted 2026-04-25: 20d TIG at ref date, needs 30d.
	now := calculatePromotionEligibility("PVT", "2026-04-25", "2026-04-25", courseCompletions{}, "Rifleman", "", promoRefDate)
	if now.Eligible {
		t.Fatal("expected not eligible at ref date")
	}
	future := calculatePromotionEligibility("PVT", "2026-04-25", "2026-04-25", courseCompletions{}, "Rifleman", "", mustParseDate("2026-05-25"))
	if !future.Eligible {
		t.Fatal("expected eligible when asOf is past the TIG gate")
	}
}

// promoLiteFull builds a lite roster entry with the fields the API actually
// populates. The shared promoLiteProfile helper deliberately leaves them
// blank, which exercises the pre-filter's "cannot tell, so fetch" path
// instead; both shapes matter, so the tests use each on purpose.
func promoLiteFull(username, rankShort, promotionDate, joinDate, position string) utils.LiteProfileResponse {
	return utils.LiteProfileResponse{
		User:          utils.User{UserID: "u-" + username, Username: username},
		Rank:          utils.Rank{RankShort: rankShort},
		UniformUrl:    "https://7cav.us/data/roster_uniforms/0/101.jpg",
		Primary:       utils.Position{PositionTitle: position},
		JoinDate:      joinDate,
		PromotionDate: promotionDate,
	}
}

// servePromoAPICountingProfiles is servePromoAPIWithRanks with a counter on
// the expensive endpoint, so tests can assert on fetches avoided rather than
// only on output.
func servePromoAPICountingProfiles(
	t *testing.T,
	roster utils.LiteRosterResponse,
	profilesByUsername map[string]utils.ProfileResponse,
	ranks *utils.RanksResponse,
) *atomic.Int64 {
	t.Helper()
	rosterBody, err := json.Marshal(roster)
	if err != nil {
		t.Fatalf("marshal roster: %v", err)
	}
	ranksBody, err := json.Marshal(ranks)
	if err != nil {
		t.Fatalf("marshal ranks: %v", err)
	}
	encoded := make(map[string][]byte, len(profilesByUsername))
	for name, p := range profilesByUsername {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal profile %q: %v", name, err)
		}
		encoded[name] = b
	}

	var hits atomic.Int64
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
			hits.Add(1)
			name := strings.TrimPrefix(r.URL.Path, "/milpacs/profile/username/")
			body, ok := encoded[name]
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
	return &hits
}

func TestPromoNeedsProfileUnfiltered(t *testing.T) {
	m := viiModel()
	cases := []struct {
		name   string
		member utils.LiteProfileResponse
		want   bool
		why    string
	}{
		{
			name:   "junior enlisted below the billet ceiling still needs fetching",
			member: promoLiteFull("Fresh.F", "PVT", "2026-05-10", "2026-05-10", "Rifleman 1/1/1/A"),
			want:   true,
			why:    "TIG is short, but MEMBER rates CPL (above PVT), so §VII could restore a previously held rank",
		},
		{
			name:   "PVT past TIG",
			member: promoLiteFull("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman 1/1/1/A"),
			want:   true,
			why:    "the standard ladder could complete once courses are known",
		},
		{
			name:   "NCO fetched for the lateral path",
			member: promoLiteFull("Nco.N", "CPL", "2026-05-14", "2026-05-14", "Rifleman 1/1/1/A"),
			want:   true,
			why:    "wings and MOS live on the full profile, so every rank with a warrant equivalent is opened",
		},
		{
			name:   "at the billet ceiling with no other path",
			member: promoLiteFull("Top.T", "MSG", "2026-05-14", "2020-01-01", "Platoon Sergeant 1/A"),
			want:   true,
			why:    "MSG has a warrant equivalent (CW5), so the lateral path keeps it in",
		},
		{
			name:   "missing promotion date defers to the profile",
			member: promoLiteFull("Blank.B", "PVT", "", "2026-01-01", "Rifleman"),
			want:   true,
			why:    "a blank date is a data problem, not evidence of ineligibility",
		},
		{
			name:   "missing position defers to the profile",
			member: promoLiteFull("NoPos.N", "PVT", "2026-05-14", "2026-05-14", ""),
			want:   true,
			why:    "billet unknown, so neither the ladder nor the ceiling can be judged",
		},
		{
			name:   "unrecognised rank defers to the profile",
			member: promoLiteFull("Odd.O", "XYZ", "2026-05-14", "2026-05-14", "Rifleman"),
			want:   true,
			why:    "no ladder and no rank-model entry; do not guess",
		},
		{
			name:   "skeletal lite record defers to the profile",
			member: promoLiteProfile("Sparse.S", "PVT", "101"),
			want:   true,
			why:    "nothing to judge on",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (promoFilter{}).needsProfile(tc.member, promoRefDate, m); got != tc.want {
				t.Errorf("promoNeedsProfile() = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

func TestPromoNeedsProfileByType(t *testing.T) {
	m := viiModel()
	freshPVT := promoLiteFull("Fresh.F", "PVT", "2026-05-10", "2026-05-10", "Rifleman 1/1/1/A")
	readyPVT := promoLiteFull("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman 1/1/1/A")
	readySL := promoLiteFull("Lead.L", "SGT", "2024-06-01", "2023-01-01", "Section Leader 1/1/A")
	specSL := promoLiteFull("Spec.S", "SPC", "2026-05-14", "2026-01-01", "Section Leader 1/1/A")
	topPSG := promoLiteFull("Top.T", "MSG", "2026-05-14", "2020-01-01", "Platoon Sergeant 1/A")

	cases := []struct {
		promoType string
		member    utils.LiteProfileResponse
		want      bool
		why       string
	}{
		{promoTypeAutomatic, readyPVT, true, "PVT's rung is automatic and TIG is met"},
		{promoTypeAutomatic, freshPVT, false, "automatic rung, but TIG cannot be met by any course record"},
		{promoTypeAutomatic, readySL, false, "SGT's rung is discretionary, so it can never satisfy an automatic filter"},
		{promoTypeDiscretionary, readySL, true, "discretionary rung in a rating billet with time served"},
		{promoTypeDiscretionary, readyPVT, false, "PVT's rung is automatic"},
		{promoTypeLateral, readySL, true, "SGT has a warrant equivalent"},
		{promoTypeLateral, readyPVT, false, "no warrant equivalent below CPL"},
		{promoTypeVii, specSL, true, "SL rates SSG, well above SPC"},
		{promoTypeVii, topPSG, false, "MSG already sits at the PSG ceiling, so nothing can be restored"},
		{promoTypeVii, readyPVT, true, "MEMBER rates CPL, above PVT"},
		// Regression: a blank position is not a member billet. Reading it as
		// one ruled a returning veteran out of a type:vii run before their
		// real billet could be seen.
		{promoTypeVii, promoLiteFull("NoPos.N", "CPL", "2026-05-14", "2020-01-01", ""), true,
			"billet unknown, so the ceiling cannot be judged"},
		{promoTypeVii, promoLiteProfile("Sparse.S", "CPL", "101"), true,
			"skeletal lite record carries no billet"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%s", tc.promoType, tc.member.User.Username), func(t *testing.T) {
			if got := (promoFilter{include: tc.promoType}).needsProfile(tc.member, promoRefDate, m); got != tc.want {
				t.Errorf("promoNeedsProfile(type=%s) = %v, want %v (%s)", tc.promoType, got, tc.want, tc.why)
			}
		})
	}
}

func TestViiPathPossibleWithoutRankModel(t *testing.T) {
	// A failed ranks fetch disables §VII for the pass, so it cannot be a
	// reason to open anybody's record.
	member := promoLiteFull("Spec.S", "SPC", "2026-05-14", "2026-01-01", "Section Leader 1/1/A")
	if viiPathPossible(member, nil) {
		t.Error("viiPathPossible with a nil model = true, want false")
	}
	if (promoFilter{}).needsProfile(member, promoRefDate, nil) {
		t.Error("SPC short on TIG with §VII disabled should not be fetched")
	}
}

// The invariant that matters: anyone the pre-filter drops must be someone
// evaluatePromoMember would have returned nil for anyway. This runs the real
// evaluator against a served profile for every dropped fixture.
func TestPreFilterNeverDropsACandidate(t *testing.T) {
	m := viiModel()
	fixtures := []struct {
		lite utils.LiteProfileResponse
		full utils.ProfileResponse
	}{
		{promoLiteFull("Fresh.F", "PVT", "2026-05-10", "2026-05-10", "Rifleman 1/1/1/A"),
			promoFullProfile("Fresh.F", "PVT", "2026-05-10", "2026-05-10", "Rifleman 1/1/1/A")},
		{promoLiteFull("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman 1/1/1/A"),
			promoFullProfile("Ready.R", "PVT", "2026-04-01", "2026-04-01", "Rifleman 1/1/1/A")},
		{promoLiteFull("Spec.S", "SPC", "2026-05-14", "2026-01-01", "Rifleman 1/1/1/A"),
			promoFullProfile("Spec.S", "SPC", "2026-05-14", "2026-01-01", "Rifleman 1/1/1/A")},
		{promoLiteFull("Course.C", "SPC", "2025-11-01", "2025-01-01", "Rifleman 1/1/1/A"),
			promoFullProfile("Course.C", "SPC", "2025-11-01", "2025-01-01", "Rifleman 1/1/1/A",
				"Graduated NCOA Phase I", "Graduated NCOA Phase II", "Attended the Server Administration Course")},
		{promoLiteFull("Top.T", "MSG", "2026-05-14", "2020-01-01", "Platoon Sergeant 1/A"),
			promoFullProfile("Top.T", "MSG", "2026-05-14", "2020-01-01", "Platoon Sergeant 1/A")},
		{promoLiteFull("Lead.L", "SGT", "2024-06-01", "2023-01-01", "Section Leader 1/1/A"),
			promoFullProfile("Lead.L", "SGT", "2024-06-01", "2023-01-01", "Section Leader 1/1/A")},
	}

	profiles := make(map[string]utils.ProfileResponse, len(fixtures))
	for _, f := range fixtures {
		profiles[f.full.User.Username] = f.full
	}
	servePromoAPIWithRanks(t, utils.LiteRosterResponse{}, profiles, viiTestRanks())

	for _, promoType := range []string{"", promoTypeAutomatic, promoTypeDiscretionary, promoTypeVii, promoTypeLateral} {
		for _, f := range fixtures {
			name := f.lite.User.Username
			if (promoFilter{include: promoType}).needsProfile(f.lite, promoRefDate, m) {
				continue // fetched anyway; nothing to prove
			}
			got, err := evaluatePromoMember(t.Context(), f.lite, promoRefDate, m)
			if err != nil {
				t.Fatalf("evaluatePromoMember(%s): %v", name, err)
			}
			if got == nil {
				continue // correctly dropped
			}
			// Dropped, but the evaluator produced a candidate: only allowed
			// if the type filter would have discarded it after the fetch.
			if len(filterPromoCandidates([]promoCandidate{*got}, promoType)) > 0 {
				t.Errorf("type=%q dropped %s before fetching, but it survives the post-fetch filter: %+v",
					promoType, name, got)
			}
		}
	}
}

// End to end: a scope filtered to discretionary promotions must not open the
// records of ranks whose next rung is automatic.
func TestRunPromoPreFilterSkipsFetches(t *testing.T) {
	lite := map[string]utils.LiteProfileResponse{}
	profiles := map[string]utils.ProfileResponse{}
	for idx := 0; idx < 10; idx++ {
		name := fmt.Sprintf("Junior.%02d", idx)
		lite[name] = promoLiteFull(name, "PFC", "2026-01-01", "2025-06-01", "Rifleman 1/1/1/A")
		profiles[name] = promoFullProfile(name, "PFC", "2026-01-01", "2025-06-01", "Rifleman 1/1/1/A")
	}
	// One discretionary-rung NCO in a rating billet.
	lite["Lead.L"] = promoLiteFull("Lead.L", "SGT", "2024-06-01", "2023-01-01", "Section Leader 1/1/A")
	profiles["Lead.L"] = promoFullProfile("Lead.L", "SGT", "2024-06-01", "2023-01-01", "Section Leader 1/1/A")

	hits := servePromoAPICountingProfiles(t, utils.LiteRosterResponse{LiteProfiles: lite}, profiles, viiTestRanks())

	i := promoInteraction("ACD", "")
	data := i.Data.(discordgo.ApplicationCommandInteractionData)
	data.Options = append(data.Options, &discordgo.ApplicationCommandInteractionDataOption{
		Name: "type", Type: discordgo.ApplicationCommandOptionString, Value: promoTypeDiscretionary,
	})
	i.Data = data

	f := &fakeResponder{}
	runPromo(f, i, afsmRefDate)

	if got := hits.Load(); got != 1 {
		t.Errorf("profile fetches = %d, want 1 (the ten automatic-rung PFCs should never be opened)", got)
	}
	if !strings.Contains(lastEditContent(f.Calls()), "Lead.L") {
		t.Errorf("the discretionary candidate is missing from the output: %q", lastEditContent(f.Calls()))
	}
}

// The unfiltered path keeps every route open, so it must still fetch the
// members a typed run would skip. Guards against the pre-filter quietly
// over-reaching into the default case.
func TestRunPromoUnfilteredStillFetchesBroadly(t *testing.T) {
	lite := map[string]utils.LiteProfileResponse{}
	profiles := map[string]utils.ProfileResponse{}
	for idx := 0; idx < 5; idx++ {
		name := fmt.Sprintf("Junior.%02d", idx)
		lite[name] = promoLiteFull(name, "PFC", "2026-05-14", "2026-05-14", "Rifleman 1/1/1/A")
		profiles[name] = promoFullProfile(name, "PFC", "2026-05-14", "2026-05-14", "Rifleman 1/1/1/A")
	}
	hits := servePromoAPICountingProfiles(t, utils.LiteRosterResponse{LiteProfiles: lite}, profiles, viiTestRanks())

	f := &fakeResponder{}
	runPromo(f, promoInteraction("ACD", ""), afsmRefDate)

	if got := hits.Load(); got != 5 {
		t.Errorf("profile fetches = %d, want 5 (§VII keeps sub-ceiling ranks in play when unfiltered)", got)
	}
}
