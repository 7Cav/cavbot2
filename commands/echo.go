package commands

import (
	"github.com/bwmarrin/discordgo"
	"log"
)

func Echo() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "echo",
			Description: "Repeats your message",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "message",
					Description: "The message to repeat",
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

			content := ""
			if option, ok := optionMap["message"]; ok {
				content = option.StringValue()
			}

			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: content,
				},
			})
			if err != nil {
				log.Printf("Failed to respond to interaction: %v\n", err)
			}
		},
	}
}
