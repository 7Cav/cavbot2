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

The ready block's `recorders` line counts the recorders that connected. A
check needs one for each channel it records at the same time. The `mixes`
line says whether the bot can build a mix. When your list has a recording
check and those lines say it can't run, set up what the first-run note under
"When `up` fails" lists, then `down` and `up` again. If you can't,
write the check down as unreachable, with the line as its reason.
`go run ./tools/smoke status` shows the same lines while the run is up, with
the recorders counted so far. A recorder whose first login failed counts once
it logs in, and one that logs back in after a cut short still counts once.

Done when `up` prints `ready` and the build, such as `smoke-09dccef`, and
every recording check on your list can run or has its reason.

## 3. Run each check

Drive each check the way your list says, and write down what you saw.

**Panel.** Open <http://localhost:8080> in the built-in browser pane with
`preview_start`, sign in through the fake forum, and pick the persona the
check needs:

| Persona | Lands on |
|---------|----------|
| Panel admin | The Hubs page, and opens the Foxhole and Settings pages too |
| Foxhole manager | The Foxhole page |
| Outsider | The Recordings page |

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

**Unreachable voice servers.** A recording check that needs the recorder cut
off from its call's voice server starts the run with `go run ./tools/smoke up
--env SMOKE_VOICE_DNS_FAIL_FILE=/tmp/cavbot2-voice-dns-fail`. While that file
exists, every lookup a recorder makes of a voice server answers "no such
host", from the host's resolver and the zone's nameservers alike. A join fails then
too, so create the file only once the recording runs, then move the call with
`smoke api`. Remove the file to let the recorder reach the server, and before
`down`, so the next run with the flag doesn't start cut off.

**Dropped voice connections.** A recording check that needs the recorder's
voice connection to drop mid-recording starts the run with `go run
./tools/smoke up --env SMOKE_VOICE_DROP_FILE=/tmp/cavbot2-voice-drop`. Each
time that file appears, every voice connection the recorders hold then drops,
and the bot log gets one "Smoke hook dropping voice connections" WARN line for
each recorder, however many times it has logged in. The line names the
recorder by its place in `RECORDER_TOKENS`. A file that says `silent` drops
them silently, as a network that stops passing traffic, audio included. The
recorder hears nothing more, and notices once the voice server has gone its
heartbeat interval plus 3 s without answering a heartbeat. An empty file or
any other content breaks the voice gateway connections abruptly, which the
recorder notices at once. Connections made after the file appears stay up, so
the recorder can rejoin. Only the file's appearance counts, so remove it
before the next drop, and before `down`.

**Cav members.** The test guild has none of the rank roles the code holds for
production, so no test account counts as a Cav member until a panel admin sets
a rank role. A check that needs a Cav member, such as owning a spawned channel
or running `/record start`, starts on the Settings page. Sign in as the panel
admin and give one rank, such as SGT, a test guild role. Clear the ranks whose
roles show as not in the server, then save. Give the test account that role
with `smoke api`, and write the grant down as a fixture. The smoke database
keeps the rank roles between runs, so look at the Settings page before you set
them again. Until you clear those ranks, every start logs a "Rank roles
missing from the guild" capture. Expect it on the test guild.

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
fix or they revert the PR. Either way the PR keeps `needs-smoke`, so the
release waits too, until a later pass shows the fix working or the revert
merges.

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
contents stay out of the PR. Then remove `needs-smoke` from each PR whose
checks all passed.

Done when every PR the pass covered has its comment, and `needs-smoke` stays
only on PRs with a failed check or checks the pass could not run. For the
second kind, tell the person why so they can run them or waive them. A waiver
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
to it, and a filled-in `.env` at the main checkout's root. Recording checks
also need:

- A recorder application invited to the test guild, with its token in
  `RECORDER_TOKENS`. The README's
  [Recorder applications](../README.md#recorder-applications) section sets one
  up.
- A second recorder, for checks that record two channels at once.
- ffmpeg with the libopus encoder on the `PATH` the bot starts with.
  `ffmpeg -hide_banner -encoders | grep libopus` shows whether a build has it.
  `up --env PATH=<dir>:$PATH` points the bot at one in `<dir>`.
