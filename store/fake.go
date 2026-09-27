package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"
)

// Fake is the in-memory Store for other packages' tests. It lives in a
// non-test file because Go test helpers cannot be imported across packages,
// so commands and panel tests can reach it; production code never constructs
// one, and the name is the review signal. The contract suite in store_test.go
// runs against Fake and Postgres alike, so a case that passes against Fake
// passes against Postgres with the same calls.
//
// A call whose context is already done fails with the context's error and
// changes nothing, as a Postgres call does, so a test can see a caller that
// hands the store a context its request has cancelled.
//
// UpsertHub, DeleteHub, SetGuildModeratorRoles and AppendChangeLog are setup
// methods off the Store interface: the settings writes of a save with no
// entry and no version check, and an entry alone. Panel and commands tests
// seed hubs, roles and entries with them, so a seed writes no entry the test
// did not ask for. A seed still adds one to the version, as another save
// would, so a test can land one between two steps of a save.
//
// Mutex-guarded because the suites run under -race and a test may drive the
// store from the goroutine a gateway handler runs on.
type Fake struct {
	mu      sync.Mutex
	nextID  int64
	hubs    map[int64]Hub
	spawned map[string]SpawnedChannel
	// guildRoles holds each guild's guild-wide moderator roles and their
	// version.
	guildRoles map[string]GuildModeratorRoles
	// changes is the change log in append order; nextChangeID is the next
	// entry's ID.
	changes      []ChangeLogEntry
	nextChangeID int64
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{
		nextID:       1,
		hubs:         make(map[int64]Hub),
		spawned:      make(map[string]SpawnedChannel),
		guildRoles:   make(map[string]GuildModeratorRoles),
		nextChangeID: 1,
	}
}

// GetHub implements Store.
func (f *Fake) GetHub(ctx context.Context, id int64) (Hub, error) {
	if err := ctx.Err(); err != nil {
		return Hub{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hubs[id]
	if !ok {
		return Hub{}, ErrNotFound
	}
	return cloneHub(h), nil
}

// ListHubs implements Store.
func (f *Fake) ListHubs(ctx context.Context, guildID string) ([]Hub, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var hubs []Hub
	for _, h := range f.hubs {
		if h.GuildID == guildID {
			hubs = append(hubs, cloneHub(h))
		}
	}
	return hubs, nil
}

// SaveHub implements Store.
func (f *Fake) SaveHub(ctx context.Context, hub Hub, entry ChangeLogEntry) (Hub, error) {
	var stored Hub
	err := f.write(ctx, &entry, func() (int64, error) {
		var err error
		if hub.ID == 0 {
			stored, err = f.insertHubLocked(hub)
		} else {
			stored, err = f.updateHubLocked(hub)
		}
		return stored.ID, err
	})
	if err != nil {
		return Hub{}, err
	}
	return cloneHub(stored), nil
}

// UpsertHub is a setup method off the Store interface: it writes no entry
// and checks no version. The hub is inserted, or updates the row on the same
// hub channel in place, and the row's version goes up by one either way, as
// a save's would.
func (f *Fake) UpsertHub(ctx context.Context, hub Hub) (Hub, error) {
	var stored Hub
	err := f.write(ctx, nil, func() (int64, error) {
		stored = f.upsertHubLocked(hub)
		return stored.ID, nil
	})
	if err != nil {
		return Hub{}, err
	}
	return cloneHub(stored), nil
}

// upsertHubLocked inserts the hub, or updates the row on the same hub
// channel in place and adds one to its version. Caller holds mu.
func (f *Fake) upsertHubLocked(hub Hub) Hub {
	if existing, ok := f.hubByChannelLocked(hub.HubChannelID); ok {
		hub.ID, hub.Version = existing.ID, existing.Version
		stored, _ := f.updateHubLocked(hub)
		return stored
	}
	stored, _ := f.insertHubLocked(hub)
	return stored
}

// insertHubLocked stores a new hub row at version 1. ErrStale when a row
// already stands on the hub channel. Caller holds mu.
func (f *Fake) insertHubLocked(hub Hub) (Hub, error) {
	if _, ok := f.hubByChannelLocked(hub.HubChannelID); ok {
		return Hub{}, ErrStale
	}
	now := time.Now()
	stored := cloneHub(hub)
	stored.ID = f.nextID
	f.nextID++
	stored.Version = 1
	stored.CreatedAt, stored.UpdatedAt = now, now
	f.hubs[stored.ID] = stored
	return stored, nil
}

// updateHubLocked writes the settings of the row with the hub's ID while it
// is at the hub's version, and adds one to it. The row keeps its guild,
// channel and created_at. ErrNotFound when no row has the ID, ErrStale when
// the row is at another version. Caller holds mu.
func (f *Fake) updateHubLocked(hub Hub) (Hub, error) {
	existing, ok := f.hubs[hub.ID]
	if !ok {
		return Hub{}, ErrNotFound
	}
	if existing.Version != hub.Version {
		return Hub{}, ErrStale
	}
	stored := cloneHub(hub)
	stored.GuildID, stored.HubChannelID = existing.GuildID, existing.HubChannelID
	stored.Version = existing.Version + 1
	stored.CreatedAt, stored.UpdatedAt = existing.CreatedAt, time.Now()
	f.hubs[stored.ID] = stored
	return stored, nil
}

// hubByChannelLocked finds the hub row for a hub channel. Caller holds mu.
func (f *Fake) hubByChannelLocked(hubChannelID string) (Hub, bool) {
	for _, h := range f.hubs {
		if h.HubChannelID == hubChannelID {
			return h, true
		}
	}
	return Hub{}, false
}

// RemoveHub implements Store.
func (f *Fake) RemoveHub(ctx context.Context, id int64, entry ChangeLogEntry) error {
	return f.removeHub(ctx, id, &entry)
}

// DeleteHub is RemoveHub with no entry, a setup method off the Store
// interface.
func (f *Fake) DeleteHub(ctx context.Context, id int64) error {
	return f.removeHub(ctx, id, nil)
}

// removeHub removes the hub row and clears the hub reference of its spawned
// rows and its entries, as the foreign keys' ON DELETE SET NULL does. The
// entry, if any, goes under no hub.
func (f *Fake) removeHub(ctx context.Context, id int64, entry *ChangeLogEntry) error {
	return f.write(ctx, entry, func() (int64, error) {
		delete(f.hubs, id)
		for channelID, sp := range f.spawned {
			if sp.HubID == id {
				sp.HubID = 0
				f.spawned[channelID] = sp
			}
		}
		for i := range f.changes {
			if f.changes[i].HubID == id {
				f.changes[i].HubID = 0
			}
		}
		return 0, nil
	})
}

// UpsertSpawnedChannel implements Store. The caller's lock is ignored: an
// existing row keeps its own and a new row starts unlocked.
func (f *Fake) UpsertSpawnedChannel(ctx context.Context, sc SpawnedChannel) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.spawned[sc.ChannelID]; ok {
		sc.CreatedAt = existing.CreatedAt
		sc.Lock = existing.Lock
	} else {
		sc.CreatedAt = time.Now()
		sc.Lock = ChannelLock{}
	}
	f.spawned[sc.ChannelID] = sc
	return nil
}

// SetSpawnedChannelLock implements Store.
func (f *Fake) SetSpawnedChannelLock(ctx context.Context, channelID string, lock ChannelLock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.spawned[channelID]
	if !ok {
		return ErrNotFound
	}
	sc.Lock = lock
	f.spawned[channelID] = sc
	return nil
}

// DeleteSpawnedChannel implements Store.
func (f *Fake) DeleteSpawnedChannel(ctx context.Context, channelID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.spawned, channelID)
	return nil
}

// ListSpawnedChannels implements Store.
func (f *Fake) ListSpawnedChannels(ctx context.Context) ([]SpawnedChannel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []SpawnedChannel
	for _, sc := range f.spawned {
		out = append(out, sc)
	}
	return out, nil
}

// GetGuildModeratorRoles implements Store.
func (f *Fake) GetGuildModeratorRoles(ctx context.Context, guildID string) (GuildModeratorRoles, error) {
	if err := ctx.Err(); err != nil {
		return GuildModeratorRoles{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	roles := f.guildRoles[guildID]
	roles.RoleIDs = slices.Clone(roles.RoleIDs)
	if roles.RoleIDs == nil {
		roles.RoleIDs = []string{}
	}
	return roles, nil
}

// SaveGuildModeratorRoles implements Store. A guild with no set stored is
// at version 0.
func (f *Fake) SaveGuildModeratorRoles(ctx context.Context, guildID string, roles GuildModeratorRoles, entry ChangeLogEntry) error {
	return f.write(ctx, &entry, func() (int64, error) {
		if f.guildRoles[guildID].Version != roles.Version {
			return 0, ErrStale
		}
		f.setGuildRolesLocked(guildID, roles.RoleIDs)
		return 0, nil
	})
}

// SetGuildModeratorRoles is a setup method off the Store interface: it
// writes no entry and checks no version. It replaces the guild's set and
// adds one to its version, as a save would.
func (f *Fake) SetGuildModeratorRoles(ctx context.Context, guildID string, roleIDs []string) error {
	return f.write(ctx, nil, func() (int64, error) {
		f.setGuildRolesLocked(guildID, roleIDs)
		return 0, nil
	})
}

// setGuildRolesLocked replaces the guild's set and adds one to its version.
// Caller holds mu.
func (f *Fake) setGuildRolesLocked(guildID string, roleIDs []string) {
	f.guildRoles[guildID] = GuildModeratorRoles{RoleIDs: slices.Clone(roleIDs), Version: f.guildRoles[guildID].Version + 1}
}

// AppendChangeLog adds one entry under the caller's HubID with nothing else
// written, a setup method off the Store interface. The caller's ID and At
// are ignored.
func (f *Fake) AppendChangeLog(ctx context.Context, e ChangeLogEntry) error {
	return f.write(ctx, &e, func() (int64, error) { return e.HubID, nil })
}

// write makes every write of hub settings, guild-wide moderator roles or a
// change log entry. It writes nothing when ctx is done or when the entry's
// diff is not a JSON value, which Postgres's JSONB column refuses too.
// Otherwise it runs apply under mu and appends the entry, if there is one,
// under the hub apply returns, zero for none. An apply that fails must
// have changed nothing, and then no entry is appended: the settings and the
// entry land together or not at all, as a Postgres transaction's do.
func (f *Fake) write(ctx context.Context, entry *ChangeLogEntry, apply func() (hubID int64, err error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry != nil && !json.Valid(entry.Diff) {
		return errors.New("store: the change log diff is not a JSON value")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	hubID, err := apply()
	if err != nil {
		return err
	}
	if entry != nil {
		e := *entry
		e.ID = f.nextChangeID
		f.nextChangeID++
		e.HubID = hubID
		e.At = time.Now()
		e.Diff = slices.Clone(e.Diff)
		f.changes = append(f.changes, e)
	}
	return nil
}

// ListChangeLog implements Store.
func (f *Fake) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]ChangeLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.listChanges(limit, func(e ChangeLogEntry) bool { return e.HubID == hubID }), nil
}

// ListModeratorChanges implements Store.
func (f *Fake) ListModeratorChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.listChanges(limit, func(e ChangeLogEntry) bool { return e.HubID == 0 && e.Action == ChangeModerators }), nil
}

// listChanges returns at most limit entries that pass keep, newest first.
func (f *Fake) listChanges(limit int, keep func(ChangeLogEntry) bool) []ChangeLogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ChangeLogEntry
	for i := len(f.changes) - 1; i >= 0 && len(out) < limit; i-- {
		e := f.changes[i]
		if !keep(e) {
			continue
		}
		e.Diff = slices.Clone(e.Diff)
		out = append(out, e)
	}
	return out
}

// cloneHub copies a hub so a caller's later edits to the slice do not reach
// the stored row, the way a database round trip would not.
func cloneHub(h Hub) Hub {
	h.ModeratorRoleIDs = slices.Clone(h.ModeratorRoleIDs)
	if h.ModeratorRoleIDs == nil {
		h.ModeratorRoleIDs = []string{}
	}
	return h
}
