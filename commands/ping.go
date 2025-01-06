package commands

import (
	"github.com/bwmarrin/discordgo"
	"log"
)

func Ping() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "ping",
			Description: "Responds with Pong!",
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Pong!",
				},
			})
			if err != nil {
				log.Printf("Failed to respond to interaction: %v\n", err)
			}
		},
	}
}
