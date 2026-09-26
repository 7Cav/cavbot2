package store

import (
	"context"
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
// Mutex-guarded because the suites run under -race and a test may drive the
// store from the goroutine a gateway handler runs on.
type Fake struct {
	mu      sync.Mutex
	nextID  int64
	hubs    map[int64]Hub
	spawned map[string]SpawnedChannel
	// guildRoles holds each guild's guild-wide moderator role IDs.
	guildRoles map[string][]string
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
		guildRoles:   make(map[string][]string),
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

// UpsertHub implements Store.
func (f *Fake) UpsertHub(ctx context.Context, hub Hub) (Hub, error) {
	if err := ctx.Err(); err != nil {
		return Hub{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	stored := cloneHub(hub)
	stored.UpdatedAt = now
	if existing, ok := f.hubByChannelLocked(hub.HubChannelID); ok {
		stored.ID = existing.ID
		stored.CreatedAt = existing.CreatedAt
	} else {
		stored.ID = f.nextID
		f.nextID++
		stored.CreatedAt = now
	}
	f.hubs[stored.ID] = stored
	return cloneHub(stored), nil
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

// DeleteHub implements Store.
func (f *Fake) DeleteHub(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
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
	return nil
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
func (f *Fake) GetGuildModeratorRoles(ctx context.Context, guildID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	roles := slices.Clone(f.guildRoles[guildID])
	if roles == nil {
		roles = []string{}
	}
	return roles, nil
}

// SetGuildModeratorRoles implements Store.
func (f *Fake) SetGuildModeratorRoles(ctx context.Context, guildID string, roleIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guildRoles[guildID] = slices.Clone(roleIDs)
	return nil
}

// AppendChangeLog implements Store.
func (f *Fake) AppendChangeLog(ctx context.Context, e ChangeLogEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e.ID = f.nextChangeID
	f.nextChangeID++
	e.At = time.Now()
	e.Diff = slices.Clone(e.Diff)
	f.changes = append(f.changes, e)
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
