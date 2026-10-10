# The error data check fails closed

The gate's `tools/errorreply` check (ADR 0016) counted anything it had no rule
for as clean, so each fix's review found one more route it missed (#547, #551,
#559, #560), all in code nobody had written. It now counts a value it can't
follow as data from an error, and anything handed to discordgo or to the
panel's response writer as reaching a person; production code reported nothing
on the day it changed. Writes through a pointer kept elsewhere, values carried
through the store or the temp VC runtime, and functions called through a
function value still follow known routes (the package doc lists them), so a
reply missed anywhere else is a bug in the fail-closed rule, not a route to
add.

## Considered options

- **Keep adding routes as reviews find them.** Go constructs and discordgo
  calls have no end, and a missing entry fails silently.
