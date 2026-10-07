#!/usr/bin/env bash
# .github/scripts/check-coverage-floors_test.sh
#
# Tests for check-coverage-floors.sh, fed a fixture floors file and fixture
# `go test -cover` output. gate.sh runs this file.

set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/check-coverage-floors.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

failures=0
current=""

# check <floors> <coverage text>: runs the script, sets $out and $code.
check() {
    printf '%s\n' "$1" >"$tmp/floors.tsv"
    set +e
    out=$(printf '%s\n' "$2" | "$script" "$tmp/floors.tsv" 2>&1)
    code=$?
    set -e
}

fail() {
    echo "FAIL: $current: $*" >&2
    while IFS= read -r l; do printf '    | %s\n' "$l"; done <<<"$out" >&2
    failures=$((failures + 1))
}

current="a package within 3 points above its floor passes without RAISE"
check $'example.com/app/a\t81' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 82.5% of statements'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
if grep -q '^RAISE' <<<"$out"; then fail "printed a RAISE line"; fi

current="a package more than 3 points above its floor gets a RAISE to one under its coverage"
check $'example.com/app/a\t86\nexample.com/app/b\t81' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements
ok  \texample.com/app/b\t0.5s\tcoverage: 94.8% of statements'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
grep -Eq '^RAISE.*example\.com/app/b[[:space:]].*[^0-9.]93([^0-9.]|$)' <<<"$out" ||
    fail "no RAISE line naming example.com/app/b and 93"

current="a package below its floor fails"
check $'example.com/app/a\t86\nexample.com/app/b\t81' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements
ok  \texample.com/app/b\t0.5s\tcoverage: 79.9% of statements'
[[ $code -eq 1 ]] || fail "exit $code, want 1"
grep -Eq '^FAIL.*example\.com/app/b[[:space:]]' <<<"$out" ||
    fail "no FAIL line naming example.com/app/b"

current="a tracked package missing from the test output fails"
check $'example.com/app/a\t86\nexample.com/app/gone\t50' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements'
[[ $code -eq 1 ]] || fail "exit $code, want 1"

if [[ $failures -ne 0 ]]; then
    echo "$failures check-coverage-floors test(s) failed" >&2
    exit 1
fi
echo "check-coverage-floors tests passed"
