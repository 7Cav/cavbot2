package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

func stringPtr(s string) *string {
	return &s
}

const maxEmbedsPerMsg = 10

// discordEmbedDescriptionLimit is Discord's hard cap on an embed description.
// The rendered description is summaryLine + separator + chunk, so the per-chunk
// budget is this minus the prefix length (see runAwol) to keep the total ≤ limit.
const discordEmbedDescriptionLimit = 4096

// awolReportFooter explains the displayed figure: days AWOL already has valid
// LOA-covered days subtracted (ADR 0008). Rendered on the healthy path.
const awolReportFooter = "AWOL days subtracts valid LOA days."

// awolDegradedFooter is the loud degraded-mode warning (#96 machinery, reworded
// for ADR 0008): staff must know the accountable-day adjustment was SKIPPED and
// the figures are raw whole-date inactivity, not merely that a column is stale.
// Never silently treats everyone as non-LOA.
const awolDegradedFooter = "⚠️ LOA cache unavailable — accountable-day adjustment SKIPPED; figures are raw inactivity (LOA NOT subtracted)."

// loaCacheReader is the LOA-cache surface /awol consumes. Production wires
// *utils.LOACache (GlobalLOACache); tests substitute a fake. GetEntries returns
// the full retained window history for a username (clock-free); /awol applies its
// own injected `now` to that history for the accountable-day calc AND for active-
// window selection, so selection and verdict can never disagree at a boundary
// (PR #161 clock-skew item). IsHealthy gates degraded mode (#96): an unhealthy
// cache is stale, so /awol falls back to raw inactivity with no LOA subtraction.
//
// This is intentionally a DIFFERENT surface from /loa's loaCacheView
// ({GetEntry,IsHealthy}): both share IsHealthy, but /awol reads the full window
// history via GetEntries (to subtract every LOA-covered date), whereas /loa reads
// a single most-relevant window via GetEntry. It is not a superset of /loa's set —
// the read methods differ (GetEntries vs GetEntry). Keeping the two interfaces
// distinct scopes each command's dependency to exactly what it reads.
type loaCacheReader interface {
	GetEntries(username string) []utils.LOAEntry
	IsHealthy(maxAge time.Duration) (bool, time.Time)
}

type AwolUser struct {
	Username     string
	MilpacUrl    string
	LastPostDate time.Time
	// DaysAWOL is the displayed figure: accountable dates past the 7-day
	// requirement (LOA-subtracted on the healthy path, raw inactivity when
	// degraded). Always > 0 for a listed user.
	DaysAWOL int
	// RawDaysSincePost is the raw whole-date inactivity span, shown as the
	// "last post Nd" secondary context regardless of LOA subtraction.
	RawDaysSincePost int
	// loaWindow is the active LOA window backing this row's ⚪ glyph and thread
	// link, or nil when the trooper is not currently on LOA. Modeled as a pointer
	// (not a value + bool pair) so a caller can't read a meaningless zero window
	// without the nil discriminator (PR #161 optional-field item).
	loaWindow *utils.LOAEntry
}

// OnLOA reports whether the trooper is currently on an active LOA (which forces
// the ⚪ glyph and a thread link regardless of days AWOL).
func (u AwolUser) OnLOA() bool { return u.loaWindow != nil }

// severityGlyph picks the row glyph: an active LOA always overrides to ⚪
// ("on LOA, still AWOL"); otherwise by days-AWOL tier (ADR 0008).
func (u AwolUser) severityGlyph() string {
	if u.OnLOA() {
		return "⚪"
	}
	switch {
	case u.DaysAWOL > 14:
		return "🔴"
	case u.DaysAWOL > 7:
		return "🟠"
	default: // > 0 by construction (listed users only)
		return "🟡"
	}
}

func Awol() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "awol",
			Description: "Return the AWOL users for a position.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "position",
					Description: "Position to check for awols. (EX: 2/B/1-7 | Reservist | S1)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "force_file_output",
					Description: "Forces the output to be a file instead of an embed. This is useful for large AWOL lists.",
					Required:    false,
				},
			},
		},
		Handler: handleAwolCommand,
	}
}

func handleAwolCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runAwol(utils.NewSessionResponder(s), utils.GlobalLOACache, time.Now(), i)
}

func runAwol(r utils.InteractionResponder, cache loaCacheReader, now time.Time, i *discordgo.InteractionCreate) {
	utils.Info("🚀 Starting AWOL check", "command", "Awol", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	position := i.ApplicationCommandData().Options[0].StringValue()
	forceFile := false
	if len(i.ApplicationCommandData().Options) > 1 {
		forceFile = i.ApplicationCommandData().Options[1].BoolValue()
	}

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AWOL data for %s...", position),
		},
	})
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	// #96 / ADR 0008: probe cache health once per invocation. When unhealthy the
	// LOA history can't be trusted, so we degrade LOUDLY — compute raw whole-date
	// inactivity with NO LOA subtraction, still flag > 7, and warn that the
	// accountable-day adjustment was skipped. We never abort (the last-forum-post
	// signal is independent) and never silently treat everyone as non-LOA. No
	// Sentry capture: operational degradation, not an internal error (ADR 0001).
	cacheHealthy, lastRefresh := cache.IsHealthy(loaCacheMaxAge)
	if !cacheHealthy {
		utils.Debug("AWOL served with unhealthy LOA cache (accountable-day adjustment skipped)",
			"command", "Awol",
			"username", i.Member.User.Username,
			"discord_id", i.Member.User.ID,
			"last_success", lastRefresh,
			"served_at", now,
			"staleness", now.Sub(lastRefresh),
		)
	}

	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, position)
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to fetch roster: %v", err))
		return
	}
	if len(roster.LiteProfiles) == 0 {
		utils.HandleError(r, i, emptyRosterSearchMessage(position))
		return
	}

	awolUsers := []AwolUser{}
	for _, member := range roster.LiteProfiles {
		if member.User.Username == "Tester.B" || strings.Contains(member.Rank.RankFull, "General") {
			continue
		}
		lastPostDate, err := time.Parse("2006-01-02 15:04:05", member.LastForumPostDate)
		if err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Failed to parse last forum post date: %v", err))
			return
		}

		rawDays := utils.DaysSinceLastPost(lastPostDate, now)

		var daysAWOL int
		var loaWindow *utils.LOAEntry
		if cacheHealthy {
			// Read the full retained history ONCE and apply our injected `now` to
			// both the accountable-day calc and active-window selection, so the
			// flag, the figure, and the [[LOA]] link can't disagree (PR #161).
			windows := cache.GetEntries(member.User.Username)
			daysAWOL = utils.AccountableDaysAWOL(lastPostDate, now, windows)
			if active, ok := utils.ActiveWindow(windows, now); ok {
				w := active
				loaWindow = &w
			}
		} else {
			// Degraded: raw inactivity overage, no LOA subtraction, no LOA decoration.
			daysAWOL = utils.RawDaysAWOL(lastPostDate, now)
		}

		if daysAWOL > 0 {
			matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
			if len(matches) < 2 {
				utils.HandleError(r, i, "❌ Failed to parse uniform URL")
				return
			}
			awolUsers = append(awolUsers, AwolUser{
				Username:         member.User.Username,
				MilpacUrl:        fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
				LastPostDate:     lastPostDate,
				DaysAWOL:         daysAWOL,
				RawDaysSincePost: rawDays,
				loaWindow:        loaWindow,
			})
		}
	}

	// Sort worst-first by days AWOL; tie-break username ascending so the order is
	// stable and deterministic for tests and staff.
	sort.Slice(awolUsers, func(a, b int) bool {
		if awolUsers[a].DaysAWOL != awolUsers[b].DaysAWOL {
			return awolUsers[a].DaysAWOL > awolUsers[b].DaysAWOL
		}
		return awolUsers[a].Username < awolUsers[b].Username
	})

	if len(awolUsers) == 0 {
		response := fmt.Sprintf("Search completed successfully: no users matching \"%s\" are AWOL.", position)
		if !cacheHealthy {
			// Never a silent all-clear while degraded: raw figures are >= the
			// LOA-adjusted figure so no AWOL member is hidden, but staff must still
			// know the cache was down and the accountable-day adjustment was skipped
			// (ADR 0008) — same wording as every other terminal degraded path.
			response += "\n" + awolDegradedFooter
		}
		if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &response,
		}); err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		}
		return
	}

	footerText := awolReportFooter
	if !cacheHealthy {
		footerText = awolDegradedFooter
	}

	// Discord caps an embed description at 4096 chars. The final description is
	// summaryLine + "\n\n" + chunk, so the chunk budget must reserve room for that
	// rendered prefix — otherwise a near-4096 chunk overflows once the summary is
	// prepended (Discord 400). Reserve the longest prefix any embed could carry.
	descPrefix := awolSummaryLine(len(awolUsers)) + "\n\n"
	chunkBudget := discordEmbedDescriptionLimit - len(descPrefix)

	var chunks []string
	currentChunk := ""
	for _, user := range awolUsers {
		userLine := awolUserLine(user)
		if currentChunk != "" && len(currentChunk)+len(userLine) > chunkBudget {
			chunks = append(chunks, currentChunk)
			currentChunk = userLine
		} else {
			currentChunk += userLine
		}
	}
	if currentChunk != "" {
		chunks = append(chunks, currentChunk)
	}
	utils.Info("Debug chunks info", "chunks_length", len(chunks), "max_embeds", maxEmbedsPerMsg)
	if len(chunks) > maxEmbedsPerMsg {
		utils.Info("⚠️ Too many AWOL users for embeds, falling back to file upload", "count", len(awolUsers))
		sendAwolFile(r, i, awolUsers, position, forceFile, cacheHealthy, now)
		utils.Info("✨ Done!", "command", "Awol")
		return
	} else if forceFile {
		utils.Info("⚠️ Force file output enabled, falling back to embeds", "count", len(awolUsers))
		sendAwolFile(r, i, awolUsers, position, forceFile, cacheHealthy, now)
		utils.Info("✨ Done!", "command", "Awol")
		return
	}

	var embeds []*discordgo.MessageEmbed
	for idx, chunk := range chunks {
		title := awolEmbedTitle(position, idx, len(chunks))
		embed := &discordgo.MessageEmbed{
			Title:       title,
			Description: awolSummaryLine(len(awolUsers)) + "\n\n" + chunk,
			Color:       0xfbcc29,
			Footer: &discordgo.MessageEmbedFooter{
				Text: footerText,
			},
			Timestamp: now.Format(time.RFC3339),
		}
		embeds = append(embeds, embed)
	}

	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: nil,
		Embeds:  &embeds,
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}

	utils.Info("✨ Done!", "command", "Awol")
}

// awolEmbedTitle renders the "AWOL — <position>" heading, paginated only when the
// list spans multiple embeds.
func awolEmbedTitle(position string, idx, total int) string {
	if total <= 1 {
		return fmt.Sprintf("AWOL — %s", position)
	}
	return fmt.Sprintf("AWOL — %s (Page %d/%d)", position, idx+1, total)
}

// awolSummaryLine renders "N flagged" — the count of AWOL-flagged members. The
// LOA dimension is conveyed by the ⚪ rows and the footer, not a separate count:
// a bare "M LOA" misread as "M LOAs subtracted" (it was really the number of
// flagged troopers currently on an active LOA), so it is dropped on both paths.
func awolSummaryLine(flagged int) string {
	return fmt.Sprintf("%d flagged", flagged)
}

// awolUserLine renders one report row:
//
//	🔴 Snuffy.B — 16d AWOL · last post 23d
//	⚪ [LOA] Reyes.J — 1d AWOL · last post 8d
//
// The name links to the milpac; an active-LOA row prefixes a [LOA] thread link.
func awolUserLine(u AwolUser) string {
	loaTag := ""
	if u.OnLOA() && u.loaWindow.ThreadID != 0 {
		loaTag = fmt.Sprintf("[[LOA]](https://7cav.us/threads/%d/) ", u.loaWindow.ThreadID)
	} else if u.OnLOA() {
		loaTag = "[LOA] "
	}
	return fmt.Sprintf("%s %s[%s](%s) — %dd AWOL · last post %dd\n",
		u.severityGlyph(),
		loaTag,
		u.Username,
		u.MilpacUrl,
		u.DaysAWOL,
		u.RawDaysSincePost,
	)
}

func sendAwolFile(r utils.InteractionResponder, i *discordgo.InteractionCreate, awolUsers []AwolUser, position string, forceFile, cacheHealthy bool, now time.Time) {
	var content strings.Builder
	_, _ = fmt.Fprintf(&content, "AWOL Report for %s\nGenerated: %s\n%s\n\n",
		position,
		now.Format("2006-01-02 15:04:05"),
		awolSummaryLine(len(awolUsers)))
	if !cacheHealthy {
		// ADR 0008: surface the degraded warning in the file too — same figures
		// (raw) and the same "adjustment skipped" wording as the embed.
		_, _ = fmt.Fprintf(&content, "%s\n\n", awolDegradedFooter)
	}

	for _, user := range awolUsers {
		loaTag := ""
		if user.OnLOA() {
			loaTag = " [LOA]"
		}
		_, _ = fmt.Fprintf(&content, "%s%s — %dd AWOL · last post %dd\nMilpac: %s\n\n",
			user.Username,
			loaTag,
			user.DaysAWOL,
			user.RawDaysSincePost,
			user.MilpacUrl)
	}
	if cacheHealthy {
		_, _ = fmt.Fprintf(&content, "%s\n", awolReportFooter)
	}

	file := &discordgo.File{
		Name:        fmt.Sprintf("awol_report_%s.txt", strings.ReplaceAll(position, "/", "-")),
		ContentType: "text/plain",
		Reader:      strings.NewReader(content.String()),
	}
	var prefix string
	if forceFile {
		prefix = fmt.Sprintf("Force File Set True, AWOL report for %s generated as file:", position)
	} else {
		prefix = fmt.Sprintf("Large AWOL report for %s generated as file:", position)
	}
	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: stringPtr(prefix),
		Files:   []*discordgo.File{file},
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to send file: %v", err))
	}
}
