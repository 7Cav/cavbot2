# Issue tracker: GitHub

Issues and PRDs for this repo live as GitHub issues. Use the `gh` CLI for all operations.

## Conventions

- **Create an issue**: `gh issue create --title "..." --body "..."`. Use a heredoc for multi-line bodies.
- **Fetch an issue**: see "When a skill says 'fetch the relevant ticket'" below.
- **List issues**: `gh issue list --state open --json number,title,labels --jq '[.[] | {number, title, labels: [.labels[].name]}]'` with `--label` and `--state` filters, then fetch the issues you need.
- **Comment on an issue**: `gh issue comment <number> --body "..."`
- **Apply / remove labels**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **Close**: `gh issue close <number> --comment "..."`

Infer the repo from `git remote -v` — `gh` does this automatically when run inside a clone.

## When a skill says "publish to the issue tracker"

Create a GitHub issue.

## When a skill says "fetch the relevant ticket"

Fetch the body and every comment into your scratchpad directory in one call:

```bash
gh issue view <n> --json body,comments --jq '.body, (.comments[] | "\n---\n@\(.author.login):\n\(.body)")' > <scratchpad>/issue-<n>.md
```

Empty output means the fetch worked. Read the file with the Read tool. Claude Code can add a false "GitHub API rate limit exceeded" hint to a `gh` call whose output quotes rate-limit text, and specs about Discord quote it often. If the hint shows up anyway, check `gh api rate_limit` and carry on while it shows calls left.

How much of a spec to read:

- Reviewing against a spec, or any spec under 20 KB (`wc -c`): all of it. Hand a review subagent the file's absolute path.
- Implementing one ticket of a spec over 20 KB: list headings with `grep -n '^#'`, then read the sections the ticket names, `## Testing Decisions`, `## Out of Scope`, and every section those refer you to.

## Wayfinding operations

A wayfinder map and its tickets are GitHub issues. GitHub's own features carry the structure; no body convention is needed.

- **The map** is one issue labelled `wayfinder:map`. Make it a sub-issue of the feature issue it charts (`--parent <n>`), so the feature page shows it.
- **Tickets** are sub-issues of the map, on the map's milestone: `gh issue create --parent <map> --milestone "<the map's milestone>"`. Each carries one `wayfinder:<type>` label: `research`, `prototype`, `grilling`, or `task`.
- **Blocking** uses native relationships: `gh issue edit <ticket> --add-blocked-by <other>`. GitHub renders the edge on both issues.
- **The frontier** is every open child of the map with nothing open in its `blockedBy` list and no assignee. This one call lists it:

  ```bash
  gh api graphql -F owner='{owner}' -F repo='{repo}' -F map=<map> -f query='query($owner:String!,$repo:String!,$map:Int!){repository(owner:$owner,name:$repo){issue(number:$map){subIssues(first:100){nodes{number title state assignees(first:1){totalCount} blockedBy(first:50){nodes{state}}}}}}}' --jq '.data.repository.issue.subIssues.nodes[] | select(.state=="OPEN" and .assignees.totalCount==0 and ([.blockedBy.nodes[]|select(.state=="OPEN")]|length)==0) | "#\(.number) \(.title)"'
  ```

  `gh issue view <map> --json subIssues` wraps the children as `{nodes, totalCount}`, so read every child with `--jq '.subIssues.nodes[]'`.
- **Claiming** is assignment: `gh issue edit <n> --add-assignee @me` before any work. An open, unassigned ticket is unclaimed.
- **Resolving** is a comment with the answer, then `gh issue close <n>`, then one line appended to the map's "Decisions so far" with `gh issue edit <map> --body-file`.
- **Out of scope** is a closed ticket plus one line in the map's "Out of scope" section. It never appears in "Decisions so far".
