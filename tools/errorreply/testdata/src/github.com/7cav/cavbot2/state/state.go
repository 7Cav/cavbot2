// Package state stands in for a package of the module that keeps what the
// bot last saw in package-level variables, which other packages read and
// set.
package state

import (
	"log"
	"os"
	"strings"
)

// Logger is the log the bot writes to.
var Logger = log.New(os.Stderr, "", 0)

// LastSweepFailure is the text of the last error the sweep met.
var LastSweepFailure string

// RecordSweep keeps the error the sweep met.
func RecordSweep(err error) {
	LastSweepFailure = err.Error()
}

// BusyMessage is the reply for a member who runs a command while Discord
// is busy.
var BusyMessage = "❌ Discord is busy. Try again in a few minutes."

// LastRefusal is the reason of the last refusal a command gave.
var LastRefusal string

// RememberRefusal keeps reason as the last refusal's.
func RememberRefusal(reason string) {
	LastRefusal = reason
}

// Report is what the last sweep wrote down, the errors it met included.
var Report strings.Builder

// WriteReport writes down an error the sweep met.
func WriteReport(err error) {
	Report.WriteString(err.Error())
}
