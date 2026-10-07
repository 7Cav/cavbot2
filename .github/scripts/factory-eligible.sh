#!/usr/bin/env bash
# .github/scripts/factory-eligible.sh
#
# Prints what the factory run may take (docs/agents/factory.md), as
# {"waiting": [...], "queue": [...]}. waiting lists the open pull requests
# labelled factory. While it lists any, queue is empty: a factory PR that is
# still in CI, or was handed back, holds the factory until the maintainer
# merges or closes it, rather than letting new work pile up behind it.
# Otherwise queue lists the open issues labelled ready-for-agent that are
# unassigned, have no open blocker and no sub-issues, and that no open pull
# request closes. A spec with sub-issues is never worked as one unit. The run
# chooses among them; this script only decides which issues are in the
# running.
#
# It lists every open issue and filters here, so the fake gh in
# factory-eligible_test.sh can answer like GitHub without copying its query
# filters. GitHub's issues listing includes open pull requests, which carry no
# blocker count. An open pull request closes an issue when its body names it
# after a closing keyword, the way GitHub links the two.
#
# Any failure exits nonzero with nothing on stdout, so a run never mistakes an
# API error for an empty queue or for no factory PR in flight. That includes
# GitHub dropping the blocker count, which would otherwise read as every issue
# being blocked.

set -euo pipefail

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

gh api 'repos/{owner}/{repo}/issues?state=open&per_page=100' --paginate >"$tmp/issues.json"
gh api 'repos/{owner}/{repo}/pulls?state=open&per_page=100' --paginate >"$tmp/pulls.json"

jq -n --slurpfile issues "$tmp/issues.json" --slurpfile pulls "$tmp/pulls.json" '
  ([ $pulls[][] | select(any(.labels[]?; .name == "factory")) | .number ] | sort) as $waiting
  | [ $pulls[][] | (.body // "")
    | scan("(?i)\\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\\s+#([0-9]+)\\b")
    | .[0] | tonumber ] as $taken
  | [ $issues[][] | select(.pull_request == null) ] as $listed
  | if any($listed[]; .issue_dependencies_summary.blocked_by == null)
    then error("GitHub no longer reports how many open blockers an issue has")
    else . end
  | { waiting: $waiting,
      queue: (if $waiting != [] then [] else
        [ $listed[]
          | select(any(.labels[]; .name == "ready-for-agent"))
          | select(.assignees == [])
          | select(.issue_dependencies_summary.blocked_by == 0)
          | select(.sub_issues_summary.total == 0)
          | select(.number as $n | any($taken[]; . == $n) | not)
          | { number, title,
              labels: [.labels[].name],
              parent: (.parent_issue_url | if . then (split("/") | last | tonumber) else null end) } ]
        end) }'
