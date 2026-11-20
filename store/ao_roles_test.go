package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// The AO roles (#231): the roles that mark a member as playing one of the
// regiment's games, saved the way the recording roles are.

// aoRolesEntry is an AO roles save's entry, with an ordinal in its diff.
func aoRolesEntry(ordinal int) ChangeLogEntry {
	e := changeEntry(ordinal)
	e.Action = ChangeAORoles
	return e
}

// storedAORoles reads guild-1's AO roles and their version.
func storedAORoles(t *testing.T, s Store) AORoles {
	t.Helper()
	roles, err := s.GetAORoles(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("GetAORoles: %v", err)
	}
	return roles
}

// A guild with nothing saved reads back no roles at version 0. A save reads
// back as the set it was saved as, and the section's list holds its entry
// and no recording roles save's. A second save from a section loaded before
// the first is stale and writes neither its set nor its entry.
func TestAORolesRoundTripAndStaleSave(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		loaded := storedAORoles(t, s)
		if len(loaded.RoleIDs) != 0 || loaded.Version != 0 {
			t.Errorf("GetAORoles with no row = %+v, want no roles at version 0", loaded)
		}

		want := []string{"role-arma", "role-sc"}
		if err := s.SaveAORoles(ctx, "guild-1", AORoles{RoleIDs: want, Version: loaded.Version}, aoRolesEntry(1)); err != nil {
			t.Fatalf("SaveAORoles: %v", err)
		}
		if err := saveRecordingRoles(t, s, []string{"role-s2"}, recordingRolesEntry(2)); err != nil {
			t.Fatalf("SaveRecordingRoles: %v", err)
		}
		saved := storedAORoles(t, s)
		if !slices.Equal(sortedRoles(saved.RoleIDs), want) {
			t.Errorf("GetAORoles = %v, want %v (as a set)", saved.RoleIDs, want)
		}

		stale := AORoles{RoleIDs: []string{"role-other"}, Version: loaded.Version}
		if err := s.SaveAORoles(ctx, "guild-1", stale, aoRolesEntry(3)); !errors.Is(err, ErrStale) {
			t.Fatalf("SaveAORoles from a section loaded before the first save = %v, want ErrStale", err)
		}
		if got := storedAORoles(t, s); !slices.Equal(sortedRoles(got.RoleIDs), want) || got.Version != saved.Version {
			t.Errorf("after the stale save GetAORoles = %+v, want the first save's %v at version %d", got, want, saved.Version)
		}
		entries, err := s.ListAORoleChanges(ctx, 10)
		if err != nil {
			t.Fatalf("ListAORoleChanges: %v", err)
		}
		var ordinals []int
		for _, e := range entries {
			ordinals = append(ordinals, ordinalOf(t, e))
		}
		if !slices.Equal(ordinals, []int{1}) {
			t.Errorf("AO roles changes are ordinals %v, want [1]: the first save's alone", ordinals)
		}
	})
}
