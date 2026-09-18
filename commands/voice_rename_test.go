package commands

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

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

// movableClock replaces the runtime clock for the test and returns a setter,
// where pinClock in temp_vc_test.go fixes one instant.
func movableClock(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	prev := tempVCNow
	now := at
	tempVCNow = func() time.Time { return now }
	t.Cleanup(func() { tempVCNow = prev })
	return func(next time.Time) { now = next }
}

// T7: Discord allows two renames per channel per ten minutes. The two at t0
// and t0+1m pass. The third, at t0+2m, is refused with the time the window
// opens, ten minutes after the first, as a Discord relative timestamp. At
// exactly that time a rename passes again.
func TestVoiceRenameWindowRefusesThirdWithinTenMinutes(t *testing.T) {
	t0 := time.Date(2026, 9, 18, 20, 0, 0, 0, time.UTC)
	setClock := movableClock(t, t0)
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)
	spawnInto(tv, fake, "user-1", "chan-1", member("Smith", testRankSGT))

	rename := func(name string) string {
		f := &fakeResponder{}
		runVoiceRename(f, tv, renameInteraction("user-1", nil, name))
		return ephemeralReply(t, f)
	}

	rename("One")
	setClock(t0.Add(time.Minute))
	rename("Two")
	if edits := fake.recordedEdits(); len(edits) != 2 {
		t.Fatalf("edits after two renames = %+v, want two", edits)
	}

	setClock(t0.Add(2 * time.Minute))
	reply := rename("Three")
	if edits := fake.recordedEdits(); len(edits) != 2 {
		t.Fatalf("edits after the third rename = %+v, want the third refused", edits)
	}
	opens := fmt.Sprintf("<t:%d:R>", t0.Add(10*time.Minute).Unix())
	if !strings.Contains(reply, opens) {
		t.Errorf("reply %q does not carry the window open time %s", reply, opens)
	}

	setClock(t0.Add(10 * time.Minute))
	rename("Four")
	if edits := fake.recordedEdits(); len(edits) != 3 {
		t.Errorf("edits once the window opened = %+v, want three", edits)
	}
}

// T8: the name is trimmed. An empty result or one over Discord's 100
// characters is refused with no edit. Counted in characters, so a name of
// 101 multi-byte characters is over the limit too.
func TestVoiceRenameNameTrimmedAndBounded(t *testing.T) {
	for _, tc := range []struct {
		label string
		name  string
		want  string // the name the edit carries, empty for a refusal
	}{
		{label: "whitespace only", name: "   \t ", want: ""},
		{label: "101 characters", name: strings.Repeat("é", 101), want: ""},
		{label: "padded", name: "  Alpha Briefing  ", want: "Alpha Briefing"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			fake := newFakeTempVCManager()
			tv := newSeededTempVC(t, fake)
			spawnInto(tv, fake, "user-1", "chan-1", member("Smith", testRankSGT))

			f := &fakeResponder{}
			runVoiceRename(f, tv, renameInteraction("user-1", nil, tc.name))

			edits := fake.recordedEdits()
			switch {
			case tc.want == "" && len(edits) != 0:
				t.Errorf("edits = %+v, want none", edits)
			case tc.want != "" && (len(edits) != 1 || edits[0].name != tc.want):
				t.Errorf("edits = %+v, want one named %q", edits, tc.want)
			}
			ephemeralReply(t, f)
		})
	}
}

// T9a: a 5xx on the edit reaches Sentry, and the invoker's reply carries no
// raw Discord body.
func TestVoiceRenameServerErrorCapturedAndSanitised(t *testing.T) {
	captures := countCaptures(t)
	fake := newFakeTempVCManager()
	tv := newSeededTempVC(t, fake)
	spawnInto(tv, fake, "user-1", "chan-1", member("Smith", testRankSGT))
	fake.editErr = restError(http.StatusInternalServerError, 0, rawBodyMarker)

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction("user-1", nil, "Alpha"))

	if reply := ephemeralReply(t, f); strings.Contains(reply, rawBodyMarker) {
		t.Errorf("reply %q leaks the raw Discord body", reply)
	}
	if *captures != 1 {
		t.Errorf("captures = %d, want 1", *captures)
	}
}

// T9b: Unknown Channel on the edit means the channel is already gone. The
// runtime untracks it and drops its row, and nothing reaches Sentry.
func TestVoiceRenameUnknownChannelUntracksQuietly(t *testing.T) {
	captures := countCaptures(t)
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	spawnInto(tv, fake, "user-1", "chan-1", member("Smith", testRankSGT))
	fake.editErr = restError(http.StatusNotFound, discordgo.ErrCodeUnknownChannel, rawBodyMarker)

	f := &fakeResponder{}
	runVoiceRename(f, tv, renameInteraction("user-1", nil, "Alpha"))

	ephemeralReply(t, f)
	if *captures != 0 {
		t.Errorf("captures = %d, want none", *captures)
	}
	if _, tracked := tv.Owner("chan-1"); tracked {
		t.Error("chan-1 is still tracked after Unknown Channel")
	}
	if rows := spawnedRows(t, st); len(rows) != 0 {
		t.Errorf("spawned rows = %+v, want none", rows)
	}
}

// T10: the registry declares /voice-rename with its one required string
// option. Always, whatever the runtime: a command that comes and goes with
// configuration loses its Server Settings restriction each time the startup
// sync deletes it, because Discord keys command permissions by command ID.
func TestRegistryDeclaresVoiceRename(t *testing.T) {
	var def *discordgo.ApplicationCommand
	for _, d := range NewRegistry(nil).GetCommands() {
		if d.Name == "voice-rename" {
			def = d
		}
	}
	if def == nil {
		t.Fatal("voice-rename is not registered")
	}
	if len(def.Options) != 1 || def.Options[0].Name != "name" ||
		def.Options[0].Type != discordgo.ApplicationCommandOptionString || !def.Options[0].Required {
		t.Errorf("options = %+v, want one required string option named name", def.Options)
	}
}

// T11: on a host with no bot store there is no runtime. The command still
// exists, so the handler refuses with the same ephemeral shape instead of
// dereferencing a nil runtime.
func TestVoiceRenameNoRuntimeRefused(t *testing.T) {
	f := &fakeResponder{}
	runVoiceRename(f, nil, renameInteraction("user-1", nil, "Alpha"))
	ephemeralReply(t, f)
}
