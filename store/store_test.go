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
	"time"

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
		// DeleteDelayMinutes is off the default 0, so a write that drops it
		// reads back wrong.
		DeleteDelayMinutes: 45,
		Enabled:            true,
		RenamingAllowed:    true,
		LockingAllowed:     true,
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
	if got.DeleteDelayMinutes != want.DeleteDelayMinutes {
		t.Errorf("DeleteDelayMinutes = %d, want %d", got.DeleteDelayMinutes, want.DeleteDelayMinutes)
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

// T1: a saved hub reads back with the settings it was written with, the
// store fills in the ID and both times, and the save's one entry lists under
// the new hub's ID, which the caller could not know.
func TestSaveHubThenGet(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		want := sampleHub("guild-1", "hub-1")

		stored, err := s.SaveHub(ctx, want, changeEntry(1))
		if err != nil {
			t.Fatalf("SaveHub: %v", err)
		}
		if stored.ID == 0 {
			t.Error("SaveHub returned ID 0, want a stored ID")
		}
		if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
			t.Errorf("SaveHub returned CreatedAt %v, UpdatedAt %v, want both set", stored.CreatedAt, stored.UpdatedAt)
		}

		got, err := s.GetHub(ctx, stored.ID)
		if err != nil {
			t.Fatalf("GetHub: %v", err)
		}
		assertHubSettings(t, got, want)
		if got := listedOrdinals(t, s, stored.ID); !slices.Equal(got, []int{1}) {
			t.Errorf("the new hub's entries are ordinals %v, want [1]", got)
		}
	})
}

// T2: a second save of the hub, by its ID at the version the first
// returned, changes the settings of the existing row, creates no second row,
// and adds its one entry under the same hub.
func TestSaveHubUpdatesInPlace(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		first, err := s.SaveHub(ctx, sampleHub("guild-1", "hub-1"), changeEntry(1))
		if err != nil {
			t.Fatalf("first SaveHub: %v", err)
		}

		want := sampleHub("guild-1", "hub-1")
		want.ID, want.Version = first.ID, first.Version
		want.BaseString = "Briefing"
		want.PermissionSource = PermissionCategory
		want.ModeratorRoleIDs = nil
		want.UserLimit = 0
		want.Bitrate = 64000
		want.DeleteDelayMinutes = 0
		want.Enabled = false
		want.RenamingAllowed = false
		want.LockingAllowed = false

		second, err := s.SaveHub(ctx, want, changeEntry(2))
		if err != nil {
			t.Fatalf("second SaveHub: %v", err)
		}
		if second.ID != first.ID {
			t.Errorf("second SaveHub returned ID %d, want the first row's %d", second.ID, first.ID)
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
		if got := listedOrdinals(t, s, first.ID); !slices.Equal(got, []int{2, 1}) {
			t.Errorf("the hub's entries are ordinals %v, want [2 1]", got)
		}
	})
}

// T2b (#349): a hub saved without "Locking allowed" reads back with it off,
// so nothing locks on a hub until someone turns the setting on.
func TestHubSavedWithoutLockingAllowedReadsBackOff(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		stored, err := s.SaveHub(ctx, Hub{
			GuildID: "guild-1", HubChannelID: "hub-1", BaseString: "Arma Voice",
			PermissionSource: PermissionCategory, Bitrate: 64000, Enabled: true,
		}, changeEntry(1))
		if err != nil {
			t.Fatalf("SaveHub: %v", err)
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

// notJSON is a save's entry whose diff is not a JSON value, which the
// change log refuses.
func notJSON() ChangeLogEntry {
	e := changeEntry(2)
	e.Diff = json.RawMessage(`{"user_limit": {"before": 1,`)
	return e
}

// storeState is everything a save can write, read back through the store:
// the guild's hubs, its guild-wide moderator roles, and the change log under
// each of those hubs and under none.
type storeState struct {
	Hubs    []Hub
	Roles   GuildModeratorRoles
	Entries map[int64][]ChangeLogEntry
}

// readState reads the store's state for guild-1.
func readState(t *testing.T, s Store) storeState {
	t.Helper()
	ctx := context.Background()
	hubs, err := s.ListHubs(ctx, "guild-1")
	if err != nil {
		t.Fatalf("ListHubs: %v", err)
	}
	slices.SortFunc(hubs, func(a, b Hub) int { return int(a.ID - b.ID) })
	roles, err := s.GetGuildModeratorRoles(ctx, "guild-1")
	if err != nil {
		t.Fatalf("GetGuildModeratorRoles: %v", err)
	}
	roles.RoleIDs = sortedRoles(roles.RoleIDs)
	st := storeState{Hubs: hubs, Roles: roles, Entries: map[int64][]ChangeLogEntry{}}
	for _, id := range append([]int64{0}, hubIDs(hubs)...) {
		entries, err := s.ListChangeLog(ctx, id, 100)
		if err != nil {
			t.Fatalf("ListChangeLog(%d): %v", id, err)
		}
		st.Entries[id] = entries
	}
	return st
}

// hubIDs returns the IDs of a list of hubs.
func hubIDs(hubs []Hub) []int64 {
	ids := make([]int64, 0, len(hubs))
	for _, h := range hubs {
		ids = append(ids, h.ID)
	}
	return ids
}

// T2c (#362): a save whose entry's diff is not a JSON value fails, and the
// store holds neither its settings nor its entry. A save's settings and its
// entry land together or not at all.
func TestSaveWithADiffThatIsNotJSONWritesNothing(t *testing.T) {
	cases := []struct {
		name string
		// save makes the save against a store holding hub-1 under hubID.
		save func(ctx context.Context, s Store, hubID int64) error
	}{
		{"SaveHub of a new hub", func(ctx context.Context, s Store, _ int64) error {
			_, err := s.SaveHub(ctx, sampleHub("guild-1", "hub-2"), notJSON())
			return err
		}},
		{"SaveHub of an existing hub", func(ctx context.Context, s Store, hubID int64) error {
			h, err := s.GetHub(ctx, hubID)
			if err != nil {
				return fmt.Errorf("GetHub: %w", err)
			}
			h.BaseString, h.UserLimit = "Briefing", 3
			_, err = s.SaveHub(ctx, h, notJSON())
			return err
		}},
		{"RemoveHub", func(ctx context.Context, s Store, hubID int64) error {
			return s.RemoveHub(ctx, hubID, notJSON())
		}},
		{"SaveGuildModeratorRoles", func(ctx context.Context, s Store, _ int64) error {
			return saveGuildRoles(t, s, []string{"role-hq"}, notJSON())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				ctx := context.Background()
				hubID := storeHub(t, s, "hub-1")
				if err := saveGuildRoles(t, s, []string{"role-mp"}, moderatorsEntry(1)); err != nil {
					t.Fatalf("SaveGuildModeratorRoles: %v", err)
				}
				before := readState(t, s)

				if err := tc.save(ctx, s, hubID); err == nil {
					t.Error("the save returned no error, want the diff refused")
				}
				if after := readState(t, s); !reflect.DeepEqual(after, before) {
					t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
				}
			})
		})
	}
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
			if _, err := s.SaveHub(ctx, h, changeEntry(1)); err != nil {
				t.Fatalf("SaveHub(%s): %v", h.HubChannelID, err)
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

// T5: a removed hub is gone from GetHub and ListHubs. Removing it again is
// ErrNotFound and writes nothing, no entry included: the change log has
// one entry for every save that takes effect.
func TestRemoveHub(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")

		if err := s.RemoveHub(ctx, hubID, removeEntry(1)); err != nil {
			t.Fatalf("RemoveHub: %v", err)
		}
		if _, err := s.GetHub(ctx, hubID); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetHub after remove error = %v, want ErrNotFound", err)
		}
		hubs, err := s.ListHubs(ctx, "guild-1")
		if err != nil {
			t.Fatalf("ListHubs: %v", err)
		}
		if len(hubs) != 0 {
			t.Errorf("ListHubs after remove = %v, want none", hubChannelIDs(hubs))
		}
		before := readState(t, s)
		if err := s.RemoveHub(ctx, hubID, removeEntry(2)); !errors.Is(err, ErrNotFound) {
			t.Errorf("second RemoveHub error = %v, want ErrNotFound", err)
		}
		if after := readState(t, s); !reflect.DeepEqual(after, before) {
			t.Errorf("the store after the second remove = %+v, want it as before, %+v", after, before)
		}
	})
}

// removeEntry is a remove's entry with an ordinal in its diff.
func removeEntry(ordinal int) ChangeLogEntry {
	e := changeEntry(ordinal)
	e.Action = ChangeRemove
	return e
}

// storeHub saves a hub, with an entry of ordinal 0, and returns its stored
// ID, for the cases that need a hub to reference.
func storeHub(t *testing.T, s Store, hubChannelID string) int64 {
	t.Helper()
	stored, err := s.SaveHub(context.Background(), sampleHub("guild-1", hubChannelID), changeEntry(0))
	if err != nil {
		t.Fatalf("SaveHub(%s): %v", hubChannelID, err)
	}
	return stored.ID
}

// resaveHub saves the stored hub again, unchanged, by its ID at the version
// the store holds, with the entry.
func resaveHub(t *testing.T, s Store, hubID int64, entry ChangeLogEntry) {
	t.Helper()
	h, err := s.GetHub(context.Background(), hubID)
	if err != nil {
		t.Fatalf("GetHub(%d): %v", hubID, err)
	}
	if _, err := s.SaveHub(context.Background(), h, entry); err != nil {
		t.Fatalf("SaveHub(%d): %v", hubID, err)
	}
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

// migratedTo resets the test database and runs the migrations up to and
// including version: the schema an older release left. The case then writes
// rows the way that release did, and openMigrated runs the rest. Skips when
// no test database is set.
func migratedTo(t *testing.T, version int64) *sql.DB {
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
	t.Cleanup(func() { _ = raw.Close() })
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
	if _, err := provider.UpTo(ctx, version); err != nil {
		t.Fatalf("migrate up to %d: %v", version, err)
	}
	return raw
}

// insertOlderHub writes a hub row naming only the columns the first schema
// has, as an older release wrote it, and returns its ID.
func insertOlderHub(t *testing.T, raw *sql.DB) int64 {
	t.Helper()
	var hubID int64
	if err := raw.QueryRowContext(context.Background(), `
		INSERT INTO hubs (guild_id, hub_channel_id, base_string, permission_source, enabled)
		VALUES ('guild-1', 'hub-1', 'Arma Voice', 'category', TRUE) RETURNING id`).Scan(&hubID); err != nil {
		t.Fatalf("insert an older hub row: %v", err)
	}
	return hubID
}

// openMigrated opens the store over the test database, which runs every
// migration after the one migratedTo stopped at.
func openMigrated(t *testing.T) *Postgres {
	t.Helper()
	s, err := Open(context.Background(), os.Getenv(testDSNVar))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// T7d (#349): rows written before the channel lock migration come through
// it with "Locking allowed" off on every hub and every spawned channel
// unlocked, the state an upgrade meets.
func TestChannelLockMigrationLeavesExistingRowsOffAndUnlocked(t *testing.T) {
	// The change log migration is the last one before the lock's.
	raw := migratedTo(t, 20260918100000)
	hubID := insertOlderHub(t, raw)
	if _, err := raw.ExecContext(context.Background(), `
		INSERT INTO spawned_channels (channel_id, hub_id, number, owner_user_id)
		VALUES ('chan-1', $1, 1, 'user-1')`, hubID); err != nil {
		t.Fatalf("insert an older spawned row: %v", err)
	}

	s := openMigrated(t)

	hub, err := s.GetHub(context.Background(), hubID)
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
	// The channel lock migration is the last one before the rename setting's.
	raw := migratedTo(t, 20260924000000)
	hubID := insertOlderHub(t, raw)

	s := openMigrated(t)

	hub, err := s.GetHub(context.Background(), hubID)
	if err != nil {
		t.Fatalf("GetHub: %v", err)
	}
	if !hub.RenamingAllowed {
		t.Error("an older hub reads back with RenamingAllowed off, want on")
	}
}

// T7f (#372): a hub written before the delete delay existed comes through
// its migration with a delay of 0, so an upgrade leaves every hub deleting
// its channels the moment they empty, as before.
func TestDeleteDelayMigrationLeavesExistingHubsAtZero(t *testing.T) {
	// The rename setting's migration is the last one before the delay's.
	raw := migratedTo(t, 20260926000000)
	hubID := insertOlderHub(t, raw)

	s := openMigrated(t)

	hub, err := s.GetHub(context.Background(), hubID)
	if err != nil {
		t.Fatalf("GetHub: %v", err)
	}
	if hub.DeleteDelayMinutes != 0 {
		t.Errorf("an older hub reads back with DeleteDelayMinutes %d, want 0", hub.DeleteDelayMinutes)
	}
}

// T7g (#373): a hub and a guild-wide set written before the version existed
// come through its migration with a version, and a save at the version they
// read back goes through, so an upgrade leaves every hub and the guild-wide
// roles saveable from the panel.
func TestVersionMigrationLeavesExistingSettingsSaveable(t *testing.T) {
	// The delete delay's migration is the last one before the version's.
	raw := migratedTo(t, 20260927000000)
	hubID := insertOlderHub(t, raw)
	if _, err := raw.ExecContext(context.Background(), `
		INSERT INTO guild_settings (guild_id, moderator_role_ids) VALUES ('guild-1', '["role-mp"]')`); err != nil {
		t.Fatalf("insert an older guild settings row: %v", err)
	}

	s := openMigrated(t)

	ctx := context.Background()
	hub, err := s.GetHub(ctx, hubID)
	if err != nil {
		t.Fatalf("GetHub: %v", err)
	}
	hub.UserLimit = 3
	if _, err := s.SaveHub(ctx, hub, changeEntry(1)); err != nil {
		t.Errorf("SaveHub of an older hub at the version it read back: %v", err)
	}
	roles := guildRoles(t, s)
	if !slices.Equal(roles.RoleIDs, []string{"role-mp"}) {
		t.Errorf("the older guild-wide set reads back as %v, want [role-mp]", roles.RoleIDs)
	}
	roles.RoleIDs = []string{"role-hq"}
	if err := s.SaveGuildModeratorRoles(ctx, "guild-1", roles, moderatorsEntry(2)); err != nil {
		t.Errorf("SaveGuildModeratorRoles of the older set at the version it read back: %v", err)
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

// T9: removing a hub keeps its spawned row, so a spawned channel of a removed
// hub survives a restart tracked and dies when empty. Nothing here asserts
// what the row's hub reference becomes.
func TestRemoveHubKeepsSpawned(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		if err := s.UpsertSpawnedChannel(ctx, SpawnedChannel{ChannelID: "chan-1", HubID: hubID, Number: 1, OwnerUserID: "user-1"}); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}

		if err := s.RemoveHub(ctx, hubID, removeEntry(1)); err != nil {
			t.Fatalf("RemoveHub: %v", err)
		}
		rows, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		findSpawned(t, rows, "chan-1")
	})
}

// T10: the guild-wide moderator roles read back as the set they were saved
// as, a guild with no row reads back empty, a second save replaces the
// first, and each save's one entry is listed among the moderator changes.
// Nothing distinguishes nil from an empty slice, the same rule as a hub's
// own roles.
func TestGuildModeratorRolesRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		if got := guildRoles(t, s).RoleIDs; len(got) != 0 {
			t.Errorf("GetGuildModeratorRoles with no row = %v, want none", got)
		}

		want := []string{"role-mp", "role-s6"}
		if err := saveGuildRoles(t, s, want, moderatorsEntry(1)); err != nil {
			t.Fatalf("SaveGuildModeratorRoles: %v", err)
		}
		if got := guildRoles(t, s).RoleIDs; !slices.Equal(sortedRoles(got), sortedRoles(want)) {
			t.Errorf("GetGuildModeratorRoles = %v, want %v (as a set)", got, want)
		}

		if err := saveGuildRoles(t, s, []string{"role-hq"}, moderatorsEntry(2)); err != nil {
			t.Fatalf("second SaveGuildModeratorRoles: %v", err)
		}
		if got := guildRoles(t, s).RoleIDs; !slices.Equal(sortedRoles(got), []string{"role-hq"}) {
			t.Errorf("GetGuildModeratorRoles after second save = %v, want [role-hq]", got)
		}
		entries, err := s.ListModeratorChanges(ctx, 10)
		if err != nil {
			t.Fatalf("ListModeratorChanges: %v", err)
		}
		var ordinals []int
		for _, e := range entries {
			ordinals = append(ordinals, ordinalOf(t, e))
		}
		if !slices.Equal(ordinals, []int{2, 1}) {
			t.Errorf("moderator changes are ordinals %v, want [2 1]", ordinals)
		}
	})
}

// T16 (#373): every save that takes effect adds exactly one to its record's
// version, a save that changes nothing included. A guild with no settings
// row is at version 0.
func TestEverySaveAddsOneToItsRecordsVersion(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hub, err := s.SaveHub(ctx, sampleHub("guild-1", "hub-1"), changeEntry(1))
		if err != nil {
			t.Fatalf("SaveHub of a new hub: %v", err)
		}
		for i, name := range []string{"a changed limit", "no change"} {
			if name == "a changed limit" {
				hub.UserLimit = 3
			}
			saved, err := s.SaveHub(ctx, hub, changeEntry(i+2))
			if err != nil {
				t.Fatalf("SaveHub with %s: %v", name, err)
			}
			if saved.Version != hub.Version+1 {
				t.Errorf("SaveHub with %s returned version %d, want %d", name, saved.Version, hub.Version+1)
			}
			got, err := s.GetHub(ctx, hub.ID)
			if err != nil {
				t.Fatalf("GetHub: %v", err)
			}
			if got.Version != hub.Version+1 {
				t.Errorf("after SaveHub with %s the hub reads back at version %d, want %d", name, got.Version, hub.Version+1)
			}
			hub = saved
		}

		roles, err := s.GetGuildModeratorRoles(ctx, "guild-1")
		if err != nil {
			t.Fatalf("GetGuildModeratorRoles with no row: %v", err)
		}
		if roles.Version != 0 {
			t.Errorf("a guild with no row reads back at version %d, want 0", roles.Version)
		}
		for i, name := range []string{"a first set", "the same set again"} {
			roles.RoleIDs = []string{"role-mp"}
			if err := s.SaveGuildModeratorRoles(ctx, "guild-1", roles, moderatorsEntry(i+1)); err != nil {
				t.Fatalf("SaveGuildModeratorRoles of %s: %v", name, err)
			}
			got, err := s.GetGuildModeratorRoles(ctx, "guild-1")
			if err != nil {
				t.Fatalf("GetGuildModeratorRoles: %v", err)
			}
			if got.Version != roles.Version+1 {
				t.Errorf("after saving %s the guild reads back at version %d, want %d", name, got.Version, roles.Version+1)
			}
			roles = got
		}
	})
}

// T17 (#373): a save over a version the record is not at fails with
// ErrStale and writes neither its settings nor its entry. A guild with no
// settings row is at version 0, so a save at 0 over a stored row fails, and
// so does a save at 1 over no row.
func TestSaveOverAnotherVersionIsStaleAndWritesNothing(t *testing.T) {
	cases := []struct {
		name string
		// seedRoles stores a guild-wide set before the save.
		seedRoles bool
		save      func(ctx context.Context, s Store, hub Hub) error
	}{
		{"a hub one version behind", false, func(ctx context.Context, s Store, hub Hub) error {
			hub.Version--
			hub.UserLimit = 3
			_, err := s.SaveHub(ctx, hub, changeEntry(3))
			return err
		}},
		{"a hub one version ahead", false, func(ctx context.Context, s Store, hub Hub) error {
			hub.Version++
			hub.UserLimit = 3
			_, err := s.SaveHub(ctx, hub, changeEntry(3))
			return err
		}},
		{"a guild-wide set at 0 over a stored row", true, func(ctx context.Context, s Store, _ Hub) error {
			return s.SaveGuildModeratorRoles(ctx, "guild-1", GuildModeratorRoles{RoleIDs: []string{"role-hq"}, Version: 0}, moderatorsEntry(3))
		}},
		{"a guild-wide set at 1 over no row", false, func(ctx context.Context, s Store, _ Hub) error {
			return s.SaveGuildModeratorRoles(ctx, "guild-1", GuildModeratorRoles{RoleIDs: []string{"role-hq"}, Version: 1}, moderatorsEntry(3))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forEachStore(t, func(t *testing.T, s Store) {
				ctx := context.Background()
				hub, err := s.SaveHub(ctx, sampleHub("guild-1", "hub-1"), changeEntry(1))
				if err != nil {
					t.Fatalf("SaveHub of a new hub: %v", err)
				}
				if hub, err = s.SaveHub(ctx, hub, changeEntry(2)); err != nil {
					t.Fatalf("second SaveHub: %v", err)
				}
				if tc.seedRoles {
					if err := saveGuildRoles(t, s, []string{"role-mp"}, moderatorsEntry(2)); err != nil {
						t.Fatalf("SaveGuildModeratorRoles: %v", err)
					}
				}
				before := readState(t, s)

				if err := tc.save(ctx, s, hub); !errors.Is(err, ErrStale) {
					t.Errorf("the save returned %v, want ErrStale", err)
				}
				if after := readState(t, s); !reflect.DeepEqual(after, before) {
					t.Errorf("the store after the stale save = %+v, want it as before, %+v", after, before)
				}
			})
		})
	}
}

// T18 (#373): a save of a new hub on a channel a hub already stands on
// fails with ErrStale and writes nothing: it never writes over that hub.
func TestSaveOfANewHubOnATakenChannelIsStaleAndWritesNothing(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		storeHub(t, s, "hub-1")
		before := readState(t, s)
		again := sampleHub("guild-1", "hub-1")
		again.BaseString = "Briefing"

		if _, err := s.SaveHub(context.Background(), again, changeEntry(1)); !errors.Is(err, ErrStale) {
			t.Errorf("SaveHub of a new hub on hub-1 returned %v, want ErrStale", err)
		}
		if after := readState(t, s); !reflect.DeepEqual(after, before) {
			t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
		}
	})
}

// T19 (#373): a save by the ID of a hub that has been removed fails with
// ErrNotFound and writes nothing: an update never inserts.
func TestSaveOfARemovedHubIsNotFoundAndInsertsNothing(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")
		hub, err := s.GetHub(ctx, hubID)
		if err != nil {
			t.Fatalf("GetHub: %v", err)
		}
		if err := s.RemoveHub(ctx, hubID, removeEntry(1)); err != nil {
			t.Fatalf("RemoveHub: %v", err)
		}
		before := readState(t, s)
		hub.UserLimit = 3

		if _, err := s.SaveHub(ctx, hub, changeEntry(2)); !errors.Is(err, ErrNotFound) {
			t.Errorf("SaveHub of the removed hub returned %v, want ErrNotFound", err)
		}
		if after := readState(t, s); !reflect.DeepEqual(after, before) {
			t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
		}
	})
}

// guildRoles reads guild-1's guild-wide moderator roles and their version.
func guildRoles(t *testing.T, s Store) GuildModeratorRoles {
	t.Helper()
	roles, err := s.GetGuildModeratorRoles(context.Background(), "guild-1")
	if err != nil {
		t.Fatalf("GetGuildModeratorRoles: %v", err)
	}
	return roles
}

// saveGuildRoles saves guild-1's guild-wide set over the version the store
// holds now, as a save from a section loaded just before it.
func saveGuildRoles(t *testing.T, s Store, roleIDs []string, entry ChangeLogEntry) error {
	t.Helper()
	current := guildRoles(t, s)
	return s.SaveGuildModeratorRoles(context.Background(), "guild-1", GuildModeratorRoles{RoleIDs: roleIDs, Version: current.Version}, entry)
}

// changeEntry is an update's change log entry, its diff naming an ordinal
// so a test can tell the entries apart. The store sets the hub reference.
func changeEntry(ordinal int) ChangeLogEntry {
	return ChangeLogEntry{
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

// listedOrdinals lists a hub's entries, newest first, and returns their
// ordinals. A hubID of zero lists the entries under no hub.
func listedOrdinals(t *testing.T, s Store, hubID int64) []int {
	t.Helper()
	entries, err := s.ListChangeLog(context.Background(), hubID, 100)
	if err != nil {
		t.Fatalf("ListChangeLog(%d): %v", hubID, err)
	}
	ordinals := []int{}
	for _, e := range entries {
		ordinals = append(ordinals, ordinalOf(t, e))
	}
	return ordinals
}

// T11: ListChangeLog returns at most limit entries of the hub, newest first
// in append order, and none of another hub's.
func TestChangeLogListReturnsTheLastNNewestFirst(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubA := storeHub(t, s, "hub-a")
		hubB := storeHub(t, s, "hub-b")
		for i := 1; i <= 12; i++ {
			resaveHub(t, s, hubA, changeEntry(i))
		}
		resaveHub(t, s, hubB, changeEntry(99))

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
// was saved with, and a time the store set.
func TestChangeLogEntryRoundTrips(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		want := ChangeLogEntry{
			ForumUserID:   4321,
			ForumUsername: "Roe.R",
			Action:        ChangeRegister,
			Diff:          json.RawMessage(`{"base_string": {"before": null, "after": "Arma Voice"}, "moderator_roles": {"before": null, "after": ["role-mp", "role-hq"]}}`),
		}
		stored, err := s.SaveHub(ctx, sampleHub("guild-1", "hub-1"), want)
		if err != nil {
			t.Fatalf("SaveHub: %v", err)
		}

		entries, err := s.ListChangeLog(ctx, stored.ID, 10)
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

// T13: a remove's one entry references no hub, and removing a hub keeps its
// earlier entries and clears their hub reference, so the whole log of a
// removed hub lists under no hub.
func TestRemoveHubListsItsEntryAndTheHubsEarlierOnesUnderNoHub(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		hubID := storeHub(t, s, "hub-1")

		if err := s.RemoveHub(ctx, hubID, removeEntry(2)); err != nil {
			t.Fatalf("RemoveHub: %v", err)
		}
		if got := listedOrdinals(t, s, hubID); len(got) != 0 {
			t.Errorf("entries under the removed hub are ordinals %v, want none", got)
		}
		if got := listedOrdinals(t, s, 0); !slices.Equal(got, []int{2, 0}) {
			t.Errorf("entries under no hub are ordinals %v, want [2 0]: the remove's, then the hub's earlier one", got)
		}
	})
}

// moderatorsEntry is a guild-wide moderator save's entry with an ordinal in
// its diff.
func moderatorsEntry(ordinal int) ChangeLogEntry {
	e := changeEntry(ordinal)
	e.Action = ChangeModerators
	return e
}

// T14 (#296): ListModeratorChanges returns at most limit guild-wide
// moderator saves, newest first in append order, and none of the other
// entries under no hub, such as a remove's, nor a moderators entry that
// references a hub.
func TestModeratorChangesListReturnsTheLastNNewestFirst(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		for i := 1; i <= 12; i++ {
			if err := saveGuildRoles(t, s, []string{fmt.Sprintf("role-%d", i)}, moderatorsEntry(i)); err != nil {
				t.Fatalf("SaveGuildModeratorRoles(%d): %v", i, err)
			}
		}
		removed := storeHub(t, s, "hub-2")
		if err := s.RemoveHub(ctx, removed, removeEntry(98)); err != nil {
			t.Fatalf("RemoveHub: %v", err)
		}
		if _, err := s.SaveHub(ctx, sampleHub("guild-1", "hub-1"), moderatorsEntry(99)); err != nil {
			t.Fatalf("SaveHub with a moderators entry: %v", err)
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
			if n := ordinalOf(t, e); n == 0 || n == 98 || n == 99 {
				t.Errorf("entry ordinal %d is listed, want neither the removed hub's entries nor the hub-referenced one", n)
			}
		}
	})
}

// The panel tells an abandoned page load from a failed read by errors.Is on
// the context's error, so a read the context ends while it runs must keep
// that error in its chain through the store's own wrapping. A
// characterization pin on Postgres alone: a Fake read has no query in flight
// to end. The read waits on a table lock another connection holds, and the
// test sees it
// waiting before the context ends, so the in-flight path is the one pinned
// and not the check database/sql makes before a query starts.
func TestPostgresReadEndedWhileWaitingWrapsTheContextError(t *testing.T) {
	cases := []struct {
		name string
		want error
		// deadline is the read's context deadline, zero for a context the
		// test cancels once the read is waiting.
		deadline time.Duration
	}{
		{"cancelled", context.Canceled, 0},
		{"deadline", context.DeadlineExceeded, 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestPostgres(t)
			raw, err := sql.Open("pgx", os.Getenv(testDSNVar))
			if err != nil {
				t.Fatalf("open raw connection: %v", err)
			}
			defer func() { _ = raw.Close() }()
			holder, err := raw.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = holder.Rollback() }()
			if _, err := holder.Exec("LOCK TABLE guild_settings IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatalf("lock: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			if tc.deadline > 0 {
				ctx, cancel = context.WithTimeout(context.Background(), tc.deadline)
			}
			defer cancel()
			read := make(chan error, 1)
			go func() {
				_, err := s.GetGuildModeratorRoles(ctx, "guild-1")
				read <- err
			}()
			awaitLockWaiter(t, raw, read)
			if tc.deadline == 0 {
				cancel()
			}

			select {
			case err = <-read:
			case <-time.After(10 * time.Second):
				t.Fatal("the read did not return after its context ended")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("read returned %v, want an error wrapping %v", err, tc.want)
			}
		})
	}
}

// awaitLockWaiter polls until a backend other than the poller waits on a
// lock, and fails the test if the read returns first. The database is the
// test's alone, so the one waiter is the read.
func awaitLockWaiter(t *testing.T, raw *sql.DB, read <-chan error) {
	t.Helper()
	const q = `SELECT count(*) FROM pg_stat_activity
		WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid()`
	giveUp := time.After(5 * time.Second)
	for {
		var waiting int
		if err := raw.QueryRow(q).Scan(&waiting); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		select {
		case err := <-read:
			t.Fatalf("the read returned %v before it waited on the lock", err)
		case <-giveUp:
			t.Fatal("the read never waited on the lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// T15 (#356, #362): a call made with a context that is already done fails
// with that context's error and changes nothing, whichever method it is. A
// panel save that outlives its request depends on the fake failing here the
// way Postgres does, or its tests could not see a save stopped halfway.
func TestCallWithADoneContextFailsAndChangesNothing(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		live := context.Background()
		hubID := storeHub(t, s, "hub-1")
		seeded := SpawnedChannel{ChannelID: "chan-1", HubID: hubID, Number: 1, OwnerUserID: "user-a"}
		if err := s.UpsertSpawnedChannel(live, seeded); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}
		if err := saveGuildRoles(t, s, []string{"role-mp"}, moderatorsEntry(1)); err != nil {
			t.Fatalf("SaveGuildModeratorRoles: %v", err)
		}
		seededRoles := guildRoles(t, s)

		done, cancel := context.WithCancel(live)
		cancel()
		calls := map[string]func() error{
			"GetHub":   func() error { _, err := s.GetHub(done, hubID); return err },
			"ListHubs": func() error { _, err := s.ListHubs(done, "guild-1"); return err },
			"SaveHub": func() error {
				_, err := s.SaveHub(done, sampleHub("guild-1", "hub-2"), changeEntry(2))
				return err
			},
			"RemoveHub": func() error { return s.RemoveHub(done, hubID, removeEntry(3)) },
			"UpsertSpawnedChannel": func() error {
				return s.UpsertSpawnedChannel(done, SpawnedChannel{ChannelID: "chan-2", HubID: hubID, Number: 2})
			},
			"SetSpawnedChannelLock": func() error {
				return s.SetSpawnedChannelLock(done, "chan-1", ChannelLock{Locked: true, LockerUserID: "user-a"})
			},
			"DeleteSpawnedChannel":   func() error { return s.DeleteSpawnedChannel(done, "chan-1") },
			"ListSpawnedChannels":    func() error { _, err := s.ListSpawnedChannels(done); return err },
			"GetGuildModeratorRoles": func() error { _, err := s.GetGuildModeratorRoles(done, "guild-1"); return err },
			"SaveGuildModeratorRoles": func() error {
				return s.SaveGuildModeratorRoles(done, "guild-1", GuildModeratorRoles{RoleIDs: []string{"role-hq"}, Version: seededRoles.Version}, moderatorsEntry(4))
			},
			"ListChangeLog":        func() error { _, err := s.ListChangeLog(done, hubID, 10); return err },
			"ListModeratorChanges": func() error { _, err := s.ListModeratorChanges(done, 10); return err },
		}
		for name, call := range calls {
			if err := call(); !errors.Is(err, context.Canceled) {
				t.Errorf("%s with a done context = %v, want context.Canceled", name, err)
			}
		}

		hubs, err := s.ListHubs(live, "guild-1")
		if err != nil {
			t.Fatalf("ListHubs: %v", err)
		}
		if got := hubChannelIDs(hubs); !slices.Equal(got, []string{"hub-1"}) {
			t.Errorf("hubs after the done calls = %v, want hub-1 alone", got)
		}
		rows, err := s.ListSpawnedChannels(live)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		if len(rows) != 1 || rows[0].ChannelID != seeded.ChannelID || rows[0].OwnerUserID != seeded.OwnerUserID || rows[0].Lock != (ChannelLock{}) {
			t.Errorf("spawned rows after the done calls = %+v, want chan-1 alone, owned by user-a, unlocked", rows)
		}
		if roles := guildRoles(t, s); !slices.Equal(roles.RoleIDs, []string{"role-mp"}) || roles.Version != seededRoles.Version {
			t.Errorf("guild moderator roles after the done calls = %+v, want [role-mp] as seeded, at %d", roles, seededRoles.Version)
		}
		if got := listedOrdinals(t, s, hubID); !slices.Equal(got, []int{0}) {
			t.Errorf("hub-1's entries after the done calls are ordinals %v, want [0], the seeded one alone", got)
		}
		if got := listedOrdinals(t, s, 0); !slices.Equal(got, []int{1}) {
			t.Errorf("entries under no hub after the done calls are ordinals %v, want [1], the seeded one alone", got)
		}
	})
}
