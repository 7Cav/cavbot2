package store

import (
	"context"
	"slices"
	"testing"
)

// The winged aviators (#869): the members someone gave the Winged Aviator
// role, by Discord user ID, kept per guild.

// wingedAviators lists the guild's winged aviators, sorted, since the store
// promises no order.
func wingedAviators(t *testing.T, s Store, guildID string) []string {
	t.Helper()
	ids, err := s.ListWingedAviators(context.Background(), guildID)
	if err != nil {
		t.Fatalf("ListWingedAviators(%s): %v", guildID, err)
	}
	slices.Sort(ids)
	return ids
}

// isWingedAviator reads one member's status.
func isWingedAviator(t *testing.T, s Store, guildID, userID string) bool {
	t.Helper()
	is, err := s.IsWingedAviator(context.Background(), guildID, userID)
	if err != nil {
		t.Fatalf("IsWingedAviator(%s, %s): %v", guildID, userID, err)
	}
	return is
}

// An added member is a winged aviator of their guild alone, listed once
// however often they're added, and a removal ends it. Removing a member who
// isn't one is no error.
func TestWingedAviatorsAddListAndRemove(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if got := wingedAviators(t, s, "guild-1"); len(got) != 0 {
			t.Fatalf("winged aviators with nothing added = %v, want none", got)
		}
		for _, id := range []string{"user-1", "user-2", "user-1"} {
			if err := s.AddWingedAviator(ctx, "guild-1", id); err != nil {
				t.Fatalf("AddWingedAviator(%s): %v", id, err)
			}
		}
		if err := s.AddWingedAviator(ctx, "guild-2", "user-3"); err != nil {
			t.Fatalf("AddWingedAviator(guild-2): %v", err)
		}

		if got, want := wingedAviators(t, s, "guild-1"), []string{"user-1", "user-2"}; !slices.Equal(got, want) {
			t.Errorf("guild-1's winged aviators = %v, want %v", got, want)
		}
		if !isWingedAviator(t, s, "guild-1", "user-1") {
			t.Error("user-1 isn't a winged aviator of guild-1 after the add")
		}
		if isWingedAviator(t, s, "guild-2", "user-1") {
			t.Error("user-1 is a winged aviator of guild-2, added only to guild-1")
		}

		if err := s.RemoveWingedAviator(ctx, "guild-1", "user-1"); err != nil {
			t.Fatalf("RemoveWingedAviator: %v", err)
		}
		if err := s.RemoveWingedAviator(ctx, "guild-1", "user-9"); err != nil {
			t.Errorf("RemoveWingedAviator of a member never added: %v, want no error", err)
		}
		if isWingedAviator(t, s, "guild-1", "user-1") {
			t.Error("user-1 is still a winged aviator after the removal")
		}
		if got, want := wingedAviators(t, s, "guild-1"), []string{"user-2"}; !slices.Equal(got, want) {
			t.Errorf("guild-1's winged aviators after the removal = %v, want %v", got, want)
		}
		if got, want := wingedAviators(t, s, "guild-2"), []string{"user-3"}; !slices.Equal(got, want) {
			t.Errorf("guild-2's winged aviators = %v, want %v", got, want)
		}
	})
}
