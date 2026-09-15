package commands

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// fakeTempVCManager records calls and injects per-call errors. Mutex-guarded
// because the suite runs under -race and the fake is shared with any goroutine
// a test spawns.
type fakeTempVCManager struct {
	mu sync.Mutex

	created     []discordgo.GuildChannelCreateData
	deleted     []string
	moves       []fakeMove
	messages    []fakeMessage
	nextChannel *discordgo.Channel

	deleteCalls int

	createErr  error
	deleteErr  error
	moveErr    error
	messageErr error
}

type fakeMove struct {
	userID    string
	channelID *string
}

type fakeMessage struct {
	channelID string
	content   string
}

func newFakeTempVCManager() *fakeTempVCManager {
	return &fakeTempVCManager{
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
	return &ch, nil
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

const (
	testTempVCGuild    = "guild-1"
	testTempVCHub      = "hub-1"
	testTempVCCategory = "cat-1"
	testTempVCHubName  = "Voice"
	testTempVCLog      = "log-1"
)

func newTestTempVC(mgr TempVCManager) *tempVC {
	return newTempVC(mgr, TempVCConfig{
		GuildID:      testTempVCGuild,
		LogChannelID: testTempVCLog,
		Hubs: []tempVCHub{{
			HubChannelID: testTempVCHub,
			CategoryID:   testTempVCCategory,
			Name:         testTempVCHubName,
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
		if h.HubChannelID == "" || h.CategoryID == "" || h.Name == "" {
			t.Errorf("hub[%d] has an empty ID or name: %+v", i, h)
		}
	}
}

func TestMustTempVCHubsRejectsMisconfig(t *testing.T) {
	cases := []struct {
		name string
		hubs []tempVCHub
	}{
		{"empty table", nil},
		{"empty hub id", []tempVCHub{{HubChannelID: "", CategoryID: "c", Name: "V"}}},
		{"empty category id", []tempVCHub{{HubChannelID: "h", CategoryID: "", Name: "V"}}},
		{"hub equals category", []tempVCHub{{HubChannelID: "x", CategoryID: "x", Name: "V"}}},
		{"empty name", []tempVCHub{{HubChannelID: "h", CategoryID: "c", Name: ""}}},
		{"negative user limit", []tempVCHub{{HubChannelID: "h", CategoryID: "c", Name: "V", UserLimit: -1}}},
		{"duplicate hub", []tempVCHub{
			{HubChannelID: "h", CategoryID: "c1", Name: "V"},
			{HubChannelID: "h", CategoryID: "c2", Name: "V"},
		}},
		{"duplicate category", []tempVCHub{
			{HubChannelID: "h1", CategoryID: "c", Name: "V"},
			{HubChannelID: "h2", CategoryID: "c", Name: "V"},
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
		{HubChannelID: "h1", CategoryID: "c1", Name: "A"},
		{HubChannelID: "h2", CategoryID: "c2", Name: "B", UserLimit: 9, Bitrate: 96000},
	}
	if got := mustTempVCHubs(good); len(got) != 2 {
		t.Fatalf("mustTempVCHubs returned %d hubs, want 2", len(got))
	}
}

func TestTempVCHubJoinCreatesOwnedChannelAndMovesUser(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

	member := &discordgo.Member{Nick: "CPL Smith.J", User: &discordgo.User{Username: "smithy"}}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))

	created := fake.createdData()
	if len(created) != 1 {
		t.Fatalf("created %d channels, want 1", len(created))
	}
	data := created[0]
	if data.Name != "Voice - 1" {
		t.Errorf("name = %q, want the hub's name plus the first number", data.Name)
	}
	if data.Type != discordgo.ChannelTypeGuildVoice {
		t.Errorf("type = %v", data.Type)
	}
	if data.ParentID != testTempVCCategory {
		t.Errorf("parent = %q", data.ParentID)
	}
	// No overwrites of its own: the channel inherits its category's, and the
	// creator gets no permission. The bot holds channel permissions.
	if len(data.PermissionOverwrites) != 0 {
		t.Errorf("overwrites = %+v, want none (inherit the category)", data.PermissionOverwrites)
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

func TestTempVCLongHubNameTruncatedToDiscordLimit(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTempVC(fake, TempVCConfig{
		GuildID:      testTempVCGuild,
		LogChannelID: testTempVCLog,
		Hubs: []tempVCHub{{
			HubChannelID: testTempVCHub,
			CategoryID:   testTempVCCategory,
			Name:         strings.Repeat("x", 120),
		}},
	})

	tv.handleVoiceStateUpdate(voiceEvent("user-5", testTempVCHub, nil))
	created := fake.createdData()
	if len(created) != 1 || len(created[0].Name) != discordChannelNameLimit {
		t.Fatalf("name length = %d, want %d", len(created[0].Name), discordChannelNameLimit)
	}
	// The number survives the cut; the base is what is shortened.
	if !strings.HasSuffix(created[0].Name, " - 1") {
		t.Errorf("name = %q, want it to keep its number", created[0].Name)
	}
}

func TestTempVCNumbersPerHubAndReusesFreedNumber(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

	// Two members spawn from the same hub: the hub numbers them 1 and 2, no
	// matter who created them.
	a := &discordgo.Member{Nick: "A"}
	tv.handleVoiceStateUpdate(voiceEvent("user-a", testTempVCHub, a))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "new-chan", a))

	fake.mu.Lock()
	fake.nextChannel = &discordgo.Channel{ID: "second-chan"}
	fake.mu.Unlock()
	b := &discordgo.Member{Nick: "B"}
	tv.handleVoiceStateUpdate(voiceEvent("user-b", testTempVCHub, b))
	tv.handleVoiceStateUpdate(voiceEvent("user-b", "second-chan", b))

	created := fake.createdData()
	if len(created) != 2 || created[0].Name != "Voice - 1" || created[1].Name != "Voice - 2" {
		t.Fatalf("created = %+v, want Voice - 1 then Voice - 2", created)
	}

	// The first channel empties and is deleted, freeing number 1. The next
	// spawn reuses it rather than minting a 3.
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", a))
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Fatalf("deleted = %v, want new-chan", ids)
	}
	fake.mu.Lock()
	fake.nextChannel = &discordgo.Channel{ID: "third-chan"}
	fake.mu.Unlock()
	tv.handleVoiceStateUpdate(voiceEvent("user-c", testTempVCHub, &discordgo.Member{Nick: "C"}))

	created = fake.createdData()
	if len(created) != 3 || created[2].Name != "Voice - 1" {
		t.Fatalf("created = %+v, want the freed Voice - 1 reused", created)
	}
}

func TestTempVCCapDisconnectsFifthJoin(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

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

func TestTempVCEmptyChannelDeletedAtOnce(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

	member := &discordgo.Member{Nick: "Owner"}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))
	// Simulate the gateway reporting the move into the new channel...
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))
	// ...then the owner disconnecting. The delete happens before the event
	// handler returns: no grace, no timer.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "", member))

	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Fatalf("deleted = %v, want new-chan the moment it emptied", ids)
	}

	tv.mu.Lock()
	defer tv.mu.Unlock()
	if _, ok := tv.occupants["new-chan"]; ok {
		t.Error("deleted channel still tracked")
	}
	if _, ok := tv.owners["new-chan"]; ok {
		t.Error("deleted channel still owned")
	}
}

func TestTempVCOnlyLastLeaverTriggersDeletion(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

	owner := &discordgo.Member{Nick: "Owner"}
	guest := &discordgo.Member{Nick: "Guest"}
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", testTempVCHub, owner))
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", "new-chan", owner))
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "new-chan", guest))

	// Owner leaves; guest remains, no deletion.
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", "", owner))
	if ids := fake.deletedIDs(); len(ids) != 0 {
		t.Fatalf("deleted %v while occupied", ids)
	}

	// Guest leaves; now it empties and deletes.
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "", guest))
	if ids := fake.deletedIDs(); len(ids) != 1 {
		t.Fatalf("deleted = %v, want the channel once its last occupant left", ids)
	}
}

func TestTempVCMoveIntoFailureReapsChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.moveErr = errors.New("target user is not connected to voice")
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

	// Must not panic and must not move anyone.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, nil))
	if moves := fake.recordedMoves(); len(moves) != 0 {
		t.Fatalf("moves = %+v, want none after create failure", moves)
	}
}

func TestTempVCDeleteFailureKeepsChannelTracked(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.deleteErr = errors.New("HTTP 403 Missing Permissions")
	tv := newTestTempVC(fake)

	member := &discordgo.Member{Nick: "Owner"}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "", member))

	if fake.deleteCallCount() != 1 {
		t.Fatalf("delete calls = %d, want 1 attempt", fake.deleteCallCount())
	}

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
	tv := newTestTempVC(fake)

	// Owner already holds 4 empty channels whose deletes will 403.
	tv.mu.Lock()
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		tv.owners[id] = "b"
		tv.occupants[id] = map[string]struct{}{}
		tv.assignedName[id] = "Voice - 1"
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
	tv := newTestTempVC(fake)

	member := &discordgo.Member{Nick: "b"}
	// Owner joins hub -> channel created -> owner moved in (join event).
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", "new-chan", member))
	// Guest joins then leaves; owner leaves; channel empties and deletes.
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "new-chan", &discordgo.Member{Nick: "Guest"}))
	tv.handleVoiceStateUpdate(voiceEvent("guest-1", "", nil))
	tv.handleVoiceStateUpdate(voiceEvent("owner-1", "", member))

	var b strings.Builder
	for _, m := range fake.logMessages() {
		b.WriteString(m.content)
		b.WriteByte('\n')
	}
	got := b.String()
	for _, want := range []string{
		"created **Voice - 1**",           // create (by name, survives deletion)
		"<@owner-1> joined **Voice - 1**", // owner moved in
		"<@guest-1> joined **Voice - 1**", // guest joins
		"<@guest-1> left **Voice - 1**",   // guest leaves
		"<@owner-1> left **Voice - 1**",   // owner leaves
		"Auto-deleted **Voice - 1**",      // channel deleted (by the bot)
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
	tv := newTestTempVC(fake)

	member := &discordgo.Member{Nick: "Owner"}
	tv.handleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member))
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))

	// Same channel again = mute/deafen toggle; must not disturb tracking or
	// spawn a second channel even though the channel ID matches nothing new.
	tv.handleVoiceStateUpdate(voiceEvent("user-1", "new-chan", member))

	if created := fake.createdData(); len(created) != 1 {
		t.Fatalf("created %d channels, want 1", len(created))
	}
	if ids := fake.deletedIDs(); len(ids) != 0 {
		t.Fatalf("deleted %v on a no-op update", ids)
	}
}

func TestTempVCIgnoresOtherGuilds(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

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
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "new-chan" {
		t.Fatalf("deleted = %v, want the vacated first channel", ids)
	}
}

func TestTempVCChannelUpdateAuditLogsRename(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

	tv.mu.Lock()
	tv.occupants["c1"] = map[string]struct{}{"u1": {}}
	tv.owners["c1"] = "u1"
	tv.assignedName["c1"] = "Voice - 1"
	tv.mu.Unlock()

	mk := func(name string, limit int) *discordgo.Channel {
		return &discordgo.Channel{ID: "c1", GuildID: testTempVCGuild, Name: name, UserLimit: limit}
	}
	upd := func(before, after *discordgo.Channel) {
		tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: after, BeforeUpdate: before})
	}

	upd(mk("Voice - 1", 0), mk("War Room", 0)) // rename
	upd(mk("War Room", 0), mk("War Room", 5))  // some other field: not a rename

	msgs := fake.logMessages()
	if len(msgs) != 1 {
		t.Fatalf("audit lines = %+v, want exactly the rename", msgs)
	}
	if want := "**Voice - 1** renamed to **War Room** by <@u1>"; !strings.Contains(msgs[0].content, want) {
		t.Errorf("audit line = %q, want it to contain %q", msgs[0].content, want)
	}
}

func TestTempVCChannelUpdateIgnoredCases(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

	tv.mu.Lock()
	tv.occupants["c1"] = map[string]struct{}{}
	tv.owners["c1"] = "u1"
	tv.assignedName["c1"] = "Voice - 1"
	tv.mu.Unlock()

	ch := func(id, guild, name string) *discordgo.Channel {
		return &discordgo.Channel{ID: id, GuildID: guild, Name: name}
	}

	// Untracked channel: ignored.
	tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: ch("other", testTempVCGuild, "X"), BeforeUpdate: ch("other", testTempVCGuild, "Y")})
	// Foreign guild: ignored.
	tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: ch("c1", "other-guild", "Z"), BeforeUpdate: ch("c1", "other-guild", "Voice - 1")})
	// No BeforeUpdate baseline: skipped.
	tv.handleChannelUpdate(&discordgo.ChannelUpdate{Channel: ch("c1", testTempVCGuild, "Whatever")})

	if msgs := fake.logMessages(); len(msgs) != 0 {
		t.Fatalf("expected no audit lines, got %+v", msgs)
	}
}

func TestTempVCGuildCreateSweep(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

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
	if ids := fake.deletedIDs(); len(ids) != 1 || ids[0] != "survivor" {
		t.Fatalf("deleted = %v, want the adopted survivor once it emptied", ids)
	}
}

func TestTempVCSweepDeleteFailureIsCaptured(t *testing.T) {
	fake := newFakeTempVCManager()
	fake.deleteErr = errors.New("permission denied")
	tv := newTestTempVC(fake)

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
	testTempVCHubNameB  = "Briefing"
)

// newTestTempVCMultiHub builds a two-hub tempVC: hub A under category A (the
// default test hub, unlimited/default bitrate) and hub B under category B with
// its own user limit and bitrate, so per-hub routing and default stamping can
// be asserted side by side.
func newTestTempVCMultiHub(mgr TempVCManager, limitB, bitrateB int) *tempVC {
	return newTempVC(mgr, TempVCConfig{
		GuildID:      testTempVCGuild,
		LogChannelID: testTempVCLog,
		Hubs: []tempVCHub{
			{HubChannelID: testTempVCHub, CategoryID: testTempVCCategory, Name: testTempVCHubName},
			{HubChannelID: testTempVCHubB, CategoryID: testTempVCCategoryB, Name: testTempVCHubNameB, UserLimit: limitB, Bitrate: bitrateB},
		},
	})
}

func TestTempVCStampsPerHubDefaults(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVCMultiHub(fake, 9, 96000)

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
	// Each hub numbers its own channels from 1 under its own name.
	if a.Name != "Voice - 1" || b.Name != "Briefing - 1" {
		t.Errorf("names = %q / %q, want Voice - 1 / Briefing - 1", a.Name, b.Name)
	}
}

func TestTempVCSweepSpansAllHubCategories(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVCMultiHub(fake, 0, 0)

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
	// The adopted survivor is recorded as one of hub B's channels.
	if tv.channelHub["survivor-b"] != testTempVCHubB {
		t.Errorf("adopted survivor hub = %q, want hub B", tv.channelHub["survivor-b"])
	}
}

func TestTempVCCapNoticePostsToJoinedHub(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVCMultiHub(fake, 0, 0)

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
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

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
	tv := newTestTempVC(fake)

	spawnOwnedChannel(tv, "owner", "CPL Owner")
	// Same rank -> tie -> lowest ID wins.
	tv.handleVoiceStateUpdate(voiceEvent("z-sgt", "new-chan", member("Zulu", testRankSGT)))
	tv.handleVoiceStateUpdate(voiceEvent("a-sgt", "new-chan", member("Alpha", testRankSGT)))

	tv.handleVoiceStateUpdate(voiceEvent("owner", "", member("CPL Owner")))

	if got := tv.controllerOf("new-chan"); got != "a-sgt" {
		t.Errorf("controller = %q, want deterministic lowest-ID a-sgt", got)
	}
}

func TestTempVCAdoptedChannelGetsNoInterimOwner(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(fake)

	// A restart survivor has occupants but no recorded creator, so nobody is
	// ever elected for it, whoever comes and goes.
	tv.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID: testTempVCGuild,
		Channels: []*discordgo.Channel{
			{ID: "survivor", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice},
		},
		VoiceStates: []*discordgo.VoiceState{{UserID: "user-a", ChannelID: "survivor"}},
	}})
	tv.handleVoiceStateUpdate(voiceEvent("g-sgt", "survivor", member("Sgt", testRankSGT)))
	tv.handleVoiceStateUpdate(voiceEvent("user-a", "", nil))

	if got := tv.controllerOf("survivor"); got != "" {
		t.Errorf("controller = %q on an adopted channel, want none", got)
	}
}
