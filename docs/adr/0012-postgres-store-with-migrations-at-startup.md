# ADR 0012: Postgres store with migrations at startup under a Watchtower deploy

## Status

Accepted (issue #286, spec #285).

## Context

The temporary voice channel feature needs settings that staff edit live and
rows that survive a restart: hubs, spawned channels, guild-wide moderator
roles, a change log. `docs/temp-vc-decisions.md` fixed the database as
Postgres in its own container in the bot's compose file, with its own volume,
so later tools share it and the bot's writes stay off the forum's server.

The deploy shape decides where migrations run. A GitHub Release pushes an
image and pings Watchtower, which pulls and restarts the bot container. Nobody
is at a terminal. Watchtower has no hook that can gate a rollout on a
migration result: a one-shot compose service sits exited and invisible to
every later release, and its lifecycle hooks cannot block an update and run
the old binary. The research behind this is in the `research/postgres-driver-migrations`
branch.

## Decision

- **Driver: `jackc/pgx/v5` through `database/sql`** via `pgx/stdlib`. Pure Go,
  so the `CGO_ENABLED=0` build stays. Same `*sql.DB` shape as the forum path,
  so goose takes the pool directly. Arrays scan through
  `pgtype.Map.SQLScanner` until the module moves to Go 1.27.
- **Migrations: `pressly/goose/v3` as a library**, SQL files embedded in the
  binary, applied at startup through a `goose.Provider` with the Postgres
  session locker, before the Discord session opens. Each file runs in its own
  transaction. A failed migration rolls back and ends the process non-zero;
  `restart: unless-stopped` retries on the next start. Nothing pending is not
  an error, so a redeploy against an existing volume is a no-op.
- **Every migration stays compatible with the previous release.** A rollback
  runs the older image against the newer schema, and goose only walks the
  versions its binary carries, so the older binary starts. It works only if
  the schema still fits it: additive changes (new table, new nullable column,
  new default) in the release that introduces them; drops and renames one
  release later, after every binary in the field stops reading the old shape.
- **The feature is inert without `BOT_DB_DSN`.** One WARN line, a nil store,
  and the bot runs as before. CI needs no Postgres to build.
- **`POSTGRES_PASSWORD` is the one variable in `.env.example` that is not in
  the compose `environment:` allowlist.** Compose reads it for the `postgres`
  service; the bot never reads it, and the same password rides inside
  `BOT_DB_DSN`. Putting it in the bot's environment would hand the process a
  secret it has no use for.
- **One interface, two implementations.** `store.Store` is the seam.
  `store.Postgres` is production; `store.Fake` is the in-memory
  implementation other packages' tests bind. The store package's contract
  suite runs against both, so the fake cannot drift from the database.
- **Store tests run against a real Postgres** from `TEST_BOT_DB_DSN`, and skip
  when it is unset. CI runs a Postgres service container. They assert round
  trips only: SQL text is an implementation detail and a driver mock would
  pin it.

## Why

golang-migrate was the alternative. Its failure state is a `dirty` flag a
human clears by hand with `force`, and its pgx driver runs each file with no
transaction of its own. Both are fine for a CLI a person runs, and wrong at
the top of `main()` at 03:00. tern is the pick if the store ever goes
pgx-native; it takes a `*pgx.Conn`, which the `database/sql` route would have
to unwrap.

Running migrations at startup makes the process the gate: no migration, no
bot. The cost is that a bad migration takes the bot down. That is the right
failure. A bot running against a schema it does not understand fails later
and less clearly.

## How to apply

- A new table or column goes in a new `store/migrations/<timestamp>_<name>.sql`
  file with `-- +goose Up` and `-- +goose Down` sections. Timestamp names;
  the second of two PRs that each add a file renumbers on rebase, or goose
  refuses the out-of-order version.
- Check the compatibility rule before writing a drop or rename: could the
  previous release's binary still run against this schema? If not, split it
  across two releases.
- A new store method arrives with the ticket that needs it, on the interface,
  on `Postgres`, on `Fake`, and in the contract suite.
- Run the store tests locally with the Docker one-liner in the README.
