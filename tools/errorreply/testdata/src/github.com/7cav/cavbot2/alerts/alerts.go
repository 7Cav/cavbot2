// Package alerts stands in for a package of the module that warns a member
// through another package's function, so a warning reaches Discord through a
// chain of calls into two other packages.
package alerts

import "github.com/7cav/cavbot2/notify"

// Warn warns the member of message.
func Warn(message string) {
	notify.Reply("⚠️ " + message)
}
