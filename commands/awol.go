package commands

import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"regexp"
	"sort"
	"strings"
	"time"
)

type AwolUser struct {
	Username          string
	MilpacUrl         string
	TimeSinceLastPost string
	LastPostDate      time.Time
}

func Awol() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "awol",
			Description: "Return the awol users for a position",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "position",
					Description: "Position to check for awols",
					Required:    true,
				},
			},
		},
		Handler: handleAwolCommand,
	}
}

func handleAwolCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	utils.Info("🚀 Starting AWOL check", "command", "Awol", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching AWOL data for %s...", i.ApplicationCommandData().Options[0].StringValue()),
		},
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}
	position := i.ApplicationCommandData().Options[0].StringValue()
	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, position)
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to fetch roster: %v", err))
		return
	}
	awolUsers := []AwolUser{}
	for _, member := range roster.LiteProfiles {
		if member.User.Username == "Tester.B" || strings.Contains(member.Rank.RankFull, "General") {
			continue
		}
		lastPostDate, err := time.Parse("2006-01-02 15:04:05", member.LastForumPostDate)
		if err != nil {
			utils.HandleError(s, i, fmt.Sprintf("❌ Failed to parse last forum post date: %v", err))
			return
		}
		if lastPostDate.Before(time.Now().AddDate(0, 0, -8)) {
			matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(member.UniformUrl)
			if len(matches) < 2 {
				utils.HandleError(s, i, "❌ Failed to parse uniform URL")
				return
			}
			awolUsers = append(awolUsers, AwolUser{
				Username:          member.User.Username,
				MilpacUrl:         fmt.Sprintf("https://7cav.us/rosters/profile/%s", matches[1]),
				TimeSinceLastPost: utils.FormatTimeSinceDuration(lastPostDate),
				LastPostDate:      lastPostDate,
			})
		}
	}
	sort.Slice(awolUsers, func(i, j int) bool {
		return awolUsers[i].LastPostDate.Before(awolUsers[j].LastPostDate)
	})
	awolUserOutput := make([]string, 0)
	for _, user := range awolUsers {
		awolUserOutput = append(awolUserOutput, fmt.Sprintf("[%s](<%s>) (%s)", user.Username, user.MilpacUrl, user.TimeSinceLastPost))
	}
	var response string
	if len(awolUserOutput) > 0 {
		response = fmt.Sprintf("The following users for search \"%s\" are AWOL:\n%s", position, strings.Join(awolUserOutput, "\n"))
	} else {
		response = fmt.Sprintf("No users for search \"%s\" are AWOL", position)
	}
	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})
	if err != nil {
		utils.HandleError(s, i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "Awol")
}
