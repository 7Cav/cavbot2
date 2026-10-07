#!/usr/bin/env bash
# .github/scripts/gate.sh
#
# The CI gate. CI's build job runs this script, so a pass here is a pass in
# CI. Run it from anywhere in the repo before you push.
#
# Steps, in order: lint, module tidiness, the floor script's own tests, the
# test suite with -race and coverage against a throwaway Postgres, the
# coverage floors, the build.
#
# Runs in several worktrees at once without interference. Each run starts its
# own Postgres through test-db.sh and removes only that container on exit, and
# keeps its coverage log and binary in a temp directory. An inherited
# TEST_BOT_DB_DSN is ignored, because the store tests drop the public schema
# before every case and must never reach a database this run did not start.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
scripts=.github/scripts

# Run on the Go version go.mod names, the one CI's setup-go installs. Coverage
# moves with the toolchain (commands covers 86.4% under 1.26.0 and 88.1% under
# 1.27.1), so a newer local Go would pass floors CI fails. Go fetches the
# pinned toolchain on first use.
GOTOOLCHAIN=go$(go list -m -f '{{.GoVersion}}')
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

step "lint"
golangci-lint run --timeout=5m

step "go.mod and go.sum are tidy"
go mod tidy -diff

step "floor script tests"
"$scripts/check-coverage-floors_test.sh"

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
