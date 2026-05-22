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

// loaUnavailableMessage formats the user-facing string shown when GlobalLOACache
// is unhealthy. lastRefresh is the cache's last successful refresh (zero == never);
// now is passed in so callers can read time.Now() once and tests stay deterministic.
func loaUnavailableMessage(lastRefresh, now time.Time) string {
	if lastRefresh.IsZero() {
		return "❌ LOA cache unavailable (never successfully refreshed). Try again shortly."
	}
	mins := int(now.Sub(lastRefresh).Minutes())
	return fmt.Sprintf("❌ LOA cache unavailable (last refresh: %d minutes ago). Try again shortly.", mins)
}

type LOAUser struct {
	Username  string
	MilpacUrl string
	StartDate time.Time
	EndDate   time.Time
	ThreadID  int64
	Active    bool
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
	utils.Info("🚀 Starting LOA check", "command", "LOA", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	position := i.ApplicationCommandData().Options[0].StringValue()

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching LOA data for %s...", position),
		},
	})
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, position)
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to fetch roster: %v", err))
		return
	}
	if len(roster.LiteProfiles) == 0 {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ The search for \"%s\" returned no troopers. Please check your search for accuracy.", position))
		return
	}

	urlRe := regexp.MustCompile(`/\d+/(\d+)\.jpg`)
	now := time.Now()
	var activeLOAs, upcomingLOAs []LOAUser

	for _, member := range roster.LiteProfiles {
		entry, ok := utils.GlobalLOACache.GetEntry(member.User.Username)
		if !ok {
			continue
		}

		matches := urlRe.FindStringSubmatch(member.UniformUrl)
		if len(matches) < 2 {
			continue
		}
		milpacUrl := fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1])

		user := LOAUser{
			Username:  member.User.Username,
			MilpacUrl: milpacUrl,
			StartDate: entry.StartDate,
			EndDate:   entry.EndDate,
			ThreadID:  entry.ThreadID,
		}

		if !now.Before(entry.StartDate) && !now.After(entry.EndDate) {
			user.Active = true
			activeLOAs = append(activeLOAs, user)
		} else if now.Before(entry.StartDate) {
			upcomingLOAs = append(upcomingLOAs, user)
		}
	}

	if len(activeLOAs) == 0 && len(upcomingLOAs) == 0 {
		response := fmt.Sprintf("No active or upcoming LOAs found for \"%s\".", position)
		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &response,
		})
		if err != nil {
			utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to edit response: %v", err))
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
		desc.WriteString("**Active LOAs**\n")
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
		desc.WriteString("**Upcoming LOAs**\n")
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
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: stringPtr(""),
		Embeds:  &embeds,
	})
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}

	utils.Info("✨ Done!", "command", "LOA")
}
