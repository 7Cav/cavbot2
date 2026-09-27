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
// <timestamp>_<slug>.sql; goose refuses an unapplied file with a lower version
// than the highest applied one, so a PR that lands after another migration
// renumbers on rebase.
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
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if hub.ID == 0 {
			stored, err = insertHub(ctx, tx, hub)
		} else {
			stored, err = updateHub(ctx, tx, hub)
		}
		if err != nil {
			return err
		}
		entry.HubID = stored.ID
		return insertChange(ctx, tx, entry)
	})
	if err != nil {
		return Hub{}, fmt.Errorf("save hub %q: %w", hub.HubChannelID, err)
	}
	return stored, nil
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
func (p *Postgres) RemoveHub(ctx context.Context, id int64, entry ChangeLogEntry) error {
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		if err := deleteHub(ctx, tx, id); err != nil {
			return err
		}
		entry.HubID = 0
		return insertChange(ctx, tx, entry)
	})
	if err != nil {
		return fmt.Errorf("remove hub %d: %w", id, err)
	}
	return nil
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
	err := p.inTx(ctx, func(tx *sql.Tx) error {
		if err := setGuildModeratorRoles(ctx, tx, guildID, roles); err != nil {
			return err
		}
		entry.HubID = 0
		return insertChange(ctx, tx, entry)
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

// insertChange appends one change log entry. A zero HubID is stored as
// NULL, the same as a spawned row's cleared reference. The diff goes in as
// JSONB, so bytes that are not a JSON value are refused here.
func insertChange(ctx context.Context, tx *sql.Tx, e ChangeLogEntry) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO change_log (hub_id, forum_user_id, forum_username, action, diff)
		VALUES ($1, $2, $3, $4, $5)`,
		sql.NullInt64{Int64: e.HubID, Valid: e.HubID != 0},
		e.ForumUserID, e.ForumUsername, string(e.Action), []byte(e.Diff))
	if err != nil {
		return fmt.Errorf("append change log entry for hub %d: %w", e.HubID, err)
	}
	return nil
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
