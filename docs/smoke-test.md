# Smoke test

A smoke test runs a change on the test guild before it merges: the real bot on
the real gateway, and the real panel in a browser. CI reaches neither. Run one
for any change a member or panel user can see: a slash command, a panel page,
or what the bot does to channels, roles and messages.

The agent that made the change runs it and reports it in the PR body. Every
step here is the agent's, except the few that say to ask the person you work
for. It needs the README's setup steps 1 to 3 done once: a test guild, a bot
invited to it, and a filled-in `.env` at the main checkout's root.

## Start a run

```bash
go run ./tools/smoke up
```

`up` builds this worktree, starts the bot on the test guild with the panel at
<http://localhost:8080>, and returns once the bot is ready, about a minute
later. It reads the credentials from the main checkout's `.env` and reports
variable names only, so a linked worktree needs no copy and nobody needs to
open the file.

One run exists at a time across all worktrees, because one bot token is one
gateway connection and one command list on the guild. `up` names the worktree
that holds the run, and `go run ./tools/smoke down` stops it from any of them.
Start every bot through the tool, so the lock sees it.

`go run ./tools/smoke help` lists the commands and their flags.

## Sign in to the panel

Open <http://localhost:8080> in the built-in browser pane with `preview_start`
and sign in. The run's fake forum offers three personas in place of
a password:

| Persona | Lands on |
|---------|----------|
| Panel admin | The Hubs page, and opens the Foxhole page too |
| Foxhole manager | The Foxhole page |
| No access | The no-access page |

Sign out from the rail to switch persona. The fake checks the redirect URI and
the PKCE verifier as the forum does. `go run ./tools/smoke forum expired`,
`refused` or `down` makes the next group check answer 401, 403 or nothing,
which brings up the session-ended, refused and forum-unavailable pages.
`forum ok` puts it back.

A restart ends every panel session, as in production, so sign in again after
each `down` and `up`.

### The real forum

Use `up --forum real` when the change touches sign-in itself: the auth
handlers in `panel/panel.go`, `panel/forum.go`, or the OAuth settings. The
panel then signs in through 7cav.us, using the forum client's registered local
redirect URI, `http://localhost:8080/auth/callback`. Ask the person you work
for to approve the Authorize click once for the whole run, restarts included.
If the pane shows the forum's login page, ask them to sign in there; the
password is theirs to type.

## Discord

`go run ./tools/smoke api METHOD PATH [JSON]` calls the Discord API as the bot.
`{guild}` in the path stands for the test guild's ID, and a change carries the
audit log reason "smoke test". It sets up fixtures such as roles and nicknames,
and reads results such as the audit log and the registered commands, with no
Discord client. A change call refuses a guild with more than 100 members.

A slash command needs a person's Discord session, since a bot cannot invoke
one. Use Claude in Chrome, where the person's Discord stays signed in, and ask
once per run, because the commands post as them. Check the channel belongs to
the test guild before typing. Read the reply on the page. An ephemeral reply
shows only there, to the invoker. Leave the window at its size;
resizing it threw off clicks in earlier runs.

## Finish

Run `go run ./tools/smoke down`. Undo the fixtures you made through `api`,
such as roles given and nicknames set. Leave channels and anything else only
a delete removes, unless the person says to delete them. Close the pane's tab
with `tabs_close`; `preview_stop` takes no ID for a URL preview.

## Report

Put the results in the PR body under a **Smoke test** heading: the build `up`
printed, such as `smoke-09dccef`, each check with the persona or Discord
action behind it, and what happened. Name what the run could not reach and
why, such as a state only a unit test can force. This repository is public, so
describe people by persona or role, and keep forum usernames, user IDs, IP
addresses and channel contents out of the PR.

A smoke test that did not run blocks the merge. Say so in the PR body with the
reason, and ask the person to run it or waive it.

## When a run will not start

`up` prints why. The usual causes:

| `up` says | Fix |
|-----------|-----|
| Port 8080 or 8091 is in use | Stop the process it names. A leftover `go run .` is the usual one |
| `docker is not on PATH`, or a Docker daemon error | Start Docker, or point the run at a Postgres of your own with `--env BOT_DB_DSN=<dsn>` |
| The bot exited during startup with `Bot store unavailable` naming a migration | Another branch migrated the smoke database. Run `up --fresh-db` |
| The guild `looks like a live server` | `GUILD_ID` in `.env` names the wrong guild |
| `.env` `has no value for` a variable | Add it to the main checkout's `.env` |
| Any other exit during startup | Read the log tail `up` printed; the README's troubleshooting table covers the startup panics |
| The forum says the redirect URI is not valid, with `--forum real` | The forum client is missing `http://localhost:8080/auth/callback`. Ask the person to register it |
