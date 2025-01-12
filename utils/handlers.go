package utils

import (
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log"
	"regexp"
	"strings"
)

func HandleError(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	log.Printf("HandleError called with message: %s", message)
	log.Printf("Interaction type: %v", i.Type)

	if i.Type == discordgo.InteractionApplicationCommand {
		log.Printf("Handling application command error")
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: message,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			log.Printf("Initial response failed with error: %v", err)
			if strings.Contains(err.Error(), "already been acknowledged") {
				log.Printf("Interaction already acknowledged, attempting edit")
				_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &message,
				})
				if err != nil {
					log.Printf("Edit attempt failed with error: %v", err)
				} else {
					log.Printf("Edit successful")
				}
			} else {
				log.Printf("Unexpected error occurred: %v", err)
			}
		} else {
			log.Printf("Initial response successful")
		}
	} else {
		log.Printf("Handling component interaction error")
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: message,
			},
		})
		if err != nil {
			log.Printf("Update response failed with error: %v", err)
			if strings.Contains(err.Error(), "already been acknowledged") {
				log.Printf("Attempting edit as fallback")
				_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &message,
				})
				if err != nil {
					log.Printf("Fallback edit failed with error: %v", err)
				} else {
					log.Printf("Fallback edit successful")
				}
			} else {
				log.Printf("Unexpected error occurred: %v", err)
			}
		} else {
			log.Printf("Update response successful")
		}
	}
	log.Printf("HandleError completed")
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
