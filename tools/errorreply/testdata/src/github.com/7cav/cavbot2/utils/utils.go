// Package utils stands in for cavbot2's utils, which holds HandleError and
// InteractionResponder.
package utils

import "github.com/bwmarrin/discordgo"

type InteractionResponder interface {
	InteractionRespond(i *discordgo.Interaction, resp *discordgo.InteractionResponse) error
	InteractionResponseEdit(i *discordgo.Interaction, edit *discordgo.WebhookEdit) error
	FollowupMessageCreate(i *discordgo.Interaction, wait bool, params *discordgo.WebhookParams) error
}

type InteractionCreate = discordgo.InteractionCreate

// HandleError sends message to the member.
func HandleError(r InteractionResponder, i *discordgo.InteractionCreate, message string) {}
