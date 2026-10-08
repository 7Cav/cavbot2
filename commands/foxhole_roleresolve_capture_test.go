package commands

import (
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// --- role resolution: GuildRoles fault capture split (#180, #194) ---

// roleLookupGM is a guild with one member reachable by ID whose role lookup
// Discord fails with err.
func roleLookupGM(err error) *fakeGuildManager {
	gm := foxholeRoleAddGM(nil)
	gm.RolesErrs = []error{err}
	return gm
}

// When Discord fails the lookup of the guild's roles, /foxhole add changes
// nothing and gives the advice for that kind of failure, never reporting the
// role as missing. A system fault pages Sentry once, naming the command, the
// guild and the role it looked for (#194); a client fault pages nothing.
func TestRunFoxholeAdd_RoleLookupFailureAdvisesAndChangesNothing(t *testing.T) {
	for _, failure := range []discordFailure{serverError, transport, unknownGuild, badRequest} {
		t.Run(failure.name, func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			gm := roleLookupGM(failure.err)
			f := &fakeResponder{}

			runFoxhole(f, gm, nil, foxholeAddInteraction())

			if n := gm.countCalls("GuildMemberRoleAdd"); n != 0 {
				t.Fatalf("the add changed %d roles without knowing the role", n)
			}
			reply := lastEditContent(f.Calls())
			if got := verdicts(reply, ""); !slices.Equal(got, []string{verdictFailed}) {
				t.Errorf("the reply's verdicts are %q, want one %q", got, verdictFailed)
			}
			assertFailureReply(t, reply, failure, rec)
			if !failure.captured {
				return
			}
			for key, want := range map[string]string{
				"command": "foxhole",
				"guild":   "guild-1",
				"role":    defaultInternalRoleName,
			} {
				if got, ok := kvValue(rec.lastKV, key); !ok || got != want {
					t.Errorf("capture context %q = %v, want %q; kv %v", key, got, want, rec.lastKV)
				}
			}
		})
	}
}

// A guild that has no role by the Foxhole role's name gets a reply naming the
// role it couldn't find. That is no fault, so Sentry gets nothing, and no
// role changes.
func TestRunFoxholeAdd_RoleNotInTheGuildIsNamed(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := foxholeRoleAddGM(nil)
	gm.roles = []*discordgo.Role{guildRole("other", "Some Other Role")}
	f := &fakeResponder{}

	runFoxhole(f, gm, nil, foxholeAddInteraction())

	assertVerdict(t, lastEditContent(f.Calls()), defaultInternalRoleName, verdictFailed)
	if rec.count != 0 {
		t.Errorf("a role missing from the guild captured %d events, want none", rec.count)
	}
	if n := gm.countCalls("GuildMemberRoleAdd"); n != 0 {
		t.Errorf("the add changed %d roles with no role to give", n)
	}
}
