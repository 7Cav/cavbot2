package commands

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// fakeGuildManager is a deterministic GuildManager for /warden integration
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

	RolesErrs            []error
	RoleCreateErrs       []error
	RoleEditErrs         []error
	RoleDeleteErrs       []error
	ChannelsErrs         []error
	ChannelPermSetErrs   []error
	MemberErrs           []error
	MembersSearchErrs    []error
	MemberRoleAddErrs    []error
	MemberRoleRemoveErrs []error
}

func (g *fakeGuildManager) record(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, name)
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

func (g *fakeGuildManager) GuildRoleCreate(_ string, _ *discordgo.RoleParams) (*discordgo.Role, error) {
	g.record("GuildRoleCreate")
	if err := popErr(&g.RoleCreateErrs); err != nil {
		return nil, err
	}
	g.nextCreatedNum++
	if g.createdRole != nil {
		return g.createdRole, nil
	}
	return &discordgo.Role{ID: fmt.Sprintf("new-role-%d", g.nextCreatedNum)}, nil
}

func (g *fakeGuildManager) GuildRoleEdit(_, _ string, _ *discordgo.RoleParams) (*discordgo.Role, error) {
	g.record("GuildRoleEdit")
	if err := popErr(&g.RoleEditErrs); err != nil {
		return nil, err
	}
	return &discordgo.Role{}, nil
}

func (g *fakeGuildManager) GuildRoleDelete(_, _ string) error {
	g.record("GuildRoleDelete")
	return popErr(&g.RoleDeleteErrs)
}

func (g *fakeGuildManager) GuildChannels(_ string) ([]*discordgo.Channel, error) {
	g.record("GuildChannels")
	if err := popErr(&g.ChannelsErrs); err != nil {
		return nil, err
	}
	return g.channels, nil
}

func (g *fakeGuildManager) ChannelPermissionSet(_, _ string, _ discordgo.PermissionOverwriteType, _, _ int64) error {
	g.record("ChannelPermissionSet")
	return popErr(&g.ChannelPermSetErrs)
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

func (g *fakeGuildManager) GuildMemberRoleAdd(_, _, _ string) error {
	g.record("GuildMemberRoleAdd")
	return popErr(&g.MemberRoleAddErrs)
}

func (g *fakeGuildManager) GuildMemberRoleRemove(_, _, _ string) error {
	g.record("GuildMemberRoleRemove")
	return popErr(&g.MemberRoleRemoveErrs)
}

// wardenInteraction builds an *InteractionCreate carrying the given option
// values plus a GuildID, which runWarden requires (it rejects DM-context).
func wardenInteraction(guildID string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	i := fakeAppCommandInteraction(opts...)
	i.GuildID = guildID
	return i
}

// wardenRole is a convenience constructor for a guild role.
func wardenRole(id, name string) *discordgo.Role {
	return &discordgo.Role{ID: id, Name: name}
}

// noOverwriteDelay zeroes the purge throttle for the duration of a test so the
// suite doesn't sleep 200ms per channel overwrite.
func noOverwriteDelay(t *testing.T) {
	t.Helper()
	prev := wardenOverwriteDelay
	wardenOverwriteDelay = 0
	t.Cleanup(func() { wardenOverwriteDelay = prev })
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

// --- runWarden routing / deferred-ephemeral acknowledge path ---

func TestRunWarden_AddDeferredEphemeralAcknowledge(t *testing.T) {
	gm := &fakeGuildManager{
		roles:       []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		membersByID: map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "add"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)

	runWarden(f, gm, i)

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
	if got := lastEditContent(calls); !strings.Contains(got, "✅ Added warden role(s)") {
		t.Fatalf("expected success edit, got %q", got)
	}
}

func TestRunWarden_InvalidSubcommandSurfacesError(t *testing.T) {
	gm := &fakeGuildManager{}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "bogus"),
		stringOption("flag", "internal"),
	)

	runWarden(f, gm, i)

	if len(f.Calls()) == 0 {
		t.Fatal("expected an error response")
	}
	if len(gm.Calls()) != 0 {
		t.Fatalf("invalid subcommand must not touch guild; got %v", gm.Calls())
	}
}

func TestRunWarden_MissingGuildIDRejected(t *testing.T) {
	gm := &fakeGuildManager{}
	f := &fakeResponder{}
	// No GuildID => DM context, must be rejected before any guild call.
	i := fakeAppCommandInteraction(
		stringOption("command", "add"),
		stringOption("flag", "internal"),
		stringOption("discordname", "x"),
	)

	runWarden(f, gm, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("DM-context must not touch guild; got %v", gm.Calls())
	}
}

// --- purge: happy path ---

func TestRunWardenPurge_HappyPath(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("old-int", wardenRoleBaseName+" Internal")},
		channels: []*discordgo.Channel{
			{
				ID: "chan-1",
				PermissionOverwrites: []*discordgo.PermissionOverwrite{
					{ID: "old-int", Type: discordgo.PermissionOverwriteTypeRole, Allow: 1, Deny: 0},
				},
			},
		},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1")

	runWardenPurge(f, gm, i, "guild-1", "internal")

	// Role recreated: create + edit + one overwrite reapply + delete old.
	if gm.countCalls("GuildRoleCreate") != 1 {
		t.Fatalf("expected 1 GuildRoleCreate, got %v", gm.Calls())
	}
	if gm.countCalls("ChannelPermissionSet") != 1 {
		t.Fatalf("expected 1 ChannelPermissionSet, got %v", gm.Calls())
	}
	if gm.countCalls("GuildRoleDelete") != 1 {
		t.Fatalf("expected 1 GuildRoleDelete, got %v", gm.Calls())
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "✅ Recreated 'Verified Warden Internal'") {
		t.Fatalf("expected success summary, got %q", got)
	}
	if !strings.Contains(got, "re-applied 1 overwrite(s)") {
		t.Fatalf("expected overwrite count in summary, got %q", got)
	}
}

func TestRunWardenPurge_BothScopeRecreatesTwoRoles(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{
			wardenRole("old-int", wardenRoleBaseName+" Internal"),
			wardenRole("old-ext", wardenRoleBaseName+" External"),
		},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1")

	runWardenPurge(f, gm, i, "guild-1", "both")

	if gm.countCalls("GuildRoleCreate") != 2 {
		t.Fatalf("expected 2 role creates for 'both', got %d (%v)", gm.countCalls("GuildRoleCreate"), gm.Calls())
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Internal") || !strings.Contains(got, "External") {
		t.Fatalf("expected both roles in summary, got %q", got)
	}
}

// --- purge: partial failure (one role recreation fails) ---

func TestRunWardenPurge_PartialFailureContinues(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{
			wardenRole("old-int", wardenRoleBaseName+" Internal"),
			wardenRole("old-ext", wardenRoleBaseName+" External"),
		},
		// First create succeeds, second fails -> External role recreation errors,
		// but the loop must continue and report a mixed summary.
		RoleCreateErrs: []error{nil, fmt.Errorf("discord 500")},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1")

	runWardenPurge(f, gm, i, "guild-1", "both")

	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "✅ Recreated 'Verified Warden Internal'") {
		t.Fatalf("expected the first role to succeed, got %q", got)
	}
	if !strings.Contains(got, "❌ Failed to recreate 'Verified Warden External'") {
		t.Fatalf("expected the second role to be reported failed, got %q", got)
	}
	// The old External role must NOT be deleted when its recreation failed.
	if gm.countCalls("GuildRoleDelete") != 1 {
		t.Fatalf("expected exactly 1 delete (only the successful role), got %d (%v)", gm.countCalls("GuildRoleDelete"), gm.Calls())
	}
}

// --- purge: guild not accessible (channels fetch fails) ---

func TestRunWardenPurge_GuildChannelsErrorSurfaces(t *testing.T) {
	noOverwriteDelay(t)
	gm := &fakeGuildManager{
		roles:        []*discordgo.Role{wardenRole("old-int", wardenRoleBaseName+" Internal")},
		ChannelsErrs: []error{fmt.Errorf("missing access")},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1")

	runWardenPurge(f, gm, i, "guild-1", "internal")

	if gm.countCalls("GuildRoleCreate") != 0 {
		t.Fatalf("must not recreate roles when channels are inaccessible; got %v", gm.Calls())
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "❌ Failed to retrieve guild channels") {
		t.Fatalf("expected guild-channels error surfaced, got %q", got)
	}
}

func TestRunWardenPurge_RoleNotFoundSurfaces(t *testing.T) {
	noOverwriteDelay(t)
	// No matching warden role in the guild -> resolveWardenRoleIDs errors out
	// before any mutation.
	gm := &fakeGuildManager{roles: []*discordgo.Role{wardenRole("x", "Some Other Role")}}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1")

	runWardenPurge(f, gm, i, "guild-1", "internal")

	if gm.countCalls("GuildChannels") != 0 {
		t.Fatalf("must short-circuit before fetching channels; got %v", gm.Calls())
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "role not found in guild") {
		t.Fatalf("expected role-not-found error, got %q", got)
	}
}

// --- remove + bulkadd coverage ---

func TestRunWarden_RemoveSuccess(t *testing.T) {
	gm := &fakeGuildManager{
		roles:       []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		membersByID: map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "remove"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)

	runWarden(f, gm, i)

	if gm.countCalls("GuildMemberRoleRemove") != 1 {
		t.Fatalf("expected 1 GuildMemberRoleRemove, got %v", gm.Calls())
	}
	if got := lastEditContent(f.Calls()); !strings.Contains(got, "✅ Removed warden role(s)") {
		t.Fatalf("expected remove success, got %q", got)
	}
}

func TestRunWarden_AddRoleAddFailureSurfaces(t *testing.T) {
	gm := &fakeGuildManager{
		roles:             []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		membersByID:       map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
		MemberRoleAddErrs: []error{fmt.Errorf("forbidden")},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "add"),
		stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	)

	runWarden(f, gm, i)

	// A plain (non-REST) error is a transport-class fault: the reply names the
	// role and surfaces the failure, but never the raw error text.
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Could not add 'Verified Warden Internal'") {
		t.Fatalf("expected role-add failure surfaced, got %q", got)
	}
	if strings.Contains(got, "forbidden") {
		t.Fatalf("reply must not leak the raw error text, got %q", got)
	}
}

func TestRunWarden_BulkAddMixedResults(t *testing.T) {
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		searchResults: map[string][]*discordgo.Member{
			"good": {{User: &discordgo.User{ID: "111", Username: "good"}}},
			// "bad" has no search result -> findGuildMember returns not-found.
		},
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", "good, bad"),
	)

	runWarden(f, gm, i)

	got := lastEditContent(f.Calls())
	if !strings.Contains(got, "Added warden role(s) to 1 user(s)") {
		t.Fatalf("expected 1 success in summary, got %q", got)
	}
	if !strings.Contains(got, "No member found matching 'bad'") {
		t.Fatalf("expected 'bad' reported as failure, got %q", got)
	}
}

func TestRunWarden_MissingDiscordnameForAdd(t *testing.T) {
	gm := &fakeGuildManager{}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "add"),
		stringOption("flag", "internal"),
	)

	runWarden(f, gm, i)

	if len(gm.Calls()) != 0 {
		t.Fatalf("missing discordname must short-circuit before guild calls; got %v", gm.Calls())
	}
	if len(f.Calls()) == 0 {
		t.Fatal("expected an error response for missing discordname")
	}
}

// CAVBOT2-6: a name search longer than Discord's 100-char query limit must be
// rejected locally, not forwarded to GuildMembersSearch (which 400s with
// "Invalid Form Body" and gets captured to Sentry as an error).
func TestFindGuildMember_OverLengthQueryRejectedBeforeSearch(t *testing.T) {
	gm := &fakeGuildManager{}
	longQuery := strings.Repeat("a", 101)

	_, err := findGuildMember(gm, "guild-1", longQuery)
	if err == nil {
		t.Fatal("expected an error for an over-length query")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Fatalf("expected an over-length rejection message, got %q", err.Error())
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("over-length query must be rejected before hitting Discord; got calls %v", gm.Calls())
	}
}

// A query at exactly Discord's 100-char limit is valid and must still reach the
// name search. Pins the boundary so a future >=100 off-by-one is caught.
func TestFindGuildMember_MaxLengthQueryStillSearches(t *testing.T) {
	gm := &fakeGuildManager{}
	maxQuery := strings.Repeat("a", 100)

	_, _ = findGuildMember(gm, "guild-1", maxQuery)

	if gm.countCalls("GuildMembersSearch") != 1 {
		t.Fatalf("a 100-char query is valid and must reach search; got calls %v", gm.Calls())
	}
}

// A multibyte name under 100 runes can exceed 100 bytes. The guard counts runes,
// so it must reach search. This is the case that justifies utf8.RuneCountInString
// over len() and would fail if the guard regressed to byte counting.
func TestFindGuildMember_MultibyteNameUnderLimitStillSearches(t *testing.T) {
	gm := &fakeGuildManager{}
	multibyte := strings.Repeat("世", 80) // 80 runes, 240 bytes

	_, _ = findGuildMember(gm, "guild-1", multibyte)

	if gm.countCalls("GuildMembersSearch") != 1 {
		t.Fatalf("a sub-limit multibyte name must reach search; got calls %v", gm.Calls())
	}
}

// The length check runs on the trimmed query, so whitespace padding around a
// 100-char core must not push it over the limit.
func TestFindGuildMember_WhitespacePaddedMaxLengthStillSearches(t *testing.T) {
	gm := &fakeGuildManager{}
	padded := "  " + strings.Repeat("a", 100) + "  "

	_, _ = findGuildMember(gm, "guild-1", padded)

	if gm.countCalls("GuildMembersSearch") != 1 {
		t.Fatalf("trimmed 100-char query must reach search; got calls %v", gm.Calls())
	}
}
