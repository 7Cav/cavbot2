# Triage Labels

The skills speak in terms of five canonical triage roles. This file maps those roles to the actual label strings used in this repo's issue tracker.

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the corresponding label string from this table.

Edit the right-hand column to match whatever vocabulary you actually use.

## `blocked` marks a factory hand-back

This repo adds a sixth state role, `blocked`. A factory run that [hands an issue back](factory.md#hand-back) swaps its `ready-for-agent` for `blocked`, and the label stays until triage answers the hand-back. The older `blocked on external dependency` label marks a different state, work waiting on something outside the repo. An issue's blockers live in GitHub's blocked-by links. On a hand-back those are all closed, because the factory takes only an issue with none open.

- **Show what needs attention.** List `blocked` issues as a fourth bucket, after `needs-info`.
- **Gather context.** Read the hand-back comment as prior triage notes. Each reason it gives for stopping is an outstanding question for the maintainer.
- **Apply the outcome.** A `ready-for-agent` brief answers every outstanding question from the hand-back. When the hand-back names a pull request, the factory waits on it until it merges or closes, so ask the maintainer which.
