#!/usr/bin/env bash
# .github/scripts/check-migrations.sh <base-ref> <dir> <now>
#
# Fails (exit 1) when the migrations in <dir>, as the working tree holds them,
# break a rule against the ones on <base-ref>, develop. A migration is a *.sql
# file in <dir>, and its number is the digits its name starts with. <now> is
# the current UTC time as YYYYMMDDhhmmss, how `goose create` numbers a file.
# gate.sh passes `date -u +%Y%m%d%H%M%S`.
#
# The rules, for a file the branch adds and develop does not have:
#   - it sorts above every migration on develop, compared as numbers;
#   - it is numbered no later than <now>.
# And for every migration on develop: the branch keeps it byte for byte.
#
# goose refuses an unapplied migration numbered below the highest one a
# database has applied, so a file that breaks the first two rules stops the
# bot at startup once a later one has shipped. goose keeps no checksum, so a
# database that applied a migration never runs a later edit to it.
#
# Exits non-zero when <base-ref> names no commit, so a CI run that could not
# fetch develop fails instead of reading it as a develop with no migrations.

set -euo pipefail

# Each rule's phrase, on every line that reports it. The tests read these.
rule_order="sorts at or below the highest migration on develop"
rule_future="is numbered later than now"
rule_changed="changes a migration already on develop"

main() {
    local base=${1:?usage: check-migrations.sh <base-ref> <dir> <now>}
    local dir=${2:?usage: check-migrations.sh <base-ref> <dir> <now>}
    local now=${3:?usage: check-migrations.sh <base-ref> <dir> <now>}

    if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
        echo "FAIL: cannot see develop, because $base names no commit. Run git fetch origin." >&2
        exit 1
    fi

    # One name per line. macOS ships bash 3.2, which has no associative arrays.
    local on_base
    on_base=$(git ls-tree --name-only "$base" "$dir/" | sed -n 's|.*/||; /\.sql$/p')

    local problems=() highest=0 changed="" renumber="" f n path
    while IFS= read -r f; do
        [[ -n $f ]] || continue
        n=$(number "$f")
        if ((n > highest)); then
            highest=$n
        fi
        path=$dir/$f
        if [[ ! -e $path || $(git hash-object "$path") != $(git rev-parse "$base:$path") ]]; then
            problems+=("$f $rule_changed")
            changed=1
        fi
    done <<<"$on_base"

    for path in "$dir"/*.sql; do
        [[ -e $path ]] || continue
        f=${path##*/}
        grep -qxF "$f" <<<"$on_base" && continue
        n=$(number "$f")
        if ((n <= highest)); then
            problems+=("$f $rule_order")
            renumber=1
        fi
        if ((n > 10#$now)); then
            problems+=("$f $rule_future")
            renumber=1
        fi
    done

    if ((${#problems[@]} == 0)); then
        echo "ok: the migrations sort after develop's, none is numbered later than now, and develop's are unchanged"
        return
    fi

    {
        printf 'FAIL: %s\n' "${problems[@]}"
        printf '\nThe highest migration on develop is %s, and now is %s UTC.\n' "$highest" "$now"
        if [[ -n $renumber ]]; then
            cat <<'EOF'

goose refuses a migration numbered below one a database has applied, and the
bot then fails at startup. Rename each file that sorts at or below develop, or
is numbered later than now, to the output of this command, run when you write
the migration or rebase it:

    date -u +%Y%m%d%H%M%S
EOF
        fi
        if [[ -n $changed ]]; then
            cat <<EOF

A database that applied a migration on develop never runs a change to it.
Restore each such file and put the change in a new migration:

    git checkout $base -- $dir/<file>

If the branch never touched the file, it is behind develop: merge $base in.
EOF
        fi
    } >&2
    exit 1
}

# number <file>: the file's number, the digits its name starts with, in base
# 10. A name with none is 0.
number() {
    local digits=${1%%[!0-9]*}
    echo $((10#${digits:-0}))
}

# Run only when executed, so the tests can source the rule phrases.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
