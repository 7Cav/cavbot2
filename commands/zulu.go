package commands

import (
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"log"
	"strings"
	"time"
)

func Zulu() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "zulu",
			Description: "Returns the current Zulu time",
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			zuluTime := time.Now().UTC().Format("15:04:05 02Jan06")
			formattedZuluTime := strings.ToUpper(zuluTime)
			log.Printf("Zulu time requested by %s %s", i.Member.User.Username, i.Member.User.ID)
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("The current Zulu time is: %s", formattedZuluTime),
				},
			})
			if err != nil {
				utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
				return
			}
		},
	}
}
