package replies

import (
	"github.com/7cav/cavbot2/state"
	"github.com/7cav/cavbot2/utils"
)

func repliesWithAnotherPackagesRecordedError() {
	utils.HandleError(nil, nil, "❌ The last sweep failed: "+state.LastSweepFailure) // want "."
}

func repliesWithAnotherPackagesFixedMessage() {
	utils.HandleError(nil, nil, state.BusyMessage)
}

func storesTheErrorInAnotherPackagesVariable(err error) {
	state.LastRefusal = "❌ Refused: " + err.Error() // want "variable"
}

func passesTheErrorToAnotherPackagesSetter(err error) {
	state.RememberRefusal(err.Error()) // want "variable"
}
