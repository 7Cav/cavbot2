#!/usr/bin/env bash
# .github/scripts/check-image-go_test.sh
#
# Tests for check-image-go.sh, fed a fixture Dockerfile. gate.sh runs this
# file.

set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/check-image-go.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

failures=0
case_name=""

# run_check <toolchain> <builder image>: runs the script against a Dockerfile
# shaped like the repo's, with the builder stage on <builder image>. Sets $out
# and $code.
run_check() {
    cat >"$tmp/Dockerfile" <<EOF
FROM $2 AS builder

WORKDIR /app

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o main .

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/main .

CMD ["./main"]
EOF
    set +e
    out=$("$script" "$1" "$tmp/Dockerfile" 2>&1)
    code=$?
    set -e
}

fail() {
    echo "FAIL: $case_name: $*" >&2
    while IFS= read -r l; do printf '    | %s\n' "$l"; done <<<"$out" >&2
    failures=$((failures + 1))
}

case_name="a builder on the pinned Go passes"
run_check go1.27.1 golang:1.27.1
[[ $code -eq 0 ]] || fail "exit $code, want 0"

# What a Dependabot docker update looks like: the tag moves, go.mod does not.
case_name="a builder tag bumped past the pinned Go fails and names the image"
run_check go1.27.1 golang:1.27.2
[[ $code -eq 1 ]] || fail "exit $code, want 1"
grep -q 'golang:1\.27\.2' <<<"$out" || fail "output does not name golang:1.27.2"

if [[ $failures -ne 0 ]]; then
    echo "$failures check-image-go test(s) failed" >&2
    exit 1
fi
echo "check-image-go tests passed"
