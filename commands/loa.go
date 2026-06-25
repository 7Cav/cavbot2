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

// loaCacheView is the minimal LOA-cache surface /loa consumes: per-member
// entry lookup plus a health probe for the staleness guard. Production wires
// *utils.LOACache (GlobalLOACache); tests substitute a fake with canned
// entries and a forced health verdict so the handler stays deterministic
// without touching the process-global singleton. /awol's loaCacheReader is a
// separate, per-command minimal surface over the same cache; the two share
// IsHealthy but read differently — /loa uses GetEntry (one most-relevant window)
// while /awol uses GetEntries (the full history, #159). They are kept distinct so
// each command's dependency stays scoped to what it actually reads. /awol gates on
// IsHealthy to render its On-LOA column (#96); /loa uses it for the staleness
// guard above.
type loaCacheView interface {
	GetEntry(username string) (utils.LOAEntry, bool)
	IsHealthy(maxAge time.Duration) (bool, time.Time)
}

// loaCacheMaxAge is the staleness threshold for the LOA cache health probe,
// shared by /loa and /awol. 2× the 15-min refresh interval: one missed refresh
// is tolerated, two consecutive misses mark the cache unhealthy.
const loaCacheMaxAge = 30 * time.Minute

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
	runLoa(utils.NewSessionResponder(s), utils.GlobalLOACache, time.Now(), i)
}

func runLoa(r utils.InteractionResponder, cache loaCacheView, now time.Time, i *discordgo.InteractionCreate) {
	username, discordID := interactionUsernameAndID(i)
	utils.Info("🚀 Starting LOA check", "command", "LOA", "username", username, "discord_id", discordID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	position := i.ApplicationCommandData().Options[0].StringValue()

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching LOA data for %s...", position),
		},
	})
	if err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	healthy, lastRefresh := cache.IsHealthy(loaCacheMaxAge)
	if !healthy {
		msg := loaUnavailableMessage(lastRefresh, now)
		utils.Debug("LOA command served unavailable message",
			"command", "LOA",
			"username", username,
			"discord_id", discordID,
			"last_success", lastRefresh,
			"served_at", now,
			"staleness", now.Sub(lastRefresh),
		)
		if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &msg}); err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		}
		return
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

	urlRe := regexp.MustCompile(`/\d+/(\d+)\.jpg`)
	var activeLOAs, upcomingLOAs []LOAUser

	for _, member := range roster.LiteProfiles {
		// #158/S3: /loa renders exactly one window per user via the single-value
		// GetEntry (most-relevant) contract, deliberately NOT iterating GetEntries.
		// This preserves the pre-history-store rendering; pinned by
		// TestGetEntry_SingleWindowContract_ForLoaRendering in utils.
		entry, ok := cache.GetEntry(member.User.Username)
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
		if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &response,
		}); err != nil {
			utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
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
	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: stringPtr(""),
		Embeds:  &embeds,
	}); err != nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}

	utils.Info("✨ Done!", "command", "LOA")
}
