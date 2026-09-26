[![Build Test](https://github.com/7Cav/cavbot2/actions/workflows/build_test.yml/badge.svg)](https://github.com/7Cav/cavbot2/actions/workflows/build_test.yml)
[![Deploy](https://github.com/7Cav/cavbot2/actions/workflows/build_and_push.yml/badge.svg)](https://github.com/7Cav/cavbot2/actions/workflows/build_and_push.yml)

# CavBot2 Readme

A Discord bot built for the 7th Cavalry Gaming Regiment using Go and DiscordGo, enabling various functions for 7Cav members.

## Prerequisites

- Go 1.25.0 or higher (see the `go` directive in `go.mod`)
- [golangci-lint](https://golangci-lint.run/) — CI installs the latest release
  (`.github/workflows/build_test.yml`), so track that. It must be built with a Go
  at least as new as `go.mod` targets, or it refuses to run.
- A C compiler (gcc/clang) if you want to run the tests with `-race`
- Docker, only if you want the container path in step 4

## Commands

| Command | Purpose |
|---------|---------|
| `/milpac` | Return a user's milpac |
| `/zulu` | Current Zulu time, or a given Zulu time shown in each viewer's own local time |
| `/gamertag_search` | Search for a user by gamertag |
| `/awol` | AWOL troopers for a position |
| `/loa` | Active and upcoming LOAs for a position |
| `/afsm` | Users eligible for the AFSM in a department |
| `/s3aar` | Attendance list for events and operations |
| `/s6-it-check` | S6 IT members eligible for full status |
| `/warden` | Warden role management |
| `/warden-bulkadd-internal` | Add a validated unit's roster to the internal Warden role |
| `/helpline` | Crisis and mental health support resources, optionally addressed to a member |
| `/enlist` | The enlistment process, with a link to the application |
| `/voice-rename` | Rename the spawned voice channel you are in |
| `/voice-lock` | Lock the spawned voice channel you are in to the people inside it and the hub's moderators, and post a lock notice in its chat with Unlock and Let someone in buttons |
| `/voice-unlock` | Unlock the spawned voice channel you are in |

The registered set lives in `commands/registry.go` — update this table when it changes.

## Setup

### 1. Discord application

At <https://discord.com/developers/applications>:

1. 'New Application', then open the 'Bot' tab.
2. 'Reset Token' and copy it. This is `DISCORD_TOKEN`. It is only shown once!
3. On the same tab, enable the 'Server Members Intent' under Privileged Gateway
   Intents. The bot requests `IntentsGuildMembers` and will not start without
   it. A guild is a Discord server.
4. Create (if you don't have one already) a Discord server that will serve as your test environment for the bot.
5. Under 'OAuth2 -> URL Generator', select the `bot` and `applications.commands`
   scopes, then the permissions below, then open the generated URL to invite the
   bot to your test environment server.

| Permission | Needed for |
|------------|-----------|
| View Channels, Send Messages, Embed Links, Attach Files | Every command's response |
| Manage Roles | `/warden` adds, removes and creates roles; `/warden-bulkadd-internal` adds them |
| Manage Channels | `/warden` sets per-channel permission overwrites |

`applications.commands` is what allows slash commands to register. Discord will
not let the bot grant a role positioned above its own, so drag the bot's role
high in the server's role list before testing `/warden`.

### 2. IDs

Enable 'Developer Mode' in the Discord client ('User Settings' -> 'Advanced'), then
right-click a server or channel and choose Copy ID.

Commands register per guild, so `GUILD_ID` must be the server you are testing in.
Guild commands appear immediately.

### 3. Environment

Copy the template — keep `.env.example` in place, it is tracked:

```bash
cp .env.example .env
```

Startup panics without these three:

| Variable | Source |
|----------|--------|
| `DISCORD_TOKEN` | Developer Portal -> Bot -> Reset Token |
| `GUILD_ID` | Right-click the server -> Copy Server ID |
| `BM_TOKEN` | [BattleMetrics](https://www.battlemetrics.com) -> Account -> Developers. Only `/s3aar` uses it; any non-empty placeholder works otherwise. |

Not checked at startup, but each one silently disables something:

| Variable | Effect if unset |
|----------|-----------------|
| `BEARER` | API token for `api.7cav.us`. Every milpac lookup fails with no startup error. Check this first if `/milpac`, `/awol` or `/afsm` come back empty. The startup rank ladder check also needs it and logs `Rank ladder check skipped, ranks fetch failed` once without it. |
| `FORUM_DB_DSN` | LOA cache stays empty, so `/loa` returns nothing. Left blank the bot logs `FORUM_DB_DSN not set, LOA cache disabled` once at startup — but `.env.example` ships a placeholder DSN, which is syntactically valid, so after `cp` you instead get `LOA cache refresh failed` once per node ID, immediately at startup and every 15 minutes after. Both mean the same thing. The production host `xenforo-db` resolves only inside the `xenforo_internal` Docker network. |
| `LOA_NODE_IDS` | The code default is `180` alone, though `.env.example` already sets the five nodes production scans (`180,400,540,178,369`), so a copied `.env` never falls back. |
| `WARDEN_ROLE_BASE_NAME` | The code default is `Verified Warden`, though `.env.example` sets what the roles are named in Discord now (`Verified Foxhole`), so a copied `.env` never falls back. Every `/warden` subcommand composes its role names from this and matches Discord **exactly**, so a value that doesn't reproduce the role name character for character fails all of them with `role not found` and changes nothing. The resolved value is logged at startup as `Warden role base name resolved`. |
| `LOG_LEVEL` | Defaults to `INFO`. Accepts `DEBUG`, `INFO`, `WARN`, `ERROR` — **uppercase only**, anything else silently means `INFO` (including the `default` that `.env.example` ships). `DEBUG` shows per-post LOA parse failures. |
| `DISCORDGO_LOG_LEVEL` | Defaults to `ERROR`, so discordgo reports only its own failures. Accepts `ERROR`, `WARN`, `INFO`, `DEBUG`; anything else means `ERROR`. `WARN` adds the frame the gateway sent when startup fails with `Discord session unavailable`. `DEBUG` also needs `LOG_LEVEL=DEBUG`, and logs every gateway event discordgo does not recognise with its full payload. |
| `SENTRY_DSN` | Sentry stays off; the bot logs `Sentry disabled (SENTRY_DSN not set)`. |
| `BOT_DB_DSN` | The bot's own Postgres store (hubs, spawned channels and the change log for temporary voice channels) stays off; the bot logs `BOT_DB_DSN not set, bot store disabled` once, every other command works as before, and `/voice-rename`, `/voice-lock` and `/voice-unlock` refuse every invocation. `.env.example` leaves it empty on purpose. Set it and the bot pings the database with a short retry, runs its migrations, loads the hub rows for temporary voice channels (`Starting temp voice channels`), and only then opens the Discord session; a database it cannot reach or migrate stops the bot with `Bot store unavailable`. With no hub rows the feature does nothing; rows arrive through the panel. Under compose the value is `postgres://cavbot:<POSTGRES_PASSWORD>@postgres:5432/cavbot?sslmode=disable`. |
| `PANEL_ADDR` | The panel, the bot's web UI, does not listen; the bot logs `PANEL_ADDR not set, panel disabled` once. Set it (`:8080` under compose) and every other `PANEL_*` variable but `PANEL_GROUP_IDS` is required; a missing one stops the bot with `Panel misconfigured` before the Discord session opens. The panel also needs `BOT_DB_DSN`, because the hub page reads the store on every load; without it the bot logs `BOT_DB_DSN not set, panel disabled` and runs with no panel. The listener starts after READY and logs `Panel listening`. `.env.example` documents each `PANEL_*` variable. |
| `POSTGRES_PASSWORD` | Read by the `postgres` service in `docker-compose.yml`, not by the bot. `.env.example` ships `change-me`; a blank value makes the image refuse to start and the bot wait on its healthcheck forever. Only the first boot of an empty volume reads it. |
| `APP_ENV` | Only tags Sentry events with an environment. No effect unless `SENTRY_DSN` is also set. |

When adding a new variable, add it to both `.env.example` and the
`environment:` block in `docker-compose.yml`. Compose does not pass through
variables that are not listed, so skipping the second step means the value never
reaches the container.

### 4. Run

**Locally** — the bot reads `.env` from the working directory at startup, so
having filled it in is enough:

```bash
go run .
```

Or build and run the binary:

```bash
go build -o cavbot2 . && ./cavbot2
```

Anything already exported in your shell wins over the file, which is how the
container gets its configuration. If you export `DISCORD_TOKEN` and then wonder
why editing `.env` changes nothing, that is why.

**In Docker** — Compose reads `.env` itself and injects the values, and `.env`
is excluded from the image by `.dockerignore`, so nothing secret is baked in.
Compose does not build the image (the service declares `image:` with no
`build:`), and the network is external, so make sure both exist before `up`:

```bash
docker build -t cavbot2:latest .
docker network create xenforo_internal   # once, if you don't already have it
docker compose up
```

Only the real `xenforo_internal` network reaches the forum database; a network
you created yourself gets the bot running, but `/loa` stays empty.

Compose also starts a `postgres:18-alpine` service for the bot's own store,
with its data in the `cavbot2_pgdata` volume. The bot waits for its healthcheck
before it starts. A Postgres minor update is a manual `docker compose pull
postgres && docker compose up -d postgres`.

A healthy startup logs, in order: `Logger initialized`, `Sentry disabled
(SENTRY_DSN not set)`, `CavBot2 starting`, the LOA cache line for whichever
`FORUM_DB_DSN` case you are in, either `BOT_DB_DSN not set, bot store disabled`
or `Bot store configured` followed by `Bot database migrated` and `Starting temp voice channels`, `Removing
deprecated commands`, `Registering commands`, `Starting Star Citizen joiner
report scheduler`, and finally `Bot is now running. Press CTRL-C to exit`. That
last line is the success signal — anything that stops earlier is a failed
start.

Expect a pause of roughly 40 seconds on `Registering commands`. The fifteen
commands are created one at a time and Discord rate-limits them, so a silent
console there is normal, not a hang.

**In production** the image comes from a GitHub Release, which pushes
`7cav/cavbot2:<tag>`, then deploys that tag to the host over SSH. GitHub
records the result as a deployment in the `production` environment. A
prerelease pushes and stops. To redeploy or roll back, run the workflow by hand
with a tag Docker Hub already holds. The reverse proxy in front of the panel
has a 90-second read timeout. Once [#355](https://github.com/7Cav/cavbot2/issues/355)
gives the hub page's reads a 10-second budget, the slowest hub page request
takes about 60 seconds. Keep the proxy's read timeout above that. If the proxy
drops a page before the panel gives up on it, the page looks like an
[abandoned page load](CONTEXT.md#observability) and reaches no one.

### One thing that is not a command

`main.go` starts a weekly Star Citizen joiner report unconditionally, with no
env var to disable it. It fires Sundays at 04:20 UTC and DMs a **hardcoded
production user ID** (`commands/star_citizen_joiners.go`). Against your own test
guild the DM simply fails, since the bot shares no server with that person. If
you point `GUILD_ID` at the live 7Cav server and leave the bot running over a
Sunday, a real person gets your test output. Prefer a test guild.

### Troubleshooting

| Symptom | Likely cause |
|---------|--------------|
| Panic naming `DISCORD_TOKEN`, `GUILD_ID` or `BM_TOKEN` | The variable is blank in `.env`, or you are running from a directory that has no `.env` |
| `Found .env but could not load it` | The file exists but is malformed — usually an unquoted value containing `#`, or a stray line with no `=` |
| `Error opening connection: websocket: close 4004` | Discord rejected the token. Re-copy `DISCORD_TOKEN` — a truncated paste or a token reset since you last copied it both land here |
| `Error opening connection: websocket: close 4014` | Disallowed intent. Enable the Server Members Intent in the Developer Portal |
| `FORUM_DB_DSN not set` at startup, or `LOA cache refresh failed` every 15 minutes | Expected without a reachable forum database; only affects `/loa` |
| `Bot database not answering, retrying` nine times, then a panic `Bot store unavailable` | `BOT_DB_DSN` names a Postgres that is not there. The compose host `postgres` resolves only inside compose; for `go run .` blank the variable or point it at a local server |
| Panic `Bot store unavailable: open bot database: the DSN does not parse` | `BOT_DB_DSN` is malformed. The value is not echoed because it carries a password; compare it against the form in `.env.example` |
| `/warden` fails with a permissions error | Bot invited without Manage Roles / Manage Channels, or its own role sits below the role it is editing |
| Commands never appear | Bot invited without `applications.commands`, or `GUILD_ID` is not the server you are in |
| Every milpac lookup fails | `BEARER` missing or expired |
| `Rank ladder check skipped, ranks fetch failed` once at startup | `BEARER` missing or expired, or `api.7cav.us` unreachable. The check runs once after READY and does not retry |
| `Bot member lacks Administrator` at startup, and a Sentry event when `SENTRY_DSN` is set | The bot's role on the guild lacks Administrator. Grant it; the bot keeps running but spawning, handover and `/warden` fail at the next call |
| `Rank ladder drift` at startup, and a Sentry event when `SENTRY_DSN` is set | The abbreviations or order of `tempVCRankRoles` in `commands/temp_vc.go` differ from the milpacs ranks endpoint. The event lists the positions that differ |
| Compose says `pull access denied` for `cavbot2:latest` | The image was never built locally — run `docker build -t cavbot2:latest .` |
| Compose says network `xenforo_internal` not found | Create it, or join the host that has it |
| golangci-lint reports a Go version mismatch | Your golangci-lint was built with an older Go than `go.mod` targets; install a build made with Go 1.25+ |

## Testing

Run the tests:

```bash
go test ./...
```

Run tests with the coverage floor check (matches CI):

```bash
go test ./... -race -cover | .github/scripts/check-coverage-floors.sh
```

CI enforces per-package coverage floors via `.github/scripts/check-coverage-floors.sh`. When a PR raises a package's coverage by more than a point or two, raise its floor in the same PR — that's how the suite ratchets up without the team having to think about it.

The `store` package tests its Postgres implementation against a real server.
They read `TEST_BOT_DB_DSN` and skip when it is unset, so `go test ./...`
passes on a machine with no database, but the floor check then fails for
`store` alone because only its in-memory Fake ran. CI starts a Postgres service
container and sets the variable. To match it locally:

```bash
docker run --rm -d --name cavbot2-test-pg -e POSTGRES_PASSWORD=postgres -p 5433:5432 postgres:18-alpine
```

```bash
export TEST_BOT_DB_DSN='postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable'
```

The tests drop and recreate the `public` schema of that database before every
case, so point the variable at a throwaway server only.

## Contributing

Contributions are welcome through issues and pull requests on our GitHub repository.

## License

Licensed under the [MIT License](https://opensource.org/licenses/MIT).
