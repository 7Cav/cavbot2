# Factory run

Nobody watches the run, so answer from the requirements and the repo wherever this file or a skill would ask the user. End the run with one line saying how it ended.

## Pick

`.github/scripts/factory-eligible.sh` prints `waiting` and `queue`. End the run with the script's error if it fails, with `waiting on` and every PR `waiting` names, as in `waiting on #901, #905`, or with `nothing to do` when `queue` is empty.

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

Once you have opened the PR, label it `factory`. An unattended run has no Auto-fix, so load the `babysit-pr` skill and follow it until the PR merges. This run is your go-ahead to merge. Where the skill says to stop and report, hand back instead. This run gets no Monitor wake-ups, so wait for the PR as the skill says to without Monitor.

`develop` merges only branches that are up to date, so update a branch that is behind with `gh pr update-branch <pr>`, and one that conflicts by merging `origin/develop` in. Run `.github/scripts/gate.sh` before you push.

Fix failing checks and conflicts up to two rounds in all, and answer CodeRabbit's findings from up to three of its reviews. Hand back once either runs out. Done when the PR has merged; end the run with its URL.

## Hand back

Hand the issue back wherever Finish says to, and when the run can't finish it for missing context, a decision the requirements leave open, or a dependency outside the repo such as a Discord or forum setting.

Comment on the issue with what stopped the run and the branch or PR it reached. For each decision or context the requirements lack, name the terms it turns on and the spec sections and glossary entries you searched for it. Leave any PR the run opened as it stands, with its `factory` label. Release the claim as you swap `ready-for-agent` for `blocked`:

```bash
gh issue edit <n> --remove-assignee @me --remove-label ready-for-agent --add-label blocked
```

End the run with the issue's number and what stopped it.

## Follow-ups

A follow-up is a gap outside the issue that a member, a panel user or the maintainer would notice, including anything you rule out of scope. It takes the place of a `spawn_task` chip, which nobody sees in an unattended run. Keep the PR on its issue and record each follow-up from what you have already seen. A later run (`docs/agents/follow-ups.md`) investigates and files it.

Record them under a `## Follow-ups` heading after Merge Danger in the PR body, and add the `follow-up` label to the PR. That later run sees only this section, so each entry names the test, command or screen where you saw the gap, quotes its output, and says why it falls outside the issue. A follow-up found after the PR opens goes in the same way before the run ends. Done when every follow-up you saw is in the PR body and the PR carries `follow-up`.
