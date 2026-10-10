package store

import (
	"context"
	"slices"
	"testing"
)

// The recording roles (#383): the guild-wide set of roles whose holders may
// record, saved the way the guild-wide moderator roles are.

// recordingRolesEntry is a recording roles save's entry, with an ordinal in
// its diff.
func recordingRolesEntry(ordinal int) ChangeLogEntry {
	e := changeEntry(ordinal)
	e.Action = ChangeRecordingRoles
	return e
}

// storedRecordingRoles reads guild-1's recording roles and their version.
func storedRecordingRoles(t *testing.T, s Store) RecordingRoles {
	t.Helper()
	roles, err := s.GetRecordingRoles(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("GetRecordingRoles: %v", err)
	}
	return roles
}

// saveRecordingRoles saves guild-1's recording roles over the version the
// store holds now, as a save from a section loaded just before it.
func saveRecordingRoles(t *testing.T, s Store, roleIDs []string, entry ChangeLogEntry) error {
	t.Helper()
	current := storedRecordingRoles(t, s)
	return s.SaveRecordingRoles(context.Background(), "guild-1", RecordingRoles{RoleIDs: roleIDs, Version: current.Version}, entry)
}

// A guild with no recording roles saved reads back none, at version 0. A
// save reads back as the set it was saved as, a second save replaces it,
// and the section's list holds each save's one entry, newest first, and
// none of a guild-wide moderator save's.
func TestRecordingRolesRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if got := storedRecordingRoles(t, s); len(got.RoleIDs) != 0 || got.Version != 0 {
			t.Errorf("GetRecordingRoles with no row = %+v, want no roles at version 0", got)
		}

		want := []string{"role-s2", "role-s6"}
		if err := saveRecordingRoles(t, s, want, recordingRolesEntry(1)); err != nil {
			t.Fatalf("SaveRecordingRoles: %v", err)
		}
		if got := storedRecordingRoles(t, s).RoleIDs; !slices.Equal(sortedRoles(got), want) {
			t.Errorf("GetRecordingRoles = %v, want %v (as a set)", got, want)
		}

		if err := saveGuildRoles(t, s, []string{"role-mp"}, moderatorsEntry(2)); err != nil {
			t.Fatalf("SaveGuildModeratorRoles: %v", err)
		}
		if err := saveRecordingRoles(t, s, []string{"role-hq"}, recordingRolesEntry(3)); err != nil {
			t.Fatalf("second SaveRecordingRoles: %v", err)
		}
		if got := storedRecordingRoles(t, s).RoleIDs; !slices.Equal(got, []string{"role-hq"}) {
			t.Errorf("GetRecordingRoles after the second save = %v, want [role-hq]", got)
		}
		entries, err := s.ListRecordingRoleChanges(context.Background(), 10)
		if err != nil {
			t.Fatalf("ListRecordingRoleChanges: %v", err)
		}
		var ordinals []int
		for _, e := range entries {
			ordinals = append(ordinals, ordinalOf(t, e))
		}
		if !slices.Equal(ordinals, []int{3, 1}) {
			t.Errorf("recording roles changes are ordinals %v, want [3 1]", ordinals)
		}
	})
}

// A save over a stored row moves the set to another version, so a section
// loaded before the save is stale.
func TestRecordingRolesSaveOverAStoredRowMovesItsVersion(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		if err := saveRecordingRoles(t, s, []string{"role-s2"}, recordingRolesEntry(1)); err != nil {
			t.Fatalf("SaveRecordingRoles: %v", err)
		}
		loaded := storedRecordingRoles(t, s)

		saved := RecordingRoles{RoleIDs: []string{"role-s6"}, Version: loaded.Version}
		if err := s.SaveRecordingRoles(context.Background(), "guild-1", saved, recordingRolesEntry(2)); err != nil {
			t.Fatalf("SaveRecordingRoles over the stored row: %v", err)
		}

		if got := storedRecordingRoles(t, s).Version; got == loaded.Version {
			t.Errorf("after a save over version %d the set reads back at %d, want another version", loaded.Version, got)
		}
	})
}
