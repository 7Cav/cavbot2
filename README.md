[![Build Test](https://github.com/7Cav/cavbot2/actions/workflows/build_test.yml/badge.svg)](https://github.com/7Cav/cavbot2/actions/workflows/build_test.yml)
[![Deploy](https://github.com/7Cav/cavbot2/actions/workflows/build_and_push.yml/badge.svg)](https://github.com/7Cav/cavbot2/actions/workflows/build_and_push.yml)

# CavBot2 Readme

A Discord bot built for the 7th Cavalry Gaming Regiment using Go and DiscordGo, enabling various functions for 7Cav members.

## Prerequisites

- Go 1.25.0 or higher (see `go` directive in `go.mod`)
- [golangci-lint](https://golangci-lint.run/) 2.5.0+, built with Go 1.25 or newer
- A C compiler (gcc) if you want to run the tests with `-race`

## Commands

| Command | Purpose |
|---------|---------|
| `/milpac` | Return a user's milpac |
| `/zulu` | Current Zulu time |
| `/gamertag_search` | Find a user by gamertag |
| `/awol` | AWOL troopers for a position |
| `/loa` | Active and upcoming LOAs for a position |
| `/afsm` | Members eligible for the AFSM in a department |
| `/s3aar` | Attendance list for events and operations |
| `/promo` | Tools to gauge promotion eligibility across the regiment |
| `/billetaudit` | Several audit tools to flag discrepancies in MILPAC |
| `/apps_beta_deploy` | Deploy the Apps beta version |
| `/s6-it-check` | S6 IT members eligible for full status |
| `/warden` | Warden role management |
| `/warden-bulkadd-internal` | Add a validated unit roster to Verified Warden Internal |
| `/apps_beta_deploy` | Deploy the Apps beta version |

## Setup

### 1. Discord application

At <https://discord.com/developers/applications>:

1. 'New Application', then open the 'Bot' tab.
2. 'Reset Token' and copy it. This is `DISCORD_TOKEN`. It is only shown once!
3. On the same tab, enable the 'Server Members Intent' under Privileged Gateway
   Intents. The bot requests `IntentsGuildMembers`; without this the gateway
   connection fails at startup. A guild is a Discord server.
4. Create (if you don't have one already) a Discord server that will serve as your test environment for the bot.
5. Under 'OAuth2 -> URL Generator', select the `bot` and `applications.commands`
   scopes plus the 'View Channels', 'Send Messages', 'Attach Files' and 'Embed Links'
   permissions, then open the generated URL to invite the bot to your test environment server.

`applications.commands` is what allows slash commands to register.

### 2. IDs

Enable 'Developer Mode' in the Discord client ('User Settings' -> 'Advanced'), then
right-click a server or channel and choose Copy ID.

Commands register per guild, so `GUILD_ID` must be the server you are testing in.
Guild commands appear immediately.

### 3. Environment

Rename `.env.example` to `.env` and fill it in.

Startup fails without these:

| Variable | Source |
|----------|--------|
| `DISCORD_TOKEN` | Developer Portal -> Bot -> Reset Token |
| `GUILD_ID` | Right-click the server -> Copy Server ID |
| `BM_TOKEN` | [BattleMetrics](https://www.battlemetrics.com) -> Account -> Developers. Only `/s3aar` uses it; any non-empty placeholder works otherwise. |

Not checked at startup, but required in practice:

| Variable | Source |
|----------|--------|
| `BEARER` | API token for `api.7cav.us`. Every api call fails without it, and there is no startup error. Check this if lookups fail. |

When adding a new variable, add it to both `.env.example` and the
`environment:` block in `docker-compose.yml`. Compose does not pass through
variables that are not listed, so skipping the second step means the value never
reaches the container.

### 4. Run

```bash
go build -o cavbot2 .
go run .
```

A healthy startup logs `Registering commands`, then
`Bot is now running. Press CTRL-C to exit`.

### Troubleshooting

| Symptom | Likely cause |
|---------|--------------|
| Panic naming `DISCORD_TOKEN`, `GUILD_ID` or `BM_TOKEN` | Variable missing from `.env` |
| Gateway connection fails | Server Members Intent not enabled |
| Commands never appear | Bot invited without `applications.commands`, or `GUILD_ID` is not the server you are in |
| Every milpac lookup fails | `BEARER` missing or expired |
| `FORUM_DB_DSN not set` warning | Expected without a forum database; only affects LOA |
| golangci-lint reports a Go version mismatch | Binary was built with an older Go than `go.mod` targets; reinstall 2.5.0+ |

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
