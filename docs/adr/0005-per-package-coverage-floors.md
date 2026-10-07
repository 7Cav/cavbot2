# ADR 0005: Per-package coverage floors with ratchet policy

## Status

Accepted (0.7.6 / PR #75). **Amended 2026-10-07**: the rule for raising a
floor changed; see *Amendment: floors track coverage, not each PR's gain*.
Per-package floors are unchanged.

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
