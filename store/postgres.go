package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/7cav/cavbot2/utils"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// migrationFiles is every migration, compiled into the binary so the
// CGO_ENABLED=0 image needs no second COPY. Files are named
// <timestamp>_<slug>.sql. Number a new one with the output of
// `date -u +%Y%m%d%H%M%S`, run when you write it or rebase it, which also
// keeps two written on the same day apart. goose refuses an unapplied file
// numbered below the highest one a database has applied, and the bot then
// fails at startup. So a file a branch adds sorts above every migration on
// develop, and is numbered no later than now so it can't sit above files
// written before that date. A migration on develop never changes, since a
// database that applied it never runs the edit. The gate enforces all three
// rules with .github/scripts/check-migrations.sh.
//
// goose's out-of-order mode stays off. It would let production apply files in
// a different order from a fresh database, so a schema could differ from the
// one the tests built without any error.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

const (
	// maxOpenConns keeps the pool as small as the forum pool: a handful of hub
	// rows and one write per spawn is no load.
	maxOpenConns = 2
	// pingAttempts and pingInterval bound the startup wait for a Postgres that
	// is restarting at the moment the bot comes up. Compose already waits for
	// the healthcheck, so this covers only a recreate that overlaps a database
	// restart.
	pingAttempts = 10
	pingInterval = time.Second
)

// Postgres is the production Store over database/sql with the pgx driver.
type Postgres struct {
	db *sql.DB
}

// Open connects to the bot's database, waits for it to answer a ping, and runs
// every pending migration under a Postgres session lock before returning. An
// error from any step means the bot must not start: main exits non-zero and
// the next start retries.
func Open(ctx context.Context, dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		// Not wrapped: pgx's parse error echoes the connection string with a
		// best-effort password redaction, and the DSN is never logged.
		return nil, errors.New("open bot database: the DSN does not parse")
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetConnMaxIdleTime(30 * time.Second)

	if err := pingWithRetry(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Postgres{db: db}, nil
}

// Close releases the connection pool.
func (p *Postgres) Close() error {
	return p.db.Close()
}

// pingWithRetry pings until the database answers or the attempts run out.
func pingWithRetry(ctx context.Context, db *sql.DB) error {
	var err error
	for attempt := 1; attempt <= pingAttempts; attempt++ {
		if err = db.PingContext(ctx); err == nil {
			return nil
		}
		if attempt == pingAttempts {
			break
		}
		utils.Warn("Bot database not answering, retrying",
			"attempt", attempt, "max_attempts", pingAttempts, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pingInterval):
		}
	}
	return fmt.Errorf("ping bot database after %d attempts: %w", pingAttempts, err)
}

// migrate applies every pending embedded migration. The session locker takes
// a Postgres advisory lock for the run, so two bot processes against one
// database (a deploy recreate that overlaps the old container) cannot
// both apply the same file. goose runs each file in its own transaction: a
// failed file rolls back and is retried on the next start.
func migrate(ctx context.Context, db *sql.DB) error {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("embedded migrations: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files,
		goose.WithSessionLocker(locker),
		goose.WithSlog(utils.Logger),
	)
	if err != nil {
		return fmt.Errorf("migration provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate bot database: %w", err)
	}
	utils.Info("Bot database migrated", "applied", len(results))
	return nil
}

// scanner is what scanHub and scanSpawnedChannel read from: a *sql.Row or a
// *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// queryAll runs a query and scans every row with scan. The caller wraps the
// error with what it was listing.
func queryAll[T any](ctx context.Context, db *sql.DB, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// hubColumns is the select list every hub read shares, in scanHub's order.
const hubColumns = `id, version, guild_id, hub_channel_id, base_string, permission_source,
	moderator_role_ids, user_limit, bitrate, delete_delay_minutes, enabled, renaming_allowed, locking_allowed,
	created_at, updated_at`

// scanHub reads one hub row in hubColumns order.
func scanHub(row scanner) (Hub, error) {
	var (
		h     Hub
		roles []byte
	)
	err := row.Scan(&h.ID, &h.Version, &h.GuildID, &h.HubChannelID, &h.BaseString, &h.PermissionSource,
		&roles, &h.UserLimit, &h.Bitrate, &h.DeleteDelayMinutes, &h.Enabled, &h.RenamingAllowed, &h.LockingAllowed, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return Hub{}, err
	}
	if err := json.Unmarshal(roles, &h.ModeratorRoleIDs); err != nil {
		return Hub{}, fmt.Errorf("decode moderator role IDs of hub %d: %w", h.ID, err)
	}
	return h, nil
}

// GetHub implements Store.
func (p *Postgres) GetHub(ctx context.Context, id int64) (Hub, error) {
	row := p.db.QueryRowContext(ctx, `SELECT `+hubColumns+` FROM hubs WHERE id = $1`, id)
	h, err := scanHub(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Hub{}, ErrNotFound
	}
	if err != nil {
		return Hub{}, fmt.Errorf("get hub %d: %w", id, err)
	}
	return h, nil
}

// ListHubs implements Store.
func (p *Postgres) ListHubs(ctx context.Context, guildID string) ([]Hub, error) {
	hubs, err := queryAll(ctx, p.db, scanHub,
		`SELECT `+hubColumns+` FROM hubs WHERE guild_id = $1 ORDER BY id`, guildID)
	if err != nil {
		return nil, fmt.Errorf("list hubs of guild %q: %w", guildID, err)
	}
	return hubs, nil
}

// SaveHub implements Store.
func (p *Postgres) SaveHub(ctx context.Context, hub Hub, entry ChangeLogEntry) (Hub, error) {
	var stored Hub
	_, err := p.saveWithEntry(ctx, entry, func(tx *sql.Tx) (int64, error) {
		var err error
		if hub.ID == 0 {
			stored, err = insertHub(ctx, tx, hub)
		} else {
			stored, err = updateHub(ctx, tx, hub)
		}
		return stored.ID, err
	})
	if err != nil {
		return Hub{}, fmt.Errorf("save hub %q: %w", hub.HubChannelID, err)
	}
	return stored, nil
}

// saveWithEntry makes a save's one store write: write stores the settings
// and returns the hub the entry goes under, zero for none, and the entry is
// appended under it, both in one transaction. It returns the entry's ID. A
// write that fails appends nothing.
func (p *Postgres) saveWithEntry(ctx context.Context, entry ChangeLogEntry, write func(tx *sql.Tx) (hubID int64, err error)) (int64, error) {
	var entryID int64
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		hubID, err := write(tx)
		if err != nil {
			return err
		}
		entry.HubID = hubID
		entryID, err = insertChange(ctx, tx, entry)
		return err
	})
	return entryID, err
}

// inTx runs fn in one transaction and commits it, or rolls it back when fn
// fails, so a save's settings and its entry land together or not at all.
// The rollback's own error is dropped: fn's error is the save's failure, and
// pgx closes a connection whose rollback fails, which ends the transaction
// with nothing written.
func (p *Postgres) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// encodeRoles is a role set as the JSONB columns hold it: never null.
func encodeRoles(ids []string) ([]byte, error) {
	if ids == nil {
		ids = []string{}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("encode moderator role IDs: %w", err)
	}
	return raw, nil
}

// insertHub writes a new hub row at version 1. A row already on the hub
// channel stops it, and nothing written is ErrStale: a new hub never writes
// over one that stands.
func insertHub(ctx context.Context, tx *sql.Tx, hub Hub) (Hub, error) {
	rolesJSON, err := encodeRoles(hub.ModeratorRoleIDs)
	if err != nil {
		return Hub{}, err
	}
	row := tx.QueryRowContext(ctx, `
		INSERT INTO hubs (guild_id, hub_channel_id, base_string, permission_source,
			moderator_role_ids, user_limit, bitrate, delete_delay_minutes, enabled, renaming_allowed, locking_allowed,
			version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 1)
		ON CONFLICT (hub_channel_id) DO NOTHING
		RETURNING `+hubColumns,
		hub.GuildID, hub.HubChannelID, hub.BaseString, string(hub.PermissionSource),
		rolesJSON, hub.UserLimit, hub.Bitrate, hub.DeleteDelayMinutes, hub.Enabled, hub.RenamingAllowed, hub.LockingAllowed)
	stored, err := scanHub(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Hub{}, ErrStale
	}
	return stored, err
}

// updateHub writes the settings of the row with the hub's ID, only while
// the row is at the hub's version, and adds one to it. The row keeps its
// guild, channel and created_at. No row with the ID is ErrNotFound, and a
// row at another version is ErrStale; either way it never inserts.
func updateHub(ctx context.Context, tx *sql.Tx, hub Hub) (Hub, error) {
	rolesJSON, err := encodeRoles(hub.ModeratorRoleIDs)
	if err != nil {
		return Hub{}, err
	}
	row := tx.QueryRowContext(ctx, `
		UPDATE hubs SET
			base_string = $3,
			permission_source = $4,
			moderator_role_ids = $5,
			user_limit = $6,
			bitrate = $7,
			delete_delay_minutes = $8,
			enabled = $9,
			renaming_allowed = $10,
			locking_allowed = $11,
			version = version + 1,
			updated_at = now()
		WHERE id = $1 AND version = $2
		RETURNING `+hubColumns,
		hub.ID, hub.Version, hub.BaseString, string(hub.PermissionSource),
		rolesJSON, hub.UserLimit, hub.Bitrate, hub.DeleteDelayMinutes, hub.Enabled, hub.RenamingAllowed, hub.LockingAllowed)
	stored, err := scanHub(row)
	if !errors.Is(err, sql.ErrNoRows) {
		return stored, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM hubs WHERE id = $1)`, hub.ID).Scan(&exists); err != nil {
		return Hub{}, err
	}
	if !exists {
		return Hub{}, ErrNotFound
	}
	return Hub{}, ErrStale
}

// RemoveHub implements Store.
func (p *Postgres) RemoveHub(ctx context.Context, id int64, entry ChangeLogEntry) (int64, error) {
	entryID, err := p.saveWithEntry(ctx, entry, func(tx *sql.Tx) (int64, error) {
		return 0, deleteHub(ctx, tx, id)
	})
	if err != nil {
		return 0, fmt.Errorf("remove hub %d: %w", id, err)
	}
	return entryID, nil
}

// HubRemoval implements Store.
func (p *Postgres) HubRemoval(ctx context.Context, id int64) (ChangeLogEntry, error) {
	e, err := scanChangeLogEntry(p.db.QueryRowContext(ctx, `
		SELECT id, hub_id, forum_user_id, forum_username, at, action, diff
		FROM change_log WHERE id = $1 AND action = $2`, id, string(ChangeRemove)))
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeLogEntry{}, ErrNotFound
	}
	if err != nil {
		return ChangeLogEntry{}, fmt.Errorf("read hub removal %d: %w", id, err)
	}
	return e, nil
}

// deleteHub deletes the hub row, and is ErrNotFound when there is none. The
// spawned rows' and the entries' hub reference clears through the foreign
// keys' ON DELETE SET NULL, so nothing here touches them.
func deleteHub(ctx context.Context, tx *sql.Tx, id int64) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM hubs WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertSpawnedChannel implements Store. The row is keyed on channel_id: a
// conflict updates hub, number and owner in place and keeps created_at and
// the lock columns, which is what the handover write relies on. A new row
// takes the lock columns' defaults, unlocked. A zero HubID and an empty
// OwnerUserID are stored as NULL.
func (p *Postgres) UpsertSpawnedChannel(ctx context.Context, sc SpawnedChannel) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO spawned_channels (channel_id, hub_id, number, owner_user_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (channel_id) DO UPDATE SET
			hub_id = EXCLUDED.hub_id,
			number = EXCLUDED.number,
			owner_user_id = EXCLUDED.owner_user_id`,
		sc.ChannelID,
		sql.NullInt64{Int64: sc.HubID, Valid: sc.HubID != 0},
		sc.Number,
		sql.NullString{String: sc.OwnerUserID, Valid: sc.OwnerUserID != ""})
	if err != nil {
		return fmt.Errorf("upsert spawned channel %q: %w", sc.ChannelID, err)
	}
	return nil
}

// SetSpawnedChannelLock implements Store. Empty user and message IDs are
// stored as NULL.
func (p *Postgres) SetSpawnedChannelLock(ctx context.Context, channelID string, lock ChannelLock) error {
	res, err := p.db.ExecContext(ctx, `
		UPDATE spawned_channels
		SET locked = $2, locker_user_id = $3, lock_notice_message_id = $4
		WHERE channel_id = $1`,
		channelID, lock.Locked,
		sql.NullString{String: lock.LockerUserID, Valid: lock.LockerUserID != ""},
		sql.NullString{String: lock.NoticeMessageID, Valid: lock.NoticeMessageID != ""})
	if err != nil {
		return fmt.Errorf("set lock of spawned channel %q: %w", channelID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set lock of spawned channel %q: %w", channelID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSpawnedChannel implements Store.
func (p *Postgres) DeleteSpawnedChannel(ctx context.Context, channelID string) error {
	if _, err := p.db.ExecContext(ctx, `DELETE FROM spawned_channels WHERE channel_id = $1`, channelID); err != nil {
		return fmt.Errorf("delete spawned channel %q: %w", channelID, err)
	}
	return nil
}

// scanSpawnedChannel reads one spawned channel row: channel_id, hub_id,
// number, owner_user_id, locked, locker_user_id, lock_notice_message_id,
// created_at. NULL hub, owner, locker and message read back as zero and
// empty.
func scanSpawnedChannel(row scanner) (SpawnedChannel, error) {
	var (
		sc      SpawnedChannel
		hubID   sql.NullInt64
		owner   sql.NullString
		locker  sql.NullString
		message sql.NullString
	)
	if err := row.Scan(&sc.ChannelID, &hubID, &sc.Number, &owner,
		&sc.Lock.Locked, &locker, &message, &sc.CreatedAt); err != nil {
		return SpawnedChannel{}, err
	}
	sc.HubID = hubID.Int64
	sc.OwnerUserID = owner.String
	sc.Lock.LockerUserID = locker.String
	sc.Lock.NoticeMessageID = message.String
	return sc, nil
}

// ListSpawnedChannels implements Store.
func (p *Postgres) ListSpawnedChannels(ctx context.Context) ([]SpawnedChannel, error) {
	out, err := queryAll(ctx, p.db, scanSpawnedChannel, `
		SELECT channel_id, hub_id, number, owner_user_id,
			locked, locker_user_id, lock_notice_message_id, created_at
		FROM spawned_channels ORDER BY channel_id`)
	if err != nil {
		return nil, fmt.Errorf("list spawned channels: %w", err)
	}
	return out, nil
}

// GetGuildModeratorRoles implements Store. No row is an empty set at
// version 0, not an error, since a guild whose guild-wide roles were never
// saved has none.
func (p *Postgres) GetGuildModeratorRoles(ctx context.Context, guildID string) (GuildModeratorRoles, error) {
	var (
		raw []byte
		out GuildModeratorRoles
	)
	err := p.db.QueryRowContext(ctx,
		`SELECT moderator_role_ids, version FROM guild_settings WHERE guild_id = $1`, guildID).Scan(&raw, &out.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return GuildModeratorRoles{RoleIDs: []string{}}, nil
	}
	if err != nil {
		return GuildModeratorRoles{}, fmt.Errorf("get guild moderator roles of guild %q: %w", guildID, err)
	}
	if err := json.Unmarshal(raw, &out.RoleIDs); err != nil {
		return GuildModeratorRoles{}, fmt.Errorf("decode guild moderator roles of guild %q: %w", guildID, err)
	}
	if out.RoleIDs == nil {
		out.RoleIDs = []string{}
	}
	return out, nil
}

// SaveGuildModeratorRoles implements Store.
func (p *Postgres) SaveGuildModeratorRoles(ctx context.Context, guildID string, roles GuildModeratorRoles, entry ChangeLogEntry) error {
	_, err := p.saveWithEntry(ctx, entry, func(tx *sql.Tx) (int64, error) {
		return 0, setGuildModeratorRoles(ctx, tx, guildID, roles)
	})
	if err != nil {
		return fmt.Errorf("save guild moderator roles of guild %q: %w", guildID, err)
	}
	return nil
}

// setGuildModeratorRoles writes the guild settings row only while it is at
// roles.Version: at version 0 it inserts the row at 1, and a row already
// there stops it; above 0 it replaces the set of the row at that version
// and adds one to it. Nothing written is ErrStale.
func setGuildModeratorRoles(ctx context.Context, tx *sql.Tx, guildID string, roles GuildModeratorRoles) error {
	rolesJSON, err := encodeRoles(roles.RoleIDs)
	if err != nil {
		return err
	}
	var res sql.Result
	if roles.Version == 0 {
		res, err = tx.ExecContext(ctx, `
			INSERT INTO guild_settings (guild_id, moderator_role_ids, version)
			VALUES ($1, $2, 1)
			ON CONFLICT (guild_id) DO NOTHING`,
			guildID, rolesJSON)
	} else {
		res, err = tx.ExecContext(ctx, `
			UPDATE guild_settings SET moderator_role_ids = $2, version = version + 1
			WHERE guild_id = $1 AND version = $3`,
			guildID, rolesJSON, roles.Version)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrStale
	}
	return nil
}

// GetRecordingRoles implements Store. No row is an empty set at version
// 0, not an error, since a guild whose recording roles were never saved
// has none.
func (p *Postgres) GetRecordingRoles(ctx context.Context, guildID string) (RecordingRoles, error) {
	var (
		raw []byte
		out RecordingRoles
	)
	err := p.db.QueryRowContext(ctx,
		`SELECT role_ids, version FROM recording_roles WHERE guild_id = $1`, guildID).Scan(&raw, &out.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return RecordingRoles{RoleIDs: []string{}}, nil
	}
	if err != nil {
		return RecordingRoles{}, fmt.Errorf("get recording roles of guild %q: %w", guildID, err)
	}
	if err := json.Unmarshal(raw, &out.RoleIDs); err != nil {
		return RecordingRoles{}, fmt.Errorf("decode recording roles of guild %q: %w", guildID, err)
	}
	if out.RoleIDs == nil {
		out.RoleIDs = []string{}
	}
	return out, nil
}

// SaveRecordingRoles implements Store.
func (p *Postgres) SaveRecordingRoles(ctx context.Context, guildID string, roles RecordingRoles, entry ChangeLogEntry) error {
	_, err := p.saveWithEntry(ctx, entry, func(tx *sql.Tx) (int64, error) {
		return 0, setRecordingRoles(ctx, tx, guildID, roles)
	})
	if err != nil {
		return fmt.Errorf("save recording roles of guild %q: %w", guildID, err)
	}
	return nil
}

// setRecordingRoles writes the guild's recording roles row only while it
// is at roles.Version, as setGuildModeratorRoles writes the guild settings
// row. Nothing written is ErrStale.
func setRecordingRoles(ctx context.Context, tx *sql.Tx, guildID string, roles RecordingRoles) error {
	rolesJSON, err := encodeRoles(roles.RoleIDs)
	if err != nil {
		return err
	}
	var res sql.Result
	if roles.Version == 0 {
		res, err = tx.ExecContext(ctx, `
			INSERT INTO recording_roles (guild_id, role_ids, version)
			VALUES ($1, $2, 1)
			ON CONFLICT (guild_id) DO NOTHING`,
			guildID, rolesJSON)
	} else {
		res, err = tx.ExecContext(ctx, `
			UPDATE recording_roles SET role_ids = $2, version = version + 1
			WHERE guild_id = $1 AND version = $3`,
			guildID, rolesJSON, roles.Version)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrStale
	}
	return nil
}

// insertChange appends one change log entry. A zero HubID is stored as
// NULL, the same as a spawned row's cleared reference. The diff goes in as
// JSONB, so bytes that are not a JSON value are refused here.
func insertChange(ctx context.Context, tx *sql.Tx, e ChangeLogEntry) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO change_log (hub_id, forum_user_id, forum_username, action, diff)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		sql.NullInt64{Int64: e.HubID, Valid: e.HubID != 0},
		e.ForumUserID, e.ForumUsername, string(e.Action), []byte(e.Diff)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("append change log entry for hub %d: %w", e.HubID, err)
	}
	return id, nil
}

// scanChangeLogEntry reads one row: id, hub_id, forum_user_id,
// forum_username, at, action, diff. A NULL hub reads back as zero.
func scanChangeLogEntry(row scanner) (ChangeLogEntry, error) {
	var (
		e     ChangeLogEntry
		hubID sql.NullInt64
		diff  []byte
	)
	if err := row.Scan(&e.ID, &hubID, &e.ForumUserID, &e.ForumUsername, &e.At, &e.Action, &diff); err != nil {
		return ChangeLogEntry{}, err
	}
	e.HubID = hubID.Int64
	e.Diff = json.RawMessage(diff)
	return e, nil
}

// ListChangeLog implements Store. Newest first is descending ID: the ID is
// the append order, and the time is not, since two appends can share a
// timestamp.
func (p *Postgres) ListChangeLog(ctx context.Context, hubID int64, limit int) ([]ChangeLogEntry, error) {
	const columns = `id, hub_id, forum_user_id, forum_username, at, action, diff`
	var (
		entries []ChangeLogEntry
		err     error
	)
	if hubID == 0 {
		entries, err = queryAll(ctx, p.db, scanChangeLogEntry,
			`SELECT `+columns+` FROM change_log WHERE hub_id IS NULL ORDER BY id DESC LIMIT $1`, limit)
	} else {
		entries, err = queryAll(ctx, p.db, scanChangeLogEntry,
			`SELECT `+columns+` FROM change_log WHERE hub_id = $1 ORDER BY id DESC LIMIT $2`, hubID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list change log of hub %d: %w", hubID, err)
	}
	return entries, nil
}

// ListModeratorChanges implements Store.
func (p *Postgres) ListModeratorChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error) {
	entries, err := queryAll(ctx, p.db, scanChangeLogEntry,
		`SELECT id, hub_id, forum_user_id, forum_username, at, action, diff
		 FROM change_log WHERE hub_id IS NULL AND action = $1 ORDER BY id DESC LIMIT $2`,
		string(ChangeModerators), limit)
	if err != nil {
		return nil, fmt.Errorf("list moderator changes: %w", err)
	}
	return entries, nil
}

// ListRecordingRoleChanges implements Store.
func (p *Postgres) ListRecordingRoleChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error) {
	entries, err := queryAll(ctx, p.db, scanChangeLogEntry,
		`SELECT id, hub_id, forum_user_id, forum_username, at, action, diff
		 FROM change_log WHERE hub_id IS NULL AND action = $1 ORDER BY id DESC LIMIT $2`,
		string(ChangeRecordingRoles), limit)
	if err != nil {
		return nil, fmt.Errorf("list recording roles changes: %w", err)
	}
	return entries, nil
}

// StartRecording implements Store.
func (p *Postgres) StartRecording(ctx context.Context, rec Recording) (Recording, error) {
	rec.StoppedAt, rec.Ended, rec.Mix, rec.Speakers = time.Time{}, "", "", nil
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO recordings (guild_id, channel_id, channel_name, starter_id, title, recorder_id, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		rec.GuildID, rec.ChannelID, rec.ChannelName, rec.StarterID, rec.Title, rec.RecorderID, rec.StartedAt).Scan(&rec.ID)
	if err != nil {
		return Recording{}, fmt.Errorf("start recording of channel %q: %w", rec.ChannelID, err)
	}
	return rec, nil
}

// StopRecording implements Store. No running row matched is ErrNotFound.
func (p *Postgres) StopRecording(ctx context.Context, id int64, at time.Time, end RecordingEnd, speakers []Speaker) error {
	speakersJSON, err := json.Marshal(storedSpeakers(speakers))
	if err != nil {
		return fmt.Errorf("stop recording %d: %w", id, err)
	}
	res, err := p.db.ExecContext(ctx, `
		UPDATE recordings SET stopped_at = $2, ended = $3, mix = $4, speakers = $5
		WHERE id = $1 AND stopped_at IS NULL`,
		id, at, string(end), string(MixProcessing), speakersJSON)
	if err != nil {
		return fmt.Errorf("stop recording %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("stop recording %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("stop recording %d: %w", id, ErrNotFound)
	}
	return nil
}

// SetRecordingMix implements Store. No row matched is ErrNotFound.
func (p *Postgres) SetRecordingMix(ctx context.Context, id int64, mix MixState) error {
	res, err := p.db.ExecContext(ctx, `UPDATE recordings SET mix = $2 WHERE id = $1`, id, string(mix))
	if err != nil {
		return fmt.Errorf("set the mix of recording %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set the mix of recording %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("set the mix of recording %d: %w", id, ErrNotFound)
	}
	return nil
}

// storedSpeaker is a speaker as the speakers column holds it: one object
// of a JSON array.
type storedSpeaker struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// storedSpeakers is the speakers column's value for a stop's speakers:
// always an array, never null.
func storedSpeakers(speakers []Speaker) []storedSpeaker {
	out := make([]storedSpeaker, 0, len(speakers))
	for _, sp := range speakers {
		out = append(out, storedSpeaker(sp))
	}
	return out
}

// ListRecordings implements Store.
func (p *Postgres) ListRecordings(ctx context.Context, guildID string) ([]Recording, error) {
	recs, err := queryAll(ctx, p.db, func(row scanner) (Recording, error) {
		var (
			rec          Recording
			stopped      sql.NullTime
			ended, mix   sql.NullString
			speakersJSON []byte
		)
		if err := row.Scan(&rec.ID, &rec.GuildID, &rec.ChannelID, &rec.ChannelName, &rec.StarterID,
			&rec.Title, &rec.RecorderID, &rec.StartedAt, &stopped, &ended, &mix, &speakersJSON); err != nil {
			return rec, err
		}
		rec.StoppedAt, rec.Ended, rec.Mix = stopped.Time, RecordingEnd(ended.String), MixState(mix.String)
		var speakers []storedSpeaker
		if err := json.Unmarshal(speakersJSON, &speakers); err != nil {
			return rec, fmt.Errorf("speakers of recording %d: %w", rec.ID, err)
		}
		for _, sp := range speakers {
			rec.Speakers = append(rec.Speakers, Speaker(sp))
		}
		return rec, nil
	}, `SELECT id, guild_id, channel_id, channel_name, starter_id, title, recorder_id, started_at, stopped_at, ended,
			mix, speakers
		FROM recordings WHERE guild_id = $1`, guildID)
	if err != nil {
		return nil, fmt.Errorf("list recordings of guild %q: %w", guildID, err)
	}
	return recs, nil
}

// ListFoxholeRecords implements Store.
func (p *Postgres) ListFoxholeRecords(ctx context.Context, guildID string) ([]FoxholeRecord, error) {
	members, err := queryAll(ctx, p.db, func(row scanner) (FoxholeRecord, error) {
		var m FoxholeRecord
		err := row.Scan(&m.MemberID, &m.Note, &m.Approved, &m.DisplayName, &m.Username)
		return m, err
	}, `SELECT member_id, note, approved, last_display_name, last_username
		FROM foxhole_records WHERE guild_id = $1 ORDER BY member_id`, guildID)
	if err != nil {
		return nil, fmt.Errorf("list Foxhole records of guild %q: %w", guildID, err)
	}
	return members, nil
}

// SaveFoxholeNote implements Store.
func (p *Postgres) SaveFoxholeNote(ctx context.Context, guildID string, save NoteSave, entry ChangeLogEntry) error {
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		if err := writeNote(ctx, tx, guildID, save); err != nil {
			return err
		}
		// A record holds a note or an approval. One left with neither goes.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM foxhole_records WHERE guild_id = $1 AND member_id = $2 AND note = '' AND NOT approved`,
			guildID, save.MemberID); err != nil {
			return err
		}
		return insertFoxholeChange(ctx, tx, entry)
	})
	if err != nil {
		return fmt.Errorf("save Foxhole note of member %q: %w", save.MemberID, err)
	}
	return nil
}

// writeNote writes the member's note and names only while the stored note
// is save.Before, a member with no record holding the empty note. The row
// is locked from the read to the write, so no other save lands between
// them. A save that starts a record inserts it only while no other save has,
// since a missing row locks nothing. Nothing written is ErrStale.
func writeNote(ctx context.Context, tx *sql.Tx, guildID string, save NoteSave) error {
	var stored string
	err := tx.QueryRowContext(ctx, `
		SELECT note FROM foxhole_records WHERE guild_id = $1 AND member_id = $2 FOR UPDATE`,
		guildID, save.MemberID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		if save.Before != "" {
			return ErrStale
		}
		return insertNote(ctx, tx, guildID, save)
	}
	if err != nil {
		return err
	}
	if stored != save.Before {
		return ErrStale
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE foxhole_records SET note = $3, last_display_name = $4, last_username = $5, updated_at = now()
		WHERE guild_id = $1 AND member_id = $2`,
		guildID, save.MemberID, save.Note, save.DisplayName, save.Username)
	return err
}

// insertNote starts the member's record with the save's note and names.
// A record another save started first stops it, and nothing written is
// ErrStale.
func insertNote(ctx context.Context, tx *sql.Tx, guildID string, save NoteSave) error {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO foxhole_records (guild_id, member_id, note, last_display_name, last_username)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (guild_id, member_id) DO NOTHING`,
		guildID, save.MemberID, save.Note, save.DisplayName, save.Username)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrStale
	}
	return nil
}

// ApproveFoxholeMembers implements Store. The records go in before the
// entry, in one transaction.
func (p *Postgres) ApproveFoxholeMembers(ctx context.Context, guildID string, members []MemberNames, entry ChangeLogEntry) error {
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		for _, n := range members {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO foxhole_records (guild_id, member_id, approved, last_display_name, last_username)
				VALUES ($1, $2, TRUE, $3, $4)
				ON CONFLICT (guild_id, member_id) DO UPDATE SET approved = TRUE,
					last_display_name = EXCLUDED.last_display_name, last_username = EXCLUDED.last_username, updated_at = now()`,
				guildID, n.MemberID, n.DisplayName, n.Username); err != nil {
				return err
			}
		}
		return insertFoxholeChange(ctx, tx, entry)
	})
	if err != nil {
		return fmt.Errorf("approve Foxhole members of guild %q: %w", guildID, err)
	}
	return nil
}

// ClearFoxholeApprovals implements Store. The records go in before the
// entry, in one transaction.
func (p *Postgres) ClearFoxholeApprovals(ctx context.Context, guildID string, memberIDs []string, entry ChangeLogEntry) error {
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range memberIDs {
			if _, err := clearApproval(ctx, tx, guildID, id); err != nil {
				return err
			}
		}
		return insertFoxholeChange(ctx, tx, entry)
	})
	if err != nil {
		return fmt.Errorf("clear Foxhole approvals of guild %q: %w", guildID, err)
	}
	return nil
}

// ClearFoxholeApprovalForRemoval implements Store. The clear and the drop
// of a record left with neither a note nor an approval run in one
// transaction.
func (p *Postgres) ClearFoxholeApprovalForRemoval(ctx context.Context, guildID, memberID string) (bool, error) {
	var cleared bool
	err := p.inTx(ctx, func(tx *sql.Tx) (err error) {
		cleared, err = clearApproval(ctx, tx, guildID, memberID)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("clear the Foxhole approval of member %q: %w", memberID, err)
	}
	return cleared, nil
}

// clearApproval clears the member's approval, when they have one, drops
// their record when that leaves it with no note, and reports whether they
// had one.
func clearApproval(ctx context.Context, tx *sql.Tx, guildID, memberID string) (bool, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE foxhole_records SET approved = FALSE, updated_at = now()
		WHERE guild_id = $1 AND member_id = $2 AND approved`, guildID, memberID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	// A record holds a note or an approval. One left with neither goes.
	_, err = tx.ExecContext(ctx, `
		DELETE FROM foxhole_records WHERE guild_id = $1 AND member_id = $2 AND note = ''`, guildID, memberID)
	return err == nil, err
}

// SetFoxholeRecordNames implements Store. The few updates run in one
// transaction, so a refresh lands whole or not at all.
func (p *Postgres) SetFoxholeRecordNames(ctx context.Context, guildID string, names []MemberNames) error {
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		for _, n := range names {
			if _, err := tx.ExecContext(ctx, `
				UPDATE foxhole_records SET last_display_name = $3, last_username = $4
				WHERE guild_id = $1 AND member_id = $2`,
				guildID, n.MemberID, n.DisplayName, n.Username); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("set Foxhole record names of guild %q: %w", guildID, err)
	}
	return nil
}

// insertFoxholeChange appends one entry to the Foxhole change log. The diff
// goes in as JSONB, so bytes that are not a JSON value are refused here.
func insertFoxholeChange(ctx context.Context, tx *sql.Tx, e ChangeLogEntry) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO foxhole_change_log (forum_user_id, forum_username, action, diff)
		VALUES ($1, $2, $3, $4)`,
		e.ForumUserID, e.ForumUsername, string(e.Action), []byte(e.Diff))
	if err != nil {
		return fmt.Errorf("append Foxhole change log entry: %w", err)
	}
	return nil
}

// ListFoxholeChanges implements Store. Newest first is descending ID, as in
// ListChangeLog.
func (p *Postgres) ListFoxholeChanges(ctx context.Context, limit int) ([]ChangeLogEntry, error) {
	entries, err := queryAll(ctx, p.db, scanFoxholeChange,
		`SELECT id, forum_user_id, forum_username, at, action, diff
		FROM foxhole_change_log ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list Foxhole changes: %w", err)
	}
	return entries, nil
}

// scanFoxholeChange reads one row of the Foxhole change log: id,
// forum_user_id, forum_username, at, action, diff.
func scanFoxholeChange(row scanner) (ChangeLogEntry, error) {
	var (
		e    ChangeLogEntry
		diff []byte
	)
	err := row.Scan(&e.ID, &e.ForumUserID, &e.ForumUsername, &e.At, &e.Action, &diff)
	e.Diff = json.RawMessage(diff)
	return e, err
}

// StartFoxholeReport implements Store. The diff goes in as JSONB, so bytes
// that are not a JSON value are refused here.
func (p *Postgres) StartFoxholeReport(ctx context.Context, entry ChangeLogEntry) (ChangeLogEntry, error) {
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO foxhole_change_log (forum_user_id, forum_username, action, diff, report)
		VALUES ($1, $2, $3, $4, 'running')
		RETURNING id, at`,
		entry.ForumUserID, entry.ForumUsername, string(entry.Action), []byte(entry.Diff)).Scan(&entry.ID, &entry.At)
	if err != nil {
		return ChangeLogEntry{}, fmt.Errorf("start Foxhole report: %w", err)
	}
	return entry, nil
}

// UpdateFoxholeReport implements Store.
func (p *Postgres) UpdateFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	return p.writeReport(ctx, id, diff, "running")
}

// EndFoxholeReport implements Store.
func (p *Postgres) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	return p.writeReport(ctx, id, diff, "ended")
}

// writeReport replaces the diff of the running report with the ID given and
// sets its report state to state, in one statement. No row matched is
// ErrNotFound.
func (p *Postgres) writeReport(ctx context.Context, id int64, diff json.RawMessage, state string) error {
	res, err := p.db.ExecContext(ctx, `
		UPDATE foxhole_change_log SET diff = $2, report = $3 WHERE id = $1 AND report = 'running'`,
		id, []byte(diff), state)
	if err != nil {
		return fmt.Errorf("write Foxhole report %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("write Foxhole report %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RunningFoxholeReports implements Store.
func (p *Postgres) RunningFoxholeReports(ctx context.Context) ([]ChangeLogEntry, error) {
	entries, err := queryAll(ctx, p.db, scanFoxholeChange,
		`SELECT id, forum_user_id, forum_username, at, action, diff
		FROM foxhole_change_log WHERE report = 'running' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list running Foxhole reports: %w", err)
	}
	return entries, nil
}

// LastFoxholeReport implements Store.
func (p *Postgres) LastFoxholeReport(ctx context.Context) (FoxholeReport, error) {
	report, err := scanFoxholeReport(p.db.QueryRowContext(ctx, `
		SELECT id, forum_user_id, forum_username, at, action, diff, report = 'running'
		FROM foxhole_change_log WHERE report IS NOT NULL ORDER BY id DESC LIMIT 1`))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return FoxholeReport{}, fmt.Errorf("read the last Foxhole report: %w", err)
	}
	return report, err
}

// FoxholeReport implements Store.
func (p *Postgres) FoxholeReport(ctx context.Context, id int64) (FoxholeReport, error) {
	report, err := scanFoxholeReport(p.db.QueryRowContext(ctx, `
		SELECT id, forum_user_id, forum_username, at, action, diff, report = 'running'
		FROM foxhole_change_log WHERE id = $1 AND report IS NOT NULL`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return FoxholeReport{}, fmt.Errorf("read Foxhole report %d: %w", id, err)
	}
	return report, err
}

// scanFoxholeReport reads the one report a query selects: id,
// forum_user_id, forum_username, at, action, diff, and whether it runs. No
// row is ErrNotFound.
func scanFoxholeReport(row *sql.Row) (FoxholeReport, error) {
	var (
		report FoxholeReport
		diff   []byte
	)
	e := &report.Entry
	err := row.Scan(&e.ID, &e.ForumUserID, &e.ForumUsername, &e.At, &e.Action, &diff, &report.Running)
	if errors.Is(err, sql.ErrNoRows) {
		return FoxholeReport{}, ErrNotFound
	}
	if err != nil {
		return FoxholeReport{}, err
	}
	e.Diff = json.RawMessage(diff)
	return report, nil
}
