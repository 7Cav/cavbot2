#!/usr/bin/env bash
# .github/scripts/check-image-go_test.sh
#
# Tests for check-image-go.sh, fed a fixture Dockerfile and go.mod. gate.sh
# runs this file.

set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/check-image-go.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

failures=0
case_name=""

# run_check <builder image> [<go.mod line>...]: runs the script against a
# Dockerfile shaped like the repo's, with the builder stage on <builder image>,
# and a go.mod with a go line and any lines given. Sets $stdout, $out (stdout
# then stderr) and $code.
run_check() {
    cat >"$tmp/Dockerfile" <<EOF
FROM $1 AS builder

WORKDIR /app

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o main .

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/main .

CMD ["./main"]
EOF
    shift
    printf '%s\n' 'module example.com/app' '' 'go 1.26.0' "$@" >"$tmp/go.mod"
    set +e
    stdout=$("$script" "$tmp/Dockerfile" "$tmp/go.mod" 2>"$tmp/stderr")
    code=$?
    set -e
    out=$stdout$'\n'$(<"$tmp/stderr")
}

fail() {
    echo "FAIL: $case_name: $*" >&2
    while IFS= read -r l; do printf '    | %s\n' "$l"; done <<<"$out" >&2
    failures=$((failures + 1))
}

# The gate and CI run on the Go this prints.
case_name="a builder on an exact golang tag passes and prints its Go"
run_check golang:1.27.3
[[ $code -eq 0 ]] || fail "exit $code, want 0"
[[ $stdout == 1.27.3 ]] || fail "stdout is '$stdout', want 1.27.3"

# What `go get` writes when it raises the go line, as a Dependabot gomod
# update can.
case_name="a toolchain line in go.mod fails"
run_check golang:1.27.3 'toolchain go1.27.3'
[[ $code -eq 1 ]] || fail "exit $code, want 1"

# A floating tag names no one Go: the image's would move under the tests.
case_name="a builder tag without an exact version fails and names the tag"
run_check golang:1.27
[[ $code -eq 1 ]] || fail "exit $code, want 1"
grep -Eq 'golang:1\.27([^0-9.]|\.([^0-9]|$)|$)' <<<"$out" ||
    fail "output does not name golang:1.27"

if [[ $failures -ne 0 ]]; then
    echo "$failures check-image-go test(s) failed" >&2
    exit 1
fi
echo "check-image-go tests passed"
