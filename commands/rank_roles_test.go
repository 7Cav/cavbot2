package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/7cav/cavbot2/store"
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

// A runtime started over saved rank roles ranks by them: a hub joiner
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
