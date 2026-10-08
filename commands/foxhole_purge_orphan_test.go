package commands

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// --- #178: purge recreation must not leave an orphan duplicate role ---

// When copying an overwrite to the new role fails, the purge deletes the new
// role, so the guild isn't left with a duplicate beside the old role it
// still has, and the reply says the role failed.
func TestRunFoxholePurge_OverwriteFailureDeletesTheNewRole(t *testing.T) {
	noOverwriteDelay(t)
	gm := oneOverwriteGuild()
	gm.ChannelPermSetErrs = []error{restError(http.StatusInternalServerError, 0, "boom")}
	f := &fakeResponder{}

	runFoxholePurge(f, gm, foxholeInteraction("guild-1"), "guild-1", "internal")

	// The fake's first create returns new-role-1. The old role stays.
	if got := gm.deletedRoles(); !slices.Equal(got, []string{"new-role-1"}) {
		t.Fatalf("the purge deleted roles %v, want only the new role new-role-1", got)
	}
	assertVerdict(t, lastEditContent(f.Calls()), defaultInternalRoleName, verdictFailed)
}

// With no overwrites to copy, a purge's only changes are creating the new
// role, then deleting the old one: the create sets every field, so no edit
// follows it to fail after the new role exists (#178).
func TestRunFoxholePurge_CreatesThenDeletesWithNoOtherRoleChange(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("old-int", foxholeRoleBaseNameDefault+" Internal")},
	}

	runFoxholePurge(&fakeResponder{}, gm, foxholeInteraction("guild-1"), "guild-1", "internal")

	var methods []string
	for _, write := range gm.guildWrites() {
		methods = append(methods, write.method)
	}
	if want := []string{"GuildRoleCreate", "GuildRoleDelete"}; !slices.Equal(methods, want) {
		t.Fatalf("the purge changed the guild with %v, want %v", methods, want)
	}
}

// When Discord refuses the new role, the purge's line for that role says it
// failed, with the advice for that kind of failure and never Discord's raw
// body. A missing permission also names Manage Roles. Only a system fault
// pages Sentry.
func TestRunFoxholePurge_RecreateFailureAdvisesOnEachKindOfDiscordFailure(t *testing.T) {
	for _, failure := range []discordFailure{serverError, unknownGuild, forbidden, badRequest} {
		t.Run(failure.name, func(t *testing.T) {
			noOverwriteDelay(t)
			rec := &captureRecorder{}
			rec.install(t)
			gm := &fakeGuildManager{
				roles:          []*discordgo.Role{guildRole("old-int", foxholeRoleBaseNameDefault+" Internal")},
				RoleCreateErrs: []error{failure.err},
			}
			f := &fakeResponder{}

			runFoxholePurge(f, gm, foxholeInteraction("guild-1"), "guild-1", "internal")

			reply := lastEditContent(f.Calls())
			assertReplyNames(t, reply, defaultInternalRoleName)
			assertFailureReply(t, reply, failure, rec)
			if failure.advice == adviceMissingPermissions {
				assertReplyNames(t, reply, "Manage Roles")
			}
		})
	}
}

// When create and overwrites succeed but deleting the OLD role fails, the
// new role exists beside the old one. The reply says the role was recreated
// with a problem left over, and names the old role's ID so the manager can
// delete it. A server error pages Sentry once.
func TestRunFoxholePurge_OldRoleDeleteFailureReportsLingeringRole(t *testing.T) {
	noOverwriteDelay(t)
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("old-int", foxholeRoleBaseNameDefault+" Internal")},
		// No channels -> no overwrites to re-apply; create succeeds, then the
		// old-role delete fails with a 5xx carrying a raw body.
		RoleDeleteErrs: []error{serverError.err},
	}
	f := &fakeResponder{}

	runFoxholePurge(f, gm, foxholeInteraction("guild-1"), "guild-1", "internal")

	reply := lastEditContent(f.Calls())
	assertVerdict(t, reply, defaultInternalRoleName, verdictLeftOver)
	assertReplyNames(t, reply, "old-int")
	assertNoLeak(t, reply, serverError.err)
	if rec.count != 1 {
		t.Fatalf("expected the system fault to be captured once, got %d", rec.count)
	}
}

// When copying an overwrite fails and deleting the new role fails too, the
// failure Sentry gets is the overwrite's, the cause, not the cleanup's. The
// reply says the role failed, not that it was recreated with a leftover.
func TestRunFoxholePurge_OverwriteFailureOutranksAFailedCleanup(t *testing.T) {
	noOverwriteDelay(t)
	rec := &captureRecorder{}
	rec.install(t)
	overwriteErr := restError(http.StatusInternalServerError, 0, "overwrite-boom")
	cleanupErr := restError(http.StatusInternalServerError, 0, "delete-boom")
	gm := oneOverwriteGuild()
	gm.ChannelPermSetErrs = []error{overwriteErr}
	gm.RoleDeleteErrs = []error{cleanupErr}
	f := &fakeResponder{}

	runFoxholePurge(f, gm, foxholeInteraction("guild-1"), "guild-1", "internal")

	if rec.count != 1 {
		t.Fatalf("Sentry got %d events, want 1", rec.count)
	}
	if !errors.Is(rec.errs[0], overwriteErr) || errors.Is(rec.errs[0], cleanupErr) {
		t.Errorf("Sentry got %v, want the overwrite failure and not the cleanup's", rec.errs[0])
	}
	if got := gm.deletedRoles(); !slices.Equal(got, []string{"new-role-1"}) {
		t.Errorf("the purge tried to delete roles %v, want only the new role new-role-1", got)
	}
	assertVerdict(t, lastEditContent(f.Calls()), defaultInternalRoleName, verdictFailed)
}
