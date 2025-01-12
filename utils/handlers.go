package utils

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log"
	"regexp"
	"strings"
)

func HandleError(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	log.Printf("Error handling interaction: %s", message)

	if i.Type == discordgo.InteractionApplicationCommand {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: message,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			if strings.Contains(err.Error(), "already been acknowledged") {
				log.Printf("Retrying error response as edit: %v", err)
				_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &message,
				})
				if err != nil {
					log.Printf("Failed to send error message: %v", err)
				}
			} else {
				log.Printf("Failed to send error message: %v", err)
			}
		}
	} else {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: message,
			},
		})
		if err != nil {
			if strings.Contains(err.Error(), "already been acknowledged") {
				log.Printf("Retrying error response as edit: %v", err)
				_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &message,
				})
				if err != nil {
					log.Printf("Failed to send error message: %v", err)
				}
			} else {
				log.Printf("Failed to send error message: %v", err)
			}
		}
	}
}
func HandleValidateBranchName(branch string) error {
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

	if len(branch) == 0 || len(branch) > 255 {
		return fmt.Errorf("branch name must be between 1 and 255 characters")
	}

	if branch[0] == '.' {
		return fmt.Errorf("invalid branch name: must not start with a dot")
	}

	if !validPattern.MatchString(branch) {
		return fmt.Errorf("invalid branch name: must start with alphanumeric and contain only alphanumeric, dots, hyphens, or underscores")
	}

	return nil
}
