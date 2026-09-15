// Package store persists hub settings and spawned channel rows for the
// temporary voice channel feature. One interface, two implementations: Postgres
// for production and an in-memory fake for tests in other packages.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by a get for an ID no row carries.
var ErrNotFound = errors.New("store: not found")

// PermissionSource is the per-hub setting that chooses what a spawned channel
// inherits its permissions from.
type PermissionSource string

const (
	// PermissionSourceCategory makes a spawned channel copy its category's
	// overwrites, the common case.
	PermissionSourceCategory PermissionSource = "category"
	// PermissionSourceHubChannel makes a spawned channel copy the hub channel's
	// own overwrites, for a stricter hub inside a looser category.
	PermissionSourceHubChannel PermissionSource = "hub_channel"
)

// Hub is one row of hub settings. ID, CreatedAt and UpdatedAt are the store's;
// every other field is the caller's.
type Hub struct {
	ID               int64
	GuildID          string
	HubChannelID     string
	BaseString       string
	PermissionSource PermissionSource
	ModeratorRoleIDs []string
	UserLimit        int
	Bitrate          int
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// SpawnedChannel is one row per live spawned channel. HubID is nil once the
// hub row is deleted; the spawned channel lives on until it empties.
// OwnerUserID is empty when the channel has no owner.
type SpawnedChannel struct {
	ChannelID   string
	HubID       *int64
	Number      int
	OwnerUserID string
	CreatedAt   time.Time
}

// Store is the persistence seam. Methods arrive with the tickets that need
// them.
type Store interface {
	// GetHub returns the hub with the given ID, or ErrNotFound.
	GetHub(ctx context.Context, id int64) (Hub, error)
	// ListHubs returns every hub of the guild.
	ListHubs(ctx context.Context, guildID string) ([]Hub, error)
	// UpsertHub inserts the hub, or updates the row that already holds its hub
	// channel ID, and returns the stored row with its ID and timestamps.
	UpsertHub(ctx context.Context, hub Hub) (Hub, error)
	// DeleteHub removes the hub row. Deleting an absent row is not an error.
	DeleteHub(ctx context.Context, id int64) error
	// UpsertSpawnedChannel inserts the row, or updates the row that already
	// holds its channel ID. Written at create and at every handover.
	UpsertSpawnedChannel(ctx context.Context, sc SpawnedChannel) error
	// DeleteSpawnedChannel removes the row. Deleting an absent row is not an
	// error.
	DeleteSpawnedChannel(ctx context.Context, channelID string) error
	// ListSpawnedChannels returns every spawned channel row.
	ListSpawnedChannels(ctx context.Context) ([]SpawnedChannel, error)
}
