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
// Mutex-guarded because the suites run under -race and a test may drive the
// store from the goroutine a gateway handler runs on.
type Fake struct {
	mu      sync.Mutex
	nextID  int64
	hubs    map[int64]Hub
	spawned map[string]SpawnedChannel
	// guildRoles holds each guild's guild-wide moderator role IDs.
	guildRoles map[string][]string
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{
		nextID:     1,
		hubs:       make(map[int64]Hub),
		spawned:    make(map[string]SpawnedChannel),
		guildRoles: make(map[string][]string),
	}
}

// GetHub implements Store.
func (f *Fake) GetHub(_ context.Context, id int64) (Hub, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hubs[id]
	if !ok {
		return Hub{}, ErrNotFound
	}
	return cloneHub(h), nil
}

// ListHubs implements Store.
func (f *Fake) ListHubs(_ context.Context, guildID string) ([]Hub, error) {
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
func (f *Fake) UpsertHub(_ context.Context, hub Hub) (Hub, error) {
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
func (f *Fake) DeleteHub(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.hubs, id)
	for channelID, sp := range f.spawned {
		if sp.HubID == id {
			sp.HubID = 0
			f.spawned[channelID] = sp
		}
	}
	return nil
}

// UpsertSpawnedChannel implements Store.
func (f *Fake) UpsertSpawnedChannel(_ context.Context, sc SpawnedChannel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.spawned[sc.ChannelID]; ok {
		sc.CreatedAt = existing.CreatedAt
	} else {
		sc.CreatedAt = time.Now()
	}
	f.spawned[sc.ChannelID] = sc
	return nil
}

// DeleteSpawnedChannel implements Store.
func (f *Fake) DeleteSpawnedChannel(_ context.Context, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.spawned, channelID)
	return nil
}

// ListSpawnedChannels implements Store.
func (f *Fake) ListSpawnedChannels(_ context.Context) ([]SpawnedChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []SpawnedChannel
	for _, sc := range f.spawned {
		out = append(out, sc)
	}
	return out, nil
}

// GetGuildModeratorRoles implements Store.
func (f *Fake) GetGuildModeratorRoles(_ context.Context, guildID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	roles := slices.Clone(f.guildRoles[guildID])
	if roles == nil {
		roles = []string{}
	}
	return roles, nil
}

// SetGuildModeratorRoles implements Store.
func (f *Fake) SetGuildModeratorRoles(_ context.Context, guildID string, roleIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guildRoles[guildID] = slices.Clone(roleIDs)
	return nil
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
