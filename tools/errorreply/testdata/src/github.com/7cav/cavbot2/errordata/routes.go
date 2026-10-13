package errordata

import (
	"strings"

	"github.com/7cav/cavbot2/notifylog"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Data that leaves an error and then goes somewhere other than the log,
// Sentry or a comparison is reported where it leaves, whatever route it
// takes after.

// #547
func CutPrefix(err error) {
	msg, _ := strings.CutPrefix(err.Error(), "HTTP ") // want ""
	utils.HandleError(nil, nil, "❌ Failed: "+msg)
}

// relay is declared before store, so a single pass over the package's
// functions would take relay's parameter for one that reaches only the log.
func relay(v string) { store(v) }

var lastReason = map[string]string{}

func store(v string) { lastReason["k"] = v }

func Relayed(err error) {
	relay(err.Error()) // want ""
	utils.HandleError(nil, nil, "❌ Failed: "+lastReason["k"])
}

// #560
func ClosureAssign(err error) {
	var reason string
	func() { reason = err.Error() }() // want ""
	utils.HandleError(nil, nil, "❌ Failed: "+reason)
}

// A log wrapper that also posts what it is handed to Discord.
func PostedLog(s *discordgo.Session, err error) {
	notifylog.Warn(s, "lookup failed", "error", err) // want ""
}
