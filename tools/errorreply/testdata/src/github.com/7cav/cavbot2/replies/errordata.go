package replies

import (
	"errors"
	"fmt"
	"time"

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

func refusal(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	return "❌ Refused: " + err.Error(), true
}

func repliesWithAHelpersRefusal(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	if reply, ok := refusal(err); ok {
		utils.HandleError(r, i, reply) // want "."
	}
}

func quotesTheResponseBody(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) {
		utils.HandleError(r, i, "❌ Discord said: "+string(rest.ResponseBody)) // want "."
	}
}

// verdict is what a classifier tells the reply about an error.
type verdict struct {
	phrase string
}

func judge(err error) verdict {
	return verdict{phrase: err.Error()}
}

func repliesWithAFieldTheHelperFilledFromTheError(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	utils.HandleError(r, i, "❌ Failed: "+judge(err).phrase) // want "."
}

func classifyFault(err error) verdict {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == 403 {
		return verdict{phrase: "missing permissions"}
	}
	return verdict{phrase: "could not reach Discord"}
}

func repliesWithAClassifiersPhrase(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	utils.HandleError(r, i, fmt.Sprintf("❌ Failed: %s.", classifyFault(err).phrase))
}

// attendee is a player the reply names, and whether their lookup failed.
type attendee struct {
	name   string
	failed bool
}

func attend(name string, err error) attendee {
	return attendee{name: name, failed: err != nil}
}

func namesAPlayerBesideAFailureFlag(r utils.InteractionResponder, i *discordgo.InteractionCreate, name string, err error) {
	utils.HandleError(r, i, "⚠️ Couldn't match "+attend(name, err).name)
}

func namesAWaitParsedFromTheErrorsText(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	if wait, perr := time.ParseDuration(err.Error()); perr == nil {
		utils.HandleError(r, i, fmt.Sprintf("❌ Discord is busy. Try again in %s.", wait))
	}
}
