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

Review the README for required environment variables if you need them.

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

For load-bearing decisions, see `docs/adr/`. For domain terminology, see `CONTEXT.md`. For the production host, the Release checklist and the MEE6 cutover, see `docs/cutover-runbook.md`.

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

## Review gates

- **Smoke test on the test guild** for any user-facing command behavior change. CI cannot exercise the live Discord gateway, but locally we can validate against a test guild. This can be done by agents.
