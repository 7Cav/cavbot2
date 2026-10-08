---
status: accepted
---

# A reply or panel answer carries no data from an error, except a time

An error from Discord, the 7Cav API or the standard library goes to the log
or to Sentry (ADR 0001). It never goes into what a member or a panel user
reads. The gate's `tools/errorreply` check holds every reply, channel post and
panel answer to this (#528, #534). A message is built from an error when data
from an error flows into it. That covers the error's `Error()` text, a format
verb applied to it, a status code, a response body, and any other field read
off it. Two kinds of data from an error pass.

- **A time or a duration read off an error**, such as the wait Discord sends
  with a 429. A time can't carry Discord's words, and dropping it costs the
  reader the exact wait. A panel admin who renames a hub's channel a third
  time inside ten minutes reads "Try again in 7 minutes", not "Try again in a
  few minutes".
- **A value of an error type this module defines, judged by what was put in
  it.** The panel's refusals are error values whose message is fixed text,
  and a Foxhole lookup's reply can be one too. Such a value passes while
  everything stored in it is fixed text, member input, a time, or a phrase
  chosen by inspecting an error. The check reports the line that stores an
  error's data in it.

Choosing between fixed messages by inspecting an error also passes, whether
with `errors.Is`, `errors.As` or a classifier like `classifyDiscordError`.
Nothing else makes a message pass. No named type, comment or allowlist does.

Decided in the triage of #534, before #528 or #534 was built.

## Considered options

- **Any data read off an error fails, a time included, and a value of any
  error type fails on sight.** This was #528's first rule. Every panel
  refusal would have to reach its page outside `error`. That means reworking
  each hub and Foxhole save, with no difference a panel user sees. The
  rate-limit refusals on the panel and in `/voice-rename` would also lose
  their wait.
- **List the types or calls that pass**, in an allowlist or by a comment at
  the call. An entry outlives the code it vouched for, and the check can't
  tell when that code changes.
