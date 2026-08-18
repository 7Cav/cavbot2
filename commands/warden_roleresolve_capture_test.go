package commands

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// --- resolveWardenRoleIDs: GuildRoles fault capture split (#194) ---
//
// The GuildRoles lookup inside resolveWardenRoleIDs must route a genuine system
// fault (5xx/transport) through the shared classifier and capture it to Sentry
// (per ADR 0001), while a 4xx stays a non-captured actionable message and the
// explicit not-found result remains non-captured. The raw Discord body must
// never reach the operator-facing message.

// A 5xx from GuildRoles during role resolution is a genuine Discord-side fault:
// it must capture to Sentry exactly once, show a body-free generic retry
// message, and never leak the raw Discord response body.
func TestResolveWardenRoleIDs_GuildRoles5xxCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{RolesErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)}}

	_, _, err := resolveWardenRoleIDs(gm, "guild-1", "internal")
	if err == nil {
		t.Fatal("expected an error from a 5xx GuildRoles lookup")
	}
	if rec.count != 1 {
		t.Fatalf("a 5xx GuildRoles fault must capture to Sentry exactly once; got %d", rec.count)
	}
	if strings.Contains(err.Error(), rawBodyMarker) {
		t.Fatalf("role resolution leaked the raw Discord body: %q", err.Error())
	}
	// #194 requires command+guild context on the capture; pin those plus role.
	for key, want := range map[string]string{
		"command": "warden",
		"guild":   "guild-1",
		"role":    wardenRoleBaseNameDefault + " Internal",
	} {
		got, ok := kvValue(rec.lastKV, key)
		if !ok {
			t.Fatalf("capture context missing %q; got kv %v", key, rec.lastKV)
		}
		if got != want {
			t.Fatalf("capture context %q = %v, want %q", key, got, want)
		}
	}
}

// A transport (non-REST) failure from GuildRoles is also a system fault: capture
// once, no raw body leak.
func TestResolveWardenRoleIDs_GuildRolesTransportErrorCaptures(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{RolesErrs: []error{errors.New("dial tcp: connection refused")}}

	_, _, err := resolveWardenRoleIDs(gm, "guild-1", "internal")
	if err == nil {
		t.Fatal("expected an error from a transport GuildRoles failure")
	}
	if rec.count != 1 {
		t.Fatalf("a transport GuildRoles fault must capture to Sentry exactly once; got %d", rec.count)
	}
}

// A 4xx from GuildRoles is an operator/config-fixable client fault: it must
// surface an actionable, body-free message and must NOT capture to Sentry.
func TestResolveWardenRoleIDs_GuildRoles4xxDoesNotCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{RolesErrs: []error{restError(http.StatusBadRequest, 50035, rawBodyMarker)}}

	_, _, err := resolveWardenRoleIDs(gm, "guild-1", "internal")
	if err == nil {
		t.Fatal("expected an error from a 4xx GuildRoles lookup")
	}
	if rec.count != 0 {
		t.Fatalf("a 4xx GuildRoles fault must NOT capture to Sentry; got %d", rec.count)
	}
	if strings.Contains(err.Error(), rawBodyMarker) {
		t.Fatalf("role resolution leaked the raw Discord body: %q", err.Error())
	}
}

// The explicit not-found result (the role simply isn't in the guild) is not a
// fault and must NOT capture to Sentry; the not-found message is preserved.
func TestResolveWardenRoleIDs_NotFoundDoesNotCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("other", "Some Other Role")},
	}

	_, _, err := resolveWardenRoleIDs(gm, "guild-1", "internal")
	if err == nil {
		t.Fatal("expected a role-not-found error")
	}
	if rec.count != 0 {
		t.Fatalf("an explicit not-found result must NOT capture to Sentry; got %d", rec.count)
	}
	if !strings.Contains(err.Error(), "role not found in guild") {
		t.Fatalf("expected the not-found message, got %q", err.Error())
	}
}
