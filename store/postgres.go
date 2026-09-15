package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// openRetryInterval is the pause between pings while Open waits for the
// database to answer.
const openRetryInterval = time.Second

// Open connects to the database at dsn and pings it until it answers or ctx
// ends. sql.Open alone never connects, so without the ping a bad DSN or a
// database mid-restart would only fail at the first query.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(30 * time.Second)
	for {
		pingErr := db.PingContext(ctx)
		if pingErr == nil {
			return db, nil
		}
		select {
		case <-ctx.Done():
			_ = db.Close()
			return nil, fmt.Errorf("database did not answer: %w (last ping: %w)", ctx.Err(), pingErr)
		case <-time.After(openRetryInterval):
		}
	}
}

// Migrate applies every embedded migration not yet in goose_db_version, under
// Postgres's session advisory lock so two starting processes never race.
// Nothing pending is not an error. The migration files ship inside the binary,
// so the Dockerfile copies nothing extra.
func Migrate(ctx context.Context, db *sql.DB) error {
	files, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

// Postgres is the production Store over a database/sql pool.
type Postgres struct {
	db *sql.DB
	// types adapts Postgres arrays to sql.Scanner for Go versions before 1.27.
	types *pgtype.Map
}

// NewPostgres wraps an opened, migrated pool.
func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db, types: pgtype.NewMap()}
}

const hubColumns = "id, guild_id, hub_channel_id, base_string, permission_source, moderator_role_ids, user_limit, bitrate, enabled, created_at, updated_at"

// scanHub reads one hubs row in hubColumns order.
func (p *Postgres) scanHub(row interface{ Scan(...any) error }) (Hub, error) {
	var h Hub
	err := row.Scan(
		&h.ID, &h.GuildID, &h.HubChannelID, &h.BaseString, &h.PermissionSource,
		p.types.SQLScanner(&h.ModeratorRoleIDs), &h.UserLimit, &h.Bitrate, &h.Enabled,
		&h.CreatedAt, &h.UpdatedAt,
	)
	return h, err
}

func (p *Postgres) GetHub(ctx context.Context, id int64) (Hub, error) {
	row := p.db.QueryRowContext(ctx, "SELECT "+hubColumns+" FROM hubs WHERE id = $1", id)
	h, err := p.scanHub(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Hub{}, ErrNotFound
	}
	return h, err
}

func (p *Postgres) ListHubs(ctx context.Context, guildID string) ([]Hub, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT "+hubColumns+" FROM hubs WHERE guild_id = $1 ORDER BY id", guildID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var hubs []Hub
	for rows.Next() {
		h, err := p.scanHub(rows)
		if err != nil {
			return nil, err
		}
		hubs = append(hubs, h)
	}
	return hubs, rows.Err()
}

func (p *Postgres) UpsertHub(ctx context.Context, hub Hub) (Hub, error) {
	roles := hub.ModeratorRoleIDs
	if roles == nil {
		// pgx encodes a nil slice as NULL, and the column is NOT NULL.
		roles = []string{}
	}
	row := p.db.QueryRowContext(ctx, `
		INSERT INTO hubs (guild_id, hub_channel_id, base_string, permission_source, moderator_role_ids, user_limit, bitrate, enabled)
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
		hub.GuildID, hub.HubChannelID, hub.BaseString, string(hub.PermissionSource), roles,
		hub.UserLimit, hub.Bitrate, hub.Enabled,
	)
	return p.scanHub(row)
}

func (p *Postgres) DeleteHub(ctx context.Context, id int64) error {
	_, err := p.db.ExecContext(ctx, "DELETE FROM hubs WHERE id = $1", id)
	return err
}

const spawnedColumns = "channel_id, hub_id, number, owner_user_id, created_at"

// scanSpawned reads one spawned_channels row in spawnedColumns order. The two
// nullable columns map to a nil HubID and an empty OwnerUserID.
func scanSpawned(row interface{ Scan(...any) error }) (SpawnedChannel, error) {
	var (
		sc    SpawnedChannel
		hubID sql.NullInt64
		owner sql.NullString
	)
	if err := row.Scan(&sc.ChannelID, &hubID, &sc.Number, &owner, &sc.CreatedAt); err != nil {
		return SpawnedChannel{}, err
	}
	if hubID.Valid {
		sc.HubID = &hubID.Int64
	}
	sc.OwnerUserID = owner.String
	return sc, nil
}

func (p *Postgres) UpsertSpawnedChannel(ctx context.Context, sc SpawnedChannel) error {
	var owner sql.NullString
	if sc.OwnerUserID != "" {
		owner = sql.NullString{String: sc.OwnerUserID, Valid: true}
	}
	var hubID sql.NullInt64
	if sc.HubID != nil {
		hubID = sql.NullInt64{Int64: *sc.HubID, Valid: true}
	}
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO spawned_channels (channel_id, hub_id, number, owner_user_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (channel_id) DO UPDATE SET
			hub_id = EXCLUDED.hub_id,
			number = EXCLUDED.number,
			owner_user_id = EXCLUDED.owner_user_id`,
		sc.ChannelID, hubID, sc.Number, owner,
	)
	return err
}

func (p *Postgres) DeleteSpawnedChannel(ctx context.Context, channelID string) error {
	_, err := p.db.ExecContext(ctx, "DELETE FROM spawned_channels WHERE channel_id = $1", channelID)
	return err
}

func (p *Postgres) ListSpawnedChannels(ctx context.Context) ([]SpawnedChannel, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT "+spawnedColumns+" FROM spawned_channels ORDER BY created_at, channel_id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SpawnedChannel
	for rows.Next() {
		sc, err := scanSpawned(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}
