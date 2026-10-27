package commands

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// The Discharged role (#893, GLOSSARY.md): a member holding it is no Cav
// member, whatever rank role they hold, so the bot answers for them as for a
// member with no rank role.

// testDischargedRole is the forum's Discharged role, the one a guild has
// until a panel admin saves the Discharged role.
const testDischargedRole = "437749895785480193"

// A discharged SGT who joins a hub spawns a channel with no owner, and an
// SGT who isn't discharged owns theirs. With nothing saved the Discharged
// role is the forum's, and so it is for rank roles saved before the
// Discharged role could be.
func TestDischargedRankHolderOwnsNoChannelTheySpawn(t *testing.T) {
	cases := []struct {
		name string
		save func(t *testing.T, st store.Store)
	}{
		{"nothing saved", func(*testing.T, store.Store) {}},
		{"rank roles saved with no Discharged role", func(t *testing.T, st store.Store) {
			saveRankRoles(t, st, DefaultRankLadder().Roles())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTempVCManager()
			st := seedStore(t, testHub())
			tc.save(t, st)
			tv := newTestTempVC(t, fake, st)

			spawnInto(tv, fake, "sgt-discharged", "chan-a", member("SgtDischarged", testRankSGT, testDischargedRole))
			spawnInto(tv, fake, "sgt-serving", "chan-b", member("SgtServing", testRankSGT))

			if owner, tracked := tv.Owner("chan-a"); owner != "" || !tracked {
				t.Errorf("Owner(chan-a) = %q, %v, want no owner, tracked: its creator holds the Discharged role", owner, tracked)
			}
			if owner, _ := tv.Owner("chan-b"); owner != "sgt-serving" {
				t.Errorf("Owner(chan-b) = %q, want sgt-serving, an SGT with no Discharged role", owner)
			}
		})
	}
}

// A discharged SGT who joins a channel with no owner doesn't take it.
func TestDischargedRankHolderJoiningAnOwnerlessChannelDoesNotOwnIt(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(t, fake, seedStore(t, testHub()))
	spawnInto(tv, fake, "guest", "chan-a", member("Guest"))

	fake.deliver(tv, voiceEvent("sgt-discharged", "chan-a", member("SgtDischarged", testRankSGT, testDischargedRole)))

	if owner, tracked := tv.Owner("chan-a"); owner != "" || !tracked {
		t.Errorf("Owner(chan-a) = %q, %v, want no owner, tracked: the SGT who joined holds the Discharged role", owner, tracked)
	}
}

// testRankCOL is the COL role ID of the ladder in code, which outranks
// SGT.
const testRankCOL = "899325943179538432"

// When the owner leaves a channel holding a discharged COL and a serving
// SGT, the SGT takes it. The COL would outrank the SGT were they not
// discharged, and has the lower user ID, so neither rank nor the tie break
// gives it to the SGT alone.
func TestHandoverPassesOverADischargedColonelForAServingSergeant(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(t, fake, seedStore(t, testHub()))
	spawnInto(tv, fake, "owner", "chan-a", member("Owner", testRankPVT))
	fake.deliver(tv, voiceEvent("a-col", "chan-a", member("ColDischarged", testRankCOL, testDischargedRole)))
	fake.deliver(tv, voiceEvent("z-sgt", "chan-a", member("Sgt", testRankSGT)))

	fake.deliver(tv, voiceEvent("owner", "", member("Owner", testRankPVT)))

	if owner, _ := tv.Owner("chan-a"); owner != "z-sgt" {
		t.Errorf("Owner(chan-a) = %q, want z-sgt, the serving SGT, over the discharged COL", owner)
	}
}

// An owner who gains the Discharged role keeps their channel while they stay
// in it, through other members joining and leaving. A later rank roles save
// applies the handover rule to every channel, so they hand it over then, to
// the serving PVT beside them.
func TestOwnerWhoGainsTheDischargedRoleHandsOverAtTheNextSave(t *testing.T) {
	fake := newFakeTempVCManager()
	tv := newTestTempVC(t, fake, seedStore(t, testHub()))
	spawnInto(tv, fake, "sgt", "chan-a", member("Sgt", testRankSGT))
	fake.deliver(tv, voiceEvent("pvt", "chan-a", member("Pvt", testRankPVT)))
	// The forum gives the SGT the Discharged role. Their next voice event,
	// a mute in the channel, carries it, as the gateway cache does.
	fake.setCachedRoles("sgt", testRankSGT, testDischargedRole)
	fake.deliver(tv, voiceEvent("sgt", "chan-a", member("Sgt", testRankSGT, testDischargedRole)))

	fake.deliver(tv, voiceEvent("guest", "chan-a", member("Guest")))
	fake.deliver(tv, voiceEvent("guest", "", member("Guest")))

	if owner, _ := tv.Owner("chan-a"); owner != "sgt" {
		t.Fatalf("Owner(chan-a) after a join and a leave = %q, want sgt, who keeps the channel until they leave", owner)
	}

	tv.ApplyRankRoles(DefaultRankLadder())

	if owner, _ := tv.Owner("chan-a"); owner != "pvt" {
		t.Errorf("Owner(chan-a) after a rank roles save = %q, want pvt, the occupant who is a Cav member", owner)
	}
}

// A retiree is a Cav member. A member holding a recording role, a rank role
// and the Retired role starts a recording.
func TestRetiredRankHolderStartsARecording(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	retiree := permMember{id: "user-retired", roles: []string{testRecordingRole, testRankSGT, "role-retired"}}
	sc.fake.setVoice(retiree.id, recordChannel)

	reply := recordAs(t, sc.rt, retiree, "start")

	if strings.Contains(reply, notCavMemberRefusal) {
		t.Errorf("reply = %q, the not-a-Cav-member refusal, want a retiree to start", reply)
	}
	if got := sc.voice.inChannel(recordChannel); len(got) != 1 {
		t.Errorf("recorders in %s = %v, want one", recordChannel, got)
	}
}

// Discord deleting the Discharged role reaches Sentry once, in an issue of
// its own: every discharged trooper counts as a Cav member again, which is
// not what a deleted rank role does.
func TestDeletedDischargedRoleReachesSentryInAnIssueOfItsOwn(t *testing.T) {
	events := recordSentry(t)
	tv := newTestTempVC(t, newFakeTempVCManager(), seedStore(t, testHub()))
	tv.handleRoleDelete(&discordgo.GuildRoleDelete{GuildID: testTempVCGuild, RoleID: testRankSGT})

	tv.handleRoleDelete(&discordgo.GuildRoleDelete{GuildID: testTempVCGuild, RoleID: testDischargedRole})

	got := events.Events()
	if len(got) != 2 {
		t.Fatalf("Sentry events after deleting a rank role and the Discharged role = %d, want 2", len(got))
	}
	if slices.Equal(got[0].Fingerprint, got[1].Fingerprint) {
		t.Errorf("fingerprints of a deleted rank role and a deleted Discharged role = %v and %v, want issues of their own",
			got[0].Fingerprint, got[1].Fingerprint)
	}
}

// A startup reaches Sentry when the Discharged role is not in the guild,
// which catches a role deleted while the bot was down, in an issue apart
// from the missing rank roles'. Once the guild has the Discharged role, the
// startup reports only the missing rank role.
func TestStartupChecksReportAMissingDischargedRoleInAnIssueOfItsOwn(t *testing.T) {
	serveRanks(t, apiRanks())
	mgr := healthyManager()
	st := seedStore(t)
	saveRankRoles(t, st, map[string]string{"SGT": "role-sgt", "GEN": "role-gen"})
	mgr.guild.Roles = append(mgr.guild.Roles, &discordgo.Role{ID: "role-sgt"})
	tv := newTestTempVC(t, mgr, st)

	events := recordSentry(t)
	RunStartupChecks(context.Background(), mgr, testTempVCGuild, testBotUserID, tv)

	got := events.Events()
	if len(got) != 2 {
		t.Fatalf("Sentry events at a startup missing GEN's rank role and the Discharged role = %d, want 2", len(got))
	}
	if slices.Equal(got[0].Fingerprint, got[1].Fingerprint) {
		t.Errorf("fingerprints of the missing rank roles and the missing Discharged role = %v and %v, want issues of their own",
			got[0].Fingerprint, got[1].Fingerprint)
	}

	mgr.guild.Roles = append(mgr.guild.Roles, &discordgo.Role{ID: testDischargedRole})
	events = recordSentry(t)
	RunStartupChecks(context.Background(), mgr, testTempVCGuild, testBotUserID, tv)

	if n := len(events.Events()); n != 1 {
		t.Errorf("Sentry events at a startup with the Discharged role in the guild = %d, want 1, the missing rank role's", n)
	}
}
