package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

const testGuildID = "guild-1"

// fakeDiscord is the manager fake beneath the panel: the gateway state the
// panel reads the guild from, the create and edit calls a save makes, and
// the create, move and delete calls the runtime makes when a join spawns.
// Mutex-guarded because the runtime is driven from the test goroutine while
// the suite runs under -race.
type fakeDiscord struct {
	mu sync.Mutex
	// channels are the guild's channels as the fake gateway state holds
	// them. A create or an edit changes them at once, as the production
	// adapter puts Discord's reply in the state.
	channels []*discordgo.Channel
	// apiReads counts the reads made of Discord's API, each of which fails:
	// the panel reads the guild and its members from the gateway state.
	apiReads int
	// apiWrites counts the writes made through Discord's API, every
	// channel, member, permission and message call among them, whether or
	// not the fake records the call's details too. A write call added to the
	// fake, a role grant among them, counts here too:
	// TestApprovalsSavesMakeNoDiscordCall holds the approvals saves to none.
	apiWrites int
	// createErr, when set, is what every create returns.
	createErr error
	// editErr, when set, is what every edit returns.
	editErr error
	// duringWrite, when set, runs inside every channel create and edit,
	// after Discord has made the change and before it answers: the moment
	// a test has the browser leave during a Discord write.
	duringWrite func()
	// premiumTier is the boost tier the fake gateway state holds.
	premiumTier discordgo.PremiumTier
	created     []fakeCreate
	edited      []fakeEdit
	deleted     []string
	spawned     int
	// voice stands in for the state cache's voice states: user ID to
	// channel ID, connected members only.
	voice map[string]string
	// guildStatus is how much of the guild the fake gateway state holds:
	// present unless a test takes it away or leaves the READY placeholder.
	guildStatus commands.GuildDataStatus
	// placeholderRead closes at the first read that finds the placeholder,
	// once holdPlaceholder has armed it.
	placeholderRead chan struct{}
	// disconnected is the bot's gateway connection down, with the fake
	// state still holding what it held when the connection dropped.
	disconnected bool
	// roles are the guild's roles as the fake gateway state holds them.
	roles []*discordgo.Role
	// memberList is the member list snapshot the fake gateway state gives:
	// complete, with the bot its one member, until a test sets one, as the
	// production adapter gives it beside a guild whose data the state holds.
	memberList commands.MemberListSnapshot
	// partialListRead closes at the first read that finds the member list
	// partial, once holdPartialList has armed it.
	partialListRead chan struct{}
	// roleWrites are the member role changes made, in order.
	roleWrites []fakeRoleWrite
	// roleErrs holds what a role change answers for the member with the
	// ID, which then keeps their roles as they were.
	roleErrs map[string]error
	// duringRoleWrite, when set, runs inside every member role change,
	// after Discord has made it and before it answers, with the fake's lock
	// released: the moment a test holds a Foxhole action mid-run.
	duringRoleWrite func(fakeRoleWrite)
}

// fakeRoleWrite is one member role change as the fake recorded it: the
// member, the role and the audit log reason.
type fakeRoleWrite struct {
	MemberID string
	RoleID   string
	Reason   string
}

// fakeEdit is one edit call as the fake recorded it: the channel, the name
// sent and the audit log reason.
type fakeEdit struct {
	ChannelID string
	Name      string
	Reason    string
}

// fakeCreate is one create call as the fake recorded it: the payload and
// the audit log reason.
type fakeCreate struct {
	Data   discordgo.GuildChannelCreateData
	Reason string
}

// testGuildRoles are the guild's roles as Discord returns them: the two
// eligible roles the moderator pickers offer, the managed role Discord made
// for a bot, and @everyone, whose ID is the guild's. A deleted role is any
// ID this list never had; the tests use role-gone.
var testGuildRoles = []*discordgo.Role{
	{ID: "role-mp", Name: "Military Police", Color: 0xebc729},
	{ID: "role-hq", Name: "Regimental HQ"},
	{ID: "role-bot", Name: "CavBot", Managed: true},
	{ID: testGuildID, Name: "@everyone"},
}

// newFakeDiscord returns a guild with one category holding a voice channel
// the tests register as a hub, a second voice channel, a text channel, and a
// voice channel with no parent outside it.
func newFakeDiscord() *fakeDiscord {
	return &fakeDiscord{channels: []*discordgo.Channel{
		{ID: "cat-1", Name: "Arma Reforger", Type: discordgo.ChannelTypeGuildCategory},
		{ID: "hub-1", Name: "Join to create", Type: discordgo.ChannelTypeGuildVoice, ParentID: "cat-1"},
		{ID: "vc-2", Name: "Squad Join", Type: discordgo.ChannelTypeGuildVoice, ParentID: "cat-1"},
		{ID: "text-1", Name: "general", Type: discordgo.ChannelTypeGuildText, ParentID: "cat-1"},
		{ID: "vc-noparent", Name: "Lobby", Type: discordgo.ChannelTypeGuildVoice},
	}, voice: map[string]string{}, guildStatus: commands.GuildDataPresent, roles: slices.Clone(testGuildRoles),
		memberList: commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
			Members: []commands.ListedMember{testBotMember}}}
}

// testBotMember is the bot as the member list holds it: a guild's member
// list always holds the bot.
var testBotMember = commands.ListedMember{ID: "200000000000000001", Username: "cavbot2"}

func (f *fakeDiscord) Channel(channelID string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.channels {
		if ch.ID == channelID {
			return ch, nil
		}
	}
	return nil, discordgo.ErrStateNotFound
}

// errAPIDown is what every read of Discord's API answers in the panel's
// tests. The panel reads the guild from the gateway state, so a page and a
// save work while the API is down.
var errAPIDown = errors.New("discord: 502 Bad Gateway")

// GuildChannelCreateComplex records the call and, as the production adapter
// does with Discord's reply, puts the new channel in the fake gateway state
// so the page a save redirects to reads it.
func (f *fakeDiscord) GuildChannelCreateComplex(_ string, data discordgo.GuildChannelCreateData, reason string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiWrites++
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, fakeCreate{Data: data, Reason: reason})
	f.spawned++
	ch := &discordgo.Channel{ID: fmt.Sprintf("spawn-%d", f.spawned), Name: data.Name, Type: data.Type, ParentID: data.ParentID}
	f.channels = append(f.channels, ch)
	if f.duringWrite != nil {
		f.duringWrite()
	}
	return ch, nil
}

// ChannelDelete records the call and takes the channel out of the guild's
// list.
func (f *fakeDiscord) ChannelDelete(channelID, _ string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiWrites++
	f.deleted = append(f.deleted, channelID)
	f.channels = slices.DeleteFunc(f.channels, func(ch *discordgo.Channel) bool { return ch.ID == channelID })
	return &discordgo.Channel{ID: channelID}, nil
}

// ChannelEdit records the call and, as the production adapter does with
// Discord's reply, renames the channel in the fake gateway state. An edit
// with no name keeps the name, since the request leaves an empty name out.
func (f *fakeDiscord) ChannelEdit(channelID string, data *discordgo.ChannelEdit, reason string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiWrites++
	if f.editErr != nil {
		return nil, f.editErr
	}
	f.edited = append(f.edited, fakeEdit{ChannelID: channelID, Name: data.Name, Reason: reason})
	for _, ch := range f.channels {
		if ch.ID == channelID && data.Name != "" {
			ch.Name = data.Name
		}
	}
	if f.duringWrite != nil {
		f.duringWrite()
	}
	return &discordgo.Channel{ID: channelID, Name: data.Name}, nil
}

// ChannelOverwritesReplace records the call as an edit with no name, or
// returns editErr. The panel's tests judge no overwrite.
func (f *fakeDiscord) ChannelOverwritesReplace(channelID string, _ []*discordgo.PermissionOverwrite, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiWrites++
	if f.editErr != nil {
		return f.editErr
	}
	f.edited = append(f.edited, fakeEdit{ChannelID: channelID, Reason: reason})
	return nil
}

func (f *fakeDiscord) GuildMemberMove(_, _ string, _ *string) error {
	f.countWrite()
	return nil
}

func (f *fakeDiscord) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error) {
	f.countWrite()
	return &discordgo.Message{ChannelID: channelID, Content: data.Content}, nil
}

// countWrite counts a write through Discord's API that the fake records
// nothing else about.
func (f *fakeDiscord) countWrite() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiWrites++
}

// GuildMember is a read of Discord's API: counted, and answered as the API
// answers while it is down.
func (f *fakeDiscord) GuildMember(_, _ string) (*discordgo.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiReads++
	return nil, errAPIDown
}

// VoiceStates copies the fake cache: who is where, and the guild's
// channels. Any guild but the test guild is absent.
func (f *fakeDiscord) VoiceStates(guildID string) commands.VoiceSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if guildID != testGuildID {
		return commands.VoiceSnapshot{}
	}
	snap := commands.VoiceSnapshot{
		Present:       true,
		ChannelByUser: make(map[string]string, len(f.voice)),
		Channels:      make(map[string]struct{}, len(f.channels)),
	}
	for user, ch := range f.voice {
		snap.ChannelByUser[user] = ch
	}
	for _, ch := range f.channels {
		snap.Channels[ch.ID] = struct{}{}
	}
	return snap
}

// GuildData copies the fake cache's channels, the guild's roles and its
// boost tier, while the fake state holds the guild's data. Any other guild
// is absent.
func (f *fakeDiscord) GuildData(guildID string) commands.GuildSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if guildID != testGuildID {
		return commands.GuildSnapshot{}
	}
	if f.guildStatus == commands.GuildDataArriving && f.placeholderRead != nil {
		close(f.placeholderRead)
		f.placeholderRead = nil
	}
	if f.guildStatus != commands.GuildDataPresent {
		return commands.GuildSnapshot{Status: f.guildStatus}
	}
	data := commands.GuildSnapshot{Status: commands.GuildDataPresent, Connected: !f.disconnected, PremiumTier: f.premiumTier}
	for _, ch := range f.channels {
		c := *ch
		data.Channels = append(data.Channels, &c)
	}
	for _, r := range f.roles {
		role := *r
		data.Roles = append(data.Roles, &role)
	}
	return data
}

// MemberList copies the member list snapshot the test set, the members and
// their role IDs included while the list is complete, and none while it
// isn't, as the production adapter gives it. Any other guild has none.
func (f *fakeDiscord) MemberList(guildID string) commands.MemberListSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if guildID != testGuildID {
		return commands.MemberListSnapshot{}
	}
	if f.memberList.Status != commands.MemberListComplete && f.partialListRead != nil {
		close(f.partialListRead)
		f.partialListRead = nil
	}
	snap := f.memberList
	snap.Members = nil
	if snap.Status != commands.MemberListComplete {
		return snap
	}
	for _, m := range f.memberList.Members {
		m.RoleIDs = slices.Clone(m.RoleIDs)
		snap.Members = append(snap.Members, m)
	}
	return snap
}

// GuildMemberRoleAdd records the call and, as Discord's member update does
// in the gateway state, gives the member the role in the member list,
// unless roleErrs refuses it. duringRoleWrite runs before it answers.
func (f *fakeDiscord) GuildMemberRoleAdd(_, userID, roleID, reason string) error {
	return f.writeRole(userID, roleID, reason, func(roles []string) []string {
		if slices.Contains(roles, roleID) {
			return roles
		}
		return append(slices.Clone(roles), roleID)
	})
}

// GuildMemberRoleRemove records the call and, as Discord's member update
// does in the gateway state, takes the role off the member in the member
// list, unless roleErrs refuses it. duringRoleWrite runs before it answers.
func (f *fakeDiscord) GuildMemberRoleRemove(_, userID, roleID, reason string) error {
	return f.writeRole(userID, roleID, reason, func(roles []string) []string {
		return slices.DeleteFunc(slices.Clone(roles), func(id string) bool { return id == roleID })
	})
}

// writeRole records one member role change and, unless roleErrs refuses
// it, applies edit to the member's role IDs in the member list. It runs
// duringRoleWrite before it answers, with the fake's lock released.
func (f *fakeDiscord) writeRole(userID, roleID, reason string, edit func([]string) []string) error {
	f.mu.Lock()
	f.apiWrites++
	write := fakeRoleWrite{MemberID: userID, RoleID: roleID, Reason: reason}
	f.roleWrites = append(f.roleWrites, write)
	err := f.roleErrs[userID]
	if err == nil {
		for i, m := range f.memberList.Members {
			if m.ID == userID {
				f.memberList.Members[i].RoleIDs = edit(m.RoleIDs)
			}
		}
	}
	during := f.duringRoleWrite
	f.mu.Unlock()
	if during != nil {
		during(write)
	}
	return err
}

// editMember changes one member of the member list in place, as a change
// made by hand in Discord reaches the gateway state.
func (f *fakeDiscord) editMember(id string, edit func(*commands.ListedMember)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.memberList.Members {
		if f.memberList.Members[i].ID == id {
			edit(&f.memberList.Members[i])
		}
	}
}

// dropMember takes a member out of the member list, as their leaving the
// server does.
func (f *fakeDiscord) dropMember(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memberList.Members = slices.DeleteFunc(slices.Clone(f.memberList.Members), func(m commands.ListedMember) bool { return m.ID == id })
}

// holdersOf returns the IDs of the members of the member list holding the
// role, sorted.
func (f *fakeDiscord) holdersOf(roleID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.memberList.Members {
		if slices.Contains(m.RoleIDs, roleID) {
			out = append(out, m.ID)
		}
	}
	slices.Sort(out)
	return out
}

// roleChanges returns every member role change made, in order.
func (f *fakeDiscord) roleChanges() []fakeRoleWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.roleWrites)
}

// failRoleWrites makes every role change to the member answer err.
func (f *fakeDiscord) failRoleWrites(memberID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.roleErrs == nil {
		f.roleErrs = map[string]error{}
	}
	f.roleErrs[memberID] = err
}

func (f *fakeDiscord) setDuringRoleWrite(during func(fakeRoleWrite)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.duringRoleWrite = during
}

// setMemberList sets the member list snapshot the fake gateway state gives.
// The fake keeps its own copy, so a role change it makes never reaches the
// fixture members a test built the snapshot from.
func (f *fakeDiscord) setMemberList(snap commands.MemberListSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap.Members = slices.Clone(snap.Members)
	for i := range snap.Members {
		snap.Members[i].RoleIDs = slices.Clone(snap.Members[i].RoleIDs)
	}
	f.memberList = snap
}

// setListStatus changes how far the member list has arrived and whether
// the bot's gateway connection is up, keeping the members the fake holds,
// as the state keeps them through a resumed session.
func (f *fakeDiscord) setListStatus(status commands.MemberListStatus, connected bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memberList.Status, f.memberList.Connected = status, connected
}

// holdPartialList leaves a partial member list snapshot in the fake gateway
// state. The channel closes at the first read that finds it partial, and
// that read still returns it.
func (f *fakeDiscord) holdPartialList(snap commands.MemberListSnapshot) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memberList = snap
	f.partialListRead = make(chan struct{})
	return f.partialListRead
}

// removeRole takes a role out of the guild's role list, as deleting it in
// Discord does.
func (f *fakeDiscord) removeRole(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roles = slices.DeleteFunc(slices.Clone(f.roles), func(r *discordgo.Role) bool { return r.ID == id })
}

// addRoles puts roles in the guild's role list.
func (f *fakeDiscord) addRoles(roles ...*discordgo.Role) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roles = append(f.roles, roles...)
}

// MemberRanks reads the payload through the runtime's shared helper. The
// panel's tests never run a sweep; the fake carries it for the interface.
func (f *fakeDiscord) MemberRanks(g *discordgo.Guild) map[string]int {
	return commands.GuildMemberRanks(g)
}

// ChannelMessageEditComplex answers every edit as done. The panel's tests
// read no message the runtime posts or edits.
func (f *fakeDiscord) ChannelMessageEditComplex(edit *discordgo.MessageEdit) (*discordgo.Message, error) {
	f.countWrite()
	return &discordgo.Message{ID: edit.ID, ChannelID: edit.Channel}, nil
}

// setVoice puts a member in a channel in the fake cache, or disconnects
// them for an empty channel.
func (f *fakeDiscord) setVoice(userID, channelID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if channelID == "" {
		delete(f.voice, userID)
		return
	}
	f.voice[userID] = channelID
}

// Guild is a read of Discord's API: counted, and answered as the API
// answers while it is down.
func (f *fakeDiscord) Guild(_ string) (*discordgo.Guild, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiReads++
	return nil, errAPIDown
}

// apiReadCount is how many reads of Discord's API the panel has made.
func (f *fakeDiscord) apiReadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apiReads
}

// apiCallCount is how many calls to Discord's API, reads and writes, the
// panel and the runtime have made.
func (f *fakeDiscord) apiCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apiReads + f.apiWrites
}

// ChannelPermissionSet accepts every overwrite set. The panel's tests judge
// no channel's permissions; the fake carries it for the interface.
func (f *fakeDiscord) ChannelPermissionSet(_, _ string, _ discordgo.PermissionOverwriteType, _, _ int64, _ string) error {
	f.countWrite()
	return nil
}

// CanSeeChannel answers that everyone sees every channel. The panel's tests
// let nobody in; the fake carries it for the interface.
func (f *fakeDiscord) CanSeeChannel(_, _ string, _ []string) (bool, error) {
	return true, nil
}

// setGuild sets how much of the guild the fake gateway state holds.
func (f *fakeDiscord) setGuild(status commands.GuildDataStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guildStatus = status
}

// holdPlaceholder leaves the READY placeholder in the fake gateway state:
// the guild's data is on its way. The channel closes at the first read that
// finds it.
func (f *fakeDiscord) holdPlaceholder() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guildStatus = commands.GuildDataArriving
	f.placeholderRead = make(chan struct{})
	return f.placeholderRead
}

// setConnected brings the bot's gateway connection up or down. The fake
// state keeps what it holds either way.
func (f *fakeDiscord) setConnected(up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = !up
}

func (f *fakeDiscord) setDuringWrite(during func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.duringWrite = during
}

func (f *fakeDiscord) setEditErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.editErr = err
}

func (f *fakeDiscord) setPremiumTier(tier discordgo.PremiumTier) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.premiumTier = tier
}

// edits returns every edit call, in order.
func (f *fakeDiscord) edits() []fakeEdit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.edited)
}

func (f *fakeDiscord) setCreateErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createErr = err
}

func (f *fakeDiscord) createCount() int {
	return len(f.creates())
}

// creates returns every create payload sent, in order.
func (f *fakeDiscord) creates() []discordgo.GuildChannelCreateData {
	var out []discordgo.GuildChannelCreateData
	for _, c := range f.createCalls() {
		out = append(out, c.Data)
	}
	return out
}

// createCalls returns every create call, payload and reason, in order.
func (f *fakeDiscord) createCalls() []fakeCreate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.created)
}

// deletes returns the ID of every channel the runtime deleted, in order.
func (f *fakeDiscord) deletes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
}

// testWorld is the panel with everything beneath it: the fake forum, the
// store fake, the fake Discord and the real runtime built over both.
type testWorld struct {
	forum   *fakeForum
	st      *store.Fake
	discord *fakeDiscord
	runtime *commands.TempVC
	p       *Panel
	b       *browser
	// actionEnded receives once each time a Foxhole action's report has
	// been written as ended, in a world built over an endWatch.
	actionEnded <-chan struct{}
	// pause is the clock the world's Foxhole actions pause on, in a world
	// built by newFoxholeWorld.
	pause *pauseClock
}

// newTestWorld builds the panel over a store holding the given hubs. The
// hubs go in before the runtime is built, the way startup loads them.
func newTestWorld(t *testing.T, hubs ...store.Hub) *testWorld {
	t.Helper()
	return newTestWorldWith(t, newFakeForum(t), hubs...)
}

func newTestWorldWith(t *testing.T, f *fakeForum, hubs ...store.Hub) *testWorld {
	t.Helper()
	st := store.NewFake()
	for _, h := range hubs {
		if _, err := st.UpsertHub(context.Background(), h); err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
	}
	return newTestWorldOver(t, st, f)
}

// newTestWorldOver builds the panel and the runtime over any store. st on
// the returned world is the store when it is the fake, and nil for a store
// that wraps it to refuse writes: such a test reads back through the fake
// it wrapped.
func newTestWorldOver(t *testing.T, st store.Store, f *fakeForum) *testWorld {
	t.Helper()
	return newTestWorldConfigured(t, st, f, testConfig(f))
}

// newTestWorldConfigured is newTestWorldOver with the panel built from the
// configuration given.
func newTestWorldConfigured(t *testing.T, st store.Store, f *fakeForum, cfg Config) *testWorld {
	t.Helper()
	discord := newFakeDiscord()
	runtime, err := commands.NewTempVC(discord, st, testGuildID)
	if err != nil {
		t.Fatalf("NewTempVC: %v", err)
	}
	foxhole, err := commands.NewFoxholeRuntime(discord, discord, st, testGuildID)
	if err != nil {
		t.Fatalf("NewFoxholeRuntime: %v", err)
	}
	p, err := New(cfg, testVersion, Deps{Store: st, Runtime: runtime, Manager: discord, Foxhole: foxhole, GuildID: testGuildID})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake, _ := st.(*store.Fake)
	return &testWorld{forum: f, st: fake, discord: discord, runtime: runtime, p: p, b: newBrowser(t, p)}
}

// testHub is a stored hub on hub-1 with the defaults a register writes.
func testHub() store.Hub {
	return store.Hub{
		GuildID: testGuildID, HubChannelID: "hub-1", BaseString: "Arma Voice",
		PermissionSource: store.PermissionCategory, Bitrate: 64000, Enabled: true, RenamingAllowed: true,
	}
}

// join feeds the runtime the gateway event for a member joining a channel,
// applied to the fake cache first, the order discordgo keeps.
func (w *testWorld) join(userID, channelID string) {
	w.discord.setVoice(userID, channelID)
	w.runtime.HandleVoiceStateUpdate(&discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: testGuildID, UserID: userID, ChannelID: channelID, Member: &discordgo.Member{},
	}})
}

// storedHubs lists the store's hub rows for the test guild.
func storedHubs(t *testing.T, st store.Store) []store.Hub {
	t.Helper()
	hubs, err := st.ListHubs(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("ListHubs: %v", err)
	}
	return hubs
}

// storedHubID reads back the surrogate ID the store gave the hub on a channel.
func storedHubID(t *testing.T, st store.Store, hubChannelID string) int64 {
	t.Helper()
	for _, h := range storedHubs(t, st) {
		if h.HubChannelID == hubChannelID {
			return h.ID
		}
	}
	t.Fatalf("no hub stored on %s", hubChannelID)
	return 0
}

// fieldText returns the text under data-field=name inside n, and fails the
// test when the element is absent.
func fieldText(t *testing.T, n *html.Node, name string) string {
	t.Helper()
	el := findElement(n, "", "data-field", name)
	if el == nil {
		t.Errorf("no element under data-field=%q", name)
		return ""
	}
	return textOf(el)
}

func TestHubListShowsEachHubWithLiveSpawnedCount(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.join("user-a", "hub-1")

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	id := strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)
	row := findElement(parseHTML(t, res), "", "data-hub", id)
	if row == nil {
		t.Fatalf("page has no element under data-hub=%q", id)
	}
	for field, want := range map[string]string{
		"spawned":       "1",
		"base_string":   "Arma Voice",
		"channel_name":  "Join to create",
		"category_name": "Arma Reforger",
	} {
		if got := fieldText(t, row, field); got != want {
			t.Errorf("data-field=%s shows %q, want %q", field, got, want)
		}
	}
}

func registerForm(channelID, baseString string) url.Values {
	return url.Values{"hub_channel": {channelID}, "base_string": {baseString}}
}

func TestRegisterWritesRowAndReachesRuntime(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice"))

	assertRedirect(t, res, "/")
	hubs := storedHubs(t, w.st)
	if len(hubs) != 1 {
		t.Fatalf("stored %d hubs, want 1", len(hubs))
	}
	h := hubs[0]
	if h.HubChannelID != "vc-2" || h.BaseString != "Squad Voice" {
		t.Errorf("stored hub = %+v, want vc-2 with base string Squad Voice", h)
	}
	if h.PermissionSource != store.PermissionCategory || h.Bitrate != 64000 || !h.Enabled {
		t.Errorf("stored hub = %+v, want permission source category, bitrate 64000, enabled", h)
	}

	w.join("user-a", "vc-2")
	if n := w.discord.createCount(); n != 1 {
		t.Errorf("a join to the registered channel made %d creates, want 1", n)
	}
}

// errorField returns the data-error value on the page's refusal note, and
// whether one is present.
func errorField(t *testing.T, res *http.Response) (string, bool) {
	t.Helper()
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode {
			if _, ok := attrValue(n, "data-error"); ok {
				found = n
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(parseHTML(t, res))
	if found == nil {
		return "", false
	}
	return attrValue(found, "data-error")
}

// A hub the panel registers allows renaming, though the register form shows
// no box for it: every new hub starts that way (#360).
func TestRegisterStoresRenamingAllowed(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	assertRedirect(t, w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice")), "/")

	if hubs := storedHubs(t, w.st); len(hubs) != 1 || !hubs[0].RenamingAllowed {
		t.Errorf("stored hubs = %+v, want one with renaming allowed", hubs)
	}
}

func TestRegisterRefusesWithTheFieldNamedAndWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		form  url.Values
		field string
	}{
		{"no channel chosen", registerForm("", "Squad Voice"), "hub_channel"},
		{"a text channel", registerForm("text-1", "Chat Voice"), "hub_channel"},
		{"a channel not in the guild", registerForm("vc-elsewhere", "Squad Voice"), "hub_channel"},
		{"a voice channel with no parent", registerForm("vc-noparent", "Lobby Voice"), "hub_channel"},
		{"a channel that is already a hub", registerForm("hub-1", "Arma Voice"), "hub_channel"},
		{"an empty base string", registerForm("vc-2", "   "), "base_string"},
		{"a base string of 91 characters", registerForm("vc-2", strings.Repeat("x", 91)), "base_string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)

			res := w.b.postForm("/hubs", tc.form)

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != tc.field {
				t.Errorf("data-error = %q (present %v), want %q", field, ok, tc.field)
			}
			if hubs := storedHubs(t, w.st); len(hubs) != 1 || hubs[0].HubChannelID != "hub-1" {
				t.Errorf("stored hubs = %+v, want the one pre-stored hub only", hubs)
			}
		})
	}
}

// pickerRoot returns the picker under data-field=name on a parsed page, and
// fails the test when there is none.
func pickerRoot(t *testing.T, doc *html.Node, name string) *html.Node {
	t.Helper()
	root := findElement(doc, "", "data-field", name)
	if root == nil {
		t.Fatalf("page has no element under data-field=%s", name)
	}
	return root
}

// postedInputs returns the inputs named name under n that would post, in
// document order: every input carrying the name that is not disabled, and,
// for a checkbox or a radio, is checked. The input's type is not a
// contract; what the form posts is.
func postedInputs(n *html.Node, name string) []*html.Node {
	var out []*html.Node
	eachLiveElement(n, func(n *html.Node) {
		got, _ := attrValue(n, "name")
		if n.Data != "input" || got != name {
			return
		}
		if _, disabled := attrValue(n, "disabled"); disabled {
			return
		}
		typ, _ := attrValue(n, "type")
		if _, checked := attrValue(n, "checked"); (typ == "checkbox" || typ == "radio") && !checked {
			return
		}
		out = append(out, n)
	})
	return out
}

// postedControls returns the values the inputs named name under n would
// post, in document order.
func postedControls(n *html.Node, name string) []string {
	var out []string
	for _, in := range postedInputs(n, name) {
		value, _ := attrValue(in, "value")
		out = append(out, value)
	}
	return out
}

// searchRows returns the IDs a picker's search list offers under n, in
// document order: the data-option value of each row.
func searchRows(n *html.Node) []string {
	var out []string
	eachLiveElement(n, func(n *html.Node) {
		if id, ok := attrValue(n, "data-option"); ok {
			out = append(out, id)
		}
	})
	return out
}

func TestRegisterPickerOffersVoiceChannelsThatAreNotHubs(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.get("/")

	root := pickerRoot(t, parseHTML(t, res), "hub_channel")
	offered := searchRows(root)
	if !slices.Contains(offered, "vc-2") {
		t.Errorf("picker %v does not offer vc-2, a voice channel that is not a hub", offered)
	}
	if slices.Contains(offered, "hub-1") {
		t.Errorf("picker %v offers hub-1, which is a hub", offered)
	}
	if slices.Contains(offered, "text-1") {
		t.Errorf("picker %v offers text-1, a text channel", offered)
	}
	if got := textOf(root); !strings.Contains(got, "Arma Reforger") {
		t.Errorf("picker text %q does not name vc-2's category Arma Reforger", got)
	}
}

func TestRefusedRegisterKeepsTheChosenChannel(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/hubs", registerForm("vc-2", "   "))

	if !isClientError(res.StatusCode) {
		t.Fatalf("status = %d, want 4xx", res.StatusCode)
	}
	root := pickerRoot(t, parseHTML(t, res), "hub_channel")
	if got := postedControls(root, "hub_channel"); !slices.Equal(got, []string{"vc-2"}) {
		t.Errorf("the register picker posts %v after the refusal, want vc-2 alone", got)
	}
}

func TestRegisterWithoutSessionRedirectsToSigninAndWritesNothing(t *testing.T) {
	w := newTestWorld(t)

	res := w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice"))

	assertRedirect(t, res, "/signin")
	if hubs := storedHubs(t, w.st); len(hubs) != 0 {
		t.Errorf("signed-out register stored %+v, want nothing", hubs)
	}
}

// updateForm is the edit form of the hub on hub-1 as a page loaded now
// posts it: every field carrying the value testHub stores, hub-1's name in
// the fake guild as the name the form loaded, and the version the store
// holds for the hub now. A test changes one field and posts the rest
// unchanged.
func updateForm(t *testing.T, st store.Store) url.Values {
	t.Helper()
	return url.Values{
		"channel_name":         {"Join to create"},
		"loaded_channel_name":  {"Join to create"},
		"base_string":          {"Arma Voice"},
		"permission_source":    {"category"},
		"user_limit":           {"0"},
		"bitrate":              {"64000"},
		"delete_delay_minutes": {"0"},
		"enabled":              {"on"},
		"renaming_allowed":     {"on"},
		"version":              {storedVersion(t, st, "hub-1")},
	}
}

// storedVersion is the version the store holds for the hub on a channel,
// as a form loaded now carries it.
func storedVersion(t *testing.T, st store.Store, hubChannelID string) string {
	t.Helper()
	for _, h := range storedHubs(t, st) {
		if h.HubChannelID == hubChannelID {
			return strconv.FormatInt(h.Version, 10)
		}
	}
	t.Fatalf("no hub stored on %s", hubChannelID)
	return ""
}

// hubPath is the update route of the stored hub on a channel.
func hubPath(t *testing.T, st store.Store, hubChannelID string) string {
	t.Helper()
	return "/hubs/" + strconv.FormatInt(storedHubID(t, st, hubChannelID), 10)
}

func TestUpdateWritesTheRowAndReachesTheRuntime(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	form := updateForm(t, w.st)
	form.Set("base_string", "Bravo Voice")
	form.Set("permission_source", "hub_channel")
	form["moderator_roles"] = []string{"role-mp"}
	form.Set("user_limit", "5")
	form.Set("bitrate", "96000")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	assertRedirect(t, res, "/")
	hubs := storedHubs(t, w.st)
	if len(hubs) != 1 {
		t.Fatalf("stored %d hubs, want 1", len(hubs))
	}
	h := hubs[0]
	if h.BaseString != "Bravo Voice" || h.PermissionSource != store.PermissionHubChannel ||
		!slices.Equal(h.ModeratorRoleIDs, []string{"role-mp"}) || h.UserLimit != 5 || h.Bitrate != 96000 || !h.Enabled {
		t.Errorf("stored hub = %+v, want Bravo Voice, hub_channel, [role-mp], limit 5, bitrate 96000, enabled", h)
	}

	w.join("user-a", "hub-1")
	creates := w.discord.creates()
	if len(creates) != 1 {
		t.Fatalf("a join after the update made %d creates, want 1", len(creates))
	}
	if !strings.Contains(creates[0].Name, "Bravo Voice") || creates[0].UserLimit != 5 {
		t.Errorf("create payload = name %q, user limit %d; want the new base string and limit 5", creates[0].Name, creates[0].UserLimit)
	}
}

func TestDisabledHubStopsSpawningAtOnceAndKeepsItsSettings(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	path := hubPath(t, w.st, "hub-1")
	off := updateForm(t, w.st)
	off.Del("enabled")

	assertRedirect(t, w.b.postForm(path, off), "/")
	w.join("user-a", "hub-1")
	if n := w.discord.createCount(); n != 0 {
		t.Errorf("a join to the disabled hub made %d creates, want 0", n)
	}
	if h := storedHubs(t, w.st)[0]; h.Enabled || h.BaseString != "Arma Voice" {
		t.Errorf("stored hub = %+v, want disabled with base string Arma Voice kept", h)
	}

	assertRedirect(t, w.b.postForm(path, updateForm(t, w.st)), "/")
	w.join("user-b", "hub-1")
	if n := w.discord.createCount(); n != 1 {
		t.Errorf("a join to the re-enabled hub made %d creates in all, want 1", n)
	}
}

// A save that turns "Locking allowed" on reaches the runtime at once: the
// owner's lock, refused before the save, goes through after it. The change
// log entry records the setting's before and after, and the form shows it
// on, so the next save keeps it.
func TestLockingAllowedSaveReachesTheRuntimeAtOnce(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.joinAs("user-owner", "hub-1", testRankSGT)
	w.joinAs("user-owner", "spawn-1", testRankSGT)

	owner := commands.Invoker{UserID: "user-owner", Roles: []string{testRankSGT}}
	if _, err := w.runtime.Lock(owner); err == nil {
		t.Fatal("a lock on a hub without locking passed, want a refusal")
	}
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Fatalf("edits before the save = %+v, want none", edits)
	}

	id := storedHubID(t, w.st, "hub-1")
	form := updateForm(t, w.st)
	form.Set("locking_allowed", "on")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

	if _, err := w.runtime.Lock(owner); err != nil {
		t.Fatalf("a lock after the save was refused: %v", err)
	}
	if edits := w.discord.edits(); len(edits) != 1 || edits[0].ChannelID != "spawn-1" {
		t.Errorf("edits after the save = %+v, want one on spawn-1", edits)
	}
	entries := storedChangeLog(t, w.st, id)
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	if got := decodeDiff(t, entries[0])["locking_allowed"]; got.Before != false || got.After != true {
		t.Errorf("diff locking_allowed = %+v, want before false, after true", got)
	}
	sec := editSection(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)), id)
	if got := postedControls(sec, "locking_allowed"); len(got) != 1 {
		t.Errorf("the form posts locking_allowed %v, want it on as saved", got)
	}
}

// A save that turns "Renaming allowed" off reaches the runtime at once and
// leaves live channels as they are (#360). The edit form of a hub with
// renaming on shows the box ticked, so a save of another field keeps it. The
// owner renames their channel, a save unticks the box, and the owner's next
// rename is refused while the channel keeps the name it had: the save edits
// no live channel. The change log entry records the setting's before and
// after, and the form shows it off as saved, so the next save keeps it off.
func TestRenamingAllowedSaveReachesTheRuntimeAtOnce(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.joinAs("user-owner", "hub-1", testRankSGT)
	w.joinAs("user-owner", "spawn-1", testRankSGT)
	id := storedHubID(t, w.st, "hub-1")

	sec := editSection(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)), id)
	if got := postedControls(sec, "renaming_allowed"); len(got) != 1 {
		t.Errorf("the form of a hub with renaming on posts renaming_allowed %q, want it on", got)
	}
	owner := commands.Invoker{UserID: "user-owner", Roles: []string{testRankSGT}}
	if _, err := w.runtime.Rename(owner, "Alpha"); err != nil {
		t.Fatalf("a rename before the save was refused: %v", err)
	}

	form := updateForm(t, w.st)
	form.Del("renaming_allowed")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

	if h := storedHubs(t, w.st)[0]; h.RenamingAllowed {
		t.Error("the stored hub allows renaming after a save that turned it off")
	}
	if _, err := w.runtime.Rename(owner, "Bravo"); err == nil {
		t.Error("a rename after the save went through, want a refusal")
	}
	if edits := w.discord.edits(); len(edits) != 1 || edits[0].ChannelID != "spawn-1" {
		t.Errorf("edits = %+v, want the one rename on spawn-1 before the save", edits)
	}
	entries := storedChangeLog(t, w.st, id)
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	if got := decodeDiff(t, entries[0])["renaming_allowed"]; got.Before != true || got.After != false {
		t.Errorf("diff renaming_allowed = %+v, want before true, after false", got)
	}
	sec = editSection(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)), id)
	if got := postedControls(sec, "renaming_allowed"); len(got) != 0 {
		t.Errorf("the form after the save posts renaming_allowed %q, want it off as saved", got)
	}
}

// A save of the delete delay (#372) stores it, records it in the change
// log and reaches the runtime at once. The form of a hub with no delay
// posts 0, so a save of another field keeps it. After a save of 30 the form
// shows 30, and a channel that empties afterwards is not deleted: the
// runtime holds it for its 30 minutes, which the test never waits out.
func TestDeleteDelaySaveStoresItAndReachesTheRuntimeAtOnce(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")

	sec := editSection(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)), id)
	if got := postedControls(sec, "delete_delay_minutes"); !slices.Equal(got, []string{"0"}) {
		t.Errorf("the form of a hub with no delay posts delete_delay_minutes %q, want 0", got)
	}

	form := updateForm(t, w.st)
	form.Set("delete_delay_minutes", "30")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

	if h := storedHubs(t, w.st)[0]; h.DeleteDelayMinutes != 30 {
		t.Errorf("stored delete delay = %d, want 30", h.DeleteDelayMinutes)
	}
	entries := storedChangeLog(t, w.st, id)
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	if got := decodeDiff(t, entries[0])["delete_delay_minutes"]; got.Before != float64(0) || got.After != float64(30) {
		t.Errorf("diff delete_delay_minutes = %+v, want before 0, after 30", got)
	}
	sec = editSection(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)), id)
	if got := postedControls(sec, "delete_delay_minutes"); !slices.Equal(got, []string{"30"}) {
		t.Errorf("the form after the save posts delete_delay_minutes %q, want 30 as saved", got)
	}

	w.join("user-a", "hub-1")
	w.join("user-a", "spawn-1")
	w.join("user-a", "")
	if deleted := w.discord.deletes(); len(deleted) != 0 {
		t.Errorf("deleted %v when spawn-1 emptied after the save, want it kept for the delay", deleted)
	}
}

// The edit form saves the delete delay's bounds: 240, the most, and then 0
// on a hub that had a delay. The refusals just past them, -1 and 241, are
// rows of TestUpdateRefusesWithTheFieldNamedAndWritesNothing.
func TestDeleteDelaySavesItsBounds(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	for _, minutes := range []int{240, 0} {
		form := updateForm(t, w.st)
		form.Set("delete_delay_minutes", strconv.Itoa(minutes))

		assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

		if got := storedHubs(t, w.st)[0].DeleteDelayMinutes; got != minutes {
			t.Errorf("stored delete delay after saving %d = %d", minutes, got)
		}
	}
}

// A hub the panel creates or registers has no delete delay, though neither
// form shows the field: every new hub starts at 0 (#372 Q1).
func TestCreateAndRegisterStoreNoDeleteDelay(t *testing.T) {
	for name, form := range map[string]url.Values{
		"create":   createForm("cat-1", "Squad Join", "Squad Voice"),
		"register": registerForm("vc-2", "Squad Voice"),
	} {
		t.Run(name, func(t *testing.T) {
			w := newTestWorld(t)
			signIn(t, w.forum, w.b)

			assertRedirect(t, w.b.postForm("/hubs", form), "/")

			if hubs := storedHubs(t, w.st); len(hubs) != 1 || hubs[0].DeleteDelayMinutes != 0 {
				t.Errorf("stored hubs = %+v, want one with a delete delay of 0", hubs)
			}
		})
	}
}

// storedChangeLog lists the change log entries of a hub, newest first.
func storedChangeLog(t *testing.T, st store.Store, hubID int64) []store.ChangeLogEntry {
	t.Helper()
	entries, err := st.ListChangeLog(context.Background(), hubID, 10)
	if err != nil {
		t.Fatalf("ListChangeLog(%d): %v", hubID, err)
	}
	return entries
}

// sameHubSettings reports whether two hubs carry the same settings, ignoring
// the store-set times.
func sameHubSettings(a, b store.Hub) bool {
	return a.ID == b.ID && a.HubChannelID == b.HubChannelID && a.BaseString == b.BaseString &&
		a.PermissionSource == b.PermissionSource && slices.Equal(a.ModeratorRoleIDs, b.ModeratorRoleIDs) &&
		a.UserLimit == b.UserLimit && a.Bitrate == b.Bitrate && a.DeleteDelayMinutes == b.DeleteDelayMinutes &&
		a.Enabled == b.Enabled
}

func TestUpdateRefusesWithTheFieldNamedAndWritesNothing(t *testing.T) {
	set := func(field, value string) func(*testing.T, store.Store) url.Values {
		return func(t *testing.T, st store.Store) url.Values {
			form := updateForm(t, st)
			form.Set(field, value)
			return form
		}
	}
	cases := []struct {
		name  string
		form  func(*testing.T, store.Store) url.Values
		field string
	}{
		{"an empty channel name", set("channel_name", "   "), "channel_name"},
		{"a channel name of 101 characters", set("channel_name", strings.Repeat("x", 101)), "channel_name"},
		{"an empty base string", set("base_string", "   "), "base_string"},
		{"a base string of 91 characters", set("base_string", strings.Repeat("x", 91)), "base_string"},
		{"a permission source outside the two", set("permission_source", "other"), "permission_source"},
		{"a user limit below 0", set("user_limit", "-1"), "user_limit"},
		{"a user limit above 99", set("user_limit", "100"), "user_limit"},
		{"a user limit that is not a number", set("user_limit", "abc"), "user_limit"},
		{"a bitrate below 8000", set("bitrate", "7999"), "bitrate"},
		{"a bitrate that is not a number", set("bitrate", "abc"), "bitrate"},
		{"a delete delay below 0", set("delete_delay_minutes", "-1"), "delete_delay_minutes"},
		{"a delete delay above 240", set("delete_delay_minutes", "241"), "delete_delay_minutes"},
		{"an empty delete delay", set("delete_delay_minutes", ""), "delete_delay_minutes"},
		{"a moderator role not in the guild", set("moderator_roles", "role-elsewhere"), "moderator_roles"},
		{"a managed moderator role", set("moderator_roles", "role-bot"), "moderator_roles"},
		{"@everyone as a moderator role", set("moderator_roles", testGuildID), "moderator_roles"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			before := storedHubs(t, w.st)[0]

			res := w.b.postForm(hubPath(t, w.st, "hub-1"), tc.form(t, w.st))

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != tc.field {
				t.Errorf("data-error = %q (present %v), want %q", field, ok, tc.field)
			}
			if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
				t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
			}
			if entries := storedChangeLog(t, w.st, before.ID); len(entries) != 0 {
				t.Errorf("a refused update appended %d change log entries, want 0", len(entries))
			}
			w.join("user-a", "hub-1")
			creates := w.discord.creates()
			if len(creates) != 1 || !strings.Contains(creates[0].Name, "Arma Voice") || creates[0].UserLimit != 0 || creates[0].Bitrate != 64000 {
				t.Errorf("a join after the refusal made creates %+v, want one with the stored settings", creates)
			}
		})
	}
}

func TestUnknownHubIsNotFound(t *testing.T) {
	for _, path := range []string{"/hubs/999", "/hubs/999/remove"} {
		t.Run(path, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)

			res := w.b.postForm(path, updateForm(t, w.st))

			if res.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", res.StatusCode)
			}
			if hubs := storedHubs(t, w.st); len(hubs) != 1 || hubs[0].BaseString != "Arma Voice" {
				t.Errorf("stored hubs = %+v, want the one pre-stored hub unchanged", hubs)
			}
			if entries := storedChangeLog(t, w.st, 0); len(entries) != 0 {
				t.Errorf("appended %d entries under no hub, want 0", len(entries))
			}
		})
	}
}

func TestRemoveDeletesTheRowLeavesTheChannelAndStopsSpawning(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.join("user-a", "hub-1")
	if n := w.discord.createCount(); n != 1 {
		t.Fatalf("the first join made %d creates, want 1", n)
	}

	res := w.b.postForm(hubPath(t, w.st, "hub-1")+"/remove", nil)

	assertRedirect(t, res, "/")
	if hubs := storedHubs(t, w.st); len(hubs) != 0 {
		t.Errorf("stored hubs after remove = %+v, want none", hubs)
	}
	if deleted := w.discord.deletes(); len(deleted) != 0 {
		t.Errorf("remove deleted channels %v, want none: the hub channel and its spawned channels stay", deleted)
	}
	rows, err := w.st.ListSpawnedChannels(context.Background())
	if err != nil {
		t.Fatalf("ListSpawnedChannels: %v", err)
	}
	if len(rows) != 1 || rows[0].ChannelID != "spawn-1" {
		t.Errorf("spawned rows after remove = %+v, want spawn-1 kept", rows)
	}
	w.join("user-b", "hub-1")
	if n := w.discord.createCount(); n != 1 {
		t.Errorf("a join after remove made %d creates in all, want 1: nothing spawns from a removed hub", n)
	}
}

// fieldChange is one field of a diff as a reader decodes it: the values are
// whatever JSON carried, null included.
type fieldChange struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// decodeDiff decodes an entry's diff the way the page does.
func decodeDiff(t *testing.T, e store.ChangeLogEntry) map[string]fieldChange {
	t.Helper()
	var diff map[string]fieldChange
	if err := json.Unmarshal(e.Diff, &diff); err != nil {
		t.Fatalf("decode diff %s: %v", e.Diff, err)
	}
	return diff
}

// wantDiffFields are the fields the specs (#285, #347, #360) say a register
// or remove entry carries, written out here so the test does not read the
// list from the code.
var wantDiffFields = []string{"hub_channel", "base_string", "permission_source", "moderator_roles", "user_limit", "bitrate", "enabled", "renaming_allowed", "locking_allowed"}

// assertActor checks an entry names the signed-in test user.
func assertActor(t *testing.T, e store.ChangeLogEntry, action store.ChangeAction) {
	t.Helper()
	if e.Action != action || e.ForumUserID != testUserID || e.ForumUsername != testUsername {
		t.Errorf("entry = action %q by %d %q, want %q by %d %q", e.Action, e.ForumUserID, e.ForumUsername, action, testUserID, testUsername)
	}
}

func TestUpdateAppendsAnEntryWithTheChangedFieldsOnly(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	form := updateForm(t, w.st)
	form.Set("base_string", "Bravo Voice")

	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

	entries := storedChangeLog(t, w.st, storedHubID(t, w.st, "hub-1"))
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeUpdate)
	diff := decodeDiff(t, entries[0])
	if len(diff) != 1 {
		t.Errorf("diff has keys %v, want base_string alone", slices.Sorted(maps.Keys(diff)))
	}
	if got := diff["base_string"]; got.Before != "Arma Voice" || got.After != "Bravo Voice" {
		t.Errorf("diff base_string = %+v, want before Arma Voice, after Bravo Voice", got)
	}
}

func TestRegisterAppendsAnEntryWithNullBefore(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	assertRedirect(t, w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice")), "/")

	entries := storedChangeLog(t, w.st, storedHubID(t, w.st, "vc-2"))
	if len(entries) != 1 {
		t.Fatalf("the new hub has %d entries, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeRegister)
	diff := decodeDiff(t, entries[0])
	for _, field := range wantDiffFields {
		c, ok := diff[field]
		if !ok {
			t.Errorf("diff lacks %s", field)
			continue
		}
		if c.Before != nil {
			t.Errorf("diff %s before = %v, want null", field, c.Before)
		}
	}
	if diff["base_string"].After != "Squad Voice" || diff["hub_channel"].After != "vc-2" {
		t.Errorf("diff after: base_string %v, hub_channel %v; want Squad Voice and vc-2", diff["base_string"].After, diff["hub_channel"].After)
	}
}

func TestRemoveAppendsAnEntryWithNullAfter(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1")+"/remove", nil), "/")

	entries := storedChangeLog(t, w.st, 0)
	if len(entries) != 1 {
		t.Fatalf("%d entries under no hub, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeRemove)
	diff := decodeDiff(t, entries[0])
	for _, field := range wantDiffFields {
		c, ok := diff[field]
		if !ok {
			t.Errorf("diff lacks %s", field)
			continue
		}
		if c.After != nil {
			t.Errorf("diff %s after = %v, want null", field, c.After)
		}
	}
	if diff["base_string"].Before != "Arma Voice" {
		t.Errorf("diff base_string before = %v, want Arma Voice", diff["base_string"].Before)
	}
}

// editSection returns the edit area of one hub on the page: the section
// under data-hub=id, distinct from the list row that carries the same ID.
func editSection(t *testing.T, res *http.Response, hubID int64) *html.Node {
	t.Helper()
	return hubSection(t, parseHTML(t, res), hubID)
}

// entryIDs returns the data-entry values under n, in document order.
func entryIDs(t *testing.T, n *html.Node) []int64 {
	t.Helper()
	var ids []int64
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if raw, ok := attrValue(n, "data-entry"); ok {
				id, err := strconv.ParseInt(raw, 10, 64)
				if err != nil {
					t.Fatalf("data-entry %q is not an ID", raw)
				}
				ids = append(ids, id)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return ids
}

func TestHubFormShowsTheLastTenEntriesNewestFirst(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	for i := 1; i <= 11; i++ {
		form := updateForm(t, w.st)
		form.Set("user_limit", strconv.Itoa(i))
		assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")
	}

	res := w.b.get("/?hub=" + strconv.FormatInt(id, 10))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
	}
	sec := editSection(t, res, id)
	shown := entryIDs(t, sec)
	var want []int64
	for _, e := range storedChangeLog(t, w.st, id) {
		want = append(want, e.ID)
	}
	if len(want) != 10 || !slices.Equal(shown, want) {
		t.Fatalf("page shows entries %v, want the store's last ten in its order %v", shown, want)
	}
	first := findElement(sec, "", "data-entry", strconv.FormatInt(shown[0], 10))
	if action, _ := attrValue(first, "data-action"); action != "update" {
		t.Errorf("first entry data-action = %q, want update", action)
	}
	if findElement(first, "", "data-change", "user_limit") == nil {
		t.Error("first entry has no data-change=user_limit element")
	}
	if got := fieldText(t, first, "username"); got != testUsername {
		t.Errorf("first entry username = %q, want %q", got, testUsername)
	}
	at := findElement(first, "", "data-field", "at")
	if at == nil {
		t.Fatal("first entry has no data-field=at element")
	}
	if raw, _ := attrValue(at, "datetime"); raw == "" {
		t.Error("first entry's time carries no datetime attribute")
	} else if _, err := time.Parse(time.RFC3339, raw); err != nil {
		t.Errorf("first entry datetime %q is not RFC 3339: %v", raw, err)
	}
}

func TestUpdateAndRemoveWithoutSessionRedirectToSigninAndWriteNothing(t *testing.T) {
	for _, suffix := range []string{"", "/remove"} {
		t.Run("POST /hubs/{id}"+suffix, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			before := storedHubs(t, w.st)[0]
			form := updateForm(t, w.st)
			form.Set("base_string", "Bravo Voice")

			res := w.b.postForm(hubPath(t, w.st, "hub-1")+suffix, form)

			assertRedirect(t, res, "/signin")
			if hubs := storedHubs(t, w.st); len(hubs) != 1 || !sameHubSettings(hubs[0], before) {
				t.Errorf("stored hubs = %+v, want the one hub unchanged", hubs)
			}
			if n := len(storedChangeLog(t, w.st, before.ID)) + len(storedChangeLog(t, w.st, 0)); n != 0 {
				t.Errorf("a signed-out post appended %d entries, want 0", n)
			}
		})
	}
}

// createForm is the create form as posted: a category, the new hub
// channel's name and the base string.
func createForm(categoryID, channelName, baseString string) url.Values {
	return url.Values{"category": {categoryID}, "channel_name": {channelName}, "base_string": {baseString}}
}

func TestCreateMakesAChannelUnderTheCategoryAndWritesARow(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice"))

	assertRedirect(t, res, "/")
	calls := w.discord.createCalls()
	if len(calls) != 1 {
		t.Fatalf("made %d creates, want 1", len(calls))
	}
	data := calls[0].Data
	if data.Type != discordgo.ChannelTypeGuildVoice || data.ParentID != "cat-1" || data.Name != "Squad Join" {
		t.Errorf("create payload = type %d, parent %q, name %q; want voice under cat-1 named Squad Join", data.Type, data.ParentID, data.Name)
	}
	if len(data.PermissionOverwrites) != 0 {
		t.Errorf("create payload carries %d overwrites, want none so the channel takes the category's permissions", len(data.PermissionOverwrites))
	}
	if !strings.Contains(calls[0].Reason, testUsername) {
		t.Errorf("create audit reason %q does not name the panel user %q", calls[0].Reason, testUsername)
	}
	hubs := storedHubs(t, w.st)
	if len(hubs) != 1 {
		t.Fatalf("stored %d hubs, want 1", len(hubs))
	}
	h := hubs[0]
	if h.HubChannelID != "spawn-1" || h.BaseString != "Squad Voice" {
		t.Errorf("stored hub = %+v, want the created channel spawn-1 with base string Squad Voice", h)
	}
	if h.PermissionSource != store.PermissionCategory || h.UserLimit != 0 || h.Bitrate != 64000 || !h.Enabled {
		t.Errorf("stored hub = %+v, want permission source category, user limit 0, bitrate 64000, enabled", h)
	}

	w.join("user-a", "spawn-1")
	if n := w.discord.createCount(); n != 2 {
		t.Errorf("a join to the created hub made %d creates in all, want 2: the hub channel and one spawn", n)
	}
}

func TestCreateAppendsAnEntryWithNullBefore(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	assertRedirect(t, w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice")), "/")

	entries := storedChangeLog(t, w.st, storedHubID(t, w.st, "spawn-1"))
	if len(entries) != 1 {
		t.Fatalf("the new hub has %d entries, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeCreate)
	diff := decodeDiff(t, entries[0])
	for _, field := range wantDiffFields {
		c, ok := diff[field]
		if !ok {
			t.Errorf("diff lacks %s", field)
			continue
		}
		if c.Before != nil {
			t.Errorf("diff %s before = %v, want null", field, c.Before)
		}
	}
	if diff["base_string"].After != "Squad Voice" || diff["hub_channel"].After != "spawn-1" {
		t.Errorf("diff after: base_string %v, hub_channel %v; want Squad Voice and spawn-1", diff["base_string"].After, diff["hub_channel"].After)
	}
	if c := diff["channel_name"]; c.Before != nil || c.After != "Squad Join" {
		t.Errorf("diff channel_name = %+v, want before null, after Squad Join", c)
	}
}

func TestCreateRefusesWithTheFieldNamedAndWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		form  url.Values
		field string
	}{
		// An unpicked select posts the key with no value, and the key is
		// what tells a create from a register.
		{"an unpicked category still lands on create", createForm("", "Squad Join", "Squad Voice"), "category"},
		{"a category not in the guild", createForm("cat-elsewhere", "Squad Join", "Squad Voice"), "category"},
		{"a voice channel as the category", createForm("vc-2", "Squad Join", "Squad Voice"), "category"},
		{"an empty channel name", createForm("cat-1", "   ", "Squad Voice"), "channel_name"},
		{"a channel name of 101 characters", createForm("cat-1", strings.Repeat("x", 101), "Squad Voice"), "channel_name"},
		{"an empty base string", createForm("cat-1", "Squad Join", "   "), "base_string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			signIn(t, w.forum, w.b)

			res := w.b.postForm("/hubs", tc.form)

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != tc.field {
				t.Errorf("data-error = %q (present %v), want %q", field, ok, tc.field)
			}
			if n := w.discord.createCount(); n != 0 {
				t.Errorf("a refused create made %d channels, want 0", n)
			}
			if hubs := storedHubs(t, w.st); len(hubs) != 0 {
				t.Errorf("stored hubs = %+v, want none", hubs)
			}
			if entries := storedChangeLog(t, w.st, 0); len(entries) != 0 {
				t.Errorf("a refused create appended %d entries, want 0", len(entries))
			}
		})
	}
}

// A create whose store write fails, the database gone away between the
// channel create and the row write, deletes the channel it made.
func TestCreateDeletesTheChannelWhenTheRowWriteFails(t *testing.T) {
	st := store.NewFake()
	w := newTestWorldOver(t, refusingStore{st}, newFakeForum(t))
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice"))

	if !isServerError(res.StatusCode) {
		t.Errorf("status = %d, want 5xx", res.StatusCode)
	}
	calls := w.discord.createCalls()
	if len(calls) != 1 {
		t.Fatalf("made %d creates, want 1", len(calls))
	}
	if deleted := w.discord.deletes(); !slices.Contains(deleted, "spawn-1") {
		t.Errorf("deleted channels %v, want the created channel spawn-1 among them", deleted)
	}
	if entries := storedChangeLog(t, st, 0); len(entries) != 0 {
		t.Errorf("a failed create appended %d entries, want 0", len(entries))
	}
}

// restError is a Discord REST error with the given status and no body, the
// shape discordgo returns for a refused call.
func restError(status int) error {
	return &discordgo.RESTError{Response: &http.Response{StatusCode: status}, Message: &discordgo.APIErrorMessage{}}
}

// rateLimitError is the error discordgo returns on a 429 when the call never
// lets it retry, as every panel call does: a *RateLimitError with both
// embedded pointers set, not a *RESTError.
func rateLimitError(retryAfter time.Duration) error {
	return &discordgo.RateLimitError{RateLimit: &discordgo.RateLimit{
		TooManyRequests: &discordgo.TooManyRequests{Message: "You are being rate limited.", RetryAfter: retryAfter},
		URL:             "https://discord.com/api/v9/channels/1549294658339868692",
	}}
}

func TestCreateRefusedByDiscordShowsWhyAndWritesNothing(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)
	w.discord.setCreateErr(restError(http.StatusForbidden))

	res := w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice"))

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	if field, ok := errorField(t, res); !ok || field != "category" {
		t.Errorf("data-error = %q (present %v), want category: the cap and the bot's view are the category's", field, ok)
	}
	if hubs := storedHubs(t, w.st); len(hubs) != 0 {
		t.Errorf("stored hubs = %+v, want none", hubs)
	}
	if entries := storedChangeLog(t, w.st, 0); len(entries) != 0 {
		t.Errorf("a refused create appended %d entries, want 0", len(entries))
	}
}

// inputValue returns the value attribute of the input under data-field=name
// inside n, and fails the test when the input is absent.
func inputValue(t *testing.T, n *html.Node, name string) string {
	t.Helper()
	el := findElement(n, "input", "data-field", name)
	if el == nil {
		t.Errorf("no input under data-field=%q", name)
		return ""
	}
	v, _ := attrValue(el, "value")
	return v
}

func TestChangedHubChannelNameRenamesTheChannelNamingThePanelUser(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	if got := inputValue(t, editSection(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)), id), "channel_name"); got != "Join to create" {
		t.Errorf("the edit form's channel name input holds %q, want the live name Join to create", got)
	}
	form := updateForm(t, w.st)
	form.Set("channel_name", "Join here")
	form.Set("base_string", "Bravo Voice")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	assertRedirect(t, res, "/")
	edits := w.discord.edits()
	if len(edits) != 1 {
		t.Fatalf("made %d edits, want 1", len(edits))
	}
	if edits[0].ChannelID != "hub-1" || edits[0].Name != "Join here" {
		t.Errorf("edit = %+v, want hub-1 renamed to Join here", edits[0])
	}
	if !strings.Contains(edits[0].Reason, testUsername) {
		t.Errorf("edit audit reason %q does not name the panel user %q", edits[0].Reason, testUsername)
	}
	if h := storedHubs(t, w.st)[0]; h.BaseString != "Bravo Voice" {
		t.Errorf("stored base string = %q, want Bravo Voice", h.BaseString)
	}
	entries := storedChangeLog(t, w.st, id)
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	if c := decodeDiff(t, entries[0])["channel_name"]; c.Before != "Join to create" || c.After != "Join here" {
		t.Errorf("diff channel_name = %+v, want before Join to create, after Join here", c)
	}
}

func TestUnchangedHubChannelNameMakesNoEditCall(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	form := updateForm(t, w.st)
	form.Set("base_string", "Bravo Voice")

	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("an unchanged name made edits %+v, want none", edits)
	}
	if h := storedHubs(t, w.st)[0]; h.BaseString != "Bravo Voice" {
		t.Errorf("stored base string = %q, want Bravo Voice", h.BaseString)
	}
}

func TestRefusedHubChannelRenameWritesNothingAndShowsWhy(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setEditErr(restError(http.StatusForbidden))
	before := storedHubs(t, w.st)[0]
	form := updateForm(t, w.st)
	form.Set("channel_name", "Join here")
	form.Set("base_string", "Bravo Voice")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	if field, ok := errorField(t, res); !ok || field != "channel_name" {
		t.Errorf("data-error = %q (present %v), want channel_name", field, ok)
	}
	if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
		t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
	}
	if entries := storedChangeLog(t, w.st, before.ID); len(entries) != 0 {
		t.Errorf("a refused rename appended %d entries, want 0", len(entries))
	}
}

// A 429 on the rename is Discord's limit on channel renames, not a lost
// connection. The note names the wait in whole minutes (#340).
func TestRateLimitedHubChannelRenameShowsTheWait(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setEditErr(rateLimitError(7*time.Minute + 42*time.Second))
	before := storedHubs(t, w.st)[0]
	form := updateForm(t, w.st)
	form.Set("channel_name", "Join here")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	note := findElement(parseHTML(t, res), "", "data-error", "channel_name")
	if note == nil {
		t.Fatal("the page carries no data-error note on channel_name")
	}
	if text := textOf(note); !strings.Contains(text, "8 minutes") {
		t.Errorf("note %q, want the wait rounded up to 8 minutes", text)
	}
	if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
		t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
	}
	if entries := storedChangeLog(t, w.st, before.ID); len(entries) != 0 {
		t.Errorf("a refused rename appended %d entries, want 0", len(entries))
	}
}

func TestBitrateIsBoundedByTheBoostTierReadAtSave(t *testing.T) {
	cases := []struct {
		name    string
		tier    discordgo.PremiumTier
		bitrate string
		refused bool
	}{
		{"tier 0 refuses 96001", discordgo.PremiumTierNone, "96001", true},
		{"tier 1 accepts 128000", discordgo.PremiumTier1, "128000", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			w.discord.setPremiumTier(tc.tier)
			before := storedHubs(t, w.st)[0]
			form := updateForm(t, w.st)
			form.Set("bitrate", tc.bitrate)

			res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

			after := storedHubs(t, w.st)[0]
			if !tc.refused {
				assertRedirect(t, res, "/")
				if after.Bitrate != 128000 {
					t.Errorf("stored bitrate = %d, want 128000", after.Bitrate)
				}
				return
			}
			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != "bitrate" {
				t.Errorf("data-error = %q (present %v), want bitrate", field, ok)
			}
			if !sameHubSettings(after, before) {
				t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
			}
		})
	}
}

// breakChannel edits the fake guild so the hub channel is gone, or has no
// parent.
func (f *fakeDiscord) breakChannel(channelID string, gone bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gone {
		f.channels = slices.DeleteFunc(f.channels, func(ch *discordgo.Channel) bool { return ch.ID == channelID })
		return
	}
	for _, ch := range f.channels {
		if ch.ID == channelID {
			ch.ParentID = ""
		}
	}
}

// secondHub is a stored hub on vc-2, healthy beside a broken hub-1.
func secondHub() store.Hub {
	return store.Hub{
		GuildID: testGuildID, HubChannelID: "vc-2", BaseString: "Squad Voice",
		PermissionSource: store.PermissionCategory, Bitrate: 64000, Enabled: true,
	}
}

// hasField reports whether n holds an element under data-field=name.
func hasField(n *html.Node, name string) bool {
	return findElement(n, "", "data-field", name) != nil
}

func TestBrokenHubOffersRemoveOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		gone bool
	}{
		{"channel gone", true},
		{"channel with no category", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub(), secondHub())
			signIn(t, w.forum, w.b)
			w.discord.breakChannel("hub-1", tc.gone)
			broken := storedHubID(t, w.st, "hub-1")
			healthy := storedHubID(t, w.st, "vc-2")

			res := w.b.get("/")

			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET / status = %d, want 200", res.StatusCode)
			}
			doc := parseHTML(t, res)
			row := findElement(doc, "", "data-hub", strconv.FormatInt(broken, 10))
			if row == nil {
				t.Fatalf("page has no row under data-hub=%d", broken)
			}
			if !hasField(row, "broken") || !hasField(row, "remove") || hasField(row, "edit") {
				t.Errorf("broken row: broken %v, remove %v, edit %v; want broken and remove present, edit absent",
					hasField(row, "broken"), hasField(row, "remove"), hasField(row, "edit"))
			}
			other := findElement(doc, "", "data-hub", strconv.FormatInt(healthy, 10))
			if other == nil {
				t.Fatalf("page has no row under data-hub=%d", healthy)
			}
			if hasField(other, "broken") || hasField(other, "remove") {
				t.Errorf("healthy row: broken %v, remove %v; want neither", hasField(other, "broken"), hasField(other, "remove"))
			}

			sec := editSection(t, w.b.get("/?hub="+strconv.FormatInt(broken, 10)), broken)
			if !hasField(sec, "remove") || hasField(sec, "save") {
				t.Errorf("broken hub's section: remove %v, save %v; want the remove form and no edit form", hasField(sec, "remove"), hasField(sec, "save"))
			}
		})
	}
}

func TestUpdateOfABrokenHubIsRefusedBeforeAnyRename(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.breakChannel("hub-1", false)
	before := storedHubs(t, w.st)[0]
	form := updateForm(t, w.st)
	form.Set("channel_name", "Join here")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
		t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
	}
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("an update of a broken hub made edits %+v, want none", edits)
	}
}

func TestHubListShowsTheLastSpawnFailureTheRuntimeHolds(t *testing.T) {
	w := newTestWorld(t, testHub(), secondHub())
	signIn(t, w.forum, w.b)
	w.discord.setCreateErr(restError(http.StatusInternalServerError))
	w.join("user-a", "hub-1")
	failed := storedHubID(t, w.st, "hub-1")
	fine := storedHubID(t, w.st, "vc-2")
	failure, ok := w.runtime.LastSpawnFailure(failed)
	if !ok {
		t.Fatal("the runtime holds no spawn failure for hub-1 after the failed join")
	}

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	doc := parseHTML(t, res)
	row := findElement(doc, "", "data-hub", strconv.FormatInt(failed, 10))
	if row == nil {
		t.Fatalf("page has no row under data-hub=%d", failed)
	}
	if got := fieldText(t, row, "failure_cause"); got != string(failure.Cause) {
		t.Errorf("data-field=failure_cause shows %q, want the runtime's cause %q", got, failure.Cause)
	}
	at := findElement(row, "", "data-field", "failure_at")
	if at == nil {
		t.Fatal("the row has no element under data-field=failure_at")
	}
	if raw, _ := attrValue(at, "datetime"); raw == "" {
		t.Error("the failure time carries no datetime attribute")
	} else if _, err := time.Parse(time.RFC3339, raw); err != nil {
		t.Errorf("failure datetime %q is not RFC 3339: %v", raw, err)
	}
	other := findElement(doc, "", "data-hub", strconv.FormatInt(fine, 10))
	if other == nil {
		t.Fatalf("page has no row under data-hub=%d", fine)
	}
	if hasField(other, "failure_cause") {
		t.Error("a hub with no failure shows a data-field=failure_cause element")
	}
}

// scriptSources returns the src of every script element under n.
func scriptSources(n *html.Node) []string {
	var out []string
	eachLiveElement(n, func(n *html.Node) {
		if src, ok := attrValue(n, "src"); n.Data == "script" && ok {
			out = append(out, src)
		}
	})
	return out
}

func TestHubPageScriptIsServedByThePanelItself(t *testing.T) {
	// ADR 0013: the panel serves its own script, and no page fetches from a
	// third party.
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	sources := scriptSources(parseHTML(t, res))
	if len(sources) == 0 {
		t.Fatal("the hub page references no script")
	}
	for _, src := range sources {
		u, err := url.Parse(src)
		if err != nil || u.Scheme != "" || u.Host != "" {
			t.Errorf("script src %q is not a path on the panel's own host", src)
			continue
		}
		got := w.b.get(src)
		if got.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", src, got.StatusCode)
		}
		if ct := got.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("GET %s Content-Type = %q, want a JavaScript type", src, ct)
		}
	}
}

// entryElements returns the data-entry elements under n, in document order.
func entryElements(n *html.Node) []*html.Node {
	var out []*html.Node
	eachLiveElement(n, func(n *html.Node) {
		if _, ok := attrValue(n, "data-entry"); ok {
			out = append(out, n)
		}
	})
	return out
}

func TestChangeLogEntriesFoldWithTheNewestOpen(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	for _, limit := range []string{"1", "2"} {
		form := updateForm(t, w.st)
		form.Set("user_limit", limit)
		assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")
	}

	res := w.b.get("/?hub=" + strconv.FormatInt(id, 10))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
	}
	entries := entryElements(editSection(t, res, id))
	if len(entries) != 2 {
		t.Fatalf("the hub form shows %d entries, want 2", len(entries))
	}
	// The fold is <details>, which folds with no script.
	if entries[0].Data != "details" {
		t.Errorf("the newest entry is a <%s>, want a <details>", entries[0].Data)
	}
	if _, open := attrValue(entries[0], "open"); !open {
		t.Error("the newest entry is not open")
	}
	if _, open := attrValue(entries[1], "open"); open {
		t.Error("the older entry is open, want it folded")
	}
	if findElement(entries[1], "", "data-change", "user_limit") == nil {
		t.Error("the folded entry carries no data-change=user_limit element")
	}
}
