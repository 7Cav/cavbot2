# The error data check fails closed

The gate's `tools/errorreply` check (ADR 0016) counted anything it had no rule
for as clean, so each fix's review found one more route it missed (#547, #551,
#559, #560), all in code nobody had written. It now counts a value it can't
follow as data from an error, and anything handed to discordgo or to the
panel's response writer as reaching a person; production code reported nothing
on the day it changed. Writes into a pointer, slice, map or channel, values
carried through the store or the temp VC runtime, and functions called through
a function value still follow known routes (the package doc lists them), so a
reply missed anywhere else is a bug in the fail-closed rule, not a route to
add.

Writes still let misses through. The check follows the ways it knows a value
goes into a container a function made and ignores any other use, so a way it
doesn't know passes an error's data with no report. The triage of #569 found
two: a call that writes into a container a closure captured, and
`fmt.Fprintf` into a `strings.Builder`. A miss there is a route to add.
Decided in the triage of #569.

## Considered options

- **Keep adding routes as reviews find them.** Go constructs and discordgo
  calls have no end, and a missing entry fails silently.
- **Fail closed on writes too.** Measured in the triage of #569, it reported
  54 places in production, and none carried data from an error. Most began at
  a handler's or callback's parameter, which the check already can't follow.
  Clearing those needs a looser rule for such a parameter, one that stops
  catching a reply the check reports today. The other 18 came from one error
  kept in a field that is only compared to nil. The check reads a struct
  whole, so that error reached replies built from the struct's other fields.
  Clearing it needs the check to read fields one at a time, a redesign of how
  it reads values. The check also took eight times as long.
