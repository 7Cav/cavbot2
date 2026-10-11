#!/usr/bin/env bash
# .github/scripts/wait-for-recordings_test.sh
#
# Tests for wait-for-recordings.sh. A fake curl answers each poll from a list.
# gate.sh runs this file.

set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/wait-for-recordings.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/bin"
cat >"$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
# Answers poll n with line n of $ANSWERS, and every poll after the list runs
# out with its last line. FAIL fails the way curl -f does on an HTTP error.
# Past 50 polls it leaves an overrun marker and stops the script, whose PID
# is in $PIDFILE, so a script that would wait forever ends and the case
# fails.
calls=$(($(cat "$CALLS") + 1))
echo "$calls" >"$CALLS"
if ((calls > 50)); then
    touch "$OVERRUN"
    kill "$(cat "$PIDFILE")"
    exit 22
fi
total=$(wc -l <"$ANSWERS")
line=$(sed -n "$((calls < total ? calls : total))p" "$ANSWERS")
if [[ $line == FAIL ]]; then
    echo "curl: (22) The requested URL returned error: 502" >&2
    exit 22
fi
printf '%s\n' "$line"
EOF
chmod +x "$tmp/bin/curl"
export PATH="$tmp/bin:$PATH" ANSWERS="$tmp/answers" CALLS="$tmp/calls" OVERRUN="$tmp/overrun" PIDFILE="$tmp/pid"

failures=0
case_name=""

# run_check <answer>...: runs the script, polling every 0 s, against a fake
# curl that gives the answers in order. Sets $out, $code and $calls.
run_check() {
    printf '%s\n' "$@" >"$ANSWERS"
    echo 0 >"$CALLS"
    rm -f "$OVERRUN"
    "$script" https://panel.example/recording-status 0 >"$tmp/out" 2>&1 &
    echo $! >"$PIDFILE"
    set +e
    wait $!
    code=$?
    set -e
    out=$(cat "$tmp/out")
    calls=$(cat "$CALLS")
}

fail() {
    echo "FAIL: $case_name: $*" >&2
    while IFS= read -r l; do printf '    | %s\n' "$l"; done <<<"$out" >&2
    failures=$((failures + 1))
}

case_name="it waits while a recording runs and goes ahead once none does"
run_check '{"recording":true}' '{"recording":true}' '{"recording":false}'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
((calls >= 3)) || fail "went ahead after $calls polls, while a recording still ran"
[[ ! -e $OVERRUN ]] || fail "still waiting after the recording stopped"

# The build running when the first release with the wait deploys has no
# status route, so it sends the poll to sign in.
case_name="a build with no status route doesn't hold the deploy"
run_check '<a href="/signin?next=%2Frecording-status">See Other</a>.'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
[[ ! -e $OVERRUN ]] || fail "still waiting on a build with no status route"

case_name="a panel that doesn't answer doesn't hold the deploy"
run_check FAIL
[[ $code -eq 0 ]] || fail "exit $code, want 0"
[[ ! -e $OVERRUN ]] || fail "still waiting on a panel that doesn't answer"

if [[ $failures -ne 0 ]]; then
    echo "$failures wait-for-recordings test(s) failed" >&2
    exit 1
fi
echo "wait-for-recordings tests passed"
