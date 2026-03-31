package commands

import (
	"fmt"
	"math/rand"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

func ForumFont() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "forumfont",
			Description: "Submit an opt-out request for the new forum font",
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			username := i.Member.User.Username
			if i.Member.Nick != "" {
				username = i.Member.Nick
			}
			utils.Info("Forum font opt-out requested", "command", "ForumFont", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
			queuePos := rand.Intn(200) + 350
			hours := rand.Intn(6) + 22
			minutes := rand.Intn(59) + 1
			ticketNum := rand.Intn(9000) + 1000
			ticketSuffix := string(rune('A' + rand.Intn(26)))
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("[CAVBOT]: Opt-out request for User [%s] received. Ticket #%d-%s has been generated. Your current position in the processing queue: %d. Estimated time until font reversion: %d hours, %d minutes.", username, ticketNum, ticketSuffix, queuePos, hours, minutes),
				},
			})
			if err != nil {
				utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
				return
			}
			utils.Info("✨ Done!", "command", "ForumFont")
		},
	}
}
