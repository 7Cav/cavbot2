package panel

import (
	"context"
	"time"

	"github.com/7cav/cavbot2/store"
)

// forSave is the service as a save runs it: the same deps, with every store
// call under its own deadline of storeTimeout, and the wait for the guild's
// data under guildWait. A page load keeps the plain store and the request's
// lifetime, which its time budget bounds.
func (s *hubService) forSave(guildWait time.Duration) *hubService {
	saving := *s
	saving.deps.Store = boundedStore{store: s.deps.Store, timeout: s.storeTimeout}
	saving.guildWait = guildWait
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

func (b boundedStore) SaveHub(ctx context.Context, hub store.Hub, entry store.ChangeLogEntry) (store.Hub, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.SaveHub(ctx, hub, entry)
}

func (b boundedStore) RemoveHub(ctx context.Context, id int64, entry store.ChangeLogEntry) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.RemoveHub(ctx, id, entry)
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

func (b boundedStore) GetGuildModeratorRoles(ctx context.Context, guildID string) (store.GuildModeratorRoles, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.GetGuildModeratorRoles(ctx, guildID)
}

func (b boundedStore) SaveGuildModeratorRoles(ctx context.Context, guildID string, roles store.GuildModeratorRoles, entry store.ChangeLogEntry) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.SaveGuildModeratorRoles(ctx, guildID, roles, entry)
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

func (b boundedStore) ListFoxholeRecords(ctx context.Context, guildID string) ([]store.FoxholeRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.ListFoxholeRecords(ctx, guildID)
}

func (b boundedStore) SaveFoxholeNote(ctx context.Context, guildID string, save store.NoteSave, entry store.ChangeLogEntry) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.SaveFoxholeNote(ctx, guildID, save, entry)
}

func (b boundedStore) SetFoxholeRecordNames(ctx context.Context, guildID string, names []store.MemberNames) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.SetFoxholeRecordNames(ctx, guildID, names)
}

func (b boundedStore) ListFoxholeChanges(ctx context.Context, limit int) ([]store.ChangeLogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.store.ListFoxholeChanges(ctx, limit)
}
