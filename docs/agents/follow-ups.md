# Follow-up run

Nobody watches the run, so decide from the pull requests, the issues and the repo wherever you would ask the user. End the run with one line saying how it ended.

A factory run (`docs/agents/factory.md`) records each follow-up, a gap it saw outside its issue, under a `## Follow-ups` heading in its PR body. It labels that PR `follow-up`. This run gives every follow-up in every labelled PR a verdict.

## Find

```bash
gh api 'repos/{owner}/{repo}/issues?labels=follow-up&state=all&per_page=100' --jq '[.[] | select(.pull_request) | {number, title, body}]'
```

This prints the labelled PRs, open and closed. End the run with the command's error if it fails, or with `nothing to do` when it prints `[]`.

## Verdicts

Judge each follow-up from its entry's evidence and current `develop`.

- **Dropped.** `develop` no longer has the gap, no member, panel user or maintainer would notice it, or an issue already declined it as `wontfix`.
- **Duplicate.** An open issue covers it.
- **Filed.** Every other follow-up. Create its issue the way `docs/agents/issue-tracker.md` says, labelled `needs-triage`. Describe the gap as behavior: what happens now, what should happen, and how to see it.

## Close out

Once every follow-up in a PR has its verdict, comment on the PR with one line per follow-up: the issue you filed, the open issue it duplicates, or why you dropped it. Then take `follow-up` off the PR and keep its other labels, since `factory` on an open PR holds the factory's queue.

End the run with the count of each verdict and the PRs the follow-ups came from.
