package commands

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// fakeTempVCManager records calls and injects per-call errors. Mutex-guarded:
// grace timers invoke ChannelDelete from timer goroutines while the test
// asserts, and the suite runs under -race.
type fakeTempVCManager struct {
	mu sync.Mutex

	created     []discordgo.GuildChannelCreateData
	deleted     []string
	moves       []fakeMove
	edits       []fakeEdit
	messages    []fakeMessage
	permSets    []fakePermSet
	permDeletes []fakePermDelete
	memberByID  map[string]*discordgo.Member
	channelByID map[string]*discordgo.Channel
	nextChannel *discordgo.Channel

	deleteCalls int

	createErr  error
	deleteErr  error
	moveErr    error
	memberErr  error
	channelErr error
	editErr    error
	messageErr error
	permErr    error
}

type fakePermSet struct {
	channelID  string
	targetID   string
	targetType discordgo.PermissionOverwriteType
	allow      int64
	deny       int64
}

type fakePermDelete struct {
	channelID string
	targetID  string
}

type fakeMove struct {
	userID    string
	channelID *string
}

type fakeEdit struct {
	channelID string
	name      string
}

type fakeMessage struct {
	channelID string
	content   string
}

func newFakeTempVCManager() *fakeTempVCManager {
	return &fakeTempVCManager{
		memberByID:  map[string]*discordgo.Member{},
		channelByID: map[string]*discordgo.Channel{},
		nextChannel: &discordgo.Channel{ID: "new-chan", Name: "created"},
	}
}

func (f *fakeTempVCManager) GuildChannelCreateComplex(_ string, data discordgo.GuildChannelCreateData) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, data)
	ch := *f.nextChannel
	ch.Name = data.Name
	// Register the created channel so a later Channel(id) read reflects the
	// name the bot assigned, the live-name comparison the retro-rename does.
	reg := ch
	f.channelByID[ch.ID] = &reg
	return &ch, nil
}

func (f *fakeTempVCManager) Channel(channelID string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.channelErr != nil {
		return nil, f.channelErr
	}
	if ch, ok := f.channelByID[channelID]; ok {
		chCopy := *ch
		return &chCopy, nil
	}
	return nil, errors.New("channel not found")
}

func (f *fakeTempVCManager) ChannelEdit(channelID string, data *discordgo.ChannelEdit) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return nil, f.editErr
	}
	f.edits = append(f.edits, fakeEdit{channelID: channelID, name: data.Name})
	if ch, ok := f.channelByID[channelID]; ok {
		ch.Name = data.Name
	} else {
		f.channelByID[channelID] = &discordgo.Channel{ID: channelID, Name: data.Name}
	}
	return f.channelByID[channelID], nil
}

func (f *fakeTempVCManager) ChannelDelete(channelID string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, channelID)
	return &discordgo.Channel{ID: channelID}, nil
}

func (f *fakeTempVCManager) deleteCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleteCalls
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

func (f *fakeTempVCManager) GuildMember(_ string, userID string) (*discordgo.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memberErr != nil {
		return nil, f.memberErr
	}
	if m, ok := f.memberByID[userID]; ok {
		return m, nil
	}
	return nil, errors.New("member not found")
}

func (f *fakeTempVCManager) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.deleted))
	copy(out, f.deleted)
	return out
}

func (f *fakeTempVCManager) createdData() []discordgo.GuildChannelCreateData {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]discordgo.GuildChannelCreateData, len(f.created))
	copy(out, f.created)
	return out
}

func (f *fakeTempVCManager) recordedMoves() []fakeMove {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeMove, len(f.moves))
	copy(out, f.moves)
	return out
}

func (f *fakeTempVCManager) recordedEdits() []fakeEdit {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeEdit, len(f.edits))
	copy(out, f.edits)
	return out
}

func (f *fakeTempVCManager) ChannelMessageSend(channelID, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.messageErr != nil {
		return f.messageErr
	}
	f.messages = append(f.messages, fakeMessage{channelID: channelID, content: content})
	return nil
}

func (f *fakeTempVCManager) recordedMessages() []fakeMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeMessage, len(f.messages))
	copy(out, f.messages)
	return out
}

func (f *fakeTempVCManager) ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.permErr != nil {
		return f.permErr
	}
	f.permSets = append(f.permSets, fakePermSet{channelID: channelID, targetID: targetID, targetType: targetType, allow: allow, deny: deny})
	return nil
}

func (f *fakeTempVCManager) ChannelPermissionDelete(channelID, targetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.permErr != nil {
		return f.permErr
	}
	f.permDeletes = append(f.permDeletes, fakePermDelete{channelID: channelID, targetID: targetID})
	return nil
}

func (f *fakeTempVCManager) recordedPermSets() []fakePermSet {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakePermSet, len(f.permSets))
	copy(out, f.permSets)
	return out
}

func (f *fakeTempVCManager) recordedPermDeletes() []fakePermDelete {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakePermDelete, len(f.permDeletes))
	copy(out, f.permDeletes)
	return out
}

const (
	testTempVCGuild    = "guild-1"
	testTempVCHub      = "hub-1"
	testTempVCCategory = "cat-1"
	testTempVCLog      = "log-1"
)

func newTestTempVC(mgr TempVCManager, grace time.Duration) *tempVC {
	return newTempVC(mgr, TempVCConfig{
		GuildID:      testTempVCGuild,
		LogChannelID: testTempVCLog,
		Hubs: []tempVCHub{{
			HubChannelID: testTempVCHub,
			CategoryID:   testTempVCCategory,
			Grace:        grace,
		}},
	})
}

// hubMessages / logMessages split recordedMessages by destination channel so a
// test can assert the user-facing hub notice and the audit trail separately.
func (f *fakeTempVCManager) hubMessages() []fakeMessage {
	var out []fakeMessage
	for _, m := range f.recordedMessages() {
		if m.channelID == testTempVCHub {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeTempVCManager) logMessages() []fakeMessage {
	var out []fakeMessage
	for _, m := range f.recordedMessages() {
		if m.channelID == testTempVCLog {
			out = append(out, m)
		}
	}
	return out
}

func voiceEvent(userID, channelID string, member *discordgo.Member) *discordgo.VoiceStateUpdate {
	return &discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID:   testTempVCGuild,
		UserID:    userID,
		ChannelID: channelID,
		Member:    member,
	}}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestLoadTempVCConfig(t *testing.T) {
	// The hubs and log channel are hardcoded tenant identifiers, so the feature
	// is always enabled and the config is built from the hardcoded table, no env
	// input.
	cfg, ok := LoadTempVCConfig("g")
	if !ok {
		t.Fatal("ok = false, want true (feature is always enabled with hardcoded IDs)")
	}
	if cfg.GuildID != "g" {
		t.Errorf("guild = %q, want %q", cfg.GuildID, "g")
	}
	if cfg.LogChannelID != tempVCLogChannelID {
		t.Errorf("log channel = %q, want %q", cfg.LogChannelID, tempVCLogChannelID)
	}
	if len(cfg.Hubs) != len(tempVCHubs) || len(cfg.Hubs) == 0 {
		t.Fatalf("hubs = %d, want the hardcoded %d (>=1)", len(cfg.Hubs), len(tempVCHubs))
	}
	for i, h := range cfg.Hubs {
		if h != tempVCHubs[i] {
			t.Errorf("hub[%d] = %+v, want %+v", i, h, tempVCHubs[i])
		}
		if h.HubChannelID == "" || h.CategoryID == "" || h.Grace <= 0 {
			t.Errorf("hub[%d] has an empty ID or non-positive grace: %+v", i, h)
		}
	}
}

func TestMustTempVCHubsRejectsMisconfig(t *testing.T) {
	cases := []struct {
		name string
		hubs []tempVCHub
	}{
		{"empty table", nil},
		{"empty hub id", []tempVCHub{{HubChannelID: "", CategoryID: "c", Grace: time.Second}}},
		{"empty category id", []tempVCHub{{HubChannelID: "h", CategoryID: "", Grace: time.Second}}},
		{"hub equals category", []tempVCHub{{HubChannelID: "x", CategoryID: "x", Grace: time.Second}}},
		{"non-positive grace", []tempVCHub{{HubChannelID: "h", CategoryID: "c", Grace: 0}}},
		{"negative user limit", []tempVCHub{{HubChannelID: "h", CategoryID: "c", Grace: time.Second, UserLimit: -1}}},
		{"duplicate hub", []tempVCHub{
			{HubChannelID: "h", CategoryID: "c1", Grace: time.Second},
			{HubChannelID: "h", CategoryID: "c2", Grace: time.Second},
		}},
		{"duplicate category", []tempVCHub{
			{HubChannelID: "h1", CategoryID: "c", Grace: time.Second},
			{HubChannelID: "h2", CategoryID: "c", Grace: time.Second},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("mustTempVCHubs(%+v) did not panic", tc.hubs)
				}
			}()
			mustTempVCHubs(tc.hubs)
		})
	}

	// A well-formed multi-hub table is accepted and returned unchanged.
	good := []tempVCHub{
		{HubChannelID: "h1", CategoryID: "c1", Grace: time.Second},
		{HubChannelID: "h2", CategoryID: "c2", UserLimit: 9, Bitrate: 96000, Grace: 30 * time.Second},
	}
	if got := mustTempVCHubs(good); len(got) != 2 {
		t.Fatalf("mustTempVCHubs returned %d hubs, want 2", len(got))
	}
}

func TestTempVCHubJoinCreatesOwnedChannelAndMovesUser(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	member := &discordgo.Member{Nick: "CPL Smith.J", User: &discordgo.User{Username: "smithy"}}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))

	created := fake.createdData()
	if len(created) != 1 {
		t.Fatalf("created %d channels, want 1", len(created))
	}
	data := created[0]
	if data.Name != "CPL Smith.J's Channel" {
		t.Errorf("name = %q", data.Name)
	}
	if data.Type != discordgo.ChannelTypeGuildVoice {
		t.Errorf("type = %v", data.Type)
	}
	if data.ParentID != testTempVCCategory {
		t.Errorf("parent = %q", data.ParentID)
	}
	if len(data.PermissionOverwrites) != 1 {
		t.Fatalf("overwrites = %d, want 1", len(data.PermissionOverwrites))
	}
	ow := data.PermissionOverwrites[0]
	if ow.ID != "user-1" || ow.Type != discordgo.PermissionOverwriteTypeMember {
		t.Errorf("overwrite target = %+v", ow)
	}
	wantAllow := int64(discordgo.PermissionManageChannels | discordgo.PermissionVoiceMoveMembers | discordgo.PermissionManageRoles)
	if ow.Allow != wantAllow {
		t.Errorf("allow = %d, want %d", ow.Allow, wantAllow)
	}

	moves := fake.recordedMoves()
	if len(moves) != 1 || moves[0].userID != "user-1" || moves[0].channelID == nil || *moves[0].channelID != "new-chan" {
		t.Fatalf("moves = %+v", moves)
	}

	tv.mu.Lock()
	defer tv.mu.Unlock()
	if tv.owners["new-chan"] != "user-1" {
		t.Errorf("owner = %q", tv.owners["new-chan"])
	}
	if _, ok := tv.occupants["new-chan"]; !ok {
		t.Error("new channel not tracked in occupants")
	}
}

func TestTempVCChannelNameFallbacks(t *testing.T) {
	t.Run("nil member falls back to REST fetch", func(t *testing.T) {
		fake := newFakeTempVCManager()
		fake.memberByID["user-2"] = &discordgo.Member{Nick: "SGT Jones.K"}
		tv := newTestTempVC(fake, time.Hour)

		tv.handleVoiceStateUpdate(voiceEvent("user-2", testTempVCHub, nil))
		created := fake.createdData()
		if len(created) != 1 || created[0].Name != "SGT Jones.K's Channel" {
			t.Fatalf("created = %+v", created)
		}
	})

	t.Run("no nick uses username, first letter capitalized", func(t *testing.T) {
		fake := newFakeTempVCManager()
		tv := newTestTempVC(fake, time.Hour)
		member := &discordgo.Member{User: &discordgo.User{Username: "plainuser"}}

		tv.handleVoiceStateUpdate(voiceEvent("user-3", testTempVCHub, member))
		created := fake.createdData()
		if len(created) != 1 || created[0].Name != "Plainuser's Channel" {
			t.Fatalf("created = %+v", created)
		}
	})

	t.Run("lowercase nick is cased to Last.F", func(t *testing.T) {
		fake := newFakeTempVCManager()
		tv := newTestTempVC(fake, time.Hour)
		member := &discordgo.Member{Nick: "cpl smith.j"}

		tv.handleVoiceStateUpdate(voiceEvent("user-6", testTempVCHub, member))
		created := fake.createdData()
		if len(created) != 1 || created[0].Name != "CPL Smith.J's Channel" {
			t.Fatalf("created = %+v, want CPL Smith.J's Channel", created)
		}
	})

	t.Run("multi-initial group is fully upper-cased", func(t *testing.T) {
		fake := newFakeTempVCManager()
		tv := newTestTempVC(fake, time.Hour)
		member := &discordgo.Member{Nick: "1lt.smith.jw"}

		tv.handleVoiceStateUpdate(voiceEvent("user-7", testTempVCHub, member))
		created := fake.createdData()
		if len(created) != 1 || created[0].Name != "1LT Smith.JW's Channel" {
			t.Fatalf("created = %+v, want 1LT Smith.JW's Channel", created)
		}
	})

	t.Run("fetch failure uses Trooper fallback", func(t *testing.T) {
		fake := newFakeTempVCManager()
		fake.memberErr = errors.New("boom")
		tv := newTestTempVC(fake, time.Hour)

		tv.handleVoiceStateUpdate(voiceEvent("user-4", testTempVCHub, nil))
		created := fake.createdData()
		if len(created) != 1 || created[0].Name != "Trooper's Channel" {
			t.Fatalf("created = %+v", created)
		}
	})

	t.Run("long nick truncated to discord limit", func(t *testing.T) {
		fake := newFakeTempVCManager()
		tv := newTestTempVC(fake, time.Hour)
		member := &discordgo.Member{Nick: strings.Repeat("x", 120)}

		tv.handleVoiceStateUpdate(voiceEvent("user-5", testTempVCHub, member))
		created := fake.createdData()
		if len(created) != 1 || len(created[0].Name) != discordChannelNameLimit {
			t.Fatalf("name length = %d, want %d", len(created[0].Name), discordChannelNameLimit)
		}
	})
}

func TestSplitNick(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // the "<rankFromNick> <nameCore>" composition
	}{
		// Canonical and separator drift all normalize to "<RANK> <Last>.<F>".
		{"canonical dotted", "1LT.Laui.M", "1LT Laui.M"},
		{"space separator", "1LT Laui.M", "1LT Laui.M"},
		{"dot-space separator", "1LT. Laui.M", "1LT Laui.M"},
		{"existing convention CPL", "CPL Smith.J", "CPL Smith.J"},
		// Trailing callsign and unauthorized suffixes are dropped: quoted,
		// bracketed, a known suffix keyword, or pure-symbol decoration.
		{"aviation callsign dropped", "1LT.Laui.M \"Bobo\"", "1LT Laui.M"},
		{"LOA suffix dropped", "1LT.Laui.M LOA", "1LT Laui.M"},
		{"cadre suffix dropped", "1LT.Laui.M <cadre>", "1LT Laui.M"},
		{"callsign on space-separated nick", "SGT Jones.K \"Ghost\"", "SGT Jones.K"},
		{"trailing emoji dropped", "CPL Smith.J 🐎", "CPL Smith.J"},
		{"multiple trailers dropped", "1LT.Laui.M \"Bobo\" <cadre>", "1LT Laui.M"},
		// A multi-word name is KEPT, not collapsed to its first token (the
		// "Sgt Major Smith" case), only recognized trailers are stripped.
		{"multi-word name kept", "Sgt Major Smith", "SGT Major Smith"},
		{"multi-word name with callsign", "SGT Major Smith \"Doc\"", "SGT Major Smith"},
		// Case-insensitive rank match; name casing preserved.
		{"lowercase rank", "1lt.laui.m", "1LT laui.m"},
		// The 0/O look-alike typo is tolerated and normalized to the canonical rank.
		{"WO1 zero typo", "W01.Laui.M", "WO1 Laui.M"},
		{"COL zero typo", "C0L.Smith.J", "COL Smith.J"},
		// Multi-character enlisted / warrant ranks.
		{"warrant rank", "CW3.Rivera.T", "CW3 Rivera.T"},
		{"recruit rank", "RCT.Newguy.A", "RCT Newguy.A"},
		// Non-parsing inputs fall back to the trimmed raw display name.
		{"no rank plain username", "plainuser", "plainuser"},
		{"unknown leading token", "XYZ.Someone.B", "XYZ.Someone.B"},
		{"bare rank only", "1LT", "1LT"},
		{"empty", "", ""},
		{"whitespace trimmed", "  1LT.Laui.M  ", "1LT Laui.M"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rank, name := splitNick(tc.in)
			got := strings.TrimSpace(rank + " " + name)
			if got != tc.want {
				t.Errorf("splitNick(%q) = (%q, %q) -> %q, want %q", tc.in, rank, name, got, tc.want)
			}
		})
	}
}

func TestSuffixNumber(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"b's Channel", 1},
		{"b's Channel (2)", 2},
		{"1LT Laui.M's Channel (10)", 10},
		{"Bob (the builder)'s Channel", 1}, // trailing is not " (n)"
		{"X (0)", 1},                       // (0) is not a valid slot
		{"X (-1)", 1},
	}
	for _, tc := range cases {
		if got := suffixNumber(tc.in); got != tc.want {
			t.Errorf("suffixNumber(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestTempVCChannelIndexAvoidsDuplicateAfterDelete(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	// User owns 4 channels: unsuffixed (slot 1), (2), (3), (4).
	tv.mu.Lock()
	for _, c := range []struct{ id, name string }{
		{"c1", "b's Channel"},
		{"c2", "b's Channel (2)"},
		{"c3", "b's Channel (3)"},
		{"c4", "b's Channel (4)"},
	} {
		tv.owners[c.id] = "b"
		tv.occupants[c.id] = map[string]struct{}{}
		tv.baseName[c.id] = "b's Channel"
		tv.assignedName[c.id] = c.name
	}
	// Delete the unsuffixed channel, freeing slot 1.
	delete(tv.owners, "c1")
	delete(tv.occupants, "c1")
	delete(tv.assignedName, "c1")
	delete(tv.baseName, "c1")
	idx := tv.nextChannelIndexLocked("b")
	tv.mu.Unlock()

	// The freed slot 1 is reused; it must NOT be 4 (which would duplicate the
	// surviving "(4)"), the count-based bug that minted two "(4)" channels.
	if idx != 1 {
		t.Fatalf("next index = %d, want 1 (reuse freed slot, never collide with a live number)", idx)
	}

	// With slots 1..4 all in use, the next is 5.
	tv.mu.Lock()
	tv.owners["c1b"] = "b"
	tv.occupants["c1b"] = map[string]struct{}{}
	tv.assignedName["c1b"] = "b's Channel"
	idx2 := tv.nextChannelIndexLocked("b")
	tv.mu.Unlock()
	if idx2 != 5 {
		t.Fatalf("next index = %d, want 5 (slots 1-4 all in use)", idx2)
	}
}

func TestTempVCDemoteSoleChannelOnDelete(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	// Owner has two channels, (1) and (2), both empty.
	tv.mu.Lock()
	for _, c := range []struct{ id, name string }{
		{"c1", "b's Channel (1)"},
		{"c2", "b's Channel (2)"},
	} {
		tv.owners[c.id] = "b"
		tv.occupants[c.id] = map[string]struct{}{}
		tv.baseName[c.id] = "b's Channel"
		tv.assignedName[c.id] = c.name
	}
	tv.mu.Unlock()
	fake.mu.Lock()
	fake.channelByID["c1"] = &discordgo.Channel{ID: "c1", Name: "b's Channel (1)"}
	fake.channelByID["c2"] = &discordgo.Channel{ID: "c2", Name: "b's Channel (2)"}
	fake.mu.Unlock()

	// c1 is deleted; the owner is down to one channel (c2), which must be
	// demoted from "(2)" back to the unnumbered base name.
	tv.deleteIfStillEmpty("c1")

	edits := fake.recordedEdits()
	if len(edits) != 1 || edits[0].channelID != "c2" || edits[0].name != "b's Channel" {
		t.Fatalf("edits = %+v, want c2 demoted to \"b's Channel\"", edits)
	}
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if tv.assignedName["c2"] != "b's Channel" {
		t.Errorf("assignedName[c2] = %q, want unnumbered base", tv.assignedName["c2"])
	}
}

func TestTempVCDemoteSkipsAlteredSoleChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	tv.mu.Lock()
	tv.owners["c1"] = "b"
	tv.occupants["c1"] = map[string]struct{}{}
	tv.baseName["c1"] = "b's Channel"
	tv.assignedName["c1"] = "b's Channel (1)"
	tv.owners["c2"] = "b"
	tv.occupants["c2"] = map[string]struct{}{}
	tv.baseName["c2"] = "b's Channel"
	tv.assignedName["c2"] = "b's Channel (2)"
	tv.mu.Unlock()
	fake.mu.Lock()
	fake.channelByID["c1"] = &discordgo.Channel{ID: "c1", Name: "b's Channel (1)"}
	fake.channelByID["c2"] = &discordgo.Channel{ID: "c2", Name: "War Room"} // owner-renamed
	fake.mu.Unlock()

	tv.deleteIfStillEmpty("c1")

	// The surviving channel was renamed by its owner, so its name is left alone.
	if edits := fake.recordedEdits(); len(edits) != 0 {
		t.Fatalf("edits = %+v, want none (altered channel must not be demoted)", edits)
	}
}

func TestTempVCCapDisconnectsFifthJoin(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	tv.mu.Lock()
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		tv.owners[id] = "host-1"
		tv.occupants[id] = map[string]struct{}{"filler": {}}
	}
	tv.mu.Unlock()

	tv.handleVoiceStateUpdate(voiceEvent("host-1", testTempVCHub, &discordgo.Member{Nick: "Host"}))

	if created := fake.createdData(); len(created) != 0 {
		t.Fatalf("created %d channels at cap, want 0", len(created))
	}
	moves := fake.recordedMoves()
	if len(moves) != 1 || moves[0].userID != "host-1" || moves[0].channelID != nil {
		t.Fatalf("moves = %+v, want single nil-channel disconnect", moves)
	}

	// The over-cap joiner is told why, in the hub chat, tagged.
	hubMsgs := fake.hubMessages()
	if len(hubMsgs) != 1 {
		t.Fatalf("hub messages = %+v, want one notification", hubMsgs)
	}
	if !strings.Contains(hubMsgs[0].content, "<@host-1>") {
		t.Errorf("notification %q does not tag the user", hubMsgs[0].content)
	}
	if strings.ContainsRune(hubMsgs[0].content, '—') {
		t.Errorf("notification %q contains an em dash (user-facing copy must not)", hubMsgs[0].content)
	}
	// And the refusal is on the audit trail.
	logMsgs := fake.logMessages()
	if len(logMsgs) != 1 || !strings.Contains(logMsgs[0].content, "limit") {
		t.Fatalf("log messages = %+v, want one cap-limit audit line", logMsgs)
	}
}

func TestTempVCCapDisconnectFailureIsCaptured(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.moveErr = errors.New("gateway hiccup")
	tv := newTestTempVC(fake, time.Hour)

	tv.mu.Lock()
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		tv.owners[id] = "host-1"
	}
	tv.mu.Unlock()

	// Must not panic; error path logs + captures.
	tv.handleVoiceStateUpdate(voiceEvent("host-1", testTempVCHub, nil))
	if created := fake.createdData(); len(created) != 0 {
		t.Fatalf("created %d channels, want 0", len(created))
	}
}

func TestTempVCEmptyChannelDeletedAfterGrace(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, 5*time.Millisecond)

	member := &discordgo.Member{Nick: "Owner"}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))
	// Simulate the gateway reporting the move into the new channel...
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))
	// ...then the owner disconnecting.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "", member))

	eventually(t, func() bool {
		for _, id := range fake.deletedIDs() {
			if id == "new-chan" {
				return true
			}
		}
		return false
	}, "empty temp channel was not deleted after grace")

	tv.mu.Lock()
	defer tv.mu.Unlock()
	if _, ok := tv.occupants["new-chan"]; ok {
		t.Error("deleted channel still tracked")
	}
	if _, ok := tv.owners["new-chan"]; ok {
		t.Error("deleted channel still owned")
	}
}

func TestTempVCRejoinWithinGraceCancelsDeletion(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, 50*time.Millisecond)

	member := &discordgo.Member{Nick: "Owner"}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "", member))
	// Rejoin before the grace elapses.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))

	time.Sleep(120 * time.Millisecond)
	if ids := fake.deletedIDs(); len(ids) != 0 {
		t.Fatalf("deleted %v despite rejoin within grace", ids)
	}
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if _, ok := tv.occupants["new-chan"]; !ok {
		t.Error("rejoined channel no longer tracked")
	}
}

func TestTempVCOnlyLastLeaverTriggersDeletion(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, 5*time.Millisecond)

	owner := &discordgo.Member{Nick: "Owner"}
	guest := &discordgo.Member{Nick: "Guest"}
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", testTempVCHub, owner))
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", "new-chan", owner))
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "new-chan", guest))

	// Owner leaves; guest remains, no deletion.
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", "", owner))
	time.Sleep(30 * time.Millisecond)
	if ids := fake.deletedIDs(); len(ids) != 0 {
		t.Fatalf("deleted %v while occupied", ids)
	}

	// Guest leaves; now it empties and deletes.
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "", guest))
	eventually(t, func() bool { return len(fake.deletedIDs()) == 1 }, "channel not deleted after last leaver")
}

func TestTempVCMoveIntoFailureReapsChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.moveErr = errors.New("target user is not connected to voice")
	tv := newTestTempVC(fake, time.Hour)

	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, &discordgo.Member{Nick: "Gone"}))

	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Fatalf("deleted = %v, want immediate reap of new-chan", ids)
	}
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if _, ok := tv.occupants["new-chan"]; ok {
		t.Error("reaped channel still tracked")
	}
}

func TestTempVCCreateFailureIsCaptured(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.createErr = errors.New("missing permissions")
	tv := newTestTempVC(fake, time.Hour)

	// Must not panic and must not move anyone.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, nil))
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Fatalf("moves = %+v, want none after create failure", moves)
	}
}

func TestTempVCDeleteFailureKeepsChannelTracked(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.deleteErr = errors.New("HTTP 403 Missing Permissions")
	tv := newTestTempVC(fake, 5*time.Millisecond)

	member := &discordgo.Member{Nick: "Owner"}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "", member))

	// Wait for the grace timer to fire and attempt (and fail) the delete.
	eventually(t, func() bool { return fake.deleteCallCount() >= 1 }, "delete was never attempted")

	// A failed delete must leave the channel tracked and owned so it still
	// counts toward the owner's cap (it is still live in Discord). Dropping it
	// here is what let a user exceed the cap with zombie channels.
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if _, tracked := tv.occupants["new-chan"]; !tracked {
		t.Error("failed-delete channel dropped from occupants; would free a cap slot for a live channel")
	}
	if tv.owners["new-chan"] != "user-1" {
		t.Error("failed-delete channel dropped from owners; would free a cap slot for a live channel")
	}
}

func TestTempVCFailedDeleteKeepsCapEnforced(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.deleteErr = errors.New("HTTP 403 Missing Permissions") // deletes never succeed
	tv := newTestTempVC(fake, time.Hour)

	// Owner already holds 4 empty channels whose deletes will 403.
	tv.mu.Lock()
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		tv.owners[id] = "b"
		tv.occupants[id] = map[string]struct{}{}
		tv.baseName[id] = "b's Channel"
		tv.assignedName[id] = "b's Channel"
	}
	tv.mu.Unlock()

	// Fire each empty channel's delete; every one 403s, so each stays tracked.
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		tv.deleteIfStillEmpty(id)
	}
	tv.mu.Lock()
	remaining := 0
	for _, owner := range tv.owners {
		if owner == "b" {
			remaining++
		}
	}
	tv.mu.Unlock()
	if remaining != 4 {
		t.Fatalf("owned after 4 failed deletes = %d, want 4 (a failed delete must not free a slot)", remaining)
	}

	// A 5th hub join now correctly hits the cap: no channel created, joiner bounced.
	tv.handleVoiceStateUpdate(voiceEvent("b", testTempVCHub, &discordgo.Member{Nick: "b"}))
	if created := fake.createdData(); len(created) != 0 {
		t.Fatalf("created %d channels at cap, want 0", len(created))
	}
	if hub := fake.hubMessages(); len(hub) != 1 {
		t.Fatalf("hub notifications = %d, want 1 cap notice", len(hub))
	}
}

func TestTempVCAuditLogsLifecycle(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, 5*time.Millisecond)

	member := &discordgo.Member{Nick: "b"}
	// Owner joins hub -> channel created -> owner moved in (join event).
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", "new-chan", member))
	// Guest joins then leaves; owner leaves; channel empties and deletes.
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "new-chan", &discordgo.Member{Nick: "Guest"}))
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "", nil))
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", "", member))

	// The delete line is the last event; once it lands, the earlier lines
	// (create/join/leave, all logged synchronously) are already recorded.
	eventually(t, func() bool {
		for _, m := range fake.logMessages() {
			if strings.Contains(m.content, "Auto-deleted") {
				return true
			}
		}
		return false
	}, "channel deletion was not audit-logged")

	var b strings.Builder
	for _, m := range fake.logMessages() {
		b.WriteString(m.content)
		b.WriteByte('\n')
	}
	got := b.String()
	for _, want := range []string{
		"created **B's Channel**",           // create (by name, survives deletion)
		"<@owner-1> joined **B's Channel**", // owner moved in
		"<@guest-1> joined **B's Channel**", // guest joins
		"<@guest-1> left **B's Channel**",   // guest leaves
		"<@owner-1> left **B's Channel**",   // owner leaves
		"Auto-deleted **B's Channel**",      // channel deleted (by the bot)
	} {
		if !strings.Contains(got, want) {
			t.Errorf("audit log missing %q\nfull log:\n%s", want, got)
		}
	}
	// The create line carries the creator's raw discord id (the id-based record).
	if !strings.Contains(got, "`owner-1`") {
		t.Errorf("create audit line missing raw creator id; log:\n%s", got)
	}
	// No <#id> mentions (they render as "#unknown" once a channel is deleted).
	if strings.Contains(got, "<#") {
		t.Errorf("audit log uses a channel mention that goes stale on deletion:\n%s", got)
	}
	// Every line is prefixed with a Zulu timestamp.
	for _, m := range fake.logMessages() {
		if !strings.Contains(m.content, "Z` ") {
			t.Errorf("audit line missing Zulu timestamp prefix: %q", m.content)
		}
	}
}

func TestTempVCMuteToggleIsNoOp(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, 5*time.Millisecond)

	member := &discordgo.Member{Nick: "Owner"}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))

	// Same channel again = mute/deafen toggle; must not disturb tracking or
	// spawn a second channel even though the channel ID matches nothing new.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))
	time.Sleep(20 * time.Millisecond)

	if created := fake.createdData(); len(created) != 1 {
		t.Fatalf("created %d channels, want 1", len(created))
	}
	if ids := fake.deletedIDs(); len(ids) != 0 {
		t.Fatalf("deleted %v on a no-op update", ids)
	}
}

func TestTempVCIgnoresOtherGuilds(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	ev := &discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID:   "other-guild",
		UserID:    "user-1",
		ChannelID: testTempVCHub,
	}}
	tv.handleVoiceStateUpdate(ev)
	if created := fake.createdData(); len(created) != 0 {
		t.Fatalf("created %d channels for foreign guild", len(created))
	}
}

func TestTempVCHubJoinFromExistingTempChannel(t *testing.T) {
	// An owner sitting in their own temp channel hops back to the hub to
	// spawn a second one (the operations-host flow). The old channel empties
	// and must be reaped; the new one must be created.
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, 5*time.Millisecond)

	member := &discordgo.Member{Nick: "Host"}
	tv.handleVoiceStateUpdate(voiceEvent("host-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("host-1", "new-chan", member))

	fake.mu.Lock()
	fake.nextChannel = &discordgo.Channel{ID: "second-chan"}
	fake.mu.Unlock()

	tv.handleVoiceStateUpdate(voiceEvent("host-1", testTempVCHub, member))

	if created := fake.createdData(); len(created) != 2 {
		t.Fatalf("created %d channels, want 2", len(created))
	}
	eventually(t, func() bool {
		for _, id := range fake.deletedIDs() {
			if id == "new-chan" {
				return true
			}
		}
		return false
	}, "vacated first temp channel was not reaped")
}

func TestTempVCSecondChannelNumbersBothChannels(t *testing.T) {
	fake := newFakeTempVCManager()
	// Long grace so the first channel survives (empty of the host) long enough
	// for the second-channel flow to run.
	tv := newTestTempVC(fake, time.Hour)

	member := &discordgo.Member{Nick: "1LT.Laui.M"}

	// First channel: host joins hub, is moved into it.
	tv.handleVoiceStateUpdate(voiceEvent("host-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("host-1", "new-chan", member))
	// A guest joins the first channel so it stays occupied when the host hops
	// back to the hub, an empty first channel would just be reaped, and there
	// would be nothing to renumber.
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "new-chan", &discordgo.Member{Nick: "Guest"}))

	// Host hops to the hub to spawn a second channel.
	fake.mu.Lock()
	fake.nextChannel = &discordgo.Channel{ID: "second-chan"}
	fake.mu.Unlock()
	tv.handleVoiceStateUpdate(voiceEvent("host-1", testTempVCHub, member))

	created := fake.createdData()
	if len(created) != 2 {
		t.Fatalf("created %d channels, want 2", len(created))
	}
	if created[0].Name != "1LT Laui.M's Channel" {
		t.Errorf("first channel created as %q, want unsuffixed", created[0].Name)
	}
	if created[1].Name != "1LT Laui.M's Channel (2)" {
		t.Errorf("second channel created as %q, want (2)", created[1].Name)
	}
	// The pre-existing first channel is retro-numbered to (1).
	edits := fake.recordedEdits()
	if len(edits) != 1 || edits[0].channelID != "new-chan" || edits[0].name != "1LT Laui.M's Channel (1)" {
		t.Fatalf("edits = %+v, want first channel renamed to (1)", edits)
	}
}

func TestTempVCSecondChannelSkipsOwnerRenamedFirst(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	// Pre-existing first channel: owned and still occupied, but the owner has
	// renamed it since creation (live name != what the bot assigned).
	tv.mu.Lock()
	tv.owners["c1"] = "host-1"
	tv.occupants["c1"] = map[string]struct{}{"guest-1": {}}
	tv.baseName["c1"] = "1LT Laui.M's Channel"
	tv.assignedName["c1"] = "1LT Laui.M's Channel"
	tv.userChannel["host-1"] = "c1"
	tv.mu.Unlock()

	fake.mu.Lock()
	fake.channelByID["c1"] = &discordgo.Channel{ID: "c1", Name: "War Room"} // owner-chosen name
	fake.nextChannel = &discordgo.Channel{ID: "second-chan"}
	fake.mu.Unlock()

	tv.handleVoiceStateUpdate(voiceEvent("host-1", testTempVCHub, &discordgo.Member{Nick: "1LT.Laui.M"}))

	// The second channel is still numbered...
	created := fake.createdData()
	if len(created) != 1 || created[0].Name != "1LT Laui.M's Channel (2)" {
		t.Fatalf("created = %+v, want a single (2) channel", created)
	}
	// ...but the owner-renamed first channel is left exactly as the owner set it.
	if edits := fake.recordedEdits(); len(edits) != 0 {
		t.Fatalf("edits = %+v, want none (owner-renamed channel must be preserved)", edits)
	}
}

func TestTempVCChannelUpdateAuditLogs(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	tv.mu.Lock()
	tv.occupants["c1"] = map[string]struct{}{"u1": {}}
	tv.owners["c1"] = "u1"
	tv.assignedName["c1"] = "b's Channel"
	tv.mu.Unlock()

	mk := func(name string, limit int, deny int64) *discordgo.Channel {
		ch := &discordgo.Channel{ID: "c1", GuildID: testTempVCGuild, Name: name, UserLimit: limit}
		if deny != 0 {
			ch.PermissionOverwrites = []*discordgo.PermissionOverwrite{
				{ID: testTempVCGuild, Type: discordgo.PermissionOverwriteTypeRole, Deny: deny},
			}
		}
		return ch
	}
	upd := func(before, after *discordgo.Channel) {
		tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: after, BeforeUpdate: before})
	}

	upd(mk("b's Channel", 0, 0), mk("War Room", 0, 0))                                                                                            // owner rename
	upd(mk("War Room", 0, 0), mk("War Room", 5, 0))                                                                                               // user limit set
	upd(mk("War Room", 5, 0), mk("War Room", 5, discordgo.PermissionVoiceConnect))                                                                // lock
	upd(mk("War Room", 5, discordgo.PermissionVoiceConnect), mk("War Room", 5, discordgo.PermissionVoiceConnect|discordgo.PermissionViewChannel)) // hide

	got := strings.Join(func() []string {
		var out []string
		for _, m := range fake.logMessages() {
			out = append(out, m.content)
		}
		return out
	}(), "\n")

	for _, want := range []string{
		"**b's Channel** renamed to **War Room** by <@u1>", // attributed to the owner
		"user limit set to 5 by <@u1>",
		"locked (@everyone can no longer connect) by <@u1>",
		"hidden (@everyone can no longer see it) by <@u1>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("audit log missing %q\nfull log:\n%s", want, got)
		}
	}
}

func TestTempVCChannelUpdateIgnoredCases(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	tv.mu.Lock()
	tv.occupants["c1"] = map[string]struct{}{}
	tv.owners["c1"] = "u1"
	tv.assignedName["c1"] = "b's Channel"
	tv.pendingRename["c1"] = "b's Channel (1)" // our own disambiguation rename is inbound
	tv.mu.Unlock()

	ch := func(id, guild, name string) *discordgo.Channel {
		return &discordgo.Channel{ID: id, GuildID: guild, Name: name}
	}

	// Our own rename: suppressed (marker consumed, no log).
	tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: ch("c1", testTempVCGuild, "b's Channel (1)"), BeforeUpdate: ch("c1", testTempVCGuild, "b's Channel")})
	// Untracked channel: ignored.
	tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: ch("other", testTempVCGuild, "X"), BeforeUpdate: ch("other", testTempVCGuild, "Y")})
	// Foreign guild: ignored.
	tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: ch("c1", "other-guild", "Z"), BeforeUpdate: ch("c1", "other-guild", "b's Channel")})
	// No BeforeUpdate baseline: skipped.
	tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: ch("c1", testTempVCGuild, "Whatever")})

	if msgs := fake.logMessages(); len(msgs) != 0 {
		t.Fatalf("expected no audit lines, got %+v", msgs)
	}
	tv.mu.Lock()
	_, marker := tv.pendingRename["c1"]
	tv.mu.Unlock()
	if marker {
		t.Error("pendingRename marker was not consumed by the bot's own rename")
	}
}

func TestTempVCGuildCreateSweep(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	guild := &discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID: testTempVCGuild,
		Channels: []*discordgo.Channel{
			{ID: testTempVCHub, ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
			{ID: "empty-orphan", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
			{ID: "occupied-survivor", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
			{ID: "text-in-category", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildText},
			{ID: "elsewhere", ParentID: "other-cat", Type: discordgo.ChannelTypeGuildVoice},
		},
		VoiceStates: []*discordgo.VoiceState{
			{UserID: "user-a", ChannelID: "occupied-survivor"},
			{UserID: "user-b", ChannelID: "elsewhere"},
		},
	}}

	tv.handleGuildCreate(guild)

	ids := fake.deletedIDs()
	if len(ids) != 1 || ids[0] != "empty-orphan" {
		t.Fatalf("deleted = %v, want only empty-orphan", ids)
	}

	tv.mu.Lock()
	defer tv.mu.Unlock()
	if _, ok := tv.occupants["occupied-survivor"]; !ok {
		t.Error("occupied survivor not adopted")
	}
	if _, ok := tv.owners["occupied-survivor"]; ok {
		t.Error("adopted channel should have no recorded owner")
	}
	if tv.userChannel["user-a"] != "occupied-survivor" || tv.userChannel["user-b"] != "elsewhere" {
		t.Errorf("userChannel seeding = %+v", tv.userChannel)
	}
}

func TestTempVCGuildCreateIgnoresOtherGuildsAndAdoptedLifecycle(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, 5*time.Millisecond)

	// Foreign guild: untouched.
	tv.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID:       "other-guild",
		Channels: []*discordgo.Channel{{ID: "x", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice}},
	}})
	if ids := fake.deletedIDs(); len(ids) != 0 {
		t.Fatalf("swept a foreign guild: %v", ids)
	}

	// Our guild with an occupied survivor: when its last occupant leaves
	// post-adoption, the normal lifecycle reaps it.
	tv.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID: testTempVCGuild,
		Channels: []*discordgo.Channel{
			{ID: "survivor", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
		},
		VoiceStates: []*discordgo.VoiceState{{UserID: "user-a", ChannelID: "survivor"}},
	}})

	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", nil))
	eventually(t, func() bool {
		for _, id := range fake.deletedIDs() {
			if id == "survivor" {
				return true
			}
		}
		return false
	}, "adopted survivor not reaped after emptying")
}

func TestTempVCSweepDeleteFailureIsCaptured(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.deleteErr = errors.New("permission denied")
	tv := newTestTempVC(fake, time.Hour)

	// Must not panic; the orphan simply survives until the next sweep.
	tv.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID: testTempVCGuild,
		Channels: []*discordgo.Channel{
			{ID: "stuck-orphan", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
		},
	}})
}

// --- Multiple hubs with per-hub default settings ---

const (
	testTempVCHubB      = "hub-b"
	testTempVCCategoryB = "cat-b"
)

// newTestTempVCMultiHub builds a two-hub tempVC: hub A under category A (the
// default test hub, unlimited/default bitrate) and hub B under category B with
// its own user limit, bitrate, and grace, so per-hub routing and default
// stamping can be asserted side by side.
func newTestTempVCMultiHub(mgr TempVCManager, graceA, graceB time.Duration, limitB, bitrateB int) *tempVC {
	return newTempVC(mgr, TempVCConfig{
		GuildID:      testTempVCGuild,
		LogChannelID: testTempVCLog,
		Hubs: []tempVCHub{
			{HubChannelID: testTempVCHub, CategoryID: testTempVCCategory, Grace: graceA},
			{HubChannelID: testTempVCHubB, CategoryID: testTempVCCategoryB, UserLimit: limitB, Bitrate: bitrateB, Grace: graceB},
		},
	})
}

func TestTempVCStampsPerHubDefaults(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVCMultiHub(fake, time.Hour, time.Hour, 9, 96000)

	// A join on hub A (no per-hub limit/bitrate) spawns a plain channel under
	// category A.
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, &discordgo.Member{Nick: "A"}))
	// A join on hub B spawns a channel under category B carrying B's limit/bitrate.
	fake.mu.Lock()
	fake.nextChannel = &discordgo.Channel{ID: "chan-b"}
	fake.mu.Unlock()
	tv.handleVoiceStateUpdate(voiceEvent("user-b", testTempVCHubB, &discordgo.Member{Nick: "B"}))

	created := fake.createdData()
	if len(created) != 2 {
		t.Fatalf("created %d channels, want 2", len(created))
	}
	a, b := created[0], created[1]
	if a.ParentID != testTempVCCategory {
		t.Errorf("hub A channel parent = %q, want %q", a.ParentID, testTempVCCategory)
	}
	if a.UserLimit != 0 || a.Bitrate != 0 {
		t.Errorf("hub A channel limit/bitrate = %d/%d, want 0/0 (Discord defaults)", a.UserLimit, a.Bitrate)
	}
	if b.ParentID != testTempVCCategoryB {
		t.Errorf("hub B channel parent = %q, want %q", b.ParentID, testTempVCCategoryB)
	}
	if b.UserLimit != 9 {
		t.Errorf("hub B channel user limit = %d, want 9", b.UserLimit)
	}
	if b.Bitrate != 96000 {
		t.Errorf("hub B channel bitrate = %d, want 96000", b.Bitrate)
	}
}

func TestTempVCPerHubGraceReapsIndependently(t *testing.T) {
	fake := newFakeTempVCManager()
	// Hub A reaps almost immediately; hub B effectively never (within the test).
	tv := newTestTempVCMultiHub(fake, 5*time.Millisecond, time.Hour, 0, 0)

	// Spawn one channel from each hub and move the owner into it.
	memberA := &discordgo.Member{Nick: "A"}
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, memberA))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "new-chan", memberA))

	fake.mu.Lock()
	fake.nextChannel = &discordgo.Channel{ID: "chan-b"}
	fake.mu.Unlock()
	memberB := &discordgo.Member{Nick: "B"}
	tv.handleVoiceStateUpdate(voiceEvent("user-b", testTempVCHubB, memberB))
	tv.handleVoiceStateUpdate(voiceEvent("user-b", "chan-b", memberB))

	// Both owners leave; only hub A's short grace should fire.
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", memberA))
	tv.handleVoiceStateUpdate(voiceEvent("user-b", "", memberB))

	eventually(t, func() bool {
		for _, id := range fake.deletedIDs() {
			if id == "new-chan" {
				return true
			}
		}
		return false
	}, "hub A channel not reaped on its short grace")

	// Hub B's channel must still be alive, its hour-long grace has not elapsed.
	for _, id := range fake.deletedIDs() {
		if id == "chan-b" {
			t.Fatal("hub B channel reaped despite its long grace")
		}
	}
}

func TestTempVCSweepSpansAllHubCategories(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVCMultiHub(fake, time.Hour, time.Hour, 0, 0)

	tv.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID: testTempVCGuild,
		Channels: []*discordgo.Channel{
			// Both hubs sit under their own categories and must never be swept.
			{ID: testTempVCHub, ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
			{ID: testTempVCHubB, ParentID: testTempVCCategoryB, Type: discordgo.ChannelTypeGuildVoice},
			// Empty orphans under each category are reaped.
			{ID: "orphan-a", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
			{ID: "orphan-b", ParentID: testTempVCCategoryB, Type: discordgo.ChannelTypeGuildVoice},
			// An occupied survivor under category B is adopted.
			{ID: "survivor-b", ParentID: testTempVCCategoryB, Type: discordgo.ChannelTypeGuildVoice},
			// A channel outside any hub category is untouched.
			{ID: "elsewhere", ParentID: "other-cat", Type: discordgo.ChannelTypeGuildVoice},
		},
		VoiceStates: []*discordgo.VoiceState{{UserID: "user-x", ChannelID: "survivor-b"}},
	}})

	deleted := fake.deletedIDs()
	gotDeleted := map[string]bool{}
	for _, id := range deleted {
		gotDeleted[id] = true
	}
	if len(deleted) != 2 || !gotDeleted["orphan-a"] || !gotDeleted["orphan-b"] {
		t.Fatalf("deleted = %v, want exactly orphan-a and orphan-b", deleted)
	}

	tv.mu.Lock()
	defer tv.mu.Unlock()
	if _, ok := tv.occupants["survivor-b"]; !ok {
		t.Error("survivor under category B not adopted")
	}
	// The adopted survivor recovers hub B's grace.
	if tv.channelGrace["survivor-b"] != time.Hour {
		t.Errorf("adopted survivor grace = %v, want hub B's 1h", tv.channelGrace["survivor-b"])
	}
}

func TestTempVCCapNoticePostsToJoinedHub(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVCMultiHub(fake, time.Hour, time.Hour, 0, 0)

	// The user is already at the cap, then joins hub B.
	tv.mu.Lock()
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		tv.owners[id] = "host"
		tv.occupants[id] = map[string]struct{}{"filler": {}}
	}
	tv.mu.Unlock()

	tv.handleVoiceStateUpdate(voiceEvent("host", testTempVCHubB, &discordgo.Member{Nick: "Host"}))

	if created := fake.createdData(); len(created) != 0 {
		t.Fatalf("created %d channels at cap, want 0", len(created))
	}
	// The cap notice lands in hub B (the hub actually joined), not hub A.
	var inB, inA int
	for _, m := range fake.recordedMessages() {
		switch m.channelID {
		case testTempVCHubB:
			inB++
		case testTempVCHub:
			inA++
		}
	}
	if inB != 1 || inA != 0 {
		t.Fatalf("cap notice placement: hubB=%d hubA=%d, want 1 in hub B only", inB, inA)
	}
}

// --- Interim ownership: hand control to the present member in the highest
// status tier (rank role breaks ties within a tier; lowest user ID is the final
// tiebreak), restore on the creator's return ---

// Real status role IDs used to exercise tier ordering: general staff (highest)
// > company staff > active member (lowest of the three).
const (
	testStatusGeneral = "109873149507555328"
	testStatusCompany = "1104551737219620894"
	testStatusActive  = "437748324960043009"
)

// Real rank role IDs (SGT outranks PVT) used to exercise the rank tiebreak within
// a status tier.
const (
	testRankSGT = "899328273752928318"
	testRankPVT = "899328617081864202"
)

// member builds a member with a nickname (used only for channel naming) and
// optional role IDs (status and/or rank roles, which drive the election).
func member(nick string, roleIDs ...string) *discordgo.Member {
	return &discordgo.Member{Nick: nick, Roles: roleIDs}
}

// spawnOwnedChannel drives the creator through hub join -> moved into "new-chan"
// so the standard tracking (owner, occupants, memberMeta) is populated the same
// way production would, then returns with the creator sitting in the channel.
func spawnOwnedChannel(tv *tempVC, creatorID, nick string) {
	tv.handleVoiceStateUpdate(voiceEvent(creatorID, testTempVCHub, member(nick)))
	tv.handleVoiceStateUpdate(voiceEvent(creatorID, "new-chan", member(nick)))
}

// controllerOf reads the current interim controller of a channel under the lock.
func (t *tempVC) controllerOf(channelID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.controller[channelID]
}

// TestOutranksForInterim covers the election comparator directly, including the
// rank-role dimension that the end-to-end tests cannot exercise until the rank
// role IDs are configured. Smaller index = higher priority.
func TestOutranksForInterim(t *testing.T) {
	cases := []struct {
		name       string
		a          memberRankMeta
		aID        string
		b          memberRankMeta
		bID        string
		aOutranksB bool
	}{
		{"higher status wins over higher rank",
			memberRankMeta{statusIdx: 2, rankIdx: 20}, "z",
			memberRankMeta{statusIdx: 5, rankIdx: 0}, "a", true},
		{"same status, higher rank wins",
			memberRankMeta{statusIdx: 3, rankIdx: 4}, "z",
			memberRankMeta{statusIdx: 3, rankIdx: 9}, "a", true},
		{"same status and rank, lowest ID wins",
			memberRankMeta{statusIdx: 3, rankIdx: 4}, "a",
			memberRankMeta{statusIdx: 3, rankIdx: 4}, "b", true},
		{"lower status loses despite better rank",
			memberRankMeta{statusIdx: 6, rankIdx: 0}, "a",
			memberRankMeta{statusIdx: 4, rankIdx: 27}, "z", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outranksForInterim(tc.a, tc.aID, tc.b, tc.bID); got != tc.aOutranksB {
				t.Errorf("outranksForInterim = %v, want %v", got, tc.aOutranksB)
			}
		})
	}
}

// TestLowestRoleIndex covers deriving a member's tier from their roles, the
// mechanism both the status and rank keys use.
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

func TestTempVCInterimHigherStatusTierWins(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour) // long grace: channel survives the creator leaving

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// A company-staff member (higher tier) and an active-only member (lower tier).
	tv.handleVoiceStateUpdate(voiceEvent("g-coy", "new-chan", member("PVT Company", testStatusCompany)))
	tv.handleVoiceStateUpdate(voiceEvent("g-act", "new-chan", member("SGT Active", testStatusActive)))

	// No interim handoff happens while the creator is present.
	if sets := fake.recordedPermSets(); len(sets) != 0 {
		t.Fatalf("interim granted while creator present: %+v", sets)
	}

	// Creator leaves -> the higher status tier wins regardless of rank.
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	sets := fake.recordedPermSets()
	if len(sets) != 1 {
		t.Fatalf("perm sets = %+v, want exactly one interim grant", sets)
	}
	got := sets[0]
	if got.channelID != "new-chan" || got.targetID != "g-coy" {
		t.Errorf("interim granted to %+v, want g-coy (company staff) on new-chan", got)
	}
	if got.targetType != discordgo.PermissionOverwriteTypeMember {
		t.Errorf("interim overwrite type = %v, want member", got.targetType)
	}
	if got.allow != int64(tempVCOwnerPerms) || got.deny != 0 {
		t.Errorf("interim allow/deny = %d/%d, want %d/0", got.allow, got.deny, int64(tempVCOwnerPerms))
	}
	tv.mu.Lock()
	if tv.controller["new-chan"] != "g-coy" {
		t.Errorf("controller = %q, want g-coy", tv.controller["new-chan"])
	}
	tv.mu.Unlock()
}

func TestTempVCInterimStatusRoleBeatsNoRole(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// A plain, role-less member and an active-member; the status holder wins.
	tv.handleVoiceStateUpdate(voiceEvent("g-plain", "new-chan", member("Nobody")))
	tv.handleVoiceStateUpdate(voiceEvent("g-act", "new-chan", member("Active One", testStatusActive)))

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	sets := fake.recordedPermSets()
	if len(sets) != 1 || sets[0].targetID != "g-act" {
		t.Fatalf("interim = %+v, want the active member over the role-less member", sets)
	}
}

func TestTempVCInterimRankBreaksTieWithinStatus(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// Two active members with different rank roles; the SGT outranks the PVT.
	tv.handleVoiceStateUpdate(voiceEvent("g-pvt", "new-chan", member("Pvt", testStatusActive, testRankPVT)))
	tv.handleVoiceStateUpdate(voiceEvent("g-sgt", "new-chan", member("Sgt", testStatusActive, testRankSGT)))

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	sets := fake.recordedPermSets()
	if len(sets) != 1 || sets[0].targetID != "g-sgt" {
		t.Fatalf("interim = %+v, want the SGT (higher rank within the active tier)", sets)
	}
}

func TestTempVCInterimAnyoneEligible(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// A single plain, role-less member still becomes interim owner: no gate.
	tv.handleVoiceStateUpdate(voiceEvent("plain", "new-chan", member("just a name")))

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	sets := fake.recordedPermSets()
	if len(sets) != 1 || sets[0].targetID != "plain" {
		t.Fatalf("interim = %+v, want the sole plain member (anyone is eligible)", sets)
	}
}

func TestTempVCInterimOwnerRestoredOnCreatorReturn(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	tv.handleVoiceStateUpdate(voiceEvent("g-act", "new-chan", member("SGT Active", testStatusActive)))
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	// Sanity: interim in place.
	if sets := fake.recordedPermSets(); len(sets) != 1 {
		t.Fatalf("expected one interim grant before return, got %+v", sets)
	}

	// Creator returns -> interim grant revoked, control back to the creator's
	// permanent overwrite.
	tv.handleVoiceStateUpdate(voiceEvent("owner", "new-chan", member("CPL Owner")))

	dels := fake.recordedPermDeletes()
	if len(dels) != 1 || dels[0].channelID != "new-chan" || dels[0].targetID != "g-act" {
		t.Fatalf("perm deletes = %+v, want revoke of g-act on new-chan", dels)
	}
	tv.mu.Lock()
	if _, ok := tv.controller["new-chan"]; ok {
		t.Error("controller still set after creator returned")
	}
	tv.mu.Unlock()
}

func TestTempVCInterimOwnerReelectedWhenStandInLeaves(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// g-coy (company staff) outranks g-act (active) by status, so g-coy is elected
	// first; when they leave, g-act is re-elected.
	tv.handleVoiceStateUpdate(voiceEvent("g-coy", "new-chan", member("Coy", testStatusCompany)))
	tv.handleVoiceStateUpdate(voiceEvent("g-act", "new-chan", member("Act", testStatusActive)))
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner"))) // g-coy elected

	tv.handleVoiceStateUpdate(voiceEvent("g-coy", "", member("Coy", testStatusCompany)))

	// The last perm set should now be a grant to g-act, and g-coy's grant revoked.
	sets := fake.recordedPermSets()
	if len(sets) != 2 || sets[1].targetID != "g-act" {
		t.Fatalf("perm sets = %+v, want second grant to g-act", sets)
	}
	if dels := fake.recordedPermDeletes(); len(dels) != 1 || dels[0].targetID != "g-coy" {
		t.Fatalf("perm deletes = %+v, want revoke of departed stand-in g-coy", dels)
	}
	tv.mu.Lock()
	if tv.controller["new-chan"] != "g-act" {
		t.Errorf("controller = %q, want g-act after re-election", tv.controller["new-chan"])
	}
	tv.mu.Unlock()
}

func TestTempVCInterimHigherTierJoinerTakesOver(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// Creator leaves with only an active-member present -> they hold interim.
	tv.handleVoiceStateUpdate(voiceEvent("g-act", "new-chan", member("Act", testStatusActive)))
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))
	if tv.controllerOf("new-chan") != "g-act" {
		t.Fatalf("controller = %q, want g-act initially", tv.controllerOf("new-chan"))
	}

	// A general-staff member joins while the creator is away and takes over.
	tv.handleVoiceStateUpdate(voiceEvent("g-gen", "new-chan", member("Gen", testStatusGeneral)))

	if tv.controllerOf("new-chan") != "g-gen" {
		t.Errorf("controller = %q, want g-gen after higher-tier join", tv.controllerOf("new-chan"))
	}
	// The takeover revokes the old stand-in and grants the new one.
	if dels := fake.recordedPermDeletes(); len(dels) != 1 || dels[0].targetID != "g-act" {
		t.Fatalf("perm deletes = %+v, want revoke of g-act", dels)
	}
}

func TestTempVCInterimOwnerTieBreaksByLowestID(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// Same status (active), no rank roles configured -> tie -> lowest ID wins.
	tv.handleVoiceStateUpdate(voiceEvent("z-act", "new-chan", member("Zulu", testStatusActive)))
	tv.handleVoiceStateUpdate(voiceEvent("a-act", "new-chan", member("Alpha", testStatusActive)))

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	sets := fake.recordedPermSets()
	if len(sets) != 1 || sets[0].targetID != "a-act" {
		t.Fatalf("interim = %+v, want deterministic lowest-ID a-act", sets)
	}
}

func TestTempVCInterimGrantFailureIsCaptured(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.permErr = errors.New("HTTP 403 Missing Permissions")
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	tv.handleVoiceStateUpdate(voiceEvent("g-act", "new-chan", member("SGT Active", testStatusActive)))

	// Must not panic even though the grant errors; controller state is still
	// recorded (the next voice event reconciles again).
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))
}

// voiceInteraction builds a /voice application-command interaction in the test
// guild, invoked by invokerID, with the given subcommand and target user.
func voiceInteraction(invokerID, sub, targetID string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:    discordgo.InteractionApplicationCommand,
		GuildID: testTempVCGuild,
		Member:  &discordgo.Member{User: &discordgo.User{ID: invokerID, Username: invokerID}},
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "voice",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				stringOption("command", sub),
				userOption("user", targetID),
			},
		},
	}}
}

func TestVoiceBlockDeniesConnectAndDisconnectsPresentTarget(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	tv.handleVoiceStateUpdate(voiceEvent("bad", "new-chan", &discordgo.Member{Nick: "Bad Guy"}))

	f := &fakeResponder{}
	tv.runVoiceCommand(f, voiceInteraction("owner", "block", "bad"))

	sets := fake.recordedPermSets()
	if len(sets) != 1 {
		t.Fatalf("perm sets = %+v, want one block overwrite", sets)
	}
	got := sets[0]
	if got.channelID != "new-chan" || got.targetID != "bad" {
		t.Errorf("block overwrite = %+v, want deny on bad@new-chan", got)
	}
	if got.allow != 0 || got.deny != int64(tempVCBlockDeny) {
		t.Errorf("block allow/deny = %d/%d, want 0/%d (Connect)", got.allow, got.deny, int64(tempVCBlockDeny))
	}
	// Target was present, so they are disconnected (nil channel). The owner's
	// move-into-channel during setup is also recorded, so look for bad's kick.
	if !hasDisconnect(fake.recordedMoves(), "bad") {
		t.Fatalf("moves = %+v, want a nil-channel disconnect of bad", fake.recordedMoves())
	}
	if c := lastEditContent(f.Calls()); !strings.Contains(c, "Blocked") || !strings.Contains(c, "<@bad>") {
		t.Errorf("confirmation = %q, want a Blocked notice tagging the target", c)
	}
}

func TestVoiceBlockAbsentTargetDoesNotDisconnect(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")

	f := &fakeResponder{}
	tv.runVoiceCommand(f, voiceInteraction("owner", "block", "somebody-else"))

	if sets := fake.recordedPermSets(); len(sets) != 1 {
		t.Fatalf("perm sets = %+v, want the block overwrite", sets)
	}
	// The absent target is never disconnected (only the owner's setup move exists).
	if hasDisconnect(fake.recordedMoves(), "somebody-else") {
		t.Fatalf("moves = %+v, want no disconnect of an absent target", fake.recordedMoves())
	}
}

// hasDisconnect reports whether moves contains a disconnect (nil channel) of
// userID.
func hasDisconnect(moves []fakeMove, userID string) bool {
	for _, m := range moves {
		if m.userID == userID && m.channelID == nil {
			return true
		}
	}
	return false
}

func TestVoicePermitClearsOverwrite(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")

	f := &fakeResponder{}
	tv.runVoiceCommand(f, voiceInteraction("owner", "permit", "bad"))

	dels := fake.recordedPermDeletes()
	if len(dels) != 1 || dels[0].channelID != "new-chan" || dels[0].targetID != "bad" {
		t.Fatalf("perm deletes = %+v, want clear of bad@new-chan", dels)
	}
	if c := lastEditContent(f.Calls()); !strings.Contains(c, "Permitted") {
		t.Errorf("confirmation = %q, want a Permitted notice", c)
	}
}

func TestVoiceRequiresBeingInOwnedChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	// Invoker is not in any temp channel.
	f := &fakeResponder{}
	tv.runVoiceCommand(f, voiceInteraction("nobody", "block", "bad"))

	if sets := fake.recordedPermSets(); len(sets) != 0 {
		t.Fatalf("perm sets = %+v, want none when invoker owns no channel", sets)
	}
	if c := lastEditContent(f.Calls()); !strings.Contains(c, "temporary voice channel") {
		t.Errorf("error = %q, want the 'must be in your channel' message", c)
	}
}

func TestVoiceRejectsNonOwner(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// A guest sits in the channel but does not own or control it.
	tv.handleVoiceStateUpdate(voiceEvent("guest", "new-chan", &discordgo.Member{Nick: "Guest"}))

	f := &fakeResponder{}
	tv.runVoiceCommand(f, voiceInteraction("guest", "block", "bad"))

	if sets := fake.recordedPermSets(); len(sets) != 0 {
		t.Fatalf("perm sets = %+v, want none for a non-owner", sets)
	}
	if c := lastEditContent(f.Calls()); !strings.Contains(c, "owner") {
		t.Errorf("error = %q, want an owner-only rejection", c)
	}
}

func TestVoiceInterimOwnerMayBlock(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// A status-holding member joins, then the creator leaves -> the guest becomes
	// interim owner (highest status present) and should be allowed to block.
	tv.handleVoiceStateUpdate(voiceEvent("standin", "new-chan", member("Standin", testStatusActive)))
	tv.handleVoiceStateUpdate(voiceEvent("intruder", "new-chan", &discordgo.Member{Nick: "Intruder"}))
	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	// Ignore the interim-grant permission set; assert the block adds another.
	before := len(fake.recordedPermSets())
	f := &fakeResponder{}
	tv.runVoiceCommand(f, voiceInteraction("standin", "block", "intruder"))

	sets := fake.recordedPermSets()
	if len(sets) != before+1 {
		t.Fatalf("perm sets = %d, want one more than %d after interim block", len(sets), before)
	}
	if c := lastEditContent(f.Calls()); !strings.Contains(c, "Blocked") {
		t.Errorf("interim owner block confirmation = %q, want a Blocked notice", c)
	}
}

func TestVoiceBlockSelfAndOwnerRejected(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)
	spawnOwnedChannel(tv, "owner", "CPL Owner")

	f := &fakeResponder{}
	tv.runVoiceCommand(f, voiceInteraction("owner", "block", "owner"))
	if c := lastEditContent(f.Calls()); !strings.Contains(c, "yourself") {
		t.Errorf("self-block error = %q, want a 'can't block yourself' message", c)
	}
	if sets := fake.recordedPermSets(); len(sets) != 0 {
		t.Fatalf("perm sets = %+v, want none on a self-block", sets)
	}
}

func TestVoiceBlockFailureSurfacedNotPanicked(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.permErr = errors.New("HTTP 403 Missing Permissions")
	tv := newTestTempVC(fake, time.Hour)
	spawnOwnedChannel(tv, "owner", "CPL Owner")

	f := &fakeResponder{}
	tv.runVoiceCommand(f, voiceInteraction("owner", "block", "bad"))

	if c := lastEditContent(f.Calls()); !strings.Contains(c, "Failed") {
		t.Errorf("error surface = %q, want a failure notice", c)
	}
}

func TestVoiceGuildOnly(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake, time.Hour)

	i := voiceInteraction("owner", "block", "bad")
	i.GuildID = "" // DM-shaped

	f := &fakeResponder{}
	tv.runVoiceCommand(f, i)
	if sets := fake.recordedPermSets(); len(sets) != 0 {
		t.Fatalf("perm sets = %+v, want none outside a guild", sets)
	}
}
