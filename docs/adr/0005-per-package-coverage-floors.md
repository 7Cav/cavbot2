# ADR 0005: Per-package coverage floors with ratchet policy

## Status

Accepted (0.7.6 / PR #75). Amended 2026-10-06: a floor now stays within 3
points of its package's coverage, and the floor script computes the new floor.

## Decision

CI enforces **per-package** coverage floors. `.github/coverage-floors.tsv`
lists each tracked package and its floor, and
`.github/scripts/check-coverage-floors.sh` fails when a package's coverage in
the `go test -cover` output is below its floor. `main` is excluded.

A floor stays at most 3 points below its package's coverage. When coverage is
more than 3 points above the floor, the script prints a RAISE line with the new
floor: the whole-number part of the coverage, minus 1. Set it in the same PR.

## Why

A single global floor either sandbags well-covered packages or false-alarms
on refactor dips elsewhere. A floor just below each package's coverage leaves
room for a refactor dip and still fails on a real loss.

The first version said to raise a floor "when a PR raises a package's coverage
by more than a point or two". Agents read that two ways. #458 and #459 compared
their own gain to two points and left `commands` at a floor of 81 with its
coverage at 94.8%. #460 set `panel`'s floor just under its coverage. Under the
first reading, `commands` later fell to 88.0% and no check failed. A rule
measured from the floor needs no baseline run of `develop`, and the script
applies it the same way for every reader.

RAISE does not fail the check. Two PRs merged close together can each gain
less than 3 points and pass that line together. A failing check would then
turn `develop` red over a gain in coverage and block every open PR,
Dependabot's included, until someone raised the floor.

## How to apply

- `.github/scripts/gate.sh` runs the check after the suite, against a Postgres
  it starts, the same way CI does. The `store` floor holds only when its
  Postgres tests run.
- When the check prints RAISE, set those floors in `.github/coverage-floors.tsv`
  in the same PR.
- A new package gets a floor in the PR that adds it, by the same rule.
