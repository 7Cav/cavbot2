package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
)

// The rank roles (#680): the role that counts as each rank on the rank
// ladder, saved for the whole ladder at once, versioned the way the
// recording roles are.

// rankRolesEntry is a rank roles save's entry, with an ordinal in its diff.
func rankRolesEntry(ordinal int) ChangeLogEntry {
	e := changeEntry(ordinal)
	e.Action = ChangeRankRoles
	return e
}

// storedRankRoles reads guild-1's rank roles and their version.
func storedRankRoles(t *testing.T, s Store) RankRoles {
	t.Helper()
	roles, err := s.GetRankRoles(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("GetRankRoles: %v", err)
	}
	return roles
}

// rankRoleOrdinals lists the ordinals of the rank roles section's entries,
// newest first.
func rankRoleOrdinals(t *testing.T, s Store) []int {
	t.Helper()
	entries, err := s.ListRankRoleChanges(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRankRoleChanges: %v", err)
	}
	var ordinals []int
	for _, e := range entries {
		ordinals = append(ordinals, ordinalOf(t, e))
	}
	return ordinals
}

// A guild with nothing saved reads back at version 0, the store's sign that
// no admin has saved the rank roles. A save at the version read writes each
// rank's role, a rank with none included, and the section's list holds its
// entry and no recording roles save's. A second save from a form loaded
// before the first is stale and writes neither its roles nor its entry.
func TestRankRolesRoundTripAndStaleSave(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		loaded := storedRankRoles(t, s)
		if loaded.Version != 0 {
			t.Errorf("GetRankRoles with nothing saved is at version %d, want 0", loaded.Version)
		}

		want := map[string]string{"GOA": "role-goa", "SGT": "role-sgt", "RCT": ""}
		if err := s.SaveRankRoles(ctx, "guild-1", RankRoles{Roles: want, Version: loaded.Version}, rankRolesEntry(1)); err != nil {
			t.Fatalf("SaveRankRoles: %v", err)
		}
		if err := saveRecordingRoles(t, s, []string{"role-s2"}, recordingRolesEntry(2)); err != nil {
			t.Fatalf("SaveRecordingRoles: %v", err)
		}
		saved := storedRankRoles(t, s)
		if !maps.Equal(saved.Roles, want) {
			t.Errorf("GetRankRoles = %v, want %v", saved.Roles, want)
		}

		stale := RankRoles{Roles: map[string]string{"GOA": "role-other"}, Version: loaded.Version}
		if err := s.SaveRankRoles(ctx, "guild-1", stale, rankRolesEntry(3)); !errors.Is(err, ErrStale) {
			t.Fatalf("SaveRankRoles from a form loaded before the first save = %v, want ErrStale", err)
		}
		if got := storedRankRoles(t, s); !maps.Equal(got.Roles, want) || got.Version != saved.Version {
			t.Errorf("after the stale save GetRankRoles = %+v, want the first save's %v at version %d", got, want, saved.Version)
		}
		if got := rankRoleOrdinals(t, s); !slices.Equal(got, []int{1}) {
			t.Errorf("rank roles changes are ordinals %v, want [1]: the first save's alone", got)
		}
	})
}

// The Discharged role (#893) saves with the rank roles. A guild with
// nothing saved reads back no Discharged role saved, nil, so the forum's
// applies. A save writes the role, and a save of none reads back as empty,
// not as never saved.
func TestRankRolesSaveTheDischargedRole(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if got := storedRankRoles(t, s).DischargedRoleID; got != nil {
			t.Errorf("Discharged role with nothing saved = %q, want nil", *got)
		}

		roles := map[string]string{"SGT": "role-sgt"}
		if err := s.SaveRankRoles(ctx, "guild-1", RankRoles{Roles: roles, DischargedRoleID: new("role-discharged")}, rankRolesEntry(1)); err != nil {
			t.Fatalf("SaveRankRoles: %v", err)
		}
		saved := storedRankRoles(t, s)
		if saved.DischargedRoleID == nil || *saved.DischargedRoleID != "role-discharged" {
			t.Errorf("Discharged role after its save = %v, want role-discharged", saved.DischargedRoleID)
		}

		if err := s.SaveRankRoles(ctx, "guild-1", RankRoles{Roles: roles, DischargedRoleID: new(""), Version: saved.Version}, rankRolesEntry(2)); err != nil {
			t.Fatalf("SaveRankRoles of none: %v", err)
		}
		if got := storedRankRoles(t, s).DischargedRoleID; got == nil || *got != "" {
			t.Errorf("Discharged role after a save of none = %v, want empty, saved as none", got)
		}
	})
}

// A rank roles row saved before the Discharged role came reads back with
// its roles and no Discharged role saved, nil, so the guild keeps the
// forum's rather than having none.
func TestRankRolesSavedBeforeTheDischargedRoleHaveNoneSaved(t *testing.T) {
	raw := migratedTo(t, 20261025183726)
	if _, err := raw.ExecContext(context.Background(), `
		INSERT INTO rank_roles (guild_id, roles, version) VALUES ('guild-1', '{"SGT": "role-sgt"}', 1)`); err != nil {
		t.Fatalf("insert a rank roles row as the previous release wrote it: %v", err)
	}

	got := storedRankRoles(t, openMigrated(t))

	if got.Roles["SGT"] != "role-sgt" || got.Version != 1 {
		t.Errorf("GetRankRoles = %+v, want SGT's role-sgt at version 1", got)
	}
	if got.DischargedRoleID != nil {
		t.Errorf("Discharged role of a row saved before it = %q, want nil", *got.DischargedRoleID)
	}
}
