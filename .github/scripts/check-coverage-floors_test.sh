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
case_name=""

# run_check <floors> <coverage text>: runs the script, sets $out and $code.
run_check() {
    printf '%s\n' "$1" >"$tmp/floors.tsv"
    set +e
    out=$(printf '%s\n' "$2" | "$script" "$tmp/floors.tsv" 2>&1)
    code=$?
    set -e
}

fail() {
    echo "FAIL: $case_name: $*" >&2
    while IFS= read -r l; do printf '    | %s\n' "$l"; done <<<"$out" >&2
    failures=$((failures + 1))
}

case_name="a package 3 points above its floor passes without RAISE"
run_check $'# floors for the fixture\nexample.com/app/a\t81' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 84.0% of statements'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
if grep -q '^RAISE' <<<"$out"; then fail "printed a RAISE line"; fi

case_name="a package more than 3 points above its floor gets a RAISE to one under its coverage"
run_check $'example.com/app/a\t86\nexample.com/app/b\t81' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements
ok  \texample.com/app/b\t0.5s\tcoverage: 84.6% of statements'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
grep -Eq '^RAISE.*example\.com/app/b[[:space:]].*[^0-9.]83([^0-9.]|$)' <<<"$out" ||
    fail "no RAISE line naming example.com/app/b and 83"

case_name="a package below its floor fails"
run_check $'example.com/app/a\t86\nexample.com/app/b\t81' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements
ok  \texample.com/app/b\t0.5s\tcoverage: 79.9% of statements'
[[ $code -eq 1 ]] || fail "exit $code, want 1"
grep -Eq '^FAIL.*example\.com/app/b[[:space:]]' <<<"$out" ||
    fail "no FAIL line naming example.com/app/b"

case_name="a tracked package missing from the test output fails"
run_check $'example.com/app/a\t86\nexample.com/app/gone\t50' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements'
[[ $code -eq 1 ]] || fail "exit $code, want 1"

case_name="a tested package with no floor fails with a NO FLOOR line naming one under its coverage"
run_check $'example.com/app/a\t86' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements
ok  \texample.com/app/b\t0.5s\tcoverage: 66.7% of statements'
[[ $code -eq 1 ]] || fail "exit $code, want 1"
grep -Eq '^NO FLOOR.*example\.com/app/b[[:space:]].*[^0-9.]65([^0-9.]|$)' <<<"$out" ||
    fail "no NO FLOOR line naming example.com/app/b and 65"

case_name="a tested package under 1% coverage with no floor gets a NO FLOOR of 0"
run_check $'example.com/app/a\t86' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements
ok  \texample.com/app/b\t0.1s\tcoverage: 0.5% of statements'
grep -Eq '^NO FLOOR.*example\.com/app/b[[:space:]].*[^0-9.]0([^0-9.]|$)' <<<"$out" ||
    fail "no NO FLOOR line naming example.com/app/b and 0"

# go test prints these for code without tests, tests that cover no statements,
# and no tests and no statements.
case_name="a package with no coverage percentage on an ok line needs no floor"
run_check $'example.com/app/a\t86' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements
\texample.com/app\t\tcoverage: 0.0% of statements
ok  \texample.com/app/empty\t0.2s\tcoverage: [no statements]
?   \texample.com/app/none\t[no test files]'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
if grep -q '^NO FLOOR' <<<"$out"; then fail "printed a NO FLOOR line"; fi

case_name="the module's root package needs a floor once it has tests"
run_check $'example.com/app/a\t86' \
    $'ok  \texample.com/app/a\t0.5s\tcoverage: 87.0% of statements
ok  \texample.com/app\t0.4s\tcoverage: 40.2% of statements'
[[ $code -eq 1 ]] || fail "exit $code, want 1"
grep -Eq '^NO FLOOR.*example\.com/app[[:space:]].*[^0-9.]39([^0-9.]|$)' <<<"$out" ||
    fail "no NO FLOOR line naming example.com/app and 39"

if [[ $failures -ne 0 ]]; then
    echo "$failures check-coverage-floors test(s) failed" >&2
    exit 1
fi
echo "check-coverage-floors tests passed"
