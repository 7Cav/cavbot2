package commands

import (
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
)

// viiTestRanks builds a rank fixture with display orders consistent with the
// production spacing viiTrackOf is tuned to: officers <=110, warrants
// 111-140, enlisted >140.
func viiTestRanks() *utils.RanksResponse {
	return &utils.RanksResponse{Ranks: []utils.RankInfo{
		{RankShort: "COL", RankFull: "Colonel", RankDisplayOrder: 60},
		{RankShort: "LTC", RankFull: "Lieutenant Colonel", RankDisplayOrder: 70},
		{RankShort: "MAJ", RankFull: "Major", RankDisplayOrder: 80},
		{RankShort: "CPT", RankFull: "Captain", RankDisplayOrder: 90},
		{RankShort: "1LT", RankFull: "First Lieutenant", RankDisplayOrder: 100},
		{RankShort: "2LT", RankFull: "Second Lieutenant", RankDisplayOrder: 110},
		{RankShort: "CW5", RankFull: "Chief Warrant Officer 5", RankDisplayOrder: 115},
		{RankShort: "CW4", RankFull: "Chief Warrant Officer 4", RankDisplayOrder: 120},
		{RankShort: "CW3", RankFull: "Chief Warrant Officer 3", RankDisplayOrder: 125},
		{RankShort: "CW2", RankFull: "Chief Warrant Officer 2", RankDisplayOrder: 130},
		{RankShort: "WO1", RankFull: "Warrant Officer 1", RankDisplayOrder: 135},
		{RankShort: "MSG", RankFull: "Master Sergeant", RankDisplayOrder: 150},
		{RankShort: "SFC", RankFull: "Sergeant First Class", RankDisplayOrder: 160},
		{RankShort: "SSG", RankFull: "Staff Sergeant", RankDisplayOrder: 170},
		{RankShort: "SGT", RankFull: "Sergeant", RankDisplayOrder: 180},
		{RankShort: "CPL", RankFull: "Corporal", RankDisplayOrder: 190},
		{RankShort: "SPC", RankFull: "Specialist", RankDisplayOrder: 195},
		{RankShort: "PFC", RankFull: "Private First Class", RankDisplayOrder: 200},
		{RankShort: "PVT", RankFull: "Private", RankDisplayOrder: 210},
		{RankShort: "Tester", RankFull: "Tester", RankDisplayOrder: 999},
	}}
}

func viiModel() *viiRankModel { return buildRankModel(viiTestRanks()) }

func viiRec(date, details string) utils.Record {
	return utils.Record{RecordDate: date, RecordDetails: details, RecordType: "RECORD_TYPE_SERVICE"}
}

func TestBuildRankModel(t *testing.T) {
	m := viiModel()
	if _, ok := m.byShort["Tester"]; ok {
		t.Error("Tester rank should be filtered")
	}
	if r := m.resolveName("Staff Sergeant"); r == nil || r.Short != "SSG" {
		t.Errorf("resolveName(Staff Sergeant) = %+v", r)
	}
	// Longest-suffix matching: "Sergeant First Class" must not resolve as
	// bare "Sergeant".
	if r := m.resolveName("Sergeant First Class"); r == nil || r.Short != "SFC" {
		t.Errorf("resolveName(Sergeant First Class) = %+v", r)
	}
	// Alias resolution.
	if r := m.resolveName("1st Lieutenant"); r == nil || r.Short != "1LT" {
		t.Errorf("resolveName(1st Lieutenant) = %+v", r)
	}
	if r := m.resolveName("Grand Poobah"); r != nil {
		t.Errorf("resolveName(Grand Poobah) = %+v, want nil", r)
	}
	// Warrant/NCO unified level: CW3 sits at SSG's level.
	cw3, ssg := m.byShort["CW3"], m.byShort["SSG"]
	lvlW, _ := m.level(cw3)
	lvlE, _ := m.level(ssg)
	if lvlW != lvlE {
		t.Errorf("level(CW3)=%d != level(SSG)=%d", lvlW, lvlE)
	}
}

func TestRankFromRecord(t *testing.T) {
	m := viiModel()
	cases := []struct {
		text string
		want string // "" = nil
	}{
		{"Promoted to Staff Sergeant (E-6)", "SSG"},
		{"Promoted to the rank of Corporal (E-4)", "CPL"},
		{"Reduced to Private (E-2)", "PVT"},
		// Last rank mentioned wins.
		{"Reduced from Sergeant (E-5) to Corporal (E-4)", "CPL"},
		// Text after "eligible" is ignored.
		{"Promoted to Corporal (E-4), eligible for Sergeant (E-5)", "CPL"},
		{"Assigned Rifleman 1/1/1/A", ""},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			got := rankFromRecord(tc.text, m)
			gotShort := ""
			if got != nil {
				gotShort = got.Short
			}
			if gotShort != tc.want {
				t.Errorf("rankFromRecord(%q) = %q, want %q", tc.text, gotShort, tc.want)
			}
		})
	}
}

func TestNormalizeRoleAndBilletFromRecord(t *testing.T) {
	if got := normalizeRole("Section Leader 1/1/A/ACD"); got != "Section Leader" {
		t.Errorf("normalizeRole unit strip = %q", got)
	}
	if got := normalizeRole("S1 Clerk Reserves"); got != "S1 Clerk" {
		t.Errorf("normalizeRole trail strip = %q", got)
	}
	// A slash inside a role name is not a unit path.
	if got := normalizeRole("Pilot/Gunner"); got != "Pilot/Gunner" {
		t.Errorf("normalizeRole role-slash = %q", got)
	}

	cases := []struct {
		text string
		want string
	}{
		{"Transferred and Assigned Section Leader 1/1/A/ACD", "Section Leader"},
		{"Reassigned to Platoon Sergeant 1/A/ACD", "Platoon Sergeant"},
		{"Assigned Rifleman 1/1/1/A", "Rifleman"},
		// Pure additional duty doesn't change the primary billet.
		{"Assigned S1 Clerk as Additional Duty", ""},
		{"Promoted to Corporal (E-4)", ""},
	}
	for _, tc := range cases {
		if got := billetFromRecord(tc.text); got != tc.want {
			t.Errorf("billetFromRecord(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestCanonicalBilletAndType(t *testing.T) {
	cases := []struct {
		role string
		want string
	}{
		{"", "MEMBER"},
		{"Rifleman", "MEMBER"},
		{"Assistant Section Leader", "ASL"},
		{"Section Leader", "SL"},
		{"Squad Leader", "SL"},
		{"Platoon Sergeant", "PSG"},
		{"First Sergeant", "FIRST_SGT"},
		{"Battalion Sergeant Major", "BN_SGM"},
		{"Platoon Leader", "PL"},
		{"Platoon Commander", "PL"}, // must not fall through to CO
		{"Company Commander", "CO"},
		{"Battalion Commander", "BN_CO"},
		{"Executive Officer", "CO_XO"},
		{"Battalion Executive Officer", "BN_XO"},
		{"S6 1IC", "DEPT1IC"},
		{"S1 2IC", "DEPT2IC"},
		{"Regimental Aide", "REGT_AIDE"},
		{"S7 Senior Instructor", "SUBDEPT_SENIOR"},
		{"S3 Lead", "SUBDEPT_LEAD"},
		{"S1 Clerk", "SUBDEPT_CLERK"},
		{"S5 Analyst", "SUBDEPT_CLERK"}, // generic staff → clerk tier
	}
	for _, tc := range cases {
		if got := canonicalBillet(tc.role); got != tc.want {
			t.Errorf("canonicalBillet(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}

	if got := viiBilletType("S1 Clerk"); got != "staff" {
		t.Errorf("viiBilletType(S1 Clerk) = %q, want staff", got)
	}
	if got := viiBilletType("Section Leader"); got != "line" {
		t.Errorf("viiBilletType(Section Leader) = %q, want line", got)
	}
	if got := viiBilletType("Reservist"); got != "" {
		t.Errorf("viiBilletType(Reservist) = %q, want empty (non-billet)", got)
	}
	if got := viiBilletType("Boot Camp"); got != "" {
		t.Errorf("viiBilletType(Boot Camp) = %q, want empty (non-billet)", got)
	}
}

func TestComputeService(t *testing.T) {
	// Enlist 2020-01-01, 6mo ELOA in 2021, retire 2023-01-01 (3y calendar −
	// ~181d ELOA), re-enlist 2025-01-01, still active at asOf 2026-01-01.
	recs := []viiRecord{
		{Date: "2020-01-01", Text: "Enlisted in the 7th Cavalry"},
		{Date: "2021-01-01", Text: "Placed on ELOA"},
		{Date: "2021-07-01", Text: "Returned from ELOA"},
		{Date: "2023-01-01", Text: "Retired from the 7th Cavalry"},
		{Date: "2025-01-01", Text: "Enlisted in the 7th Cavalry"},
	}
	s := computeService(recs, "2026-01-01")
	if len(s.Departures) != 1 || s.Departures[0].Type != "retire" {
		t.Fatalf("departures = %+v, want one retirement", s.Departures)
	}
	dep := s.Departures[0]
	// 2020-01-01→2023-01-01 = 1096d minus 181d ELOA = 915d.
	if dep.ConsecDays != 915 {
		t.Errorf("retirement ConsecDays = %d, want 915", dep.ConsecDays)
	}
	if s.CurrentConse != 365 {
		t.Errorf("CurrentConse = %d, want 365", s.CurrentConse)
	}
	if s.TotalActive != 915+365 {
		t.Errorf("TotalActive = %d, want %d", s.TotalActive, 915+365)
	}

	// Reserve transfer records a "reserve" departure.
	recs2 := []viiRecord{
		{Date: "2020-01-01", Text: "Enlisted in the 7th Cavalry"},
		{Date: "2022-06-01", Text: "Transferred and Assigned Reserves"},
	}
	s2 := computeService(recs2, "2026-01-01")
	if len(s2.Departures) != 1 || s2.Departures[0].Type != "reserve" {
		t.Fatalf("departures = %+v, want one reserve transfer", s2.Departures)
	}
}

// Departmental service while in the Reserves keeps the retirement timer
// running: the reserve transfer snapshots a departure, and losing the staff
// duty later records the deferred departure with the dept months included.
func TestComputeServiceDeptReserveTimer(t *testing.T) {
	recs := []viiRecord{
		{Date: "2023-01-01", Text: "Enlisted in the 7th Cavalry"},
		{Date: "2023-06-01", Text: "Transferred and Assigned S6 Clerk"},
		{Date: "2024-06-01", Text: "Transferred and Assigned Reserves"},
		{Date: "2025-06-01", Text: "Relieved of Duties"},
	}
	s := computeService(recs, "2026-01-01")
	if len(s.Departures) != 2 {
		t.Fatalf("departures = %+v, want snapshot + deferred", s.Departures)
	}
	// Snapshot at the transfer: 2023-01-01→2024-06-01 = 517d (< 730, not yet
	// retirement-eligible).
	if s.Departures[0].ConsecDays != 517 || s.Departures[0].TotalDays != 517 {
		t.Errorf("snapshot departure = %+v, want 517/517", s.Departures[0])
	}
	// Deferred at the relief: 2023-01-01→2025-06-01 = 882d, since the dept year in
	// the Reserves counted, pushing past the 730d threshold.
	if s.Departures[1].ConsecDays != 882 || s.Departures[1].Type != "reserve" {
		t.Errorf("deferred departure = %+v, want reserve/882", s.Departures[1])
	}
	if s.TotalActive != 882 || s.CurrentConse != 0 {
		t.Errorf("TotalActive/CurrentConse = %d/%d, want 882/0", s.TotalActive, s.CurrentConse)
	}

	// A line member (no staff duty) transferring to the Reserves stops the
	// timer at the transfer, with no dept credit.
	recsLine := []viiRecord{
		{Date: "2023-01-01", Text: "Enlisted in the 7th Cavalry"},
		{Date: "2023-06-01", Text: "Transferred and Assigned Rifleman 1/1/1/A"},
		{Date: "2024-06-01", Text: "Transferred and Assigned Reserves"},
	}
	sLine := computeService(recsLine, "2026-01-01")
	if len(sLine.Departures) != 1 {
		t.Fatalf("line departures = %+v, want one", sLine.Departures)
	}
	if sLine.TotalActive != 517 {
		t.Errorf("line TotalActive = %d, want 517 (timer stopped at transfer)", sLine.TotalActive)
	}
}

// A departure (retirement/discharge/death) drops all billets: a staff duty
// held before retirement must NOT leak into a later reserve span and keep the
// timer running. Regression for the staff-duty-drop-on-departure rule.
func TestComputeServiceDepartureDropsStaffDuty(t *testing.T) {
	recs := []viiRecord{
		{Date: "2020-01-01", Text: "Enlisted in the 7th Cavalry"},
		{Date: "2020-02-01", Text: "Assigned S6 Clerk"},
		{Date: "2023-01-01", Text: "Retired from the 7th Cavalry"},
		// Returns with no new assignment; the S6 duty must NOT persist.
		{Date: "2023-06-01", Text: "Enlisted in the 7th Cavalry"},
		{Date: "2024-01-01", Text: "Transferred and Assigned Reserves"},
	}
	s := computeService(recs, "2026-01-01")
	// Second span closes AT the reserve transfer (no dept duty to continue
	// it), so nothing accrues after 2024-01-01.
	if s.CurrentConse != 0 {
		t.Errorf("CurrentConse = %d, want 0 (stale staff duty leaked past retirement)", s.CurrentConse)
	}
	// 1096 (first span) + 214 (return→reserve) = 1310; a leak would inflate
	// this past 2000.
	if s.TotalActive != 1310 {
		t.Errorf("TotalActive = %d, want 1310", s.TotalActive)
	}
}

func TestParseDisciplineAndStandingChecks(t *testing.T) {
	recs := []viiRecord{
		{Date: "2026-01-01", Text: "Letter of Reprimand issued, 90 Days NFA"},
		{Date: "2024-01-01", Text: "Article 15 proceedings concluded"},
		{Date: "2026-02-01", Text: "Negative Counselling, No Additional NFA"},
		{Date: "2026-03-01", Text: "Letter of Reprimand, 30 Days NFA to be served on re-enlistment"},
	}
	dl := parseDiscipline(recs)
	if len(dl) != 4 {
		t.Fatalf("parsed %d discipline records, want 4", len(dl))
	}
	if dl[0].NFADays != 90 {
		t.Errorf("NFADays = %d, want 90", dl[0].NFADays)
	}
	if !dl[1].IsArticle {
		t.Error("Article 15 not flagged")
	}
	if dl[2].NFADays != 0 {
		t.Errorf("'No Additional NFA' NFADays = %d, want 0", dl[2].NFADays)
	}
	if !dl[3].Deferred {
		t.Error("deferred NFA not flagged")
	}

	// Active NFA window: [start, start+days).
	if !activeNFAAt(dl, "2026-01-15") {
		t.Error("expected active NFA on 2026-01-15")
	}
	if activeNFAAt(dl, "2026-04-01") {
		t.Error("expected NFA expired by 2026-04-01")
	}
	// Deferred NFA never blocks.
	if activeNFAAt([]viiDiscipline{dl[3]}, "2026-03-15") {
		t.Error("deferred NFA should not be active")
	}
	if !articleWithinYear(dl, "2024-06-01") {
		t.Error("expected article within a year of 2024-06-01")
	}
	if articleWithinYear(dl, "2026-01-01") {
		t.Error("article from 2024 should be outside the 1y window in 2026")
	}
	// "No Favorable Action for N Days" phrasing also parses.
	dl2 := parseDiscipline([]viiRecord{{Date: "2026-01-01", Text: "No Favorable Action for 45 Days"}})
	if len(dl2) != 1 || dl2[0].NFADays != 45 {
		t.Errorf("NFA-for phrasing parsed as %+v, want 45 days", dl2)
	}
}

// --- analyze scenarios -----------------------------------------------------

// viiVetProfile: enlisted 2019, made SSG as a line Section Leader, retired
// with 2y+ TIS, returned 2026 as CPL in a Section Leader billet (SL rates
// SSG). The §VII textbook case.
func viiVetProfile() *utils.ProfileResponse {
	return &utils.ProfileResponse{
		User: utils.User{Username: "Vet.V"},
		Rank: utils.Rank{RankShort: "CPL", RankFull: "Corporal"},
		Primary: utils.Position{
			PositionTitle: "Section Leader 1/1/A/ACD",
		},
		Records: []utils.Record{
			viiRec("2019-01-01", "Enlisted in the 7th Cavalry"),
			viiRec("2019-02-01", "Assigned Rifleman 1/1/1/A"),
			viiRec("2020-01-01", "Promoted to Sergeant (E-5)"),
			viiRec("2020-06-01", "Transferred and Assigned Section Leader 1/1/A/ACD"),
			viiRec("2021-01-01", "Promoted to Staff Sergeant (E-6)"),
			viiRec("2021-06-01", "Retired from the 7th Cavalry"),
			viiRec("2026-01-01", "Returned from Retirement"),
			viiRec("2026-01-01", "Enlisted in the 7th Cavalry"),
			viiRec("2026-01-02", "Promoted to Corporal (E-4)"),
			viiRec("2026-01-03", "Transferred and Assigned Section Leader 1/1/A/ACD"),
		},
	}
}

func TestViiAnalyzeEligibleVeteran(t *testing.T) {
	a := viiAnalyze(viiVetProfile(), viiModel(), promoRefDate.UTC())
	if !a.EligibleNow {
		t.Fatalf("EligibleNow = false, want true (result %+v)", a)
	}
	if a.Target == nil || a.Target.Short != "SSG" {
		t.Errorf("Target = %+v, want SSG", a.Target)
	}
	if !a.Qualifies {
		t.Error("Qualifies = false, want true (retirement departure + return)")
	}
	if !a.TargetHeldSameRole {
		t.Error("TargetHeldSameRole = false, want true (held SSG as Section Leader)")
	}
	if a.TargetHeldDate != "2021-01-01" {
		t.Errorf("TargetHeldDate = %q, want 2021-01-01", a.TargetHeldDate)
	}
}

// A member who never departed (rank reduction, continuous service) is not a
// returning veteran, so §VII must not fire despite a held higher rank.
// Regression: smoke test flagged a continuously-serving SGT who had held SSG.
func TestViiAnalyzeNoDepartureNotEligible(t *testing.T) {
	p := &utils.ProfileResponse{
		User:    utils.User{Username: "Steady.S"},
		Rank:    utils.Rank{RankShort: "SGT", RankFull: "Sergeant"},
		Primary: utils.Position{PositionTitle: "Section Leader 1/1/A/ACD"},
		Records: []utils.Record{
			viiRec("2024-01-01", "Enlisted in the 7th Cavalry"),
			viiRec("2024-02-01", "Transferred and Assigned Section Leader 1/1/A/ACD"),
			viiRec("2024-08-03", "Promoted to Staff Sergeant (E-6)"),
			viiRec("2025-06-01", "Reduced to Sergeant (E-5)"),
		},
	}
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if a.EligibleNow || a.PotentiallyEligible || a.Qualifies {
		t.Errorf("continuous service must not qualify for §VII: %+v", a)
	}
}

// A reserve transfer made BEFORE reaching retirement eligibility is not a
// qualifying departure. Regression: smoke test flagged a member who left to
// the Reserves with ~5 months TIS.
func TestViiAnalyzeIneligibleReserveDepartureNotQualifying(t *testing.T) {
	p := &utils.ProfileResponse{
		User:    utils.User{Username: "Early.E"},
		Rank:    utils.Rank{RankShort: "CPL", RankFull: "Corporal"},
		Primary: utils.Position{PositionTitle: "Section Leader 1/1/A/ACD"},
		Records: []utils.Record{
			viiRec("2024-01-01", "Enlisted in the 7th Cavalry"),
			viiRec("2024-02-01", "Transferred and Assigned Section Leader 1/1/A/ACD"),
			viiRec("2024-03-12", "Promoted to Staff Sergeant (E-6)"),
			viiRec("2024-06-01", "Transferred and Assigned Reserves"),
			viiRec("2026-01-01", "Reinstated to Active Duty"),
			viiRec("2026-01-02", "Promoted to Corporal (E-4)"),
			viiRec("2026-01-03", "Transferred and Assigned Section Leader 1/1/A/ACD"),
		},
	}
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if a.EligibleNow || a.PotentiallyEligible || a.Qualifies {
		t.Errorf("pre-eligibility reserve departure must not qualify for §VII: %+v", a)
	}
}

// Departmental service in the Reserves pushing the timer past the threshold
// makes the (deferred) reserve departure qualifying: the veteran is §VII
// eligible on return where they wouldn't be without the dept credit.
func TestViiAnalyzeDeptReserveVeteranEligible(t *testing.T) {
	p := &utils.ProfileResponse{
		User:    utils.User{Username: "Dept.D"},
		Rank:    utils.Rank{RankShort: "CPL", RankFull: "Corporal"},
		Primary: utils.Position{PositionTitle: "Section Leader 1/1/A/ACD"},
		Records: []utils.Record{
			viiRec("2023-01-01", "Enlisted in the 7th Cavalry"),
			viiRec("2023-02-01", "Transferred and Assigned Section Leader 1/1/A/ACD"),
			viiRec("2023-06-01", "Promoted to Staff Sergeant (E-6)"),
			viiRec("2023-08-01", "Assigned S7 Instructor as Additional Duty"),
			// 517d TIS at transfer (not yet eligible), but the S7 duty keeps
			// the timer running through the Reserves…
			viiRec("2024-06-01", "Transferred and Assigned Reserves"),
			// …to 882d at relief: past the 730d threshold, qualifying.
			viiRec("2025-06-01", "Relieved of Duties"),
			viiRec("2026-01-01", "Reinstated to Active Duty"),
			viiRec("2026-01-02", "Promoted to Corporal (E-4)"),
			viiRec("2026-01-03", "Transferred and Assigned Section Leader 1/1/A/ACD"),
		},
	}
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if !a.Qualifies {
		t.Fatalf("dept-extended reserve departure should qualify: %+v", a)
	}
	if !a.EligibleNow || a.Target == nil || a.Target.Short != "SSG" {
		t.Errorf("EligibleNow/Target = %v/%+v, want true/SSG", a.EligibleNow, a.Target)
	}
}

func TestViiAnalyzeBilletCeilingCaps(t *testing.T) {
	// Same history but returning into a MEMBER billet (Rifleman rates CPL):
	// no held rank fits between ceiling and current CPL → ineligible.
	p := viiVetProfile()
	p.Primary.PositionTitle = "Rifleman 1/1/1/A"
	p.Records[len(p.Records)-1] = viiRec("2026-01-03", "Transferred and Assigned Rifleman 1/1/1/A")
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if a.EligibleNow {
		t.Errorf("EligibleNow = true, want false (MEMBER billet caps at CPL): %+v", a)
	}
	if a.PotentiallyEligible {
		t.Errorf("PotentiallyEligible = true, want false (billet-too-low is a hard no)")
	}
}

func TestViiAnalyzeWrongBilletType(t *testing.T) {
	// Held SSG in a LINE billet only; returns into a STAFF billet → the rank
	// wasn't earned in this billet type → ineligible.
	p := viiVetProfile()
	p.Primary.PositionTitle = "S3 Lead"
	p.Records[len(p.Records)-1] = viiRec("2026-01-03", "Transferred and Assigned S3 Lead")
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if a.EligibleNow {
		t.Errorf("EligibleNow = true, want false (SSG held in line, billet is staff): %+v", a)
	}
}

func TestViiAnalyzeDowngradeIsPotential(t *testing.T) {
	p := viiVetProfile()
	p.Records = append(p.Records, viiRec("2025-06-01", "Retirement status downgraded to Honorable Discharge"))
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if a.EligibleNow {
		t.Error("EligibleNow = true, want false (downgraded retirement)")
	}
	if !a.PotentiallyEligible {
		t.Fatalf("PotentiallyEligible = false, want true: %+v", a)
	}
	if a.PotentialRank == nil || a.PotentialRank.Short != "SSG" {
		t.Errorf("PotentialRank = %+v, want SSG", a.PotentialRank)
	}
	if !strings.Contains(a.Reason(), "case-by-case") {
		t.Errorf("Reason = %q, want downgrade explanation", a.Reason())
	}
}

func TestViiAnalyzeStandingBlocks(t *testing.T) {
	p := viiVetProfile()
	// NFA active at the ref date (2026-05-15).
	p.Records = append(p.Records, viiRec("2026-05-01", "Letter of Reprimand issued, 90 Days NFA"))
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if a.EligibleNow {
		t.Error("EligibleNow = true, want false (active NFA)")
	}
	if !a.Blocked || !a.PotentiallyEligible {
		t.Errorf("Blocked=%v PotentiallyEligible=%v, want true/true", a.Blocked, a.PotentiallyEligible)
	}
	if !strings.Contains(a.Reason(), "standing clears") {
		t.Errorf("Reason = %q, want standing-block explanation", a.Reason())
	}
}

func TestViiAnalyzeWarrantDisplayForAviator(t *testing.T) {
	// Aviator with wings held CW3 (unified level = SSG); returning into an SL
	// billet the target should DISPLAY as CW3, not SSG.
	p := viiVetProfile()
	p.Mos = "155F"
	p.Awards = []utils.Award{{AwardName: "Army Aviator Badge"}}
	for i, r := range p.Records {
		if r.RecordDetails == "Promoted to Staff Sergeant (E-6)" {
			p.Records[i].RecordDetails = "Promoted to Chief Warrant Officer 3 (W-3)"
		}
	}
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if !a.EligibleNow {
		t.Fatalf("EligibleNow = false, want true: %+v", a)
	}
	if a.Target == nil || a.Target.Short != "CW3" {
		t.Errorf("Target = %+v, want CW3 (warrant display for winged aviator)", a.Target)
	}
}

func TestViiAnalyzeNoHistoryIneligible(t *testing.T) {
	p := &utils.ProfileResponse{
		User:    utils.User{Username: "New.N"},
		Rank:    utils.Rank{RankShort: "PVT"},
		Primary: utils.Position{PositionTitle: "Rifleman 1/1/1/A"},
		Records: []utils.Record{
			viiRec("2026-01-01", "Enlisted in the 7th Cavalry"),
			viiRec("2026-01-02", "Assigned Rifleman 1/1/1/A"),
		},
	}
	a := viiAnalyze(p, viiModel(), promoRefDate.UTC())
	if a.EligibleNow || a.PotentiallyEligible || a.Qualifies {
		t.Errorf("fresh trooper should be fully ineligible: %+v", a)
	}
}
