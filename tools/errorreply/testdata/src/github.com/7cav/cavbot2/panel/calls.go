package panel

import (
	"net/http"

	"github.com/7cav/cavbot2/commands"
)

func answersWithAPhraseChosenFromTheError(w http.ResponseWriter, err error) {
	http.Error(w, "Discord did not rename the channel: "+commands.DiscordErrorDetail(err)+".", http.StatusUnprocessableEntity)
}

func answersWithTheWaitReadOffTheError(w http.ResponseWriter, err error) {
	http.Error(w, "Discord is busy. "+commands.RateLimitWait(err)+".", http.StatusTooManyRequests)
}

func answersWithAnErrorsTextFromAnotherPackage(w http.ResponseWriter) {
	http.Error(w, "The last sweep failed: "+commands.LastSweepFailure(), http.StatusInternalServerError) // want "panel"
}

func answersWithAnotherPackagesSentenceAroundTheError(w http.ResponseWriter, err error) {
	http.Error(w, commands.Describe(err.Error()), http.StatusInternalServerError) // want "panel"
}
