package store

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Fake is the in-memory Store for tests in other packages. It holds the same
// contract as Postgres, and the store package's own contract tests run against
// both, so a test that passes on the fake passes on the database.
type Fake struct {
	mu      sync.Mutex
	nextID  int64
	hubs    map[int64]Hub
	spawned map[string]SpawnedChannel
}

// NewFake returns an empty in-memory store.
func NewFake() *Fake {
	return &Fake{hubs: make(map[int64]Hub), spawned: make(map[string]SpawnedChannel)}
}

func (f *Fake) GetHub(_ context.Context, id int64) (Hub, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hub, ok := f.hubs[id]
	if !ok {
		return Hub{}, ErrNotFound
	}
	return cloneHub(hub), nil
}

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

func (f *Fake) UpsertHub(_ context.Context, hub Hub) (Hub, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for id, existing := range f.hubs {
		if existing.HubChannelID == hub.HubChannelID {
			hub.ID = id
			hub.CreatedAt = existing.CreatedAt
			hub.UpdatedAt = now
			f.hubs[id] = cloneHub(hub)
			return cloneHub(hub), nil
		}
	}
	f.nextID++
	hub.ID = f.nextID
	hub.CreatedAt = now
	hub.UpdatedAt = now
	f.hubs[hub.ID] = cloneHub(hub)
	return cloneHub(hub), nil
}

func (f *Fake) DeleteHub(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.hubs, id)
	// Spawned rows outlive their hub with the reference cleared.
	for chID, sc := range f.spawned {
		if sc.HubID != nil && *sc.HubID == id {
			sc.HubID = nil
			f.spawned[chID] = sc
		}
	}
	return nil
}

func (f *Fake) UpsertSpawnedChannel(_ context.Context, sc SpawnedChannel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.spawned[sc.ChannelID]; ok {
		sc.CreatedAt = existing.CreatedAt
	} else {
		sc.CreatedAt = time.Now()
	}
	f.spawned[sc.ChannelID] = cloneSpawned(sc)
	return nil
}

func (f *Fake) DeleteSpawnedChannel(_ context.Context, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.spawned, channelID)
	return nil
}

func (f *Fake) ListSpawnedChannels(_ context.Context) ([]SpawnedChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]SpawnedChannel, 0, len(f.spawned))
	for _, sc := range f.spawned {
		rows = append(rows, cloneSpawned(sc))
	}
	return rows, nil
}

// cloneSpawned copies the pointer field so a caller's later edit never reaches
// the stored row.
func cloneSpawned(sc SpawnedChannel) SpawnedChannel {
	if sc.HubID != nil {
		id := *sc.HubID
		sc.HubID = &id
	}
	return sc
}

// cloneHub copies the slice field so a caller's later edit never reaches the
// stored row, the way a database round trip would not.
func cloneHub(h Hub) Hub {
	h.ModeratorRoleIDs = slices.Clone(h.ModeratorRoleIDs)
	return h
}
