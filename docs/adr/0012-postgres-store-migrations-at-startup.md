# ADR 0012: Postgres store with migrations at startup under a Watchtower deploy

## Status

Accepted (issue #286, from spec #285).

## Context

The temporary voice channel feature needs settings that survive a restart and
are edited live from a panel: hubs, and the spawned channels the runtime tracks
(`docs/temp-vc-decisions.md`, "Settled while charting"). The bot has no
database of its own; the forum's MySQL in `utils/loa.go` is read-only and not
ours to write to.

The deploy shape decides how the schema moves. A GitHub Release pushes an image
and pings Watchtower, which stops the container, pulls, and starts the new image
with the old options. Nobody is at a terminal, and nothing in that chain can run
a step before the new binary or refuse the rollout on that step's result
(`docs/research/postgres-driver-migrations.md` on the
`research/postgres-driver-migrations` branch works through the hooks and why
they do not fit).

## Decision

- **Postgres, in its own container in `docker-compose.yml`, with a named
  volume.** `postgres:18-alpine`, pinned to the major because a major changes
  the on-disk format. It carries the Watchtower exclude label so a bot release
  never restarts the database.
- **`jackc/pgx/v5` through `database/sql`.** Pure Go, so the `CGO_ENABLED=0`
  build is unchanged, and the store has the same `*sql.DB` shape as the forum
  path. The DSN is `BOT_DB_DSN`; unset means no store, one WARN, and every
  command works as before.
- **`pressly/goose/v3` as a library, SQL files embedded in the binary, run at
  process startup** through a provider with the Postgres session locker. The
  bot pings the database with a short retry, runs every pending migration, and
  only then opens the Discord session. A failed migration rolls back (goose
  wraps each file in a transaction) and the process exits non-zero; with
  `restart: unless-stopped` the next start retries. The process is the gate: no
  migration, no bot.
- **Every migration stays compatible with the previous release.** A rollback
  runs the older image against the newer schema, and the older binary applies
  nothing, since goose only walks the files it has. So a release adds tables and
  nullable columns only. A drop or a rename waits one release, after every
  running binary has stopped reading the old name. Nothing runs the `Down`
  sections: not the bot, not the tests. They are kept as the goose file
  convention for a maintainer's hand rollback and nothing else.

## Considered options

- **golang-migrate.** Rejected. A failed migration sets a dirty flag that a
  human clears by hand before the bot starts again, and its pgx driver runs a
  file as one statement with no transaction of its own. Both are wrong at the
  top of `main()` at 03:00.
- **A one-shot migrate service in compose.** Rejected. Watchtower recreates
  only the bot container, so the service would run on a manual `compose up`
  and never again.
- **A schema the bot checks but does not migrate.** Rejected. It moves the
  hand step to every release instead of removing it.

## How to apply

- New migration: one file in `store/migrations/`, named
  `<YYYYMMDDHHMMSS>_<slug>.sql`, with `-- +goose Up` and `-- +goose Down`
  sections. goose refuses an unapplied file with a lower version than the
  highest applied one, so a PR that lands after another migration renumbers on
  rebase.
- Additive in the release that introduces it. Drops and renames one release
  later, after the code stopped reading the old name.
- Nothing asserts SQL text. Store tests run against a real Postgres from
  `TEST_BOT_DB_DSN` and skip when it is unset; CI provides one. The test helper
  drops and recreates the schema and opens the store through `store.Open`, so
  a bad migration reddens every Postgres case and no separate migration test
  exists.
- A new env var goes in `.env.example` and the compose `environment:` block
  both, as before.
