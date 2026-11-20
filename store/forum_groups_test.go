package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// forumGroupsEntry is a forum groups save's entry, with an ordinal in its
// diff.
func forumGroupsEntry(ordinal int) ChangeLogEntry {
	e := changeEntry(ordinal)
	e.Action = ChangeForumGroups
	return e
}

// storedForumGroups reads guild-1's forum groups and their version.
func storedForumGroups(t *testing.T, s Store) ForumGroups {
	t.Helper()
	groups, err := s.GetForumGroups(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("GetForumGroups: %v", err)
	}
	return groups
}

// A guild with nothing saved reads back the Foxhole group 323 and no Joiners
// groups at version 0. A save reads back as saved, and the section's list
// holds its entry and no AO roles save's. A second save from a section
// loaded before the first is stale and writes neither its groups nor its
// entry.
func TestForumGroupsRoundTripAndStaleSave(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		loaded := storedForumGroups(t, s)
		if loaded.FoxholeGroupID != 323 || len(loaded.JoinersGroupIDs) != 0 || loaded.Version != 0 {
			t.Errorf("GetForumGroups with no row = %+v, want the Foxhole group 323 and no Joiners groups at version 0", loaded)
		}

		want := ForumGroups{FoxholeGroupID: 400, JoinersGroupIDs: []int{410, 411}, Version: loaded.Version}
		if err := s.SaveForumGroups(ctx, "guild-1", want, forumGroupsEntry(1)); err != nil {
			t.Fatalf("SaveForumGroups: %v", err)
		}
		if err := s.SaveAORoles(ctx, "guild-1", AORoles{RoleIDs: []string{"role-sc"}}, aoRolesEntry(2)); err != nil {
			t.Fatalf("SaveAORoles: %v", err)
		}
		saved := storedForumGroups(t, s)
		joiners := slices.Sorted(slices.Values(saved.JoinersGroupIDs))
		if saved.FoxholeGroupID != want.FoxholeGroupID || !slices.Equal(joiners, want.JoinersGroupIDs) {
			t.Errorf("GetForumGroups = %+v, want the Foxhole group 400 and the Joiners groups [410 411]", saved)
		}

		stale := ForumGroups{FoxholeGroupID: 500, Version: loaded.Version}
		if err := s.SaveForumGroups(ctx, "guild-1", stale, forumGroupsEntry(3)); !errors.Is(err, ErrStale) {
			t.Fatalf("SaveForumGroups from a section loaded before the first save = %v, want ErrStale", err)
		}
		if got := storedForumGroups(t, s); got.FoxholeGroupID != want.FoxholeGroupID || got.Version != saved.Version {
			t.Errorf("after the stale save GetForumGroups = %+v, want the first save's at version %d", got, saved.Version)
		}
		entries, err := s.ListForumGroupChanges(ctx, 10)
		if err != nil {
			t.Fatalf("ListForumGroupChanges: %v", err)
		}
		var ordinals []int
		for _, e := range entries {
			ordinals = append(ordinals, ordinalOf(t, e))
		}
		if !slices.Equal(ordinals, []int{1}) {
			t.Errorf("forum groups changes are ordinals %v, want [1]: the first save's alone", ordinals)
		}
	})
}
