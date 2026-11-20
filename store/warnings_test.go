package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// Warnings (#971): one row per /warn, kept per guild and member, and the
// warning immune roles, saved the way the recording roles are.

// addWarning stores a warning of the member in the guild with the reason
// given.
func addWarning(t *testing.T, s Store, guildID, memberID, reason string) Warning {
	t.Helper()
	w, err := s.AddWarning(context.Background(), Warning{
		GuildID: guildID, MemberID: memberID, ModeratorID: "mod-1", ChannelID: "chan-1", Reason: reason,
	})
	if err != nil {
		t.Fatalf("AddWarning(%s): %v", reason, err)
	}
	return w
}

// listedReasons lists the member's warnings in the guild, in the order the
// store returns them, by reason.
func listedReasons(t *testing.T, s Store, guildID, memberID string) []string {
	t.Helper()
	ws, err := s.ListWarnings(context.Background(), guildID, memberID)
	if err != nil {
		t.Fatalf("ListWarnings(%s, %s): %v", guildID, memberID, err)
	}
	reasons := []string{}
	for _, w := range ws {
		reasons = append(reasons, w.Reason)
	}
	return reasons
}

// A member's warnings list newest first, theirs in that guild alone. A
// notice recorded on one shows in the list. An ID of another guild's
// warning neither takes a notice nor removes it, and a removal returns the
// warning it deleted.
func TestWarningsAddListNoticeAndRemove(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if got := listedReasons(t, s, "guild-1", "member-1"); len(got) != 0 {
			t.Fatalf("warnings with none added = %v, want none", got)
		}
		first := addWarning(t, s, "guild-1", "member-1", "first")
		if first.ID == 0 || first.CreatedAt.IsZero() {
			t.Errorf("AddWarning = %+v, want ID and CreatedAt set", first)
		}
		second := addWarning(t, s, "guild-1", "member-1", "second")
		addWarning(t, s, "guild-1", "member-1", "third")
		addWarning(t, s, "guild-1", "member-2", "other member")
		other := addWarning(t, s, "guild-2", "member-1", "other guild")

		if got, want := listedReasons(t, s, "guild-1", "member-1"), []string{"third", "second", "first"}; !slices.Equal(got, want) {
			t.Errorf("member-1's warnings in guild-1 = %v, want %v", got, want)
		}

		if err := s.SetWarningNotice(ctx, "guild-1", first.ID, "msg-1"); err != nil {
			t.Fatalf("SetWarningNotice: %v", err)
		}
		if err := s.SetWarningNotice(ctx, "guild-2", first.ID, "msg-x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetWarningNotice under another guild = %v, want ErrNotFound", err)
		}
		ws, err := s.ListWarnings(ctx, "guild-1", "member-1")
		if err != nil {
			t.Fatalf("ListWarnings: %v", err)
		}
		if got := ws[len(ws)-1]; got.ID != first.ID || got.MessageID != "msg-1" {
			t.Errorf("oldest warning after the notice = %+v, want ID %d with message msg-1", got, first.ID)
		}

		if _, err := s.RemoveWarning(ctx, "guild-1", other.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("RemoveWarning of guild-2's warning under guild-1 = %v, want ErrNotFound", err)
		}
		if got := listedReasons(t, s, "guild-2", "member-1"); !slices.Equal(got, []string{"other guild"}) {
			t.Errorf("guild-2's warnings after the refused removal = %v, want it kept", got)
		}
		removed, err := s.RemoveWarning(ctx, "guild-1", second.ID)
		if err != nil {
			t.Fatalf("RemoveWarning: %v", err)
		}
		if removed.ID != second.ID || removed.Reason != "second" {
			t.Errorf("RemoveWarning = %+v, want the second warning", removed)
		}
		if got, want := listedReasons(t, s, "guild-1", "member-1"), []string{"third", "first"}; !slices.Equal(got, want) {
			t.Errorf("member-1's warnings after the removal = %v, want %v", got, want)
		}
		if _, err := s.RemoveWarning(ctx, "guild-1", second.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("RemoveWarning of a removed warning = %v, want ErrNotFound", err)
		}
	})
}

// warningImmuneRolesEntry is a warning immune roles save's entry, with an
// ordinal in its diff.
func warningImmuneRolesEntry(ordinal int) ChangeLogEntry {
	e := changeEntry(ordinal)
	e.Action = ChangeWarningImmuneRoles
	return e
}

// storedWarningImmuneRoles reads guild-1's warning immune roles and their
// version.
func storedWarningImmuneRoles(t *testing.T, s Store) WarningImmuneRoles {
	t.Helper()
	roles, err := s.GetWarningImmuneRoles(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("GetWarningImmuneRoles: %v", err)
	}
	return roles
}

// A guild with nothing saved reads back no roles at version 0. A save reads
// back as the set it was saved as, and the section's list holds its entry
// and no recording roles save's. A second save from a section loaded before
// the first is stale and writes neither its set nor its entry.
func TestWarningImmuneRolesRoundTripAndStaleSave(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		loaded := storedWarningImmuneRoles(t, s)
		if len(loaded.RoleIDs) != 0 || loaded.Version != 0 {
			t.Errorf("GetWarningImmuneRoles with no row = %+v, want no roles at version 0", loaded)
		}

		want := []string{"role-nco", "role-officer"}
		if err := s.SaveWarningImmuneRoles(ctx, "guild-1", WarningImmuneRoles{RoleIDs: want, Version: loaded.Version}, warningImmuneRolesEntry(1)); err != nil {
			t.Fatalf("SaveWarningImmuneRoles: %v", err)
		}
		if err := saveRecordingRoles(t, s, []string{"role-s2"}, recordingRolesEntry(2)); err != nil {
			t.Fatalf("SaveRecordingRoles: %v", err)
		}
		saved := storedWarningImmuneRoles(t, s)
		if !slices.Equal(sortedRoles(saved.RoleIDs), want) {
			t.Errorf("GetWarningImmuneRoles = %v, want %v (as a set)", saved.RoleIDs, want)
		}

		stale := WarningImmuneRoles{RoleIDs: []string{"role-other"}, Version: loaded.Version}
		if err := s.SaveWarningImmuneRoles(ctx, "guild-1", stale, warningImmuneRolesEntry(3)); !errors.Is(err, ErrStale) {
			t.Fatalf("SaveWarningImmuneRoles from a section loaded before the first save = %v, want ErrStale", err)
		}
		if got := storedWarningImmuneRoles(t, s); !slices.Equal(sortedRoles(got.RoleIDs), want) || got.Version != saved.Version {
			t.Errorf("after the stale save GetWarningImmuneRoles = %+v, want the first save's %v at version %d", got, want, saved.Version)
		}
		entries, err := s.ListWarningImmuneRoleChanges(ctx, 10)
		if err != nil {
			t.Fatalf("ListWarningImmuneRoleChanges: %v", err)
		}
		var ordinals []int
		for _, e := range entries {
			ordinals = append(ordinals, ordinalOf(t, e))
		}
		if !slices.Equal(ordinals, []int{1}) {
			t.Errorf("warning immune roles changes are ordinals %v, want [1]: the first save's alone", ordinals)
		}
	})
}
