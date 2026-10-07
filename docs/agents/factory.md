# Factory run

Nobody watches the run, so answer from the requirements and the repo wherever a skill would ask the user. End the run with one line saying how it ended.

## Pick

`.github/scripts/factory-eligible.sh` prints `waiting` and `queue`. End the run with the script's error if it fails, or with `nothing to do` when both are empty.

A PR in `waiting` holds the factory until it merges or closes. Read it with `gh pr view <pr> --json mergeStateStatus,autoMergeRequest`. `develop` merges only branches that are up to date, so when auto-merge is on and the state is `BEHIND`, run `gh pr update-branch <pr>` and end the run with `updated #<pr>`. Otherwise end it with `waiting on #<pr>`.

Take one issue from `queue` in this order, lowest number first within a bucket:

1. **Bug fixes**, such as anything labelled `bug`.
2. **Tracer bullets**, such as an issue with a `parent` spec.
3. **Polish**, such as error messages, UX or docs.
4. **Refactors**, with no user-visible change.

Read an issue's body only when its title, labels and parent leave the bucket unclear. Claim the issue the moment you pick it, with `gh issue edit <n> --add-assignee @me`.

## Requirements

The requirements are the issue, its thread, its `parent` issue when it has one, and the GLOSSARY.md entries they name. In `/implement`'s words the issue is the ticket and its parent is the spec. An agent brief in the thread wins where it differs from the rest.

Fetch the issue and its parent as `docs/agents/issue-tracker.md` says, and read as much of the spec as it says. Done when you have read all of that, before `/implement` loads `tdd`.

## Build

Run `/implement` on the issue. A change a member or panel user can see gets its smoke checks and `needs-smoke` on the PR, as `/implement` says, and a smoke pass runs them before a release (`docs/smoke-test.md`).

## Finish

Once `/implement` has opened its PR, label it `factory` and turn on squash auto-merge with `gh pr merge <pr> --auto --squash`. Auto-fix wakes this session for a failing check, a conflict with `develop`, or a review comment. Fix each and push again, up to two rounds in all. End the run with the PR's URL once auto-merge and Auto-fix are on.

## Hand back

Hand the issue back when the run can't finish it: missing context, a decision the requirements leave open, a dependency outside the repo such as a Discord or forum setting, or checks still failing after two rounds.

Comment on the issue with what stopped the run and the branch or PR it reached. For each decision or context the requirements lack, name the terms it turns on and the spec sections and glossary entries you searched for it. Then turn off the PR's auto-merge (`gh pr merge <pr> --disable-auto`) and Auto-fix, and leave the PR open with its `factory` label. Release the claim as you swap `ready-for-agent` for `blocked`:

```bash
gh issue edit <n> --remove-assignee @me --remove-label ready-for-agent --add-label blocked
```

End the run with the issue's number and what stopped it.

## Follow-ups

A follow-up is a gap outside the issue that a member, a panel user or the maintainer would notice, including anything you rule out of scope. It takes the place of a `spawn_task` chip, which nobody sees in an unattended run. Keep the PR on its issue and record each follow-up from what you have already seen. A later run (`docs/agents/follow-ups.md`) investigates and files it.

Record them under a `## Follow-ups` heading after Merge Danger in the PR body, and add the `follow-up` label to the PR. That later run sees only this section, so each entry names the test, command or screen where you saw the gap, quotes its output, and says why it falls outside the issue. A follow-up found after the PR opens goes in the same way before the run ends.
