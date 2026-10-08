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
# A package whose tests report a coverage percentage needs a floor. One with no
# floor fails the check with a NO FLOOR line. That line names the floor to set,
# found by the RAISE rule but never below 0. A package without tests needs no
# floor. Today `main` is one of those.
#
# The `store` floor assumes its Postgres tests ran. They skip when
# TEST_BOT_DB_DSN is unset, and the Fake alone covers far less, so a local run
# with no database fails this check for `store` alone. gate.sh starts a
# Postgres and sets the variable.

set -euo pipefail

floors_file=${1:?usage: check-coverage-floors.sh <floors-file> < go-test-output}
FLOORS=$(grep -v '^#' "$floors_file" || true)
if [[ -z "$FLOORS" ]]; then
    echo "ERROR: no floors in $floors_file" >&2
    exit 1
fi

# Coverage more than this many points above a floor gets a RAISE line.
raise_margin=3

# floor_for <coverage>: the floor for a coverage percentage. That is its
# whole-number part, minus 1, and never below 0.
floor_for() {
    local f=$((${1%.*} - 1))
    [[ $f -lt 0 ]] && f=0
    echo "$f"
}

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
            if awk -v c="$cov" -v f="$floor" -v m="$raise_margin" 'BEGIN { exit (c - f > m) ? 0 : 1 }'; then
                raise+=("RAISE $pkg to $(floor_for "$cov") (floor ${floor}%, coverage ${cov}%)")
            fi
        fi
    else
        echo "ERROR: could not parse coverage for $pkg from: $line" >&2
        fail=1
    fi
done <<<"$FLOORS"

# go test reports a tested package's coverage on an `ok` line. Code without
# tests gets a line with no `ok`. Tests that cover no statements get no
# percentage. Neither needs a floor.
floored_pkgs=$(cut -f1 <<<"$FLOORS")
while IFS= read -r line; do
    [[ "$line" =~ ^ok[[:space:]]+([^[:space:]]+)[[:space:]].*coverage:[[:space:]]+([0-9]+\.[0-9]+)%[[:space:]]of[[:space:]]statements ]] || continue
    pkg="${BASH_REMATCH[1]}"
    cov="${BASH_REMATCH[2]}"
    grep -Fxq -- "$pkg" <<<"$floored_pkgs" && continue
    echo "NO FLOOR: $pkg needs a floor of $(floor_for "$cov") (coverage ${cov}%)" >&2
    fail=1
done <<<"$output"

if [[ ${#raise[@]} -gt 0 ]]; then
    echo
    printf '%s\n' "${raise[@]}"
    echo "Coverage is more than $raise_margin points above these floors. Set each to the"
    echo "number given, in $floors_file, in this PR (ADR 0005)."
fi

if [[ $fail -ne 0 ]]; then
    echo
    echo "Coverage floor check failed. Floors live in $floors_file."
    exit 1
fi
