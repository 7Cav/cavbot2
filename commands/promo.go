package commands

// /promo: S1 promotion checker (issue 7Cav/cavbot2#4).
//
// Lists every trooper in a scope (a position/unit, or all Active Duty
// holders of one rank) whose promotion would be possible as of a given date
// (default today), via either the standard ladder (promo_eligibility.go) or
// the Veteran Rank Retention Ch.4 §VII alternative path (vii.go). Both
// engines are ports of the author's internal S1 promotion tooling.

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/sync/errgroup"
)

// Promotion-path values for the `type`/`exclude` filter options. automatic
// and discretionary are the standard-ladder Type values from
// promotionRequirements; vii and lateral are the alternative paths.
const (
	promoTypeAutomatic     = "automatic"
	promoTypeDiscretionary = "discretionary"
	promoTypeVii           = "vii"
	promoTypeLateral       = "lateral"
)

// promoFilter narrows the candidate list. include keeps only candidates with
// that path; exclude drops candidates whose ONLY path is the excluded one. The
// two are mutually exclusive (validated in runPromo); both empty = no filter.
type promoFilter struct {
	include string
	exclude string
}

// apply narrows candidates per the filter (no-op when neither is set).
func (f promoFilter) apply(candidates []promoCandidate) []promoCandidate {
	switch {
	case f.include != "":
		return filterPromoCandidates(candidates, f.include)
	case f.exclude != "":
		return filterPromoExcluding(candidates, f.exclude)
	default:
		return candidates
	}
}

// phrase is the noun phrase for prose ("automatic promotion", "promotion
// excluding §VII", or plain "promotion").
func (f promoFilter) phrase() string { return promoFilterPhrase(f.include, f.exclude) }

// tag labels filenames and logs ("automatic", "excl-vii", or "").
func (f promoFilter) tag() string {
	if f.exclude != "" {
		return "excl-" + f.exclude
	}
	return f.include
}

// promoCandidate is one line of /promo output.
type promoCandidate struct {
	Username  string
	MilpacURL string
	RankShort string
	// Primary is the trooper's primary billet (milpac position title).
	Primary string
	Verdict promoEligibility
	// Vii is non-nil when the §VII path was evaluated; ViaVII marks a
	// candidate eligible through §VII (standard gates may be unmet).
	Vii    *viiResult
	ViaVII bool
	// LateralTarget is the warrant rank a winged aviator NCO can move to
	// laterally (e.g. CPL → WO1); empty when the lateral path doesn't apply.
	LateralTarget string
}

// promoMessageLimit is the per-message packing bound, under Discord's
// 2000-character cap with headroom for the sweep's header prefix. Long
// candidate lists span multiple messages instead of truncating.
const promoMessageLimit = 1900

// promoOverflowReserve is the space held back from promoMessageLimit for the
// "…and N more" notice, so appending it can never overflow the message.
const promoOverflowReserve = 80

// promoDisclaimer heads every /promo output (list and single-trooper), since
// both derive eligibility from hand-entered milpac data.
const promoDisclaimer = "⚠️ Due to potential discrepancies in MILPAC notation, this command may produce inaccurate results. Treat the output of this command as a candidate list, not a guarantee. Please report inaccurate outputs to S6 so that we may investigate and repair."

// promoRankOrder lists milpac rank short forms most-senior-first (military
// precedence: officers, then warrants, then enlisted, the milpac display
// order). It drives candidate-list sorting; ranks not listed sort last.
var promoRankOrder = []string{
	"GOA", "GEN", "LTG", "MG", "BG", "COL", "LTC", "MAJ", "CPT", "1LT", "2LT",
	"CW5", "CW4", "CW3", "CW2", "WO1",
	"CSM", "SGM", "1SG", "MSG", "SFC", "SSG", "SGT", "CPL", "SPC", "PFC", "PVT", "RCT",
}

var promoRankSeniority = func() map[string]int {
	m := make(map[string]int, len(promoRankOrder))
	for idx, rank := range promoRankOrder {
		m[rank] = idx
	}
	return m
}()

// promoRankIndex returns the sort key for a rank short form: seniority index
// (lower = more senior), with unknown ranks after every known one.
func promoRankIndex(rankShort string) int {
	if idx, ok := promoRankSeniority[strings.ToUpper(rankShort)]; ok {
		return idx
	}
	return len(promoRankOrder)
}

// promoNextRankIndex is the seniority sort key for a "next rank" cell, taking
// the first rank of a compound target (SPC's "CPL/WO1" sorts as CPL).
func promoNextRankIndex(nextRank string) int {
	return promoRankIndex(strings.SplitN(nextRank, "/", 2)[0])
}

// promoDate renders dates in the org's DDMMMYY style (e.g. 23JUL26).
func promoDate(t time.Time) string {
	return strings.ToUpper(t.Format("02Jan06"))
}

// parsePromoDate parses user-entered dates in the org's DDMMMYY style
// (case-insensitive, e.g. 30JUL26); YYYY-MM-DD is accepted as a fallback.
func parsePromoDate(s string) (time.Time, error) {
	v := strings.TrimSpace(s)
	if len(v) == 7 {
		// Go month names parse case-sensitively, so canonicalize to "02Jan06".
		canon := v[:2] + strings.ToUpper(v[2:3]) + strings.ToLower(v[3:5]) + v[5:]
		if t, err := time.Parse("02Jan06", canon); err == nil {
			return t, nil
		}
	}
	return time.Parse("2006-01-02", v)
}

// scopeDisplay renders a scope label for prose: the activeduty sentinel reads
// "active duty"; a position/unit or rank code is upper-cased (ACD, S1, PFC)
// regardless of how the user typed it, since those are always upper-case in
// milpac usage.
func scopeDisplay(scope string) string {
	if isActiveDutyScope(scope) {
		return "active duty"
	}
	return strings.ToUpper(scope)
}

// promoPhrase is the "<type> promotion" noun phrase for sentences, e.g.
// "No active duty members eligible for lateral promotion as of 23JUL26".
func promoPhrase(promoType string) string {
	switch promoType {
	case "":
		return "promotion"
	case promoTypeVii:
		return "§VII promotion"
	default:
		return promoType + " promotion"
	}
}

// promoTypeWord is a single filter value rendered for prose (§VII for vii).
func promoTypeWord(t string) string {
	if t == promoTypeVii {
		return "§VII"
	}
	return t
}

// promoFilterPhrase is the noun phrase reflecting the active filter: the
// include phrase for a type filter, or "promotion excluding <path>" for an
// exclude filter (the two are mutually exclusive).
func promoFilterPhrase(promoType, excludeType string) string {
	if excludeType != "" {
		return "promotion excluding " + promoTypeWord(excludeType)
	}
	return promoPhrase(promoType)
}

// upperFirst capitalizes the leading ASCII letter for sentence starts.
func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

func Promo() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "promo",
			Description: "Check promotion eligibility (standard ladder + §VII) for a position scope or one trooper",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "position",
					Description: "Position/unit (fuzzy, e.g. 'ACD', '1-7', 'S1') or 'activeduty' for the whole roster (default)",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "user",
					Description: "Check one trooper by forum username (full verdict breakdown)",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "rank",
					Description: "Check every active-duty trooper at this rank (milpac short form, e.g. PFC)",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "as_of",
					Description: "Check eligibility as of this date (DDMMMYY, e.g. 30JUL26; default today)",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "force_file_output",
					Description: "Always attach the full candidate list as a file. Long lists attach automatically.",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "type",
					Description: "Only list one promotion path",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "automatic", Value: promoTypeAutomatic},
						{Name: "discretionary", Value: promoTypeDiscretionary},
						{Name: "vii (veteran rank retention)", Value: promoTypeVii},
						{Name: "lateral", Value: promoTypeLateral},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "exclude",
					Description: "Hide one promotion path (e.g. exclude:vii shows everyone except §VII-only candidates)",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "automatic", Value: promoTypeAutomatic},
						{Name: "discretionary", Value: promoTypeDiscretionary},
						{Name: "vii (veteran rank retention)", Value: promoTypeVii},
						{Name: "lateral", Value: promoTypeLateral},
					},
				},
			},
		},
		Handler: handlePromoCommand,
	}
}

func handlePromoCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runPromo(utils.NewSessionResponder(s), i, time.Now())
}

// runPromo is the testable core; now pins "today" for tests.
func runPromo(r utils.InteractionResponder, i *discordgo.InteractionCreate, now time.Time) {
	username, discordID := interactionUsernameAndID(i)
	utils.Info("🚀 Starting Promo Check", "command", "Promo", "username", username, "discord_id", discordID)

	position, user, rank, promoType, excludeType := "", "", "", "", ""
	forceFile := false
	asOf := now
	for _, opt := range i.ApplicationCommandData().Options {
		switch opt.Name {
		case "position":
			position = opt.StringValue()
		case "user":
			user = opt.StringValue()
		case "rank":
			rank = opt.StringValue()
		case "type":
			promoType = opt.StringValue()
		case "exclude":
			excludeType = opt.StringValue()
		case "force_file_output":
			forceFile = opt.BoolValue()
		case "as_of":
			parsed, err := parsePromoDate(opt.StringValue())
			if err != nil {
				utils.HandleError(r, i, fmt.Sprintf("❌ Invalid as_of date %q. Use DDMMMYY (e.g. 30JUL26).", opt.StringValue()))
				return
			}
			asOf = parsed
		}
	}
	utils.Debug("🔍 Processing promo options", "position", position, "user", user, "rank", rank, "type", promoType, "as_of", asOf.Format("2006-01-02"))

	// `user` is a single-trooper check and stands alone; `position` and `rank`
	// combine (position scopes the roster, rank narrows within it). Default to
	// the whole Active Duty roster when no scope is given.
	if user != "" {
		if position != "" || rank != "" {
			utils.HandleError(r, i, "❌ `user` checks one trooper on its own; don't combine it with `position` or `rank`.")
			return
		}
		runPromoUser(r, i, user, asOf)
		return
	}
	if promoType != "" && excludeType != "" {
		utils.HandleError(r, i, "❌ Use either `type` (show only one path) or `exclude` (hide one path), not both.")
		return
	}
	if position == "" {
		position = promoActiveDutyScope
	}
	rank = strings.ToUpper(strings.TrimSpace(rank))

	// Scope label: the position, optionally narrowed by a rank (e.g. "ACD PVT",
	// "active duty PVT"). Prose uses scopeDisplay/promoFilterPhrase; filenames
	// and logs keep the raw scope (plus a filter tag for logs).
	scope := scopeDisplay(position)
	if rank != "" {
		scope = strings.TrimSpace(scope + " " + rank)
	}
	filterTag := promoType
	if excludeType != "" {
		filterTag = "excl-" + excludeType
	}
	logScope := scope
	if filterTag != "" {
		logScope += " (" + filterTag + ")"
	}

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Checking %s eligibility for %s as of %s...", promoFilterPhrase(promoType, excludeType), scope, promoDate(asOf)),
		},
	})
	if err != nil {
		utils.CaptureError("❌ Interaction response failed", err)
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	// Same fan-out shape as AFSM: per-member milpac fetches are ~1.4s each and
	// rosters run 50+, so serial evaluation would blow the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	filter := promoFilter{include: promoType, exclude: excludeType}
	res, err := collectPromoCandidates(ctx, position, rank, asOf, filter)
	if err != nil {
		utils.CaptureError("❌ Roster fetch failed", err)
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to fetch roster: %v", err))
		return
	}
	// Position/rank are user-supplied, so an empty roster is a plausible user
	// outcome: message only, no Sentry (ADR 0002).
	if res.EmptyRoster {
		switch {
		case rank != "" && !isActiveDutyScope(position):
			utils.HandleError(r, i, fmt.Sprintf("❌ No %s troopers hold rank %s.", scopeDisplay(position), rank))
		case rank != "":
			utils.HandleError(r, i, fmt.Sprintf("❌ No active-duty troopers hold rank %s. Use the milpac short form (e.g. PFC, SGT, CW2).", rank))
		default:
			utils.HandleError(r, i, emptyRosterSearchMessage(position))
		}
		return
	}
	res.Candidates = filter.apply(res.Candidates)

	message, omitted := formatPromoMessage(scope, filter, asOf, res.Candidates, res.SkippedCount, res.ViiActive)
	attach := forceFile || omitted > 0
	edit := &discordgo.WebhookEdit{Content: &message}
	if attach {
		// The message keeps the inline list (and its "…and N more" notice);
		// the full detail rides along as both a CSV (for spreadsheets) and an
		// HTML report (for reading with clickable links).
		edit.Files = []*discordgo.File{
			promoCSVFile(scope, filter, asOf, res),
			promoReportFile(scope, filter, asOf, res),
		}
	}
	if err := r.InteractionResponseEdit(i.Interaction, edit); err != nil {
		captureDeferredEditFailure(i, "Promo", err)
		return
	}
	utils.Info("✨ Done!", "command", "Promo", "scope", logScope,
		"eligible", len(res.Candidates), "omitted_from_message", omitted, "attached_file", attach)
}

// candidateFilterCategories lists the filter categories a candidate matches:
// its standard ladder type (automatic/discretionary) if standard-eligible,
// plus vii and/or lateral for those paths. These are the values the type and
// exclude filters key off. A candidate in the list always has at least one.
func candidateFilterCategories(c promoCandidate) []string {
	var cats []string
	if c.Verdict.Eligible {
		cats = append(cats, c.Verdict.Type) // automatic or discretionary
	}
	if c.ViaVII {
		cats = append(cats, promoTypeVii)
	}
	if c.LateralTarget != "" {
		cats = append(cats, promoTypeLateral)
	}
	return cats
}

// filterPromoCandidates keeps only candidates matching the given promotion
// path. automatic/discretionary require STANDARD eligibility of that ladder
// type; vii keeps §VII eligibles and lateral keeps winged-aviator warrant
// moves, both regardless of standard-ladder status.
func filterPromoCandidates(candidates []promoCandidate, promoType string) []promoCandidate {
	filtered := make([]promoCandidate, 0, len(candidates))
	for _, c := range candidates {
		for _, cat := range candidateFilterCategories(c) {
			if cat == promoType {
				filtered = append(filtered, c)
				break
			}
		}
	}
	return filtered
}

// filterPromoExcluding drops candidates whose ONLY path is the excluded one -
// a candidate keeps its place if it has any other reason to be promoted. So
// exclude:vii hides §VII-only troopers but keeps someone eligible via both the
// standard ladder and §VII.
func filterPromoExcluding(candidates []promoCandidate, excludeType string) []promoCandidate {
	filtered := make([]promoCandidate, 0, len(candidates))
	for _, c := range candidates {
		for _, cat := range candidateFilterCategories(c) {
			if cat != excludeType {
				filtered = append(filtered, c)
				break
			}
		}
	}
	return filtered
}

// evaluatePromoMember returns (candidate, nil) when the trooper is eligible
// for promotion as of asOf, (nil, nil) when not, and (nil, err) when the
// record could not be processed and should be skipped + reported.
func evaluatePromoMember(
	ctx context.Context,
	member utils.LiteProfileResponse,
	asOf time.Time,
	rankModel *viiRankModel,
) (*promoCandidate, error) {
	utils.Debug("👤 Processing member", "username", member.User.Username)

	rankShort := member.Rank.RankShort
	_, hasLadder := promotionRequirements[rankShort]
	if !hasLadder && rankModel == nil {
		// No standard ladder for this rank (COL+, or unrecognized) and no
		// §VII path this run: not a candidate, not worth a milpac fetch.
		// With a rank model present the fetch must happen: §VII can restore a
		// previously-held rank regardless of the standard ladder.
		return nil, nil
	}

	// Course completions and §VII service history live in the full profile.
	fullProfile, err := utils.GetMilpacByUsername(ctx, member.User.Username)
	if err != nil {
		return nil, fmt.Errorf("milpac fetch failed: %w", err)
	}

	courses := parseCourseCompletions(fullProfile.Records)
	verdict := calculatePromotionEligibility(
		rankShort,
		fullProfile.PromotionDate,
		fullProfile.JoinDate,
		courses,
		fullProfile.Primary.PositionTitle,
		fullProfile.Mos,
		asOf,
	)

	var vii *viiResult
	if rankModel != nil {
		v := viiAnalyze(fullProfile, rankModel, asOf)
		vii = &v
	}
	viaVII := vii != nil && vii.EligibleNow

	// Lateral path: an NCO whose rank has a warrant equivalent moves across
	// when flight-qualified (wings + 15-series MOS, the same pairing the §VII
	// engine uses for warrant display), e.g. CPL → WO1.
	lateralTarget := ""
	if target, ok := viiE2W[strings.ToUpper(rankShort)]; ok &&
		viiHasWings(fullProfile) && viiIsAviation(fullProfile) {
		lateralTarget = target
	}

	if !verdict.Eligible && !viaVII && lateralTarget == "" {
		utils.Debug("⏳ Member not eligible", "username", member.User.Username, "days_until", verdict.DaysUntilEligible)
		return nil, nil
	}

	milpacID, err := utils.ExtractMilpacIDFromUniformURL(member.UniformUrl)
	if err != nil {
		return nil, fmt.Errorf("uniform URL parse failed: %w", err)
	}

	utils.Info("✨ Member eligible for promotion", "username", member.User.Username, "next_rank", verdict.NextRank, "via_vii", viaVII, "lateral", lateralTarget)
	return &promoCandidate{
		Username:      member.User.Username,
		MilpacURL:     fmt.Sprintf("https://7cav.us/rosters/profile/%s", milpacID),
		RankShort:     rankShort,
		Primary:       fullProfile.Primary.PositionTitle,
		Verdict:       verdict,
		Vii:           vii,
		ViaVII:        viaVII,
		LateralTarget: lateralTarget,
	}, nil
}

// formatPromoMessage renders the candidate list as a single Discord message
// and reports how many candidates did not fit. A roster-wide scope can return
// hundreds of candidates (far more than Discord will carry and more than
// anyone wants paged through a channel), so the message shows as many as fit
// and the caller attaches the full report when omitted > 0. The disclaimer
// always renders: eligibility is parsed from user-entered milpac data, so
// formatting drift can silently skew results (same rationale as /afsm).
func formatPromoMessage(scope string, filter promoFilter, asOf time.Time, candidates []promoCandidate, skippedCount int, viiActive bool) (string, int) {
	footer := promoFooter(skippedCount, viiActive)

	var b strings.Builder
	b.WriteString(promoDisclaimer)
	b.WriteString("\n\n")

	if len(candidates) == 0 {
		b.WriteString(fmt.Sprintf("No %s members eligible for %s as of %s", scope, filter.phrase(), promoDate(asOf)))
		b.WriteString(footer)
		return strings.TrimRight(b.String(), "\n"), 0
	}

	b.WriteString(fmt.Sprintf("**%s members eligible for %s as of %s:**\n", upperFirst(scope), filter.phrase(), promoDate(asOf)))

	// Reserve room for the footer and a worst-case overflow notice so the
	// message cannot be pushed over the limit by what gets appended after
	// the loop.
	budget := promoMessageLimit - len(footer) - promoOverflowReserve
	listed := 0
	for _, c := range candidates {
		line := formatPromoLine(c)
		if b.Len()+len(line) > budget {
			break
		}
		b.WriteString(line)
		listed++
	}
	omitted := len(candidates) - listed
	if omitted > 0 {
		b.WriteString(fmt.Sprintf("…and %d more. Full list in the attached report.", omitted))
	}
	b.WriteString(footer)
	return strings.TrimRight(b.String(), "\n"), omitted
}

// promoFooter renders the shared trailing notices (§VII degradation, skipped
// members) appended to the message.
func promoFooter(skippedCount int, viiActive bool) string {
	var footer strings.Builder
	if !viiActive {
		footer.WriteString("\nℹ️ Veteran Rank Retention (§VII) check unavailable this run (rank data fetch failed); standard ladder only.")
	}
	if skippedCount > 0 {
		noun := "members"
		if skippedCount == 1 {
			noun = "member"
		}
		footer.WriteString(fmt.Sprintf("\n⚠️ %d %s skipped due to errors (reported)", skippedCount, noun))
	}
	return footer.String()
}

// formatPromoLine renders one candidate. Standard candidates show the ladder
// step; §VII-only candidates the restoration target and previously-held
// evidence; lateral-only candidates the warrant move. A candidate eligible
// via several paths reads as the most conventional one (standard, then
// §VII), with the other paths appended as flags.
func formatPromoLine(c promoCandidate) string {
	lateralNote := func() string {
		if c.LateralTarget == "" {
			return ""
		}
		return ", also lateral → " + c.LateralTarget
	}
	if !c.Verdict.Eligible && !c.ViaVII {
		return fmt.Sprintf("[%s](<%s>) %s → %s (lateral, flight wings)\n", c.Username, c.MilpacURL, c.RankShort, c.LateralTarget)
	}
	if c.ViaVII && !c.Verdict.Eligible {
		line := fmt.Sprintf("[%s](<%s>) %s → %s (§VII veteran retention", c.Username, c.MilpacURL, c.RankShort, c.Vii.Target.Short)
		if c.Vii.TargetHeldDate != "" {
			line += ", held " + c.Vii.TargetHeldDate
		}
		return line + lateralNote() + ")\n"
	}
	line := fmt.Sprintf("[%s](<%s>) %s → %s (%s", c.Username, c.MilpacURL, c.RankShort, c.Verdict.NextRank, c.Verdict.Type)
	if len(c.Verdict.PendingDisplayCourses) > 0 {
		line += ", pending: " + strings.Join(c.Verdict.PendingDisplayCourses, ", ")
	}
	if c.ViaVII {
		line += ", also §VII → " + c.Vii.Target.Short
	}
	return line + lateralNote() + ")\n"
}

// promoScan is the result of one position-scope eligibility pass.
type promoScan struct {
	Candidates   []promoCandidate
	SkippedCount int
	// ViiActive is false when the ranks fetch failed and the pass degraded
	// to standard-ladder-only.
	ViiActive   bool
	EmptyRoster bool
}

// collectPromoCandidates runs the full eligibility pass for a position scope:
// roster fetch, then the shared evaluatePromoRoster tail. Errors are returned
// only for the roster fetch; per-member failures are counted in SkippedCount
// and reported to Sentry.
// promoActiveDutyScope is the special position value that sweeps the entire
// Active Duty roster (ROSTER_TYPE_COMBAT) instead of a fuzzy position search.
// It is also the default scope when /promo is called with no mode option.
// The legacy "active-duty" spelling is still accepted (isActiveDutyScope).
const promoActiveDutyScope = "activeduty"

func isActiveDutyScope(position string) bool {
	return strings.EqualFold(strings.ReplaceAll(position, "-", ""), promoActiveDutyScope)
}

// isDevcomScope matches the DEVCOM department, which spans the DEVCOM HQ and
// its D/DEVCOM sub-unit; a fuzzy search on one term misses the other, so
// resolvePositionRoster merges both.
func isDevcomScope(position string) bool {
	return strings.EqualFold(strings.TrimSpace(position), "DEVCOM")
}

// resolvePositionRoster fetches the lite roster for a position scope,
// special-casing activeduty (the whole combat roster) and DEVCOM (HQ +
// D/DEVCOM sub-unit, merged and deduplicated by username). Shared by /promo
// and /billetaudit.
func resolvePositionRoster(ctx context.Context, position string) (*utils.LiteRosterResponse, error) {
	if isActiveDutyScope(position) {
		return utils.GetLiteRoster(ctx, "ROSTER_TYPE_COMBAT")
	}
	if isDevcomScope(position) {
		merged := map[string]utils.LiteProfileResponse{}
		for _, term := range []string{"DEVCOM", "D/DEVCOM"} {
			r, err := utils.GetRosterByFuzzyPositionSearch(ctx, term)
			if err != nil {
				return nil, err
			}
			for _, p := range r.LiteProfiles {
				merged[p.User.Username] = p // dedup: same member from both searches
			}
		}
		return &utils.LiteRosterResponse{LiteProfiles: merged}, nil
	}
	return utils.GetRosterByFuzzyPositionSearch(ctx, position)
}

// collectPromoCandidates runs the eligibility pass for a position scope,
// optionally narrowed to a single rank (case-insensitive milpac short form).
// position + rank combine: position scopes the roster, rank filters within it.
func collectPromoCandidates(ctx context.Context, position, rank string, asOf time.Time, filter promoFilter) (promoScan, error) {
	roster, err := resolvePositionRoster(ctx, position)
	if err != nil {
		return promoScan{}, err
	}

	members := make([]utils.LiteProfileResponse, 0, len(roster.LiteProfiles))
	for _, member := range roster.LiteProfiles {
		if rank == "" || strings.EqualFold(member.Rank.RankShort, rank) {
			members = append(members, member)
		}
	}
	utils.Info("📋 Retrieved roster", "member_count", len(members), "position", position, "rank", rank)

	if len(members) == 0 {
		return promoScan{EmptyRoster: true}, nil
	}
	return evaluatePromoRoster(ctx, members, position, asOf, filter), nil
}

// evaluatePromoRoster is the scope-independent tail of an eligibility pass:
// rank-model fetch (degradable), concurrent per-member evaluation, and the
// seniority sort. scopeLabel is for error reporting only.
func evaluatePromoRoster(ctx context.Context, members []utils.LiteProfileResponse, scopeLabel string, asOf time.Time, filter promoFilter) promoScan {
	// Rank model for the §VII path, fetched once per pass (after the callers'
	// empty-roster early returns, so an empty scope costs no extra call). A
	// failure degrades to standard-ladder-only rather than failing the pass.
	var rankModel *viiRankModel
	if ranksResp, ranksErr := utils.GetRanks(ctx); ranksErr != nil {
		utils.CaptureError("Ranks fetch failed; §VII path disabled for this pass", ranksErr)
	} else {
		rankModel = buildRankModel(ranksResp)
	}

	// Drop the members the lite roster already rules out, so the fan-out only
	// spends milpac fetches on records that could change the answer.
	fetchable := make([]utils.LiteProfileResponse, 0, len(members))
	for _, member := range members {
		if filter.needsProfile(member, asOf, rankModel) {
			fetchable = append(fetchable, member)
		}
	}
	utils.Info("🔎 Pre-filtered roster on lite data",
		"scope", scopeLabel, "filter", filter.tag(), "roster", len(members),
		"fetching", len(fetchable), "ruled_out", len(members)-len(fetchable))
	members = fetchable

	results := make([]*promoCandidate, len(members))
	errs := make([]error, len(members))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(10)
	for idx, member := range members {
		idx, member := idx, member
		g.Go(func() error {
			results[idx], errs[idx] = evaluatePromoMember(gctx, member, asOf, rankModel)
			return nil
		})
	}
	_ = g.Wait()

	scan := promoScan{ViiActive: rankModel != nil}
	for idx, member := range members {
		if err := errs[idx]; err != nil {
			utils.CaptureError("Promo member evaluation failed", err, "username", member.User.Username, "position", scopeLabel)
			scan.SkippedCount++
			continue
		}
		if results[idx] != nil {
			scan.Candidates = append(scan.Candidates, *results[idx])
		}
	}

	// Most-senior current rank first, then alphabetical for stable output.
	sort.Slice(scan.Candidates, func(a, b int) bool {
		ra, rb := promoRankIndex(scan.Candidates[a].RankShort), promoRankIndex(scan.Candidates[b].RankShort)
		if ra != rb {
			return ra < rb
		}
		return scan.Candidates[a].Username < scan.Candidates[b].Username
	})
	return scan
}

// ---------------------------------------------------------------------------
// Single-trooper mode
// ---------------------------------------------------------------------------

// runPromoUser handles /promo user:<name>, the full verdict breakdown for
// one trooper (the issue #4 "auto-validate a promotion" optional), mirroring
// the source tool's single-member view.
func runPromoUser(r utils.InteractionResponder, i *discordgo.InteractionCreate, username string, asOf time.Time) {
	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Checking promotion eligibility for %s as of %s...", username, promoDate(asOf)),
		},
	})
	if err != nil {
		utils.CaptureError("❌ Interaction response failed", err)
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	profile, err := utils.GetMilpacByUsername(ctx, username)
	if err != nil {
		// Username is user-supplied: not-found is a plausible outcome.
		utils.HandleError(r, i, fmt.Sprintf("❌ No milpac found for %q. Use the forum username exactly as it appears on the roster.", username))
		return
	}

	var vii *viiResult
	viiActive := false
	if ranksResp, ranksErr := utils.GetRanks(ctx); ranksErr != nil {
		utils.CaptureError("Ranks fetch failed; §VII path disabled for this pass", ranksErr)
	} else {
		v := viiAnalyze(profile, buildRankModel(ranksResp), asOf)
		vii = &v
		viiActive = true
	}

	verdict := calculatePromotionEligibility(
		profile.Rank.RankShort,
		profile.PromotionDate,
		profile.JoinDate,
		parseCourseCompletions(profile.Records),
		profile.Primary.PositionTitle,
		profile.Mos,
		asOf,
	)

	response := formatPromoUserVerdict(profile, verdict, vii, viiActive, asOf)
	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &response}); err != nil {
		captureDeferredEditFailure(i, "Promo", err)
		return
	}
	utils.Info("✨ Done!", "command", "Promo", "user", username, "eligible", verdict.Eligible || (vii != nil && vii.EligibleNow))
}

func checkmark(ok bool) string {
	if ok {
		return "✅"
	}
	return "❌"
}

// formatPromoUserVerdict renders the single-trooper breakdown: every standard
// gate with its status, then the §VII verdict with evidence or reasons.
func formatPromoUserVerdict(profile *utils.ProfileResponse, v promoEligibility, vii *viiResult, viiActive bool, asOf time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Promotion verdict for %s (%s) as of %s**\n",
		profile.User.Username, profile.Rank.RankShort, promoDate(asOf))

	if v.NoRequirements {
		if _, known := promoRankSeniority[strings.ToUpper(profile.Rank.RankShort)]; known {
			// Recognized ranks with no TIG/TIS ladder entry (1SG, CSM, SGM,
			// general officers). Several still advance, but by taking a
			// different billet, not by a time-based ladder (e.g. 1SG→SGM/CSM
			// into a Bn/Regt HQ senior-enlisted seat). COL is NOT here: it has
			// a ladder entry gated on the regiment-HQ MOS. State it neutrally
			// rather than claiming "topped out".
			fmt.Fprintf(&b, "Standard ladder: no standard TIG/TIS ladder for %s. Any advancement is billet-based and handled by S1 (see 7CAV-R-023).\n", profile.Rank.RankShort)
		} else {
			b.WriteString("Standard ladder: no standard promotion ladder for this rank.\n")
		}
	} else {
		status := "not yet eligible"
		if v.Eligible {
			status = "eligible"
		}
		fmt.Fprintf(&b, "Standard ladder: **%s for %s** (%s)\n", status, v.NextRank, v.Type)
		if v.TigRequired == 0 {
			fmt.Fprintf(&b, "%s TIG %s (eligibility is not determined by TIG)\n", checkmark(v.TigMet), formatDays(v.TigDays))
		} else {
			fmt.Fprintf(&b, "%s TIG %s (need %s)\n", checkmark(v.TigMet), formatDays(v.TigDays), formatDays(v.TigRequired))
		}
		if v.TisRequired > 0 {
			fmt.Fprintf(&b, "%s TIS %s (need %s)\n", checkmark(v.TisMet), formatDays(v.TisDays), formatDays(v.TisRequired))
		}
		if len(v.MissingCourses) > 0 {
			labels := make([]string, 0, len(v.MissingCourses))
			for _, c := range v.MissingCourses {
				labels = append(labels, courseLabels[c])
			}
			fmt.Fprintf(&b, "❌ Courses missing: %s\n", strings.Join(labels, ", "))
		} else if len(v.PendingDisplayCourses) > 0 {
			fmt.Fprintf(&b, "ℹ️ Pending (non-blocking): %s\n", strings.Join(v.PendingDisplayCourses, ", "))
		}
		if len(v.RequiredBillets) > 0 {
			detected := v.DetectedBillet
			if detected == "" {
				detected = "none detected"
			}
			fmt.Fprintf(&b, "%s Billet: %s (needs %s)\n", checkmark(v.BilletMet), detected, strings.Join(v.RequiredBillets, "/"))
		}
		if len(v.RequiredMos) > 0 {
			detected := v.DetectedMos
			if detected == "" {
				detected = "none listed"
			}
			fmt.Fprintf(&b, "%s MOS: %s (needs %s, regiment HQ)\n", checkmark(v.MosMet), detected, strings.Join(v.RequiredMos, "/"))
		}
		if !v.Eligible && v.DaysUntilEligible > 0 {
			fmt.Fprintf(&b, "⏳ ~%d day(s) until time requirements met.\n", v.DaysUntilEligible)
		}
	}

	if target, ok := viiE2W[strings.ToUpper(profile.Rank.RankShort)]; ok &&
		viiHasWings(profile) && viiIsAviation(profile) {
		fmt.Fprintf(&b, "\nLateral: **eligible for %s** (flight wings + aviation MOS).", target)
	}

	switch {
	case !viiActive:
		b.WriteString("\nℹ️ Veteran Rank Retention (§VII) check unavailable this run (rank data fetch failed).")
	case vii.EligibleNow:
		fmt.Fprintf(&b, "\nVeteran Rank Retention (§VII): **eligible for %s**", vii.Target.Short)
		if vii.TargetHeldDate != "" {
			fmt.Fprintf(&b, ", held %s", vii.TargetHeldDate)
			if vii.TargetHeldRole != "" {
				fmt.Fprintf(&b, " as %s", vii.TargetHeldRole)
			}
		}
		b.WriteString(".")
	case vii.PotentiallyEligible:
		target := ""
		if vii.PotentialRank != nil {
			target = " for " + vii.PotentialRank.Short
		}
		fmt.Fprintf(&b, "\nVeteran Rank Retention (§VII): potentially eligible%s.\n%s", target, vii.Reason())
	default:
		b.WriteString("\nVeteran Rank Retention (§VII): not applicable.")
	}

	b.WriteString("\n\n")
	b.WriteString(promoDisclaimer)
	return b.String()
}

// formatDays renders a day count like the source tool ("3m 15d", "1y 2m").
func formatDays(days int) string {
	if days < 0 {
		return "unknown"
	}
	if days < 30 {
		return fmt.Sprintf("%dd", days)
	}
	months := days / 30
	if months < 12 {
		return fmt.Sprintf("%dm %dd", months, days%30)
	}
	return fmt.Sprintf("%dy %dm", months/12, months%12)
}

// ---------------------------------------------------------------------------
// Report export (HTML + CSV)
// ---------------------------------------------------------------------------

// promoCandidatePaths lists a candidate's eligibility paths, most conventional
// first: standard, then §VII, then lateral.
func promoCandidatePaths(c promoCandidate) []string {
	var paths []string
	if c.Verdict.Eligible {
		paths = append(paths, "standard")
	}
	if c.ViaVII {
		paths = append(paths, "§VII")
	}
	if c.LateralTarget != "" {
		paths = append(paths, "lateral")
	}
	return paths
}

// promoCandidateTypes lists the promotion type(s) a candidate qualifies under,
// automatic first; automatic is always prioritised over the discretionary
// paths in display. §VII and lateral are ALWAYS discretionary, never
// automatic, so a standard-automatic candidate who also has a §VII path reads
// "automatic & discretionary".
func promoCandidateTypes(c promoCandidate) []string {
	set := map[string]bool{}
	if c.Verdict.Eligible {
		set[c.Verdict.Type] = true // "automatic" or "discretionary"
	}
	if c.ViaVII || c.LateralTarget != "" {
		set["discretionary"] = true
	}
	var out []string
	for _, t := range []string{"automatic", "discretionary"} {
		if set[t] {
			out = append(out, t)
		}
	}
	return out
}

// promoReportFile renders the full candidate list as a standalone HTML
// report, attached whenever the list outruns one message or the caller asks
// for it. HTML rather than CSV because this is read rather than pivoted: at
// a few hundred rows the table stays legible in a browser, milpac links stay
// clickable, and there is no spreadsheet import step. Everything is inlined
// so the file works straight from a Discord download.
func promoReportFile(scope string, filter promoFilter, asOf time.Time, scan promoScan) *discordgo.File {
	title := fmt.Sprintf("%s eligibility: %s, %s",
		upperFirst(filter.phrase()), scope, promoDate(asOf))

	var b strings.Builder
	writeReportHead(&b, title, promoReportCSS)
	fmt.Fprintf(&b, "<p class=\"meta\">%d candidate(s)", len(scan.Candidates))
	if scan.SkippedCount > 0 {
		fmt.Fprintf(&b, " · %d skipped due to errors (reported)", scan.SkippedCount)
	}
	if !scan.ViiActive {
		b.WriteString(" · §VII check unavailable this run, standard ladder only")
	}
	b.WriteString("</p>\n")
	b.WriteString("<div class=\"controls\"><input id=\"q\" type=\"search\" placeholder=\"Filter (e.g. discretionary, SGT, §VII)…\" autofocus>" +
		"<span class=\"hint\">click a column to sort · <span id=\"shown\"></span></span></div>\n")

	b.WriteString("<table id=\"t\"><thead><tr>" +
		"<th>Trooper</th><th>Rank</th><th>Billet</th><th>Next</th><th>Path</th><th>Type</th>" +
		"<th>TIG</th><th>TIS</th><th>Pending</th><th>§VII held</th><th>Lateral</th>" +
		"</tr></thead><tbody>\n")
	for _, c := range scan.Candidates {
		viiHeld := ""
		if c.ViaVII {
			viiHeld = c.Vii.Target.Short
			if c.Vii.TargetHeldDate != "" {
				viiHeld += " (held " + c.Vii.TargetHeldDate
				if c.Vii.TargetHeldRole != "" {
					viiHeld += " as " + c.Vii.TargetHeldRole
				}
				viiHeld += ")"
			}
		}
		// data-sort carries the raw sort key so a column sorts by meaning, not
		// display text: Rank and Next by seniority (lower index = more senior),
		// TIG/TIS by day count rather than the "3m 15d" string.
		fmt.Fprintf(&b, "<tr><td><a href=\"%s\">%s</a></td><td data-sort=\"%d\">%s</td><td>%s</td><td data-sort=\"%d\">%s</td><td>%s</td><td>%s</td>"+
			"<td data-sort=\"%d\">%s</td><td data-sort=\"%d\">%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			html.EscapeString(c.MilpacURL), html.EscapeString(c.Username),
			promoRankIndex(c.RankShort), html.EscapeString(c.RankShort),
			html.EscapeString(c.Primary),
			promoNextRankIndex(c.Verdict.NextRank), html.EscapeString(c.Verdict.NextRank),
			html.EscapeString(strings.Join(promoCandidatePaths(c), ", ")),
			html.EscapeString(strings.Join(promoCandidateTypes(c), " & ")),
			c.Verdict.TigDays, formatDays(c.Verdict.TigDays),
			c.Verdict.TisDays, formatDays(c.Verdict.TisDays),
			html.EscapeString(strings.Join(c.Verdict.PendingDisplayCourses, ", ")),
			html.EscapeString(viiHeld), html.EscapeString(c.LateralTarget))
	}
	b.WriteString("</tbody></table>\n")
	writeReportFoot(&b, promoDisclaimer, promoReportScript)

	return reportFile(
		fmt.Sprintf("promo_report_%s_%s.html", promoFileScope(scope, filter), asOf.Format("2006-01-02")),
		"text/html", b.String())
}

// promoReportScript powers the HTML report's client-side filter and
// column sort. Self-contained (no external libs) so it runs straight from a
// Discord download; no template data is interpolated, so it is a static
// constant. A cell may carry a data-sort attribute (raw numeric value) which
// the sort uses in place of the display text.
// promoReportCSS adds the interactive affordances on top of reportBaseCSS:
// the filter box and the sortable, clickable column headers.
const promoReportCSS = `.controls{margin:0 0 1rem;font-size:.9rem}
.controls input{padding:.35rem .5rem;font-size:.9rem;width:16rem;max-width:100%}
.controls .hint{color:#777;margin-left:.5rem}
th{cursor:pointer;user-select:none;white-space:nowrap}
th:hover{background:#e9e9e9}
th.sorted-asc::after{content:" \25B2";color:#888}
th.sorted-desc::after{content:" \25BC";color:#888}`

const promoReportScript = `<script>
(function(){
  var table=document.getElementById('t'), tbody=table.tBodies[0];
  var q=document.getElementById('q'), shown=document.getElementById('shown');
  var total=tbody.rows.length;
  function rows(){return Array.prototype.slice.call(tbody.rows);}
  function updateCount(){
    var n=rows().filter(function(r){return r.style.display!=='none';}).length;
    shown.textContent=n+' of '+total+' shown';
  }
  function applyFilter(){
    var s=q.value.toLowerCase();
    rows().forEach(function(r){
      r.style.display=r.textContent.toLowerCase().indexOf(s)>-1?'':'none';
    });
    updateCount();
  }
  q.addEventListener('input',applyFilter);
  function val(td){
    var d=td.getAttribute('data-sort');
    if(d!==null){var f=parseFloat(d);return isNaN(f)?d:f;}
    return td.textContent.toLowerCase();
  }
  var headers=table.tHead.rows[0].cells, dir={};
  Array.prototype.forEach.call(headers,function(th,idx){
    th.addEventListener('click',function(){
      var asc=dir[idx]=!dir[idx];
      Array.prototype.forEach.call(headers,function(h){h.className='';});
      th.className=asc?'sorted-asc':'sorted-desc';
      rows().sort(function(a,b){
        var av=val(a.cells[idx]),bv=val(b.cells[idx]);
        if(av<bv)return asc?-1:1;
        if(av>bv)return asc?1:-1;
        return 0;
      }).forEach(function(r){tbody.appendChild(r);});
    });
  });
  updateCount();
})();
</script>
`

// promoFileScope makes a scope+type label safe for a filename.
func promoFileScope(scope string, filter promoFilter) string {
	repl := strings.NewReplacer(" ", "-", "/", "-")
	fileScope := strings.ToLower(repl.Replace(strings.TrimSpace(scope)))
	if tag := filter.tag(); tag != "" {
		fileScope += "_" + tag
	}
	return fileScope
}

// promoCSVFile is the machine-readable companion to the HTML report, for
// pivoting in a spreadsheet. Same rows as the HTML table; encoding/csv handles
// escaping, and the strings.Builder target means no I/O error path in practice.
func promoCSVFile(scope string, filter promoFilter, asOf time.Time, scan promoScan) *discordgo.File {
	rows := make([][]string, 0, len(scan.Candidates))
	for _, c := range scan.Candidates {
		viiHeld := ""
		if c.ViaVII {
			viiHeld = c.Vii.Target.Short
			if c.Vii.TargetHeldDate != "" {
				viiHeld += " (held " + c.Vii.TargetHeldDate
				if c.Vii.TargetHeldRole != "" {
					viiHeld += " as " + c.Vii.TargetHeldRole
				}
				viiHeld += ")"
			}
		}
		rows = append(rows, []string{
			c.Username, c.RankShort, c.Primary, c.Verdict.NextRank,
			strings.Join(promoCandidatePaths(c), ", "), strings.Join(promoCandidateTypes(c), " & "),
			fmt.Sprintf("%d", c.Verdict.TigDays), fmt.Sprintf("%d", c.Verdict.TisDays),
			strings.Join(c.Verdict.PendingDisplayCourses, ";"), viiHeld, c.LateralTarget, c.MilpacURL,
		})
	}
	header := []string{
		"username", "current_rank", "primary_billet", "next_rank", "path", "type",
		"tig_days", "tis_days", "pending_courses", "vii_held", "lateral_target", "milpac_url",
	}
	return reportFile(
		fmt.Sprintf("promo_report_%s_%s.csv", promoFileScope(scope, filter), asOf.Format("2006-01-02")),
		"text/csv", csvReport(header, rows))
}
