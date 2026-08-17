package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// --- #178: purge recreation must not leave an orphan duplicate role ---

// When the channel-overwrite re-application step fails after the new role has
// already been created, the function must delete the new role so the guild is
// not left with a duplicate (new orphan alongside the still-present old role).
func TestRecreateRole_OverwriteFailureCleansUpNewRole(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("old-int", wardenRoleBaseNameDefault+" Internal")},
		channels: []*discordgo.Channel{
			{
				ID: "chan-1",
				PermissionOverwrites: []*discordgo.PermissionOverwrite{
					{ID: "old-int", Type: discordgo.PermissionOverwriteTypeRole, Allow: 1, Deny: 0},
				},
			},
		},
		// The overwrite re-application fails after the new role already exists.
		ChannelPermSetErrs: []error{restError(http.StatusInternalServerError, 0, "boom")},
	}

	_, _, err := recreateRoleWithChannelOverwrites(gm, "guild-1", "old-int", gm.channels)
	if err == nil {
		t.Fatal("expected an error when the overwrite step fails")
	}

	// New role was created, then deleted as cleanup. The OLD role must NOT be
	// deleted (its recreation failed), so exactly one delete -> the new role.
	if gm.countCalls("GuildRoleCreate") != 1 {
		t.Fatalf("expected the new role to be created, got %v", gm.Calls())
	}
	if gm.countCalls("GuildRoleDelete") != 1 {
		t.Fatalf("expected exactly 1 delete (the orphan new role), got %d (%v)", gm.countCalls("GuildRoleDelete"), gm.Calls())
	}
	// The delete target must be EXACTLY the newly created role id (the fake's
	// first create returns "new-role-1"), never the old role.
	if got := gm.lastDeletedRoleID(); got != "new-role-1" {
		t.Fatalf("cleanup must delete the new role %q, deleted %q", "new-role-1", got)
	}
}

// The redundant post-create edit has been dropped: the create already sets every
// field. Pin the mutation sequence to exactly create-then-delete-old, so a
// re-edit step (the create->edit failure window #178 closed) cannot reappear.
func TestRecreateRole_DoesNotReEditAfterCreate(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("old-int", wardenRoleBaseNameDefault+" Internal")},
	}

	_, _, err := recreateRoleWithChannelOverwrites(gm, "guild-1", "old-int", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With no overwrites to re-apply, the only mutations are: create the new role,
	// then delete the old one. Any GuildRoleEdit between them would be the dropped
	// redundant re-apply.
	wantMutations := []string{"GuildRoleCreate", "GuildRoleDelete"}
	var gotMutations []string
	for _, c := range gm.Calls() {
		if c == "GuildRoleCreate" || c == "GuildRoleEdit" || c == "GuildRoleDelete" {
			gotMutations = append(gotMutations, c)
		}
	}
	if strings.Join(gotMutations, ",") != strings.Join(wantMutations, ",") {
		t.Fatalf("expected mutation sequence %v (no redundant edit), got %v", wantMutations, gm.Calls())
	}
}

// The purge summary must report a failed recreate clearly and must NEVER include
// the raw Discord response body. A 5xx recreate failure is routed through the
// classifier, so the summary carries a sanitized phrase, not "HTTP 500, {json}".
func TestRunWardenPurge_RecreateFailureSummaryHasNoRawBody(t *testing.T) {
	noOverwriteDelay(t)
	captureCount, _ := installCountingCapture(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("old-int", wardenRoleBaseNameDefault+" Internal")},
		// Create fails with a 5xx carrying a raw body that must not leak.
		RoleCreateErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1")

	runWardenPurge(f, gm, i, "guild-1", "internal")

	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Failed to recreate 'Verified Warden Internal'") {
		t.Fatalf("expected a clear failed-recreate line, got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) || strings.Contains(got, "HTTP 500") {
		t.Fatalf("summary must not contain the raw Discord body, got %q", got)
	}
	// A 5xx is a genuine system fault: it must be captured exactly once.
	if *captureCount != 1 {
		t.Fatalf("expected the system fault to be captured once, got %d", *captureCount)
	}
}

// A 4xx recreate failure (client/config fault, e.g. missing permissions) is
// reported clearly but must NOT page Sentry and must not leak the body.
func TestRunWardenPurge_RecreateClientFaultNotCaptured(t *testing.T) {
	noOverwriteDelay(t)
	captureCount, _ := installCountingCapture(t)
	gm := &fakeGuildManager{
		roles:          []*discordgo.Role{wardenRole("old-int", wardenRoleBaseNameDefault+" Internal")},
		RoleCreateErrs: []error{restError(http.StatusForbidden, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1")

	runWardenPurge(f, gm, i, "guild-1", "internal")

	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Failed to recreate 'Verified Warden Internal'") {
		t.Fatalf("expected a clear failed-recreate line, got %q", got)
	}
	// A 403 must carry the permission-specific hint, not just the generic prefix.
	if !strings.Contains(got, "missing permissions") || !strings.Contains(got, "Manage Roles") {
		t.Fatalf("expected the permission-specific hint for a 403, got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) || strings.Contains(got, "HTTP 403") {
		t.Fatalf("summary must not contain the raw Discord body, got %q", got)
	}
	if *captureCount != 0 {
		t.Fatalf("a 4xx client fault must not be captured to Sentry, got %d", *captureCount)
	}
}

// When create + overwrites succeed but deleting the OLD role fails, the new role
// was created (a duplicate now exists). The summary must say the recreate
// happened and the old role lingers / needs manual cleanup — NOT "the role was
// not recreated", which would be the inverse of the truth.
func TestRunWardenPurge_OldRoleDeleteFailureReportsLingeringRole(t *testing.T) {
	noOverwriteDelay(t)
	captureCount, _ := installCountingCapture(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("old-int", wardenRoleBaseNameDefault+" Internal")},
		// No channels -> no overwrites to re-apply; create succeeds, then the
		// old-role delete fails with a 5xx carrying a raw body.
		RoleDeleteErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1")

	runWardenPurge(f, gm, i, "guild-1", "internal")

	got := lastEditContent(f.Calls())
	// Must report the recreate as having happened, with a lingering old role.
	if strings.Contains(got, "the role was not recreated") || strings.Contains(got, "The role was not recreated") {
		t.Fatalf("must NOT claim the role was not recreated (it was); got %q", got)
	}
	if !strings.Contains(got, "Recreated 'Verified Warden Internal'") {
		t.Fatalf("expected the recreate to be reported as done, got %q", got)
	}
	if !strings.Contains(got, "could not be deleted") || !strings.Contains(got, "manually") {
		t.Fatalf("expected a lingering-old-role / manual-cleanup notice, got %q", got)
	}
	// The leftover old role id must be named so the operator can find it.
	if !strings.Contains(got, "old-int") {
		t.Fatalf("expected the lingering old role id in the summary, got %q", got)
	}
	if strings.Contains(got, rawBodyMarker) || strings.Contains(got, "HTTP 500") {
		t.Fatalf("summary must not contain the raw Discord body, got %q", got)
	}
	// A 5xx delete failure is a genuine system fault: capture it once.
	if *captureCount != 1 {
		t.Fatalf("expected the system fault to be captured once, got %d", *captureCount)
	}
}

// Edge case: the overwrite step fails (forcing the orphan-cleanup path) AND the
// cleanup delete of the new role also fails. The function must still return the
// ORIGINAL "reapply overwrites" error (the cleanup-delete error is logged, not
// returned, so it can't mask the real cause) and must return normally without a
// panic.
func TestRecreateRole_CleanupDeleteAlsoFailsReturnsOriginalError(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("old-int", wardenRoleBaseNameDefault+" Internal")},
		channels: []*discordgo.Channel{
			{
				ID: "chan-1",
				PermissionOverwrites: []*discordgo.PermissionOverwrite{
					{ID: "old-int", Type: discordgo.PermissionOverwriteTypeRole, Allow: 1, Deny: 0},
				},
			},
		},
		// Overwrite re-apply fails -> triggers orphan cleanup.
		ChannelPermSetErrs: []error{restError(http.StatusInternalServerError, 0, "overwrite-boom")},
		// The cleanup delete of the NEW role then also fails.
		RoleDeleteErrs: []error{restError(http.StatusInternalServerError, 0, "delete-boom")},
	}

	newRoleID, _, err := recreateRoleWithChannelOverwrites(gm, "guild-1", "old-int", gm.channels)
	if err == nil {
		t.Fatal("expected an error when the overwrite step fails")
	}
	// The returned error must be the original overwrite failure, not the
	// secondary cleanup-delete failure.
	if !strings.Contains(err.Error(), "reapply overwrites") {
		t.Fatalf("expected the original 'reapply overwrites' error to surface, got %v", err)
	}
	if strings.Contains(err.Error(), "delete-boom") {
		t.Fatalf("the secondary cleanup-delete error must not mask the original cause, got %v", err)
	}
	// On this path nothing usable was left -> newRoleID is empty, so runWardenPurge
	// reports "not recreated" rather than a lingering-role notice.
	if newRoleID != "" {
		t.Fatalf("overwrite-failure path must report no usable new role, got newRoleID %q", newRoleID)
	}
	// The cleanup was attempted exactly once (and failed).
	if gm.countCalls("GuildRoleDelete") != 1 {
		t.Fatalf("expected exactly one (failed) cleanup delete, got %d (%v)", gm.countCalls("GuildRoleDelete"), gm.Calls())
	}
}
