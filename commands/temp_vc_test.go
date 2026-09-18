package commands

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// fakeTempVCManager records calls and injects per-call errors. Mutex-guarded
// because the suite runs under -race and the fake is shared with any goroutine
// a test spawns. channels stands in for discordgo's state cache.
type fakeTempVCManager struct {
	mu sync.Mutex

	channels    map[string]*discordgo.Channel
	created     []fakeCreate
	deleted     []fakeDelete
	moves       []fakeMove
	nextChannel *discordgo.Channel

	deleteCalls int

	channelErr error
	createErr  error
	deleteErr  error
	moveErr    error
}

type fakeCreate struct {
	data   discordgo.GuildChannelCreateData
	reason string
}

type fakeDelete struct {
	channelID string
	reason    string
}

type fakeMove struct {
	userID    string
	channelID *string
}

const (
	testTempVCGuild    = "guild-1"
	testTempVCHub      = "hub-1"
	testTempVCCategory = "cat-1"
)

// newFakeTempVCManager returns a fake whose state cache holds the test hub
// channel under the test category.
func newFakeTempVCManager() *fakeTempVCManager {
	return &fakeTempVCManager{
		channels: map[string]*discordgo.Channel{
			testTempVCHub: {ID: testTempVCHub, ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
		},
		nextChannel: &discordgo.Channel{ID: "new-chan"},
	}
}

func (f *fakeTempVCManager) Channel(channelID string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.channelErr != nil {
		return nil, f.channelErr
	}
	ch, ok := f.channels[channelID]
	if !ok {
		return nil, discordgo.ErrStateNotFound
	}
	return ch, nil
}

func (f *fakeTempVCManager) GuildChannelCreateComplex(_ string, data discordgo.GuildChannelCreateData, reason string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, fakeCreate{data: data, reason: reason})
	ch := *f.nextChannel
	ch.Name = data.Name
	return &ch, nil
}

func (f *fakeTempVCManager) ChannelDelete(channelID, reason string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, fakeDelete{channelID: channelID, reason: reason})
	return &discordgo.Channel{ID: channelID}, nil
}

func (f *fakeTempVCManager) GuildMemberMove(_ string, userID string, channelID *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moves = append(f.moves, fakeMove{userID: userID, channelID: channelID})
	return nil
}

// setNextChannel changes the channel the next create returns.
func (f *fakeTempVCManager) setNextChannel(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextChannel = &discordgo.Channel{ID: id}
}

func (f *fakeTempVCManager) deleteCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleteCalls
}

func (f *fakeTempVCManager) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.deleted))
	for _, d := range f.deleted {
		out = append(out, d.channelID)
	}
	return out
}

func (f *fakeTempVCManager) recordedDeletes() []fakeDelete {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeDelete, len(f.deleted))
	copy(out, f.deleted)
	return out
}

func (f *fakeTempVCManager) recordedCreates() []fakeCreate {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCreate, len(f.created))
	copy(out, f.created)
	return out
}

// createdNames returns the name of every created channel, in order.
func (f *fakeTempVCManager) createdNames() []string {
	var out []string
	for _, c := range f.recordedCreates() {
		out = append(out, c.data.Name)
	}
	return out
}

func (f *fakeTempVCManager) recordedMoves() []fakeMove {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeMove, len(f.moves))
	copy(out, f.moves)
	return out
}

// testHub is the hub row every test starts from: the test hub channel, base
// string "Voice", category permissions, a user limit and a bitrate distinct
// from Discord's defaults so a payload that drops either goes red.
func testHub() store.Hub {
	return store.Hub{
		GuildID:          testTempVCGuild,
		HubChannelID:     testTempVCHub,
		BaseString:       "Voice",
		PermissionSource: store.PermissionCategory,
		UserLimit:        5,
		Bitrate:          96000,
		Enabled:          true,
	}
}

// seedStore returns a Fake holding the given hubs.
func seedStore(t *testing.T, hubs ...store.Hub) *store.Fake {
	t.Helper()
	st := store.NewFake()
	for _, h := range hubs {
		if _, err := st.UpsertHub(context.Background(), h); err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
	}
	return st
}

// newTestTempVC builds the runtime over a fake manager and a store, loading
// the store's hubs the way startup does.
func newTestTempVC(t *testing.T, mgr TempVCManager, st store.Store) *TempVC {
	t.Helper()
	tv, err := newTempVC(mgr, st, testTempVCGuild)
	if err != nil {
		t.Fatalf("newTempVC: %v", err)
	}
	return tv
}

// newSeededTempVC is the one-hub fixture: the test hub stored, the fake
// manager's cache holding its channel.
func newSeededTempVC(t *testing.T, mgr TempVCManager) *TempVC {
	t.Helper()
	return newTestTempVC(t, mgr, seedStore(t, testHub()))
}

// spawnedRows lists the store's spawned channel rows.
func spawnedRows(t *testing.T, st store.Store) []store.SpawnedChannel {
	t.Helper()
	rows, err := st.ListSpawnedChannels(context.Background())
	if err != nil {
		t.Fatalf("ListSpawnedChannels: %v", err)
	}
	return rows
}

// storedHubID reads back the surrogate ID the store gave the test hub.
func storedHubID(t *testing.T, st store.Store) int64 {
	t.Helper()
	hubs, err := st.ListHubs(context.Background(), testTempVCGuild)
	if err != nil {
		t.Fatalf("ListHubs: %v", err)
	}
	for _, h := range hubs {
		if h.HubChannelID == testTempVCHub {
			return h.ID
		}
	}
	t.Fatal("test hub not stored")
	return 0
}

func voiceEvent(userID, channelID string, member *discordgo.Member) *discordgo.VoiceStateUpdate {
	return &discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID:   testTempVCGuild,
		UserID:    userID,
		ChannelID: channelID,
		Member:    member,
	}}
}

func TestTempVCJoinSpawnsFromStoredHub(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, &discordgo.Member{Nick: "Smith"}))

	creates := fake.recordedCreates()
	if len(creates) != 1 {
		t.Fatalf("created %d channels, want 1", len(creates))
	}
	data := creates[0].data
	if data.Name != "Voice - 1" {
		t.Errorf("name = %q, want the base string plus the first number", data.Name)
	}
	if data.Type != discordgo.ChannelTypeGuildVoice {
		t.Errorf("type = %v, want voice", data.Type)
	}
	if data.ParentID != testTempVCCategory {
		t.Errorf("parent = %q, want the hub channel's parent %q", data.ParentID, testTempVCCategory)
	}
	if data.UserLimit != 5 || data.Bitrate != 96000 {
		t.Errorf("limit/bitrate = %d/%d, want the hub row's 5/96000", data.UserLimit, data.Bitrate)
	}
	// Permission source category: no overwrites, so Discord copies the
	// category's. nil and empty are the same on the wire.
	if len(data.PermissionOverwrites) != 0 {
		t.Errorf("overwrites = %+v, want none", data.PermissionOverwrites)
	}
	if !strings.Contains(creates[0].reason, testTempVCHub) || !strings.Contains(creates[0].reason, "user-1") {
		t.Errorf("create audit reason %q does not name the hub and the creator", creates[0].reason)
	}

	moves := fake.recordedMoves()
	if len(moves) != 1 || moves[0].userID != "user-1" || moves[0].channelID == nil || *moves[0].channelID != "new-chan" {
		t.Fatalf("moves = %+v, want user-1 into new-chan", moves)
	}

	rows := spawnedRows(t, st)
	if len(rows) != 1 {
		t.Fatalf("spawned rows = %+v, want one", rows)
	}
	row := rows[0]
	if row.ChannelID != "new-chan" || row.Number != 1 || row.OwnerUserID != "user-1" || row.HubID != storedHubID(t, st) {
		t.Errorf("row = %+v, want new-chan, number 1, owner user-1, the stored hub's ID", row)
	}
}

// --- Interim ownership: hand control to the present member with the highest
// rank role (lowest user ID is the final tiebreak), restore on the creator's
// return. Bookkeeping only; nothing changes in Discord. ---

// Real rank role IDs (SGT outranks PVT) used to exercise the election.
const (
	testRankSGT = "899328273752928318"
	testRankPVT = "899328617081864202"
	testRankCPT = "899326238685024267"
)

// member builds a member with a nickname and optional rank role IDs, which
// drive the election.
func member(nick string, roleIDs ...string) *discordgo.Member {
	return &discordgo.Member{Nick: nick, Roles: roleIDs}
}

// spawnOwnedChannel drives the creator through hub join -> moved into "new-chan"
// so the standard tracking (owner, occupants, memberMeta) is populated the same
// way production would, then returns with the creator sitting in the channel.
func spawnOwnedChannel(tv *TempVC, creatorID, nick string) {
	tv.handleVoiceStateUpdate(voiceEvent(creatorID, testTempVCHub, member(nick)))
	tv.handleVoiceStateUpdate(voiceEvent(creatorID, "new-chan", member(nick)))
}

// controllerOf reads the current interim controller of a channel under the lock.
func (t *TempVC) controllerOf(channelID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.controller[channelID]
}

// TestOutranksForInterim covers the election comparator directly. Smaller
// index = higher priority.
func TestOutranksForInterim(t *testing.T) {
	cases := []struct {
		name       string
		a          memberRankMeta
		aID        string
		b          memberRankMeta
		bID        string
		aOutranksB bool
	}{
		{"higher rank wins",
			memberRankMeta{rankIdx: 4}, "z",
			memberRankMeta{rankIdx: 9}, "a", true},
		{"same rank, lowest ID wins",
			memberRankMeta{rankIdx: 4}, "a",
			memberRankMeta{rankIdx: 4}, "b", true},
		{"lower rank loses despite lower ID",
			memberRankMeta{rankIdx: 27}, "a",
			memberRankMeta{rankIdx: 0}, "z", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outranksForInterim(tc.a, tc.aID, tc.b, tc.bID); got != tc.aOutranksB {
				t.Errorf("outranksForInterim = %v, want %v", got, tc.aOutranksB)
			}
		})
	}
}

// TestLowestRoleIndex covers deriving a member's rank index from their roles.
func TestLowestRoleIndex(t *testing.T) {
	idx := map[string]int{"hi": 0, "mid": 3, "lo": 7}
	cases := []struct {
		name  string
		roles []string
		want  int
	}{
		{"nil member -> fallback", nil, 99},
		{"no matching role -> fallback", []string{"x", "y"}, 99},
		{"single match", []string{"mid"}, 3},
		{"best (lowest) of several", []string{"lo", "hi", "mid"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m *discordgo.Member
			if tc.roles != nil {
				m = &discordgo.Member{Roles: tc.roles}
			}
			if got := lowestRoleIndex(m, idx, 99); got != tc.want {
				t.Errorf("lowestRoleIndex = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTempVCInterimHigherRankWins(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// Two guests with different rank roles; the SGT outranks the PVT.
	tv.handleVoiceStateUpdate(voiceEvent("g-pvt", "new-chan", member("Pvt", testRankPVT)))
	tv.handleVoiceStateUpdate(voiceEvent("g-sgt", "new-chan", member("Sgt", testRankSGT)))

	// No interim handoff happens while the creator is present.
	if got := tv.controllerOf("new-chan"); got != "" {
		t.Fatalf("controller = %q while creator present, want none", got)
	}

	// Creator leaves -> the higher rank wins.
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	if got := tv.controllerOf("new-chan"); got != "g-sgt" {
		t.Errorf("controller = %q, want g-sgt (the higher rank)", got)
	}
}

func TestTempVCInterimRankRoleBeatsNoRole(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// A plain, role-less member and a PVT; the rank holder wins.
	tv.handleVoiceStateUpdate(voiceEvent("a-plain", "new-chan", member("Nobody")))
	tv.handleVoiceStateUpdate(voiceEvent("z-pvt", "new-chan", member("Pvt", testRankPVT)))

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	if got := tv.controllerOf("new-chan"); got != "z-pvt" {
		t.Errorf("controller = %q, want the rank holder over the role-less member", got)
	}
}

func TestTempVCInterimAnyoneEligible(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// A single plain, role-less member still becomes interim owner: no gate.
	tv.handleVoiceStateUpdate(voiceEvent("plain", "new-chan", member("just a name")))

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	if got := tv.controllerOf("new-chan"); got != "plain" {
		t.Errorf("controller = %q, want the sole plain member (anyone is eligible)", got)
	}
}

func TestTempVCInterimOwnerRestoredOnCreatorReturn(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	tv.handleVoiceStateUpdate(voiceEvent("g-sgt", "new-chan", member("Sgt", testRankSGT)))
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	// Sanity: interim in place.
	if got := tv.controllerOf("new-chan"); got != "g-sgt" {
		t.Fatalf("controller = %q before return, want g-sgt", got)
	}

	// Creator returns -> the stand-in steps down; the creator's own claim holds.
	tv.handleVoiceStateUpdate(voiceEvent("owner", "new-chan", member("CPL Owner")))

	if got := tv.controllerOf("new-chan"); got != "" {
		t.Errorf("controller = %q after creator returned, want none", got)
	}
}

func TestTempVCInterimOwnerReelectedWhenStandInLeaves(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// g-sgt outranks g-pvt, so g-sgt is elected first; when they leave, g-pvt
	// is re-elected.
	tv.handleVoiceStateUpdate(voiceEvent("g-sgt", "new-chan", member("Sgt", testRankSGT)))
	tv.handleVoiceStateUpdate(voiceEvent("g-pvt", "new-chan", member("Pvt", testRankPVT)))
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner"))) // g-sgt elected
	if got := tv.controllerOf("new-chan"); got != "g-sgt" {
		t.Fatalf("controller = %q, want g-sgt first", got)
	}

	tv.handleVoiceStateUpdate(voiceEvent("g-sgt", "", member("Sgt", testRankSGT)))

	if got := tv.controllerOf("new-chan"); got != "g-pvt" {
		t.Errorf("controller = %q, want g-pvt after re-election", got)
	}
}

func TestTempVCInterimHigherRankJoinerTakesOver(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// Creator leaves with only a PVT present -> they hold interim.
	tv.handleVoiceStateUpdate(voiceEvent("g-pvt", "new-chan", member("Pvt", testRankPVT)))
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))
	if got := tv.controllerOf("new-chan"); got != "g-pvt" {
		t.Fatalf("controller = %q, want g-pvt initially", got)
	}

	// A CPT joins while the creator is away and takes over.
	tv.handleVoiceStateUpdate(voiceEvent("g-cpt", "new-chan", member("Cpt", testRankCPT)))

	if got := tv.controllerOf("new-chan"); got != "g-cpt" {
		t.Errorf("controller = %q, want g-cpt after a higher rank joined", got)
	}
}

func TestTempVCInterimOwnerTieBreaksByLowestID(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// Same rank -> tie -> lowest ID wins.
	tv.handleVoiceStateUpdate(voiceEvent("z-sgt", "new-chan", member("Zulu", testRankSGT)))
	tv.handleVoiceStateUpdate(voiceEvent("a-sgt", "new-chan", member("Alpha", testRankSGT)))

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	if got := tv.controllerOf("new-chan"); got != "a-sgt" {
		t.Errorf("controller = %q, want deterministic lowest-ID a-sgt", got)
	}
}

func TestTempVCHubChannelPermissionSourceCopiesOverwrites(t *testing.T) {
	fake := newFakeTempVCManager()
	want := []*discordgo.PermissionOverwrite{
		{ID: "role-mp", Type: discordgo.PermissionOverwriteTypeRole, Allow: discordgo.PermissionVoiceMoveMembers},
		{ID: "role-guest", Type: discordgo.PermissionOverwriteTypeRole, Deny: discordgo.PermissionVoiceConnect},
	}
	fake.channels[testTempVCHub].PermissionOverwrites = want
	hub := testHub()
	hub.PermissionSource = store.PermissionHubChannel
	tv := newTestTempVC(t, fake, seedStore(t, hub))

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, nil))

	creates := fake.recordedCreates()
	if len(creates) != 1 {
		t.Fatalf("created %d channels, want 1", len(creates))
	}
	got := creates[0].data.PermissionOverwrites
	if len(got) != len(want) {
		t.Fatalf("overwrites = %+v, want the hub channel's %d entries and nothing else", got, len(want))
	}
	// Compared as a set: the order Discord receives them in is not a contract.
	for _, w := range want {
		found := false
		for _, g := range got {
			if g.ID == w.ID && g.Type == w.Type && g.Allow == w.Allow && g.Deny == w.Deny {
				found = true
			}
		}
		if !found {
			t.Errorf("overwrite for %s missing from the payload: %+v", w.ID, got)
		}
	}
}

func TestTempVCNumbersPerHubAndReusesFreedNumber(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	// Two members spawn from the same hub: the hub numbers them 1 and 2.
	a := member("A")
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))
	fake.setNextChannel("second-chan")
	b := member("B")
	tv.handleVoiceStateUpdate(voiceEvent("user-b", testTempVCHub, b))
	tv.handleVoiceStateUpdate(voiceEvent("user-b", "second-chan", b))

	// The first channel empties: deleted at once, with an audit reason, and
	// its row goes with it.
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", a))
	deletes := fake.recordedDeletes()
	if len(deletes) != 1 || deletes[0].channelID != "new-chan" {
		t.Fatalf("deletes = %+v, want new-chan the moment it emptied", deletes)
	}
	if deletes[0].reason == "" {
		t.Error("delete carried no audit log reason")
	}
	for _, row := range spawnedRows(t, st) {
		if row.ChannelID == "new-chan" {
			t.Errorf("row for the deleted channel still stored: %+v", row)
		}
	}

	// The freed number is reused rather than a 3 minted.
	fake.setNextChannel("third-chan")
	tv.handleVoiceStateUpdate(voiceEvent("user-c", testTempVCHub, member("C")))

	if names := fake.createdNames(); len(names) != 3 || names[0] != "Voice - 1" || names[1] != "Voice - 2" || names[2] != "Voice - 1" {
		t.Fatalf("created = %v, want Voice - 1, Voice - 2, then the freed Voice - 1", names)
	}
	var third *store.SpawnedChannel
	for _, row := range spawnedRows(t, st) {
		if row.ChannelID == "third-chan" {
			third = &row
		}
	}
	if third == nil || third.Number != 1 {
		t.Errorf("third row = %+v, want number 1", third)
	}
}

func TestTempVCTwoHubsNumberIndependentlyWithOwnSettings(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.channels["hub-2"] = &discordgo.Channel{ID: "hub-2", ParentID: "cat-2", Type: discordgo.ChannelTypeGuildVoice}
	second := store.Hub{
		GuildID: testTempVCGuild, HubChannelID: "hub-2", BaseString: "Briefing",
		PermissionSource: store.PermissionCategory, UserLimit: 12, Bitrate: 128000, Enabled: true,
	}
	tv := newTestTempVC(t, fake, seedStore(t, testHub(), second))

	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, member("A")))
	fake.setNextChannel("chan-b")
	tv.handleVoiceStateUpdate(voiceEvent("user-b", "hub-2", member("B")))

	creates := fake.recordedCreates()
	if len(creates) != 2 {
		t.Fatalf("created %d channels, want 2", len(creates))
	}
	a, b := creates[0].data, creates[1].data
	if a.Name != "Voice - 1" || a.ParentID != testTempVCCategory || a.UserLimit != 5 || a.Bitrate != 96000 {
		t.Errorf("hub 1 payload = %+v, want Voice - 1 under cat-1 with 5/96000", a)
	}
	if b.Name != "Briefing - 1" || b.ParentID != "cat-2" || b.UserLimit != 12 || b.Bitrate != 128000 {
		t.Errorf("hub 2 payload = %+v, want Briefing - 1 under cat-2 with 12/128000", b)
	}
}

func TestTempVCDisabledHubSpawnsNothing(t *testing.T) {
	fake := newFakeTempVCManager()
	hub := testHub()
	hub.Enabled = false
	st := seedStore(t, hub)
	tv := newTestTempVC(t, fake, st)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

	if creates := fake.recordedCreates(); len(creates) != 0 {
		t.Errorf("created %d channels from a disabled hub, want 0", len(creates))
	}
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Errorf("moves = %+v, want none", moves)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestTempVCHubWithoutCategorySpawnsNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeTempVCManager)
	}{
		{"hub channel has no parent", func(f *fakeTempVCManager) { f.channels[testTempVCHub].ParentID = "" }},
		{"hub channel absent from the cache", func(f *fakeTempVCManager) { delete(f.channels, testTempVCHub) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTempVCManager()
			tc.setup(fake)
			st := seedStore(t, testHub())
			tv := newTestTempVC(t, fake, st)

			tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

			if creates := fake.recordedCreates(); len(creates) != 0 {
				t.Errorf("created %d channels, want 0 (no API call)", len(creates))
			}
			if moves := fake.recordedMoves(); len(moves) != 0 {
				t.Errorf("moves = %+v, want none", moves)
			}
			if rows := spawnedRows(t, st); len(rows) != 0 {
				t.Errorf("rows = %+v, want none", rows)
			}
		})
	}
}
