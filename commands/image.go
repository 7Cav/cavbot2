package commands

import (
	"github.com/bwmarrin/discordgo"
	"log"
)

func Image() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "image",
			Description: "Returns a specified image",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "url",
					Description: "The URL of the image to send",
					Required:    true,
				},
			},
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			options := i.ApplicationCommandData().Options
			optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
			for _, opt := range options {
				optionMap[opt.Name] = opt
			}

			imageURL := optionMap["url"].StringValue()

			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{
						{
							Image: &discordgo.MessageEmbedImage{
								URL: imageURL,
							},
						},
					},
				},
			})
			if err != nil {
				log.Printf("Failed to respond to interaction: %v\n", err)
			}
		},
	}
}
