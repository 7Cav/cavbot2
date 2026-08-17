package commands

import (
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// These tests drive the configured base name to the role IDs the commands
// mutate. A name that composes fine but matches no role in the guild is the
// failure that matters, and asserting on composed names would miss it.

// TestWardenRoleIDsResolveUnderConfiguredBaseName covers the single-role scopes.
func TestWardenRoleIDsResolveUnderConfiguredBaseName(t *testing.T) {
	t.Setenv(wardenRoleBaseNameEnv, "Verified Foxhole")

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("role-foxhole-int", "Verified Foxhole Internal")},
	}

	roleIDs, _, err := resolveWardenRoleIDs(gm, "guild-1", "internal")
	if err != nil {
		t.Fatalf("configured base name must resolve against the guild: %v", err)
	}

	if len(roleIDs) != 1 || roleIDs[0] != "role-foxhole-int" {
		t.Fatalf("expected the configured role's id, got %v", roleIDs)
	}
}

// TestWardenBothScopeResolvesUnderConfiguredBaseName covers the two-role scope.
// Membership, not order: nothing downstream depends on Internal preceding
// External.
func TestWardenBothScopeResolvesUnderConfiguredBaseName(t *testing.T) {
	t.Setenv(wardenRoleBaseNameEnv, "Verified Foxhole")

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{
			wardenRole("role-foxhole-int", "Verified Foxhole Internal"),
			wardenRole("role-foxhole-ext", "Verified Foxhole External"),
		},
	}

	roleIDs, _, err := resolveWardenRoleIDs(gm, "guild-1", "both")
	if err != nil {
		t.Fatalf("configured base name must resolve both scopes: %v", err)
	}

	resolved := map[string]bool{}
	for _, id := range roleIDs {
		resolved[id] = true
	}
	if len(roleIDs) != 2 || !resolved["role-foxhole-int"] || !resolved["role-foxhole-ext"] {
		t.Fatalf("expected both configured role ids, got %v", roleIDs)
	}
}

// TestBulkAddInternalAppliesConfiguredRole covers /warden-bulkadd-internal,
// which resolves its role independently of the four /warden subcommands. The
// observable is the role reaching the member, not the absence of an error.
func TestBulkAddInternalAppliesConfiguredRole(t *testing.T) {
	t.Setenv(wardenRoleBaseNameEnv, "Verified Foxhole")

	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Trooper.A", "111111111111111111"),
	), http.StatusOK, nil)

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("role-foxhole-int", "Verified Foxhole Internal")},
	}
	f := &fakeResponder{}

	runWardenBulkAddInternal(f, gm, wardenBulkAddInternalInteraction("D/ACD"))

	adds := gm.roleAddCalls()
	if len(adds) != 1 {
		t.Fatalf("expected the roster member to be added, got %d add(s)", len(adds))
	}
	if adds[0].roleID != "role-foxhole-int" {
		t.Fatalf("add must target the role resolved from the configured name, got %q", adds[0].roleID)
	}
}
