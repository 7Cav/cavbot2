package panel

import (
	"context"
	"time"

	"github.com/7cav/cavbot2/store"
)

// forSave is the service as a save runs it: the same deps, with every store
// call under its own deadline of storeTimeout. A page load keeps the plain
// store and the request's lifetime.
func (s *hubService) forSave() *hubService {
	saving := *s
	saving.deps.Store = boundedStore{store: s.deps.Store, timeout: s.storeTimeout}
	return &saving
}

// boundedStore gives each call its own deadline, timeout from the call's
// start, so a store that stops answering fails the call instead of holding
// the save. Every method is written out, so no call a save makes can pass
// through unbounded.
type boundedStore struct {
	store   store.Store
	timeout time.Duration
}

func (b boundedStore) GetHub(ctx context.Context, id int64) (store.Hub, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.GetHub(ctx, id)
}

func (b boundedStore) ListHubs(ctx context.Context, guildID string) ([]store.Hub, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.ListHubs(ctx, guildID)
}

func (b boundedStore) UpsertHub(ctx context.Context, hub store.Hub) (store.Hub, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.UpsertHub(ctx, hub)
}

func (b boundedStore) DeleteHub(ctx context.Context, id int64) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.DeleteHub(ctx, id)
}

func (b boundedStore) UpsertSpawnedChannel(ctx context.Context, sc store.SpawnedChannel) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.UpsertSpawnedChannel(ctx, sc)
}

func (b boundedStore) SetSpawnedChannelLock(ctx context.Context, channelID string, lock store.ChannelLock) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.SetSpawnedChannelLock(ctx, channelID, lock)
}

func (b boundedStore) DeleteSpawnedChannel(ctx context.Context, channelID string) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.DeleteSpawnedChannel(ctx, channelID)
}

func (b boundedStore) ListSpawnedChannels(ctx context.Context) ([]store.SpawnedChannel, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.ListSpawnedChannels(ctx)
}

func (b boundedStore) GetGuildModeratorRoles(ctx context.Context, guildID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.GetGuildModeratorRoles(ctx, guildID)
}

func (b boundedStore) SetGuildModeratorRoles(ctx context.Context, guildID string, roleIDs []string) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.SetGuildModeratorRoles(ctx, guildID, roleIDs)
}

func (b boundedStore) AppendChangeLog(ctx context.Context, entry store.ChangeLogEntry) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.AppendChangeLog(ctx, entry)
}

func (b boundedStore) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]store.ChangeLogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.ListChangeLog(ctx, hubID, limit)
}

func (b boundedStore) ListModeratorChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.ListModeratorChanges(ctx, limit)
}
