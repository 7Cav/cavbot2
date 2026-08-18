package commands

import (

	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/7cav/cavbot2/utils"
)

func Enlist() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "enlist",
			Description: "Show the Enlistment Process",
		},
		Handler: handleEnlistCommand,
	}
}
func handleEnlistCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	runEnlist(utils.NewSessionResponder(s), i)
}

func runEnlist(r utils.InteractionResponder, i *discordgo.InteractionCreate) {
	const enlistImageURL = "https://wiki.7cav.us/images/3/38/Cav_Enlistment_Infographic_1000_2000px_1_1.png"

	err := r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{
				{
					Title:       "Enlistment Process",
					Description: "Follow the steps below to enlist.",
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
}
