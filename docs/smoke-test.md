# Smoke test

A smoke test runs a change on the test guild, where the bot is on the live
gateway and the panel is in a real browser. CI reaches neither. Every change a
member or panel user can see gets one before the release that ships it: a
slash command, a panel page, or what the bot does to channels, roles and
messages (ADR 0015).

A smoke test has two halves. The PR that makes the change writes its check
list (step 1) and carries the `needs-smoke` label. A smoke pass (steps 2 to 6)
runs the check lists of one or more PRs on one build: an open PR's branch, or
`develop` for every merged PR still labelled `needs-smoke`. Before a release,
run passes on `develop` until no merged PR carries the label.

You run every step of a pass and write the report. The person you work for
comes in at two points only, and the step that needs them says so: consent on
the real forum, and slash commands that post as them.

## 1. Write the check list

List each behavior the change alters that a member or panel user would notice.
A check is an action and the result you expect, such as "Foxhole manager
presses Purge External, and the preview lists every External holder." Note how
you will drive each check: the panel, `smoke api`, or a slash command. When a
live run cannot reach a check, such as a state only a unit test can force,
write the reason next to it.

Put the list in the PR body under a **Smoke checks** heading and add the
`needs-smoke` label. Whoever runs the pass may come to it days later with only
that section, so each check names its persona or account and the result to
expect.

Done when every changed behavior in the diff has a check or a reason, and the
PR carries `needs-smoke`.

## 2. Start a pass

Pick the build. For one open PR, check out its branch with every change
committed. For merged PRs, bring a clean checkout of `develop` up to
`origin/develop` and list the PRs still waiting. One pass covers them all, so
collect each one's **Smoke checks** section.

```bash
gh pr list --state merged --label needs-smoke --json number,title --jq '.[] | "#\(.number) \(.title)"'
```

Then start the bot:

```bash
go run ./tools/smoke up
```

`up` builds this worktree and starts the bot on the test guild, with the panel
at <http://localhost:8080>. It reads the credentials from the main checkout's
`.env` and prints variable names only. One run at a time exists across all
worktrees, since one bot token is one gateway connection. When another worktree holds
the run, `up` names it, and `go run ./tools/smoke down` stops it from
anywhere. Start every bot through the tool, so the lock covers it.
`go run ./tools/smoke help` lists the commands and flags.

Done when `up` prints `ready` and the build, such as `smoke-09dccef`.

## 3. Run each check

Drive each check the way your list says, and write down what you saw.

**Panel.** Open <http://localhost:8080> in the built-in browser pane with
`preview_start`, sign in through the fake forum, and pick the persona the
check needs:

| Persona | Lands on |
|---------|----------|
| Panel admin | The Hubs page, and opens the Foxhole page too |
| Foxhole manager | The Foxhole page |
| No access | The no-access page |

Sign out from the rail to switch persona. `go run ./tools/smoke forum
expired`, `refused` or `down` makes the forum answer the next group check with
a 401, a 403 or nothing, which brings up the session-ended, refused and
forum-unavailable pages. `forum ok` restores it.

**Sign-in itself.** When the change touches the auth handlers in
`panel/panel.go`, `panel/forum.go` or the OAuth settings, start the run with
`up --forum real`, which signs in through 7cav.us. Ask the person once per run
to approve your Authorize clicks, restarts included. If the pane shows the
forum's login page, ask them to sign in there.

**Discord state.** `go run ./tools/smoke api METHOD PATH [JSON]` calls Discord
as the bot, with `{guild}` standing for the test guild's ID. Use it to set up
fixtures, such as roles and nicknames, and to read results from roles, the
audit log and the registered commands. Write down each fixture as you make it,
for step 5.

**Slash commands.** A slash command runs from a person's Discord account. Ask
the person once per run to let you post as them, then use Claude in Chrome,
where their Discord stays signed in. Confirm the channel is in the test guild
before typing. Read the reply on the page, the one place an ephemeral reply
shows. Keep the window at its size, since resizing threw off clicks in earlier
runs.

Done when every check has a result: what you did, as which persona or account,
and what you saw.

## 4. Handle a failed check

On an open PR's branch, fix the code, commit, then `down` and `up`. A restart
ends every panel session, as in production, so sign in again. Run the failed
checks and every check the fix could affect.

On `develop` the change has already merged. File an issue labelled `bug` and
`needs-triage` for each failed check: the check, what you saw, the build, and
the PR it came from. The person decides whether the next release waits for a
fix or the PR is reverted.

On an open PR's branch, done when every check passes on one build without a
`-dirty` suffix, so the build names a commit the PR holds. On `develop`, done
when every failed check has its issue.

## 5. Clean up

Run `go run ./tools/smoke down`. Undo every fixture on your list. Leave
channels, and anything else only a delete removes, for the person to delete.
Close the pane's tab with `tabs_close`.

Done when `go run ./tools/smoke status` prints `no smoke run is up` and every
fixture is undone.

## 6. Report

Comment on each PR the pass covered: the build, each check with its persona or
account and result, each unreachable check with its reason, and the issue for
each failure. A comment works on open and merged PRs alike, and a merged PR's
body already became its squash commit. This repository is public, so name
people by persona or role. Forum usernames, user IDs, IP addresses and channel
contents stay out of the PR. Then remove the PR's `needs-smoke` label.

Done when every PR the pass covered has its comment and no longer carries
`needs-smoke`. When a pass could not run a PR's checks at all, its label stays,
and you tell the person why so they can run them or waive them. A waiver
removes the label too.

## When `up` fails

`up` prints why. These fixes are not in its messages:

| `up` says | Fix |
|-----------|-----|
| Port 8080 or 8091 is in use | Stop the process it names, usually a bot started outside the tool |
| The bot exited with `Bot store unavailable`, naming a migration | Another branch migrated the smoke database. Run `up --fresh-db` |
| The forum says the redirect URI is not valid, with `--forum real` | Ask the person to register `http://localhost:8080/auth/callback` on the forum client |
| Any other exit during startup | Use the README's troubleshooting table |

A first run needs the README's setup steps 1 to 3: a test guild, a bot invited
to it, and a filled-in `.env` at the main checkout's root.
