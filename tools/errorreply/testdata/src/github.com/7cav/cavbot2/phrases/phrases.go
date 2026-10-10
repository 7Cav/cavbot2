// Package phrases stands in for a package of the module whose helpers hand
// back the text they're given, so a reply's text comes back out of a call
// into another package.
package phrases

// Quote is message, read back through a closure.
func Quote(message string) string {
	return func() string { return message }()
}
