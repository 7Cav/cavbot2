# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go Discord bot (module `github.com/7cav/cavbot2`) for the 7th Cavalry Gaming Regiment, built on `bwmarrin/discordgo`. It exposes slash commands that integrate with the 7Cav milpacs API and the Xenforo forum MySQL DB.

## Common commands

```bash
go build -o cavbot2 .                     # build the binary (CI uses this exact command)
go run .                                  # run locally (requires env vars; see below)
go mod tidy                               # sync deps after changing imports
golangci-lint run --timeout=5m            # lint (no .golangci config; uses defaults — same as CI)
docker build -t cavbot2:latest . && docker compose up   # run via Docker (see README step 4)
```

Tests live in `*_test.go` files alongside the code they cover. Run them with `go test ./...`. CI runs the suite with coverage enforcement — see the Testing section of README.md and `.github/scripts/check-coverage-floors.sh` for the floor policy.

The `store` package's tests run against a real Postgres named by `TEST_BOT_DB_DSN` and skip when it is unset, so `go test ./...` passes with no database but the store coverage floor does not. To match CI locally:

```bash
docker run --rm -d --name cavbot2-test-pg -e POSTGRES_PASSWORD=postgres -p 5433:5432 postgres:18-alpine
export TEST_BOT_DB_DSN='postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable'
```

The tests drop and recreate the `public` schema of that database before every case. Point the variable at a throwaway server only.

## Required environment variables

`init()` in `main.go` panics if any of these are missing:

- `DISCORD_TOKEN`, `GUILD_ID`, `BM_TOKEN`

Optional but feature-gating:

- `BEARER` — bearer token for `api.7cav.us`. Milpacs lookups fail without it, and the startup rank ladder check logs one WARN and skips.
- `FORUM_DB_DSN` — MySQL DSN for the Xenforo forum DB. The production DSN points at host `xenforo-db`, which resolves **only inside the `xenforo_internal` Docker network** (declared `external: true` in `docker-compose.yml`). Running `go run .` outside that network won't error at startup — `sql.Open` defers the connection — but every `Refresh` will log `"LOA cache refresh failed"` at WARN and the cache will stay empty. To test LOA locally, either run via `docker compose` or substitute a reachable DSN.
- `LOA_NODE_IDS` — comma-separated Xenforo node IDs to scan for LOA threads. Code default is `180`; **production scans 5 nodes** (`180,400,540,178,369`), which is what `.env.example` sets, so a copied `.env` never hits the code default.
- `WARDEN_ROLE_BASE_NAME` — base name the `/warden` roles are composed from (`<base> Internal`, `<base> External`). Code default is `Verified Warden`; **`.env.example` sets what the roles are named in Discord now** (`Verified Foxhole`), so a copied `.env` never hits the code default — same arrangement as `LOA_NODE_IDS`. Matching is **exact**: the configured base has to reproduce the role name character for character, and a value that doesn't fails every warden subcommand with `role not found` and changes nothing. Read per invocation, so it takes effect on restart with no rebuild. The resolved value is logged at startup (`Warden role base name resolved`), which is the fastest way to confirm it reached the process.
- `BOT_DB_DSN` is the Postgres DSN for the bot's own store (`store/`), the hubs, spawned channels and guild settings of the temporary voice channel feature. Unset: one WARN at startup (`BOT_DB_DSN not set, bot store disabled`), no store, every command works as before, and `/voice-rename` is registered but refuses every invocation (it stays registered so its Server Settings restriction, which Discord keys by command ID, survives a store-less start). Set: the bot pings it with a short retry, runs the embedded migrations, loads the hub rows and the guild-wide moderator roles into the temporary voice channel runtime (`Starting temp voice channels` with the count) and registers its gateway handlers, all before it opens the Discord session. With no hub rows nothing spawns; a hub row is what turns the feature on. A database it cannot reach or migrate stops the bot with `Bot store unavailable` and a non-zero exit, and `restart: unless-stopped` retries. The DSN is never logged, and a DSN that does not parse is reported without echoing it. `.env.example` leaves it empty so a copied `.env` stays inert; the compose value is in its comment. See ADR 0012.
- `PANEL_ADDR` and the other `PANEL_*` variables configure the panel, the bot's web UI in `panel/`. The decision to run it inside the bot is recorded in `docs/temp-vc-decisions.md`. Unset `PANEL_ADDR`: one WARN at startup (`PANEL_ADDR not set, panel disabled`), no listener, every command works as before. Set: the bot reads `PANEL_BASE_URL`, `PANEL_OAUTH_CLIENT_ID`, `PANEL_OAUTH_CLIENT_SECRET`, `PANEL_OAUTH_AUTHORIZE_URL`, `PANEL_OAUTH_TOKEN_URL` and `PANEL_OAUTH_USERINFO_URL` before the Discord session opens and stops with `Panel misconfigured` naming the first one missing; `PANEL_GROUP_IDS` defaults to `71,47,44` and `.env.example` sets the same. The panel also needs `BOT_DB_DSN`: its hub page reads the store on every load, so `PANEL_ADDR` set with `BOT_DB_DSN` empty means one more WARN (`BOT_DB_DSN not set, panel disabled`) and no listener. The listener starts after READY and logs `Panel listening` with the address, the base URL and the group allowlist. The client secret is never logged. The compose value for `PANEL_ADDR` is `:8080`, reached over the external `edge` network and never published on the host.
- `LOG_LEVEL` — `DEBUG` / `INFO` / `WARN` / `ERROR` (default `INFO`)
- `DISCORDGO_LOG_LEVEL` — discordgo's *own* gateway logging, separate from `LOG_LEVEL`. `ERROR` (default) / `WARN` / `INFO` / `DEBUG`; unrecognised values mean `ERROR`. `WARN` is where discordgo names the frame the gateway sent instead of `READY`, so it is the level to set when startup fails with `Discord session unavailable`. Levels map faithfully onto the slog wrappers, so both gates apply: discordgo's debug output needs `LOG_LEVEL=DEBUG` as well. At `WARN` and above discordgo logs every gateway event it does not recognise with the event's full payload.

`.env.example` lists all of these. Copy it to `.env` — `init()` in `main.go` loads that file via `godotenv` before reading any variable, so it serves local `go run .` and `docker compose` alike. Anything already exported in the environment takes precedence over the file, and a missing file is not an error (that is the container and CI case). A malformed one panics rather than starting with partial configuration.

## Architecture

### External integrations

- **7Cav API** (`utils/milpacs.go`): generic `makeAPIRequest[T]` against `https://api.7cav.us/api/v1/`, auth via `BEARER`. Use this for any new milpac/profile lookup rather than rolling your own resty client.
- **Xenforo MySQL**: read-only queries against `xf_thread` / `xf_post`. Connection pool deliberately tiny (`SetMaxOpenConns(2)`) since this is a low-rate background scan.
- **Bot Postgres** (`store/`): the `postgres` service in `docker-compose.yml`, reached through `BOT_DB_DSN`. The only database the bot writes to.

## Versioning & deploy

`Version` is injected at Docker build via ldflags (see ADR 0003). Local `go build` / `go run` show `dev`.

## Conventions worth knowing

- Logging is `slog` via `utils.Info/Warn/Error/Debug` — always use these wrappers, not the stdlib `slog` directly, so log level routing stays consistent.
- Commands log a `"🚀 Starting ..."` line at entry and `"✨ Done!"` at successful exit, with `"command"`, `"username"`, and `"discord_id"` fields. Match this pattern when adding commands so log greps stay uniform.
- Prefer ephemeral responses for admin/management commands (`MessageFlagsEphemeral`) — see `warden.go` for the deferred-ephemeral pattern (`deferEphemeral` + `editEphemeral`).

## Build/deploy quirks

### Docker compose `environment:` allowlist vs `.env` (2026-05-14)

The compose service uses an explicit `environment:` block listing each variable as `KEY: ${KEY}`. **Only those keys reach the container.** Adding a var to `.env` alone (without a matching compose-block line) means `.env` feeds compose's interpolator but the var never propagates into the running process — the bot will log `"Sentry disabled (SENTRY_DSN not set)"` despite the value being in `.env`.

## See also (durable docs)

For load-bearing decisions, see `docs/adr/`. For domain terminology, see `CONTEXT.md`.

## Agent skills

### Issue tracker

GitHub Issues at `7cav/cavbot2` (`gh` CLI). See `docs/agents/issue-tracker.md`.

### Triage labels

Canonical names used verbatim (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `CONTEXT.md` + `docs/adr/` at repo root. See `docs/agents/domain.md`.

## CI gate

```bash
set -euo pipefail
golangci-lint run --timeout=5m
go mod tidy
if [[ -z "${TEST_BOT_DB_DSN:-}" ]]; then
  docker rm -f cavbot2-gate-pg >/dev/null 2>&1 || true
  docker run --rm -d --name cavbot2-gate-pg -e POSTGRES_PASSWORD=postgres -p 5433:5432 postgres:18-alpine >/dev/null
  trap 'docker rm -f cavbot2-gate-pg >/dev/null 2>&1 || true' EXIT
  until docker exec cavbot2-gate-pg pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
  export TEST_BOT_DB_DSN='postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable'
fi
go test ./... -race -cover -covermode=atomic | tee /tmp/cover.log
./.github/scripts/check-coverage-floors.sh < /tmp/cover.log
go build -o cavbot2
```

## Branch naming

`<type>/<issue-N>-<short-slug>` — type is `feat|fix|chore|docs|test|refactor`, matching the commit prefix. Example: `feat/65-roster-search-hint`.

## Commit prefix

Conventional Commits with optional scope: `<type>(<scope>): <subject>`. Scope is the touched directory or feature (`commands`, `utils`, `loa`, etc.). Subject in imperative mood, lowercase first letter.

## Manual review gates

- **Smoke test on the test guild** for any user-facing command behavior change. CI cannot exercise the live Discord gateway, but locally we can validate against a test guild.
- **Env-var parity check** when adding a new env var: both `.env.example` AND the `environment:` block in `docker-compose.yml` must list it (see "Docker compose `environment:` allowlist vs `.env`" quirk above).
