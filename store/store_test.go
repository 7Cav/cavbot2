package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/pressly/goose/v3"
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
		RenamingAllowed:  true,
		LockingAllowed:   true,
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
	if got.RenamingAllowed != want.RenamingAllowed {
		t.Errorf("RenamingAllowed = %v, want %v", got.RenamingAllowed, want.RenamingAllowed)
	}
	if got.LockingAllowed != want.LockingAllowed {
		t.Errorf("LockingAllowed = %v, want %v", got.LockingAllowed, want.LockingAllowed)
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
		want.RenamingAllowed = false
		want.LockingAllowed = false

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

// T2b (#349): a hub saved without "Locking allowed" reads back with it off,
// so nothing locks on a hub until someone turns the setting on.
func TestHubSavedWithoutLockingAllowedReadsBackOff(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		stored, err := s.UpsertHub(ctx, Hub{
			GuildID: "guild-1", HubChannelID: "hub-1", BaseString: "Arma Voice",
			PermissionSource: PermissionCategory, Bitrate: 64000, Enabled: true,
		})
		if err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
		got, err := s.GetHub(ctx, stored.ID)
		if err != nil {
			t.Fatalf("GetHub: %v", err)
		}
		if got.LockingAllowed {
			t.Error("LockingAllowed = true on a hub saved without it, want false")
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
		if got.Lock != (ChannelLock{}) {
			t.Errorf("a new row's lock = %+v, want unlocked", got.Lock)
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

// listedSpawned lists the spawned rows and returns the one for a channel.
func listedSpawned(t *testing.T, s Store, channelID string) SpawnedChannel {
	t.Helper()
	rows, err := s.ListSpawnedChannels(context.Background())
	if err != nil {
		t.Fatalf("ListSpawnedChannels: %v", err)
	}
	return findSpawned(t, rows, channelID)
}

// T7b (#349): a spawned row's lock reads back as it was set, a later write
// of the row (a handover) keeps it, and setting the zero lock unlocks it with
// no locker or notice left behind.
func TestSpawnedChannelLockRoundTripsAndOutlivesAHandover(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		row := SpawnedChannel{ChannelID: "chan-1", HubID: hubID, Number: 2, OwnerUserID: "user-owner"}
		if err := s.UpsertSpawnedChannel(ctx, row); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}
		lock := ChannelLock{Locked: true, LockerUserID: "user-locker", NoticeMessageID: "msg-1"}

		if err := s.SetSpawnedChannelLock(ctx, "chan-1", lock); err != nil {
			t.Fatalf("SetSpawnedChannelLock: %v", err)
		}
		if got := listedSpawned(t, s, "chan-1"); got.Lock != lock || got.OwnerUserID != "user-owner" || got.Number != 2 {
			t.Errorf("row after the lock = %+v, want lock %+v with owner user-owner and number 2 kept", got, lock)
		}

		row.OwnerUserID = "user-next"
		if err := s.UpsertSpawnedChannel(ctx, row); err != nil {
			t.Fatalf("UpsertSpawnedChannel at the handover: %v", err)
		}
		if got := listedSpawned(t, s, "chan-1"); got.Lock != lock || got.OwnerUserID != "user-next" {
			t.Errorf("row after the handover = %+v, want owner user-next and lock %+v kept", got, lock)
		}

		if err := s.SetSpawnedChannelLock(ctx, "chan-1", ChannelLock{}); err != nil {
			t.Fatalf("SetSpawnedChannelLock to unlock: %v", err)
		}
		if got := listedSpawned(t, s, "chan-1"); got.Lock != (ChannelLock{}) || got.OwnerUserID != "user-next" {
			t.Errorf("row after the unlock = %+v, want unlocked with owner user-next kept", got)
		}
	})
}

// T7c (#349): setting the lock of a channel with no row is ErrNotFound and
// makes no row.
func TestSetSpawnedChannelLockWithNoRowIsNotFound(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		err := s.SetSpawnedChannelLock(ctx, "chan-none", ChannelLock{Locked: true, LockerUserID: "user-1"})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("SetSpawnedChannelLock with no row error = %v, want ErrNotFound", err)
		}
		rows, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("ListSpawnedChannels = %v, want none", rows)
		}
	})
}

// T7d (#349): rows written before the channel lock migration come through
// it with "Locking allowed" off on every hub and every spawned channel
// unlocked, the state an upgrade meets.
func TestChannelLockMigrationLeavesExistingRowsOffAndUnlocked(t *testing.T) {
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
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, raw, files)
	if err != nil {
		t.Fatalf("migration provider: %v", err)
	}
	// The change log migration is the last one before the lock's.
	if _, err := provider.UpTo(ctx, 20260918100000); err != nil {
		t.Fatalf("migrate to the schema before the lock: %v", err)
	}
	var hubID int64
	if err := raw.QueryRowContext(ctx, `
		INSERT INTO hubs (guild_id, hub_channel_id, base_string, permission_source, enabled)
		VALUES ('guild-1', 'hub-1', 'Arma Voice', 'category', TRUE) RETURNING id`).Scan(&hubID); err != nil {
		t.Fatalf("insert an older hub row: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO spawned_channels (channel_id, hub_id, number, owner_user_id)
		VALUES ('chan-1', $1, 1, 'user-1')`, hubID); err != nil {
		t.Fatalf("insert an older spawned row: %v", err)
	}

	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	hub, err := s.GetHub(ctx, hubID)
	if err != nil {
		t.Fatalf("GetHub: %v", err)
	}
	if hub.LockingAllowed {
		t.Error("an older hub reads back with LockingAllowed on, want off")
	}
	if got := listedSpawned(t, s, "chan-1"); got.Lock != (ChannelLock{}) || got.OwnerUserID != "user-1" {
		t.Errorf("an older spawned row reads back as %+v, want unlocked with owner user-1", got)
	}
}

// T7e (#360): a hub written before the rename setting existed comes through
// its migration with "Renaming allowed" on, so an upgrade leaves every hub
// renaming as it did.
func TestRenamingAllowedMigrationLeavesExistingHubsOn(t *testing.T) {
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
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, raw, files)
	if err != nil {
		t.Fatalf("migration provider: %v", err)
	}
	// The channel lock migration is the last one before the rename setting's.
	if _, err := provider.UpTo(ctx, 20260924000000); err != nil {
		t.Fatalf("migrate to the schema before the rename setting: %v", err)
	}
	var hubID int64
	if err := raw.QueryRowContext(ctx, `
		INSERT INTO hubs (guild_id, hub_channel_id, base_string, permission_source, enabled)
		VALUES ('guild-1', 'hub-1', 'Arma Voice', 'category', TRUE) RETURNING id`).Scan(&hubID); err != nil {
		t.Fatalf("insert an older hub row: %v", err)
	}

	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	hub, err := s.GetHub(ctx, hubID)
	if err != nil {
		t.Fatalf("GetHub: %v", err)
	}
	if !hub.RenamingAllowed {
		t.Error("an older hub reads back with RenamingAllowed off, want on")
	}
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

// T10: the guild-wide moderator roles read back as the set they were written
// as, a guild with no row reads back empty, and a second write replaces the
// first. Nothing distinguishes nil from an empty slice, the same rule as a
// hub's own roles.
func TestGuildModeratorRolesRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		got, err := s.GetGuildModeratorRoles(ctx, "guild-1")
		if err != nil {
			t.Fatalf("GetGuildModeratorRoles with no row: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("GetGuildModeratorRoles with no row = %v, want none", got)
		}

		want := []string{"role-mp", "role-s6"}
		if err := s.SetGuildModeratorRoles(ctx, "guild-1", want); err != nil {
			t.Fatalf("SetGuildModeratorRoles: %v", err)
		}
		got, err = s.GetGuildModeratorRoles(ctx, "guild-1")
		if err != nil {
			t.Fatalf("GetGuildModeratorRoles: %v", err)
		}
		if !slices.Equal(sortedRoles(got), sortedRoles(want)) {
			t.Errorf("GetGuildModeratorRoles = %v, want %v (as a set)", got, want)
		}

		if err := s.SetGuildModeratorRoles(ctx, "guild-1", []string{"role-hq"}); err != nil {
			t.Fatalf("second SetGuildModeratorRoles: %v", err)
		}
		got, err = s.GetGuildModeratorRoles(ctx, "guild-1")
		if err != nil {
			t.Fatalf("GetGuildModeratorRoles after second set: %v", err)
		}
		if !slices.Equal(sortedRoles(got), []string{"role-hq"}) {
			t.Errorf("GetGuildModeratorRoles after second set = %v, want [role-hq]", got)
		}
	})
}

// changeEntry is a change log fixture for one hub, its diff naming the
// ordinal it was appended at so a test can tell the entries apart.
func changeEntry(hubID int64, ordinal int) ChangeLogEntry {
	return ChangeLogEntry{
		HubID:         hubID,
		ForumUserID:   1234,
		ForumUsername: "Doe.J",
		Action:        ChangeUpdate,
		Diff:          json.RawMessage(fmt.Sprintf(`{"user_limit":{"before":%d,"after":%d}}`, ordinal-1, ordinal)),
	}
}

// ordinalOf reads the ordinal back out of a changeEntry diff.
func ordinalOf(t *testing.T, e ChangeLogEntry) int {
	t.Helper()
	var diff map[string]struct{ After int }
	if err := json.Unmarshal(e.Diff, &diff); err != nil {
		t.Fatalf("decode diff %s: %v", e.Diff, err)
	}
	return diff["user_limit"].After
}

// T11: ListChangeLog returns at most limit entries of the hub, newest first
// in append order, and none of another hub's.
func TestChangeLogListReturnsTheLastNNewestFirst(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubA, hubB := storeHub(t, s, "hub-a"), storeHub(t, s, "hub-b")
		for i := 1; i <= 12; i++ {
			if err := s.AppendChangeLog(ctx, changeEntry(hubA, i)); err != nil {
				t.Fatalf("AppendChangeLog(%d): %v", i, err)
			}
		}
		if err := s.AppendChangeLog(ctx, changeEntry(hubB, 99)); err != nil {
			t.Fatalf("AppendChangeLog(hub-b): %v", err)
		}

		entries, err := s.ListChangeLog(ctx, hubA, 10)
		if err != nil {
			t.Fatalf("ListChangeLog: %v", err)
		}
		if len(entries) != 10 {
			t.Fatalf("ListChangeLog returned %d entries, want 10", len(entries))
		}
		if got := ordinalOf(t, entries[0]); got != 12 {
			t.Errorf("first entry is ordinal %d, want 12 (the newest)", got)
		}
		if got := ordinalOf(t, entries[9]); got != 3 {
			t.Errorf("tenth entry is ordinal %d, want 3", got)
		}
		for _, e := range entries {
			if e.HubID != hubA {
				t.Errorf("entry %+v is not hub-a's", e)
			}
		}
	})
}

// decodeDiff decodes a diff the way a reader would, so two diffs compare as
// objects and never as bytes: JSONB reorders keys and drops whitespace.
func decodeDiff(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var diff map[string]any
	if err := json.Unmarshal(raw, &diff); err != nil {
		t.Fatalf("decode diff %s: %v", raw, err)
	}
	return diff
}

// T12: an entry reads back with the forum user, the action and the diff it
// was appended with, and a time the store set.
func TestChangeLogEntryRoundTrips(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		want := ChangeLogEntry{
			HubID:         hubID,
			ForumUserID:   4321,
			ForumUsername: "Roe.R",
			Action:        ChangeRegister,
			Diff:          json.RawMessage(`{"base_string": {"before": null, "after": "Arma Voice"}, "moderator_roles": {"before": null, "after": ["role-mp", "role-hq"]}}`),
		}
		if err := s.AppendChangeLog(ctx, want); err != nil {
			t.Fatalf("AppendChangeLog: %v", err)
		}

		entries, err := s.ListChangeLog(ctx, hubID, 10)
		if err != nil {
			t.Fatalf("ListChangeLog: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("ListChangeLog returned %d entries, want 1", len(entries))
		}
		got := entries[0]
		if got.ForumUserID != 4321 || got.ForumUsername != "Roe.R" || got.Action != ChangeRegister {
			t.Errorf("entry = %+v, want forum user 4321 Roe.R and action register", got)
		}
		if got.At.IsZero() {
			t.Error("At is zero, want a time set by the store")
		}
		if diff, wantDiff := decodeDiff(t, got.Diff), decodeDiff(t, want.Diff); !reflect.DeepEqual(diff, wantDiff) {
			t.Errorf("diff = %v, want %v", diff, wantDiff)
		}
	})
}

// T13: deleting a hub keeps its entries and clears their hub reference, so
// they list under no hub, the same as an entry appended with none.
func TestDeleteHubClearsTheChangeLogReference(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		if err := s.AppendChangeLog(ctx, changeEntry(hubID, 1)); err != nil {
			t.Fatalf("AppendChangeLog: %v", err)
		}
		if err := s.AppendChangeLog(ctx, changeEntry(0, 2)); err != nil {
			t.Fatalf("AppendChangeLog with no hub: %v", err)
		}

		if err := s.DeleteHub(ctx, hubID); err != nil {
			t.Fatalf("DeleteHub: %v", err)
		}
		byHub, err := s.ListChangeLog(ctx, hubID, 10)
		if err != nil {
			t.Fatalf("ListChangeLog(hub): %v", err)
		}
		if len(byHub) != 0 {
			t.Errorf("ListChangeLog(hub) after delete = %v, want none", byHub)
		}
		noHub, err := s.ListChangeLog(ctx, 0, 10)
		if err != nil {
			t.Fatalf("ListChangeLog(0): %v", err)
		}
		if len(noHub) != 2 {
			t.Fatalf("ListChangeLog(0) returned %d entries, want 2", len(noHub))
		}
		if got := ordinalOf(t, noHub[0]); got != 2 {
			t.Errorf("first entry under no hub is ordinal %d, want 2", got)
		}
		if got := ordinalOf(t, noHub[1]); got != 1 {
			t.Errorf("second entry under no hub is ordinal %d, want 1 (the removed hub's)", got)
		}
	})
}

// moderatorsEntry is a guild-wide moderator save with an ordinal in its
// diff, under no hub the way the panel appends it.
func moderatorsEntry(hubID int64, ordinal int) ChangeLogEntry {
	e := changeEntry(hubID, ordinal)
	e.Action = ChangeModerators
	return e
}

// T14 (#296): ListModeratorChanges returns at most limit guild-wide
// moderator saves, newest first in append order, and neither a remove entry
// under no hub nor a moderators entry that references a hub.
func TestModeratorChangesListReturnsTheLastNNewestFirst(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		for i := 1; i <= 12; i++ {
			if err := s.AppendChangeLog(ctx, moderatorsEntry(0, i)); err != nil {
				t.Fatalf("AppendChangeLog(%d): %v", i, err)
			}
		}
		remove := changeEntry(0, 98)
		remove.Action = ChangeRemove
		if err := s.AppendChangeLog(ctx, remove); err != nil {
			t.Fatalf("AppendChangeLog(remove): %v", err)
		}
		if err := s.AppendChangeLog(ctx, moderatorsEntry(hubID, 99)); err != nil {
			t.Fatalf("AppendChangeLog(hub-referenced): %v", err)
		}

		entries, err := s.ListModeratorChanges(ctx, 10)
		if err != nil {
			t.Fatalf("ListModeratorChanges: %v", err)
		}
		if len(entries) != 10 {
			t.Fatalf("ListModeratorChanges returned %d entries, want 10", len(entries))
		}
		if got := ordinalOf(t, entries[0]); got != 12 {
			t.Errorf("first entry is ordinal %d, want 12 (the newest)", got)
		}
		if got := ordinalOf(t, entries[9]); got != 3 {
			t.Errorf("tenth entry is ordinal %d, want 3", got)
		}
		for _, e := range entries {
			if n := ordinalOf(t, e); n == 98 || n == 99 {
				t.Errorf("entry ordinal %d is listed, want neither the remove nor the hub-referenced one", n)
			}
		}
	})
}
