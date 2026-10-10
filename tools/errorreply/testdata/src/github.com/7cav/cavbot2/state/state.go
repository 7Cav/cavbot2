// Package state stands in for a package of the module that keeps what the
// bot last saw in package-level variables, which other packages read and
// set.
package state

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
