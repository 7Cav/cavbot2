package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// /voice-rename (#292). Every case drives the runtime through the gateway
// event helpers so the invoker sits in a spawned channel the way production
// tracking puts them there, then runs the handler through the fake responder
// and the fake manager. Nothing here asserts reply wording, the order of the
// checks, or a log line.

// renameInteraction builds the interaction /voice-rename receives: a guild
// command from a member with the given roles, carrying the one name option.
func renameInteraction(userID string, roles []string, name string) *discordgo.InteractionCreate {
	i := fakeAppCommandInteraction(stringOption("name", name))
	i.GuildID = testTempVCGuild
	i.Member = &discordgo.Member{
		User:  &discordgo.User{ID: userID, Username: "tester"},
		Roles: roles,
	}
	return i
}

// ephemeralReply asserts the reply took the deferred-ephemeral shape: one
// deferred response carrying the ephemeral flag, then one edit of it. A branch
// that answered with a fresh response instead (the utils.HandleError shape)
// fails here. It returns the edit's content for the caller to inspect.
func ephemeralReply(t *testing.T, f *fakeResponder) string {
	t.Helper()
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("responder calls = %+v, want one deferred response then one edit", calls)
	}
	first := calls[0]
	if first.Method != "Respond" || first.Response == nil ||
		first.Response.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource {
		t.Fatalf("first call = %+v, want a deferred response", first)
	}
	if first.Response.Data == nil || first.Response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Errorf("deferred response flags = %v, want ephemeral", first.Response.Data)
	}
	second := calls[1]
	if second.Method != "Edit" || second.Edit == nil || second.Edit.Content == nil {
		t.Fatalf("second call = %+v, want an edit with content", second)
	}
	return *second.Edit.Content
}

// T2: the owner, inside their spawned channel, renames it. The edit carries
// the name and an audit reason naming the invoker.
func TestVoiceRenameOwnerRenamesOwnChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)
	spawnInto(tv, fake, "user-1", "chan-1", member("Smith", testRankSGT))

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction("user-1", nil, "Alpha Briefing"))

	edits := fake.recordedEdits()
	if len(edits) != 1 {
		t.Fatalf("edits = %+v, want one", edits)
	}
	if edits[0].channelID != "chan-1" || edits[0].name != "Alpha Briefing" {
		t.Errorf("edit = %+v, want chan-1 renamed to Alpha Briefing", edits[0])
	}
	if !strings.Contains(edits[0].reason, "user-1") {
		t.Errorf("audit reason %q does not name the invoker", edits[0].reason)
	}
	ephemeralReply(t, f)
}

// T3: an invoker in a voice channel that is not a spawned channel (the hub
// itself, where a member who was never moved sits) is refused. No edit.
func TestVoiceRenameOutsideSpawnedChannelRefused(t *testing.T) {
	fake := newFakeTempVCManager()
	// A hub the runtime knows but that spawns nothing, so the join leaves
	// the member sitting in the hub channel.
	hub := testHub()
	hub.Enabled = false
	tv := newTestTempVC(t, fake, seedStore(t, hub))
	tv.HandleVoiceStateUpdate(voiceEvent("user-1", testTempVCHub, member("Smith", testRankSGT)))

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction("user-1", nil, "Alpha"))

	if edits := fake.recordedEdits(); len(edits) != 0 {
		t.Errorf("edits = %+v, want none", edits)
	}
	ephemeralReply(t, f)
}

// T4: a non-owner in the channel is refused and the reply names the owner.
func TestVoiceRenameNonOwnerToldWhoOwns(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)
	spawnInto(tv, fake, "user-owner", "chan-1", member("Smith", testRankSGT))
	tv.HandleVoiceStateUpdate(voiceEvent("user-2", "chan-1", member("Jones", testRankPVT)))

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction("user-2", nil, "Alpha"))

	if edits := fake.recordedEdits(); len(edits) != 0 {
		t.Errorf("edits = %+v, want none", edits)
	}
	if reply := ephemeralReply(t, f); !strings.Contains(reply, "user-owner") {
		t.Errorf("reply %q does not name the owner", reply)
	}
}

// T5: a channel with no owner (a guest created it) cannot be renamed by an
// invoker holding no moderator role. No edit.
func TestVoiceRenameOwnerlessChannelRefusedForNonModerator(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)
	spawnInto(tv, fake, "user-guest", "chan-1", member("Guest"))

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction("user-guest", nil, "Alpha"))

	if edits := fake.recordedEdits(); len(edits) != 0 {
		t.Errorf("edits = %+v, want none", edits)
	}
	ephemeralReply(t, f)
}

// Moderator role IDs the hub and guild fixtures name. They are not rank
// roles, so holding one never makes a member an owner candidate.
const (
	testModRoleHub   = "role-mod-hub"
	testModRoleGuild = "role-mod-guild"
)

// T6a: a member holding one of the hub's moderator roles renames a channel
// they do not own.
func TestVoiceRenameHubModeratorRenamesOwnedChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	hub := testHub()
	hub.ModeratorRoleIDs = []string{testModRoleHub}
	tv := newTestTempVC(t, fake, seedStore(t, hub))
	spawnInto(tv, fake, "user-owner", "chan-1", member("Smith", testRankSGT))
	tv.HandleVoiceStateUpdate(voiceEvent("user-mod", "chan-1", member("Jones", testRankPVT, testModRoleHub)))

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction("user-mod", []string{testRankPVT, testModRoleHub}, "Alpha"))

	edits := fake.recordedEdits()
	if len(edits) != 1 || edits[0].channelID != "chan-1" {
		t.Fatalf("edits = %+v, want one on chan-1", edits)
	}
	ephemeralReply(t, f)
}

// T6b: a member holding a guild-wide moderator role renames an ownerless
// channel, and the channel still has no owner afterwards.
func TestVoiceRenameGuildModeratorRenamesOwnerlessChannel(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	if err := st.SetGuildModeratorRoles(context.Background(), testTempVCGuild, []string{testModRoleGuild}); err != nil {
		t.Fatalf("SetGuildModeratorRoles: %v", err)
	}
	tv := newTestTempVC(t, fake, st)
	spawnInto(tv, fake, "user-guest", "chan-1", member("Guest"))
	tv.HandleVoiceStateUpdate(voiceEvent("user-mod", "chan-1", member("Jones", testModRoleGuild)))

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction("user-mod", []string{testModRoleGuild}, "Alpha"))

	edits := fake.recordedEdits()
	if len(edits) != 1 || edits[0].channelID != "chan-1" {
		t.Fatalf("edits = %+v, want one on chan-1", edits)
	}
	if owner, tracked := tv.Owner("chan-1"); !tracked || owner != "" {
		t.Errorf("Owner(chan-1) = %q, %v; want no owner and still tracked", owner, tracked)
	}
	ephemeralReply(t, f)
}
