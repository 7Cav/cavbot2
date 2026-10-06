// Package store is the bot's own database: the hubs the panel edits, the
// spawned channels the runtime tracks across a restart, the change log of
// every panel save, and the Foxhole page's notes and approvals, with a
// change log of their own that also holds each Foxhole action's report. Postgres in production (postgres.go), an in-memory
// Fake for other packages' tests (fake.go). The forum's MySQL stays in utils;
// this package never touches it.
//
// Migrations run at startup, and every one stays compatible with the previous
// release, since a rollback runs the older image against the newer schema:
// additive in the release that introduces it, drops and renames one release
// later (spec #285).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned by GetHub, SaveHub and RemoveHub when no hub has
// the requested ID, and by SetSpawnedChannelLock when no row has the
// channel. Compare with errors.Is.
var ErrNotFound = errors.New("store: not found")

// ErrStale is returned by a combined write whose record is not as the
// caller read it (#373): SaveHub of a hub whose row is at another version,
// or of a new hub on a channel a hub already stands on,
// SaveGuildModeratorRoles of a set whose row is at another version, and
// SaveFoxholeNote over a note that isn't the one the saver loaded. The
// write lands neither the settings nor the entry. Compare with errors.Is.
var ErrStale = errors.New("store: the record changed since it was read")

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
	// SaveHub fills it on return.
	ID int64
	// Version counts the saves of the hub that took effect: every SaveHub
	// that takes effect adds exactly one, a save that changes nothing
	// included. A caller hands back the version it read, and SaveHub writes
	// only over that version (#373).
	Version      int64
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
	// DeleteDelayMinutes is the hub's delete delay (GLOSSARY.md): how long, in
	// whole minutes, a spawned channel must stay empty before the bot deletes
	// it. 0 deletes it the moment it empties, and a hub stored before the
	// field existed reads back 0. The panel bounds it at 0 to 240.
	DeleteDelayMinutes int
	Enabled            bool
	// RenamingAllowed is whether /voice-rename works on the hub's spawned
	// channels. On for every hub stored before the field existed; SaveHub
	// writes the caller's value, so a Hub built without it stores it off.
	// Turning it off leaves every channel's current name in place.
	RenamingAllowed bool
	// LockingAllowed is whether /voice-lock works on the hub's spawned
	// channels. Off by default, and a hub stored before the field existed
	// reads back off. Turning it off leaves existing locks in place.
	LockingAllowed bool
	// CreatedAt and UpdatedAt are set by the store, never by the caller.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GuildModeratorRoles is a guild's guild-wide moderator roles with the
// version of the settings row that holds them.
type GuildModeratorRoles struct {
	// RoleIDs is a set, under the same rule as Hub.ModeratorRoleIDs: no
	// promised order, and nil and empty are one thing.
	RoleIDs []string
	// Version counts the saves of the set that took effect, as Hub.Version
	// does. A guild with no settings row is at version 0.
	Version int64
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
	// Lock is the channel's lock, so the restart sweep can restore it.
	// SetSpawnedChannelLock is its one writer: UpsertSpawnedChannel ignores
	// the caller's value, starts a new row unlocked and keeps an existing
	// row's lock, so a handover never touches a lock.
	Lock ChannelLock
	// CreatedAt is set by the store at insert and kept on update. Informational.
	CreatedAt time.Time
}

// ChannelLock is a spawned channel's lock as its row records it (spec #347).
// The zero value is an unlocked channel. The row is the bot's truth about a
// lock: the bot never infers one from Discord's permissions.
type ChannelLock struct {
	Locked bool
	// LockerUserID is the member who locked the channel, empty when it is
	// unlocked.
	LockerUserID string
	// NoticeMessageID is the lock notice's message ID, empty when the channel
	// is unlocked or its notice was never posted.
	NoticeMessageID string
}

// ChangeAction is what a change log entry records: which kind of panel save
// made it.
type ChangeAction string

const (
	// ChangeCreate is the panel creating a hub channel and its hub.
	ChangeCreate ChangeAction = "create"
	// ChangeRegister is the panel making an existing channel a hub.
	ChangeRegister ChangeAction = "register"
	// ChangeUpdate is a save of a hub's settings.
	ChangeUpdate ChangeAction = "update"
	// ChangeRemove is the panel deleting a hub row.
	ChangeRemove ChangeAction = "remove"
	// ChangeModerators is a save of the guild-wide moderator roles.
	ChangeModerators ChangeAction = "moderators"
	// ChangeNote is a save of one member's note on the Foxhole page. Its
	// entry goes in the Foxhole change log, never the hub page's.
	ChangeNote ChangeAction = "note"
	// ChangeApprove is an approvals save on the Foxhole page that marks
	// members approved collaborators. Its entry goes in the Foxhole change
	// log.
	ChangeApprove ChangeAction = "approve"
	// ChangeClearApproval is an approvals save on the Foxhole page that
	// clears members' approvals. Its entry goes in the Foxhole change log.
	ChangeClearApproval ChangeAction = "clear_approval"
	// ChangePurge is a purge started on the Foxhole page. Its entry is the
	// purge's report, in the Foxhole change log.
	ChangePurge ChangeAction = "purge"
)

// FoxholeReport is the report of a Foxhole action started on the Foxhole
// page as the Foxhole change log holds it: the entry the action wrote when
// it started, its diff as the action last wrote it, and whether the action
// is still running. The runtime decides the diff's shape; the store keeps
// the bytes.
type FoxholeReport struct {
	Entry   ChangeLogEntry
	Running bool
}

// FoxholeRecord is one member's Foxhole record (spec #434): their note,
// whether they are an approved collaborator, and the display name and
// username the panel last saw them under. The store holds one for each
// member of the guild with a note or an approval, and nothing about who
// holds a Foxhole role: a command purge recreates the role under a new ID.
type FoxholeRecord struct {
	MemberID string
	Note     string
	Approved bool
	// DisplayName and Username are the names the panel last saw, so a
	// member who left the server still shows under a name.
	DisplayName string
	Username    string
}

// NoteSave is one save of a member's note: the note as the saver loaded it,
// the note they saved, and the member's names as the panel sees them now.
type NoteSave struct {
	MemberID string
	// Before is the note as the saver loaded it, empty for a member with no
	// record. The save writes only over it.
	Before      string
	Note        string
	DisplayName string
	Username    string
}

// ChangeLogEntry is one row of the change log: who saved what through the
// panel, when, and from what to what.
type ChangeLogEntry struct {
	// ID is the surrogate key, set by the store.
	ID int64
	// HubID is the hub the save was about, or zero for a save about no hub:
	// the guild-wide moderator roles, a remove, and every entry of a hub
	// whose row has since been deleted, since the reference clears with the
	// row. Set by the store from the save that writes the entry.
	HubID         int64
	ForumUserID   int
	ForumUsername string
	// At is set by the store when it writes the entry, never by the caller.
	At     time.Time
	Action ChangeAction
	// Diff is a JSON object keyed by field name, each value an object with
	// "before" and "after". The store keeps the bytes and never reads inside
	// them; the panel's service layer decides the shape. Bytes that are not
	// a JSON value are refused, and the save writes nothing.
	Diff json.RawMessage
}

// MemberNames is a member's display name and username as the panel saw
// them.
type MemberNames struct {
	MemberID    string
	DisplayName string
	Username    string
}

// Store is the one seam between the bot and its database. Two implementations:
// Postgres here and Fake for tests.
//
// A panel save is one call: SaveHub, RemoveHub or SaveGuildModeratorRoles
// writes the save's settings and its change log entry in one transaction,
// so both land or neither does, and no method writes either alone (#362).
// The transaction never leaves the store. The pool holds two connections,
// and a transaction handed to panel code would hold one of them while that
// code ran; a call to the plain store made there by mistake would wait on
// the other connection, or write outside the transaction.
type Store interface {
	// GetHub returns the hub with this ID, or ErrNotFound.
	GetHub(ctx context.Context, id int64) (Hub, error)
	// ListHubs returns every hub of the guild, in no promised order.
	ListHubs(ctx context.Context, guildID string) ([]Hub, error)
	// SaveHub writes a save's hub and its change log entry together, and the
	// stored row comes back with ID, Version, CreatedAt and UpdatedAt filled.
	// A hub with no ID is inserted; ErrStale when a hub already stands on its
	// HubChannelID. A hub with an ID updates that row's settings only while
	// the row is at hub.Version, and never inserts: ErrStale when the row is
	// at another version, ErrNotFound when no row has the ID. An update keeps
	// the row's GuildID and HubChannelID. Either way the row's version goes
	// up by one. The entry is appended under the stored row's ID, whatever
	// HubID the caller set, so a create or register entry references the hub
	// it made.
	SaveHub(ctx context.Context, hub Hub, entry ChangeLogEntry) (Hub, error)
	// RemoveHub deletes the hub and appends a remove's change log entry
	// together. The entry references no hub, whatever HubID the caller set,
	// since the row is gone, and the hub's earlier entries clear their
	// reference to match. Spawned channel rows of the hub keep their rows
	// with the hub reference cleared. Removing a hub that does not exist is
	// ErrNotFound and appends no entry, since nothing took effect.
	RemoveHub(ctx context.Context, id int64, entry ChangeLogEntry) error

	// UpsertSpawnedChannel inserts the row, or updates the existing row for
	// the same ChannelID in place. It writes hub, number and owner, never the
	// lock: a new row is unlocked and an existing row keeps its lock.
	UpsertSpawnedChannel(ctx context.Context, sc SpawnedChannel) error
	// SetSpawnedChannelLock replaces the lock on the row for channelID and
	// changes nothing else on it. ErrNotFound when no row has that channel.
	SetSpawnedChannelLock(ctx context.Context, channelID string, lock ChannelLock) error
	// DeleteSpawnedChannel removes the row. Deleting a row that does not exist
	// is not an error.
	DeleteSpawnedChannel(ctx context.Context, channelID string) error
	// ListSpawnedChannels returns every spawned channel row, in no promised
	// order.
	ListSpawnedChannels(ctx context.Context) ([]SpawnedChannel, error)

	// GetGuildModeratorRoles returns the guild-wide moderator roles, the
	// roles that may rename, lock and unlock any spawned channel of every
	// hub, as far as each hub's settings allow, with their version. A guild
	// with no row reads back as an empty set at version 0, with no error.
	GetGuildModeratorRoles(ctx context.Context, guildID string) (GuildModeratorRoles, error)
	// SaveGuildModeratorRoles replaces the guild-wide moderator role IDs
	// with roles.RoleIDs and appends the save's change log entry together,
	// only while the guild's row is at roles.Version, 0 meaning no row, and
	// the row ends one version on. ErrStale when it is at another version.
	// The entry references no hub, whatever HubID the caller set.
	SaveGuildModeratorRoles(ctx context.Context, guildID string, roles GuildModeratorRoles, entry ChangeLogEntry) error

	// ListChangeLog returns at most limit entries whose hub reference is
	// hubID, newest first in append order. A hubID of zero lists the entries
	// that reference no hub.
	ListChangeLog(ctx context.Context, hubID int64, limit int) ([]ChangeLogEntry, error)
	// ListModeratorChanges returns at most limit guild-wide moderator saves,
	// the entries with action ChangeModerators that reference no hub, newest
	// first in append order. The no-hub bucket also holds every remove and
	// the earlier entries of removed hubs; this is the panel's guild-wide
	// section reading its own.
	ListModeratorChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error)

	// ListFoxholeRecords returns every Foxhole record of the guild, in no
	// promised order.
	ListFoxholeRecords(ctx context.Context, guildID string) ([]FoxholeRecord, error)
	// SaveFoxholeNote writes the member's note and names and appends the
	// save's entry to the Foxhole change log together, only while the
	// stored note is save.Before, a member with no record holding the empty
	// note. ErrStale otherwise, and nothing written. A save that leaves a
	// member with no note and no approval removes their record, since the
	// store holds records only for members with one or the other.
	SaveFoxholeNote(ctx context.Context, guildID string, save NoteSave, entry ChangeLogEntry) error
	// ApproveFoxholeMembers marks each member given an approved
	// collaborator and appends the save's entry to the Foxhole change log
	// together. A member with a record keeps their note and takes the names
	// given; one with none gets a record, approved with no note, under the
	// names given.
	ApproveFoxholeMembers(ctx context.Context, guildID string, members []MemberNames, entry ChangeLogEntry) error
	// ClearFoxholeApprovals clears the approval of each member given and
	// appends the save's entry to the Foxhole change log together. A member
	// left with no note and no approval loses their record, as in
	// SaveFoxholeNote. A member with no record is left with none.
	ClearFoxholeApprovals(ctx context.Context, guildID string, memberIDs []string, entry ChangeLogEntry) error
	// SetFoxholeRecordNames replaces the last-seen names of each member
	// given who has a record, and starts no record for one who hasn't. It
	// appends no entry: no person made the change.
	SetFoxholeRecordNames(ctx context.Context, guildID string, names []MemberNames) error
	// ListFoxholeChanges returns at most limit entries of the Foxhole page's
	// change log, newest first in append order. The Foxhole change log is
	// kept apart from the hub page's: no hub page list returns its entries,
	// and it returns none of theirs. Entries reference no hub.
	ListFoxholeChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error)

	// StartFoxholeReport appends a Foxhole action's report to the Foxhole
	// change log, marked running, and returns the entry with its ID and time
	// set. The time is when the action started.
	StartFoxholeReport(ctx context.Context, entry ChangeLogEntry) (ChangeLogEntry, error)
	// UpdateFoxholeReport replaces the diff of the running report with the ID
	// given, which stays running. ErrNotFound when no running report has the
	// ID, a save's entry and an ended report among them, and nothing written.
	UpdateFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error
	// EndFoxholeReport replaces the diff of the running report with the ID
	// given and marks it ended, after which it takes no more writes.
	// ErrNotFound as UpdateFoxholeReport.
	EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error
	// RunningFoxholeReports returns every report still marked running, each
	// with its diff as last written, oldest first. An ended report and a
	// save's entry are never among them.
	RunningFoxholeReports(ctx context.Context) ([]ChangeLogEntry, error)
	// LastFoxholeReport returns the newest report in the Foxhole change log,
	// running or not, however many saves' entries were appended after it.
	// ErrNotFound when the log holds none.
	LastFoxholeReport(ctx context.Context) (FoxholeReport, error)
}
