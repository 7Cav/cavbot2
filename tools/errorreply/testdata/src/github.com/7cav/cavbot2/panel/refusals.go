package panel

import (
	"errors"
	"net/http"

	"github.com/7cav/cavbot2/commands"
)

func namesTheMissingRole(w http.ResponseWriter, role string) {
	var missing *commands.MissingRoleError
	if errors.As(commands.StartFoxholeAction(role, false), &missing) {
		http.Error(w, "The "+missing.Role+" role isn't in the server.", http.StatusUnprocessableEntity)
	}
}

func quotesASweepErrorsReason(w http.ResponseWriter, err error) {
	var failed *commands.SweepError
	if errors.As(commands.Sweep(err), &failed) {
		http.Error(w, "Couldn't sweep: "+failed.Reason, http.StatusBadGateway) // want "panel"
	}
}

func answersWithTheLastSweepsError(w http.ResponseWriter) {
	http.Error(w, commands.LastSweep().Error(), http.StatusBadGateway) // want "panel"
}

func answersWithANoticeBuiltFromTheError(w http.ResponseWriter, err error) {
	var notice *commands.NoticeError
	if errors.As(commands.Notice("Refused: "+err.Error()), &notice) { // want "panel"
		http.Error(w, notice.Message, http.StatusUnprocessableEntity)
	}
}
