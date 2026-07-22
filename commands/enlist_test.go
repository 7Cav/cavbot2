package commands

import (
	"context"
	"fmt"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
	"regexp"
	"sort"
	"strings"
)

// Enlist defines the global structure for the /enlist slash command
func Enlist() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "enlist",
			Description: "Show the Enlistment Process",
		},
		Handler: handleEnlistCommand,
	}
}
// handleEnlistCommand executes when someone triggers /enlist
func handleEnlistCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// The 7th Cavalry attachment image URL
	const enlistImageURL = "https://7cav.us/attachments/2078/"

	// Construct the interaction response payload
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: enlistImageURL,
		},
	})

	if err != nil {
		fmt.Printf("Error sending enlist command response: %v\n", err)
	}
}
