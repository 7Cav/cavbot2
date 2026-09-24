package commands

import (
	"fmt"
	"slices"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// --- The permission fixture and the can-join oracle (#347 spec, testing
// decisions; #348). Lock tests assert who can join a channel, never the
// shape of its overwrite list: they read a channel's list off the fake
// (overwritesOf) and ask canJoin for each fixture member. ---

// The fixture's roles. None is a rank role, so a fixture member owns
// nothing unless a test adds a rank role to them.
const (
	// permRoleMember allows View and Connect on the category, the common
	// shape of a unit's role on the live guild.
	permRoleMember = "role-member"
	// permRoleViewer allows View alone on the category: its holder sees a
	// spawned channel and cannot join it.
	permRoleViewer = "role-viewer"
	// permRoleMod is the fixture hub's moderator role (permFixtureHub).
	permRoleMod = "role-mod"
)

// permJoin is what joining a voice channel takes: View Channel and Connect
// both. The raw Connect bit alone never counts.
const permJoin = discordgo.PermissionViewChannel | discordgo.PermissionVoiceConnect

// permMember is a member the oracle judges: a user ID and the roles held.
// A test that needs someone the spec names beyond the five below builds
// its own, O or X with permRoleViewer for instance.
type permMember struct {
	id    string
	roles []string
}

// The spec's fixture members.
var (
	// permM holds the member role and is not a guest.
	permM = permMember{id: "user-m", roles: []string{permRoleMember}}
	// permG holds the member role and is inside when the channel locks.
	permG = permMember{id: "user-g", roles: []string{permRoleMember}}
	// permV holds the viewer role: sees the channel, and the source alone
	// does not let them join.
	permV = permMember{id: "user-v", roles: []string{permRoleViewer}}
	// permN holds no role and cannot see the channel.
	permN = permMember{id: "user-n"}
	// permMOD holds the member role and the fixture hub's moderator role.
	permMOD = permMember{id: "user-mod", roles: []string{permRoleMember, permRoleMod}}
)

// permMembers lists the fixture members in the spec's order, for loops that
// compare every member's verdict.
var permMembers = []permMember{permM, permG, permV, permN, permMOD}

// discordMember is the member object a voice event or an interaction
// carries for m.
func (m permMember) discordMember() *discordgo.Member {
	return &discordgo.Member{User: &discordgo.User{ID: m.id}, Roles: slices.Clone(m.roles)}
}

// permCategoryOverwrites is the fixture category's list, the live guild's
// common shape: @everyone denied View and Connect, the member role allowed
// both, the viewer role allowed View alone.
func permCategoryOverwrites() []*discordgo.PermissionOverwrite {
	return []*discordgo.PermissionOverwrite{
		{ID: testTempVCGuild, Type: discordgo.PermissionOverwriteTypeRole, Deny: permJoin},
		{ID: permRoleMember, Type: discordgo.PermissionOverwriteTypeRole, Allow: permJoin},
		{ID: permRoleViewer, Type: discordgo.PermissionOverwriteTypeRole, Allow: discordgo.PermissionViewChannel},
	}
}

// permHubChannelOverwrites is the fixture hub channel's own list: a stricter
// hub inside the looser category, which is what source hub_channel is for.
// The member role sees it and is denied Connect, and the moderator role
// alone connects. So the two sources disagree about M and G, and a channel
// that took the wrong one gives them the wrong verdict.
func permHubChannelOverwrites() []*discordgo.PermissionOverwrite {
	return []*discordgo.PermissionOverwrite{
		{ID: testTempVCGuild, Type: discordgo.PermissionOverwriteTypeRole, Deny: permJoin},
		{ID: permRoleMember, Type: discordgo.PermissionOverwriteTypeRole,
			Allow: discordgo.PermissionViewChannel, Deny: discordgo.PermissionVoiceConnect},
		{ID: permRoleViewer, Type: discordgo.PermissionOverwriteTypeRole, Allow: discordgo.PermissionViewChannel},
		{ID: permRoleMod, Type: discordgo.PermissionOverwriteTypeRole, Allow: permJoin},
	}
}

// installPermFixture puts the fixture's category, with its list, into the
// fake cache, and gives the test hub channel its own stricter list.
func installPermFixture(f *fakeTempVCManager) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[testTempVCCategory] = &discordgo.Channel{
		ID:                   testTempVCCategory,
		GuildID:              testTempVCGuild,
		Type:                 discordgo.ChannelTypeGuildCategory,
		PermissionOverwrites: permCategoryOverwrites(),
	}
	f.channels[testTempVCHub].PermissionOverwrites = permHubChannelOverwrites()
}

// permFixtureHub is the test hub with the given permission source and the
// fixture's moderator role.
func permFixtureHub(source store.PermissionSource) store.Hub {
	hub := testHub()
	hub.PermissionSource = source
	hub.ModeratorRoleIDs = []string{permRoleMod}
	return hub
}

// newPermFixture builds the one-hub runtime over the permission fixture:
// the fake cache holds the fixture's category and hub channel, and the
// store holds permFixtureHub with the given source.
func newPermFixture(t *testing.T, source store.PermissionSource) (*fakeTempVCManager, *store.Fake, *TempVC) {
	t.Helper()
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	st := seedStore(t, permFixtureHub(source))
	return fake, st, newTestTempVC(t, fake, st)
}

// fixturePermissions is what discordgo computes for a member holding the
// given roles in a voice channel carrying the given overwrites: a State
// built for the call holds the guild, whose @everyone role has View and
// Connect at guild level and whose fixture roles have nothing, the channel,
// and the member.
func fixturePermissions(overwrites []*discordgo.PermissionOverwrite, userID string, roles []string) (int64, error) {
	const probe = "can-join-probe"
	st := discordgo.NewState()
	err := st.GuildAdd(&discordgo.Guild{
		ID: testTempVCGuild,
		Roles: []*discordgo.Role{
			{ID: testTempVCGuild, Permissions: permJoin},
			{ID: permRoleMember},
			{ID: permRoleViewer},
			{ID: permRoleMod},
		},
		Channels: []*discordgo.Channel{{
			ID: probe, GuildID: testTempVCGuild, Type: discordgo.ChannelTypeGuildVoice,
			PermissionOverwrites: cloneOverwrites(overwrites),
		}},
		Members: []*discordgo.Member{{
			GuildID: testTempVCGuild, User: &discordgo.User{ID: userID}, Roles: slices.Clone(roles),
		}},
	})
	if err != nil {
		return 0, fmt.Errorf("State.GuildAdd: %w", err)
	}
	perms, err := st.UserChannelPermissions(userID, probe)
	if err != nil {
		return 0, fmt.Errorf("State.UserChannelPermissions(%s): %w", userID, err)
	}
	return perms, nil
}

// channelPermissions is fixturePermissions for a fixture member, failing
// the test when discordgo cannot compute it.
func channelPermissions(t *testing.T, overwrites []*discordgo.PermissionOverwrite, m permMember) int64 {
	t.Helper()
	perms, err := fixturePermissions(overwrites, m.id, m.roles)
	if err != nil {
		t.Fatal(err)
	}
	return perms
}

// canJoin is the can-join oracle: whether m can join a voice channel
// carrying the given overwrites. Read a channel's list with overwritesOf.
func canJoin(t *testing.T, overwrites []*discordgo.PermissionOverwrite, m permMember) bool {
	t.Helper()
	return channelPermissions(t, overwrites, m)&permJoin == permJoin
}

// canSee reports whether m sees a voice channel carrying the given
// overwrites, the View Channel half of joining.
func canSee(t *testing.T, overwrites []*discordgo.PermissionOverwrite, m permMember) bool {
	t.Helper()
	return channelPermissions(t, overwrites, m)&discordgo.PermissionViewChannel != 0
}

// The fixture has the shape the spec gives it, so the lock tests' verdicts
// mean what they say. Guild-level @everyone joins a channel with no
// overwrites. Under the category, M, G and MOD join and V and N do not; V
// sees the channel and N does not. Under the hub channel's own list MOD
// alone joins, so the two sources disagree. Connect without View is not a
// join.
func TestPermFixtureHasTheSpecShape(t *testing.T) {
	if !canJoin(t, nil, permN) {
		t.Error("N cannot join a channel with no overwrites, want guild-level @everyone to join")
	}

	category := permCategoryOverwrites()
	for _, tc := range []struct {
		m             permMember
		sees          bool
		joinsCategory bool
		joinsHub      bool
	}{
		{permM, true, true, false},
		{permG, true, true, false},
		{permV, true, false, false},
		{permN, false, false, false},
		{permMOD, true, true, true},
	} {
		if got := canSee(t, category, tc.m); got != tc.sees {
			t.Errorf("%s sees the category's channel = %v, want %v", tc.m.id, got, tc.sees)
		}
		if got := canJoin(t, category, tc.m); got != tc.joinsCategory {
			t.Errorf("%s can join under the category = %v, want %v", tc.m.id, got, tc.joinsCategory)
		}
		if got := canJoin(t, permHubChannelOverwrites(), tc.m); got != tc.joinsHub {
			t.Errorf("%s can join under the hub channel's list = %v, want %v", tc.m.id, got, tc.joinsHub)
		}
	}

	// @everyone keeps its guild-level Connect here and loses View.
	hidden := []*discordgo.PermissionOverwrite{
		{ID: testTempVCGuild, Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionViewChannel},
	}
	if canJoin(t, hidden, permN) {
		t.Error("N can join a channel it cannot see, want Connect alone not to count")
	}
}

// A spawned channel gives every fixture member the verdict its hub's
// permission source gives: the hub channel's under source hub_channel, the
// category's under source category. The fixture's two sources disagree, so
// a spawn that took the other kind's list goes red.
func TestTempVCSpawnedChannelJoinsLikeItsPermissionSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source store.PermissionSource
		from   string
	}{
		{"hub channel", store.PermissionHubChannel, testTempVCHub},
		{"category", store.PermissionCategory, testTempVCCategory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, _, tv := newPermFixture(t, tc.source)

			spawnInto(tv, fake, permMOD.id, "new-chan", permMOD.discordMember())

			spawned, source := fake.overwritesOf(t, "new-chan"), fake.overwritesOf(t, tc.from)
			for _, m := range permMembers {
				if got, want := canJoin(t, spawned, m), canJoin(t, source, m); got != want {
					t.Errorf("%s can join the spawned channel = %v, want %v as under the %s", m.id, got, want, tc.name)
				}
			}
		})
	}
}

// permissionSourceOverwrites returns the list of the source the hub names,
// read from the cache: the category's for source category, the hub
// channel's own for source hub_channel. Unlock puts it back on a locked
// channel, so each member's verdict under it is the verdict under the
// source itself.
func TestTempVCPermissionSourceReadsTheSourceList(t *testing.T) {
	for _, tc := range []struct {
		source store.PermissionSource
		from   string
	}{
		{store.PermissionCategory, testTempVCCategory},
		{store.PermissionHubChannel, testTempVCHub},
	} {
		t.Run(string(tc.source), func(t *testing.T) {
			fake, _, tv := newPermFixture(t, tc.source)

			got, err := tv.permissionSourceOverwrites(permFixtureHub(tc.source))
			if err != nil {
				t.Fatalf("permissionSourceOverwrites: %v", err)
			}
			source := fake.overwritesOf(t, tc.from)
			for _, m := range permMembers {
				if g, w := canJoin(t, got, m), canJoin(t, source, m); g != w {
					t.Errorf("%s can join under the returned list = %v, want %v as under %s", m.id, g, w, tc.from)
				}
			}
		})
	}
}

// A source that cannot be read is an error for either kind, so unlock can
// refuse rather than put some other list on the channel.
func TestTempVCPermissionSourceUnreadableIsAnError(t *testing.T) {
	missingHub := func(f *fakeTempVCManager) { f.dropChannel(testTempVCHub) }
	noParent := func(f *fakeTempVCManager) { f.channels[testTempVCHub].ParentID = "" }
	missingCategory := func(f *fakeTempVCManager) { f.dropChannel(testTempVCCategory) }
	for _, tc := range []struct {
		name   string
		source store.PermissionSource
		breaks func(f *fakeTempVCManager)
	}{
		{"hub channel missing from the cache, source category", store.PermissionCategory, missingHub},
		{"hub channel missing from the cache, source hub_channel", store.PermissionHubChannel, missingHub},
		{"hub channel with no parent, source category", store.PermissionCategory, noParent},
		{"hub channel with no parent, source hub_channel", store.PermissionHubChannel, noParent},
		{"category missing from the cache, source category", store.PermissionCategory, missingCategory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, _, tv := newPermFixture(t, tc.source)
			tc.breaks(fake)

			if got, err := tv.permissionSourceOverwrites(permFixtureHub(tc.source)); err == nil {
				t.Errorf("permissionSourceOverwrites = %d overwrites and no error, want an error", len(got))
			}
		})
	}
}
