package store

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Fake is the in-memory Store for other packages' tests. It lives in a
// non-test file so commands and panel tests can import it, the same way
// utils/testapi.go ships a test hook in the production package. The contract
// suite in store_test.go runs against Fake and Postgres alike, which is what
// makes a command test that passes against Fake mean something.
//
// Mutex-guarded because the suites run under -race and a test may drive the
// store from the goroutine a gateway handler runs on.
type Fake struct {
	mu      sync.Mutex
	nextID  int64
	hubs    map[int64]Hub
	spawned map[string]Spawned
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{
		nextID:  1,
		hubs:    make(map[int64]Hub),
		spawned: make(map[string]Spawned),
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

// UpsertSpawned implements Store.
func (f *Fake) UpsertSpawned(_ context.Context, s Spawned) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.spawned[s.ChannelID]; ok {
		s.CreatedAt = existing.CreatedAt
	} else {
		s.CreatedAt = time.Now()
	}
	f.spawned[s.ChannelID] = s
	return nil
}

// DeleteSpawned implements Store.
func (f *Fake) DeleteSpawned(_ context.Context, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.spawned, channelID)
	return nil
}

// ListSpawned implements Store.
func (f *Fake) ListSpawned(_ context.Context) ([]Spawned, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Spawned
	for _, sp := range f.spawned {
		out = append(out, sp)
	}
	return out, nil
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
