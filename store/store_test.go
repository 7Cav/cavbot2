package store_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
)

// testDSNEnv names the variable the Postgres suite reads. Unset, the Postgres
// half of every contract test skips and only the fake runs, so `go test ./...`
// passes on a machine with no database. CI sets it.
const testDSNEnv = "TEST_BOT_DB_DSN"

// implementation is one Store under contract test. open returns a store with
// empty tables, or skips the test.
type implementation struct {
	name string
	open func(t *testing.T) store.Store
}

// implementations lists every Store the contract suite runs against: the fake
// always, Postgres when TEST_BOT_DB_DSN is set.
func implementations() []implementation {
	return []implementation{
		{name: "fake", open: func(_ *testing.T) store.Store { return store.NewFake() }},
		{name: "postgres", open: openPostgres},
	}
}

func openPostgres(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv(testDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", testDSNEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE spawned_channels, hubs RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return store.NewPostgres(db)
}

// forEach runs fn once per implementation as a subtest.
func forEach(t *testing.T, fn func(t *testing.T, s store.Store)) {
	t.Helper()
	for _, impl := range implementations() {
		t.Run(impl.name, func(t *testing.T) {
			fn(t, impl.open(t))
		})
	}
}

// hubFixture is the literal every hub round trip starts from. Its values are
// the caller-supplied fields only; the store fills ID and the timestamps.
func hubFixture() store.Hub {
	return store.Hub{
		GuildID:          "guild-1",
		HubChannelID:     "hub-chan-1",
		BaseString:       "Arma Voice",
		PermissionSource: store.PermissionSourceHubChannel,
		ModeratorRoleIDs: []string{"role-mp", "role-hq"},
		UserLimit:        12,
		Bitrate:          96000,
		Enabled:          false,
	}
}

// assertHubFields compares the caller-supplied fields of got against want.
// Timestamps are the store's and are never compared.
func assertHubFields(t *testing.T, got, want store.Hub) {
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
	if !slices.Equal(got.ModeratorRoleIDs, want.ModeratorRoleIDs) {
		t.Errorf("ModeratorRoleIDs = %v, want %v", got.ModeratorRoleIDs, want.ModeratorRoleIDs)
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

func TestUpsertHubThenGetHubRoundTrips(t *testing.T) {
	forEach(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		saved, err := s.UpsertHub(ctx, hubFixture())
		if err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
		if saved.ID == 0 {
			t.Fatalf("UpsertHub returned ID 0")
		}
		got, err := s.GetHub(ctx, saved.ID)
		if err != nil {
			t.Fatalf("GetHub: %v", err)
		}
		if got.ID != saved.ID {
			t.Errorf("GetHub ID = %d, want %d", got.ID, saved.ID)
		}
		assertHubFields(t, got, hubFixture())
	})
}

// hubChannelIDs is the set a hub list is compared by; order is not a contract.
func hubChannelIDs(hubs []store.Hub) []string {
	ids := make([]string, 0, len(hubs))
	for _, h := range hubs {
		ids = append(ids, h.HubChannelID)
	}
	slices.Sort(ids)
	return ids
}

func TestListHubsReturnsEveryHubOfTheGuild(t *testing.T) {
	forEach(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		first := hubFixture()
		second := hubFixture()
		second.HubChannelID = "hub-chan-2"
		for _, h := range []store.Hub{first, second} {
			if _, err := s.UpsertHub(ctx, h); err != nil {
				t.Fatalf("UpsertHub: %v", err)
			}
		}
		got, err := s.ListHubs(ctx, first.GuildID)
		if err != nil {
			t.Fatalf("ListHubs: %v", err)
		}
		want := []string{"hub-chan-1", "hub-chan-2"}
		if ids := hubChannelIDs(got); !slices.Equal(ids, want) {
			t.Errorf("ListHubs hub channel IDs = %v, want %v", ids, want)
		}
	})
}

func TestUpsertHubUpdatesTheRowThatHoldsTheHubChannel(t *testing.T) {
	forEach(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		first, err := s.UpsertHub(ctx, hubFixture())
		if err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
		changed := hubFixture()
		changed.BaseString = "Arma Ops"
		second, err := s.UpsertHub(ctx, changed)
		if err != nil {
			t.Fatalf("second UpsertHub: %v", err)
		}
		if second.ID != first.ID {
			t.Fatalf("second UpsertHub ID = %d, want the first row's %d", second.ID, first.ID)
		}
		got, err := s.GetHub(ctx, first.ID)
		if err != nil {
			t.Fatalf("GetHub: %v", err)
		}
		if got.BaseString != "Arma Ops" {
			t.Errorf("BaseString after update = %q, want %q", got.BaseString, "Arma Ops")
		}
	})
}

func TestGetHubUnknownIDIsNotFound(t *testing.T) {
	forEach(t, func(t *testing.T, s store.Store) {
		_, err := s.GetHub(context.Background(), 4242)
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetHub(unknown) error = %v, want ErrNotFound", err)
		}
	})
}

func TestDeleteHubRemovesTheRow(t *testing.T) {
	forEach(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		saved, err := s.UpsertHub(ctx, hubFixture())
		if err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
		if err := s.DeleteHub(ctx, saved.ID); err != nil {
			t.Fatalf("DeleteHub: %v", err)
		}
		if _, err := s.GetHub(ctx, saved.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetHub after delete error = %v, want ErrNotFound", err)
		}
		hubs, err := s.ListHubs(ctx, saved.GuildID)
		if err != nil {
			t.Fatalf("ListHubs: %v", err)
		}
		if ids := hubChannelIDs(hubs); len(ids) != 0 {
			t.Errorf("ListHubs after delete = %v, want none", ids)
		}
	})
}

// spawnedByChannel indexes a spawned channel list by channel ID.
func spawnedByChannel(rows []store.SpawnedChannel) map[string]store.SpawnedChannel {
	out := make(map[string]store.SpawnedChannel, len(rows))
	for _, r := range rows {
		out[r.ChannelID] = r
	}
	return out
}

func TestUpsertSpawnedChannelThenListReturnsNumberAndOwner(t *testing.T) {
	forEach(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		hub, err := s.UpsertHub(ctx, hubFixture())
		if err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
		row := store.SpawnedChannel{
			ChannelID:   "spawned-1",
			HubID:       &hub.ID,
			Number:      3,
			OwnerUserID: "user-creator",
		}
		if err := s.UpsertSpawnedChannel(ctx, row); err != nil {
			t.Fatalf("UpsertSpawnedChannel: %v", err)
		}
		listed, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		got, ok := spawnedByChannel(listed)["spawned-1"]
		if !ok {
			t.Fatalf("ListSpawnedChannels lacks spawned-1: %v", listed)
		}
		if got.Number != 3 {
			t.Errorf("Number = %d, want 3", got.Number)
		}
		if got.OwnerUserID != "user-creator" {
			t.Errorf("OwnerUserID = %q, want %q", got.OwnerUserID, "user-creator")
		}

		// A handover is an upsert of the same channel with the new owner.
		row.OwnerUserID = "user-successor"
		if err := s.UpsertSpawnedChannel(ctx, row); err != nil {
			t.Fatalf("second UpsertSpawnedChannel: %v", err)
		}
		listed, err = s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		if len(listed) != 1 {
			t.Fatalf("ListSpawnedChannels after handover has %d rows, want 1", len(listed))
		}
		if got := listed[0].OwnerUserID; got != "user-successor" {
			t.Errorf("OwnerUserID after handover = %q, want %q", got, "user-successor")
		}
	})
}

func TestDeleteSpawnedChannelRemovesTheRow(t *testing.T) {
	forEach(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		for _, id := range []string{"spawned-1", "spawned-2"} {
			if err := s.UpsertSpawnedChannel(ctx, store.SpawnedChannel{ChannelID: id, Number: 1}); err != nil {
				t.Fatalf("UpsertSpawnedChannel(%s): %v", id, err)
			}
		}
		if err := s.DeleteSpawnedChannel(ctx, "spawned-1"); err != nil {
			t.Fatalf("DeleteSpawnedChannel: %v", err)
		}
		listed, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		byID := spawnedByChannel(listed)
		if _, still := byID["spawned-1"]; still {
			t.Errorf("spawned-1 still listed after delete")
		}
		if _, kept := byID["spawned-2"]; !kept {
			t.Errorf("spawned-2 missing after deleting spawned-1")
		}
	})
}

func TestSpawnedRowsSurviveTheirHubDelete(t *testing.T) {
	forEach(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		hub, err := s.UpsertHub(ctx, hubFixture())
		if err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
		for _, id := range []string{"spawned-1", "spawned-2"} {
			if err := s.UpsertSpawnedChannel(ctx, store.SpawnedChannel{ChannelID: id, HubID: &hub.ID, Number: 1}); err != nil {
				t.Fatalf("UpsertSpawnedChannel(%s): %v", id, err)
			}
		}
		if err := s.DeleteHub(ctx, hub.ID); err != nil {
			t.Fatalf("DeleteHub: %v", err)
		}
		listed, err := s.ListSpawnedChannels(ctx)
		if err != nil {
			t.Fatalf("ListSpawnedChannels: %v", err)
		}
		byID := spawnedByChannel(listed)
		for _, id := range []string{"spawned-1", "spawned-2"} {
			if _, ok := byID[id]; !ok {
				t.Errorf("%s missing after its hub was deleted", id)
			}
		}
	})
}
