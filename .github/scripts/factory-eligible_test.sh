#!/usr/bin/env bash
# .github/scripts/factory-eligible_test.sh
#
# Tests for factory-eligible.sh. A fake gh answers its two calls from fixture
# files shaped like GitHub's REST responses. gate.sh runs this file.

set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/factory-eligible.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/bin"
cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Prints the fixture for the endpoint called, as gh api prints one page.
# FAIL_ON names the one call that fails instead.
case "$*" in
    *"/issues"*) call=issues ;;
    *"/pulls"*) call=pulls ;;
    *) echo "fake gh: unexpected call: $*" >&2; exit 2 ;;
esac
if [[ ${FAIL_ON:-} == "$call" ]]; then
    echo "gh: HTTP 502" >&2
    exit 1
fi
cat "$FIXTURES/$call.json"
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH" FIXTURES="$tmp"

failures=0
case_name=""

# issue <number> [jq filter]: one open issue as GitHub's issues listing returns
# it, eligible for the queue unless the filter changes it.
issue() {
    jq -nc --argjson n "$1" '{
        number: $n, title: "Issue \($n)", state: "open",
        labels: [{name: "ready-for-agent"}], assignees: [],
        issue_dependencies_summary: {blocked_by: 0, blocking: 0, total_blocked_by: 0, total_blocking: 0},
        sub_issues_summary: {total: 0, completed: 0, percent_completed: 0},
        parent_issue_url: null} | '"${2:-.}"
}

# pull <number> <body> [label]: one open pull request as GitHub's pulls listing
# returns it.
pull() {
    jq -nc --argjson n "$1" --arg body "$2" --arg label "${3:-}" '{
        number: $n, state: "open", body: $body,
        labels: (if $label == "" then [] else [{name: $label}] end)}'
}

# run_check <issues JSON array> <pulls JSON array> [call to fail]: runs the
# script, sets $out (stdout) and $code.
run_check() {
    printf '%s\n' "$1" >"$tmp/issues.json"
    printf '%s\n' "$2" >"$tmp/pulls.json"
    set +e
    out=$(FAIL_ON=${3:-} "$script" 2>"$tmp/err")
    code=$?
    set -e
}

# field <jq filter>: reads $out, printing nothing when it isn't JSON.
field() { jq -c "$1" <<<"$out" 2>/dev/null || true; }

fail() {
    echo "FAIL: $case_name: $*" >&2
    while IFS= read -r l; do printf '    | %s\n' "$l"; done <<<"$out$(cat "$tmp/err")" >&2
    failures=$((failures + 1))
}

case_name="an open, unclaimed, unblocked ready-for-agent issue is queued with its parent"
run_check "[$(issue 7 '.parent_issue_url = "https://api.github.com/repos/7Cav/cavbot2/issues/381"')]" '[]'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
[[ $(field '.queue[0].number') == 7 ]] || fail "first queued issue is not 7"
[[ $(field '.queue[0].title') == '"Issue 7"' ]] || fail "its title is not Issue 7"
[[ $(field '.queue[0].labels | index("ready-for-agent") != null') == true ]] || fail "its labels lack ready-for-agent"
[[ $(field '.queue[0].parent') == 381 ]] || fail "its parent is not 381"

case_name="an issue without ready-for-agent stays out of the queue"
run_check "[$(issue 1),$(issue 2 '.labels = [{name: "needs-triage"}]')]" '[]'
[[ $(field '[.queue[].number]') == '[1]' ]] || fail "queue is $(field '[.queue[].number]'), want [1]"

case_name="an assigned issue stays out of the queue"
run_check "[$(issue 1),$(issue 2 '.assignees = [{login: "SyniRon"}]')]" '[]'
[[ $(field '[.queue[].number]') == '[1]' ]] || fail "queue is $(field '[.queue[].number]'), want [1]"

case_name="an issue with an open blocker stays out of the queue"
run_check "[$(issue 1),$(issue 2 '.issue_dependencies_summary.blocked_by = 1 | .issue_dependencies_summary.total_blocked_by = 1')]" '[]'
[[ $(field '[.queue[].number]') == '[1]' ]] || fail "queue is $(field '[.queue[].number]'), want [1]"

case_name="an issue with sub-issues stays out of the queue"
run_check "[$(issue 1),$(issue 2 '.sub_issues_summary.total = 13')]" '[]'
[[ $(field '[.queue[].number]') == '[1]' ]] || fail "queue is $(field '[.queue[].number]'), want [1]"

case_name="an issue an open PR closes stays out, and one it only mentions stays in"
run_check "[$(issue 1),$(issue 2),$(issue 3)]" "[$(pull 50 $'## Summary\n\nFixes #2'),$(pull 51 'Refs #3')]"
[[ $(field '[.queue[].number]') == '[1,3]' ]] || fail "queue is $(field '[.queue[].number]'), want [1,3]"

case_name="an issue without a blocker count fails the run instead of reading as blocked"
run_check "[$(issue 1 'del(.issue_dependencies_summary)')]" '[]'
[[ $code -ne 0 ]] || fail "exit 0, want nonzero"

# GitHub's issues listing includes open pull requests, without the dependency,
# sub-issue and parent fields an issue carries.
case_name="an open pull request in the issues listing doesn't fail the run"
run_check "[$(issue 1),$(jq -nc '{number: 499, title: "docs: a PR", state: "open", labels: [], assignees: [], pull_request: {url: "https://api.github.com/repos/7Cav/cavbot2/pulls/499"}}')]" '[]'
[[ $code -eq 0 ]] || fail "exit $code, want 0"

case_name="an open factory PR is waiting and holds the queue"
run_check "[$(issue 1)]" "[$(pull 60 'Closes #9' factory)]"
[[ $code -eq 0 ]] || fail "exit $code, want 0"
[[ $(field '.waiting') == '[60]' ]] || fail "waiting is $(field '.waiting'), want [60]"
[[ $(field '.queue') == '[]' ]] || fail "queue is $(field '.queue'), want []"

case_name="a failed pull request listing fails the run instead of reading as nothing in flight"
run_check "[$(issue 1)]" "[$(pull 60 'Closes #9' factory)]" pulls
[[ $code -ne 0 ]] || fail "exit 0, want nonzero"
[[ -z $out ]] || fail "printed output on failure"

if [[ $failures -ne 0 ]]; then
    echo "$failures factory-eligible test(s) failed" >&2
    exit 1
fi
echo "factory-eligible tests passed"
