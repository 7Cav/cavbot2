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
| `/zulu` | Current Zulu time |
| `/gamertag_search` | Search for a user by gamertag |
| `/awol` | AWOL troopers for a position |
| `/loa` | Active and upcoming LOAs for a position |
| `/afsm` | Users eligible for the AFSM in a department |
| `/s3aar` | Attendance list for events and operations |
| `/s6-it-check` | S6 IT members eligible for full status |
| `/warden` | Warden role management |
| `/warden-bulkadd-internal` | Add a validated unit's roster to Verified Warden Internal |
| `/apps_beta_deploy` | Deploy the Apps beta version |

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
| `BEARER` | API token for `api.7cav.us`. Every milpac lookup fails with no startup error — check this first if `/milpac`, `/awol` or `/afsm` come back empty. |
| `GITHUB_APP_KEY`, `GITHUB_APP_CLIENT_ID` | `/apps_beta_deploy` cannot authenticate. The key is a base64-encoded PEM. |
| `FORUM_DB_DSN` | LOA cache stays empty, so `/loa` returns nothing. Left blank the bot logs `FORUM_DB_DSN not set, LOA cache disabled` once at startup — but `.env.example` ships a placeholder DSN, which is syntactically valid, so after `cp` you instead get `LOA cache refresh failed` once per node ID, immediately at startup and every 15 minutes after. Both mean the same thing. The production host `xenforo-db` resolves only inside the `xenforo_internal` Docker network. |
| `LOA_NODE_IDS` | The code default is `180` alone, though `.env.example` already sets the five nodes production scans (`180,400,540,178,369`), so a copied `.env` never falls back. |
| `LOG_LEVEL` | Defaults to `INFO`. Accepts `DEBUG`, `INFO`, `WARN`, `ERROR` — **uppercase only**, anything else silently means `INFO` (including the `default` that `.env.example` ships). `DEBUG` shows per-post LOA parse failures. |
| `DISCORDGO_LOG_LEVEL` | discordgo's own gateway logging stays at `ERROR`, which is where it has always been — this only ever adds output, never removes it. Accepts `ERROR`, `WARN`, `INFO`, `DEBUG`; anything else means `ERROR`. Set `WARN` if startup dies with `Discord session unavailable`: that is the level at which discordgo names the frame the gateway sent instead of `READY`. `DEBUG` needs `LOG_LEVEL=DEBUG` too, and logs every gateway event discordgo does not recognise along with its full payload — keep it off in production. |
| `SENTRY_DSN` | Sentry stays off; the bot logs `Sentry disabled (SENTRY_DSN not set)`. |
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

A healthy startup logs, in order: `Logger initialized`, `Sentry disabled
(SENTRY_DSN not set)`, `CavBot2 starting`, the LOA cache line for whichever
`FORUM_DB_DSN` case you are in, `Removing deprecated commands`, `Registering
commands`, `Starting Star Citizen joiner report scheduler`, and finally `Bot is
now running. Press CTRL-C to exit`. That last line is the success signal —
anything that stops earlier is a failed start.

Expect a pause of roughly 40 seconds on `Registering commands`. The eleven
commands are created one at a time and Discord rate-limits them, so a silent
console there is normal, not a hang.

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
| `/warden` fails with a permissions error | Bot invited without Manage Roles / Manage Channels, or its own role sits below the role it is editing |
| Commands never appear | Bot invited without `applications.commands`, or `GUILD_ID` is not the server you are in |
| Every milpac lookup fails | `BEARER` missing or expired |
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

## Contributing

Contributions are welcome through issues and pull requests on our GitHub repository.

## License

Licensed under the [MIT License](https://opensource.org/licenses/MIT).
