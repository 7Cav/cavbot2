package commands

import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"regexp"
	"time"
)

func GamertagSearch() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "gamertag_search",
			Description: "Search for a user by gamertag",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "gamertag",
					Description: "Gamertag to search for.",
					Required:    true,
				},
			},
		},
		Handler: handleGamertagSearchCommand,
	}
}

func handleGamertagSearchCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	gamertag := i.ApplicationCommandData().Options[0].StringValue()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching milpac data for gamertag `%s`...", gamertag),
		},
	})
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}
	utils.Info("Gamertag search requested", "command", "GamertagSearch", "gamertag", gamertag, "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
	user, err := utils.GetUserByGamertag(ctx, gamertag)
	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to fetch user: %v", err))
		return
	}
	utils.Info("User found", "command", "GamertagSearch", "gamertag", gamertag, "username", user.User.Username, "discord_id", user.DiscordID)
	matches := regexp.MustCompile(`/\d+/(\d+)\.jpg`).FindStringSubmatch(user.UniformUrl)
	if len(matches) < 2 {
		utils.HandleError(utils.NewSessionResponder(s), i, "❌ Failed to parse uniform URL")
		return
	}
	id := matches[1]
	milpacUrl := fmt.Sprintf("https://7cav.us/rosters/profile/%s", id)
	response := fmt.Sprintf("Found user for gamertag `%s`: [%s %s](%s)", gamertag, user.Rank.RankShort, user.User.Username, milpacUrl)

	_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &response,
	})

	if err != nil {
		utils.HandleError(utils.NewSessionResponder(s), i, fmt.Sprintf("❌ Failed to edit response: %v", err))
		return
	}
	utils.Info("✨ Done!", "command", "GamertagSearch")
}
