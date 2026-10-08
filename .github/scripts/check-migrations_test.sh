#!/usr/bin/env bash
# .github/scripts/check-migrations_test.sh
#
# Tests for check-migrations.sh, run in a fixture repo whose origin/develop
# holds the base migrations. gate.sh runs this file.

set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/check-migrations.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The fixture repo's commits must not pick up the caller's signing or hooks.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

failures=0
case_name=""
repo=""

# base <file>...: makes a fresh repo whose origin/develop holds each named
# migration, and checks it out. Each file's text names the file.
base() {
    repo=$tmp/repo-$((++repos))
    git init -q -b develop "$repo"
    mkdir -p "$repo/store/migrations"
    local f
    for f in "$@"; do
        printf -- '-- +goose Up\n-- %s\nSELECT 1;\n' "$f" >"$repo/store/migrations/$f"
    done
    git -C "$repo" add -A
    git -C "$repo" -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m base
    git -C "$repo" update-ref refs/remotes/origin/develop HEAD
}
repos=0

# add <file>: writes a new migration into the fixture's working tree.
add() {
    printf -- '-- +goose Up\nSELECT 1;\n' >"$repo/store/migrations/$1"
}

# run_check <now> [base ref]: runs the script in the fixture repo against
# origin/develop, or the ref given. Sets $out and $code.
run_check() {
    set +e
    out=$(cd "$repo" && "$script" "${2:-origin/develop}" store/migrations "$1" 2>&1)
    code=$?
    set -e
}

fail() {
    echo "FAIL: $case_name: $*" >&2
    while IFS= read -r l; do printf '    | %s\n' "$l"; done <<<"$out" >&2
    failures=$((failures + 1))
}

# fired <rule> <file>: whether $out has a line naming both <file> and the
# script's phrase for <rule> (order, future or changed).
fired() {
    local phrase
    phrase=$(
        # shellcheck source=/dev/null # read for its rule_* phrases only
        source "$script"
        var=rule_$1
        printf '%s' "${!var}"
    ) || true
    [[ -n $phrase ]] || {
        echo "check-migrations.sh defines no rule_$1" >&2
        return 1
    }
    grep -F "$phrase" <<<"$out" | grep -qF "$2"
}

case_name="a branch with no migration changes passes"
base 20260915000000_hubs.sql 20261007053151_roster.sql
run_check 20261008120000
[[ $code -eq 0 ]] || fail "exit $code, want 0"

# What `date -u +%Y%m%d%H%M%S` prints the moment the gate runs.
case_name="a migration numbered at now passes"
base 20260915000000_hubs.sql 20261007053151_roster.sql
add 20261008120000_today.sql
run_check 20261008120000
[[ $code -eq 0 ]] || fail "exit $code, want 0"

case_name="two added migrations that each sort after develop's pass"
base 20260915000000_hubs.sql 20261007053151_roster.sql
add 20261008090000_first.sql
add 20261008100000_second.sql
run_check 20261008120000
[[ $code -eq 0 ]] || fail "exit $code, want 0"

case_name="a migration numbered a day after now fails and names the file"
base 20260915000000_hubs.sql 20261007053151_roster.sql
add 20261009120000_tomorrow.sql
run_check 20261008120000
[[ $code -eq 1 ]] || fail "exit $code, want 1"
fired future 20261009120000_tomorrow.sql || fail "no future line naming 20261009120000_tomorrow.sql"

# Same run. Neither number below is part of the file's name.
case_name="a failure prints develop's highest number, now, and the command that numbers a migration"
grep -qF 20261007053151 <<<"$out" || fail "does not print develop's highest, 20261007053151"
grep -qF 20261008120000 <<<"$out" || fail "does not print now, 20261008120000"
grep -qF 'date -u +%Y%m%d%H%M%S' <<<"$out" || fail "does not print date -u +%Y%m%d%H%M%S"

case_name="a migration numbered at develop's highest fails and names the file"
base 20260915000000_hubs.sql 20261007053151_roster.sql
add 20261007053151_same.sql
run_check 20261008120000
[[ $code -eq 1 ]] || fail "exit $code, want 1"
fired order 20261007053151_same.sql || fail "no order line naming 20261007053151_same.sql"

# As text, 9 sorts after 2026...; goose compares numbers.
case_name="a migration whose shorter number sorts below develop's fails"
base 20260915000000_hubs.sql
add 9_short.sql
run_check 20261008120000
[[ $code -eq 1 ]] || fail "exit $code, want 1"
fired order 9_short.sql || fail "no order line naming 9_short.sql"

# A database that applied the file never runs the edit, while a fresh one does.
case_name="a comment-only edit to a migration on develop fails and names the file"
base 20260915000000_hubs.sql 20261007053151_roster.sql
printf -- '-- explains the hub table\n' >>"$repo/store/migrations/20260915000000_hubs.sql"
run_check 20261008120000
[[ $code -eq 1 ]] || fail "exit $code, want 1"
fired changed 20260915000000_hubs.sql || fail "no changed line naming 20260915000000_hubs.sql"

# The new name passes as an added file, so only the missing old name fails.
case_name="renaming a migration on develop fails and names the old file"
base 20260915000000_hubs.sql 20261007053151_roster.sql
git -C "$repo" mv store/migrations/20261007053151_roster.sql store/migrations/20261008110000_roster.sql
run_check 20261008120000
[[ $code -eq 1 ]] || fail "exit $code, want 1"
fired changed 20261007053151_roster.sql || fail "no changed line naming 20261007053151_roster.sql"

# CI checks out one commit. If the fetch of develop goes missing, the check
# must not read an absent develop as one with no migrations.
case_name="a base ref that names no commit fails"
base 20261007053151_roster.sql
run_check 20261008120000 origin/missing
[[ $code -ne 0 ]] || fail "exit 0, want non-zero"

if [[ $failures -ne 0 ]]; then
    echo "$failures check-migrations test(s) failed" >&2
    exit 1
fi
echo "check-migrations tests passed"
