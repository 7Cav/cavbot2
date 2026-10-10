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
	// recordingRoles holds each guild's recording roles and their version.
	recordingRoles map[string]RecordingRoles
	// changes is the hub page's change log, and foxholeChanges the Foxhole
	// page's, kept apart as the two tables keep them.
	changes        changeLog
	foxholeChanges changeLog
	// members holds each guild's Foxhole records by member ID.
	members map[string]map[string]FoxholeRecord
	// reports marks the entries of the Foxhole change log that are Foxhole
	// actions' reports, by ID, each true while its action runs.
	reports map[int64]bool
}

// changeLog is one change log in append order, and the ID its next entry
// gets.
type changeLog struct {
	entries []ChangeLogEntry
	nextID  int64
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{
		nextID:         1,
		hubs:           make(map[int64]Hub),
		spawned:        make(map[string]SpawnedChannel),
		guildRoles:     make(map[string]GuildModeratorRoles),
		recordingRoles: make(map[string]RecordingRoles),
		changes:        changeLog{nextID: 1},
		foxholeChanges: changeLog{nextID: 1},
		members:        make(map[string]map[string]FoxholeRecord),
		reports:        make(map[int64]bool),
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
	err := f.writeAllOrNothing(ctx, &entry, func() (int64, error) {
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
	err := f.writeAllOrNothing(ctx, nil, func() (int64, error) {
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
// entry, if any, goes under no hub. ErrNotFound when no row has the ID.
func (f *Fake) removeHub(ctx context.Context, id int64, entry *ChangeLogEntry) error {
	return f.writeAllOrNothing(ctx, entry, func() (int64, error) {
		if _, ok := f.hubs[id]; !ok {
			return 0, ErrNotFound
		}
		delete(f.hubs, id)
		for channelID, sp := range f.spawned {
			if sp.HubID == id {
				sp.HubID = 0
				f.spawned[channelID] = sp
			}
		}
		for i := range f.changes.entries {
			if f.changes.entries[i].HubID == id {
				f.changes.entries[i].HubID = 0
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
	return f.writeAllOrNothing(ctx, &entry, func() (int64, error) {
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
	return f.writeAllOrNothing(ctx, nil, func() (int64, error) {
		f.setGuildRolesLocked(guildID, roleIDs)
		return 0, nil
	})
}

// setGuildRolesLocked replaces the guild's set and adds one to its version.
// Caller holds mu.
func (f *Fake) setGuildRolesLocked(guildID string, roleIDs []string) {
	f.guildRoles[guildID] = GuildModeratorRoles{RoleIDs: slices.Clone(roleIDs), Version: f.guildRoles[guildID].Version + 1}
}

// GetRecordingRoles implements Store.
func (f *Fake) GetRecordingRoles(ctx context.Context, guildID string) (RecordingRoles, error) {
	if err := ctx.Err(); err != nil {
		return RecordingRoles{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	roles := f.recordingRoles[guildID]
	roles.RoleIDs = slices.Clone(roles.RoleIDs)
	if roles.RoleIDs == nil {
		roles.RoleIDs = []string{}
	}
	return roles, nil
}

// SaveRecordingRoles implements Store. A guild with no set stored is at
// version 0.
func (f *Fake) SaveRecordingRoles(ctx context.Context, guildID string, roles RecordingRoles, entry ChangeLogEntry) error {
	return f.writeAllOrNothing(ctx, &entry, func() (int64, error) {
		current := f.recordingRoles[guildID]
		if current.Version != roles.Version {
			return 0, ErrStale
		}
		f.recordingRoles[guildID] = RecordingRoles{RoleIDs: slices.Clone(roles.RoleIDs), Version: current.Version + 1}
		return 0, nil
	})
}

// AppendChangeLog adds one entry under the caller's HubID with nothing else
// written, a setup method off the Store interface. The caller's ID and At
// are ignored.
func (f *Fake) AppendChangeLog(ctx context.Context, e ChangeLogEntry) error {
	return f.writeAllOrNothing(ctx, &e, func() (int64, error) { return e.HubID, nil })
}

// writeAllOrNothing makes every write of hub settings, guild-wide moderator
// roles, recording roles or a change log entry. It writes nothing when ctx is done or when
// the entry's diff is not a JSON value, which Postgres's JSONB column
// refuses too. Otherwise it runs apply under mu and appends the entry, if
// there is one, under the hub apply returns, zero for none. An apply that
// fails must have changed nothing, and then no entry is appended: the
// settings and the entry land together or not at all, as a Postgres
// transaction's do.
func (f *Fake) writeAllOrNothing(ctx context.Context, entry *ChangeLogEntry, apply func() (hubID int64, err error)) error {
	return f.writeToLog(ctx, &f.changes, entry, apply)
}

// writeToLog is writeAllOrNothing with the entry appended to log.
func (f *Fake) writeToLog(ctx context.Context, log *changeLog, entry *ChangeLogEntry, apply func() (hubID int64, err error)) error {
	_, err := f.appendToLog(ctx, log, entry, apply)
	return err
}

// appendToLog is writeToLog, and returns the entry as appended, with its ID
// and time, or the zero entry when there is none. apply runs before the
// append under mu, so log.nextID is then the ID the entry gets.
func (f *Fake) appendToLog(ctx context.Context, log *changeLog, entry *ChangeLogEntry, apply func() (hubID int64, err error)) (ChangeLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return ChangeLogEntry{}, err
	}
	if entry != nil && !json.Valid(entry.Diff) {
		return ChangeLogEntry{}, errors.New("store: the change log diff is not a JSON value")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	hubID, err := apply()
	if err != nil {
		return ChangeLogEntry{}, err
	}
	if entry == nil {
		return ChangeLogEntry{}, nil
	}
	e := *entry
	e.ID = log.nextID
	log.nextID++
	e.HubID = hubID
	e.At = time.Now()
	e.Diff = slices.Clone(e.Diff)
	log.entries = append(log.entries, e)
	e.Diff = slices.Clone(e.Diff)
	return e, nil
}

// ListChangeLog implements Store.
func (f *Fake) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]ChangeLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.listChanges(&f.changes, limit, func(e ChangeLogEntry) bool { return e.HubID == hubID }), nil
}

// ListModeratorChanges implements Store.
func (f *Fake) ListModeratorChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.listChanges(&f.changes, limit, func(e ChangeLogEntry) bool { return e.HubID == 0 && e.Action == ChangeModerators }), nil
}

// ListRecordingRoleChanges implements Store.
func (f *Fake) ListRecordingRoleChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.listChanges(&f.changes, limit, func(e ChangeLogEntry) bool { return e.HubID == 0 && e.Action == ChangeRecordingRoles }), nil
}

// listChanges returns at most limit entries of log that pass keep, newest
// first.
func (f *Fake) listChanges(log *changeLog, limit int, keep func(ChangeLogEntry) bool) []ChangeLogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ChangeLogEntry
	for i := len(log.entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := log.entries[i]
		if !keep(e) {
			continue
		}
		e.Diff = slices.Clone(e.Diff)
		out = append(out, e)
	}
	return out
}

// ListFoxholeRecords implements Store.
func (f *Fake) ListFoxholeRecords(ctx context.Context, guildID string) ([]FoxholeRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []FoxholeRecord
	for _, m := range f.members[guildID] {
		out = append(out, m)
	}
	return out, nil
}

// SaveFoxholeNote implements Store.
func (f *Fake) SaveFoxholeNote(ctx context.Context, guildID string, save NoteSave, entry ChangeLogEntry) error {
	return f.writeToLog(ctx, &f.foxholeChanges, &entry, func() (int64, error) {
		members := f.members[guildID]
		if members == nil {
			members = map[string]FoxholeRecord{}
			f.members[guildID] = members
		}
		m := members[save.MemberID]
		if m.Note != save.Before {
			return 0, ErrStale
		}
		m.MemberID, m.Note, m.DisplayName, m.Username = save.MemberID, save.Note, save.DisplayName, save.Username
		members[save.MemberID] = m
		if m.Note == "" && !m.Approved {
			delete(members, save.MemberID)
		}
		return 0, nil
	})
}

// ApproveFoxholeMembers implements Store.
func (f *Fake) ApproveFoxholeMembers(ctx context.Context, guildID string, members []MemberNames, entry ChangeLogEntry) error {
	return f.writeToLog(ctx, &f.foxholeChanges, &entry, func() (int64, error) {
		records := f.members[guildID]
		if records == nil {
			records = map[string]FoxholeRecord{}
			f.members[guildID] = records
		}
		for _, n := range members {
			m := records[n.MemberID]
			m.MemberID, m.Approved, m.DisplayName, m.Username = n.MemberID, true, n.DisplayName, n.Username
			records[n.MemberID] = m
		}
		return 0, nil
	})
}

// ClearFoxholeApprovals implements Store.
func (f *Fake) ClearFoxholeApprovals(ctx context.Context, guildID string, memberIDs []string, entry ChangeLogEntry) error {
	return f.writeToLog(ctx, &f.foxholeChanges, &entry, func() (int64, error) {
		for _, id := range memberIDs {
			f.clearApproval(guildID, id)
		}
		return 0, nil
	})
}

// ClearFoxholeApprovalForRemoval implements Store.
func (f *Fake) ClearFoxholeApprovalForRemoval(ctx context.Context, guildID, memberID string) (bool, error) {
	cleared := false
	err := f.writeAllOrNothing(ctx, nil, func() (int64, error) {
		cleared = f.clearApproval(guildID, memberID)
		return 0, nil
	})
	return cleared, err
}

// clearApproval clears the member's approval, when they have one, drops
// their record when that leaves it with no note, and reports whether they
// had one. The caller holds mu.
func (f *Fake) clearApproval(guildID, memberID string) bool {
	m, ok := f.members[guildID][memberID]
	if !ok || !m.Approved {
		return false
	}
	m.Approved = false
	f.members[guildID][memberID] = m
	if m.Note == "" {
		delete(f.members[guildID], memberID)
	}
	return true
}

// SetFoxholeRecordNames implements Store.
func (f *Fake) SetFoxholeRecordNames(ctx context.Context, guildID string, names []MemberNames) error {
	return f.writeAllOrNothing(ctx, nil, func() (int64, error) {
		for _, n := range names {
			if m, ok := f.members[guildID][n.MemberID]; ok {
				m.DisplayName, m.Username = n.DisplayName, n.Username
				f.members[guildID][n.MemberID] = m
			}
		}
		return 0, nil
	})
}

// ListFoxholeChanges implements Store.
func (f *Fake) ListFoxholeChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.listChanges(&f.foxholeChanges, limit, func(ChangeLogEntry) bool { return true }), nil
}

// StartFoxholeReport implements Store.
func (f *Fake) StartFoxholeReport(ctx context.Context, entry ChangeLogEntry) (ChangeLogEntry, error) {
	return f.appendToLog(ctx, &f.foxholeChanges, &entry, func() (int64, error) {
		f.reports[f.foxholeChanges.nextID] = true
		return 0, nil
	})
}

// UpdateFoxholeReport implements Store.
func (f *Fake) UpdateFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	return f.writeReport(ctx, id, diff, true)
}

// EndFoxholeReport implements Store.
func (f *Fake) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	return f.writeReport(ctx, id, diff, false)
}

// writeReport replaces the diff of the running report with the ID given,
// which stays running as running says. It writes nothing when ctx is done,
// when the diff is not a JSON value, or when no running report has the ID.
func (f *Fake) writeReport(ctx context.Context, id int64, diff json.RawMessage, running bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !json.Valid(diff) {
		return errors.New("store: the change log diff is not a JSON value")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.reports[id] {
		return ErrNotFound
	}
	for i := range f.foxholeChanges.entries {
		if f.foxholeChanges.entries[i].ID == id {
			f.foxholeChanges.entries[i].Diff = slices.Clone(diff)
		}
	}
	f.reports[id] = running
	return nil
}

// RunningFoxholeReports implements Store.
func (f *Fake) RunningFoxholeReports(ctx context.Context) ([]ChangeLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ChangeLogEntry
	for _, e := range f.foxholeChanges.entries {
		if f.reports[e.ID] {
			e.Diff = slices.Clone(e.Diff)
			out = append(out, e)
		}
	}
	return out, nil
}

// LastFoxholeReport implements Store.
func (f *Fake) LastFoxholeReport(ctx context.Context) (FoxholeReport, error) {
	if err := ctx.Err(); err != nil {
		return FoxholeReport{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.foxholeChanges.entries) - 1; i >= 0; i-- {
		e := f.foxholeChanges.entries[i]
		if running, ok := f.reports[e.ID]; ok {
			e.Diff = slices.Clone(e.Diff)
			return FoxholeReport{Entry: e, Running: running}, nil
		}
	}
	return FoxholeReport{}, ErrNotFound
}

// FoxholeReport implements Store.
func (f *Fake) FoxholeReport(ctx context.Context, id int64) (FoxholeReport, error) {
	if err := ctx.Err(); err != nil {
		return FoxholeReport{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	running, ok := f.reports[id]
	if !ok {
		return FoxholeReport{}, ErrNotFound
	}
	for _, e := range f.foxholeChanges.entries {
		if e.ID == id {
			e.Diff = slices.Clone(e.Diff)
			return FoxholeReport{Entry: e, Running: running}, nil
		}
	}
	return FoxholeReport{}, ErrNotFound
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
