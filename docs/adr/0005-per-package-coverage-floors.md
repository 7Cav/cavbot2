# ADR 0005: Per-package coverage floors with ratchet policy

## Status

Accepted (0.7.6 / PR #75).

## Decision

CI enforces **per-package** coverage floors via
`.github/scripts/check-coverage-floors.sh` — a pure-bash script that parses
`go test -cover` on stdin and applies inlined per-package floors. `main` is
excluded.

When a PR raises a package's coverage by more than a point or two, raise its
floor in the same PR.

## Why

A single global floor either sandbags well-covered packages or false-alarms
on refactor dips elsewhere. Per-package floors set just-below baseline let
the suite ratchet up organically without anyone having to remember to bump
a number.

## How to apply

- Local check matches CI: `go test ./... -cover | .github/scripts/check-coverage-floors.sh`.
- After a PR that raises a package's coverage non-trivially, edit the floor
  for that package in the script in the same PR. That's the ratchet.
- Don't add new packages without a starter floor — even `1%` is enough to
  begin the ratchet.
