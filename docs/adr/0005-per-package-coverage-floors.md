# ADR 0005: Per-package coverage floors with ratchet policy

## Status

Accepted (0.7.6 / PR #75). **Amended 2026-10-07**: the rule for raising a
floor changed; see *Amendment: floors track coverage, not each PR's gain*.
**Amended 2026-10-08**: a tested package with no floor fails the check.
**Amended 2026-10-08**: floors come from the toolchain `go.mod` pins.
*Superseded by the 2026-10-27 amendment.*
**Amended 2026-10-27**: floors come from the Go the `Dockerfile`'s `golang`
tag names. Per-package floors are unchanged.

## Decision

CI enforces **per-package** coverage floors via
`.github/scripts/check-coverage-floors.sh` — a pure-bash script that parses
`go test -cover` on stdin and applies inlined per-package floors. `main` is
excluded.

When a PR raises a package's coverage by more than a point or two, raise its
floor in the same PR. *Superseded: see the amendment below.*

## Why

A single global floor either sandbags well-covered packages or false-alarms
on refactor dips elsewhere. Per-package floors set just-below baseline let
the suite ratchet up organically without anyone having to remember to bump
a number.

## How to apply

*Superseded by the amendment's How to apply.*

- Local check matches CI: `go test ./... -cover | .github/scripts/check-coverage-floors.sh`.
  Since the store package landed (#299) this holds only with `TEST_BOT_DB_DSN` exported: the `store`
  package's Postgres tests skip without it and the package falls under its
  floor. The CI gate block in `CLAUDE.md` starts a throwaway Postgres for that.
- After a PR that raises a package's coverage non-trivially, edit the floor
  for that package in the script in the same PR. That's the ratchet.
- Don't add new packages without a starter floor — even `1%` is enough to
  begin the ratchet.

## Amendment: floors track coverage, not each PR's gain (2026-10-07)

The raise rule above was read two ways. One reading measured a PR's own gain
against "a point or two" and left the floor alone below that. The other kept
the floor just below coverage, as *Why* intends. Under the first, a run of
small gains never moves a floor, so the gap between floor and coverage keeps
growing and a loss of that size passes CI. `commands` sat 13 points above its
floor of 81, then lost almost 7 points of coverage without a failed check.

### Decision

A floor stays within 3 points of its package's coverage. When coverage is more
than 3 points above the floor, the floor moves to the whole-number part of the
coverage minus 1, in the same PR. The floor check computes and prints that
number, so every reader gets the same one.

Coverage above that line never fails the check. Only a package below its floor
fails.

Floors come from coverage on the Go version CI runs, the one `go.mod` names.
*Superseded by the 2026-10-08 toolchain amendment.*
The same code covers differently under different Go versions: `commands`
covers 86.4% under 1.26.0 and 88.1% under 1.27.1, more than the 1 to 2 points
a fresh floor leaves.

### Considered options

- A rule based on each PR's gain, with a sharper threshold. Every PR would
  need a baseline run of `develop` to measure its gain, and floors would still
  fall behind coverage one small gain at a time.
- Failing the check when coverage passes the 3-point line. Two PRs merged
  close together can each stay under it and cross it together. `develop`
  would then go red over a coverage gain and block every open PR, Dependabot's
  included, until someone raised the floor.

### How to apply

- `.github/scripts/gate.sh` runs the floor check the way CI does: on CI's Go
  version, after the full suite, against a Postgres it starts. The `store`
  floor holds only when its Postgres tests run.
- Floors live in `.github/coverage-floors.tsv`. When the check prints a RAISE
  line, set that floor in the same PR.
- A new package gets a floor in the PR that adds it, by the same rule.
  *Superseded by the 2026-10-08 amendment.*

## Amendment: a tested package with no floor fails the check (2026-10-08)

A package whose tests report a percentage needs a floor. The check fails one
that has none and prints a `NO FLOOR` line naming the floor to set, by the
RAISE rule but never below 0. A package without tests needs no floor, since a
floor of 0 guards nothing, so `main` needs no exemption by name. Unlike RAISE,
this fails the check. Only a PR that adds a package's first test, or deletes
its floor, can cause it, so `develop` never goes red over it (#482).

## Amendment: floors come from the toolchain `go.mod` pins (2026-10-08)

`go.mod` pins one exact Go in its `toolchain` line. CI's jobs, the gate and the
image's builder all run on it, so floors come from coverage on that toolchain.
The `go` directive stays at 1.26.0, because it also sets the binary's GODEBUG
defaults. A PR that moves to a new Go changes the `toolchain` line and the
Dockerfile's `golang` tag together, and sets the floors its RAISE lines name
(#483). *Superseded by the 2026-10-27 amendment.*

## Amendment: the `Dockerfile`'s `golang` tag names the Go (2026-10-27)

The builder stage's `golang:X.Y.Z` tag in the `Dockerfile` names the one Go
that CI's Lint and Build jobs, the gate and the image run on, so floors come
from coverage on that Go. A PR that moves to a new Go changes that tag alone.
`go.mod` keeps its `go 1.26.0` line for the GODEBUG defaults and has no
`toolchain` line. The gate fails when one appears (#899).

The tag is the only line Dependabot can bump. Its `docker` updates move the
tag and nothing else, and it has no way to move a `toolchain` line
(https://redirect.github.com/dependabot/dependabot-core/issues/13520). With
both lines, every golang bump failed `Build` until someone moved the
`toolchain` line by hand, as on #897. Now a golang bump merges through
auto-merge on its own unless the new Go puts a package under its floor, and
then the maintainer sets that floor by hand.

`go get` writes the `toolchain` line back when it raises the `go` line, as a
Dependabot `gomod` update can. The gate fails that update on purpose. Raising
the `go` line also moves the GODEBUG defaults, so a person should look at it.

### Considered options

- Keep both lines, with a workflow that moves the `toolchain` line on
  Dependabot's PR. A commit pushed with `GITHUB_TOKEN` waits for a person to
  approve its CI run, and a `workflow_dispatch` run can't satisfy a required
  check, so the workflow would need a GitHub App token. The maintainer turned
  the App down. Auto-merge is there to cut the PRs they handle, not to ship
  sooner, and setting a floor by hand now and then is fine.
