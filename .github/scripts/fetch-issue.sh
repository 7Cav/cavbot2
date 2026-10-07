#!/usr/bin/env bash
# .github/scripts/fetch-issue.sh <n>
#
# Prints issue <n> for an agent to read: its body, then each comment headed by
# its author. Only what people who can push to the repository wrote gets
# through. The repository is public, so anyone can comment on an issue, and an
# issue's author can edit its body after a maintainer labels it
# ready-for-agent. A comment by anyone else is left out, and so is a body,
# with a note in its place.
#
# Any failure exits nonzero with nothing on stdout, so an API error never
# reads as an empty issue.

set -euo pipefail

n=${1:?usage: fetch-issue.sh <issue number>}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

gh api 'repos/{owner}/{repo}/collaborators?per_page=100' --paginate >"$tmp/collaborators.json"
gh issue view "$n" --json author,body,comments >"$tmp/issue.json"

jq -r --slurpfile collaborators "$tmp/collaborators.json" '
  [ $collaborators[][] | select(.permissions.push) | .login ] as $pushers
  | def by_pusher: .author.login as $l | any($pushers[]; . == $l);
  (if by_pusher then .body
   else "(The body is by @\(.author.login), who can'"'"'t push to this repository, so it is left out. Work from the comments below.)" end),
  (.comments[] | select(by_pusher) | "\n---\n@\(.author.login):\n\(.body)")' "$tmp/issue.json"
