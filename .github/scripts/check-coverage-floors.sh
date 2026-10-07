#!/usr/bin/env bash
# .github/scripts/check-coverage-floors.sh <floors-file>
#
# Reads `go test -cover` output on stdin and fails (exit 1) if any tracked
# package's coverage is below its floor. The floors file lists the tracked
# packages, one per line: import path, a tab, the floor in percent. Lines
# starting with # are comments. gate.sh passes .github/coverage-floors.tsv.
#
# A package more than 3 points above its floor gets a RAISE line naming its new
# floor: the whole-number part of its coverage, minus 1. RAISE does not change
# the exit code. ADR 0005 says to apply it in the same PR.
#
# The `main` package is intentionally not tracked: it's entrypoint wiring
# (DISCORD_TOKEN check, gateway open, command registration) and is not
# realistically unit-testable without a substantial refactor.
#
# The `store` floor assumes its Postgres tests ran. They skip when
# TEST_BOT_DB_DSN is unset, and the Fake alone covers far less, so a local run
# with no database fails this check for `store` alone. gate.sh starts a
# Postgres and sets the variable.

set -euo pipefail

floors_file=${1:?usage: check-coverage-floors.sh <floors-file> < go-test-output}
FLOORS=$(grep -v '^#' "$floors_file")

# Read `go test -cover` output from stdin.
output=$(cat)

fail=0
raise=()
while IFS=$'\t' read -r pkg floor; do
    [[ -z "$pkg" ]] && continue
    # Extract the "coverage: X.X% of statements" for this package.
    line=$(grep -E "^(ok|---)[[:space:]]+${pkg//./\\.}([[:space:]]|$)" <<<"$output" || true)
    if [[ -z "$line" ]]; then
        echo "ERROR: package $pkg not found in test output" >&2
        fail=1
        continue
    fi
    if [[ "$line" =~ coverage:[[:space:]]+([0-9]+\.[0-9]+)% ]]; then
        cov="${BASH_REMATCH[1]}"
        if awk -v c="$cov" -v f="$floor" 'BEGIN { exit (c < f) ? 0 : 1 }'; then
            echo "FAIL: $pkg coverage ${cov}% < floor ${floor}%" >&2
            fail=1
        else
            margin=$(awk -v c="$cov" -v f="$floor" 'BEGIN { printf "%.1f", c - f }')
            echo "OK:   $pkg coverage ${cov}% >= floor ${floor}%, margin ${margin}"
            if awk -v c="$cov" -v f="$floor" 'BEGIN { exit (c - f > 3) ? 0 : 1 }'; then
                raise+=("RAISE $pkg to $((${cov%.*} - 1)) (floor ${floor}%, coverage ${cov}%)")
            fi
        fi
    else
        echo "ERROR: could not parse coverage for $pkg from: $line" >&2
        fail=1
    fi
done <<<"$FLOORS"

if [[ ${#raise[@]} -gt 0 ]]; then
    echo
    printf '%s\n' "${raise[@]}"
    echo "Coverage is more than 3 points above these floors. Set each to the"
    echo "number given, in $floors_file, in this PR (ADR 0005)."
fi

if [[ $fail -ne 0 ]]; then
    echo
    echo "Coverage floor check failed. Floors live in $floors_file."
    exit 1
fi
