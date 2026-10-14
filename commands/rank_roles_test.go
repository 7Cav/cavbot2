package commands

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// The rank roles (#680): a panel admin sets the role that counts as each
// rank on the rank ladder, and everything that asks whether a member is a
// Cav member reads the saved roles.

// testRoleSavedSGT is a role a panel admin saved as SGT's rank role, in
// place of the ladder's role ID in code.
const testRoleSavedSGT = "role-saved-sgt"

// saveRankRoles saves the guild's rank roles through the store's save, the
// one call the panel's rank roles save makes. A rank the map leaves out has
// no rank role.
func saveRankRoles(t *testing.T, st store.Store, roles map[string]string) {
	t.Helper()
	ctx := context.Background()
	current, err := st.GetRankRoles(ctx, testTempVCGuild)
	if err != nil {
		t.Fatalf("GetRankRoles: %v", err)
	}
	entry := store.ChangeLogEntry{ForumUserID: 1, ForumUsername: "admin", Action: store.ChangeRankRoles, Diff: json.RawMessage(`{}`)}
	if err := st.SaveRankRoles(ctx, testTempVCGuild, store.RankRoles{Roles: roles, Version: current.Version}, entry); err != nil {
		t.Fatalf("SaveRankRoles: %v", err)
	}
}

// A runtime started over saved rank roles ranks by them. A hub joiner
// holding the role saved for SGT owns the channel they spawn, and one
// holding only the ladder's SGT role in code, which the save cleared, owns
// nothing.
func TestTempVCStartedOverSavedRankRolesRanksJoinersByThem(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	saveRankRoles(t, st, map[string]string{"SGT": testRoleSavedSGT})
	tv := newTestTempVC(t, fake, st)

	spawnInto(tv, fake, "user-saved", "chan-a", member("Saved", testRoleSavedSGT))
	spawnInto(tv, fake, "user-default", "chan-b", member("Default", testRankSGT))

	if owner, _ := tv.Owner("chan-a"); owner != "user-saved" {
		t.Errorf("Owner(chan-a) = %q, want user-saved, who holds the saved SGT role", owner)
	}
	if owner, _ := tv.Owner("chan-b"); owner != "" {
		t.Errorf("Owner(chan-b) = %q, want no owner: the code's SGT role is no rank role once a save cleared it", owner)
	}
}

// ladderWithout is the ladder with the role IDs in code, but with no rank
// role for the ranks given, as a save that cleared them leaves it.
func ladderWithout(abbrevs ...string) RankLadder {
	roles := make(map[string]string, len(tempVCRankRoles))
	for _, rr := range tempVCRankRoles {
		roles[rr.abbrev] = rr.roleID
	}
	for _, a := range abbrevs {
		delete(roles, a)
	}
	return NewRankLadder(roles)
}

// A save that clears SGT hands each channel an SGT owns over at once, by
// the handover rule. The highest-ranked occupant who still holds a rank
// role takes it, or nobody when none does. Each handover writes the channel's row
// and posts one ownership notice, naming the new owner or nobody.
func TestTempVCSaveClearingARankHandsItsOwnersChannelsOver(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	spawnInto(tv, fake, "sgt-a", "chan-a", member("SgtA", testRankSGT))
	fake.deliver(tv, voiceEvent("pvt-a", "chan-a", member("PvtA", testRankPVT)))
	fake.deliver(tv, voiceEvent("guest-a", "chan-a", member("GuestA")))
	spawnInto(tv, fake, "sgt-b", "chan-b", member("SgtB", testRankSGT))
	fake.deliver(tv, voiceEvent("guest-b", "chan-b", member("GuestB")))
	before := map[string]int{"chan-a": len(noticesIn(t, fake, "chan-a")), "chan-b": len(noticesIn(t, fake, "chan-b"))}

	tv.ApplyRankRoles(ladderWithout("SGT"))

	if owner, _ := tv.Owner("chan-a"); owner != "pvt-a" {
		t.Errorf("Owner(chan-a) = %q, want pvt-a, the occupant who still holds a rank role", owner)
	}
	if row, ok := rowFor(t, st, "chan-a"); !ok || row.OwnerUserID != "pvt-a" {
		t.Errorf("chan-a row = %+v (present %v), want owner pvt-a", row, ok)
	}
	if notices := noticesIn(t, fake, "chan-a")[before["chan-a"]:]; len(notices) != 1 || !strings.Contains(notices[0].data.Content, "pvt-a") {
		t.Errorf("new notices in chan-a = %+v, want one carrying pvt-a", notices)
	}

	if owner, tracked := tv.Owner("chan-b"); owner != "" || !tracked {
		t.Errorf("Owner(chan-b) = %q, %v, want no owner, tracked: no occupant holds a rank role", owner, tracked)
	}
	if row, ok := rowFor(t, st, "chan-b"); !ok || row.OwnerUserID != "" {
		t.Errorf("chan-b row = %+v (present %v), want no owner", row, ok)
	}
	notices := noticesIn(t, fake, "chan-b")[before["chan-b"]:]
	if len(notices) != 1 {
		t.Fatalf("new notices in chan-b = %d, want one saying there is no owner", len(notices))
	}
	if c := notices[0].data.Content; strings.Contains(c, "sgt-b") || strings.Contains(c, "guest-b") {
		t.Errorf("notice %q names a user, want no user ID", c)
	}
}

// A save that makes a role SGT's rank role gives a channel with no owner to
// its occupant who holds that role, with no join or leave. The occupant took
// the role in Discord after joining, so their join carried none of it. The
// save reads their roles from the gateway state cache.
func TestTempVCSaveSettingARankGivesAnOwnerlessChannelToItsHolder(t *testing.T) {
	fake := newFakeTempVCManager()
	st := seedStore(t, testHub())
	tv := newTestTempVC(t, fake, st)
	spawnInto(tv, fake, "guest-c", "chan-c", member("GuestC"))
	fake.deliver(tv, voiceEvent("recruit-c", "chan-c", member("RecruitC")))
	if owner, _ := tv.Owner("chan-c"); owner != "" {
		t.Fatalf("Owner(chan-c) before the save = %q, want no owner", owner)
	}
	fake.setCachedRoles("recruit-c", testRoleSavedSGT)
	before := len(noticesIn(t, fake, "chan-c"))

	tv.ApplyRankRoles(NewRankLadder(map[string]string{"SGT": testRoleSavedSGT}))

	if owner, _ := tv.Owner("chan-c"); owner != "recruit-c" {
		t.Errorf("Owner(chan-c) = %q, want recruit-c, who holds the role saved for SGT", owner)
	}
	if row, ok := rowFor(t, st, "chan-c"); !ok || row.OwnerUserID != "recruit-c" {
		t.Errorf("chan-c row = %+v (present %v), want owner recruit-c", row, ok)
	}
	if notices := noticesIn(t, fake, "chan-c")[before:]; len(notices) != 1 || !strings.Contains(notices[0].data.Content, "recruit-c") {
		t.Errorf("new notices in chan-c = %+v, want one carrying recruit-c", notices)
	}
}

// capturedValue reports whether a capture's key/values carry the value
// given, under whatever key.
func capturedValue(kv []any, want string) bool {
	for i := 1; i < len(kv); i += 2 {
		if v, ok := kv[i].(string); ok && v == want {
			return true
		}
	}
	return false
}

// Discord deleting a role that is a rank's rank role reaches on-call, once,
// naming the rank and the role. Deleting any other role reaches nobody.
func TestTempVCDeletedRankRoleCapturesTheRank(t *testing.T) {
	rec := recordCaptures(t)
	tv := newTestTempVC(t, newFakeTempVCManager(), seedStore(t, testHub()))

	tv.handleRoleDelete(&discordgo.GuildRoleDelete{GuildID: testTempVCGuild, RoleID: "role-not-a-rank"})
	if rec.count != 0 {
		t.Fatalf("captures after deleting a role that is no rank's = %d, want 0", rec.count)
	}

	tv.handleRoleDelete(&discordgo.GuildRoleDelete{GuildID: testTempVCGuild, RoleID: testRankSGT})

	kv := singleCapture(t, rec, errRankRoleDeleted)
	if !capturedValue(kv, "SGT") || !capturedValue(kv, testRankSGT) {
		t.Errorf("capture carries %v, want the rank SGT and the role %s", kv, testRankSGT)
	}
}

// capturesOf returns the key/values of each capture whose error is want.
func capturesOf(rec *captureRecorder, want error) [][]any {
	var out [][]any
	for i, err := range rec.errs {
		if errors.Is(err, want) {
			out = append(out, rec.kvs[i])
		}
	}
	return out
}

// missingRanksOf returns the ranks a missing rank roles capture names,
// found by type so no key name is asserted.
func missingRanksOf(t *testing.T, kv []any) []string {
	t.Helper()
	for _, v := range kv {
		if missing, ok := v.([]missingRankRole); ok {
			var ranks []string
			for _, m := range missing {
				ranks = append(ranks, m.Rank)
			}
			slices.Sort(ranks)
			return ranks
		}
	}
	t.Fatalf("capture carries no missing rank roles: %v", kv)
	return nil
}

// A startup reaches on-call once, naming each rank whose rank role is not in
// the guild, which catches a role deleted while the bot was down. A rank
// with no rank role is not missing. Once every rank role is in the guild,
// the check captures nothing.
func TestStartupChecksCaptureRankRolesMissingFromTheGuild(t *testing.T) {
	serveRanks(t, apiRanks())
	mgr := healthyManager()
	st := seedStore(t)
	saveRankRoles(t, st, map[string]string{"GEN": "role-gen", "SGT": "role-sgt", "PVT": "role-pvt"})
	mgr.guild.Roles = append(mgr.guild.Roles, &discordgo.Role{ID: "role-sgt"})
	tv := newTestTempVC(t, mgr, st)

	rec := recordCaptures(t)
	RunStartupChecks(context.Background(), mgr, testTempVCGuild, testBotUserID, tv)

	captures := capturesOf(rec, errRankRolesMissing)
	if len(captures) != 1 {
		t.Fatalf("missing rank roles captures = %d, want 1", len(captures))
	}
	if got := missingRanksOf(t, captures[0]); !slices.Equal(got, []string{"GEN", "PVT"}) {
		t.Errorf("capture names ranks %v, want GEN and PVT", got)
	}

	mgr.guild.Roles = append(mgr.guild.Roles, &discordgo.Role{ID: "role-gen"}, &discordgo.Role{ID: "role-pvt"})
	rec = recordCaptures(t)
	RunStartupChecks(context.Background(), mgr, testTempVCGuild, testBotUserID, tv)

	if n := len(capturesOf(rec, errRankRolesMissing)); n != 0 {
		t.Errorf("missing rank roles captures with every rank role in the guild = %d, want 0", n)
	}
}

// A save that gives SGT a role makes its holders Cav members to /record
// start at once. A recording role holder holding it is refused as no Cav
// member before the save, and starts a recording after it.
func TestRecordStartReadsTheSavedRankRoles(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	holder := permMember{id: "user-r", roles: []string{testRecordingRole, testRoleSavedSGT}}
	sc.fake.setVoice(holder.id, recordChannel)

	if reply := recordAs(t, sc.rt, holder, "start"); !strings.Contains(reply, notCavMemberRefusal) {
		t.Fatalf("reply before the save = %q, want the not-a-Cav-member refusal", reply)
	}

	sc.tv.ApplyRankRoles(NewRankLadder(map[string]string{"SGT": testRoleSavedSGT}))

	reply := recordAs(t, sc.rt, holder, "start")
	if strings.Contains(reply, notCavMemberRefusal) {
		t.Errorf("reply after the save = %q, still the not-a-Cav-member refusal", reply)
	}
	if got := sc.voice.inChannel(recordChannel); len(got) != 1 {
		t.Errorf("recorders in %s after the save's start = %v, want one", recordChannel, got)
	}
}
