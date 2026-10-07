---
name: implement
description: "Implement a spec or tickets and ship it as a PR with Auto-fix armed."
disable-model-invocation: true
---

Implement the work described by the user in the spec or tickets, then ship it as a pull request into the **base**. The base is `develop` unless the user names another. Running `/implement` is your go-ahead to push, open the PR, and arm Auto-fix.

If the user passes a ticket reference, fetch it as `docs/agents/issue-tracker.md` says and state its title before starting. If the reference is ambiguous, ask.

If you're on the base, create a branch for the work.

Load the `tdd` skill and build every requirement under its rules, running `go build ./...` and the touched packages' tests after each slice. Done when every requirement in the spec or tickets is built under those rules and its tests are green. A docs-only change is done once built.

Commit your work.

Load the `code-review` skill with the base as the fixed point, the spec or tickets as the spec, and the approved seam ledger. Done when every finding is fixed and committed, or declined with a reason you give in your final message.

Run `.github/scripts/gate.sh`. Push the branch once it passes.

Load the `pr` skill and open the PR, ready for review. The PR closes each ticket it implements, or the spec when there are no tickets. When a member or panel user would notice the change, the PR carries its smoke checks and `needs-smoke`, as step 1 of `docs/smoke-test.md` says.

Arm Auto-fix on the PR. Done when the PR's status shows Auto-fix on. If this session has no Auto-fix, stop and say it isn't armed.
