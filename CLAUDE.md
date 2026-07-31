# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go Discord bot (module `github.com/7cav/cavbot2`) for the 7th Cavalry Gaming Regiment, built on `bwmarrin/discordgo`. It exposes slash commands that integrate with the 7Cav milpacs API, the Xenforo forum MySQL DB, and the GitHub Apps API.

## Common commands

```bash
go build -o cavbot2 .                     # build the binary (CI uses this exact command)
go run .                                  # run locally (requires env vars; see below)
go mod tidy                               # sync deps after changing imports
golangci-lint run --timeout=5m            # lint (no .golangci config; uses defaults — same as CI)
docker build -t cavbot2:latest . && docker compose up   # run via Docker (see README step 4)
```

Tests live in `*_test.go` files alongside the code they cover. Run them with `go test ./...`. CI runs the suite with coverage enforcement — see the Testing section of README.md and `.github/scripts/check-coverage-floors.sh` for the floor policy.

## Required environment variables

`init()` in `main.go` panics if any of these are missing:

- `DISCORD_TOKEN`, `GUILD_ID`, `BM_TOKEN`

Optional but feature-gating:

- `BEARER` — bearer token for `api.7cav.us` (milpacs lookups will fail without it)
- `FORUM_DB_DSN` — MySQL DSN for the Xenforo forum DB. The production DSN points at host `xenforo-db`, which resolves **only inside the `xenforo_internal` Docker network** (declared `external: true` in `docker-compose.yml`). Running `go run .` outside that network won't error at startup — `sql.Open` defers the connection — but every `Refresh` will log `"LOA cache refresh failed"` at WARN and the cache will stay empty. To test LOA locally, either run via `docker compose` or substitute a reachable DSN.
- `LOA_NODE_IDS` — comma-separated Xenforo node IDs to scan for LOA threads. Code default is `180`; **production scans 5 nodes** (`180,400,540,178,369`), which is what `.env.example` sets, so a copied `.env` never hits the code default.
- `GITHUB_APP_KEY` (base64-encoded PEM), `GITHUB_APP_CLIENT_ID` — needed for `/apps_beta_deploy`
- `LOG_LEVEL` — `DEBUG` / `INFO` / `WARN` / `ERROR` (default `INFO`)
- `DISCORDGO_LOG_LEVEL` — discordgo's *own* gateway logging, separate from `LOG_LEVEL`. `ERROR` (default) / `WARN` / `INFO` / `DEBUG`; unrecognised values mean `ERROR`. `WARN` is where discordgo names the frame the gateway sent instead of `READY`, so it is the level to set when startup fails with `Discord session unavailable`. Levels map faithfully onto the slog wrappers, so both gates apply: discordgo's debug output needs `LOG_LEVEL=DEBUG` as well. At `WARN` and above discordgo logs every gateway event it does not recognise with the event's full payload.

`.env.example` lists all of these. Copy it to `.env` — `init()` in `main.go` loads that file via `godotenv` before reading any variable, so it serves local `go run .` and `docker compose` alike. Anything already exported in the environment takes precedence over the file, and a missing file is not an error (that is the container and CI case). A malformed one panics rather than starting with partial configuration.

## Architecture

For command registration, interaction routing, and error-handling conventions, see `docs/adr/` (notably 0004, 0006, 0007). The notes below cover code areas without a dedicated ADR.

### LOA cache (utils/loa.go)

`utils.GlobalLOACache` is a process-global, mutex-guarded cache of forum LOA posts. `initLOACache()` in `main.go` does an initial refresh on startup, then runs `Refresh` every 15 minutes in a goroutine. Refresh is **incremental** — `lastSyncedPostDate` tracks the high-water mark and subsequent queries only pull newer posts. Entries whose `EndDate` has passed are pruned each refresh.

Parsing keys off the `Username`, `Start Date`, and `End Date` field labels. `parseLOAPost` first strips formatting-only BBCode (`[B]`, `[COLOR=…]`, `[SIZE=…]`, etc.) so label/value matching is agnostic to the post's bold/color/size wrapping — real posts vary widely (plain `[B]Start Date[/B]`, no formatting, non-yellow colors, `[SIZE]`-wrapped dates). Date values accept abbreviated or full month names, with or without the comma (`loaDateLayouts`). Posts that still don't match (free-text dates, `Start:`/`End:` label variants, dates only in the title) return `false` and are logged at DEBUG. If the forum changes the *label wording* itself, matching breaks silently — the regression cases in `loa_test.go` are seeded from real forum bodies to catch drift.

### External integrations

- **7Cav API** (`utils/milpacs.go`): generic `makeAPIRequest[T]` against `https://api.7cav.us/api/v1/`, auth via `BEARER`. Use this for any new milpac/profile lookup rather than rolling your own resty client.
- **GitHub Apps** (`utils/github.go`): `GithubAuth(clientID, pem)` → installation token. Used by `/apps_beta_deploy` to dispatch the `dev_deploy.yml` workflow on `7cav/adr`. The PEM key is read from env as base64 and decoded at use site.
- **Xenforo MySQL**: read-only queries against `xf_thread` / `xf_post`. Connection pool deliberately tiny (`SetMaxOpenConns(2)`) since this is a low-rate background scan.

## Versioning & deploy

`Version` is injected at Docker build via ldflags (see ADR 0003). Local `go build` / `go run` show `dev`.

- `build_and_push.yml` runs **only on GitHub Releases**, builds the Docker image tagged with the release ref (and injects the same ref as `Version`), pushes to Docker Hub as `7cav/cavbot2:<tag>` and `:latest`, then pings a Watchtower endpoint to force-pull on the prod host.
- `build_test.yml` runs lint + test (with coverage floor enforcement) + build on push/PR to `develop`. **Default branch is `develop`, not `main`.** PRs target `develop`.

## Conventions worth knowing

- Logging is `slog` via `utils.Info/Warn/Error/Debug` — always use these wrappers, not the stdlib `slog` directly, so log level routing stays consistent.
- Commands log a `"🚀 Starting ..."` line at entry and `"✨ Done!"` at successful exit, with `"command"`, `"username"`, and `"discord_id"` fields. Match this pattern when adding commands so log greps stay uniform.
- Prefer ephemeral responses for admin/management commands (`MessageFlagsEphemeral`) — see `warden.go` for the deferred-ephemeral pattern (`deferEphemeral` + `editEphemeral`).

## Build/deploy quirks

### Docker compose `environment:` allowlist vs `.env` (2026-05-14)

The compose service uses an explicit `environment:` block listing each variable as `KEY: ${KEY}`. **Only those keys reach the container.** Adding a var to `.env` alone (without a matching compose-block line) means `.env` feeds compose's interpolator but the var never propagates into the running process — the bot will log `"Sentry disabled (SENTRY_DSN not set)"` despite the value being in `.env`.

Two-line fix: add the key to both files. 5-second debug check: `docker compose exec <svc> env | grep <KEY>` — empty output proves the var didn't make the trip.

## See also (durable docs)

For load-bearing decisions, see `docs/adr/`. For domain terminology, see `CONTEXT.md`.

## Agent skills

### Issue tracker

GitHub Issues at `7cav/cavbot2` (`gh` CLI). See `docs/agents/issue-tracker.md`.

### Triage labels

Canonical names used verbatim (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `CONTEXT.md` + `docs/adr/` at repo root. See `docs/agents/domain.md`.

## Agent-cycle config

The headings below are consumed by `syni-run-on-issue`. Edit freely; the orchestrator re-reads them on every run.

## CI gate

```bash
set -euo pipefail
golangci-lint run --timeout=5m
go mod tidy
go test ./... -race -cover -covermode=atomic | tee /tmp/cover.log
./.github/scripts/check-coverage-floors.sh < /tmp/cover.log
go build -o cavbot2
```

Mirrors `.github/workflows/build_test.yml` exactly. `pipefail` ensures a `go test` failure isn't swallowed by `tee`.

## Branch naming

`<type>/<issue-N>-<short-slug>` — type is `feat|fix|chore|docs|test|refactor`, matching the commit prefix. Example: `feat/65-roster-search-hint`.

## Commit prefix

Conventional Commits with optional scope: `<type>(<scope>): <subject>`. Scope is the touched directory or feature (`commands`, `utils`, `loa`, etc.). Subject in imperative mood, lowercase first letter.

## Land strategy

`pr` — open a PR against `develop` (not `main`); human merges.

## Manual review gates

- **Smoke test on the test guild** for any user-facing command behavior change. CI cannot exercise the live Discord gateway. Skip only for pure refactors and non-command changes.
- **Env-var parity check** when adding a new env var: both `.env.example` AND the `environment:` block in `docker-compose.yml` must list it (see "Docker compose `environment:` allowlist vs `.env`" quirk above).
