package commands

// Promotion-eligibility engine, ported from the author's internal S1
// promotion tooling (private). Requirements are transcribed from 7CAV-R-023
// Rank Promotion and Reduction Guidelines, which is the source of truth for
// every threshold below.
//
// Scope note: this implements the STANDARD promotion ladder only. The
// Veteran Rank Retention (Ch.4 §VII) alternative path from the source tool's
// veteran-retention engine is intentionally out of scope here: it needs full service
// record archaeology and is tracked as a follow-up.

import (
	"regexp"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// promotionRequirement describes what a rank needs to advance to nextRank.
// courses are hard requirements (block eligibility); displayCourses are
// informational only and surface in output without gating.
type promotionRequirement struct {
	NextRank       string
	TigMonths      int // time-in-grade, months (30-day months per R-023 practice)
	TisMonths      int // time-in-service, months; 0 = no TIS requirement
	Type           string
	Courses        []string
	DisplayCourses []string
	// Billets that satisfy the position requirement; nil = no requirement.
	Billets []string
	// MosCodes gates the step on the trooper's MOS (prefix match), for
	// promotions defined by an appointment rather than a position title -
	// COL→BG requires a regiment-HQ MOS. nil = no MOS requirement.
	MosCodes []string
}

// promotionRequirements is keyed by rankShort. Transcribed from the source
// tool's requirements table (7CAV-R-023), with one deliberate deviation: the
// source surfaced ODS as an informational "pending" course on the NCO ladder,
// but ODS only matters for commissioning (which needs a Platoon Leader
// assignment first). S1 asked for it to be dropped from /promo output
// (2026-07-23). DisplayCourses stays plumbed for future informational needs.
var promotionRequirements = map[string]promotionRequirement{
	"PVT": {NextRank: "PFC", TigMonths: 1, Type: "automatic"},
	"PFC": {NextRank: "SPC", TigMonths: 2, Type: "automatic"},
	"SPC": {NextRank: "CPL/WO1", TigMonths: 4, Type: "automatic", Courses: []string{"ncoaPhase1", "ncoaPhase2", "sac"}},
	"CPL": {NextRank: "SGT", TigMonths: 4, TisMonths: 9, Type: "discretionary", Billets: []string{"ASL", "SL"}},
	"WO1": {NextRank: "CW2", TigMonths: 4, TisMonths: 9, Type: "discretionary", Billets: []string{"ASL", "SL"}},
	"SGT": {NextRank: "SSG", TigMonths: 6, TisMonths: 15, Type: "discretionary", Billets: []string{"SL", "PSG"}},
	"CW2": {NextRank: "CW3", TigMonths: 6, TisMonths: 15, Type: "discretionary", Billets: []string{"SL", "PSG"}},
	"SSG": {NextRank: "SFC", TigMonths: 6, TisMonths: 22, Type: "discretionary", Billets: []string{"PSG"}},
	"CW3": {NextRank: "CW4", TigMonths: 6, TisMonths: 22, Type: "discretionary", Billets: []string{"PSG"}},
	"SFC": {NextRank: "MSG", TigMonths: 6, TisMonths: 28, Type: "discretionary", Billets: []string{"PSG"}},
	"CW4": {NextRank: "CW5", TigMonths: 6, TisMonths: 28, Type: "discretionary", Billets: []string{"PSG"}},
	// Officer billet gates: a Platoon Leader rates 2LT-1LT, so PL satisfies
	// the 1LT gate but NOT the CPT gate.
	"2LT": {NextRank: "1LT", TigMonths: 6, Type: "discretionary", Billets: []string{"PL", "XO", "CO", "DEPT"}},
	"1LT": {NextRank: "CPT", TigMonths: 6, Type: "discretionary", Billets: []string{"XO", "CO", "DEPT"}},
	"CPT": {NextRank: "MAJ", TigMonths: 6, Type: "discretionary", Courses: []string{"rdptc"}, Billets: []string{"XO", "CO", "DEPT"}},
	"MAJ": {NextRank: "LTC", TigMonths: 6, Type: "discretionary", Courses: []string{"rdptc"}, Billets: []string{"XO", "CO", "DEPT"}},
	"LTC": {NextRank: "COL", TigMonths: 6, Type: "discretionary"},
	// COL→BG needs a regiment-HQ billet, denoted by a general-staff MOS
	// (S1, 2026-07-25), and nine months at COL: R-023 Ch.2 §III sets the
	// minimum TIG for O-7 at nine months, the only rung whose TIG comes from
	// that section rather than the Chapter 2 table. The same section notes
	// that billet limitations can waive it (Ch.5), which the ladder does not
	// model: a waived promotion simply proposes early, as billet/fast-track
	// promotions do at every other rank.
	"COL": {NextRank: "BG", TigMonths: 9, Type: "discretionary", MosCodes: generalStaffMos},
}

// generalStaffMos are the MOS codes denoting a regiment-HQ (Regimental Staff)
// appointment, which gates COL→BG (S1, 2026-07-25): 00B General Officer, 00Z
// Command Sergeant Major, 01A Officer Generalist. 00D (Officer Pool) is NOT
// included: the MOS table lists it separately from Regimental Staff, and a
// pool officer is not in a regiment-HQ billet.
var generalStaffMos = []string{"00B", "00Z", "01A"}

// courseLabels maps internal course keys to display names.
var courseLabels = map[string]string{
	"ncoaPhase1": "NCOA I",
	"ncoaPhase2": "NCOA II",
	"sac":        "SAC",
	"ods":        "ODS",
	"rdptc":      "RDPTC",
}

// courseCompletions tracks which gating courses a trooper's service record shows.
type courseCompletions struct {
	NcoaPhase1 bool
	NcoaPhase2 bool
	Sac        bool
	Ods        bool
	Rdptc      bool
}

func (c courseCompletions) has(key string) bool {
	switch key {
	case "ncoaPhase1":
		return c.NcoaPhase1
	case "ncoaPhase2":
		return c.NcoaPhase2
	case "sac":
		return c.Sac
	case "ods":
		return c.Ods
	case "rdptc":
		return c.Rdptc
	}
	return false
}

// parseCourseCompletions scans milpac service records for course-graduation
// entries. String heuristics mirror the source implementation, which was
// tuned against real record wording ("Graduated NCOA Warrior Leadership
// Course Phase I", "Graduated NCOA WLC - 01*01-14", "Attended the Server
// Administration Course", ...).
func parseCourseCompletions(records []utils.Record) courseCompletions {
	var c courseCompletions
	for _, record := range records {
		details := strings.ToLower(record.RecordDetails)

		if strings.Contains(details, "ncoa") &&
			(strings.Contains(details, "phase i") || strings.Contains(details, "phase 1") || strings.Contains(details, "wlc")) {
			// "phase i" is a prefix of "phase ii": disambiguate.
			if strings.Contains(details, "phase ii") || strings.Contains(details, "phase 2") {
				c.NcoaPhase2 = true
			} else {
				c.NcoaPhase1 = true
			}
		}
		if strings.Contains(details, "ncoa") &&
			(strings.Contains(details, "phase ii") || strings.Contains(details, "phase 2")) {
			c.NcoaPhase2 = true
		}
		if strings.Contains(details, "server admin") {
			c.Sac = true
		}
		if strings.Contains(details, "officer development school") || strings.Contains(details, " ods") {
			c.Ods = true
		}
		if strings.Contains(details, "disciplinary process training") || strings.Contains(details, "rdptc") {
			c.Rdptc = true
		}
	}
	return c
}

var (
	reSL   = regexp.MustCompile(`\bsl\b`)
	rePL   = regexp.MustCompile(`\bpl\b`)
	reXO   = regexp.MustCompile(`\bxo\b`)
	reDept = regexp.MustCompile(`\bs[0-9]\b|department|\b1ic\b|\b2ic\b|\baide\b|director|\bncoa\b|\brtc\b|\brrd\b`)
)

// detectBillet classifies a free-text MILPAC position title into a billet
// code. Check order matters and mirrors the source: ASL before SL (substring
// overlap), "platoon commander" (PL) before "commander" (CO).
func detectBillet(positionTitle string) string {
	if positionTitle == "" {
		return ""
	}
	t := strings.ToLower(positionTitle)
	switch {
	case strings.Contains(t, "platoon sergeant") || strings.Contains(t, "psg"):
		return "PSG"
	case strings.Contains(t, "first sergeant") || strings.Contains(t, "1sg"):
		return "1SG"
	case strings.Contains(t, "assistant section leader") || strings.Contains(t, "asl"):
		return "ASL"
	case strings.Contains(t, "section leader") || reSL.MatchString(t):
		return "SL"
	case strings.Contains(t, "platoon leader") || strings.Contains(t, "platoon commander") || rePL.MatchString(t):
		return "PL"
	case strings.Contains(t, "executive officer") || reXO.MatchString(t):
		return "XO"
	case strings.Contains(t, "commanding officer") || strings.Contains(t, "commander"):
		return "CO"
	case reDept.MatchString(t):
		return "DEPT"
	case strings.Contains(t, "fire team leader") || strings.Contains(t, "ftl"):
		return "FTL"
	case strings.Contains(t, "rto") || strings.Contains(t, "radio"):
		return "RTO"
	}
	return ""
}

// billetMeetsRequirement reports whether a detected billet satisfies the
// rank's billet gate. nil gate = always satisfied.
func billetMeetsRequirement(detected string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	if detected == "" {
		return false
	}
	for _, b := range required {
		if b == detected {
			return true
		}
	}
	return false
}

// mosMeetsRequirement reports whether the trooper's MOS satisfies the rank's
// MOS gate. Empty gate = always satisfied. Matching is a case-insensitive
// prefix test, mirroring how §VII reads the aviation MOS (^15), since the
// milpac MOS field leads with the code.
func mosMeetsRequirement(mos string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	m := strings.ToUpper(strings.TrimSpace(mos))
	for _, code := range required {
		if strings.HasPrefix(m, strings.ToUpper(code)) {
			return true
		}
	}
	return false
}

// daysBetween returns whole days from dateStr (YYYY-MM-DD) to asOf, or -1 if
// the date is missing/unparseable/sentinel. Mirrors the source daysSince, with
// "now" generalized to an arbitrary as-of date per issue #4's date filter.
func daysBetween(dateStr string, asOf time.Time) int {
	if dateStr == "" || dateStr == "0" || dateStr == "0001-01-01" {
		return -1
	}
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return -1
	}
	if date.Year() < 2000 {
		return -1
	}
	return int(asOf.Sub(date).Hours() / 24)
}

// promoEligibility is the verdict for one trooper.
type promoEligibility struct {
	Eligible       bool
	NoRequirements bool // rank has no ladder entry (e.g. COL and above)
	NextRank       string
	Type           string
	TigMet         bool
	TisMet         bool
	CoursesMet     bool
	BilletMet      bool
	DetectedBillet string
	// RequiredBillets echoes the rank's billet gate for display; nil = none.
	RequiredBillets []string
	// MOS gate (COL→BG): MosMet is true when no MOS is required or the
	// trooper's MOS matches. RequiredMos echoes the gate for display.
	MosMet         bool
	DetectedMos    string
	RequiredMos    []string
	TigDays        int
	TisDays        int
	TigRequired    int
	TisRequired    int
	MissingCourses []string
	// Non-gating courses the trooper hasn't completed (display labels), e.g.
	// ODS pending for an NCO ladder rank. Informational only.
	PendingDisplayCourses []string
	// Days until both time gates are met (0 when met or unknowable). Course
	// and billet gates are point-in-time facts and are NOT projected.
	DaysUntilEligible int
}

// calculatePromotionEligibility evaluates the standard promotion ladder for a
// trooper as of asOf. Port of the source function of the same name, minus the
// §VII veteran-retention alternative path (see file header).
func calculatePromotionEligibility(
	rankShort, promotionDate, joinDate string,
	courses courseCompletions,
	positionTitle, mos string,
	asOf time.Time,
) promoEligibility {
	req, ok := promotionRequirements[rankShort]
	if !ok {
		return promoEligibility{NoRequirements: true, NextRank: "--"}
	}

	tigDays := daysBetween(promotionDate, asOf)
	tisDays := daysBetween(joinDate, asOf)
	tigRequired := req.TigMonths * 30
	tisRequired := req.TisMonths * 30

	tigMet := tigDays >= 0 && tigDays >= tigRequired
	tisMet := req.TisMonths == 0 || (tisDays >= 0 && tisDays >= tisRequired)

	var missing []string
	for _, course := range req.Courses {
		if !courses.has(course) {
			missing = append(missing, course)
		}
	}
	coursesMet := len(missing) == 0

	var pendingDisplay []string
	for _, course := range req.DisplayCourses {
		if !courses.has(course) {
			pendingDisplay = append(pendingDisplay, courseLabels[course])
		}
	}

	detected := detectBillet(positionTitle)
	billetMet := billetMeetsRequirement(detected, req.Billets)

	mosMet := mosMeetsRequirement(mos, req.MosCodes)

	daysUntilTig := 0
	if !tigMet && tigDays >= 0 {
		daysUntilTig = tigRequired - tigDays
	}
	daysUntilTis := 0
	if !tisMet && tisDays >= 0 {
		daysUntilTis = tisRequired - tisDays
	}
	daysUntil := daysUntilTig
	if daysUntilTis > daysUntil {
		daysUntil = daysUntilTis
	}

	return promoEligibility{
		Eligible:              tigMet && tisMet && coursesMet && billetMet && mosMet,
		NextRank:              req.NextRank,
		Type:                  req.Type,
		TigMet:                tigMet,
		TisMet:                tisMet,
		CoursesMet:            coursesMet,
		BilletMet:             billetMet,
		DetectedBillet:        detected,
		RequiredBillets:       req.Billets,
		MosMet:                mosMet,
		DetectedMos:           strings.TrimSpace(mos),
		RequiredMos:           req.MosCodes,
		TigDays:               tigDays,
		TisDays:               tisDays,
		TigRequired:           tigRequired,
		TisRequired:           tisRequired,
		MissingCourses:        missing,
		PendingDisplayCourses: pendingDisplay,
		DaysUntilEligible:     daysUntil,
	}
}

// ---------------------------------------------------------------------------
// Lite-roster pre-filter
// ---------------------------------------------------------------------------
//
// A full milpac fetch costs roughly a second and the roster-wide scopes run
// to hundreds of members, so the expensive call is worth avoiding wherever
// the lite roster already proves it cannot change the answer. The lite
// payload carries rank, primary position, join date and promotion date -
// everything the standard ladder needs except course completions.
//
// This mirrors how /afsm narrows on the roster payload before reaching for
// profiles; /awol and /loa never fetch profiles at all.
//
// How much this actually saves depends almost entirely on the type filter.
// Measured against a representative company (107 members, mixed ranks and
// billets, half recently promoted):
//
//	type=discretionary   93% of fetches skipped
//	type=lateral         71%
//	type=automatic       64%
//	type=vii             17%
//	no type filter        2%
//
// The unfiltered case is close to worthless and that is inherent, not a gap
// to close later: §VII can only be excluded where the billet ceiling is no
// more senior than the rank already held, which is rare below the top of a
// billet, and the lateral path has to open every NCO record to look for
// flight wings. Anything better for unfiltered runs (the weekly sweep
// included) needs a profile cache rather than a smarter filter.
//
// The one invariant that matters: the pre-filter must never drop somebody
// evaluatePromoMember would have returned as a candidate. Every check below
// is therefore written to fail towards fetching: unknown rank, missing
// date, or absent position title all mean "cannot tell, so fetch".

// promoAllCourses is a course record with everything marked complete. Feeding
// it to the ladder answers "could this member qualify if their record turns
// out to be perfect?", which is exactly the question the lite payload can
// answer without the record itself.
var promoAllCourses = courseCompletions{
	NcoaPhase1: true, NcoaPhase2: true, Sac: true, Ods: true, Rdptc: true,
}

// needsProfile reports whether any promotion path the filter still allows
// could apply to a member, judging only from lite-roster data. False means the
// milpac fetch cannot change the output and is skipped.
//
// The filter matters a great deal here: an include (type) filter narrows to a
// single path so whole rank groups can be ruled out before the fetch; an
// exclude filter keeps every path but the excluded one, which is the same set
// of predicates OR'd together. An unfiltered run has to keep every path open
// and saves comparatively little, because §VII can only be ruled out where the
// billet ceiling is no more senior than the rank already held, and the lateral
// path has to fetch every NCO to see their wings.
func (f promoFilter) needsProfile(member utils.LiteProfileResponse, asOf time.Time, rankModel *viiRankModel) bool {
	// A path is "kept" unless it is the excluded one or (for an include
	// filter) not the included one.
	keeps := func(path string) bool {
		if f.exclude != "" {
			return path != f.exclude
		}
		if f.include != "" {
			return path == f.include
		}
		return true
	}
	// For automatic/discretionary the ladder type must also match; §VII and
	// lateral have their own possibility checks.
	standardKind := func(kind string) bool {
		return keeps(kind) && ladderTypeMatches(member, kind) && standardPathPossible(member, asOf)
	}
	return standardKind(promoTypeAutomatic) ||
		standardKind(promoTypeDiscretionary) ||
		(keeps(promoTypeVii) && viiPathPossible(member, rankModel)) ||
		(keeps(promoTypeLateral) && lateralPathPossible(member))
}

// ladderTypeMatches reports whether the member's next rung is of the
// requested kind. The ladder type is a property of the current rank alone, so
// a run filtered to automatic promotions need never open a discretionary
// rank's record. An unknown rank has no rung and cannot match.
func ladderTypeMatches(member utils.LiteProfileResponse, promoType string) bool {
	req, ok := promotionRequirements[member.Rank.RankShort]
	return ok && req.Type == promoType
}

// standardPathPossible tests the ladder's non-course gates against lite data.
// Courses are assumed complete because the lite payload cannot see them: if a
// member fails TIG, TIS or billet even with a perfect course record, no
// profile fetch can rescue them.
func standardPathPossible(member utils.LiteProfileResponse, asOf time.Time) bool {
	// Absent dates or position mean the lite record cannot settle the
	// question: a milpac with a blank promotion date is a data problem, not
	// evidence of ineligibility, so defer to the full profile.
	if member.PromotionDate == "" || member.JoinDate == "" || member.Primary.PositionTitle == "" {
		return true
	}
	// The lite roster carries no MOS, so a MOS-gated step (COL→BG) can't be
	// judged here: always fetch to check.
	if len(promotionRequirements[member.Rank.RankShort].MosCodes) > 0 {
		return true
	}
	return calculatePromotionEligibility(
		member.Rank.RankShort,
		member.PromotionDate,
		member.JoinDate,
		promoAllCourses,
		member.Primary.PositionTitle,
		"", // MOS unknown from lite data; ranks needing it are handled above
		asOf,
	).Eligible
}

// lateralPathPossible reports whether the NCO-to-warrant move is even
// available at this rank. Flight wings and the aviation MOS live on the full
// profile, so any rank with a warrant equivalent still has to be fetched;
// this rules out the junior enlisted ranks below CPL, which is most of a
// roster.
func lateralPathPossible(member utils.LiteProfileResponse) bool {
	_, ok := viiE2W[strings.ToUpper(member.Rank.RankShort)]
	return ok
}

// viiPathPossible reports whether Veteran Rank Retention could produce a
// target for this member. §VII restores a previously held rank only up to
// what the current billet rates, so if the billet ceiling is no more senior
// than the rank already held, no service history can qualify them and the
// records fetch is pointless.
func viiPathPossible(member utils.LiteProfileResponse, m *viiRankModel) bool {
	if m == nil {
		return false // ranks fetch failed; the §VII path is off this pass
	}
	current := m.byShort[member.Rank.RankShort]
	if current == nil {
		return true // unrecognised rank: don't guess
	}
	curLvl, ok := m.level(current)
	if !ok {
		return true
	}
	if member.Primary.PositionTitle == "" {
		// Without a billet there is no ceiling to compare against, and an
		// absent title is not the same as a member billet: treating it as
		// one would silently drop a returning veteran whose real billet
		// rates well above their current rank.
		return true
	}

	ceil := viiBilletCeiling[canonicalBillet(normalizeRole(member.Primary.PositionTitle))]
	ceilShort := ceil.Enlisted
	if viiTrackOf(current.Order) == "officer" {
		ceilShort = ceil.Officer
	}
	if ceilShort == "" {
		// No ceiling defined for this billet on this track: viiAnalyze
		// cannot produce a restoration target either, so nothing to fetch
		// for. (It may still flag the billet for manual review, but that
		// never makes somebody a candidate.)
		return false
	}
	ceilRank := m.byShort[ceilShort]
	if ceilRank == nil {
		return true
	}
	ceilLvl, ok := m.level(ceilRank)
	if !ok {
		return true
	}
	// Lower level is more senior; a target can only exist strictly above the
	// rank currently held.
	return ceilLvl < curLvl
}
