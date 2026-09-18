// Package store is the bot's own database: the hubs the panel edits, the
// spawned channels the runtime tracks across a restart, and the change log
// of every panel save. Postgres in production (postgres.go), an in-memory
// Fake for other packages' tests (fake.go). The forum's MySQL stays in utils;
// this package never touches it.
//
// ADR 0012 records why migrations run at startup and the rule every migration
// has to follow.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned by GetHub when no hub has the requested ID. Compare
// with errors.Is.
var ErrNotFound = errors.New("store: not found")

// PermissionSource is the per-hub setting that chooses what a spawned channel
// inherits its permissions from.
type PermissionSource string

const (
	// PermissionCategory copies the hub channel's category: the create payload
	// carries no overwrites, so Discord syncs the new channel from birth.
	PermissionCategory PermissionSource = "category"
	// PermissionHubChannel copies the hub channel's own overwrite list, for a
	// stricter hub that shares a looser category.
	PermissionHubChannel PermissionSource = "hub_channel"
)

// Hub is one row of the hubs table: a hub channel and the settings it stamps
// on the channels it spawns.
type Hub struct {
	// ID is the surrogate key. Zero on a Hub that has never been stored;
	// UpsertHub fills it on return.
	ID           int64
	GuildID      string
	HubChannelID string
	// BaseString is what spawned channels are named from: "<BaseString> - <n>".
	BaseString       string
	PermissionSource PermissionSource
	// ModeratorRoleIDs is a set. The store returns the same IDs in no promised
	// order; an empty set reads back as a slice of length zero, and callers
	// must not distinguish nil from empty.
	ModeratorRoleIDs []string
	UserLimit        int
	Bitrate          int
	Enabled          bool
	// CreatedAt and UpdatedAt are set by the store, never by the caller.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SpawnedChannel is one row of the spawned channels table, written at create
// and at every handover so the restart sweep can restore number and owner.
type SpawnedChannel struct {
	ChannelID string
	// HubID is the hub's surrogate ID, or zero once the hub row is gone: the
	// reference clears when the hub is deleted, so a spawned channel of a
	// removed hub keeps its row and dies when empty.
	HubID  int64
	Number int
	// OwnerUserID is empty when the channel has no owner.
	OwnerUserID string
	// CreatedAt is set by the store at insert and kept on update. Informational.
	CreatedAt time.Time
}

// ChangeAction is what a change log entry records: which kind of panel save
// made it.
type ChangeAction string

const (
	// ChangeCreate is the panel creating a hub channel and its hub. The
	// create form is a later ticket's; the value is in the check from the
	// start so that ticket adds no migration.
	ChangeCreate ChangeAction = "create"
	// ChangeRegister is the panel making an existing channel a hub.
	ChangeRegister ChangeAction = "register"
	// ChangeUpdate is a save of a hub's settings.
	ChangeUpdate ChangeAction = "update"
	// ChangeRemove is the panel deleting a hub row.
	ChangeRemove ChangeAction = "remove"
	// ChangeModerators is a save of the guild-wide moderator roles, a later
	// ticket's form, in the check from the start for the same reason.
	ChangeModerators ChangeAction = "moderators"
)

// ChangeLogEntry is one row of the change log: who saved what through the
// panel, when, and from what to what.
type ChangeLogEntry struct {
	// ID is the surrogate key, set by the store.
	ID int64
	// HubID is the hub the save was about, or zero for a save about no hub:
	// the guild-wide moderator roles, and every entry of a hub whose row has
	// since been deleted, since the reference clears with the row.
	HubID         int64
	ForumUserID   int
	ForumUsername string
	// At is set by the store at append, never by the caller.
	At     time.Time
	Action ChangeAction
	// Diff is a JSON object keyed by field name, each value an object with
	// "before" and "after". The store keeps the bytes and never reads inside
	// them; the panel's service layer decides the shape.
	Diff json.RawMessage
}

// Store is the one seam between the bot and its database. Two implementations:
// Postgres here and Fake for tests.
type Store interface {
	// GetHub returns the hub with this ID, or ErrNotFound.
	GetHub(ctx context.Context, id int64) (Hub, error)
	// ListHubs returns every hub of the guild, in no promised order.
	ListHubs(ctx context.Context, guildID string) ([]Hub, error)
	// UpsertHub inserts the hub, or updates the existing row for the same
	// HubChannelID in place. It returns the stored row with ID, CreatedAt and
	// UpdatedAt filled; the caller's values for those three are ignored.
	UpsertHub(ctx context.Context, hub Hub) (Hub, error)
	// DeleteHub removes the hub. Deleting a hub that does not exist is not an
	// error. Spawned channel rows of the hub keep their rows with the hub reference
	// cleared.
	DeleteHub(ctx context.Context, id int64) error

	// UpsertSpawnedChannel inserts the row, or updates the existing row for
	// the same ChannelID in place.
	UpsertSpawnedChannel(ctx context.Context, sc SpawnedChannel) error
	// DeleteSpawnedChannel removes the row. Deleting a row that does not exist
	// is not an error.
	DeleteSpawnedChannel(ctx context.Context, channelID string) error
	// ListSpawnedChannels returns every spawned channel row, in no promised
	// order.
	ListSpawnedChannels(ctx context.Context) ([]SpawnedChannel, error)

	// GetGuildModeratorRoles returns the guild-wide moderator role IDs, the
	// roles that may rename any spawned channel of every hub. A guild with no
	// row reads back as an empty set with no error. Same set rule as
	// Hub.ModeratorRoleIDs: no promised order, and nil and empty are one thing.
	GetGuildModeratorRoles(ctx context.Context, guildID string) ([]string, error)
	// SetGuildModeratorRoles replaces the guild-wide moderator role IDs.
	SetGuildModeratorRoles(ctx context.Context, guildID string, roleIDs []string) error

	// AppendChangeLog adds one entry. The caller's ID and At are ignored.
	AppendChangeLog(ctx context.Context, entry ChangeLogEntry) error
	// ListChangeLog returns at most limit entries whose hub reference is
	// hubID, newest first in append order. A hubID of zero lists the entries
	// that reference no hub.
	ListChangeLog(ctx context.Context, hubID int64, limit int) ([]ChangeLogEntry, error)
}
