package replies

import (
	"github.com/7cav/cavbot2/alerts"
	"github.com/7cav/cavbot2/notify"
	"github.com/7cav/cavbot2/state"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
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

func readsAnotherPackagesReport() {
	_ = state.Report.String()
}

func logsTheErrorThroughAnotherPackagesLogger(err error) {
	state.Logger.Print("sweep failed: " + err.Error())
}

func repliesWithTheErrorThroughAnotherPackagesWrapper(err error) {
	notify.Reply("❌ Failed: " + err.Error()) // want "Discord"
}

func repliesWithAFixedMessageThroughAnotherPackagesWrapper() {
	notify.Reply(state.BusyMessage)
}

func postsTheErrorThroughAnotherPackagesWrapper(s *discordgo.Session, err error) {
	notify.Post(s, "❌ Failed: "+err.Error()) // want "Discord"
}

func postsAFixedMessageThroughAnotherPackagesWrapper(s *discordgo.Session) {
	notify.Post(s, state.BusyMessage)
}

func warnsWithTheErrorThroughAChainOfWrappers(err error) {
	alerts.Warn(err.Error()) // want "Discord"
}

func repliesWithTheErrorThroughAnotherPackagesMethod(n notify.Notifier, err error) {
	n.Reply("❌ Failed: " + err.Error()) // want "Discord"
}
