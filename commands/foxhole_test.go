package commands

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// fakeGuildManager is a deterministic GuildManager for /foxhole integration
// tests. It records every call and serves canned data; per-method error queues
// (popped FIFO via popErr) let a test inject a failure on a specific call. The
// zero value answers role/member lookups from the maps below, so a test only
// populates what it exercises.
type fakeGuildManager struct {
	mu    sync.Mutex
	calls []string

	// roles returned by GuildRoles (name->ID lookups and fetch-by-ID both read it).
	roles []*discordgo.Role
	// members keyed by the query (mention/ID/name) GuildMember/Search receives.
	membersByID    map[string]*discordgo.Member
	searchResults  map[string][]*discordgo.Member
	channels       []*discordgo.Channel
	createdRole    *discordgo.Role // returned by GuildRoleCreate
	nextCreatedNum int

	// channelMessages records every ChannelMessageSend (the purge fallback
	// surface) so a test can assert what was delivered, and where.
	channelMessages []sentChannelMessage

	// deletedRoleIDs records the roleID of every GuildRoleDelete call, so a test
	// can assert WHICH role a recreate failure cleaned up (the new orphan, not
	// the old role).
	deletedRoleIDs []string

	// roleAdds records the full argument tuple of every GuildMemberRoleAdd call,
	// so a test can prove the right Discord IDs reached the right role (the bare
	// Calls() log only keeps the method name).
	roleAdds []roleAddCall

	// writes records every call that changes the guild, with the audit log
	// reason it carried, in the order they were made.
	writes []guildWrite

	// overwrites records every ChannelPermissionSet call's channel, target
	// and permissions, so a test can read which overwrites a purge left.
	overwrites []overwriteWrite

	// roleAddErrsByUser fails every GuildMemberRoleAdd for the member it
	// keys with that member's error, ahead of MemberRoleAddErrs.
	roleAddErrsByUser map[string]error

	RolesErrs            []error
	RoleCreateErrs       []error
	RoleDeleteErrs       []error
	ChannelsErrs         []error
	ChannelPermSetErrs   []error
	MemberErrs           []error
	MembersSearchErrs    []error
	MemberRoleAddErrs    []error
	MemberRoleRemoveErrs []error
	ChannelMessageErrs   []error
}

// sentChannelMessage is one recorded ChannelMessageSend call.
type sentChannelMessage struct {
	channelID string
	content   string
}

// guildWrite is one recorded call that changes the guild: the method name
// and the audit log reason it carried.
type guildWrite struct {
	method string
	reason string
}

// overwriteWrite is one recorded ChannelPermissionSet call.
type overwriteWrite struct {
	channelID, targetID string
	allow, deny         int64
}

// roleAddCall is one recorded GuildMemberRoleAdd call with every argument kept,
// so a test can assert the exact (guildID, userID, roleID) tuples a command
// emitted rather than only how many adds happened.
type roleAddCall struct {
	guildID string
	userID  string
	roleID  string
}

func (g *fakeGuildManager) record(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, name)
}

// recordWrite records a call that changes the guild, under name, with its
// audit log reason.
func (g *fakeGuildManager) recordWrite(name, reason string) {
	g.record(name)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writes = append(g.writes, guildWrite{method: name, reason: reason})
}

// guildWrites returns a copy of every recorded call that changed the guild.
func (g *fakeGuildManager) guildWrites() []guildWrite {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]guildWrite(nil), g.writes...)
}

func (g *fakeGuildManager) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

func (g *fakeGuildManager) countCalls(name string) int {
	n := 0
	for _, c := range g.Calls() {
		if c == name {
			n++
		}
	}
	return n
}

func (g *fakeGuildManager) GuildRoles(_ string) ([]*discordgo.Role, error) {
	g.record("GuildRoles")
	if err := popErr(&g.RolesErrs); err != nil {
		return nil, err
	}
	return g.roles, nil
}

func (g *fakeGuildManager) GuildRoleCreate(_ string, _ *discordgo.RoleParams, auditReason string) (*discordgo.Role, error) {
	g.recordWrite("GuildRoleCreate", auditReason)
	if err := popErr(&g.RoleCreateErrs); err != nil {
		return nil, err
	}
	g.nextCreatedNum++
	if g.createdRole != nil {
		return g.createdRole, nil
	}
	return &discordgo.Role{ID: fmt.Sprintf("new-role-%d", g.nextCreatedNum)}, nil
}

func (g *fakeGuildManager) GuildRoleDelete(_, roleID, auditReason string) error {
	g.recordWrite("GuildRoleDelete", auditReason)
	g.mu.Lock()
	g.deletedRoleIDs = append(g.deletedRoleIDs, roleID)
	g.mu.Unlock()
	return popErr(&g.RoleDeleteErrs)
}

// deletedRoles returns the roleID of every GuildRoleDelete call, in order.
// Lets a test assert a recreate failure deleted the new orphan role rather
// than the old role.
func (g *fakeGuildManager) deletedRoles() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.deletedRoleIDs...)
}

func (g *fakeGuildManager) GuildChannels(_ string) ([]*discordgo.Channel, error) {
	g.record("GuildChannels")
	if err := popErr(&g.ChannelsErrs); err != nil {
		return nil, err
	}
	return g.channels, nil
}

func (g *fakeGuildManager) ChannelPermissionSet(channelID, targetID string, _ discordgo.PermissionOverwriteType, allow, deny int64, auditReason string) error {
	g.recordWrite("ChannelPermissionSet", auditReason)
	if err := popErr(&g.ChannelPermSetErrs); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.overwrites = append(g.overwrites, overwriteWrite{channelID: channelID, targetID: targetID, allow: allow, deny: deny})
	return nil
}

// overwritesSet returns a copy of every overwrite a ChannelPermissionSet
// call made.
func (g *fakeGuildManager) overwritesSet() []overwriteWrite {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]overwriteWrite(nil), g.overwrites...)
}

func (g *fakeGuildManager) GuildMember(_, userID string) (*discordgo.Member, error) {
	g.record("GuildMember")
	if err := popErr(&g.MemberErrs); err != nil {
		return nil, err
	}
	if m, ok := g.membersByID[userID]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("member %s not found", userID)
}

func (g *fakeGuildManager) GuildMembersSearch(_, query string, _ int) ([]*discordgo.Member, error) {
	g.record("GuildMembersSearch")
	if err := popErr(&g.MembersSearchErrs); err != nil {
		return nil, err
	}
	return g.searchResults[query], nil
}

func (g *fakeGuildManager) GuildMemberRoleAdd(guildID, userID, roleID, auditReason string) error {
	g.recordWrite("GuildMemberRoleAdd", auditReason)
	g.mu.Lock()
	g.roleAdds = append(g.roleAdds, roleAddCall{guildID: guildID, userID: userID, roleID: roleID})
	err, failed := g.roleAddErrsByUser[userID]
	g.mu.Unlock()
	if failed {
		return err
	}
	return popErr(&g.MemberRoleAddErrs)
}

// roleAddCalls returns a copy of every recorded GuildMemberRoleAdd tuple.
func (g *fakeGuildManager) roleAddCalls() []roleAddCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]roleAddCall(nil), g.roleAdds...)
}

func (g *fakeGuildManager) GuildMemberRoleRemove(_, _, _, auditReason string) error {
	g.recordWrite("GuildMemberRoleRemove", auditReason)
	return popErr(&g.MemberRoleRemoveErrs)
}

func (g *fakeGuildManager) ChannelMessageSend(channelID, content string) error {
	g.record("ChannelMessageSend")
	g.mu.Lock()
	g.channelMessages = append(g.channelMessages, sentChannelMessage{channelID: channelID, content: content})
	g.mu.Unlock()
	return popErr(&g.ChannelMessageErrs)
}

// foxholeInteraction builds a /foxhole *InteractionCreate carrying the given
// option values plus a GuildID, which runFoxhole requires (it rejects
// DM-context).
func foxholeInteraction(guildID string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	i := fakeAppCommandInteraction(opts...)
	i.Data = discordgo.ApplicationCommandInteractionData{Name: "foxhole", Options: opts}
	i.GuildID = guildID
	return i
}

// guildRole is a convenience constructor for a guild role.
func guildRole(id, name string) *discordgo.Role {
	return &discordgo.Role{ID: id, Name: name}
}

// noOverwriteDelay zeroes the purge throttle for the duration of a test so the
// suite doesn't sleep 200ms per channel overwrite.
func noOverwriteDelay(t *testing.T) {
	t.Helper()
	prev := purgeOverwriteDelay
	purgeOverwriteDelay = 0
	t.Cleanup(func() { purgeOverwriteDelay = prev })
}

// lastEditContent returns the Content of the last Edit call, or "<none>".
func lastEditContent(calls []recordedCall) string {
	for idx := len(calls) - 1; idx >= 0; idx-- {
		if calls[idx].Method == "Edit" && calls[idx].Edit != nil && calls[idx].Edit.Content != nil {
			return *calls[idx].Edit.Content
		}
	}
	return "<none>"
}

// lastResponseContent returns the Content of the last immediate Respond call
// (the surface utils.HandleError uses for an un-deferred rejection), or "<none>".
func lastResponseContent(calls []recordedCall) string {
	for idx := len(calls) - 1; idx >= 0; idx-- {
		if calls[idx].Method == "Respond" && calls[idx].Response != nil && calls[idx].Response.Data != nil {
			return calls[idx].Response.Data.Content
		}
	}
	return "<none>"
}

// The verdict markers a Foxhole command's reply opens a line with.
const (
	verdictDone     = "✅"
	verdictFailed   = "❌"
	verdictLeftOver = "⚠️"
)

// verdicts returns the verdict marker that opens each line of reply naming
// name, in order, or each line's when name is empty: verdictDone,
// verdictFailed, verdictLeftOver, or "" for a line with none. Blank lines
// are skipped. The marker is the one piece of reply text the Foxhole
// command tests read besides names and production constants, because a
// plain-text reply has no other signal of how each change went; the rest of
// a sentence is free to change.
func verdicts(reply, name string) []string {
	var out []string
	for _, line := range strings.Split(reply, "\n") {
		if strings.TrimSpace(line) == "" || !strings.Contains(line, name) {
			continue
		}
		marker := ""
		for _, m := range []string{verdictDone, verdictFailed, verdictLeftOver} {
			if strings.HasPrefix(line, m) {
				marker = m
			}
		}
		out = append(out, marker)
	}
	return out
}

// assertVerdict fails unless reply has exactly one line naming name, and
// that line opens with want.
func assertVerdict(t *testing.T, reply, name, want string) {
	t.Helper()
	if got := verdicts(reply, name); !slices.Equal(got, []string{want}) {
		t.Errorf("the lines naming %q carry verdicts %q, want one %q; reply %q", name, got, want, reply)
	}
}

// adviceKinds is the advice for each kind of Discord failure a reply tells
// apart.
var adviceKinds = []string{adviceTransient, adviceAbsent, adviceConfigFault, adviceMissingPermissions, adviceRejected}

// assertAdvice fails unless reply carries want, one kind of Discord
// failure's advice, and none of the other kinds'.
func assertAdvice(t *testing.T, reply, want string) {
	t.Helper()
	for _, advice := range adviceKinds {
		if has := strings.Contains(reply, advice); has != (advice == want) {
			t.Errorf("reply %q carries advice %q: %v, want %v", reply, advice, has, advice == want)
		}
	}
}

// assertReplyNames fails unless reply names each of the members and roles.
func assertReplyNames(t *testing.T, reply string, names ...string) {
	t.Helper()
	for _, name := range names {
		if !strings.Contains(reply, name) {
			t.Errorf("reply %q does not name %q", reply, name)
		}
	}
}

// --- runFoxhole routing / deferred-ephemeral acknowledge path ---

func TestRunFoxhole_AddDeferredEphemeralAcknowledge(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	gm := &fakeGuildManager{
		roles:       []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		membersByID: map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
	}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1",
		stringOption("command", "add"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)

	runFoxhole(f, gm, nil, i)

	calls := f.Calls()
	if len(calls) == 0 {
		t.Fatal("expected at least one responder call")
	}
	// The deferred-ephemeral pattern is load-bearing: the first response MUST be
	// a deferred-channel-message with the ephemeral flag set.
	first := calls[0]
	if first.Method != "Respond" {
		t.Fatalf("calls[0]: expected Respond (defer), got %q", first.Method)
	}
	if first.Response.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource {
		t.Fatalf("expected deferred response type, got %v", first.Response.Type)
	}
	if first.Response.Data == nil || first.Response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("expected MessageFlagsEphemeral on the defer; got flags=%v", first.Response.Data)
	}
	// Role added, success edit follows.
	if gm.countCalls("GuildMemberRoleAdd") != 1 {
		t.Fatalf("expected 1 GuildMemberRoleAdd, got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	reply := lastEditContent(calls)
	assertVerdict(t, reply, "trooper", verdictDone)
	assertReplyNames(t, reply, defaultInternalRoleName)
	// A reply delivered is no fault: nothing reaches Sentry.
	if rec.count != 0 {
		t.Errorf("a clean add captured %d events to Sentry, want none", rec.count)
	}
}

func TestRunFoxhole_InvalidSubcommandSurfacesError(t *testing.T) {
	gm := &fakeGuildManager{}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1",
		stringOption("command", "bogus"),
		stringOption("flag", "internal"),
	)

	runFoxhole(f, gm, nil, i)

	if len(f.Calls()) == 0 {
		t.Fatal("expected an error response")
	}
	if len(gm.Calls()) != 0 {
		t.Fatalf("invalid subcommand must not touch guild; got %v", gm.Calls())
	}
}

func TestRunFoxhole_MissingGuildIDRejected(t *testing.T) {
	gm := &fakeGuildManager{}
	f := &fakeResponder{}
	// No GuildID => DM context, must be rejected before any guild call.
	i := fakeAppCommandInteraction(
		stringOption("command", "add"),
		stringOption("flag", "internal"),
		stringOption("discordname", "x"),
	)

	runFoxhole(f, gm, nil, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("DM-context must not touch guild; got %v", gm.Calls())
	}
}

// A DM-shaped interaction has a nil Member (Discord populates interaction.User
// instead), and a malformed one has neither. The entry-log read of the
// invoking user must not panic, and the guild-context guard must refuse the
// run before any guild call. Regression for #177.
func TestRunFoxhole_MemberlessInteractionRefusedWithoutPanic(t *testing.T) {
	cases := []struct {
		name string
		user *discordgo.User
	}{
		{"DM context with a user", &discordgo.User{ID: "555", Username: "dmuser"}},
		{"neither member nor user", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gm := &fakeGuildManager{}
			f := &fakeResponder{}
			i := fakeAppCommandInteraction(
				stringOption("command", "add"),
				stringOption("flag", "internal"),
				stringOption("discordname", "x"),
			)
			i.Member = nil
			i.User = tc.user

			runFoxhole(f, gm, nil, i) // must not panic on the entry-log Member deref

			if len(gm.Calls()) != 0 {
				t.Fatalf("a memberless interaction must not touch the guild; got %v", gm.Calls())
			}
			if got := verdicts(lastResponseContent(f.Calls()), ""); !slices.Equal(got, []string{verdictFailed}) {
				t.Fatalf("the refusal's verdicts are %q, want one %q", got, verdictFailed)
			}
		})
	}
}

// --- purge: happy path ---

// oneOverwriteGuild is a guild whose Internal role, old-int, carries an
// overwrite on one channel.
func oneOverwriteGuild() *fakeGuildManager {
	return &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("old-int", foxholeRoleBaseNameDefault+" Internal")},
		channels: []*discordgo.Channel{
			{
				ID: "chan-1",
				PermissionOverwrites: []*discordgo.PermissionOverwrite{
					{ID: "old-int", Type: discordgo.PermissionOverwriteTypeRole, Allow: 1, Deny: 0},
				},
			},
		},
	}
}

// A purge recreates the role, carries its channel overwrite to the new role
// (new-role-1, the fake's first created role), deletes the old role, and
// says the role was recreated.
func TestRunFoxholePurge_HappyPath(t *testing.T) {
	noOverwriteDelay(t)
	gm := oneOverwriteGuild()
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1")

	runFoxholePurge(f, gm, i, "guild-1", "internal")

	if gm.countCalls("GuildRoleCreate") != 1 {
		t.Fatalf("expected 1 GuildRoleCreate, got %v", gm.Calls())
	}
	want := []overwriteWrite{{channelID: "chan-1", targetID: "new-role-1", allow: 1}}
	if got := gm.overwritesSet(); !slices.Equal(got, want) {
		t.Fatalf("the purge left overwrites %+v, want the old role's carried to the new role, %+v", got, want)
	}
	if got := gm.deletedRoles(); !slices.Equal(got, []string{"old-int"}) {
		t.Fatalf("the purge deleted roles %v, want only the old role old-int", got)
	}
	assertVerdict(t, lastEditContent(f.Calls()), defaultInternalRoleName, verdictDone)
}

func TestRunFoxholePurge_BothScopeRecreatesTwoRoles(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{
			guildRole("old-int", foxholeRoleBaseNameDefault+" Internal"),
			guildRole("old-ext", foxholeRoleBaseNameDefault+" External"),
		},
	}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1")

	runFoxholePurge(f, gm, i, "guild-1", "both")

	if gm.countCalls("GuildRoleCreate") != 2 {
		t.Fatalf("expected 2 role creates for 'both', got %d (%v)", gm.countCalls("GuildRoleCreate"), gm.Calls())
	}
	reply := lastEditContent(f.Calls())
	assertVerdict(t, reply, defaultInternalRoleName, verdictDone)
	assertVerdict(t, reply, foxholeRoleBaseNameDefault+" External", verdictDone)
}

// --- purge: partial failure (one role recreation fails) ---

func TestRunFoxholePurge_PartialFailureContinues(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{
			guildRole("old-int", foxholeRoleBaseNameDefault+" Internal"),
			guildRole("old-ext", foxholeRoleBaseNameDefault+" External"),
		},
		// First create succeeds, second fails -> External role recreation errors,
		// but the loop must continue and report a mixed summary.
		RoleCreateErrs: []error{nil, fmt.Errorf("discord 500")},
	}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1")

	runFoxholePurge(f, gm, i, "guild-1", "both")

	reply := lastEditContent(f.Calls())
	assertVerdict(t, reply, defaultInternalRoleName, verdictDone)
	assertVerdict(t, reply, foxholeRoleBaseNameDefault+" External", verdictFailed)
	// The old External role must NOT be deleted when its recreation failed.
	if got := gm.deletedRoles(); !slices.Equal(got, []string{"old-int"}) {
		t.Fatalf("the purge deleted roles %v, want only old-int, whose recreation succeeded", got)
	}
}

func TestRunFoxholePurge_RoleNotFoundSurfaces(t *testing.T) {
	noOverwriteDelay(t)
	// No matching Foxhole role in the guild: the purge stops before any
	// mutation, and names the role it couldn't find.
	gm := &fakeGuildManager{roles: []*discordgo.Role{guildRole("x", "Some Other Role")}}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1")

	runFoxholePurge(f, gm, i, "guild-1", "internal")

	if gm.countCalls("GuildChannels") != 0 {
		t.Fatalf("must short-circuit before fetching channels; got %v", gm.Calls())
	}
	assertVerdict(t, lastEditContent(f.Calls()), defaultInternalRoleName, verdictFailed)
}

// --- remove + bulkadd coverage ---

func TestRunFoxhole_RemoveSuccess(t *testing.T) {
	gm := &fakeGuildManager{
		roles:       []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		membersByID: map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
	}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1",
		stringOption("command", "remove"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)

	runFoxhole(f, gm, nil, i)

	if gm.countCalls("GuildMemberRoleRemove") != 1 {
		t.Fatalf("expected 1 GuildMemberRoleRemove, got %v", gm.Calls())
	}
	reply := lastEditContent(f.Calls())
	assertVerdict(t, reply, "trooper", verdictDone)
	assertReplyNames(t, reply, defaultInternalRoleName)
}

func TestRunFoxhole_BulkAddMixedResults(t *testing.T) {
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			"good": {{User: &discordgo.User{ID: "111", Username: "good"}}},
			// "bad" has no search result, so no member matches it.
		},
	}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", "good, bad"),
	)

	runFoxhole(f, gm, nil, i)

	calls := f.Calls()
	embed := lastEditEmbed(calls)
	if embed == nil {
		t.Fatal("expected the reply to name the added member, got no embed")
	}
	if mentioned := regexp.MustCompile(`<@(\d+)>`).FindAllStringSubmatch(embed.Description, -1); len(mentioned) != 1 || mentioned[0][1] != "111" {
		t.Fatalf("expected the reply to name only the added member 111, got embed %q", embed.Description)
	}
	assertVerdict(t, lastEditContent(calls), "bad", verdictFailed)
}

func TestRunFoxhole_MissingDiscordnameForAdd(t *testing.T) {
	gm := &fakeGuildManager{}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1",
		stringOption("command", "add"),
		stringOption("flag", "internal"),
	)

	runFoxhole(f, gm, nil, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("missing discordname must short-circuit before guild calls; got %v", gm.Calls())
	}
	if got := verdicts(lastResponseContent(f.Calls()), ""); !slices.Equal(got, []string{verdictFailed}) {
		t.Fatalf("the refusal's verdicts are %q, want one %q", got, verdictFailed)
	}
}

// Discord's member search takes 1 to 100 characters, counted in runes. A
// longer name is refused before it reaches Discord, which would answer 400
// and page Sentry (CAVBOT2-6); a name at the limit, or a multibyte name
// under it in runes but over it in bytes, still searches.
func TestRunFoxhole_AddNameQueryLengthLimit(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		searches bool
	}{
		{"100 characters", strings.Repeat("a", 100), true},
		{"101 characters", strings.Repeat("a", 101), false},
		{"80 multibyte runes in 240 bytes", strings.Repeat("世", 80), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gm := &fakeGuildManager{roles: []*discordgo.Role{guildRole("r-int", defaultInternalRoleName)}}
			f := &fakeResponder{}

			runFoxhole(f, gm, nil, foxholeInteraction("guild-1",
				stringOption("command", "add"),
				stringOption("flag", "internal"),
				stringOption("discordname", tc.query),
			))

			if searched := gm.countCalls("GuildMembersSearch") == 1; searched != tc.searches {
				t.Fatalf("the name search ran: %v, want %v; calls %v", searched, tc.searches, gm.Calls())
			}
			if !tc.searches {
				if got := verdicts(lastEditContent(f.Calls()), ""); !slices.Equal(got, []string{verdictFailed}) {
					t.Errorf("the refusal's verdicts are %q, want one %q", got, verdictFailed)
				}
			}
		})
	}
}
