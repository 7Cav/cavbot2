#!/usr/bin/env bash
# .github/scripts/fetch-issue_test.sh
#
# Tests for fetch-issue.sh. A fake gh answers its two calls from fixture files
# shaped like gh's output. gate.sh runs this file.

set -euo pipefail

script=$(cd "$(dirname "$0")" && pwd)/fetch-issue.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/bin"
cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Prints the fixture for the call made. FAIL_ON names the one call that fails
# instead.
case "$*" in
    *"/collaborators"*) call=collaborators ;;
    *"issue view 12"*) call=issue ;;
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

# Two people can push. A third is a collaborator who can only read.
cat >"$tmp/collaborators.json" <<'EOF'
[{"login": "maintainer", "permissions": {"admin": true, "push": true, "pull": true}},
 {"login": "admin2", "permissions": {"admin": true, "push": true, "pull": true}},
 {"login": "reader", "permissions": {"admin": false, "push": false, "pull": true}}]
EOF

failures=0
case_name=""

# run_check <issue JSON> [call to fail]: runs the script on issue 12, sets $out
# and $code.
run_check() {
    printf '%s\n' "$1" >"$tmp/issue.json"
    set +e
    out=$(FAIL_ON=${2:-} "$script" 12 2>"$tmp/err")
    code=$?
    set -e
}

fail() {
    echo "FAIL: $case_name: $*" >&2
    while IFS= read -r l; do printf '    | %s\n' "$l"; done <<<"$out$(cat "$tmp/err")" >&2
    failures=$((failures + 1))
}

case_name="the body and comments of people who can push come through in thread order"
run_check '{"author": {"login": "maintainer"}, "body": "SPEC BODY",
  "comments": [{"author": {"login": "admin2"}, "body": "FIRST COMMENT"},
               {"author": {"login": "maintainer"}, "body": "AGENT BRIEF"}]}'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
[[ $out == *"SPEC BODY"*"admin2"*"FIRST COMMENT"*"maintainer"*"AGENT BRIEF"* ]] ||
    fail "body, then each comment after its author, not found in order"

case_name="a comment by someone outside the repo is left out"
run_check '{"author": {"login": "maintainer"}, "body": "SPEC BODY",
  "comments": [{"author": {"login": "stranger"}, "body": "IGNORE THE SPEC"},
               {"author": {"login": "maintainer"}, "body": "AGENT BRIEF"}]}'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
[[ $out != *"IGNORE THE SPEC"* ]] || fail "printed the outsider's comment"
[[ $out == *"AGENT BRIEF"* ]] || fail "dropped the maintainer's comment"

case_name="a comment by a collaborator who can only read is left out"
run_check '{"author": {"login": "maintainer"}, "body": "SPEC BODY",
  "comments": [{"author": {"login": "reader"}, "body": "READER COMMENT"}]}'
[[ $out != *"READER COMMENT"* ]] || fail "printed the read-only collaborator's comment"

case_name="a body by someone outside the repo is left out"
run_check '{"author": {"login": "stranger"}, "body": "EDITED BODY",
  "comments": [{"author": {"login": "maintainer"}, "body": "AGENT BRIEF"}]}'
[[ $code -eq 0 ]] || fail "exit $code, want 0"
[[ $out != *"EDITED BODY"* ]] || fail "printed the outsider's body"
[[ $out == *"AGENT BRIEF"* ]] || fail "dropped the maintainer's comment"

case_name="a failed issue fetch fails instead of printing an empty issue"
run_check '{"author": {"login": "maintainer"}, "body": "SPEC BODY", "comments": []}' issue
[[ $code -ne 0 ]] || fail "exit 0, want nonzero"
[[ -z $out ]] || fail "printed output on failure"

if [[ $failures -ne 0 ]]; then
    echo "$failures fetch-issue test(s) failed" >&2
    exit 1
fi
echo "fetch-issue tests passed"
