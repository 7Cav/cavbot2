# Factory run

Nobody watches the run, so answer from the requirements and the repo wherever a skill would ask the user. End the run with one line saying how it ended.

## Pick

`.github/scripts/factory-eligible.sh` prints `waiting` and `queue`. End the run with the script's error if it fails, or with `nothing to do` when both are empty.

A PR in `waiting` holds the factory until it merges or closes, and this run tends it instead of picking an issue. Read its state with `gh pr view <pr> --json mergeStateStatus,autoMergeRequest,headRefName` and `gh pr checks <pr> --required`, then act on the first that applies:

1. **Auto-merge is off.** The PR was handed back, or the maintainer took it over. End the run with `waiting on #<pr>`.
2. **The state is `DIRTY`**, a conflict with `develop`, or **a required check failed** (`gh pr checks` exits 1). Fix it, as *Fix a waiting PR* says.
3. **The state is `BEHIND`.** `develop` merges only branches that are up to date, so run `gh pr update-branch <pr>` and end the run with `updated #<pr>`.
4. **Anything else**, such as checks still running or a review thread open. End the run with `waiting on #<pr>`.

Take one issue from `queue` in this order, lowest number first within a bucket:

1. **Bug fixes**, such as anything labelled `bug`.
2. **Tracer bullets**, such as an issue with a `parent` spec.
3. **Polish**, such as error messages, UX or docs.
4. **Refactors**, with no user-visible change.

Read an issue's body only when its title, labels and parent leave the bucket unclear. Claim the issue the moment you pick it, with `gh issue edit <n> --add-assignee @me`. Done when the issue is assigned to you.

## Fix a waiting PR

A PR gets two fix rounds. Count them with `gh pr view <pr> --json comments --jq '[.comments[].body | select(startswith("Factory fix round"))] | length'`. Once two are spent, hand back the issue the PR closes.

Check the PR out with `gh pr checkout <pr> --detach`, since an earlier run's worktree may still hold its branch. Read a failed check's log with `gh run view <run> --log-failed`, for the run `gh pr checks` links. Resolve a conflict by running `git fetch origin develop` and merging `origin/develop` in. Fix the cause, run `.github/scripts/gate.sh`, and push with `git push origin HEAD:<headRefName>`. Then comment on the PR `Factory fix round <n>: <what failed and what you changed>`. Done when the push has landed and the comment is posted. End the run with `fixed #<pr>`.

## Requirements

The requirements are the issue, its thread, its `parent` issue when it has one, and the GLOSSARY.md entries they name. In `/implement`'s words the issue is the ticket and its parent is the spec. An agent brief in the thread wins where it differs from the rest.

Fetch the issue and its parent as `docs/agents/issue-tracker.md` says, and read as much of the spec as it says. Done when you have read all of that, before `/implement` loads `tdd`.

## Finish

Leave the PR's smoke checks to a later pass, since the smoke tool runs one bot across all worktrees and the maintainer's own sessions need it.

Once `/implement` has opened its PR, label it `factory` and turn on squash auto-merge with `gh pr merge <pr> --auto --squash`. An unattended local session has no Auto-fix, so a later run tends the PR from `waiting` (Pick). End the run with the PR's URL once auto-merge is on.

## Hand back

Hand the issue back when the run can't finish it: missing context, a decision the requirements leave open, a dependency outside the repo such as a Discord or forum setting, or a waiting PR with both fix rounds spent.

Comment on the issue with what stopped the run and the branch or PR it reached. For each decision or context the requirements lack, name the terms it turns on and the spec sections and glossary entries you searched for it. Then turn off the PR's auto-merge (`gh pr merge <pr> --disable-auto`), and leave the PR open with its `factory` label. Release the claim as you swap `ready-for-agent` for `blocked`:

```bash
gh issue edit <n> --remove-assignee @me --remove-label ready-for-agent --add-label blocked
```

End the run with the issue's number and what stopped it.

## Follow-ups

A follow-up is a gap outside the issue that a member, a panel user or the maintainer would notice, including anything you rule out of scope. It takes the place of a `spawn_task` chip, which nobody sees in an unattended run. Keep the PR on its issue and record each follow-up from what you have already seen. A later run (`docs/agents/follow-ups.md`) investigates and files it.

Record them under a `## Follow-ups` heading after Merge Danger in the PR body, and add the `follow-up` label to the PR. That later run sees only this section, so each entry names the test, command or screen where you saw the gap, quotes its output, and says why it falls outside the issue. A follow-up found after the PR opens goes in the same way before the run ends. Done when every follow-up you saw is in the PR body and the PR carries `follow-up`.
