# Smoke tests run before a release, not before a merge

Status: accepted, 2026-10-07. Replaces the rule that a change members can see
is smoke-tested before it merges.

A change a member or panel user can see still gets a smoke test on the test
guild, but the test now comes before the release that ships the change, not
before the change merges. The PR lists its checks and carries the
`needs-smoke` label. A smoke pass on `develop` runs the lists of every merged
PR still labelled, and a release waits until none is. A change reaches members
only through a published release, since `build_and_push.yml` deploys on
nothing else, so the release is where the test protects them. The old gate
dates from when the bot had no panel and the maintainer sat in on every
change. An agent working through the backlog unattended can't clear it.
Slash-command checks post from the maintainer's Discord account and need their
consent on each run, so every PR members can see would sit open until the
maintainer came back.

## Considered options

- **Keep the gate and let unattended PRs wait.** Each waiting PR falls behind
  `develop`, which only merges branches that are up to date, and each still
  needs its own smoke run once the maintainer is back.
- **Have the unattended agent run the checks it can reach**, the panel and
  `smoke api`, and leave slash commands for later. A later pass is still
  needed for the rest. The smoke tool also allows one bot on the test guild
  across all worktrees, so an unattended run holding it blocks the
  maintainer's own sessions.

## Consequences

- `develop` can hold changes nobody has smoke-tested. Until a pass clears the
  list, its tip is unproven on the test guild.
- A pass on `develop` tests the merged changes together, as the release ships
  them.
- A failed check is a bug in merged code. Before the next release, the
  maintainer has it fixed or reverts the PR.
