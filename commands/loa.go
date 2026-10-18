package commands

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The headings /loa lists LOAs under: Active for the LOAs the 7Cav API
// states open, Upcoming for the ones it states upcoming.
const (
	loaActiveHeading   = "**Active LOAs**"
	loaUpcomingHeading = "**Upcoming LOAs**"
)

// loaNoneFormat answers a position none of whose members has an open or
// upcoming LOA, with the position in place of %s.
const loaNoneFormat = "No active or upcoming LOAs found for \"%s\"."

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

func LOA() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "loa",
			Description: "Show active and upcoming LOAs for a position.",
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

	// The LOA call runs alongside the roster call, so it adds no wait. If
	// the run ends early, the buffered channel lets the call finish.
	type loasAnswer struct {
		loas *utils.LOAsResponse
		err  error
	}
	loasCh := make(chan loasAnswer, 1)
	go func() {
		loas, err := utils.GetCurrentLOAs(ctx)
		loasCh <- loasAnswer{loas, err}
	}()

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

	activeLOAs, upcomingLOAs := loaUsersByState(roster, answer.loas.LOAs)

	if len(activeLOAs) == 0 && len(upcomingLOAs) == 0 {
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
	if len(activeLOAs) > 0 {
		desc.WriteString(loaActiveHeading + "\n")
		for _, u := range activeLOAs {
			threadTag := ""
			if u.ThreadID != 0 {
				threadTag = fmt.Sprintf(" [[Thread]](https://7cav.us/threads/%d/)", u.ThreadID)
			}
			_, _ = fmt.Fprintf(&desc, "[%s](%s) — %s → %s%s\n",
				u.Username, u.MilpacUrl,
				u.StartDate.Format("Jan 2, 2006"),
				u.EndDate.Format("Jan 2, 2006"),
				threadTag)
		}
	}
	if len(upcomingLOAs) > 0 {
		if len(activeLOAs) > 0 {
			desc.WriteString("\n")
		}
		desc.WriteString(loaUpcomingHeading + "\n")
		for _, u := range upcomingLOAs {
			threadTag := ""
			if u.ThreadID != 0 {
				threadTag = fmt.Sprintf(" [[Thread]](https://7cav.us/threads/%d/)", u.ThreadID)
			}
			_, _ = fmt.Fprintf(&desc, "[%s](%s) — %s → %s%s\n",
				u.Username, u.MilpacUrl,
				u.StartDate.Format("Jan 2, 2006"),
				u.EndDate.Format("Jan 2, 2006"),
				threadTag)
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("LOAs for %s", position),
		Description: desc.String(),
		Color:       0xfbcc29,
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Active: %d | Upcoming: %d", len(activeLOAs), len(upcomingLOAs)),
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

// loaUsersByState lists each LOA the 7Cav API states open or upcoming under
// the roster member whose forum user ID equals its Subject's, one entry per
// LOA. Usernames are never compared, so an LOA filed for a trooper by
// someone else, or before a rename, is listed for its Subject. A
// needs-review or ended LOA is listed in neither, and so is an LOA whose
// dates don't read or whose Subject is no member of the roster.
func loaUsersByState(roster *utils.LiteRosterResponse, loas []utils.LOA) (active, upcoming []LOAUser) {
	membersByForumUserID := map[string]utils.LiteProfileResponse{}
	for _, member := range roster.LiteProfiles {
		membersByForumUserID[member.User.UserID] = member
	}
	for _, l := range loas {
		if l.State != utils.LOAStateOpen && l.State != utils.LOAStateUpcoming {
			continue
		}
		member, ok := membersByForumUserID[l.Subject.UserID]
		if !ok {
			continue
		}
		milpacID, err := utils.ExtractMilpacIDFromUniformURL(member.UniformUrl)
		if err != nil {
			continue
		}
		start, startErr := time.Parse(time.DateOnly, l.StartDate)
		end, endErr := time.Parse(time.DateOnly, l.EndDate)
		if startErr != nil || endErr != nil {
			utils.Warn("LOA skipped: dates unreadable",
				"thread_id", l.ThreadID, "start", l.StartDate, "end", l.EndDate)
			continue
		}
		// A thread ID that doesn't parse leaves the line without a thread link.
		threadID, _ := strconv.ParseInt(l.ThreadID, 10, 64)
		user := LOAUser{
			Username:  member.User.Username,
			MilpacUrl: fmt.Sprintf("https://7cav.us/rosters/profile/%s", milpacID),
			StartDate: start,
			EndDate:   end,
			ThreadID:  threadID,
		}
		if l.State == utils.LOAStateOpen {
			active = append(active, user)
		} else {
			upcoming = append(upcoming, user)
		}
	}
	return active, upcoming
}
