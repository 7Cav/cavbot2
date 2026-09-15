// Package store is the bot's own database: the hubs the panel edits and the
// spawned channels the runtime tracks across a restart. Postgres in production
// (postgres.go), an in-memory Fake for other packages' tests (fake.go). The
// forum's MySQL stays in utils; this package never touches it.
//
// ADR 0012 records why migrations run at startup and the rule every migration
// has to follow.
package store

import (
	"context"
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

// Spawned is one row of the spawned channels table, written at create and at
// every handover so the restart sweep can restore number and owner.
type Spawned struct {
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

// Store is the one seam between the bot and its database. Two implementations:
// Postgres here and Fake for tests. Guild settings and the change log arrive
// with the tickets that need them.
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
	// error. Spawned rows of the hub keep their rows with the hub reference
	// cleared.
	DeleteHub(ctx context.Context, id int64) error

	// UpsertSpawned inserts the row, or updates the existing row for the same
	// ChannelID in place.
	UpsertSpawned(ctx context.Context, s Spawned) error
	// DeleteSpawned removes the row. Deleting a row that does not exist is not
	// an error.
	DeleteSpawned(ctx context.Context, channelID string) error
	// ListSpawned returns every spawned row, in no promised order.
	ListSpawned(ctx context.Context) ([]Spawned, error)
}
