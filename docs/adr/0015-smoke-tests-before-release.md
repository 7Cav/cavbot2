---
status: accepted
---

# Smoke tests run before a release, not before a merge

A change members can see now merges once CI passes and gets its test-guild
smoke test before the release that ships it, because a published release is
the only way a change reaches members. The old pre-merge gate dates from when
the maintainer sat in on every change. An agent working the backlog
unattended can't clear it, since slash-command checks post from the
maintainer's Discord account and need their consent on each run.

## Considered options

- **Have the unattended agent run the checks it can reach**, the panel and
  `smoke api`, and leave slash commands for later. A later pass is still
  needed for the rest, and the smoke tool runs one bot across all worktrees,
  so an unattended run holding it blocks the maintainer's own sessions.
