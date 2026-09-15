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
	// the healthcheck, so this covers only the Watchtower recreate case.
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
// database (a Watchtower recreate that overlaps the old container) cannot
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
const hubColumns = `id, guild_id, hub_channel_id, base_string, permission_source,
	moderator_role_ids, user_limit, bitrate, enabled, created_at, updated_at`

// scanHub reads one hub row in hubColumns order.
func scanHub(row scanner) (Hub, error) {
	var (
		h     Hub
		roles []byte
	)
	err := row.Scan(&h.ID, &h.GuildID, &h.HubChannelID, &h.BaseString, &h.PermissionSource,
		&roles, &h.UserLimit, &h.Bitrate, &h.Enabled, &h.CreatedAt, &h.UpdatedAt)
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

// UpsertHub implements Store. The row is keyed on hub_channel_id: a conflict
// updates the settings in place and keeps the row's ID and created_at.
func (p *Postgres) UpsertHub(ctx context.Context, hub Hub) (Hub, error) {
	roles := hub.ModeratorRoleIDs
	if roles == nil {
		roles = []string{}
	}
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		return Hub{}, fmt.Errorf("encode moderator role IDs: %w", err)
	}
	row := p.db.QueryRowContext(ctx, `
		INSERT INTO hubs (guild_id, hub_channel_id, base_string, permission_source,
			moderator_role_ids, user_limit, bitrate, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (hub_channel_id) DO UPDATE SET
			guild_id = EXCLUDED.guild_id,
			base_string = EXCLUDED.base_string,
			permission_source = EXCLUDED.permission_source,
			moderator_role_ids = EXCLUDED.moderator_role_ids,
			user_limit = EXCLUDED.user_limit,
			bitrate = EXCLUDED.bitrate,
			enabled = EXCLUDED.enabled,
			updated_at = now()
		RETURNING `+hubColumns,
		hub.GuildID, hub.HubChannelID, hub.BaseString, string(hub.PermissionSource),
		rolesJSON, hub.UserLimit, hub.Bitrate, hub.Enabled)
	stored, err := scanHub(row)
	if err != nil {
		return Hub{}, fmt.Errorf("upsert hub %q: %w", hub.HubChannelID, err)
	}
	return stored, nil
}

// DeleteHub implements Store. The spawned rows' hub reference clears through
// the foreign key's ON DELETE SET NULL, so nothing here touches them.
func (p *Postgres) DeleteHub(ctx context.Context, id int64) error {
	if _, err := p.db.ExecContext(ctx, `DELETE FROM hubs WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete hub %d: %w", id, err)
	}
	return nil
}

// UpsertSpawnedChannel implements Store. The row is keyed on channel_id: a
// conflict updates hub, number and owner in place and keeps created_at, which
// is what the handover write relies on. A zero HubID and an empty OwnerUserID
// are stored as NULL.
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

// DeleteSpawnedChannel implements Store.
func (p *Postgres) DeleteSpawnedChannel(ctx context.Context, channelID string) error {
	if _, err := p.db.ExecContext(ctx, `DELETE FROM spawned_channels WHERE channel_id = $1`, channelID); err != nil {
		return fmt.Errorf("delete spawned channel %q: %w", channelID, err)
	}
	return nil
}

// scanSpawnedChannel reads one spawned channel row: channel_id, hub_id,
// number, owner_user_id, created_at. NULL hub and owner read back as zero and
// empty.
func scanSpawnedChannel(row scanner) (SpawnedChannel, error) {
	var (
		sc    SpawnedChannel
		hubID sql.NullInt64
		owner sql.NullString
	)
	if err := row.Scan(&sc.ChannelID, &hubID, &sc.Number, &owner, &sc.CreatedAt); err != nil {
		return SpawnedChannel{}, err
	}
	sc.HubID = hubID.Int64
	sc.OwnerUserID = owner.String
	return sc, nil
}

// ListSpawnedChannels implements Store.
func (p *Postgres) ListSpawnedChannels(ctx context.Context) ([]SpawnedChannel, error) {
	out, err := queryAll(ctx, p.db, scanSpawnedChannel, `
		SELECT channel_id, hub_id, number, owner_user_id, created_at
		FROM spawned_channels ORDER BY channel_id`)
	if err != nil {
		return nil, fmt.Errorf("list spawned channels: %w", err)
	}
	return out, nil
}
