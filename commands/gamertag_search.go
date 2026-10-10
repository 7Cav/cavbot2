package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
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
	runGamertagSearch(utils.NewSessionResponder(s), i)
}

func runGamertagSearch(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	gamertag := i.ApplicationCommandData().Options[0].StringValue()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := acknowledge(r, i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Fetching milpac data for gamertag `%s`...", gamertag),
		},
	})
	if err != nil {
		replyAckFailed(r, i, err)
		return
	}
	username, discordID := interactionUsernameAndID(i)
	utils.Info("Gamertag search requested", "command", "GamertagSearch", "gamertag", gamertag, "username", username, "discord_id", discordID)

	user, err := utils.GetUserByGamertag(ctx, gamertag)
	if errors.Is(err, utils.ErrNotFound) {
		replyError(r, i, fmt.Sprintf("❌ %s `%s`. Check the spelling and try again.", gamertagNotFound, gamertag))
		return
	}
	if err != nil {
		replyLookupFailed(r, i, "gamertag_search", err)
		return
	}
	utils.Info("User found", "command", "GamertagSearch", "gamertag", gamertag, "username", user.User.Username, "discord_id", user.DiscordID)

	id, err := utils.ExtractMilpacIDFromUniformURL(user.UniformUrl)
	if err != nil {
		replyError(r, i, "❌ Failed to parse uniform URL")
		return
	}
	milpacUrl := fmt.Sprintf("https://7cav.us/rosters/profile/%s", id)
	response := fmt.Sprintf("Found user for gamertag `%s`: [%s %s](%s)", gamertag, user.Rank.RankShort, user.User.Username, milpacUrl)

	if err := r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &response}); err != nil {
		captureDeferredEditFailure(i, "gamertag_search", err)
		return
	}
	utils.Info("✨ Done!", "command", "GamertagSearch")
}

// gamertagNotFound opens the reply to /gamertag_search for a gamertag no
// trooper has.
const gamertagNotFound = "No trooper has the gamertag"
