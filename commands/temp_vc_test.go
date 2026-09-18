package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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
	messages    []fakeMessage
	nextChannel *discordgo.Channel
	// member and guild are what GuildMember and Guild return; the startup
	// Administrator check reads both.
	member *discordgo.Member
	guild  *discordgo.Guild

	deleteCalls int

	channelErr error
	createErr  error
	deleteErr  error
	moveErr    error
	messageErr error
	memberErr  error
	guildErr   error

	// createStarted and createRelease, when set, make every create signal
	// that it has started and then wait until release is closed, so a test
	// can hold two creates in flight at once. Each such create returns a
	// channel ID of its own.
	createStarted chan struct{}
	createRelease chan struct{}
	createSeq     int
	// moveHook, when set, runs inside every move call before the fake
	// answers, outside the fake's lock. The runtime holds no lock across the
	// move either, so a test can feed a gateway event that lands while the
	// move is in flight.
	moveHook func()
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

type fakeMessage struct {
	channelID string
	data      *discordgo.MessageSend
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
	// The barrier runs outside the fake's lock so two creates can wait at once.
	if f.createStarted != nil {
		f.createStarted <- struct{}{}
		<-f.createRelease
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, fakeCreate{data: data, reason: reason})
	ch := *f.nextChannel
	if f.createStarted != nil {
		f.createSeq++
		ch.ID = fmt.Sprintf("chan-%d", f.createSeq)
	}
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
	if f.moveHook != nil {
		f.moveHook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moves = append(f.moves, fakeMove{userID: userID, channelID: channelID})
	return nil
}

func (f *fakeTempVCManager) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.messageErr != nil {
		return nil, f.messageErr
	}
	f.messages = append(f.messages, fakeMessage{channelID: channelID, data: data})
	return &discordgo.Message{ChannelID: channelID, Content: data.Content}, nil
}

func (f *fakeTempVCManager) GuildMember(_, _ string) (*discordgo.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memberErr != nil {
		return nil, f.memberErr
	}
	return f.member, nil
}

func (f *fakeTempVCManager) Guild(_ string) (*discordgo.Guild, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.guildErr != nil {
		return nil, f.guildErr
	}
	return f.guild, nil
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

func (f *fakeTempVCManager) recordedMessages() []fakeMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeMessage, len(f.messages))
	copy(out, f.messages)
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

	// The creator holds a rank role, so the row names them as owner.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("Smith", testRankSGT)))

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

// --- Ownership and handover (#291): the owner always holds a rank role, a
// handover is final, and every change is one ownership notice in the spawned
// channel's chat plus a row write. Bookkeeping only; ownership grants no
// Discord permission. ---

// Real rank role IDs from the ladder: CPT outranks SGT outranks PVT.
const (
	testRankSGT = "899328273752928318"
	testRankPVT = "899328617081864202"
	testRankCPT = "899326238685024267"
)

// member builds a member with a nickname and optional rank role IDs, which
// drive the handover rule.
func member(nick string, roleIDs ...string) *discordgo.Member {
	return &discordgo.Member{Nick: nick, Roles: roleIDs}
}

// spawnInto drives a member through hub join -> moved into channelID, so
// tracking (owner, occupants, ranks) is populated the way production's event
// sequence populates it, and returns with the member sitting in the channel.
func spawnInto(tv *TempVC, fake *fakeTempVCManager, userID, channelID string, m *discordgo.Member) {
	fake.setNextChannel(channelID)
	tv.handleVoiceStateUpdate(voiceEvent(userID, testTempVCHub, m))
	tv.handleVoiceStateUpdate(voiceEvent(userID, channelID, m))
}

// noticesIn returns the messages sent to one channel's text chat, in order,
// and checks each pings nobody: allowed mentions present, nothing parsed, no
// users, no roles. Every ownership notice reads through here, so the check
// is written once.
func noticesIn(t *testing.T, fake *fakeTempVCManager, channelID string) []fakeMessage {
	t.Helper()
	var out []fakeMessage
	for _, m := range fake.recordedMessages() {
		if m.channelID != channelID {
			continue
		}
		am := m.data.AllowedMentions
		if am == nil || len(am.Parse) != 0 || len(am.Users) != 0 || len(am.Roles) != 0 {
			t.Errorf("notice in %s has allowed mentions %+v, want nobody pingable", channelID, am)
		}
		out = append(out, m)
	}
	return out
}

// rowFor reads one spawned channel row back from the store.
func rowFor(t *testing.T, st store.Store, channelID string) (store.SpawnedChannel, bool) {
	t.Helper()
	for _, row := range spawnedRows(t, st) {
		if row.ChannelID == channelID {
			return row, true
		}
	}
	return store.SpawnedChannel{}, false
}

func TestTempVCCreatorWithRankRoleOwnsAtCreate(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	spawnInto(tv, fake, "user-1", "new-chan", member("Sgt", testRankSGT))

	if owner, tracked := tv.Owner("new-chan"); owner != "user-1" || !tracked {
		t.Errorf("Owner(new-chan) = %q, %v, want the creator user-1, tracked", owner, tracked)
	}
	if owner, tracked := tv.Owner(testTempVCHub); tracked {
		t.Errorf("Owner(hub) = %q, tracked, want untracked: a hub channel is not a spawned channel", owner)
	}
	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "user-1" {
		t.Errorf("row = %+v (present %v), want owner user-1", row, ok)
	}
	notices := noticesIn(t, fake, "new-chan")
	if len(notices) != 1 {
		t.Fatalf("notices in new-chan = %d, want one at create", len(notices))
	}
	if !strings.Contains(notices[0].data.Content, "user-1") {
		t.Errorf("notice %q does not carry the owner's ID", notices[0].data.Content)
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
			captures := countCaptures(t)

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
			// The bot's own refusal: a log line and a panel note, never a
			// Sentry event and no message.
			if *captures != 0 {
				t.Errorf("captures = %d, want 0", *captures)
			}
			if msgs := fake.recordedMessages(); len(msgs) != 0 {
				t.Errorf("messages = %+v, want none (no API call)", msgs)
			}
		})
	}
}

// countCaptures swaps the Sentry seam for a counter and restores it after the
// test. Handlers run synchronously in these tests, so no lock is needed.
func countCaptures(t *testing.T) *int {
	t.Helper()
	prev := captureError
	n := 0
	captureError = func(string, error, ...any) { n++ }
	t.Cleanup(func() { captureError = prev })
	return &n
}

// failingStore wraps a Fake and fails the methods a test arms. Everything
// else reaches the Fake.
type failingStore struct {
	*store.Fake
	upsertSpawnedErr error
	listSpawnedErr   error
}

func (f *failingStore) UpsertSpawnedChannel(ctx context.Context, sc store.SpawnedChannel) error {
	if f.upsertSpawnedErr != nil {
		return f.upsertSpawnedErr
	}
	return f.Fake.UpsertSpawnedChannel(ctx, sc)
}

func (f *failingStore) ListSpawnedChannels(ctx context.Context) ([]store.SpawnedChannel, error) {
	if f.listSpawnedErr != nil {
		return nil, f.listSpawnedErr
	}
	return f.Fake.ListSpawnedChannels(ctx)
}

func TestTempVCFailedRowWriteKeepsChannelTrackedAndCaptures(t *testing.T) {
	fake := newFakeTempVCManager()
	st := &failingStore{Fake: seedStore(t, testHub()), upsertSpawnedErr: errors.New("connection reset")}
	tv := newTestTempVC(t, fake, st)
	captures := countCaptures(t)

	a := member("A")
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))

	if *captures != 1 {
		t.Errorf("captures = %d, want 1 for the failed row write", *captures)
	}
	if creates, moves := fake.recordedCreates(), fake.recordedMoves(); len(creates) != 1 || len(moves) != 1 {
		t.Fatalf("creates/moves = %d/%d, want 1/1: the channel is live", len(creates), len(moves))
	}

	// Still tracked: when the member leaves, the channel is deleted.
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", a))
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Errorf("deleted = %v, want new-chan (it stayed tracked without a row)", ids)
	}
}

func TestTempVCCreateFailureCapturesAndMovesNobody(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.createErr = errors.New("HTTP 403 Missing Permissions")
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	captures := countCaptures(t)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

	if *captures != 1 {
		t.Errorf("captures = %d, want 1", *captures)
	}
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Errorf("moves = %+v, want none after a failed create", moves)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

func TestTempVCMoveIntoFailureDeletesChannelAndWritesNoRow(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.moveErr = errors.New("target user is not connected to voice")
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("Gone")))

	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Fatalf("deleted = %v, want the new channel deleted at once", ids)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
}

// voiceChannel builds a guild voice channel for a GUILD_CREATE payload.
func voiceChannel(id, parentID, name string) *discordgo.Channel {
	return &discordgo.Channel{ID: id, ParentID: parentID, Name: name, Type: discordgo.ChannelTypeGuildVoice}
}

// guildCreate builds a GUILD_CREATE payload for the test guild.
func guildCreate(channels []*discordgo.Channel, voiceStates ...*discordgo.VoiceState) *discordgo.GuildCreate {
	return &discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID:          testTempVCGuild,
		Channels:    channels,
		VoiceStates: voiceStates,
	}}
}

func TestTempVCRestartSweepReadsRowsAndTouchesNothingElse(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	ctx := context.Background()
	hubID := storedHubID(t, st)

	// A: row whose channel is gone from the guild. B: present and empty.
	// C: present and occupied, its hub row deleted through the store. D:
	// present and occupied, number 1, named without a digit so only the row
	// can supply the number.
	gone, err := st.UpsertHub(ctx, store.Hub{GuildID: testTempVCGuild, HubChannelID: "hub-gone", BaseString: "Old", PermissionSource: store.PermissionCategory, Enabled: true})
	if err != nil {
		t.Fatalf("UpsertHub: %v", err)
	}
	for _, row := range []store.SpawnedChannel{
		{ChannelID: "chan-a", HubID: hubID, Number: 4, OwnerUserID: "user-x"},
		{ChannelID: "chan-b", HubID: hubID, Number: 3},
		{ChannelID: "chan-c", HubID: gone.ID, Number: 1, OwnerUserID: "user-a"},
		{ChannelID: "chan-d", HubID: hubID, Number: 1, OwnerUserID: "user-b"},
	} {
		if err := st.UpsertSpawnedChannel(ctx, row); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}
	}
	if err := st.DeleteHub(ctx, gone.ID); err != nil {
		t.Fatalf("DeleteHub: %v", err)
	}
	tv := newTestTempVC(t, fake, st)

	tv.handleGuildCreate(guildCreate(
		[]*discordgo.Channel{
			voiceChannel(testTempVCHub, testTempVCCategory, "Hub"),
			voiceChannel("chan-b", testTempVCCategory, "Voice - 3"),
			voiceChannel("chan-c", "cat-old", "Old - 1"),
			voiceChannel("chan-d", testTempVCCategory, "Owner picked this"),
			voiceChannel("human-made", testTempVCCategory, "Made by hand"),
		},
		&discordgo.VoiceState{UserID: "user-a", ChannelID: "chan-c"},
		&discordgo.VoiceState{UserID: "user-b", ChannelID: "chan-d"},
	))

	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "chan-b" {
		t.Fatalf("deleted = %v, want chan-b alone: never a channel without a row", ids)
	}
	remaining := map[string]bool{}
	for _, row := range spawnedRows(t, st) {
		remaining[row.ChannelID] = true
	}
	if remaining["chan-a"] || remaining["chan-b"] || !remaining["chan-c"] || !remaining["chan-d"] {
		t.Errorf("rows after sweep = %v, want chan-c and chan-d only", remaining)
	}

	// C was tracked again although its hub row is gone: when user-a leaves,
	// it is deleted.
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", member("A")))
	if ids := fake.deletedIDs(); len(ids) != 2 || ids[1] != "chan-c" {
		t.Errorf("deleted = %v, want chan-c once it emptied", ids)
	}

	// D holds number 1 from its row, so the next spawn from the hub is 2.
	tv.handleVoiceStateUpdate(voiceEvent("user-c", testTempVCHub, member("C")))
	if names := fake.createdNames(); len(names) != 1 || names[0] != "Voice - 2" {
		t.Errorf("created = %v, want Voice - 2 (number 1 came from D's row)", names)
	}
}

func TestTempVCFailedDeleteKeepsRowAndSweepRetries(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.deleteErr = errors.New("HTTP 500 Internal Server Error")
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	countCaptures(t)

	a := member("A")
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", a))

	if fake.deleteCallCount() != 1 {
		t.Fatalf("delete attempts = %d, want 1", fake.deleteCallCount())
	}
	if rows := spawnedRows(t, st); len(rows) != 1 || rows[0].ChannelID != "new-chan" {
		t.Fatalf("rows = %+v, want the row kept while the channel is live", rows)
	}

	// Discord recovers. The next sweep finds the channel present and empty
	// and retries the delete; this time the row goes.
	fake.mu.Lock()
	fake.deleteErr = nil
	fake.mu.Unlock()
	tv.handleGuildCreate(guildCreate([]*discordgo.Channel{
		voiceChannel(testTempVCHub, testTempVCCategory, "Hub"),
		voiceChannel("new-chan", testTempVCCategory, "Voice - 1"),
	}))

	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Errorf("deleted = %v, want new-chan on the retry", ids)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none after the retry", rows)
	}
}

func TestTempVCSweepListFailureCapturesAndDeletesNothing(t *testing.T) {
	fake := newFakeTempVCManager()
	base := seedStore(t, testHub())
	if err := base.UpsertSpawnedChannel(context.Background(), store.SpawnedChannel{ChannelID: "chan-b", HubID: storedHubID(t, base), Number: 1}); err != nil {
		t.Fatalf("UpsertSpawnedChannel: %v", err)
	}
	st := &failingStore{Fake: base, listSpawnedErr: errors.New("connection refused")}
	tv := newTestTempVC(t, fake, st)
	captures := countCaptures(t)

	tv.handleGuildCreate(guildCreate([]*discordgo.Channel{
		voiceChannel(testTempVCHub, testTempVCCategory, "Hub"),
		voiceChannel("chan-b", testTempVCCategory, "Voice - 1"),
	}))

	if *captures != 1 {
		t.Errorf("captures = %d, want 1", *captures)
	}
	if fake.deleteCallCount() != 0 {
		t.Errorf("delete attempts = %d, want 0 when the rows cannot be read", fake.deleteCallCount())
	}
	if rows := spawnedRows(t, base); len(rows) != 1 {
		t.Errorf("rows = %+v, want the one row untouched", rows)
	}
}

func TestTempVCChannelDeleteFreesRowAndNumber(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	a := member("A")
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))

	// Someone deletes the spawned channel in Discord's UI.
	tv.handleChannelDelete(&discordgo.ChannelDelete{Channel: &discordgo.Channel{ID: "new-chan", GuildID: testTempVCGuild}})

	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none after CHANNEL_DELETE", rows)
	}
	fake.setNextChannel("second-chan")
	tv.handleVoiceStateUpdate(voiceEvent("user-b", testTempVCHub, member("B")))
	if names := fake.createdNames(); len(names) != 2 || names[1] != "Voice - 1" {
		t.Errorf("created = %v, want the freed Voice - 1 reused", names)
	}

	// The hub channel itself is deleted: the handler does nothing. The hub
	// row stays for the panel to show as broken, and nothing is touched.
	tv.handleChannelDelete(&discordgo.ChannelDelete{Channel: &discordgo.Channel{ID: testTempVCHub, GuildID: testTempVCGuild}})
	hubs, err := st.ListHubs(context.Background(), testTempVCGuild)
	if err != nil || len(hubs) != 1 {
		t.Errorf("hubs = %+v (err %v), want the hub row kept", hubs, err)
	}
	if rows := spawnedRows(t, st); len(rows) != 1 || rows[0].ChannelID != "second-chan" {
		t.Errorf("rows = %+v, want second-chan untouched", rows)
	}
	if fake.deleteCallCount() != 0 {
		t.Errorf("delete attempts = %d, want 0", fake.deleteCallCount())
	}
}

func TestTempVCApplyHubAndRemoveHubChangeRoutingInProcess(t *testing.T) {
	fake := newFakeTempVCManager()
	st := store.NewFake()
	tv := newTestTempVC(t, fake, st)

	// No hub stored: the join spawns nothing, and the member sits in the hub.
	tv.handleVoiceStateUpdate(voiceEvent("user-z", testTempVCHub, member("Z")))
	if creates := fake.recordedCreates(); len(creates) != 0 {
		t.Fatalf("created %d channels with no hub, want 0", len(creates))
	}

	// The panel saves a hub: the next join spawns from it.
	hub, err := st.UpsertHub(context.Background(), testHub())
	if err != nil {
		t.Fatalf("UpsertHub: %v", err)
	}
	tv.ApplyHub(hub)
	a := member("A")
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))

	// The panel changes the base string: the live channel keeps its name
	// and its number, so the next spawn is 2 under the new base.
	hub.BaseString = "Ops"
	tv.ApplyHub(hub)
	fake.setNextChannel("second-chan")
	tv.handleVoiceStateUpdate(voiceEvent("user-b", testTempVCHub, member("B")))
	if names := fake.createdNames(); len(names) != 2 || names[0] != "Voice - 1" || names[1] != "Ops - 2" {
		t.Fatalf("created = %v, want Voice - 1 then Ops - 2", names)
	}
	if fake.deleteCallCount() != 0 {
		t.Errorf("delete attempts = %d, want 0: a base change touches no live channel", fake.deleteCallCount())
	}

	// The panel removes the hub: a join spawns nothing more.
	tv.RemoveHub(testTempVCHub)
	tv.handleVoiceStateUpdate(voiceEvent("user-c", testTempVCHub, member("C")))
	if creates := fake.recordedCreates(); len(creates) != 2 {
		t.Errorf("created %d channels after RemoveHub, want the count to stay at 2", len(creates))
	}
}

func TestTempVCIgnoresOtherGuilds(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	tv.handleVoiceStateUpdate(&discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: "other-guild", UserID: "user-1", ChannelID: testTempVCHub,
	}})
	tv.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID:       "other-guild",
		Channels: []*discordgo.Channel{voiceChannel("x", testTempVCCategory, "X")},
	}})

	if creates := fake.recordedCreates(); len(creates) != 0 {
		t.Errorf("created %d channels for a foreign guild, want 0", len(creates))
	}
	if fake.deleteCallCount() != 0 {
		t.Errorf("delete attempts = %d for a foreign guild, want 0", fake.deleteCallCount())
	}
}

func TestTempVCMuteToggleIsNoOp(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)

	a := member("A")
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, a))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", a))

	// Same channel again = mute/deafen toggle; must not disturb tracking or
	// spawn a second channel.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", a))

	if creates := fake.recordedCreates(); len(creates) != 1 {
		t.Errorf("created %d channels, want 1", len(creates))
	}
	if fake.deleteCallCount() != 0 {
		t.Errorf("delete attempts = %d on a no-op update, want 0", fake.deleteCallCount())
	}
}

func TestTempVCConcurrentJoinsGetDistinctNumbers(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.createStarted = make(chan struct{})
	fake.createRelease = make(chan struct{})
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	// Two members join the hub on two gateway goroutines.
	done := make(chan struct{}, 2)
	for _, user := range []string{"user-a", "user-b"} {
		go func(user string) {
			tv.handleVoiceStateUpdate(voiceEvent(user, testTempVCHub, member(user)))
			done <- struct{}{}
		}(user)
	}

	// Both creates are in flight before either commits. A runtime that held
	// the lock across the create would never let the second one start.
	deadline := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-fake.createStarted:
		case <-deadline:
			t.Fatal("second create did not start while the first was in flight: the create must not run under the lock")
		}
	}
	close(fake.createRelease)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-deadline:
			t.Fatal("a join handler did not return")
		}
	}

	names := fake.createdNames()
	sort.Strings(names)
	if len(names) != 2 || names[0] != "Voice - 1" || names[1] != "Voice - 2" {
		t.Errorf("created = %v, want Voice - 1 and Voice - 2, one each", names)
	}
	var numbers []int
	for _, row := range spawnedRows(t, st) {
		numbers = append(numbers, row.Number)
	}
	sort.Ints(numbers)
	if len(numbers) != 2 || numbers[0] != 1 || numbers[1] != 2 {
		t.Errorf("row numbers = %v, want 1 and 2", numbers)
	}
}

// unknownChannelErr is the error discordgo returns when a channel is already
// gone: HTTP 404 with Discord code 10003. restError lives in
// warden_resterror_test.go.
func unknownChannelErr() error {
	return restError(http.StatusNotFound, discordgo.ErrCodeUnknownChannel, "Unknown Channel")
}

// --- Spawn failures (#290): the member hears about it in the hub chat, the
// panel reads the last failure per hub, Sentry hears once per streak. ---

// hubMessagesMentioning returns the messages sent to the hub channel whose
// content carries the user's ID and whose allowed mentions name only them.
// Nobody else may be pinged, so Parse must be empty.
func hubMessagesMentioning(t *testing.T, fake *fakeTempVCManager, userID string) []fakeMessage {
	t.Helper()
	var out []fakeMessage
	for _, m := range fake.recordedMessages() {
		if m.channelID != testTempVCHub {
			t.Errorf("message sent to %q, want the hub channel %q", m.channelID, testTempVCHub)
			continue
		}
		if !strings.Contains(m.data.Content, userID) {
			t.Errorf("message content %q does not carry user %q", m.data.Content, userID)
			continue
		}
		am := m.data.AllowedMentions
		if am == nil || len(am.Parse) != 0 || len(am.Users) != 1 || am.Users[0] != userID {
			t.Errorf("allowed mentions = %+v, want users [%s] and nothing parsed", am, userID)
			continue
		}
		out = append(out, m)
	}
	return out
}

func TestTempVCCreateFailureMessagesTheMemberInTheHubChat(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.createErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, "Missing Permissions")
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	countCaptures(t)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

	if msgs := hubMessagesMentioning(t, fake, "user-1"); len(msgs) != 1 {
		t.Errorf("messages = %d, want exactly one in the hub chat", len(msgs))
	}
	// No disconnect and no move: the member stays where they are.
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Errorf("moves = %+v, want none", moves)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: a failure writes nothing", rows)
	}
}

func TestTempVCUnknownChannelOnDeleteIsQuietCleanup(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.deleteErr = unknownChannelErr()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	captures := countCaptures(t)

	// The channel was deleted by hand; the member's disconnect arrives first.
	a := member("A")
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", a))

	if *captures != 0 {
		t.Errorf("captures = %d, want 0: an already-gone channel is not a failure", *captures)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
	// Untracked, so its number is free again.
	fake.setNextChannel("second-chan")
	tv.handleVoiceStateUpdate(voiceEvent("user-b", testTempVCHub, member("B")))
	if names := fake.createdNames(); len(names) != 2 || names[1] != "Voice - 1" {
		t.Errorf("created = %v, want Voice - 1 reused", names)
	}
}

// categoryCapErr is the refusal Discord sends for a category holding its 50
// channels: HTTP 400, Invalid Form Body, with the parent_id field carrying
// CHANNEL_PARENT_MAX_CHANNELS. This body is authored from Discord's documented
// form-error shape, not captured from a live refusal. The smoke test on the
// test guild is where it gets compared with the real response.
func categoryCapErr() error {
	e := restError(http.StatusBadRequest, discordgo.ErrCodeInvalidFormBody, "Invalid Form Body")
	e.ResponseBody = []byte(`{"message": "Invalid Form Body", "code": 50035, "errors": {"parent_id": {"_errors": [{"code": "CHANNEL_PARENT_MAX_CHANNELS", "message": "Maximum number of channels in category reached (50)"}]}}}`)
	return e
}

// failedSpawnMessage runs one join against a create that fails with err, under
// the one member and hub fixture, and returns the content of the hub chat
// message. The fixture is the same for every call, so the error is the only
// thing that can change the text.
func failedSpawnMessage(t *testing.T, createErr error) string {
	t.Helper()
	fake := newFakeTempVCManager()
	fake.createErr = createErr
	tv := newSeededTempVC(t, fake)
	countCaptures(t)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

	msgs := hubMessagesMentioning(t, fake, "user-1")
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want one", len(msgs))
	}
	return msgs[0].data.Content
}

func TestTempVCCapAndGenericMessagesAreTwoTexts(t *testing.T) {
	guildCap := failedSpawnMessage(t, restError(http.StatusBadRequest, discordgo.ErrCodeMaximumNumberOfGuildChannelsReached, "Maximum number of guild channels reached (500)"))
	categoryCap := failedSpawnMessage(t, categoryCapErr())
	forbidden := failedSpawnMessage(t, restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, "Missing Permissions"))
	serverError := failedSpawnMessage(t, restError(http.StatusInternalServerError, 0, "Internal Server Error"))

	if guildCap != categoryCap {
		t.Errorf("guild cap %q and category cap %q differ, want the one cap text", guildCap, categoryCap)
	}
	if forbidden != serverError {
		t.Errorf("403 %q and 500 %q differ, want the one generic text", forbidden, serverError)
	}
	if guildCap == forbidden {
		t.Errorf("cap and generic texts are both %q, want two texts", guildCap)
	}
}

func TestTempVCMoveIntoFailureMessagesOnlyAMemberStillInTheHub(t *testing.T) {
	t.Run("member still in the hub", func(t *testing.T) {
		fake := newFakeTempVCManager()
		fake.moveErr = restError(http.StatusInternalServerError, 0, "Internal Server Error")
		tv := newSeededTempVC(t, fake)
		countCaptures(t)

		tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

		if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
			t.Errorf("deleted = %v, want the new channel", ids)
		}
		if msgs := hubMessagesMentioning(t, fake, "user-1"); len(msgs) != 1 {
			t.Errorf("messages = %d, want one: the member is still waiting in the hub", len(msgs))
		}
	})

	t.Run("member left the hub during the move", func(t *testing.T) {
		fake := newFakeTempVCManager()
		fake.moveErr = restError(http.StatusBadRequest, discordgo.ErrCodeTargetIsNotConnectedToVoice, "Target user is not connected to voice")
		tv := newSeededTempVC(t, fake)
		countCaptures(t)
		fake.moveHook = func() {
			tv.handleVoiceStateUpdate(voiceEvent("user-1", "", member("A")))
		}

		tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

		if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
			t.Errorf("deleted = %v, want the new channel", ids)
		}
		if msgs := fake.recordedMessages(); len(msgs) != 0 {
			t.Errorf("messages = %+v, want none: the member is gone", msgs)
		}
	})
}

// pinClock fixes the runtime's clock at a known instant for the test.
func pinClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := tempVCNow
	tempVCNow = func() time.Time { return at }
	t.Cleanup(func() { tempVCNow = prev })
}

func TestTempVCLastSpawnFailureIsKeptPerHubAndClearedBySuccess(t *testing.T) {
	at := time.Date(2026, time.September, 18, 20, 30, 0, 0, time.UTC)
	pinClock(t, at)
	fake := newFakeTempVCManager()
	fake.channels[testTempVCHub].ParentID = ""
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	hubID := storedHubID(t, st)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

	got, ok := tv.LastSpawnFailure(hubID)
	if !ok {
		t.Fatal("LastSpawnFailure = none, want the no-category refusal recorded")
	}
	if !got.At.Equal(at) || got.Cause != SpawnFailureNoCategory {
		t.Errorf("LastSpawnFailure = %+v, want at %v with cause %q", got, at, SpawnFailureNoCategory)
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the store holds no failure", rows)
	}

	// The category comes back and the next join spawns: the failure clears.
	fake.channels[testTempVCHub].ParentID = testTempVCCategory
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "", member("A")))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

	if got, ok := tv.LastSpawnFailure(hubID); ok {
		t.Errorf("LastSpawnFailure = %+v after a successful spawn, want none", got)
	}
}

// setCreateErr changes what the next creates return, under the fake's lock.
func (f *fakeTempVCManager) setCreateErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createErr = err
}

func TestTempVCCreateFailuresCaptureOncePerStreakPerHub(t *testing.T) {
	t.Run("each capturing class captures on its first failure", func(t *testing.T) {
		cases := []struct {
			name string
			err  error
		}{
			{"guild cap", restError(http.StatusBadRequest, discordgo.ErrCodeMaximumNumberOfGuildChannelsReached, "Maximum number of guild channels reached (500)")},
			{"429", restError(http.StatusTooManyRequests, 0, "You are being rate limited.")},
			{"500", restError(http.StatusInternalServerError, 0, "Internal Server Error")},
			{"transport", errors.New("dial tcp: connection refused")},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fake := newFakeTempVCManager()
				fake.createErr = tc.err
				tv := newSeededTempVC(t, fake)
				captures := countCaptures(t)

				tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

				if *captures != 1 {
					t.Errorf("captures = %d, want 1", *captures)
				}
			})
		}
	})

	t.Run("a streak captures once and a success opens the next", func(t *testing.T) {
		fake := newFakeTempVCManager()
		serverErr := restError(http.StatusInternalServerError, 0, "Internal Server Error")
		fake.createErr = serverErr
		tv := newSeededTempVC(t, fake)
		captures := countCaptures(t)

		tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))
		tv.handleVoiceStateUpdate(voiceEvent("user-2", testTempVCHub, member("B")))
		if *captures != 1 {
			t.Fatalf("captures = %d after two failures, want 1", *captures)
		}

		fake.setCreateErr(nil)
		tv.handleVoiceStateUpdate(voiceEvent("user-1", "", member("A")))
		tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))
		tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member("A")))

		fake.setCreateErr(serverErr)
		tv.handleVoiceStateUpdate(voiceEvent("user-2", "", member("B")))
		tv.handleVoiceStateUpdate(voiceEvent("user-2", testTempVCHub, member("B")))
		if *captures != 2 {
			t.Errorf("captures = %d after a success and a new failure, want 2", *captures)
		}
	})

	t.Run("hubs streak independently", func(t *testing.T) {
		fake := newFakeTempVCManager()
		fake.channels["hub-2"] = &discordgo.Channel{ID: "hub-2", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice}
		fake.createErr = restError(http.StatusInternalServerError, 0, "Internal Server Error")
		second := testHub()
		second.HubChannelID = "hub-2"
		second.BaseString = "Other"
		tv := newTestTempVC(t, fake, seedStore(t, testHub(), second))
		captures := countCaptures(t)

		tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))
		tv.handleVoiceStateUpdate(voiceEvent("user-2", testTempVCHub, member("B")))
		tv.handleVoiceStateUpdate(voiceEvent("user-3", "hub-2", member("C")))

		if *captures != 2 {
			t.Errorf("captures = %d, want 2: one streak per hub", *captures)
		}
	})
}

// setDeleteErr changes what the next deletes return, under the fake's lock.
func (f *fakeTempVCManager) setDeleteErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteErr = err
}

// spawnAndLeave drives a member through the hub into a fresh spawned channel
// and out again, so the runtime attempts one delete.
func spawnAndLeave(tv *TempVC, fake *fakeTempVCManager, userID, channelID string) {
	fake.setNextChannel(channelID)
	m := member(userID)
	tv.handleVoiceStateUpdate(voiceEvent(userID, testTempVCHub, m))
	tv.handleVoiceStateUpdate(voiceEvent(userID, channelID, m))
	tv.handleVoiceStateUpdate(voiceEvent(userID, "", m))
}

func TestTempVCDeleteFailuresClassify(t *testing.T) {
	t.Run("429 is a WARN line and the channel stays tracked", func(t *testing.T) {
		fake := newFakeTempVCManager()
		fake.deleteErr = restError(http.StatusTooManyRequests, 0, "You are being rate limited.")
		st := seedStore(t, testHub())
		tv := newTestTempVC(t, fake, st)
		captures := countCaptures(t)

		spawnAndLeave(tv, fake, "user-a", "chan-a")

		if *captures != 0 {
			t.Errorf("captures = %d, want 0", *captures)
		}
		if rows := spawnedRows(t, st); len(rows) != 1 {
			t.Fatalf("rows = %+v, want the row kept for the next attempt", rows)
		}
		fake.setDeleteErr(nil)
		tv.handleGuildCreate(guildCreate([]*discordgo.Channel{
			voiceChannel(testTempVCHub, testTempVCCategory, "Hub"),
			voiceChannel("chan-a", testTempVCCategory, "Voice - 1"),
		}))
		if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "chan-a" {
			t.Errorf("deleted = %v, want chan-a on the sweep's retry", ids)
		}
	})

	t.Run("403 captures once per streak and a success opens the next", func(t *testing.T) {
		fake := newFakeTempVCManager()
		forbidden := restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, "Missing Permissions")
		fake.deleteErr = forbidden
		st := seedStore(t, testHub())
		tv := newTestTempVC(t, fake, st)
		captures := countCaptures(t)

		spawnAndLeave(tv, fake, "user-a", "chan-a")
		spawnAndLeave(tv, fake, "user-b", "chan-b")
		if *captures != 1 {
			t.Fatalf("captures = %d after two failed deletes, want 1", *captures)
		}
		if rows := spawnedRows(t, st); len(rows) != 2 {
			t.Fatalf("rows = %+v, want both kept", rows)
		}

		fake.setDeleteErr(nil)
		tv.handleGuildCreate(guildCreate([]*discordgo.Channel{
			voiceChannel(testTempVCHub, testTempVCCategory, "Hub"),
			voiceChannel("chan-a", testTempVCCategory, "Voice - 1"),
			voiceChannel("chan-b", testTempVCCategory, "Voice - 2"),
		}))
		if rows := spawnedRows(t, st); len(rows) != 0 {
			t.Fatalf("rows = %+v, want none once the deletes go through", rows)
		}

		fake.setDeleteErr(forbidden)
		spawnAndLeave(tv, fake, "user-c", "chan-c")
		if *captures != 2 {
			t.Errorf("captures = %d after a success and a new failure, want 2", *captures)
		}
	})

	t.Run("transport captures", func(t *testing.T) {
		fake := newFakeTempVCManager()
		fake.deleteErr = errors.New("dial tcp: connection refused")
		tv := newSeededTempVC(t, fake)
		captures := countCaptures(t)

		spawnAndLeave(tv, fake, "user-a", "chan-a")

		if *captures != 1 {
			t.Errorf("captures = %d, want 1", *captures)
		}
	})
}

func TestTempVCCapFailureRecordsTheFullCause(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.createErr = restError(http.StatusBadRequest, discordgo.ErrCodeMaximumNumberOfGuildChannelsReached, "Maximum number of guild channels reached (500)")
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	countCaptures(t)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("A")))

	got, ok := tv.LastSpawnFailure(storedHubID(t, st))
	if !ok || got.Cause != SpawnFailureFull {
		t.Errorf("LastSpawnFailure = %+v (present %v), want cause %q", got, ok, SpawnFailureFull)
	}
}

func TestTempVCCreatorWithoutRankRoleMeansNoOwner(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	spawnInto(tv, fake, "guest-1", "new-chan", member("Guest"))

	if owner, tracked := tv.Owner("new-chan"); owner != "" || !tracked {
		t.Errorf("Owner(new-chan) = %q, %v, want no owner, tracked", owner, tracked)
	}
	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "" {
		t.Errorf("row = %+v (present %v), want an empty owner", row, ok)
	}
	notices := noticesIn(t, fake, "new-chan")
	if len(notices) != 1 {
		t.Fatalf("notices in new-chan = %d, want one at create", len(notices))
	}
	if strings.Contains(notices[0].data.Content, "guest-1") {
		t.Errorf("notice %q names the guest creator, want no user ID", notices[0].data.Content)
	}
}

func TestTempVCOwnerLeavesHighestRankTakesOver(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	spawnInto(tv, fake, "owner", "new-chan", member("Sgt", testRankSGT))
	tv.handleVoiceStateUpdate(voiceEvent("g-pvt", "new-chan", member("Pvt", testRankPVT)))
	tv.handleVoiceStateUpdate(voiceEvent("g-cpt", "new-chan", member("Cpt", testRankCPT)))
	if notices := noticesIn(t, fake, "new-chan"); len(notices) != 1 {
		t.Fatalf("notices = %d while the owner is present, want the create notice alone", len(notices))
	}

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("Sgt", testRankSGT)))

	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "g-cpt" {
		t.Errorf("row = %+v (present %v), want the CPT as owner", row, ok)
	}
	notices := noticesIn(t, fake, "new-chan")
	if len(notices) != 2 {
		t.Fatalf("notices = %d, want a second one for the handover", len(notices))
	}
	if !strings.Contains(notices[1].data.Content, "g-cpt") {
		t.Errorf("handover notice %q does not carry the CPT's ID", notices[1].data.Content)
	}
}

// A channel whose owner leaves with no rank holder present has no owner. That
// is the none outcome inside CONTEXT.md's Handover entry ("with no such
// occupant the channel has no owner"), so it is a handover: a row write with
// an empty owner and a notice saying so.
func TestTempVCOwnerLeavesWithNoRankHolderMeansNoOwner(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	// IDs that no notice wording can contain by accident.
	spawnInto(tv, fake, "sgt-1", "new-chan", member("Sgt", testRankSGT))
	tv.handleVoiceStateUpdate(voiceEvent("guest-9", "new-chan", member("Guest")))

	tv.handleVoiceStateUpdate(voiceEvent("sgt-1", "", member("Sgt", testRankSGT)))

	if owner, tracked := tv.Owner("new-chan"); owner != "" || !tracked {
		t.Errorf("Owner(new-chan) = %q, %v, want no owner, tracked", owner, tracked)
	}
	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "" {
		t.Errorf("row = %+v (present %v), want an empty owner", row, ok)
	}
	notices := noticesIn(t, fake, "new-chan")
	if len(notices) != 2 {
		t.Fatalf("notices = %d, want a second one saying there is no owner", len(notices))
	}
	if c := notices[1].data.Content; strings.Contains(c, "sgt-1") || strings.Contains(c, "guest-9") {
		t.Errorf("notice %q names an occupant, want no user ID", c)
	}
}

func TestTempVCTieBreaksByLowestUserID(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	// Two SGTs. The lowest user ID is the lowest snowflake: the 17 digit ID
	// is the smaller number although it sorts after the 18 digit one as a
	// string. The higher ID joins first, so taking the first occupant loses.
	const older, newer = "99999999999999999", "100000000000000000"
	spawnInto(tv, fake, "cpt-1", "new-chan", member("Cpt", testRankCPT))
	tv.handleVoiceStateUpdate(voiceEvent(newer, "new-chan", member("Sgt", testRankSGT)))
	tv.handleVoiceStateUpdate(voiceEvent(older, "new-chan", member("Sgt", testRankSGT)))

	tv.handleVoiceStateUpdate(voiceEvent("cpt-1", "", member("Cpt", testRankCPT)))

	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != older {
		t.Errorf("row = %+v (present %v), want the lowest snowflake %s", row, ok, older)
	}
}

func TestTempVCHandoverIsFinal(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	// The creator outranks the SGT, so a rule that re-elected on every
	// occupancy change would hand the channel back when they return.
	spawnInto(tv, fake, "cpt-1", "new-chan", member("Cpt", testRankCPT))
	tv.handleVoiceStateUpdate(voiceEvent("sgt-1", "new-chan", member("Sgt", testRankSGT)))
	tv.handleVoiceStateUpdate(voiceEvent("cpt-1", "", member("Cpt", testRankCPT)))
	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "sgt-1" {
		t.Fatalf("row = %+v (present %v), want the SGT after the creator left", row, ok)
	}

	tv.handleVoiceStateUpdate(voiceEvent("cpt-1", "new-chan", member("Cpt", testRankCPT)))

	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "sgt-1" {
		t.Errorf("row = %+v (present %v), want the SGT kept: a returning creator is an ordinary occupant", row, ok)
	}
	if notices := noticesIn(t, fake, "new-chan"); len(notices) != 2 {
		t.Errorf("notices = %d, want two (create and one handover), none for the return", len(notices))
	}
}

func TestTempVCRankHolderJoiningOwnerlessChannelTakesOver(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)

	spawnInto(tv, fake, "guest-1", "new-chan", member("Guest"))

	tv.handleVoiceStateUpdate(voiceEvent("pvt-1", "new-chan", member("Pvt", testRankPVT)))

	if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "pvt-1" {
		t.Errorf("row = %+v (present %v), want the PVT as owner", row, ok)
	}
	notices := noticesIn(t, fake, "new-chan")
	if len(notices) != 2 {
		t.Fatalf("notices = %d, want a second one for the handover", len(notices))
	}
	if !strings.Contains(notices[1].data.Content, "pvt-1") {
		t.Errorf("handover notice %q does not carry the PVT's ID", notices[1].data.Content)
	}
}

func TestTempVCHandoverRowWriteHealsAndCaptures(t *testing.T) {
	t.Run("the handover upsert heals a channel whose create write failed", func(t *testing.T) {
		fake := newFakeTempVCManager()
		st := &failingStore{Fake: seedStore(t, testHub()), upsertSpawnedErr: errors.New("connection reset")}
		tv := newTestTempVC(t, fake, st)
		countCaptures(t)

		spawnInto(tv, fake, "sgt-1", "new-chan", member("Sgt", testRankSGT))
		if _, ok := rowFor(t, st, "new-chan"); ok {
			t.Fatal("row present although the create write failed")
		}
		st.upsertSpawnedErr = nil
		tv.handleVoiceStateUpdate(voiceEvent("pvt-1", "new-chan", member("Pvt", testRankPVT)))

		tv.handleVoiceStateUpdate(voiceEvent("sgt-1", "", member("Sgt", testRankSGT)))

		row, ok := rowFor(t, st, "new-chan")
		if !ok || row.HubID != storedHubID(t, st) || row.Number != 1 || row.OwnerUserID != "pvt-1" {
			t.Errorf("row = %+v (present %v), want the hub's ID, number 1 and owner pvt-1", row, ok)
		}
	})

	t.Run("a failed handover write captures and the channel stays tracked", func(t *testing.T) {
		fake := newFakeTempVCManager()
		st := &failingStore{Fake: seedStore(t, testHub())}
		tv := newTestTempVC(t, fake, st)
		captures := countCaptures(t)

		spawnInto(tv, fake, "sgt-1", "new-chan", member("Sgt", testRankSGT))
		tv.handleVoiceStateUpdate(voiceEvent("pvt-1", "new-chan", member("Pvt", testRankPVT)))
		st.upsertSpawnedErr = errors.New("connection reset")

		tv.handleVoiceStateUpdate(voiceEvent("sgt-1", "", member("Sgt", testRankSGT)))

		if *captures != 1 {
			t.Errorf("captures = %d, want 1 for the failed handover write", *captures)
		}
		// The live owner is the PVT although the row still names the SGT.
		if owner, tracked := tv.Owner("new-chan"); owner != "pvt-1" || !tracked {
			t.Errorf("Owner(new-chan) = %q, %v, want pvt-1, tracked", owner, tracked)
		}
		if row, ok := rowFor(t, st, "new-chan"); !ok || row.OwnerUserID != "sgt-1" {
			t.Errorf("row = %+v (present %v), want the stale create row kept", row, ok)
		}
		// Still tracked: the last occupant leaving deletes the channel.
		tv.handleVoiceStateUpdate(voiceEvent("pvt-1", "", member("Pvt", testRankPVT)))
		if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
			t.Errorf("deleted = %v, want new-chan once it emptied", ids)
		}
	})
}

// guildMember builds a member with a user ID and optional rank role IDs, for
// the member list a GUILD_CREATE payload carries.
func guildMember(userID string, roleIDs ...string) *discordgo.Member {
	return &discordgo.Member{User: &discordgo.User{ID: userID}, Roles: roleIDs}
}

// The sweep restores the owner from the row when they are present. An absent
// owner counts as having left, and a row with no owner is a channel with no
// owner, so both elect by the handover rule from the occupants, whose roles
// come from the payload's member list. CONTEXT.md's Restart sweep entry: an
// occupied channel's owner is "restored or elected by the handover rule".
func TestTempVCRestartSweepRestoresOwnerOrElects(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	ctx := context.Background()
	hubID := storedHubID(t, st)
	for _, row := range []store.SpawnedChannel{
		{ChannelID: "chan-p", HubID: hubID, Number: 1, OwnerUserID: "pvt-p"},
		{ChannelID: "chan-q", HubID: hubID, Number: 2, OwnerUserID: "gone-q"},
		{ChannelID: "chan-r", HubID: hubID, Number: 3},
	} {
		if err := st.UpsertSpawnedChannel(ctx, row); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}
	}
	tv := newTestTempVC(t, fake, st)

	g := guildCreate(
		[]*discordgo.Channel{
			voiceChannel(testTempVCHub, testTempVCCategory, "Hub"),
			voiceChannel("chan-p", testTempVCCategory, "Voice - 1"),
			voiceChannel("chan-q", testTempVCCategory, "Voice - 2"),
			voiceChannel("chan-r", testTempVCCategory, "Voice - 3"),
		},
		&discordgo.VoiceState{UserID: "pvt-p", ChannelID: "chan-p"},
		&discordgo.VoiceState{UserID: "sgt-p", ChannelID: "chan-p"},
		&discordgo.VoiceState{UserID: "guest-q", ChannelID: "chan-q"},
		&discordgo.VoiceState{UserID: "sgt-q", ChannelID: "chan-q"},
		&discordgo.VoiceState{UserID: "cpt-r", ChannelID: "chan-r"},
	)
	g.Members = []*discordgo.Member{
		guildMember("pvt-p", testRankPVT),
		guildMember("sgt-p", testRankSGT),
		guildMember("guest-q"),
		guildMember("sgt-q", testRankSGT),
		guildMember("cpt-r", testRankCPT),
	}
	tv.handleGuildCreate(g)

	// P: the row's owner is present and keeps the channel, outranked or not.
	if owner, tracked := tv.Owner("chan-p"); owner != "pvt-p" || !tracked {
		t.Errorf("Owner(chan-p) = %q, %v, want the row's owner pvt-p restored", owner, tracked)
	}
	if notices := noticesIn(t, fake, "chan-p"); len(notices) != 0 {
		t.Errorf("notices in chan-p = %d, want none: a restored owner is no handover", len(notices))
	}

	// Q: the row's owner is absent, so the SGT takes over, ahead of the guest.
	if row, ok := rowFor(t, st, "chan-q"); !ok || row.OwnerUserID != "sgt-q" {
		t.Errorf("chan-q row = %+v (present %v), want owner sgt-q", row, ok)
	}
	if notices := noticesIn(t, fake, "chan-q"); len(notices) != 1 || !strings.Contains(notices[0].data.Content, "sgt-q") {
		t.Errorf("notices in chan-q = %+v, want one carrying sgt-q", notices)
	}

	// R: no owner in the row, a CPT present: they take over.
	if row, ok := rowFor(t, st, "chan-r"); !ok || row.OwnerUserID != "cpt-r" {
		t.Errorf("chan-r row = %+v (present %v), want owner cpt-r", row, ok)
	}
	if notices := noticesIn(t, fake, "chan-r"); len(notices) != 1 || !strings.Contains(notices[0].data.Content, "cpt-r") {
		t.Errorf("notices in chan-r = %+v, want one carrying cpt-r", notices)
	}
}
