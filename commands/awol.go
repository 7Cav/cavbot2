package commands

import (
	"context"
	"fmt"
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

// discordMessageEmbedTextLimit is Discord's cap on the text of all of a
// message's embeds together: titles, descriptions, footers, author names,
// and field names and values.
const discordMessageEmbedTextLimit = 6000

// awolReportFooter explains the displayed figure: days AWOL already has valid
// LOA-covered days subtracted (ADR 0008). Rendered on the healthy path.
const awolReportFooter = "AWOL days subtracts valid LOA days."

// awolDegradedFooter is the loud warning on a report the LOAs couldn't be
// read for (ADR 0008): staff must know no LOA days were subtracted and the
// figures are raw whole-date inactivity. /awol never silently treats
// everyone as non-LOA.
const awolDegradedFooter = "⚠️ LOAs couldn't be read, so NO LOA days were subtracted. Figures are raw inactivity."

// awolLOAHistoryDays is how far back /awol asks the 7Cav API for ended
// LOAs, in days before the bot's UTC today. The API refuses a date more
// than 365 days before its own UTC today, and the spare day absorbs a bot
// clock running behind the API's across UTC midnight.
const awolLOAHistoryDays = 364

// awolReviewLookbackDays is how many UTC dates before a trooper's last forum
// post a needs-review LOA may be posted and still tag their row. It keeps
// months-old broken threads off the report, and still catches an LOA filed
// just before a trooper's last post or by a leader while they were away.
const awolReviewLookbackDays = 14

// awolLOATag marks the row of a trooper with an LOA that includes today.
const awolLOATag = "[LOA]"

// awolNeedsReviewTag marks the row of a trooper with a needs-review LOA, so
// a leader reads its thread before acting on the row.
const awolNeedsReviewTag = "[LOA needs review]"

// awolAllClearFormat answers a position where nobody is AWOL, with the
// position in place of %s.
const awolAllClearFormat = "Search completed successfully: no users matching \"%s\" are AWOL."

// The notes a report sent as a file carries, with the position in place of
// %s: one for force_file_output, one for a report too large for embeds.
const (
	awolForcedFileFormat = "Force File Set True, AWOL report for %s generated as file:"
	awolLargeFileFormat  = "Large AWOL report for %s generated as file:"
)

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
	// reviewLOA is the needs-review LOA this row is tagged for, or nil when
	// there is none. It never changes the glyph.
	reviewLOA *awolReview
}

// awolReview is a needs-review LOA as a row's tag needs it.
type awolReview struct {
	// threadID is the LOA's thread, or 0 when its ID doesn't parse, which
	// leaves the tag unlinked.
	threadID int64
	posted   time.Time
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
	runAwol(utils.NewSessionResponder(s), time.Now(), i)
}

func runAwol(r utils.InteractionResponder, now time.Time, i *discordgo.InteractionCreate) {
	username, discordID := interactionUsernameAndID(i)
	utils.Info("🚀 Starting AWOL check", "command", "Awol", "username", username, "discord_id", discordID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	position := i.ApplicationCommandData().Options[0].StringValue()
	forceFile := false
	if len(i.ApplicationCommandData().Options) > 1 {
		forceFile = i.ApplicationCommandData().Options[1].BoolValue()
	}

	err := acknowledge(r, i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AWOL data for %s...", position),
		},
	})
	if err != nil {
		replyAckFailed(r, i, err)
		return
	}

	loas := callLOAsAlongside(ctx, func(ctx context.Context) (*utils.LOAsResponse, error) {
		return getLOAs(ctx, now.AddDate(0, 0, -awolLOAHistoryDays))
	})
	defer loas.stop()

	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, position)
	if err != nil {
		replyLookupFailed(r, i, "awol", err)
		return
	}
	if len(roster.LiteProfiles) == 0 {
		replyError(r, i, emptyRosterSearchMessage(position))
		return
	}

	// ADR 0008: when the LOAs can't be read, /awol degrades LOUDLY. It
	// computes raw whole-date inactivity with NO LOA subtraction, still
	// flags > 7, and warns that no LOA days were subtracted. It never aborts
	// (the last-forum-post signal is independent) and never silently treats
	// everyone as non-LOA.
	answer := loas.wait()
	loasRead := answer.err == nil
	var windowsByForumUserID map[string][]utils.LOAEntry
	var reviewsByForumUserID map[string][]awolReview
	if loasRead {
		windowsByForumUserID = loaWindowsByForumUserID(answer.loas.LOAs)
		reviewsByForumUserID = needsReviewByForumUserID(answer.loas.LOAs)
	} else {
		captureError("LOAs unreadable, AWOL report shows raw inactivity", answer.err,
			"command", "awol", "guild_id", i.GuildID, "position", position)
	}

	// Issue #163: a single unparseable record must not abort the whole report.
	// Both parse failures below are genuine upstream errors (the forum should
	// always return a parseable date; a roster profile should always carry a
	// valid uniform URL), so each is captured to Sentry naming the member and
	// the bad value (ADR 0001), then skipped — mirroring /afsm.
	awolUsers := []AwolUser{}
	skippedCount := 0
	for _, member := range roster.LiteProfiles {
		if member.User.Username == "Tester.B" || strings.Contains(member.Rank.RankFull, "General") {
			continue
		}
		lastPostDate, err := time.Parse("2006-01-02 15:04:05", member.LastForumPostDate)
		if err != nil {
			utils.CaptureError(
				"AWOL member skipped: unparseable last forum post date",
				err,
				"username", member.User.Username,
				"discord_id", member.DiscordID,
				"last_forum_post_date", member.LastForumPostDate,
			)
			skippedCount++
			continue
		}

		rawDays := utils.DaysSinceLastPost(lastPostDate, now)

		var daysAWOL int
		var loaWindow *utils.LOAEntry
		var reviewLOA *awolReview
		if loasRead {
			// Apply our injected `now` to both the accountable-day calc and
			// active-window selection, so the flag, the figure, and the [[LOA]]
			// link can't disagree (PR #161).
			windows := windowsByForumUserID[member.User.UserID]
			daysAWOL = utils.AccountableDaysAWOL(lastPostDate, now, windows)
			if active, ok := utils.ActiveWindow(windows, now); ok {
				w := active
				loaWindow = &w
			}
			reviewLOA = awolReviewLOA(reviewsByForumUserID[member.User.UserID], lastPostDate)
		} else {
			// Degraded: raw inactivity overage, no LOA subtraction, no LOA decoration.
			daysAWOL = utils.RawDaysAWOL(lastPostDate, now)
		}

		if daysAWOL > 0 {
			milpacID, err := utils.ExtractMilpacIDFromUniformURL(member.UniformUrl)
			if err != nil {
				utils.CaptureError(
					"AWOL member skipped: unparseable uniform URL",
					err,
					"username", member.User.Username,
					"discord_id", member.DiscordID,
					"uniform_url", member.UniformUrl,
				)
				skippedCount++
				continue
			}
			awolUsers = append(awolUsers, AwolUser{
				Username:         member.User.Username,
				MilpacUrl:        fmt.Sprintf("https://7cav.us/rosters/profile/%s", milpacID),
				LastPostDate:     lastPostDate,
				DaysAWOL:         daysAWOL,
				RawDaysSincePost: rawDays,
				loaWindow:        loaWindow,
				reviewLOA:        reviewLOA,
			})
		}
	}

	if skippedCount > 0 {
		utils.Info("⚠️ Members skipped during AWOL check", "position", position, "count", skippedCount)
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
		response := fmt.Sprintf(awolAllClearFormat, position)
		if note := awolSkippedNote(skippedCount); note != "" {
			// Issue #163: never a silent all-clear when records were skipped — a
			// skipped member could have been the one who was AWOL.
			response += "\n" + note
		}
		if !loasRead {
			// Never a silent all-clear while degraded: raw figures are >= the
			// LOA-adjusted figure so no AWOL member is hidden, but staff must still
			// know the LOAs couldn't be read and no LOA days were subtracted
			// (ADR 0008) — same wording as every other terminal degraded path.
			response += "\n" + awolDegradedFooter
		}
		if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &response,
		}); err != nil {
			captureDeferredEditFailure(i, "awol", err)
		}
		return
	}

	footerText := awolReportFooter
	if !loasRead {
		footerText = awolDegradedFooter
	}

	// Discord caps an embed description at 4096 chars. The final description is
	// the summary block + "\n\n" + chunk, so the chunk budget must reserve room
	// for that rendered prefix — otherwise a near-4096 chunk overflows once the
	// summary is prepended (Discord 400). Reserve the longest prefix any embed
	// could carry. The block includes the skipped-records note when present
	// (issue #163) so staff see the report is partial.
	summaryBlock := awolSummaryLine(len(awolUsers))
	if note := awolSkippedNote(skippedCount); note != "" {
		summaryBlock += "\n" + note
	}
	descPrefix := summaryBlock + "\n\n"
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
		sendAwolFile(r, i, awolUsers, position, forceFile, loasRead, now, skippedCount)
		utils.Info("✨ Done!", "command", "Awol")
		return
	} else if forceFile {
		utils.Info("⚠️ Force file output enabled, falling back to embeds", "count", len(awolUsers))
		sendAwolFile(r, i, awolUsers, position, forceFile, loasRead, now, skippedCount)
		utils.Info("✨ Done!", "command", "Awol")
		return
	}

	var embeds []*discordgo.MessageEmbed
	for idx, chunk := range chunks {
		title := awolEmbedTitle(position, idx, len(chunks))
		embed := &discordgo.MessageEmbed{
			Title:       title,
			Description: summaryBlock + "\n\n" + chunk,
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
		captureDeferredEditFailure(i, "awol", err)
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

// awolSkippedNote renders the visible partial-coverage annotation for the report
// (issue #163): "⚠️ N record(s) skipped due to errors (reported)" — same shape
// and "(reported)" suffix as /afsm's note, but with a "record(s)" noun since
// /awol skips raw roster records rather than evaluated members. Returns ""
// when nothing was skipped.
func awolSkippedNote(skipped int) string {
	if skipped == 0 {
		return ""
	}
	noun := "records"
	if skipped == 1 {
		noun = "record"
	}
	return fmt.Sprintf("⚠️ %d %s skipped due to errors (reported)", skipped, noun)
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
//	🔴 ⚠️ [LOA needs review] Kade.T — 23d AWOL · last post 30d
//
// The name links to the milpac; an active-LOA row prefixes a [LOA] thread link,
// and a needs-review row a ⚠️ [LOA needs review] thread link.
func awolUserLine(u AwolUser) string {
	tags := ""
	if u.OnLOA() {
		tags = awolThreadTag(awolLOATag, u.loaWindow.ThreadID)
	}
	if u.reviewLOA != nil {
		tags += "⚠️ " + awolThreadTag(awolNeedsReviewTag, u.reviewLOA.threadID)
	}
	return fmt.Sprintf("%s %s[%s](%s) — %s\n",
		u.severityGlyph(),
		tags,
		u.Username,
		u.MilpacUrl,
		awolFigures(u.DaysAWOL, u.RawDaysSincePost),
	)
}

// awolThreadTag renders a row tag as a link to its LOA thread, or bare when
// the thread ID is unknown.
func awolThreadTag(tag string, threadID int64) string {
	if threadID == 0 {
		return tag + " "
	}
	return fmt.Sprintf("[%s](%s) ", tag, loaThreadURL(threadID))
}

// awolFileThreadLine renders a file entry's line giving a tag its LOA
// thread, or "" when the thread ID is unknown and the tag stays bare.
func awolFileThreadLine(tag string, threadID int64) string {
	if threadID == 0 {
		return ""
	}
	return tag + " " + loaThreadURL(threadID) + "\n"
}

// awolFigures renders a row's days AWOL and its raw days since the last
// post, as both the embed and the file show them.
func awolFigures(daysAWOL, rawDaysSincePost int) string {
	return fmt.Sprintf("%dd AWOL · last post %dd", daysAWOL, rawDaysSincePost)
}

func sendAwolFile(r utils.InteractionResponder, i *discordgo.InteractionCreate, awolUsers []AwolUser, position string, forceFile, loasRead bool, now time.Time, skippedCount int) {
	var content strings.Builder
	_, _ = fmt.Fprintf(&content, "AWOL Report for %s\nGenerated: %s\n%s\n\n",
		position,
		now.Format("2006-01-02 15:04:05"),
		awolSummaryLine(len(awolUsers)))
	if note := awolSkippedNote(skippedCount); note != "" {
		// Issue #163: the file report must surface partial coverage too.
		_, _ = fmt.Fprintf(&content, "%s\n\n", note)
	}
	if !loasRead {
		// ADR 0008: surface the degraded warning in the file too — same figures
		// (raw) and the same warning as the embed.
		_, _ = fmt.Fprintf(&content, "%s\n\n", awolDegradedFooter)
	}

	for _, user := range awolUsers {
		tags, threadLines := "", ""
		if user.OnLOA() {
			tags = " " + awolLOATag
			threadLines = awolFileThreadLine(awolLOATag, user.loaWindow.ThreadID)
		}
		if user.reviewLOA != nil {
			tags += " " + awolNeedsReviewTag
			threadLines += awolFileThreadLine(awolNeedsReviewTag, user.reviewLOA.threadID)
		}
		_, _ = fmt.Fprintf(&content, "%s%s — %s\nMilpac: %s\n%s\n",
			user.Username,
			tags,
			awolFigures(user.DaysAWOL, user.RawDaysSincePost),
			user.MilpacUrl,
			threadLines)
	}
	if loasRead {
		_, _ = fmt.Fprintf(&content, "%s\n", awolReportFooter)
	}

	file := &discordgo.File{
		Name:        fmt.Sprintf("awol_report_%s.txt", strings.ReplaceAll(position, "/", "-")),
		ContentType: "text/plain",
		Reader:      strings.NewReader(content.String()),
	}
	var prefix string
	if forceFile {
		prefix = fmt.Sprintf(awolForcedFileFormat, position)
	} else {
		prefix = fmt.Sprintf(awolLargeFileFormat, position)
	}
	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: stringPtr(prefix),
		Files:   []*discordgo.File{file},
	}); err != nil {
		captureDeferredEditFailure(i, "awol", err)
	}
}

// loaWindowsByForumUserID files each open, upcoming and ended LOA under its
// Subject's forum user ID, the only key /awol matches an LOA to a trooper
// on. Usernames are never compared, so an LOA keeps counting for a trooper
// who has renamed. A needs-review LOA covers none of its days, so a mistyped
// LOA can't hide anyone (ADR 0008), and neither does an LOA in a state the
// bot doesn't know. An LOA whose dates don't read is skipped.
func loaWindowsByForumUserID(loas []utils.LOA) map[string][]utils.LOAEntry {
	byID := map[string][]utils.LOAEntry{}
	for _, l := range loas {
		switch l.State {
		case utils.LOAStateOpen, utils.LOAStateUpcoming, utils.LOAStateEnded:
		default:
			continue
		}
		entry, ok := loaEntryOf(l)
		if !ok {
			continue
		}
		byID[l.Subject.UserID] = append(byID[l.Subject.UserID], entry)
	}
	return byID
}

// awolReviewLOA picks the needs-review LOA that tags a trooper's row: the
// latest of reviews posted no earlier than awolReviewLookbackDays UTC dates
// before their last forum post, or nil when none was.
func awolReviewLOA(reviews []awolReview, lastPost time.Time) *awolReview {
	last := lastPost.UTC()
	cutoff := time.Date(last.Year(), last.Month(), last.Day()-awolReviewLookbackDays, 0, 0, 0, 0, time.UTC)
	var picked *awolReview
	for i := range reviews {
		if reviews[i].posted.Before(cutoff) {
			continue
		}
		if picked == nil || reviews[i].posted.After(picked.posted) {
			picked = &reviews[i]
		}
	}
	return picked
}

// needsReviewByForumUserID files each needs-review LOA under its Subject's
// forum user ID, as loaWindowsByForumUserID files the LOAs that count.
func needsReviewByForumUserID(loas []utils.LOA) map[string][]awolReview {
	byID := map[string][]awolReview{}
	for _, l := range loas {
		if l.State != utils.LOAStateNeedsReview {
			continue
		}
		byID[l.Subject.UserID] = append(byID[l.Subject.UserID], awolReview{
			threadID: loaThreadID(l),
			posted:   time.Unix(l.PostDate, 0),
		})
	}
	return byID
}
