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

Once you have opened the PR, label it `factory`. CodeRabbit's approval skips the findings it writes outside the diff, so you merge on `pr-ready check`'s verdict. An unattended run has no Auto-fix, so watch the PR yourself until it merges:

1. Run `timeout 30m gh pr checks <pr> --watch`, which returns once every check on the head commit finishes, or after 30 minutes. That includes `CodeRabbit`, which finishes as CodeRabbit posts its review of the commit.
2. Run `pr-ready open <pr>`, then `pr-ready check <pr>`, and act on its verdict:
   - `merged`: done.
   - `closed`: someone closed the PR without merging it. Leave the PR as they left it and hand back now. It can also close while you work on an entry, so run `pr-ready check <pr>` again before each push or post to the PR.
   - `ready`: merge with `gh pr merge <pr> --squash --match-head-commit <head>`, where `<head>` is the `head` that `check` printed, then go back to 2. When a push has moved the head since `check` read it, the merge fails, so go back to 1.
   - `act`: each entry in `act` waits on you. Clear every one, then go back to 1. Run `.github/scripts/gate.sh` before you push.
     - `behind`: `develop` merges only branches that are up to date, so run `gh pr update-branch <pr>`.
     - `conflict`: merge `origin/develop` in.
     - `check_failed`: read its log with `gh run view <run> --log-failed`, taking `<run>` from its `url`, and fix it.
     - `thread`: its `body` is the thread's last comment. Answer a finding with the commit that fixes it, or with why you decline it, in a reply to `repos/{owner}/{repo}/pulls/<pr>/comments/<reply_to>/replies`. Start every reply with `@coderabbitai`, so CodeRabbit answers in threads it didn't open. When CodeRabbit's reply accepts your fix or your decline, resolve the thread, since the merge waits on every thread:

       ```bash
       gh api graphql -f thread=<thread> -f query='mutation($thread:ID!){resolveReviewThread(input:{threadId:$thread}){thread{isResolved}}}'
       ```

       If CodeRabbit answers a decline by pressing the finding, fix the finding when its reply persuades you, or hand back.
     - `finding_unthreaded` or `review_unreadable`: CodeRabbit reviewed again after `open` ran, so run `pr-ready open <pr>` again.
   - `wait`: CodeRabbit or a check acts next. Go back to 2 each minute until the verdict changes. CodeRabbit can take 15 minutes to answer a reply. After 30 straight minutes of `wait`, post `@coderabbitai review` once, since CodeRabbit pauses its reviews of new pushes after five reviewed commits. Hand back if 30 more minutes pass.
   - An error from `pr-ready`: hand back with it.

Fix failing checks and conflicts up to two rounds in all, and answer up to three CodeRabbit reviews that request changes. Hand back once either runs out. Done when the PR has merged; end the run with its URL.

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
