#!/usr/bin/env bash
# .github/scripts/gate.sh
#
# The CI gate. CI's build job runs this script, so a pass here is a pass in
# CI. Run it from anywhere in the repo before you push. The one exception is
# the migration check: CI fetches develop first, while a local run reads this
# worktree's origin/develop as it stands, so fetch before you trust a local pass.
#
# Steps, in order: the glossary's entry format, lint, the error reply check,
# module tidiness, the image's Go, the tests of the scripts in this directory,
# the migrations against develop's, the test suite with -race and coverage
# against a throwaway Postgres, the coverage floors, the build.
#
# Runs in several worktrees at once without interference. Each run starts its
# own Postgres through test-db.sh and removes only that container on exit, and
# keeps its coverage log and binary in a temp directory. An inherited
# TEST_BOT_DB_DSN is ignored, because the store tests drop the public schema
# before every case and must never reach a database this run did not start.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
scripts=.github/scripts

# Run on the toolchain go.mod pins with its toolchain line. CI's setup-go
# installs that one and the image builds on it, so the gate tests the Go
# production runs. Coverage moves with the toolchain, so another local Go would
# pass floors CI fails. Go fetches the pinned toolchain on first use.
GOTOOLCHAIN=$(awk '$1 == "toolchain" { print $2 }' go.mod)
if [[ -z "$GOTOOLCHAIN" ]]; then
    echo "go.mod has no toolchain line. CI, this gate and the image all build on it." >&2
    exit 1
fi
export GOTOOLCHAIN

work=$(mktemp -d)
worktree=$(basename "$PWD")
db=cavbot2-gate-${worktree//[^a-zA-Z0-9_.-]/-}-$$
cleanup() {
    docker rm -f "$db" >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT
# Turn Ctrl-C and SIGTERM into an exit so the EXIT trap removes the container.
trap 'exit 130' INT
trap 'exit 143' TERM

step() { printf '\n==> %s\n' "$*"; }

# What this run tests, read before any step so a commit made mid-run cannot
# change the summary.
commit=$(git rev-parse --short HEAD)
if [[ -z "$(git status --porcelain)" ]]; then
    tree="working tree clean"
else
    tree="working tree has uncommitted changes"
fi

step "glossary entry format"
# Each GLOSSARY.md entry is a bare `**Term**:` line with its definition below.
# Bulleted entries are the retired style.
bad=$(grep -nHE '^[[:space:]]*- |^\*\*' GLOSSARY.md | grep -vE '^GLOSSARY\.md:[0-9]+:\*\*[^*]+\*\*:$' || true)
if [[ -n "$bad" ]]; then
    printf '%s\n' "$bad"
    cat <<'EOF'
Write each entry as a term line, its definition, then the words to avoid,
with a blank line between entries:

**Term**:
One or two sentences on what the term is.
_Avoid_: synonym, other synonym
EOF
    exit 1
fi

step "lint"
golangci-lint run --timeout=5m

step "error replies carry no error text"
# A member's error reply is a fixed message. The error goes to the log or
# to Sentry (#478).
go run ./tools/errorreply ./...

step "go.mod and go.sum are tidy"
go mod tidy -diff

step "the image builds on the Go this gate tests"
"$scripts/check-image-go.sh" "$GOTOOLCHAIN" Dockerfile

step "script tests"
for t in "$scripts"/*_test.sh; do
    "$t"
done

step "migrations sort after develop's and leave its own unchanged"
# CI checks out a single commit, so fetch develop itself. A failed fetch stops
# the gate, and the check fails without develop rather than passing. Run
# locally, the check reads this worktree's origin/develop.
if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    git fetch --no-tags --depth=1 origin +refs/heads/develop:refs/remotes/origin/develop
fi
"$scripts/check-migrations.sh" origin/develop store/migrations "$(date -u +%Y%m%d%H%M%S)"

step "Postgres for the store tests"
TEST_BOT_DB_DSN=$("$scripts/test-db.sh" "$db")
export TEST_BOT_DB_DSN

step "tests"
go test ./... -race -cover -covermode=atomic | tee "$work/cover.log"

step "coverage floors"
"$scripts/check-coverage-floors.sh" .github/coverage-floors.tsv <"$work/cover.log" | tee "$work/floors.log"

step "build"
go build -o "$work/cavbot2" .

# A summary to paste into a reviewer's prompt, so they can tell which tree
# passed without running the suite again.
step "gate passed"
echo "commit $commit, $tree, $(go env GOVERSION)"
cat "$work/floors.log"
