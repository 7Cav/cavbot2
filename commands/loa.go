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

// The headings /loa lists LOAs under: Active for the LOAs the 7Cav API
// states open, Upcoming for the ones it states upcoming, and Needs review
// for the ones it can't take as written.
const (
	loaActiveHeading      = "**Active LOAs**"
	loaUpcomingHeading    = "**Upcoming LOAs**"
	loaNeedsReviewHeading = "**Needs review**"
)

// What a needs-review LOA's line says is wrong with it, one phrase for each
// rule the 7Cav API names.
const (
	loaReviewDatesUnreadable = "dates can't be read"
	loaReviewEndBeforeStart  = "ends before it starts"
	loaReviewTooLong         = "runs more than 14 days"
	// loaReviewOtherRule stands in for a rule with no phrase of its own,
	// such as one the API added since, so /loa still lists the LOA and the
	// leader reads its thread.
	loaReviewOtherRule = "can't be taken as written"
)

// loaReviewProblems holds the phrase for each rule the 7Cav API names.
var loaReviewProblems = map[string]string{
	utils.LOAReviewReasonDatesUnreadable: loaReviewDatesUnreadable,
	utils.LOAReviewReasonEndBeforeStart:  loaReviewEndBeforeStart,
	utils.LOAReviewReasonTooLong:         loaReviewTooLong,
}

// loaReviewProblem says what is wrong with an LOA that tripped reason, or
// gives loaReviewOtherRule for a rule with no phrase of its own.
func loaReviewProblem(reason string) string {
	if problem, ok := loaReviewProblems[reason]; ok {
		return problem
	}
	return loaReviewOtherRule
}

// loaNoneFormat answers a position none of whose members has an open,
// upcoming or needs-review LOA, with the position in place of %s.
const loaNoneFormat = "No active, upcoming or needs-review LOAs found for \"%s\"."

// loaUnavailableReply answers a run the LOAs couldn't be read for. It stands
// in for the whole list, so an empty list never reads as "nobody is on
// leave".
const loaUnavailableReply = "❌ LOAs couldn't be read from the 7Cav API, so they can't be listed right now. Try again in a few minutes."

type LOAUser struct {
	Username  string
	MilpacUrl string
	StartDate time.Time
	EndDate   time.Time
	ThreadID  int64
}

// loaReview is a needs-review LOA as /loa lists it: its Subject, its thread
// and what is wrong with it in plain words.
type loaReview struct {
	Username  string
	MilpacUrl string
	ThreadID  int64
	Problem   string
}

func LOA() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "loa",
			Description: "Show active, upcoming and needs-review LOAs for a position.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "position",
					Description: "Position to check for LOAs. (EX: 2/B/1-7 | Reservist | S1)",
					Required:    true,
				},
			},
		},
		Handler: handleLOACommand,
	}
}

func handleLOACommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runLoa(utils.NewSessionResponder(s), time.Now(), i)
}

func runLoa(r utils.InteractionResponder, now time.Time, i *discordgo.InteractionCreate) {
	username, discordID := interactionUsernameAndID(i)
	utils.Info("🚀 Starting LOA check", "command", "LOA", "username", username, "discord_id", discordID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	position := i.ApplicationCommandData().Options[0].StringValue()

	err := acknowledge(r, i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching LOA data for %s...", position),
		},
	})
	if err != nil {
		replyAckFailed(r, i, err)
		return
	}

	loasCh := callLOAsAlongside(func() (*utils.LOAsResponse, error) {
		return utils.GetCurrentLOAs(ctx)
	})

	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, position)
	if err != nil {
		replyLookupFailed(r, i, "loa", err)
		return
	}
	if len(roster.LiteProfiles) == 0 {
		replyError(r, i, emptyRosterSearchMessage(position))
		return
	}

	answer := <-loasCh
	if answer.err != nil {
		captureError("LOAs unreadable, /loa can't list them", answer.err,
			"command", "loa", "guild_id", i.GuildID, "position", position)
		replyError(r, i, loaUnavailableReply)
		return
	}

	activeLOAs, upcomingLOAs, reviews := loaUsersByState(roster, answer.loas.LOAs)

	if len(activeLOAs) == 0 && len(upcomingLOAs) == 0 && len(reviews) == 0 {
		response := fmt.Sprintf(loaNoneFormat, position)
		if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &response,
		}); err != nil {
			captureDeferredEditFailure(i, "loa", err)
		}
		return
	}

	sort.Slice(activeLOAs, func(a, b int) bool {
		return activeLOAs[a].EndDate.Before(activeLOAs[b].EndDate)
	})
	sort.Slice(upcomingLOAs, func(a, b int) bool {
		return upcomingLOAs[a].StartDate.Before(upcomingLOAs[b].StartDate)
	})

	var desc strings.Builder
	writeLOASection(&desc, loaActiveHeading, activeLOAs)
	writeLOASection(&desc, loaUpcomingHeading, upcomingLOAs)
	writeLOAReviewSection(&desc, reviews)

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("LOAs for %s", position),
		Description: desc.String(),
		Color:       0xfbcc29,
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Active: %d | Upcoming: %d | Needs review: %d", len(activeLOAs), len(upcomingLOAs), len(reviews)),
		},
		Timestamp: now.Format(time.RFC3339),
	}

	embeds := []*discordgo.MessageEmbed{embed}
	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: stringPtr(""),
		Embeds:  &embeds,
	}); err != nil {
		captureDeferredEditFailure(i, "loa", err)
		return
	}

	utils.Info("✨ Done!", "command", "LOA")
}

// writeLOASection writes heading and a line for each of users, linking the
// trooper's milpac, when known, and the LOA's thread. It writes nothing for
// no users, and a blank line before the heading when a section came before.
func writeLOASection(desc *strings.Builder, heading string, users []LOAUser) {
	if len(users) == 0 {
		return
	}
	writeLOAHeading(desc, heading)
	for _, u := range users {
		_, _ = fmt.Fprintf(desc, "%s — %s → %s%s\n",
			loaTrooper(u.Username, u.MilpacUrl),
			u.StartDate.Format("Jan 2, 2006"),
			u.EndDate.Format("Jan 2, 2006"),
			loaThreadTag(u.ThreadID))
	}
}

// writeLOAReviewSection writes the Needs review heading and a line for each
// of reviews saying what is wrong with the LOA, linked as writeLOASection
// links its lines. It writes nothing for no reviews.
func writeLOAReviewSection(desc *strings.Builder, reviews []loaReview) {
	if len(reviews) == 0 {
		return
	}
	writeLOAHeading(desc, loaNeedsReviewHeading)
	for _, r := range reviews {
		_, _ = fmt.Fprintf(desc, "%s — %s%s\n",
			loaTrooper(r.Username, r.MilpacUrl), r.Problem, loaThreadTag(r.ThreadID))
	}
}

// writeLOAHeading writes heading on its own line, after a blank line when a
// section came before it.
func writeLOAHeading(desc *strings.Builder, heading string) {
	if desc.Len() > 0 {
		desc.WriteString("\n")
	}
	desc.WriteString(heading + "\n")
}

// loaTrooper is username linked to milpacURL, or username alone when the
// link is unknown.
func loaTrooper(username, milpacURL string) string {
	if milpacURL == "" {
		return username
	}
	return fmt.Sprintf("[%s](%s)", username, milpacURL)
}

// loaThreadTag links an LOA's thread, or is "" when its thread ID is unknown.
func loaThreadTag(threadID int64) string {
	if threadID == 0 {
		return ""
	}
	return fmt.Sprintf(" [[Thread]](https://7cav.us/threads/%d/)", threadID)
}

// loaUsersByState lists each LOA the 7Cav API states open, upcoming or
// needs review under the roster member whose forum user ID equals its
// Subject's, one entry per LOA. Usernames are never compared, so an LOA
// filed for a trooper by someone else, or before a rename, is listed for its
// Subject. An ended LOA is listed nowhere, and so is an open or upcoming LOA
// whose dates don't read, or an LOA whose Subject is no member of the
// roster. A member whose milpac link can't be built is still listed, by
// name.
func loaUsersByState(roster *utils.LiteRosterResponse, loas []utils.LOA) (active, upcoming []LOAUser, reviews []loaReview) {
	membersByForumUserID := map[string]utils.LiteProfileResponse{}
	for _, member := range roster.LiteProfiles {
		membersByForumUserID[member.User.UserID] = member
	}
	// milpacURLs holds each listed member's milpac link by forum user ID, so
	// a uniform URL that doesn't parse reaches Sentry once per member.
	milpacURLs := map[string]string{}
	milpacURLOf := func(member utils.LiteProfileResponse) string {
		milpacURL, read := milpacURLs[member.User.UserID]
		if !read {
			milpacURL = loaMilpacURL(member)
			milpacURLs[member.User.UserID] = milpacURL
		}
		return milpacURL
	}
	for _, l := range loas {
		member, ok := membersByForumUserID[l.Subject.UserID]
		if !ok {
			continue
		}
		switch l.State {
		case utils.LOAStateNeedsReview:
			reviews = append(reviews, loaReview{
				Username:  member.User.Username,
				MilpacUrl: milpacURLOf(member),
				ThreadID:  loaThreadID(l),
				Problem:   loaReviewProblem(l.ReviewReason),
			})
		case utils.LOAStateOpen, utils.LOAStateUpcoming:
			entry, ok := loaEntryOf(l)
			if !ok {
				continue
			}
			user := LOAUser{
				Username:  member.User.Username,
				MilpacUrl: milpacURLOf(member),
				StartDate: entry.StartDate,
				EndDate:   entry.EndDate,
				ThreadID:  entry.ThreadID,
			}
			if l.State == utils.LOAStateOpen {
				active = append(active, user)
			} else {
				upcoming = append(upcoming, user)
			}
		}
	}
	return active, upcoming, reviews
}

// loaMilpacURL builds member's milpac profile link from their uniform URL.
// A uniform URL that doesn't parse is a bad roster record: it goes to
// Sentry, and the link is "", so the trooper's LOA still shows by name.
func loaMilpacURL(member utils.LiteProfileResponse) string {
	milpacID, err := utils.ExtractMilpacIDFromUniformURL(member.UniformUrl)
	if err != nil {
		captureError("LOA listed without a milpac link: unparseable uniform URL", err,
			"command", "loa",
			"username", member.User.Username,
			"discord_id", member.DiscordID,
			"uniform_url", member.UniformUrl,
		)
		return ""
	}
	return fmt.Sprintf("https://7cav.us/rosters/profile/%s", milpacID)
}
