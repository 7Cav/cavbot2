package commands

import (
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"strings"
)

// enlistFormURL is the application itself. The infographic names this address,
// but only as pixels inside the image, so the card carries it as text too.
const enlistFormURL = "https://7cav.us/enlist"

// enlistImageURL is the enlistment infographic on the wiki. The five steps of
// the process live only inside this image.
const enlistImageURL = "https://wiki.7cav.us/images/3/38/Cav_Enlistment_Infographic_1000_2000px_1_1.png"

func Enlist() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "enlist",
			Description: "Show the enlistment process",
		},
		Handler: handleEnlistCommand,
	}
}

func handleEnlistCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runEnlist(utils.NewSessionResponder(s), i)
}

func runEnlist(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	username, discordID := interactionUsernameAndID(i)
	utils.Info("🚀 Starting Enlist", "command", "Enlist", "username", username,
		"discord_id", discordID)

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{
				{
					Title: "Enlistment Process",
					Description: fmt.Sprintf("Start your application at [%s](%s). The steps below show what to expect.",
						strings.TrimPrefix(enlistFormURL, "https://"), enlistFormURL),
					Color: 0xfbcc29, // Cav Yellow
					Image: &discordgo.MessageEmbedImage{
						URL: enlistImageURL,
					},
				},
			},
		},
	})

	if err != nil {
		utils.Error("❌ Interaction response failed", "error", err)
		utils.HandleError(r, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
		return
	}

	utils.Info("✨ Done!", "command", "Enlist")
}
