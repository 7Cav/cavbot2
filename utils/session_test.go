package utils

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// newFakeSession returns a session for the scripted gateway.
func newFakeSession(t *testing.T) *discordgo.Session {
	t.Helper()

	dg, err := discordgo.New("Bot fake-token-for-test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	// Teardown hygiene only: without this, discordgo's listen goroutine spins
	// in reconnect() against the torn-down fake gateway for the rest of the run.
	dg.ShouldReconnectOnError = false
	t.Cleanup(func() { _ = dg.Close() })

	return dg
}

func TestOpenSessionReadySessionIsUsable(t *testing.T) {
	newScriptedGateway(t, func(int) gatewayLogin { return gatewayLogin{answer: "READY"} })
	dg := newFakeSession(t)

	if err := OpenSession(dg); err != nil {
		t.Fatalf("OpenSession returned %v, want nil", err)
	}
	if dg.State.User == nil {
		t.Fatal("State.User is nil after OpenSession reported success")
	}
}

// TestOpenSessionRejectsSessionThatNeverBecameReady pins the invariant the
// whole bug turns on: a nil error must mean the session is usable. discordgo
// treats a non-READY second frame as non-fatal (wsapi.go:178-182) and Open()
// returns nil, leaving State.User unpopulated for the caller to walk into.
func TestOpenSessionRejectsSessionThatNeverBecameReady(t *testing.T) {
	newScriptedGateway(t, func(int) gatewayLogin { return gatewayLogin{invalid: true} })
	dg := newFakeSession(t)

	if err := OpenSession(dg); err == nil {
		t.Fatal("OpenSession reported success for a session that never became ready; the caller would panic dereferencing State.User")
	}
}
