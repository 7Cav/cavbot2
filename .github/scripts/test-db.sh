#!/usr/bin/env bash
# .github/scripts/test-db.sh [container-name]
#
# Starts a throwaway Postgres for the store package's tests and prints its DSN
# on stdout:
#
#   dsn=$(.github/scripts/test-db.sh)
#   TEST_BOT_DB_DSN=$dsn go test ./store/...
#
# Every run gets its own container and a host port Docker picks, so worktrees
# running at the same time never share a database. They must not: the store
# tests drop and recreate the public schema before every case.
#
# The container name goes to stderr. Without an argument it is
# cavbot2-test-db-<worktree>-<pid>. Remove it with `docker rm -f <name>`, or
# remove every one left behind:
#
#   docker rm -f $(docker ps -aq --filter label=cavbot2-test-db)
#
# The image is the same major as docker-compose.yml's postgres service.

set -euo pipefail

image=postgres:18-alpine
ready_timeout=60

worktree=$(basename "$(git rev-parse --show-toplevel 2>/dev/null || pwd)")
name=${1:-cavbot2-test-db-${worktree//[^a-zA-Z0-9_.-]/-}-$$}

started=0
cleanup_on_failure() {
    if [[ $started -eq 1 ]]; then
        docker rm -f "$name" >/dev/null 2>&1 || true
    fi
}
trap cleanup_on_failure EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker run --rm -d --name "$name" --label cavbot2-test-db \
    -e POSTGRES_PASSWORD=postgres -p 127.0.0.1::5432 "$image" >/dev/null
started=1
echo "test database container: $name" >&2

# Probe over TCP. The image's first-boot init runs a temporary server that
# listens on the unix socket only, then restarts; a socket probe can report
# ready before that restart and the first test connection then fails.
deadline=$((SECONDS + ready_timeout))
until docker exec "$name" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1; do
    if ((SECONDS >= deadline)); then
        echo "Postgres in $name not ready after ${ready_timeout}s" >&2
        docker logs "$name" 2>&1 | tail -20 >&2 || true
        exit 1
    fi
    sleep 1
done

port=$(docker port "$name" 5432/tcp | head -1)
port=${port##*:}
trap - EXIT INT TERM
echo "postgres://postgres:postgres@127.0.0.1:${port}/postgres?sslmode=disable"
