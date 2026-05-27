package commands

import (
	"strings"
	"testing"
)

func TestEmptyRosterSearchMessage(t *testing.T) {
	got := emptyRosterSearchMessage("Q/Z/9-9")

	if !strings.Contains(got, `"Q/Z/9-9"`) {
		t.Errorf("message %q missing the quoted position", got)
	}
	// CONTEXT.md *Position* examples: 2/B/1-7, Reservist, S1. The acceptance
	// criterion requires these be sourced from one place; this test pins them
	// in the user-facing string so a future edit can't quietly drop one.
	for _, example := range []string{"2/B/1-7", "Reservist", "S1"} {
		if !strings.Contains(got, example) {
			t.Errorf("message %q missing example %q", got, example)
		}
	}
	// The criterion requires acknowledging *both* causes — no-match and
	// unrecognized input — rather than asserting the input was wrong.
	if strings.Contains(got, "Please check your search for accuracy") {
		t.Errorf("message %q still asserts input was wrong", got)
	}
	if !strings.HasPrefix(got, "❌") {
		t.Errorf("message %q missing the ❌ prefix used by other handle-error responses", got)
	}
}

