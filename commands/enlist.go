package commands

import (


	"github.com/bwmarrin/discordgo"
	"fmt"

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
	const enlistImageURL = "https://wiki.7cav.us/images/3/38/Cav_Enlistment_Infographic_1000_2000px_1_1.png"

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
// Passing the image inside the Embeds array forces Discord to render it
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
		fmt.Printf("Error sending enlist command response: %v\n", err)
	}
}
