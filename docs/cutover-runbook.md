# Cutover runbook: temporary voice channels from MEE6 to cavbot2

**Who:** the maintainer, in one evening.
**Sources:** spec [#285](https://github.com/7Cav/cavbot2/issues/285), the
cutover order in `docs/temp-vc-decisions.md`, and ADR 0012 (store and
migrations). Terms are the "Temporary voice channels" section of
`CONTEXT.md`.

The feature is inert until `BOT_DB_DSN` and `PANEL_ADDR` reach the process,
and spawns nothing until a hub row is enabled. So the order below turns things
on one at a time: the host first, then the release, then the command gate,
then the hubs, and MEE6 goes off between the transcription and the enabling.
Nobody is dropped from voice at any step.

## Before the evening

Do these on any day before the cutover. Each is checkable.

### 1. CI green on `develop` at the commit to release

```bash
gh run list --branch develop --workflow build_test.yml -L 1 --json headSha,conclusion --jq '.[]|"\(.headSha[0:7]) \(.conclusion)"'
```

The SHA is the one the release tag will point at, and the conclusion is
`success`.

### 2. Smoke test passed on the test guild

The list on issue [#297](https://github.com/7Cav/cavbot2/issues/297), run
against the commit to be released and recorded there: register a hub, join,
spawn, handover with the ownership notice, rename as owner and as moderator,
delete on empty, restart sweep with an occupied channel, panel sign-in,
create a hub channel, edit, remove, guild-wide moderator roles, and the spawn
failure message on a full category.

### 3. The host compose file

Watchtower recreates the bot container from the running container's own
configuration. It never reads `docker-compose.yml`. So the compose file on
the host has to declare the `postgres` service, the `edge` network and the
new variables before the release, and the container has to be recreated from
it once, or the release starts a bot with no store and no panel.

On `traycer.7cav.us` the compose directory is `/etc/compose/cavbot2/`. Bring
its `docker-compose.yml` to the repo's file with two host-only differences on
the `cavbot2` service:

- `image: 7cav/cavbot2:latest`, the Docker Hub image the release pushes, in
  place of the local `cavbot2:latest`.
- The label `com.centurylinklabs.watchtower.enable: "true"`, so Watchtower
  keeps updating the bot. The `postgres` service keeps its `"false"`.

Check the result against the repo before moving on:

```bash
diff <(ssh traycer.7cav.us cat /etc/compose/cavbot2/docker-compose.yml) docker-compose.yml
```

Only those lines should differ. The `GITHUB_APP_*` lines on the host
are from a command removed in #243 and go.

### 4. The host `.env`

`/etc/compose/cavbot2/.env` already carries the final names (done 2026-09-19
for #297, backup beside it): the seven `PANEL_*` variables, `PANEL_GROUP_IDS`,
`POSTGRES_PASSWORD` and `BOT_DB_DSN`, with `PANEL_OAUTH_REVOKE_URL` removed.
The file is root-only, so edit it with `sudo`. Keep `POSTGRES_PASSWORD` and
the password inside `BOT_DB_DSN` the same: the postgres image reads the
former on the first boot of an empty volume and never again, and the bot
connects with the latter.

Confirm the key names without printing the values:

```bash
ssh traycer.7cav.us 'sudo cut -d= -f1 /etc/compose/cavbot2/.env | grep -v "^#"'
```

Expected among them, and no `PANEL_OAUTH_REVOKE_URL`:

```
BOT_DB_DSN
POSTGRES_PASSWORD
PANEL_ADDR
PANEL_BASE_URL
PANEL_OAUTH_CLIENT_ID
PANEL_OAUTH_CLIENT_SECRET
PANEL_OAUTH_AUTHORIZE_URL
PANEL_OAUTH_TOKEN_URL
PANEL_OAUTH_USERINFO_URL
PANEL_GROUP_IDS
```

### 5. Recreate the container from the new compose file

```bash
ssh traycer.7cav.us 'cd /etc/compose/cavbot2 && docker compose up -d'
```

This starts the database and recreates the bot with the new environment and
networks, still on the current release, which ignores the new variables. The
bot restarts once here, which is a few seconds of `/warden` and `/loa` being
away. Check:

```bash
ssh traycer.7cav.us 'docker ps --filter name=cavbot2 --format "{{.Names}}\t{{.Image}}\t{{.Status}}"'
```

`cavbot2-db` is `healthy` and `cavbot2` is `Up`. The bot's log ends in `Bot
is now running`.

## Release checklist

Each line has a command or a place to look, and the value to expect.

1. **Tag.** Create the release from `develop`; the tag is the version the
   binary reports. `build_and_push.yml` builds the image, pushes
   `7cav/cavbot2:<tag>` and `7cav/cavbot2:latest`, and pings Watchtower.

   ```bash
   gh release create 0.16.0 --target develop --generate-notes --title 0.16.0
   ```

   ```bash
   gh run list --workflow build_and_push.yml -L 1 --json conclusion,displayTitle --jq '.[]|"\(.conclusion) \(.displayTitle)"'
   ```

   `success`.

2. **Watchtower pull.** Within a minute of the workflow finishing the bot is
   on the new image:

   ```bash
   ssh traycer.7cav.us 'docker logs cavbot2 --since 15m 2>&1 | grep "CavBot2 starting"'
   ```

   The line carries `version=0.16.0`.

3. **The migration log line.** The store came up and every migration ran
   before the Discord session opened:

   ```bash
   ssh traycer.7cav.us 'docker logs cavbot2 --since 15m 2>&1 | grep -E "Bot database migrated|Starting temp voice channels"'
   ```

   `Bot database migrated applied=3` on the first release (the three files in
   `store/migrations/`), then `Starting temp voice channels hubs=0`. A log
   that stops at `Bot store unavailable` means the database was not
   reachable or a migration failed; the container restarts and retries, and
   `docker logs cavbot2-db` says why.

4. **The panel answering at `cavbot2.7cav.us`.** The listener started after
   READY, and the route through Cloudflare and NPM reaches it:

   ```bash
   ssh traycer.7cav.us 'docker logs cavbot2 --since 15m 2>&1 | grep "Panel listening"'
   ```

   ```bash
   curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://cavbot2.7cav.us/
   ```

   The log line carries `addr=[::]:8080`, the base URL and `group_ids="[71 47
   44]"`. The curl returns `303` with a redirect to `/signin`, not a `502`.

5. **An empty hub list.** Open `https://cavbot2.7cav.us/`, sign in through
   the forum, and see the hub page with no hubs and the guild-wide moderator
   section empty. The sign-in proves the OAuth client, the redirect URI and
   the group check; the empty list proves the page reads the store.

## The seven cutover steps

In this order. Steps 3 and 4 happen while MEE6 still runs; step 5 switches
it off; step 6 turns the bot's hubs on.

### 1. Release

The Release checklist above, every line green.

### 2. Restrict `/voice-rename` to the 29 rank roles

In Discord, Server Settings, Integrations, the bot, then the `/voice-rename`
command. Turn off the `@everyone` entry and add each of the 29 rank roles
from the code ladder (`tempVCRankRoles` in `commands/temp_vc.go`), most
senior first:

`GOA`, `GEN`, `LTG`, `MG`, `BG`, `COL`, `LTC`, `MAJ`, `CPT`, `1LT`, `2LT`,
`CW5`, `CW4`, `CW3`, `CW2`, `WO1`, `CSM`, `SGM`, `1SG`, `MSG`, `SFC`, `SSG`,
`SGT`, `CPL`, `SPC`, `PFC`, `PVT`, `AR`, `RCT`.

Until this is applied the command is visible to every member, and a guest
in a spawned channel reaches the no-owner refusal, which logs at WARN. That
is why it sits before the hubs are enabled. Discord's 100-entry limit per
command is not reached.

### 3. Transcribe every hub from MEE6, registered disabled

For each hub on the live guild (17 at the last count, table on
[#262](https://github.com/7Cav/cavbot2/issues/262)), open its settings in
the MEE6 dashboard and register it in the panel with the same values:

- Hub channel: pick the existing "Join to create" channel in the register
  picker. The category comes from its parent and is read-only.
- Base string: what MEE6 names the spawned channels, without the number.
  The bot names them `<base string> - <n>`.
- Permission source: `category` unless the hub channel's own overwrites are
  stricter than its category's (Foxhole Secure is the known case), then
  `hub_channel`.
- Moderator roles: the roles MEE6 lists as moderators for that hub, if any.
  Roles common to every hub go in the guild-wide section once, at the top of
  the page.
- User limit and bitrate as MEE6 has them.
- **Enabled: off.** A disabled hub keeps its settings and ignores joins, so
  nothing spawns while MEE6 still runs.

MEE6's ignored roles are not carried over; with rename as the only command
there is nothing to be exempt from. Each register appends a change log entry
under the hub, so the panel shows what was transcribed and when.

### 4. Delete every empty `#`-named channel in a hub category

MEE6 leaves channels named with a `#` in the hub categories when it loses
track of them. Delete each one that is empty, in Discord. The bot's restart
sweep never touches a channel it holds no row for, so these would otherwise
stay forever.

### 5. Switch the MEE6 Temporary Channels plugin off

In the MEE6 dashboard for the guild, the Temporary Channels plugin, off. The
maintainer has dashboard access. From this moment a join to a hub does
nothing until step 6, so do 6 right after.

### 6. Enable the hubs in the panel

For each hub, open its form, turn Enabled on, Save. The runtime picks up each
save in-process; no restart. Join one hub and confirm a spawned channel
appears, named `<base string> - 1`, with the ownership notice in its chat,
and that it is deleted when you leave. The log shows `Temp VC created` and
`Temp VC deleted`.

### 7. Delete each occupied MEE6 channel by hand once it empties

List the MEE6-made channels still occupied at step 5 (they carry MEE6's
names, not `<base string> - <n>`). Nobody is moved. When each one empties,
delete it in Discord. MEE6 is off, so it will not delete them itself, and the
bot holds no row for them, so it will not either.

## After the evening

- Watch Sentry for the first day. A Discord-side create failure captures once
  per streak per hub; a refusal the bot makes itself (a hub with no category)
  is a WARN line and a note on the hub list, never a Sentry event.
- The hub list shows each hub's last spawn failure with its time and cause. A
  hub marked broken has lost its channel or its category; Remove it and
  register the channel again once it has a category.
- The rank ladder drift check and the Administrator check run once per
  process start and capture to Sentry. `Rank ladder drift` means the milpacs
  ranks endpoint and `tempVCRankRoles` disagree; fix the ladder and release.

## Rollback

Rolling back the image is safe at any step: every migration is additive and
an older binary runs against the newer schema (ADR 0012). Pin the image on
the host and recreate:

```bash
ssh traycer.7cav.us 'cd /etc/compose/cavbot2 && sed -i "s#7cav/cavbot2:latest#7cav/cavbot2:0.15.1#" docker-compose.yml && docker compose up -d cavbot2'
```

Then switch the MEE6 plugin back on. Hub rows and the change log stay in
Postgres for the next attempt. Restore `:latest` in the compose file before
the next release, or Watchtower keeps the pin.
