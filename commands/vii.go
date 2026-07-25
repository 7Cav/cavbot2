package commands

// Veteran Rank Retention (Ch.4 §VII) analysis engine.
//
// Ported from the author's internal veteran-rank-retention audit tool
// (private). Thresholds and billet tables follow 7CAV-R-023 and the wiki
// Rank Promotion & Reduction Guidelines billet/rank tables. Determines whether a member is
// eligible to be restored to a previously-held rank in an appropriate billet,
// as an ALTERNATIVE path to the standard promotion ladder: standard TIG/TIS
// is waived, the proposal process still applies.
//
// The source engine's display-only extras (ops/class counts, service-medal lists,
// retirement eligibility check, secondary-billet retirement credit) are not
// ported; this file covers the eligibility determination only.
//
// Behavioral fidelity notes: regexes, check ordering, and threshold constants
// are transcribed 1:1 from the source; where the source had quirks (e.g. the
// rank-parse regex only matching single-digit grades, so "(O-11)" never
// parses) those quirks are preserved deliberately, since this engine's outputs
// were validated against real milpac records in production use, and "fixing"
// a quirk here would desync the two implementations.
//
// Two deliberate deviations from the source, per S1 clarification of the
// policy (2026-07-23, smoke-test review):
//   - Eligibility is GATED on a qualifying departure: an actual retirement,
//     or a reserve transfer made while retirement-eligible. The source
//     computed this ("Qualifies") but surfaced it as context only, which
//     flagged members who never departed or who left to reserves before
//     reaching retirement eligibility.
//   - Departmental (staff) service counts toward the retirement timer,
//     including service rendered while in the Reserves (see computeService).

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
)

const (
	viiConsecDays = 730  // 2y consecutive TIS for a qualifying reserve departure
	viiTotalDays  = 1095 // or 3y total
)

// billetCeiling is the authorised maximum rank for a billet, transcribed
// from the "Billet Minimum / Maximum Rank Tables" (Ch.4 §II) and "Department
// Staff" (Ch.4 §III) tables of 7CAV-R-023:
// https://wiki.7cav.us/wiki/Rank_Promotion_and_Reduction_Guidelines
// Verified against revision 17274 (last modified 24OCT24). An empty string
// means R-023 defines no ceiling for that track, which is different from a
// ceiling of zero: the §VII engine treats it as "cannot rate anyone here".
//
// Only Enlisted and Officer are read. Warrant is carried because R-023 states
// the maxima as pairs ("SGT / CW2", "SSG / CW3") and dropping half the table
// would make it harder to check against the source, but the engine never
// consults it: viiW2E normalises a warrant rank to its NCO equivalent before
// any comparison, so a warrant officer is measured against the Enlisted
// ceiling. Correcting a Warrant value therefore changes nothing, since the
// behaviour lives in viiW2E and the Enlisted column.
//
// Minimum ranks are deliberately not modelled. R-023 gives them, but they
// govern whether someone may be *assigned* a billet, which is S1's business
// at assignment time, not whether an already-assigned member is promotable.
type billetCeiling struct {
	Enlisted string
	Warrant  string
	Officer  string
}

var viiBilletCeiling = map[string]billetCeiling{
	// line NCO/warrant
	"MEMBER":    {Enlisted: "CPL", Warrant: "WO1"},
	"ASL":       {Enlisted: "SGT", Warrant: "CW2"},
	"SL":        {Enlisted: "SSG", Warrant: "CW3"},
	"PSG":       {Enlisted: "MSG", Warrant: "CW5"},
	"FIRST_SGT": {Enlisted: "1SG"},
	"BN_SGM":    {Enlisted: "SGM"},
	// staff / department
	"SUBDEPT_CLERK":  {Enlisted: "CPL"},
	"SUBDEPT_SENIOR": {Enlisted: "SGT"},
	"SUBDEPT_LEAD":   {Enlisted: "SSG", Warrant: "CW3"},
	"DEVCOM_LEAD":    {Enlisted: "MSG", Officer: "CPT"},
	"DEPT2IC":        {Enlisted: "SFC", Warrant: "CW4", Officer: "CPT"},
	"DEPT1IC":        {Enlisted: "MSG", Warrant: "CW5", Officer: "MAJ"},
	// R-023 lists Regimental Aide as MSG/LTC with no warrant grade; the
	// unread Warrant column is left empty here to match the source exactly.
	"REGT_AIDE": {Enlisted: "MSG", Officer: "LTC"},
	// officer command
	"PL":    {Officer: "1LT"},
	"CO_XO": {Officer: "MAJ"},
	"CO":    {Officer: "MAJ"},
	"BN_XO": {Officer: "LTC"},
	"BN_CO": {Officer: "COL"},
}

// Warrant ⇄ NCO interchangeability: a warrant rank is the same LEVEL as its
// NCO equivalent, so all rank math runs on the unified enlisted scale.
var viiW2E = map[string]string{"WO1": "CPL", "CW2": "SGT", "CW3": "SSG", "CW4": "SFC", "CW5": "MSG"}
var viiE2W = map[string]string{"CPL": "WO1", "SGT": "CW2", "SSG": "CW3", "SFC": "CW4", "MSG": "CW5"}

var (
	viiStaffRe     = regexp.MustCompile(`(?i)\bS\d\b|\bRRD\b|\bSPD\b|\bRTC\b|Recruit Training Command|NCOA|\bMP\b|Military Police|\bAide\b|\bClerk\b|Analyst|Investigator|Recruiter|Instructor|Coordinator|\b1IC\b|\b2IC\b|Operations|Department|Center of Excellence|Forum|Social Media|Publisher|Public Relations|\bStaff\b|\bLead\b|\bSenior\b|Technical|\bWAG\b`)
	viiNonBilletRe = regexp.MustCompile(`(?i)\bBoot Camp\b|\bReserves?\b|\bReservist\b|\bELOA\b`)
	viiAviatorRe   = regexp.MustCompile(`(?i)Aviator Badge`)
	viiMosAvnRe    = regexp.MustCompile(`^15`)
)

// viiBilletType classifies a role into "staff" or "line"; "" means the role
// is not a billet at all (Boot Camp / Reserves / ELOA) and doesn't count as
// holding a rank in any billet type.
func viiBilletType(role string) string {
	if role == "" {
		return ""
	}
	if viiNonBilletRe.MatchString(role) {
		return ""
	}
	if viiStaffRe.MatchString(role) {
		return "staff"
	}
	return "line"
}

func viiIsAviation(profile *utils.ProfileResponse) bool {
	return viiMosAvnRe.MatchString(strings.TrimSpace(profile.Mos))
}

func viiHasWings(profile *utils.ProfileResponse) bool {
	for _, a := range profile.Awards {
		if viiAviatorRe.MatchString(a.AwardName) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Rank model
// ---------------------------------------------------------------------------

type viiRank struct {
	Short string
	Full  string
	Order int
}

type viiRankModel struct {
	byShort  map[string]*viiRank
	byNameLc map[string]*viiRank
	// names sorted longest-first for suffix matching in resolveName
	sortedNames []string
	nameAliases map[string]string
	all         []*viiRank
}

// buildRankModel indexes the /milpacs/ranks reference list. Mirrors the source
// buildRankModel: filters the Tester rank, aliases 1st/2nd lieutenant, and
// exposes level() on the unified enlisted scale.
func buildRankModel(resp *utils.RanksResponse) *viiRankModel {
	m := &viiRankModel{
		byShort:     map[string]*viiRank{},
		byNameLc:    map[string]*viiRank{},
		nameAliases: map[string]string{"1st lieutenant": "first lieutenant", "2nd lieutenant": "second lieutenant"},
	}
	for _, r := range resp.Ranks {
		if r.RankShort == "Tester" {
			continue
		}
		o := &viiRank{Short: r.RankShort, Full: r.RankFull, Order: r.RankDisplayOrder}
		m.byShort[o.Short] = o
		m.byNameLc[strings.ToLower(o.Full)] = o
		m.all = append(m.all, o)
	}
	for n := range m.byNameLc {
		m.sortedNames = append(m.sortedNames, n)
	}
	for a := range m.nameAliases {
		m.sortedNames = append(m.sortedNames, a)
	}
	sort.Slice(m.sortedNames, func(a, b int) bool { return len(m.sortedNames[a]) > len(m.sortedNames[b]) })
	return m
}

// resolveName resolves a rank phrase to a rank by longest-suffix match, the
// same way the source resolveName does.
func (m *viiRankModel) resolveName(phrase string) *viiRank {
	p := strings.ToLower(strings.TrimSpace(phrase))
	for _, n := range m.sortedNames {
		if p == n || strings.HasSuffix(p, " "+n) || strings.HasSuffix(p, n) {
			canonical := n
			if alias, ok := m.nameAliases[n]; ok {
				canonical = alias
			}
			return m.byNameLc[canonical]
		}
	}
	return nil
}

// enlEquiv maps a warrant rank to its NCO equivalent (else unchanged).
func (m *viiRankModel) enlEquiv(rk *viiRank) *viiRank {
	if rk == nil {
		return nil
	}
	if e, ok := viiW2E[rk.Short]; ok {
		if er, ok := m.byShort[e]; ok {
			return er
		}
	}
	return rk
}

// level is the unified (enlisted-scale) display order used for ALL
// comparisons; lower = more senior. Returns (0, false) when unknown.
func (m *viiRankModel) level(rk *viiRank) (int, bool) {
	e := m.enlEquiv(rk)
	if e == nil {
		return 0, false
	}
	return e.Order, true
}

// viiTrackOf classifies a display order: officer <=110, warrant <=140, else
// enlisted. Thresholds match the production display-order spacing the source tool
// was built against.
func viiTrackOf(order int) string {
	if order <= 110 {
		return "officer"
	}
	if order <= 140 {
		return "warrant"
	}
	return "enlisted"
}

// ---------------------------------------------------------------------------
// Record parsing
// ---------------------------------------------------------------------------

// viiRankRecordRe extracts "<Rank Name> (E-6)"-style rank mentions. Grade
// suffix is a single digit, as in the source (see fidelity note in file header).
var viiRankRecordRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9 ]*?)\s*\([EOW]-?\d\)`)
var viiEligibleSplitRe = regexp.MustCompile(`(?i)\beligible\b`)

// rankFromRecord returns the LAST resolvable rank mentioned before any
// "eligible" clause, because promotion records read "Promoted to X (E-n)".
func rankFromRecord(text string, m *viiRankModel) *viiRank {
	if loc := viiEligibleSplitRe.FindStringIndex(text); loc != nil {
		text = text[:loc[0]]
	}
	var last *viiRank
	for _, match := range viiRankRecordRe.FindAllStringSubmatch(text, -1) {
		if r := m.resolveName(match[1]); r != nil {
			last = r
		}
	}
	return last
}

var (
	// Unit path = uppercase/digit codes only (e.g. A/1/A/1-7) so a role name
	// containing a slash ("Pilot/Gunner") is NOT mistaken for a unit.
	viiUnitPathRe = regexp.MustCompile(`\b[A-Z0-9]+(?:/[A-Z0-9-]+)+\b`)
	viiTrailRe    = regexp.MustCompile(`(?i)\s+(?:Reserves?|Reservist|Boot Camp|RTC(?: DCS)?|DCS RTC|DEVCOM)\.?$`)
	viiTrailOfRe  = regexp.MustCompile(`(?i)\s+of$`)
	viiSpacesRe   = regexp.MustCompile(`\s{2,}`)

	viiAddlDutyRe    = regexp.MustCompile(`(?i)as Additional Duty`)
	viiTransAssignRe = regexp.MustCompile(`(?i)Transferred and Assigned\s+(.+)$`)
	viiReassignRe    = regexp.MustCompile(`(?i)Reassigned to\s+(.+)$`)
	viiAssignRe      = regexp.MustCompile(`(?i)\bAssigned\s+(.+)$`)
	viiBilletSplitRe = regexp.MustCompile(`(?i),| in Lieu| as Additional Duty`)
	viiPrimaryMoveRe = regexp.MustCompile(`(?i)Transferred and Assigned|Reassigned to`)
	viiRelievedRe    = regexp.MustCompile(`(?i)Relieved of Duties`)
)

func normalizeRole(t string) string {
	if t == "" {
		return ""
	}
	t = strings.TrimSpace(t)
	t = strings.TrimSuffix(t, ".")
	if loc := viiUnitPathRe.FindStringIndex(t); loc != nil {
		t = strings.TrimSpace(t[:loc[0]])
	} else {
		t = strings.TrimSpace(viiTrailRe.ReplaceAllString(t, ""))
	}
	t = viiTrailOfRe.ReplaceAllString(t, "")
	t = strings.TrimSuffix(t, ",")
	t = viiSpacesRe.ReplaceAllString(t, " ")
	return strings.TrimSpace(t)
}

// capturedBillet extracts the assigned role from any assignment record,
// additional-duty or not. Used directly by the retirement-timer walk, where a
// secondary departmental duty still counts as departmental service.
func capturedBillet(t string) string {
	var captured string
	if m := viiTransAssignRe.FindStringSubmatch(t); m != nil {
		captured = m[1]
	} else if m := viiReassignRe.FindStringSubmatch(t); m != nil {
		captured = m[1]
	} else if m := viiAssignRe.FindStringSubmatch(t); m != nil {
		captured = m[1]
	} else {
		return ""
	}
	if loc := viiBilletSplitRe.FindStringIndex(captured); loc != nil {
		captured = captured[:loc[0]]
	}
	return normalizeRole(strings.TrimSpace(captured))
}

// billetFromRecord extracts the PRIMARY billet from a record. Pure
// additional-duty assignments don't change the primary billet.
func billetFromRecord(t string) string {
	if viiAddlDutyRe.MatchString(t) && !regexp.MustCompile(`(?i)Transferred and Assigned`).MatchString(t) {
		return ""
	}
	return capturedBillet(t)
}

var viiBattalionRe = regexp.MustCompile(`(?i)battalion|\b\d-7\b`)

type canonRule struct {
	re  *regexp.Regexp
	key string
}

// Ordered: 1IC/2IC before generic Senior/Lead; ASL before SL; "platoon
// commander" resolves PL before the bare "commander" check resolves CO.
var viiCanonRules = []canonRule{
	{regexp.MustCompile(`(?i)sergeant major`), "BN_SGM"},
	{regexp.MustCompile(`(?i)first sergeant`), "FIRST_SGT"},
	{regexp.MustCompile(`(?i)platoon sergeant`), "PSG"},
	{regexp.MustCompile(`(?i)assistant (?:section|squad) leader`), "ASL"},
	{regexp.MustCompile(`(?i)(?:section|squad) leader`), "SL"},
	{regexp.MustCompile(`(?i)platoon (?:leader|commander)`), "PL"},
	{regexp.MustCompile(`(?i)\b1ic\b|1 ?ic`), "DEPT1IC"},
	{regexp.MustCompile(`(?i)\b2ic\b`), "DEPT2IC"},
	{regexp.MustCompile(`(?i)regimental aide|\baide\b`), "REGT_AIDE"},
	{regexp.MustCompile(`(?i)devcom lead`), "DEVCOM_LEAD"},
	{regexp.MustCompile(`(?i)\bsenior\b`), "SUBDEPT_SENIOR"},
	{regexp.MustCompile(`(?i)\blead\b`), "SUBDEPT_LEAD"},
	{regexp.MustCompile(`(?i)\bclerk\b`), "SUBDEPT_CLERK"},
}

var (
	viiXORe = regexp.MustCompile(`(?i)executive officer|company xo|\bxo\b`)
	viiCORe = regexp.MustCompile(`(?i)commander|commanding officer|company co\b`)
)

// canonicalBillet maps a normalized role to a billet-ceiling key.
func canonicalBillet(role string) string {
	if role == "" {
		return "MEMBER"
	}
	bn := viiBattalionRe.MatchString(role)
	for _, rule := range viiCanonRules {
		if rule.re.MatchString(role) {
			return rule.key
		}
	}
	if viiXORe.MatchString(role) {
		if bn {
			return "BN_XO"
		}
		return "CO_XO"
	}
	if viiCORe.MatchString(role) {
		if bn {
			return "BN_CO"
		}
		return "CO"
	}
	if viiStaffRe.MatchString(role) {
		return "SUBDEPT_CLERK"
	}
	return "MEMBER"
}

// ---------------------------------------------------------------------------
// Service + discipline history
// ---------------------------------------------------------------------------

var (
	viiRetiredRe   = regexp.MustCompile(`(?i)Retired from the .*?(?:Cavalry|Calvary)`)
	viiDowngradeRe = regexp.MustCompile(`(?i)Retirement (?:status )?downgraded`)
	viiInLieuRe    = regexp.MustCompile(`(?i)in Lieu of Retirement`)
	// viiMemorialRe matches death/memorial departures (RIP, Arlington, Wall of
	// Honor). Like retirement and discharge, these drop all billets and end
	// service. Shared with /billetaudit.
	viiMemorialRe   = regexp.MustCompile(`(?i)Wall of Honor|Arlington|Rest in Peace|\bRIP\b|Killed in Action|\bKIA\b|In Memoriam|passed away`)
	viiEnlistRe     = regexp.MustCompile(`(?i)Enlisted in the .*?(?:Cavalry|Calvary)`)
	viiReinstateRe  = regexp.MustCompile(`(?i)Reinstated`)
	viiDischargeRe  = regexp.MustCompile(`(?i)Discharge`)
	viiUpgradeRe    = regexp.MustCompile(`(?i)Upgrade`)
	viiEloaStartRe  = regexp.MustCompile(`(?i)Placed on ELOA`)
	viiEloaEndRe    = regexp.MustCompile(`(?i)Returned from ELOA`)
	viiReserveRe    = regexp.MustCompile(`(?i)Reserv`)
	viiToReservesRe = regexp.MustCompile(`(?i)Transferred (?:and|to).*Reserv|Assigned Reserv`)
)

type viiRecord struct {
	Date string // YYYY-MM-DD
	Text string
	Type string
}

type viiDeparture struct {
	Date       string
	Type       string // "retire" | "reserve"
	ConsecDays int
	TotalDays  int
	Text       string
}

type viiService struct {
	Departures   []viiDeparture
	TotalActive  int
	CurrentConse int
}

func daysBetweenISO(a, b string) int {
	ta, errA := time.Parse("2006-01-02", a)
	tb, errB := time.Parse("2006-01-02", b)
	if errA != nil || errB != nil {
		return 0
	}
	return int(tb.Sub(ta).Hours() / 24)
}

func addDaysISO(d string, n int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// computeService replays the record stream through the enlistment state
// machine (active / eloa / reserve / out), accumulating service spans (ELOA
// time paused) and recording departures with the TIS held at that moment.
//
// Departmental service counts toward the retirement timer (S1 clarification,
// 2026-07-23): a reserve transfer while holding a departmental (staff) duty
// records a departure snapshot but leaves the span running; the deferred
// departure is recorded when the departmental duty ends. A staff duty picked
// up while already in the Reserves opens a fresh span. Staff tracking uses
// capturedBillet (not billetFromRecord): secondary departmental duties count.
func computeService(recs []viiRecord, asOf string) viiService {
	status := "out"
	enlistStart := ""
	eloaPause := 0
	eloaStart := ""
	totalPrior := 0
	staffRole := ""
	var departures []viiDeparture

	// peekSpan reports (consecutive, total) days as of d without closing the
	// open span; closeSpan folds the span into totalPrior and resets.
	peekSpan := func(d string) (int, int) {
		if enlistStart == "" {
			return 0, totalPrior
		}
		p := eloaPause
		if status == "eloa" && eloaStart != "" {
			p += daysBetweenISO(eloaStart, d)
		}
		c := daysBetweenISO(enlistStart, d) - p
		if c < 0 {
			c = 0
		}
		return c, totalPrior + c
	}
	closeSpan := func(d string) int {
		c, _ := peekSpan(d)
		if enlistStart == "" {
			return 0
		}
		totalPrior += c
		enlistStart, eloaPause, eloaStart = "", 0, ""
		return c
	}

	for _, r := range recs {
		t, d := r.Text, r.Date
		toReserve := viiReserveRe.MatchString(t)
		deptActive := viiBilletType(staffRole) == "staff"
		switch {
		case viiEnlistRe.MatchString(t) || (viiReinstateRe.MatchString(t) && !toReserve):
			if status != "active" {
				status = "active"
				// A span already open (reserve + departmental duty) continues
				// uninterrupted, because departmental service was still service.
				if enlistStart == "" {
					enlistStart = d
					eloaPause, eloaStart = 0, ""
				}
			}
		case viiReinstateRe.MatchString(t) && toReserve:
			closeSpan(d)
			status = "reserve"
		case viiEloaStartRe.MatchString(t):
			if status == "active" {
				status = "eloa"
				eloaStart = d
			}
		case viiEloaEndRe.MatchString(t):
			if status == "eloa" && eloaStart != "" {
				eloaPause += daysBetweenISO(eloaStart, d)
				eloaStart = ""
			}
			status = "active"
			if enlistStart == "" {
				enlistStart = d
			}
		case viiRetiredRe.MatchString(t) && !viiInLieuRe.MatchString(t):
			c := closeSpan(d)
			departures = append(departures, viiDeparture{Date: d, Type: "retire", ConsecDays: c, TotalDays: totalPrior, Text: t})
			status = "out"
		case viiToReservesRe.MatchString(t):
			c, tot := peekSpan(d)
			departures = append(departures, viiDeparture{Date: d, Type: "reserve", ConsecDays: c, TotalDays: tot, Text: t})
			if !deptActive {
				closeSpan(d)
			}
			status = "reserve"
		case viiDischargeRe.MatchString(t) && !viiDowngradeRe.MatchString(t) && !viiUpgradeRe.MatchString(t):
			closeSpan(d)
			status = "out"
		case viiMemorialRe.MatchString(t):
			closeSpan(d)
			status = "out"
		}

		// Staff-duty tracking (after the state switch: the transfer cases read
		// the pre-record duty state). Only real billets update the single duty
		// slot; a non-billet capture like "Reserves" leaves a continuing
		// departmental duty in place (dept members are routinely reservists).
		// A relief clears the slot; so does any billet-dropping departure
		// (retirement, discharge, death); otherwise a pre-departure duty
		// would leak into a later reserve span and inflate the timer.
		switch {
		case viiRetiredRe.MatchString(t) && !viiInLieuRe.MatchString(t),
			viiDischargeRe.MatchString(t) && !viiDowngradeRe.MatchString(t) && !viiUpgradeRe.MatchString(t),
			viiMemorialRe.MatchString(t),
			viiRelievedRe.MatchString(t):
			staffRole = ""
		default:
			if b := capturedBillet(t); b != "" && viiBilletType(b) != "" {
				staffRole = b
			}
		}
		// While in the Reserves, departmental service keeps the timer running:
		// picking up a staff duty opens a span, losing it closes the span and
		// records the deferred reserve departure.
		nowDept := viiBilletType(staffRole) == "staff"
		if status == "reserve" {
			if nowDept && enlistStart == "" {
				enlistStart, eloaPause, eloaStart = d, 0, ""
			} else if !nowDept && enlistStart != "" {
				c := closeSpan(d)
				departures = append(departures, viiDeparture{Date: d, Type: "reserve", ConsecDays: c, TotalDays: totalPrior, Text: t})
			}
		}
	}

	cur := 0
	if enlistStart != "" {
		p := eloaPause
		if status == "eloa" && eloaStart != "" {
			p += daysBetweenISO(eloaStart, asOf)
		}
		cur = daysBetweenISO(enlistStart, asOf) - p
		if cur < 0 {
			cur = 0
		}
	}
	return viiService{Departures: departures, TotalActive: totalPrior + cur, CurrentConse: cur}
}

var (
	viiDiscRe       = regexp.MustCompile(`(?i)Letter of Reprimand|Negative Counsell?ing|No Favorable Action|\bNFA\b|Article\s*\d`)
	viiArticleRe    = regexp.MustCompile(`(?i)Article\s*(?:15|32|\d+)`)
	viiNoNFARe      = regexp.MustCompile(`(?i)No(?:t)?\s+(?:Additional\s+)?NFA|No Favorable Action`)
	viiNFADaysRe    = regexp.MustCompile(`(?i)(\d+)\s*Days?\s*(?:NFA|No Favorable Action)`)
	viiNFAForRe     = regexp.MustCompile(`(?i)No Favorable Action for\s*(\d+)\s*Days?`)
	viiHasNFADaysRe = regexp.MustCompile(`(?i)\d+\s*Days?\s*NFA`)
	viiDeferredRe   = regexp.MustCompile(`(?i)to be served on re-?enlistment`)
)

type viiDiscipline struct {
	Date      string
	NFADays   int
	IsArticle bool
	Deferred  bool
	Text      string
}

func parseDiscipline(recs []viiRecord) []viiDiscipline {
	var out []viiDiscipline
	for _, r := range recs {
		t := r.Text
		isDisc := r.Type == "RECORD_TYPE_DISCIPLINARY" || viiDiscRe.MatchString(t)
		if !isDisc {
			continue
		}
		nfaDays := 0
		// "No NFA" / bare "No Favorable Action" (without a day count) = zero;
		// Go's regexp has no lookahead, so the source (?!\s+for) is expressed as
		// "matches the no-NFA phrasing AND has no explicit day count".
		bareNoNFA := viiNoNFARe.MatchString(t) && !viiNFAForRe.MatchString(t) && !viiHasNFADaysRe.MatchString(t)
		if !bareNoNFA {
			if m := viiNFADaysRe.FindStringSubmatch(t); m != nil {
				nfaDays, _ = strconv.Atoi(m[1]) // regex guarantees digits
			} else if m := viiNFAForRe.FindStringSubmatch(t); m != nil {
				nfaDays, _ = strconv.Atoi(m[1]) // regex guarantees digits
			}
		}
		out = append(out, viiDiscipline{
			Date:      r.Date,
			NFADays:   nfaDays,
			IsArticle: viiArticleRe.MatchString(t),
			Deferred:  viiDeferredRe.MatchString(t),
			Text:      t,
		})
	}
	return out
}

func activeNFAAt(dl []viiDiscipline, date string) bool {
	for _, d := range dl {
		if d.NFADays > 0 && !d.Deferred && d.Date <= date && date < addDaysISO(d.Date, d.NFADays) {
			return true
		}
	}
	return false
}

func articleWithinYear(dl []viiDiscipline, date string) bool {
	for _, d := range dl {
		diff := daysBetweenISO(d.Date, date)
		if d.IsArticle && diff >= 0 && diff <= 365 {
			return true
		}
	}
	return false
}

func viiMeetsTIS(consec, total int) bool {
	return consec >= viiConsecDays || total >= viiTotalDays
}

func retEligibleAt(consec, total int, dl []viiDiscipline, date string) bool {
	return viiMeetsTIS(consec, total) && !activeNFAAt(dl, date) && !articleWithinYear(dl, date)
}

// ---------------------------------------------------------------------------
// Eligibility walk
// ---------------------------------------------------------------------------

// viiResult is the eligibility verdict for one member. Trimmed to the fields
// the bot needs; the source analyze also returns display-only context (ops/class
// counts, medal lists, TIG/TIS strings) that has no consumer here.
type viiResult struct {
	// EligibleNow: the member has a qualifying departure (retirement, or a
	// reserve transfer while retirement-eligible) AND a previously-held,
	// more-senior rank fits the current billet's ceiling in the same billet
	// type, with clean standing and no retirement downgrade.
	EligibleNow bool
	// Target is the restorable rank (warrant/NCO display equivalence
	// applied). Nil unless a candidate rank was found.
	Target *viiRank
	// TargetHeldDate/Role: evidence of where/when the target rank was held.
	TargetHeldDate     string
	TargetHeldRole     string
	TargetHeldSameRole bool
	// PotentiallyEligible: a grantable rank exists but is held up by a
	// retirement downgrade or standing block, or the billet has no ceiling
	// in the table (manual review).
	PotentiallyEligible bool
	PotentialRank       *viiRank
	Reasons             []string
	// Qualifies: the member has a qualifying departure (retirement, or a
	// reserve transfer while retirement-eligible). Gates EligibleNow and
	// PotentiallyEligible, a deliberate deviation from the source, which
	// carried this as context only (S1 clarification, 2026-07-23).
	Qualifies  bool
	Downgraded bool
	Blocked    bool
}

// Reason joins Reasons for display.
func (v viiResult) Reason() string { return strings.Join(v.Reasons, " ") }

type viiHeld struct {
	rank       *viiRank
	types      map[string]bool
	dateByType map[string]string
	roleDate   map[string]string
}

// viiAnalyze ports the source analyze() eligibility path. asOf pins "today" for
// standing checks, mirroring the /promo date filter.
func viiAnalyze(profile *utils.ProfileResponse, m *viiRankModel, asOf time.Time) viiResult {
	asOfISO := asOf.Format("2006-01-02")

	recs := make([]viiRecord, 0, len(profile.Records))
	for _, r := range profile.Records {
		date := r.RecordDate
		if len(date) > 10 {
			date = date[:10]
		}
		if date == "" || r.RecordDetails == "" {
			continue
		}
		recs = append(recs, viiRecord{Date: date, Text: r.RecordDetails, Type: r.RecordType})
	}
	// ISO dates sort lexically.
	for i := 1; i < len(recs); i++ {
		for j := i; j > 0 && recs[j].Date < recs[j-1].Date; j-- {
			recs[j], recs[j-1] = recs[j-1], recs[j]
		}
	}

	service := computeService(recs, asOfISO)
	discipline := parseDiscipline(recs)

	downgraded := false
	downgradeText := ""
	for _, r := range recs {
		if viiDowngradeRe.MatchString(r.Text) {
			downgraded = true
			downgradeText = r.Text
			break
		}
	}

	// Qualifying departures gate §VII outright (S1 clarification, 2026-07-23):
	// an actual retirement, or a reserve transfer made while
	// retirement-eligible (reserves in lieu of retirement). No qualifying
	// departure (including a "Returned from Retirement" with no parseable
	// departure, where the held ranks can't be trusted either) means the
	// veteran-retention path simply does not apply.
	var qDeps []viiDeparture
	for _, d := range service.Departures {
		if d.Type == "retire" || (d.Type == "reserve" && retEligibleAt(d.ConsecDays, d.TotalDays, discipline, d.Date)) {
			qDeps = append(qDeps, d)
		}
	}
	qualifies := len(qDeps) > 0

	// Walk records (billet BEFORE rank). Build held: for each rank LEVEL the
	// member ever held, the set of billet TYPES it was held in. A rank counts
	// as "earned in" a type if held in that type AT ANY TIME (CoC rule:
	// promoted in staff but later serving at that rank in a line billet =
	// line).
	var running *viiRank
	curRole := ""
	held := map[int]*viiHeld{}

	stamp := func(rk *viiRank, billetT, role, date string) {
		if rk == nil || billetT == "" {
			return
		}
		lvl, ok := m.level(rk)
		if !ok {
			return
		}
		h, exists := held[lvl]
		if !exists {
			h = &viiHeld{rank: rk, types: map[string]bool{}, dateByType: map[string]string{}, roleDate: map[string]string{}}
			held[lvl] = h
		}
		h.types[billetT] = true
		if date != "" {
			if prev, ok := h.dateByType[billetT]; !ok || date < prev {
				h.dateByType[billetT] = date // earliest at that rank in that type
			}
			if role != "" {
				if prev, ok := h.roleDate[role]; !ok || date < prev {
					h.roleDate[role] = date // ...and per billet role
				}
			}
		}
	}

	for _, r := range recs {
		b := billetFromRecord(r.Text)
		// A primary billet changes only via "Transferred and Assigned" /
		// "Reassigned to"; a bare "Assigned <staff role>" is a secondary
		// department duty layered on the existing primary (a bare assign to a
		// LINE billet, like boot-camp Trooper or First Sergeant, is still primary).
		if b != "" {
			if viiPrimaryMoveRe.MatchString(r.Text) || viiBilletType(b) == "line" {
				curRole = b
			}
		} else if viiRelievedRe.MatchString(r.Text) && !viiAddlDutyRe.MatchString(r.Text) {
			curRole = ""
		}
		if rk := rankFromRecord(r.Text, m); rk != nil {
			running = rk
		}
		if running != nil {
			stamp(running, viiBilletType(curRole), curRole, r.Date)
		}
	}

	// Current rank; unknown shorts get a sentinel order that classifies as
	// enlisted and outranks nothing (matches the source's order:999 fallback).
	current := m.byShort[profile.Rank.RankShort]
	if current == nil {
		current = &viiRank{Short: profile.Rank.RankShort, Full: profile.Rank.RankFull, Order: 999}
	}
	aviation := viiIsAviation(profile)
	wings := viiHasWings(profile)
	isOfficer := viiTrackOf(current.Order) == "officer"
	sameGroup := func(rk *viiRank) bool { return (viiTrackOf(rk.Order) == "officer") == isOfficer }
	curLvl, curLvlOK := m.level(current)
	if !curLvlOK {
		curLvl = current.Order
	}
	role := normalizeRole(profile.Primary.PositionTitle)
	key := canonicalBillet(role)
	curType := viiBilletType(role)
	if curType == "" {
		curType = "line"
	}
	ceil := viiBilletCeiling[key]
	ceilShort := ceil.Enlisted
	if isOfficer {
		ceilShort = ceil.Officer
	}
	ceilLvl := -1
	if ceilShort != "" {
		if cr, ok := m.byShort[ceilShort]; ok {
			if l, ok := m.level(cr); ok {
				ceilLvl = l
			}
		}
	}

	// Display a rank as a warrant rank when the member is CURRENTLY a warrant
	// officer, or is an aviator with wings; otherwise the NCO equivalent.
	expressWarrant := viiTrackOf(current.Order) == "warrant" || (aviation && wings)
	dispRank := func(rk *viiRank) *viiRank {
		if rk == nil {
			return nil
		}
		e := m.enlEquiv(rk)
		if expressWarrant {
			if w, ok := viiE2W[e.Short]; ok {
				if wr, ok := m.byShort[w]; ok {
					return wr
				}
			}
		}
		return e
	}

	// Eligible rank = the most senior rank ACTUALLY HELD in the same billet
	// type that the CURRENT billet rates (level between ceiling and current).
	// No capping of a too-high rank; they must have held it; if the billet
	// can't rate any held rank above their current one, they're INELIGIBLE.
	type lvlEntry struct {
		lvl int
		h   *viiHeld
	}
	var higherSameType []lvlEntry
	for lvl, h := range held {
		if sameGroup(h.rank) && lvl < curLvl && h.types[curType] {
			higherSameType = append(higherSameType, lvlEntry{lvl, h})
		}
	}
	minBy := func(entries []lvlEntry) *lvlEntry {
		if len(entries) == 0 {
			return nil
		}
		best := &entries[0]
		for i := range entries[1:] {
			if entries[i+1].lvl < best.lvl {
				best = &entries[i+1]
			}
		}
		return best
	}
	hasHigherSameType := len(higherSameType) > 0

	var targetRank *viiRank
	targetHeldDate, targetHeldRole := "", ""
	targetHeldSameRole := false
	if ceilLvl >= 0 {
		var withinCeil []lvlEntry
		for _, e := range higherSameType {
			if e.lvl >= ceilLvl {
				withinCeil = append(withinCeil, e)
			}
		}
		if fit := minBy(withinCeil); fit != nil {
			targetRank = fit.h.rank
			if d, ok := fit.h.roleDate[role]; ok {
				targetHeldDate, targetHeldRole, targetHeldSameRole = d, role, true
			} else {
				bestRole, bestDate := "", ""
				for rr, dd := range fit.h.roleDate {
					if viiBilletType(rr) == curType && (bestDate == "" || dd < bestDate) {
						bestRole, bestDate = rr, dd
					}
				}
				targetHeldRole = bestRole
				if bestDate != "" {
					targetHeldDate = bestDate
				} else {
					targetHeldDate = fit.h.dateByType[curType]
				}
			}
		}
	}

	blocked := activeNFAAt(discipline, asOfISO) || articleWithinYear(discipline, asOfISO)
	eligibleNow := qualifies && targetRank != nil && !downgraded && !blocked

	// Without a qualifying departure the member is not a returning veteran at
	// all, with no "potentially eligible" reasons either.
	var reasons []string
	if qualifies && targetRank != nil && !eligibleNow {
		if downgraded {
			r := "Retirement was downgraded to an Honorable Discharge"
			if downgradeText != "" {
				r += fmt.Sprintf(" (%q)", downgradeText)
			}
			reasons = append(reasons, r+". Per CoC, eligibility is decided case-by-case.")
		}
		if blocked {
			var parts []string
			if activeNFAAt(discipline, asOfISO) {
				parts = append(parts, "active NFA")
			}
			if articleWithinYear(discipline, asOfISO) {
				parts = append(parts, "Article 15/32 within the last year")
			}
			reasons = append(reasons, fmt.Sprintf("Promotion blocked by %s; eligible once standing clears.", strings.Join(parts, " + ")))
		}
	} else if qualifies && targetRank == nil && ceilLvl < 0 && hasHigherSameType {
		displayRole := role
		if displayRole == "" {
			displayRole = "unknown"
		}
		reasons = append(reasons, fmt.Sprintf("Current billet (%s) has no defined rank rating in the billet table; needs manual review.", displayRole))
	}
	potentiallyEligible := len(reasons) > 0
	var potentialRank *viiRank
	if potentiallyEligible {
		pr := targetRank
		if pr == nil {
			pr = minBy(higherSameType).h.rank
		}
		potentialRank = dispRank(pr)
	}

	return viiResult{
		EligibleNow:         eligibleNow,
		Target:              dispRank(targetRank),
		TargetHeldDate:      targetHeldDate,
		TargetHeldRole:      targetHeldRole,
		TargetHeldSameRole:  targetHeldSameRole,
		PotentiallyEligible: potentiallyEligible,
		PotentialRank:       potentialRank,
		Reasons:             reasons,
		Qualifies:           qualifies,
		Downgraded:          downgraded,
		Blocked:             blocked,
	}
}
