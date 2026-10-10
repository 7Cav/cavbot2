---
status: accepted
---

# The error data check fails closed

The gate's `tools/errorreply` check (ADR 0016) used to report a message to
Discord or a panel answer only when it reached an error by a route it knew.
Anything it had no rule for counted as clean. So every Go construct it didn't
know, and every Discord or panel call missing from its list, let an error's
text through without a report. Each fix's review found the next gap (#547,
#551, #559, #560), and each was in code nobody had written.

Now a value passes only when the check has a rule for everything it's built
from. Every call into discordgo counts as a message to Discord, and every
panel call that leaves the module carrying the response writer counts as a
panel answer. A construct the check can't follow fails the gate on the line
an agent just wrote. The agent rewrites the line or teaches the check the
route. On the day this changed, the stricter check reported nothing in the
bot's production code.

Writes through a pointer kept somewhere else are the one area where the check
still follows known routes. Failing closed there needs the check to know where
every address goes. A rule that failed any address leaving its function
reported 93 lines of ordinary reply building, such as a nested
`&discordgo.InteractionResponseData{...}` or a constructor that returns a
pointer. A missed reply there is a gap in those routes. A missed reply
anywhere else is a bug in the fail-closed rule, not a route to add.

## Considered options

- **Keep adding routes and calls as reviews find them.** Each fix was small,
  but Go constructs and discordgo calls have no end, and a missing entry
  fails silently.
- **Fail closed on writes through pointers too.** It reported 93 ordinary
  lines of production code, as above.
