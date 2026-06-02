#!/usr/bin/env bash
# .github/scripts/check-coverage-floors.sh
#
# Reads `go test -cover` output on stdin and fails (exit 1) if any tracked
# package's coverage is below its declared floor. Tracked packages and floors
# are inlined below — when a PR adds tests that meaningfully raise a package's
# coverage, the floor should be raised in the same PR.
#
# The `main` package is intentionally not tracked: it's entrypoint wiring
# (DISCORD_TOKEN check, gateway open, command registration) and is not
# realistically unit-testable without a substantial refactor.

set -euo pipefail

# Inline floor table: pkg<TAB>min-percent. Bump these when tests land.
# `read -d '' ... || true`: -d '' reads until NUL; since no NUL appears in the
# heredoc, read returns 1 at EOF. Without `|| true`, `set -e` would kill the
# script before any check runs. Don't "clean up" the `|| true`.
read -r -d '' FLOORS <<'EOF' || true
github.com/7cav/cavbot2/utils	84
github.com/7cav/cavbot2/commands	77
EOF

# Read `go test -cover` output from stdin.
output=$(cat)

fail=0
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
            echo "OK:   $pkg coverage ${cov}% >= floor ${floor}%"
        fi
    else
        echo "ERROR: could not parse coverage for $pkg from: $line" >&2
        fail=1
    fi
done <<<"$FLOORS"

if [[ $fail -ne 0 ]]; then
    echo
    echo "Coverage floor check failed. To raise a floor, edit this script:"
    echo "  .github/scripts/check-coverage-floors.sh"
    exit 1
fi
