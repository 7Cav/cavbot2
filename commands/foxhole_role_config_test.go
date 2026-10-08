package commands

import (
	"net/http"
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// These tests drive the configured base name to the role IDs the commands
// mutate. A name that composes fine but matches no role in the guild is the
// failure that matters, and asserting on composed names would miss it.

// configuredRoleGuild holds the Foxhole roles named from the base name
// "Configured Base", and one member reachable by ID.
func configuredRoleGuild() *fakeGuildManager {
	return &fakeGuildManager{
		roles: []*discordgo.Role{
			guildRole("role-default-int", defaultInternalRoleName),
			guildRole("role-configured-int", "Configured Base Internal"),
			guildRole("role-configured-ext", "Configured Base External"),
		},
		membersByID: map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
	}
}

// /foxhole add gives the member the roles named from the configured base
// name, for one scope and for both. Membership, not order: nothing
// downstream depends on Internal preceding External.
func TestFoxholeAddGivesTheRolesNamedFromTheConfiguredBaseName(t *testing.T) {
	cases := []struct {
		scope string
		want  []string
	}{
		{"internal", []string{"role-configured-int"}},
		{"both", []string{"role-configured-ext", "role-configured-int"}},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			t.Setenv(foxholeRoleBaseNameEnv, "Configured Base")
			gm := configuredRoleGuild()

			runFoxhole(&fakeResponder{}, gm, nil, foxholeInteraction("guild-1",
				stringOption("command", "add"),
				stringOption("flag", tc.scope),
				stringOption("discordname", "123456789012345678"),
			))

			var got []string
			for _, add := range gm.roleAddCalls() {
				got = append(got, add.roleID)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("the add gave roles %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBulkAddInternalAppliesConfiguredRole covers /foxhole-bulkadd-internal,
// which resolves its role independently of the four /foxhole subcommands. The
// observable is the role reaching the member, not the absence of an error.
func TestBulkAddInternalAppliesConfiguredRole(t *testing.T) {
	t.Setenv(foxholeRoleBaseNameEnv, "Verified Foxhole")

	rec := &captureRecorder{}
	rec.install(t)
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Trooper.A", "111111111111111111"),
	), http.StatusOK, nil)

	gm := &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("role-foxhole-int", "Verified Foxhole Internal")},
	}
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, gm, nil, foxholeBulkAddInternalInteraction("D/ACD"))

	adds := gm.roleAddCalls()
	if len(adds) != 1 {
		t.Fatalf("expected the roster member to be added, got %d add(s)", len(adds))
	}
	if adds[0].roleID != "role-foxhole-int" {
		t.Fatalf("add must target the role resolved from the configured name, got %q", adds[0].roleID)
	}
}
