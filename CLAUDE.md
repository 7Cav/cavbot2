# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go Discord bot (module `github.com/7cav/cavbot2`) for the 7th Cavalry Gaming Regiment, built on `bwmarrin/discordgo`. It exposes slash commands that integrate with the 7Cav milpacs API and the Xenforo forum MySQL DB.

## Common commands

```bash
.github/scripts/gate.sh                   # the CI gate (see "CI gate" below)
go build -o cavbot2 .                     # build the binary
go run .                                  # run locally (requires env vars; see below)
go mod tidy                               # sync deps after changing imports
golangci-lint run --timeout=5m            # lint (config in .golangci.yml)
docker build -t cavbot2:latest . && docker compose up   # run via Docker (see README step 4)
```

Tests live in `*_test.go` files alongside the code they cover. Run them with `go test ./...`. Coverage floors live in `.github/coverage-floors.tsv`, and ADR 0005 says when to change one.

The `store` package's tests run against a real Postgres named by `TEST_BOT_DB_DSN` and skip when it is unset. To run them, start a throwaway database and pass its DSN:

```bash
dsn=$(.github/scripts/test-db.sh)
TEST_BOT_DB_DSN=$dsn go test ./store/...
```

Each call starts a new container and prints its name on stderr, so reuse one DSN across commands and remove the container with `docker rm -f <name>` when done. The tests drop and recreate the `public` schema before every case, so the variable always names a database `test-db.sh` started.

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
- Prefer ephemeral responses for admin/management commands (`MessageFlagsEphemeral`) — see `deferEphemeral` + `editEphemeral` for the deferred-ephemeral pattern.

## Build/deploy quirks

### Docker compose `environment:` allowlist vs `.env` (2026-05-14)

The compose service uses an explicit `environment:` block listing each variable as `KEY: ${KEY}`. **Only those keys reach the container.** Adding a var to `.env` alone (without a matching compose-block line) means `.env` feeds compose's interpolator but the var never propagates into the running process — the bot will log `"Sentry disabled (SENTRY_DSN not set)"` despite the value being in `.env`.

## See also (durable docs)

For load-bearing decisions, see `docs/adr/`. For domain terminology, see `GLOSSARY.md`.

## Agent skills

### Issue tracker

GitHub Issues at `7cav/cavbot2` (`gh` CLI). See `docs/agents/issue-tracker.md`.

### Triage labels

Canonical names used verbatim (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `GLOSSARY.md` + `docs/adr/` at repo root. See `docs/agents/domain.md`.

## CI gate

Run `.github/scripts/gate.sh` before you push. CI's Build job runs the same script, and each run starts its own Postgres, so worktrees run it side by side. When its floor check prints RAISE, set those floors in the same PR (ADR 0005). The summary it ends with (commit, tree state, coverage per package) is the test evidence for a review of that commit.

## Review gates

- **Smoke test on the test guild** for any change a member or panel user can see: a slash command, a panel page, or what the bot does in Discord. The agent that made the change runs it with `go run ./tools/smoke` and reports it in the PR body, following `docs/smoke-test.md`. Panel sign-in goes through a local fake forum, so the agent completes it alone.
