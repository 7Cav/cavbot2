package store

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/7cav/cavbot2/utils"
)

// TestMain initializes the package-level logger the migrate step logs through,
// so Open does not fail on a nil *slog.Logger.
func TestMain(m *testing.M) {
	utils.Logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	os.Exit(m.Run())
}

// testDSNVar names the Postgres the store tests run against. Unset means the
// Postgres half of the suite skips and only the Fake runs, which is what
// happens on a laptop with no database; CI sets it.
const testDSNVar = "TEST_BOT_DB_DSN"

// forEachStore runs one contract case against every implementation, so the
// Fake and Postgres are held to the same calls and results. Each case gets a
// fresh store.
func forEachStore(t *testing.T, run func(t *testing.T, s Store)) {
	t.Helper()
	t.Run("fake", func(t *testing.T) {
		run(t, NewFake())
	})
	t.Run("postgres", func(t *testing.T) {
		run(t, openTestPostgres(t))
	})
}

// openTestPostgres drops and recreates the public schema, then opens the store
// through Open so the migrate step is what prepares the database. No table is
// named here: a wrong embed path or a bad migration reddens every Postgres case.
func openTestPostgres(t *testing.T) Store {
	t.Helper()
	dsn := os.Getenv(testDSNVar)
	if dsn == "" {
		t.Skipf("%s not set", testDSNVar)
	}
	ctx := context.Background()

	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// sampleHub is the fixture every hub case starts from.
func sampleHub(guildID, hubChannelID string) Hub {
	return Hub{
		GuildID:          guildID,
		HubChannelID:     hubChannelID,
		BaseString:       "Arma Voice",
		PermissionSource: PermissionHubChannel,
		ModeratorRoleIDs: []string{"role-mp", "role-hq"},
		UserLimit:        12,
		Bitrate:          96000,
		Enabled:          true,
	}
}

// sortedRoles copies and sorts a role ID list so two sets compare regardless
// of the order the store returns them in.
func sortedRoles(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// assertHubSettings compares the caller-supplied fields of two hubs: everything
// except ID and the store-set times.
func assertHubSettings(t *testing.T, got, want Hub) {
	t.Helper()
	if got.GuildID != want.GuildID {
		t.Errorf("GuildID = %q, want %q", got.GuildID, want.GuildID)
	}
	if got.HubChannelID != want.HubChannelID {
		t.Errorf("HubChannelID = %q, want %q", got.HubChannelID, want.HubChannelID)
	}
	if got.BaseString != want.BaseString {
		t.Errorf("BaseString = %q, want %q", got.BaseString, want.BaseString)
	}
	if got.PermissionSource != want.PermissionSource {
		t.Errorf("PermissionSource = %q, want %q", got.PermissionSource, want.PermissionSource)
	}
	if !slices.Equal(sortedRoles(got.ModeratorRoleIDs), sortedRoles(want.ModeratorRoleIDs)) {
		t.Errorf("ModeratorRoleIDs = %v, want %v (as a set)", got.ModeratorRoleIDs, want.ModeratorRoleIDs)
	}
	if got.UserLimit != want.UserLimit {
		t.Errorf("UserLimit = %d, want %d", got.UserLimit, want.UserLimit)
	}
	if got.Bitrate != want.Bitrate {
		t.Errorf("Bitrate = %d, want %d", got.Bitrate, want.Bitrate)
	}
	if got.Enabled != want.Enabled {
		t.Errorf("Enabled = %v, want %v", got.Enabled, want.Enabled)
	}
}

// T1: a stored hub reads back with the settings it was written with, and the
// store fills in the ID and both times.
func TestUpsertHubThenGet(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		want := sampleHub("guild-1", "hub-1")

		stored, err := s.UpsertHub(ctx, want)
		if err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
		if stored.ID == 0 {
			t.Error("UpsertHub returned ID 0, want a stored ID")
		}
		if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
			t.Errorf("UpsertHub returned CreatedAt %v, UpdatedAt %v, want both set", stored.CreatedAt, stored.UpdatedAt)
		}

		got, err := s.GetHub(ctx, stored.ID)
		if err != nil {
			t.Fatalf("GetHub: %v", err)
		}
		assertHubSettings(t, got, want)
	})
}

// T2: a second upsert for the same hub channel changes the settings of the
// existing row and creates no second row.
func TestUpsertHubUpdatesInPlace(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		first, err := s.UpsertHub(ctx, sampleHub("guild-1", "hub-1"))
		if err != nil {
			t.Fatalf("first UpsertHub: %v", err)
		}

		want := sampleHub("guild-1", "hub-1")
		want.BaseString = "Briefing"
		want.PermissionSource = PermissionCategory
		want.ModeratorRoleIDs = nil
		want.UserLimit = 0
		want.Bitrate = 64000
		want.Enabled = false

		second, err := s.UpsertHub(ctx, want)
		if err != nil {
			t.Fatalf("second UpsertHub: %v", err)
		}
		if second.ID != first.ID {
			t.Errorf("second UpsertHub returned ID %d, want the first row's %d", second.ID, first.ID)
		}

		got, err := s.GetHub(ctx, first.ID)
		if err != nil {
			t.Fatalf("GetHub: %v", err)
		}
		assertHubSettings(t, got, want)
		if len(got.ModeratorRoleIDs) != 0 {
			t.Errorf("ModeratorRoleIDs = %v, want none", got.ModeratorRoleIDs)
		}

		hubs, err := s.ListHubs(ctx, "guild-1")
		if err != nil {
			t.Fatalf("ListHubs: %v", err)
		}
		if len(hubs) != 1 {
			t.Errorf("ListHubs returned %d hubs, want 1", len(hubs))
		}
	})
}

// T3: an ID nothing was written under is ErrNotFound.
func TestGetHubUnknown(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		_, err := s.GetHub(context.Background(), 404)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("GetHub(404) error = %v, want ErrNotFound", err)
		}
	})
}

// hubChannelIDs collects the hub channel IDs of a list, sorted, so two lists
// compare as sets.
func hubChannelIDs(hubs []Hub) []string {
	ids := make([]string, 0, len(hubs))
	for _, h := range hubs {
		ids = append(ids, h.HubChannelID)
	}
	slices.Sort(ids)
	return ids
}

// T4: ListHubs returns every hub of the guild and none of another guild's.
func TestListHubsByGuild(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		for _, h := range []Hub{
			sampleHub("guild-a", "hub-a1"),
			sampleHub("guild-a", "hub-a2"),
			sampleHub("guild-b", "hub-b1"),
		} {
			if _, err := s.UpsertHub(ctx, h); err != nil {
				t.Fatalf("UpsertHub(%s): %v", h.HubChannelID, err)
			}
		}

		hubs, err := s.ListHubs(ctx, "guild-a")
		if err != nil {
			t.Fatalf("ListHubs: %v", err)
		}
		if got, want := hubChannelIDs(hubs), []string{"hub-a1", "hub-a2"}; !slices.Equal(got, want) {
			t.Errorf("ListHubs(guild-a) = %v, want %v", got, want)
		}
	})
}

// T5: a deleted hub is gone from GetHub and ListHubs, and deleting it again is
// not an error.
func TestDeleteHub(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		stored, err := s.UpsertHub(ctx, sampleHub("guild-1", "hub-1"))
		if err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}

		if err := s.DeleteHub(ctx, stored.ID); err != nil {
			t.Fatalf("DeleteHub: %v", err)
		}
		if _, err := s.GetHub(ctx, stored.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetHub after delete error = %v, want ErrNotFound", err)
		}
		hubs, err := s.ListHubs(ctx, "guild-1")
		if err != nil {
			t.Fatalf("ListHubs: %v", err)
		}
		if len(hubs) != 0 {
			t.Errorf("ListHubs after delete = %v, want none", hubChannelIDs(hubs))
		}
		if err := s.DeleteHub(ctx, stored.ID); err != nil {
			t.Errorf("second DeleteHub error = %v, want nil", err)
		}
	})
}

// storeHub writes a hub and returns its stored ID, for the spawned cases that
// need a hub to reference.
func storeHub(t *testing.T, s Store, hubChannelID string) int64 {
	t.Helper()
	stored, err := s.UpsertHub(context.Background(), sampleHub("guild-1", hubChannelID))
	if err != nil {
		t.Fatalf("UpsertHub(%s): %v", hubChannelID, err)
	}
	return stored.ID
}

// findSpawned returns the row for a channel from a list, or fails the test.
func findSpawned(t *testing.T, rows []SpawnedChannel, channelID string) SpawnedChannel {
	t.Helper()
	for _, r := range rows {
		if r.ChannelID == channelID {
			return r
		}
	}
	t.Fatalf("ListSpawnedChannels has no row for %q, got %v", channelID, rows)
	return SpawnedChannel{}
}

// T6: a spawned row reads back with the fields it was written with, and a
// second write for the same channel (a handover) replaces the owner in place.
func TestUpsertSpawnedThenList(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		want := SpawnedChannel{ChannelID: "chan-1", HubID: hubID, Number: 3, OwnerUserID: "user-creator"}

		if err := s.UpsertSpawnedChannel(ctx, want); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}
		rows, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		got := findSpawned(t, rows, "chan-1")
		if got.HubID != want.HubID || got.Number != want.Number || got.OwnerUserID != want.OwnerUserID {
			t.Errorf("ListSpawnedChannels row = %+v, want HubID %d, Number %d, OwnerUserID %q",
				got, want.HubID, want.Number, want.OwnerUserID)
		}

		want.OwnerUserID = "user-next"
		if err := s.UpsertSpawnedChannel(ctx, want); err != nil {
			t.Fatalf("second UpsertSpawnedChannel: %v", err)
		}
		rows, err = s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		if len(rows) != 1 {
			t.Errorf("ListSpawnedChannels returned %d rows after a second upsert, want 1", len(rows))
		}
		if got := findSpawned(t, rows, "chan-1"); got.OwnerUserID != "user-next" {
			t.Errorf("OwnerUserID after handover = %q, want %q", got.OwnerUserID, "user-next")
		}
	})
}

// T7: a channel with no owner round-trips with an empty OwnerUserID.
func TestUpsertSpawnedNoOwner(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		if err := s.UpsertSpawnedChannel(ctx, SpawnedChannel{ChannelID: "chan-1", HubID: hubID, Number: 1}); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}
		rows, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		if got := findSpawned(t, rows, "chan-1"); got.OwnerUserID != "" {
			t.Errorf("OwnerUserID = %q, want empty", got.OwnerUserID)
		}
	})
}

// T8: a deleted spawned row is gone from ListSpawnedChannels, and deleting it again is
// not an error.
func TestDeleteSpawnedChannel(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		if err := s.UpsertSpawnedChannel(ctx, SpawnedChannel{ChannelID: "chan-1", HubID: hubID, Number: 1}); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}

		if err := s.DeleteSpawnedChannel(ctx, "chan-1"); err != nil {
			t.Fatalf("DeleteSpawnedChannel: %v", err)
		}
		rows, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("ListSpawnedChannels after delete = %v, want none", rows)
		}
		if err := s.DeleteSpawnedChannel(ctx, "chan-1"); err != nil {
			t.Errorf("second DeleteSpawnedChannel error = %v, want nil", err)
		}
	})
}

// T9: deleting a hub keeps its spawned row, so a spawned channel of a removed
// hub survives a restart tracked and dies when empty. Nothing here asserts
// what the row's hub reference becomes.
func TestDeleteHubKeepsSpawned(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		if err := s.UpsertSpawnedChannel(ctx, SpawnedChannel{ChannelID: "chan-1", HubID: hubID, Number: 1, OwnerUserID: "user-1"}); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}

		if err := s.DeleteHub(ctx, hubID); err != nil {
			t.Fatalf("DeleteHub: %v", err)
		}
		rows, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		findSpawned(t, rows, "chan-1")
	})
}
