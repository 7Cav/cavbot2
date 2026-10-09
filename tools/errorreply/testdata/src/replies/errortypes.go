package replies

import (
	"fmt"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// lookupFailed is a lookup's error whose text is the member's reply.
type lookupFailed struct {
	reply string
}

func (e *lookupFailed) Error() string { return e.reply }

func lookUp(name string) (string, error) {
	if name == "" {
		return "", &lookupFailed{reply: "❌ Empty query"}
	}
	return "", &lookupFailed{reply: fmt.Sprintf("❌ No member found matching %q", name)}
}

func repliesWithWhatTheLookupWrote(r utils.InteractionResponder, i *discordgo.InteractionCreate, name string) {
	if _, err := lookUp(name); err != nil {
		utils.HandleError(r, i, err.Error())
	}
}

// wrappedFailure is an error that keeps another error's text.
type wrappedFailure struct {
	reply string
}

func (e *wrappedFailure) Error() string { return e.reply }

func wrap(err error) error {
	return &wrappedFailure{reply: "❌ Failed: " + err.Error()} // want "."
}

func repliesWithTheWrappedFailure(r utils.InteractionResponder, i *discordgo.InteractionCreate, err error) {
	utils.HandleError(r, i, wrap(err).Error())
}
