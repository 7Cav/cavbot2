[![Build Test](https://github.com/7Cav/cavbot2/actions/workflows/build_test.yml/badge.svg)](https://github.com/7Cav/cavbot2/actions/workflows/build_test.yml)
[![Deploy](https://github.com/7Cav/cavbot2/actions/workflows/build_and_push.yml/badge.svg)](https://github.com/7Cav/cavbot2/actions/workflows/build_and_push.yml)

# CavBot2 Readme

A Discord bot built for the 7th Cavalry Gaming Regiment using Go and DiscordGo, enabling various functions for 7Cav members.

## Prerequisites

- Go 1.21 or later. The `toolchain` line in `go.mod` pins the Go that CI, the
  gate and the image build on, and Go downloads it on first use.
- [golangci-lint](https://golangci-lint.run/) at the version CI pins in
  `GOLANGCI_LINT_VERSION` (`.github/workflows/build_test.yml`). It must be built
  with a Go at least as new as the `toolchain` line in `go.mod`, or it refuses
  to run.
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
| `/s3aar` | Disabled due to disuse |
| `/s6-it-check` | S6 IT members eligible for full status |
| `/foxhole` | Add, remove, bulk-add or purge the Foxhole roles |
| `/foxhole-bulkadd-internal` | Add a validated unit's roster to the Internal Foxhole role |
| `/warden`, `/warden-bulkadd-internal` | The old names of the two commands above, kept until a cleanup release on or after 6 November 2026. They run the same code, and each run under them ends with a note naming the new command and that date |
| `/helpline` | Crisis and mental health support resources, optionally addressed to a member |
| `/enlist` | The enlistment process, with a link to the application |
| `/voice-rename` | Rename the spawned voice channel you are in, optionally making it a knock channel (🚦) |
| `/voice-lock` | Lock the spawned voice channel you are in to the people inside it and the hub's moderators, and post a lock notice in its chat with Unlock and Let someone in buttons |
| `/voice-unlock` | Unlock the spawned voice channel you are in |
| `/record start`, `/record stop` | Start recording the voice or stage channel you are in, with an optional title, and stop it. A recorder joins the channel at the start and leaves at the stop. Needs a recording role (set in the panel) and a rank role |

The registered set lives in `commands/registry.go` — update this table when it changes.

`/foxhole` and the panel's Foxhole page match names differently. `/foxhole add`, `remove` and `bulkadd` send a name to Discord's member search, which matches the start of a name, and refuse a name with more than one hit. The page's paste box reads the bot's own member list and needs a whole username, server nickname or global name, ignoring case. When a line matches two to five members it asks the manager to pick one. So the same name can find a member on one path and miss on the other, and an ID or mention is the one form both read the same way. `bulkadd` takes at most 50 entries, and the paste box has no cap.

The two paths never change the roles at the same time. While an action started on the Foxhole page runs, `/foxhole` and `/foxhole-bulkadd-internal` refuse and change nothing. The reply names the action, who started it and how far it has got. While one of those commands runs, the page names it and refuses to start an action. Two commands still run together.

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
| Manage Roles | `/foxhole` adds, removes and creates roles; `/foxhole-bulkadd-internal` adds them |
| Manage Channels | `/foxhole` sets per-channel permission overwrites |

`applications.commands` is what allows slash commands to register. Discord will
not let the bot grant a role positioned above its own, so drag the bot's role
high in the server's role list before testing `/foxhole`.

#### Recorder applications

A recorder is the account that joins a voice channel to record it
([GLOSSARY.md](GLOSSARY.md)), and each one is a Discord application of its
own, separate from the bot's. Recording stays off until at least one recorder
token is set in `RECORDER_TOKENS`, and removing every token and restarting
turns it off again. Skip this on a host that doesn't record. For now a recorder
joins the channel at `/record start` and leaves at `/record stop`, and
writes one track per speaker under `RECORDINGS_DIR` (#381). Temporary voice
channels ignore it.

1. 'New Application', named for what it is, such as `CavBot Recorder`, so
   members can tell it from the bot.
2. On the 'Bot' tab, 'Reset Token' and copy it into `RECORDER_TOKENS`. Leave
   every Privileged Gateway Intent off. A recorder asks only for the guilds
   and voice states intents ([ADR 0014](docs/adr/0014-voice-on-disgo-and-dave-go.md)).
3. Under 'OAuth2 -> URL Generator', select the `bot` scope and the
   Administrator permission, then open the generated URL to invite the
   recorder to the same server as the bot. It holds Administrator like the
   bot, since its token sits in the same environment (ADR 0014).

Another recorder is another application, its token added to `RECORDER_TOKENS`
after a comma, with no code change.

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
| `FOXHOLE_ROLE_BASE_NAME` | Defaults to `Verified Foxhole`, what the roles are named in Discord now. Every `/foxhole` subcommand composes its role names from this and matches Discord **exactly**, so a value that doesn't reproduce the role name character for character fails all of them with `role not found` and changes nothing. The resolved value is logged at startup as `Foxhole role base name resolved`. Until the cleanup release that drops the old command names, the bot also reads the old variable, `WARDEN_ROLE_BASE_NAME`, when this one is unset or empty, and logs a warning naming it at startup. |
| `LOG_LEVEL` | Defaults to `INFO`. Accepts `DEBUG`, `INFO`, `WARN`, `ERROR` — **uppercase only**, anything else silently means `INFO` (including the `default` that `.env.example` ships). `DEBUG` shows per-post LOA parse failures. |
| `DISCORDGO_LOG_LEVEL` | Defaults to `ERROR`, so discordgo reports only its own failures. Accepts `ERROR`, `WARN`, `INFO`, `DEBUG`; anything else means `ERROR`. `WARN` adds the frame the gateway sent when startup fails with `Discord session unavailable`. `DEBUG` also needs `LOG_LEVEL=DEBUG`, and logs every gateway event discordgo does not recognise with its full payload. |
| `SENTRY_DSN` | Sentry stays off; the bot logs `Sentry disabled (SENTRY_DSN not set)`. |
| `BOT_DB_DSN` | The bot's own Postgres store (hubs, spawned channels and the change log for temporary voice channels, and the Foxhole page's notes, approvals and change log) stays off; the bot logs `BOT_DB_DSN not set, bot store disabled` once, every other command works as before, and `/voice-rename`, `/voice-lock`, `/voice-unlock` and `/record` refuse every invocation. `.env.example` leaves it empty on purpose. Set it and the bot pings the database with a short retry, runs its migrations, loads the hub rows for temporary voice channels (`Starting temp voice channels`), ends any Foxhole action the last run left running as stopped by a restart (`Foxhole action stopped by a restart`), and only then opens the Discord session; a database it cannot reach or migrate stops the bot with `Bot store unavailable`. With no hub rows the feature does nothing; rows arrive through the panel. Under compose the value is `postgres://cavbot:<POSTGRES_PASSWORD>@postgres:5432/cavbot?sslmode=disable`. |
| `PANEL_ADDR` | The panel, the bot's web UI, does not listen; the bot logs `PANEL_ADDR not set, panel disabled` once. Set it (`:8080` under compose) and every other `PANEL_*` variable but `PANEL_GROUP_IDS` is required; a missing one stops the bot with `Panel misconfigured` before the Discord session opens. The panel also needs `BOT_DB_DSN`, because the hub page reads the store on every load; without it the bot logs `BOT_DB_DSN not set, panel disabled` and runs with no panel. The listener starts after READY and logs `Panel listening`. `.env.example` documents each `PANEL_*` variable. |
| `FOXHOLE_GROUP_ID` | Defaults to `323`, the forum's Foxhole group. A forum user in it, as primary or secondary group, is a Foxhole manager: sign-in takes them to the panel's Foxhole page, and the hub page and its saves refuse them. Panel admins open the Foxhole page whatever their groups. The group check reads it on every request, so adding or removing a manager on the forum needs no deploy. With `PANEL_ADDR` set, a value that isn't a number stops the bot with `Panel misconfigured`. |
| `RECORDER_TOKENS` | Recording stays off, and nobody can record; the bot logs `RECORDER_TOKENS not set, recording off` once, and `/record start` replies that recording is off. `/record` stays in the command list either way, so it keeps its Server Settings roles. Set it to a recorder token, or several separated by commas ([Recorder applications](#recorder-applications)), and each recorder connects at startup, logs `Recorder connected` with its user ID, and shows online. Removing every token and restarting turns recording off again. A token that fails to connect goes to Sentry as `Recorder failed to connect`, and the bot and the other recorders keep running. |
| `RECORDINGS_DIR` | Where recordings go: a directory per recording, named by its ID, holding one Ogg Opus track per speaker, named by the speaker's Discord ID. Defaults to `recordings` under the working directory. `docker-compose.yml` sets it to `/recordings`, the mount point of the `cavbot2_recordings` volume, and doesn't read it from `.env`, so the tracks always land on the volume and survive a redeploy. A directory the bot can't write to goes to Sentry as `Track not written` at a speaker's first words, and the recording keeps running without that track. |
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
or `Bot store configured` followed by `Bot database migrated`, then
`RECORDER_TOKENS not set, recording off` or a `Recorder connected` line per
recorder, then `Starting temp voice channels` when the store is configured,
`Panel listening` when the panel is configured, `Registering commands`,
`Commands registered`, `Starting Star Citizen joiner report scheduler`, and
finally `Bot is now running. Press CTRL-C to exit`. That
last line is the success signal — anything that stops earlier is a failed
start.

Registering the commands takes under a second, since the registry goes to
Discord in one request. Discord rate-limits that request, so after restarts
seconds apart it can hold one for up to about a minute. A silent console on
`Registering commands` is then normal, not a hang. The panel already listens
by then, and a stop still shuts the bot down cleanly.

**In production** the image comes from a GitHub Release, which pushes
`7cav/cavbot2:<tag>`, then deploys that tag to the host over SSH. GitHub
records the result as a deployment in the `production` environment. A
prerelease pushes and stops. To redeploy or roll back, run the workflow by hand
with a tag Docker Hub already holds. After a deploy succeeds, the workflow
records it in Sentry as the release `cavbot2@<tag>` with the commits since the
previous release, so a `Fixes CAVBOT2-N` line in a commit resolves that Sentry
issue once its release deploys. The bot sends the same name with its events;
only Sentry sees the prefix. That step reads an organization auth token from
the `SENTRY_AUTH_TOKEN` repository secret, and without it the run ends red
after a good deploy. The reverse proxy in front of the panel
has a 90-second read timeout. A hub or Foxhole page load takes about 20 seconds
at the slowest: the 10-second group check, then the page's 10-second time
budget. The budget covers the page's store reads and any wait for Discord to
send the bot the guild's data, or on the Foxhole page the member list. A roster preview's fetch of the unit's roster from the 7Cav API counts against the same budget, and its Confirm fetches the roster again under a budget of its own. A save can take longer. It waits at most the same 10 seconds
for that data, and its create or rename goes to Discord with up to 20 seconds
per attempt. discordgo retries a call Discord answers with a 502, up to three
times. A Foxhole action, such as a purge, runs in the background and its
request redirects at once, so the proxy never cuts one off however long it
runs. Keep the proxy's read timeout above the slowest request. If the proxy drops a page before the panel gives up on it, the page
looks like an [abandoned page load](GLOSSARY.md#observability) and reaches no
one.

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
| A panic line ending `[recovered, repanicked]`, its stack trace starting in `StartWatch.ReportFailure` | Expected for a failed start with `SENTRY_DSN` set. The bot caught the panic to send it to Sentry, then let the same panic go on. The frames below `panic(...)` name the step that failed |
| `Failed start already sent to Sentry` just before the panic | The bot failed to start with the same message less than an hour ago, and that failure went to Sentry at the logged `sent_at`. While a restart loop keeps failing with one message, that message goes to Sentry at most once an hour, so the loop can't use up the org's Sentry quota. A start that reaches running clears the record, and recreating the container clears it too |
| `Recorder failed to connect` at startup, with `recorder=N`, and a Sentry event when `SENTRY_DSN` is set | Discord refused the Nth token in `RECORDER_TOKENS`, usually one reset since you copied it. Reset it on that recorder application's 'Bot' tab and paste the new one. The bot and the other recorders keep running without it |
| `/record start` replies that something went wrong with the recorder, and `Recorder failed to join` goes to Sentry with `recorder` and `channel_id` | Discord didn't let the recorder into the channel within 30 seconds. Check the recorder is in the server and holds Administrator ([Recorder applications](#recorder-applications)). The recorder is free again for the next start |
| `Voice connection lost mid-recording` goes to Sentry with `recorder` and the close that started it | disgo couldn't keep the recorder's voice connection: the voice gateway closed and a fresh connection failed, or Discord removed the recorder from the channel (close 4014: moved, kicked, the channel deleted, or its gateway session dropped). Audio stops reaching the tracks. A brief voice reconnect that succeeds sends nothing |
| `DAVE decrypt failures` goes to Sentry with `recorder`, `user_id` and `frames` | Half a second of that speaker's audio in a row failed end-to-end decryption, and it's missing from their track. `DAVE decrypt recovered` in the log, with `frames_lost`, marks when it came back |
| `Voice packets unreadable` goes to Sentry with `recorder` | Half a second of the channel's packets in a row failed transport decryption, so no speaker's audio reached the tracks |
| `Track not written` goes to Sentry with `recording_id` and `user_id` | The bot couldn't create or write that speaker's track under `RECORDINGS_DIR`, usually a full disk or a directory it can't write to. The track keeps what reached the disk before, and takes nothing more |
| `/foxhole` replies that a Foxhole action is running on the Foxhole page | A manager started an action on the panel's Foxhole page, and the role-changing commands refuse until it ends. Try again then, or press Stop on the page's progress block |
| `/foxhole` fails with a permissions error | Bot invited without Manage Roles / Manage Channels, or its own role sits below the role it is editing |
| Commands never appear | Bot invited without `applications.commands`, or `GUILD_ID` is not the server you are in |
| Every milpac lookup fails | `BEARER` missing or expired |
| `Rank ladder check skipped, ranks fetch failed` once at startup | `BEARER` missing or expired, or `api.7cav.us` unreachable. The check runs once after READY and does not retry |
| `Bot member lacks Administrator` at startup, and a Sentry event when `SENTRY_DSN` is set | The bot's role on the guild lacks Administrator. Grant it; the bot keeps running but spawning, handover and `/foxhole` fail at the next call |
| `Rank ladder drift` at startup, and a Sentry event when `SENTRY_DSN` is set | The abbreviations or order of `tempVCRankRoles` in `commands/temp_vc.go` differ from the milpacs ranks endpoint. The event lists the positions that differ |
| Compose says `pull access denied` for `cavbot2:latest` | The image was never built locally — run `docker build -t cavbot2:latest .` |
| Compose says network `xenforo_internal` not found | Create it, or join the host that has it |
| golangci-lint stops with `used to build golangci-lint is lower than the targeted Go version` | Your golangci-lint was built with an older Go than the `toolchain` line in `go.mod`. Install a build made with a Go at least as new as that line. The `targeted Go version` in the message is the Go version golangci-lint needs |

## Testing

Run the tests:

```bash
go test ./...
```

Run the CI gate, the same script CI's Build job runs. It needs Docker and
`golangci-lint`:

```bash
.github/scripts/gate.sh
```

It lints, checks that `go.mod` and `go.sum` are tidy, runs the suite with
`-race` and coverage, checks the coverage floors and builds. The `store`
package's tests need a real Postgres, so the gate starts a throwaway one of
its own and removes it on exit. Several checkouts can run the gate at once.
It runs on the toolchain `go.mod` pins in its `toolchain` line, whatever Go
you have installed, because coverage differs between Go versions. CI and the
image's builder use the same toolchain, so the image production runs is built
by the Go the tests ran on. One gate step fails when the Dockerfile's `golang`
tag names another version. To move to a new Go, change the `toolchain` line
and that tag in the same PR.

CI enforces per-package coverage floors, listed in
`.github/coverage-floors.tsv`. A floor stays within 3 points of its package's
coverage. When coverage climbs past that, the floor check prints a RAISE line
with the new floor, and the floor goes up in the same PR. A package with tests
needs a floor. Without one, the check fails with a NO FLOOR line naming the
floor to set. ADR 0005 has the reasoning.

To run the `store` tests on their own, start a throwaway database. The script
prints its DSN, and the container's name on stderr:

```bash
dsn=$(.github/scripts/test-db.sh)
```

```bash
TEST_BOT_DB_DSN=$dsn go test ./store/...
```

Without `TEST_BOT_DB_DSN` the Postgres tests skip and only the in-memory Fake
runs. The tests drop and recreate the `public` schema before every case, so
give them only a database `test-db.sh` started.

### Smoke test

CI never reaches the Discord gateway or a browser, so a change members can see
also runs on a test guild before the release that ships it.
`go run ./tools/smoke up` starts this checkout's bot there, with the panel at
<http://localhost:8080> and a fake forum to sign in through.
[docs/smoke-test.md](docs/smoke-test.md) covers the
rest.

## Contributing

Contributions are welcome through issues and pull requests on our GitHub repository.

## License

Licensed under the [MIT License](https://opensource.org/licenses/MIT).
