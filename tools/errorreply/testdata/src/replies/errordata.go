package replies

import (
	"errors"
	"fmt"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

func namesTheWaitFromARateLimit(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	var limited *discordgo.RateLimitError
	if errors.As(err, &limited) {
		utils.HandleError(r, i, fmt.Sprintf("❌ Discord is busy. Try again in %s.", limited.RetryAfter))
	}
}

func namesTheStatusCode(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) {
		utils.HandleError(r, i, fmt.Sprintf("❌ Discord answered %d.", rest.Response.StatusCode)) // want "."
	}
}

const discordRefused = "❌ Discord refused that. Check the bot's permissions."

func picksAFixedMessageWithErrorsAs(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	reply := fetchFailed
	var rest *discordgo.RESTError
	if errors.As(err, &rest) {
		reply = discordRefused
	}
	utils.HandleError(r, i, reply)
}
