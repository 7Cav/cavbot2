package commands

import (
	"fmt"
	"math/rand"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

func FixTickets() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "fixtickets",
			Description: "Clear the font reversion ticket backlog",
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			utils.Info("Fix tickets requested", "command", "FixTickets", "username", i.Member.User.Username, "discord_id", i.Member.User.ID)
			processed := rand.Intn(200) + 350
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("[CAVBOT]: ⚠️ WARNING: Ticket queue jam detected in the font reversion pipeline. Initiating manual flush... done. %d tickets have been bulk-processed and all pending font reversions have been applied. Apologies for the delay — a paperclip had lodged itself in the ticket processor. All users should now be restored to their original font. Sorry about that!", processed),
				},
			})
			if err != nil {
				utils.HandleError(s, i, fmt.Sprintf("❌ Failed to respond to interaction: %v", err))
				return
			}
			utils.Info("✨ Done!", "command", "FixTickets")
		},
	}
}
