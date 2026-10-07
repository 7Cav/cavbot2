# Factory run

Nobody watches the run, so answer from the requirements and the repo wherever this file or a skill would ask the user. End the run with one line saying how it ended.

## Pick

`.github/scripts/factory-eligible.sh` prints `waiting` and `queue`. End the run with the script's error if it fails, or with `nothing to do` when both are empty.

A PR in `waiting` holds the factory until it merges or closes. Read it with `gh pr view <pr> --json mergeStateStatus,autoMergeRequest`. `develop` merges only branches that are up to date, so when auto-merge is on and the state is `BEHIND`, run `gh pr update-branch <pr>` and end the run with `updated #<pr>`. Otherwise end it with `waiting on #<pr>`.

Take one issue from `queue` in this order, lowest number first within a bucket:

1. **Bug fixes**, such as anything labelled `bug`.
2. **Tracer bullets**, such as an issue with a `parent` spec.
3. **Polish**, such as error messages, UX or docs.
4. **Refactors**, with no user-visible change.

Read an issue's body only when its title, labels and parent leave the bucket unclear. Claim the issue the moment you pick it, with `gh issue edit <n> --add-assignee @me`. Done when the issue is assigned to you.

## Requirements

The requirements are the issue, its thread, its `parent` issue when it has one, and the GLOSSARY.md entries they name. In Implement's words below, the issue is the ticket and its parent is the spec. An agent brief in the thread wins where it differs from the rest.

Fetch the issue and its parent as `docs/agents/issue-tracker.md` says, and read as much of the spec as it says. Done when you have read all of that, before you load `tdd`.

## Implement

Implement the work described by the user in the spec or tickets, then ship it as a pull request into the **base**. The base is `develop` unless the user names another. This run is your go-ahead to push, open the PR, and arm Auto-fix.

If the user passes a ticket reference, fetch it as `docs/agents/issue-tracker.md` says and state its title before starting. If the reference is ambiguous, ask.

If you're on the base, create a branch for the work.

Load the `tdd` skill and build every requirement under its rules, running `go build ./...` and the touched packages' tests after each slice. Done when every requirement in the spec or tickets is built under those rules and its tests are green. A docs-only change is done once built.

Commit your work.

Load the `code-review` skill with the base as the fixed point, the spec or tickets as the spec, and the approved seam ledger. Done when every finding is fixed and committed, or declined with a reason you give in your final message.

Run `.github/scripts/gate.sh`. Push the branch once it passes.

Load the `pr` skill and open the PR, ready for review. The PR closes each ticket it implements, or the spec when there are no tickets. When a member or panel user would notice the change, the PR carries its smoke checks and `needs-smoke`, as step 1 of `docs/smoke-test.md` says.

Arm Auto-fix on the PR. Done when the PR's status shows Auto-fix on, or, in an unattended local session, which has no Auto-fix, once you have said it isn't armed.

## Finish

Leave the PR's smoke checks to a later pass, since the smoke tool runs one bot across all worktrees and the maintainer's own sessions need it.

Once you have opened the PR, label it `factory` and turn on squash auto-merge with `gh pr merge <pr> --auto --squash`. An unattended run has no Auto-fix, so watch the PR's CI yourself until it merges:

1. Run `gh pr checks <pr> --required --watch`, which returns once the required checks finish.
2. Read `gh pr view <pr> --json state,mergeStateStatus` and act on it:
   - `MERGED`: done.
   - `BEHIND`: `develop` merges only branches that are up to date, so run `gh pr update-branch <pr>` and go back to 1.
   - `DIRTY`, or a required check failed: fix it and go back to 1. For a conflict, merge `origin/develop` in. For a failed check, read its log with `gh run view <run> --log-failed`. Run `.github/scripts/gate.sh` before you push.
   - Still open with checks passing: auto-merge is about to merge it. Wait a minute and read it again.

Fix failing checks and conflicts up to two rounds in all. Done when the PR has merged; end the run with its URL.

## Hand back

Hand the issue back when the run can't finish it: missing context, a decision the requirements leave open, a dependency outside the repo such as a Discord or forum setting, or checks still failing after two rounds.

Comment on the issue with what stopped the run and the branch or PR it reached. For each decision or context the requirements lack, name the terms it turns on and the spec sections and glossary entries you searched for it. Then turn off the PR's auto-merge (`gh pr merge <pr> --disable-auto`), and leave the PR open with its `factory` label. Release the claim as you swap `ready-for-agent` for `blocked`:

```bash
gh issue edit <n> --remove-assignee @me --remove-label ready-for-agent --add-label blocked
```

End the run with the issue's number and what stopped it.

## Follow-ups

A follow-up is a gap outside the issue that a member, a panel user or the maintainer would notice, including anything you rule out of scope. It takes the place of a `spawn_task` chip, which nobody sees in an unattended run. Keep the PR on its issue and record each follow-up from what you have already seen. A later run (`docs/agents/follow-ups.md`) investigates and files it.

Record them under a `## Follow-ups` heading after Merge Danger in the PR body, and add the `follow-up` label to the PR. That later run sees only this section, so each entry names the test, command or screen where you saw the gap, quotes its output, and says why it falls outside the issue. A follow-up found after the PR opens goes in the same way before the run ends. Done when every follow-up you saw is in the PR body and the PR carries `follow-up`.
