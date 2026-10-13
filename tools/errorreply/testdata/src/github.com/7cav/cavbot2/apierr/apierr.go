// Package apierr holds an error type whose text is another error's.
package apierr

// Wrapped keeps the error an API call failed with and says what it says.
type Wrapped struct {
	Cause error
}

func (w *Wrapped) Error() string { return "api call failed: " + w.Cause.Error() }
