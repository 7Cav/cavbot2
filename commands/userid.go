// commands/userid.go
package commands

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log"
)

func UserID() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "userid",
			Description: "Returns the ID of the mentioned user",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "The user to get the ID for",
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

			user := optionMap["user"].UserValue(s)
			response := fmt.Sprintf("%s's ID is: %s", user.Username, user.ID)

			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: response,
				},
			})
			if err != nil {
				log.Printf("Failed to respond to interaction: %v\n", err)
			}
		},
	}
}
