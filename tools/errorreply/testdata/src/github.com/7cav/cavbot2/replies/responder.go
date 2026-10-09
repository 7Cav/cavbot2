package replies

import (
	"fmt"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

func respondsWithTheError(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	_ = r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ // want "."
		Data: &discordgo.InteractionResponseData{Content: fmt.Sprintf("❌ Failed: %v", err)},
	})
}

func editsInTheError(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	content := "❌ Failed: " + err.Error()
	_ = r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content}) // want "."
}

func followsUpWithTheError(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	_ = r.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: err.Error()}) // want "."
}

func editReply(r utils.InteractionResponder, i *discordgo.InteractionCreate, content string) {
	_ = r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
}

func editsThroughAWrapper(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	editReply(r, i, err.Error()) // want "."
}

func putsTheErrorInAnEmbedField(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	_ = r.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{ // want "."
		Data: &discordgo.InteractionResponseData{
			Content: "❌ Some members weren't added.",
			Embeds: []*discordgo.MessageEmbed{{
				Title:  "Failures",
				Fields: []*discordgo.MessageEmbedField{{Name: "Reason", Value: err.Error()}},
			}},
		},
	})
}

func editReplyLater(r utils.InteractionResponder, i *discordgo.InteractionCreate, content string) {
	send := func() {
		_ = r.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
	}
	send()
}

func editsThroughAClosure(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	editReplyLater(r, i, err.Error()) // want "."
}
