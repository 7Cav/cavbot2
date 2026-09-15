# Issue tracker: GitHub

Issues and PRDs for this repo live as GitHub issues. Use the `gh` CLI for all operations.

## Conventions

- **Create an issue**: `gh issue create --title "..." --body "..."`. Use a heredoc for multi-line bodies.
- **Read an issue**: `gh issue view <number> --comments`, filtering comments by `jq` and also fetching labels.
- **List issues**: `gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'` with appropriate `--label` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> --body "..."`
- **Apply / remove labels**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **Close**: `gh issue close <number> --comment "..."`

Infer the repo from `git remote -v` — `gh` does this automatically when run inside a clone.

## When a skill says "publish to the issue tracker"

Create a GitHub issue.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --comments`.

## Wayfinding operations

A wayfinder map and its tickets are GitHub issues. GitHub's own features carry the structure; no body convention is needed.

- **The map** is one issue labelled `wayfinder:map`. Make it a sub-issue of the feature issue it charts (`--parent <n>`), so the feature page shows it.
- **Tickets** are sub-issues of the map: `gh issue create --parent <map>`. Each carries one `wayfinder:<type>` label: `research`, `prototype`, `grilling`, or `task`.
- **Blocking** uses native relationships: `gh issue edit <ticket> --add-blocked-by <other>`. GitHub renders the edge on both issues.
- **The frontier** is every open child of the map with nothing open in its `blockedBy` list and no assignee. Query it with `gh issue view <map> --json subIssues`, then `gh issue view <n> --json state,assignees,blockedBy` per child.
- **Claiming** is assignment: `gh issue edit <n> --add-assignee @me` before any work. An open, unassigned ticket is unclaimed.
- **Resolving** is a comment with the answer, then `gh issue close <n>`, then one line appended to the map's "Decisions so far" with `gh issue edit <map> --body-file`.
- **Out of scope** is a closed ticket plus one line in the map's "Out of scope" section. It never appears in "Decisions so far".
