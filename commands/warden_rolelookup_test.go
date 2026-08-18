package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// --- findGuildRoleIDByName: explicit not-found contract (#180) ---

// A matching role returns its id, no error, and the not-found sentinel is NOT
// reported. This is the "found" branch.
func TestFindGuildRoleIDByName_Found(t *testing.T) {
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("role-abc", "Verified Warden Internal")},
	}

	id, err := findGuildRoleIDByName(gm, "guild-1", "Verified Warden Internal")
	if err != nil {
		t.Fatalf("found role should not error, got %v", err)
	}
	if id != "role-abc" {
		t.Fatalf("expected id %q, got %q", "role-abc", id)
	}
}

// No matching role returns the explicit errRoleNotFound sentinel — NOT a bare
// empty-string-with-nil-error. This is the contract change #180 is about: a
// missing role must be a distinct, checkable signal a caller cannot misread as
// success.
func TestFindGuildRoleIDByName_NotFoundIsExplicit(t *testing.T) {
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("other", "Some Other Role")},
	}

	id, err := findGuildRoleIDByName(gm, "guild-1", "Verified Warden Internal")
	if !errors.Is(err, errRoleNotFound) {
		t.Fatalf("not-found must return errRoleNotFound sentinel, got err=%v id=%q", err, id)
	}
}

// A genuine GuildRoles API failure returns a real error that is distinguishable
// from not-found: it must NOT satisfy errors.Is(err, errRoleNotFound), so a
// caller cannot mistake an upstream fault for "role absent". The underlying
// error is wrapped/passed through unchanged for capture.
func TestFindGuildRoleIDByName_APIErrorIsNotNotFound(t *testing.T) {
	apiErr := errors.New("boom: guild roles unavailable")
	gm := &fakeGuildManager{RolesErrs: []error{apiErr}}

	_, err := findGuildRoleIDByName(gm, "guild-1", "Verified Warden Internal")
	if err == nil {
		t.Fatal("API failure must return a non-nil error")
	}
	if errors.Is(err, errRoleNotFound) {
		t.Fatal("API failure must NOT be classified as errRoleNotFound")
	}
	if !errors.Is(err, apiErr) {
		t.Fatalf("API failure must surface the underlying error, got %v", err)
	}
}

// --- resolveWardenRoleIDs: each user-facing outcome unchanged (#180) ---

// Found: resolveWardenRoleIDs returns the resolved ids and names, no error.
func TestResolveWardenRoleIDs_Found(t *testing.T) {
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseNameDefault+" Internal")},
	}

	ids, names, err := resolveWardenRoleIDs(gm, "guild-1", "internal")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(ids) != 1 || ids[0] != "r-int" {
		t.Fatalf("expected [r-int], got %v", ids)
	}
	if len(names) != 1 || names[0] != wardenRoleBaseNameDefault+" Internal" {
		t.Fatalf("unexpected names %v", names)
	}
}

// Not-found: resolveWardenRoleIDs surfaces the same distinct "role not found in
// guild" message as before, naming the missing role.
func TestResolveWardenRoleIDs_NotFoundMessageUnchanged(t *testing.T) {
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("other", "Some Other Role")},
	}

	_, _, err := resolveWardenRoleIDs(gm, "guild-1", "internal")
	if err == nil {
		t.Fatal("expected role-not-found error")
	}
	got := err.Error()
	if !strings.Contains(got, "role not found in guild") {
		t.Fatalf("expected role-not-found message, got %q", got)
	}
	if !strings.Contains(got, wardenRoleBaseNameDefault+" Internal") {
		t.Fatalf("not-found message must name the missing role, got %q", got)
	}
}

// API error: a genuine GuildRoles failure surfaces the distinct "Failed to
// retrieve guild roles" message — it is NOT silently treated as not-found.
func TestResolveWardenRoleIDs_APIErrorMessageUnchanged(t *testing.T) {
	gm := &fakeGuildManager{RolesErrs: []error{errors.New("boom")}}

	_, _, err := resolveWardenRoleIDs(gm, "guild-1", "internal")
	if err == nil {
		t.Fatal("expected guild-roles API error")
	}
	got := err.Error()
	if !strings.Contains(got, "Failed to retrieve guild roles") {
		t.Fatalf("expected API-failure message, got %q", got)
	}
	if strings.Contains(got, "role not found") {
		t.Fatalf("API failure must not be reported as not-found, got %q", got)
	}
}
