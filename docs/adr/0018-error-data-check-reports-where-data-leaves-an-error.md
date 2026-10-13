---
status: accepted
---

# The error data check reports where data leaves an error

The gate's `tools/errorreply` check (ADR 0016) traced each reply, channel
post and panel answer back to the data it was built from. Go gives data
endless routes, so each review found another one the check missed (#547,
#551, #559, #560, #569, #645, #646), all in code nobody had written. The
check now reports the place data leaves an error:

- a call of one of its methods, or a method value or method expression
- a read of one of its fields
- a conversion of it, or of a value holding it, to an interface other than
  `error`
- a recovered panic
- a call outside the module handed one, unless the call's signature lets it
  return or write nothing but a bool, a time or an error, as with `errors.Is`
- a generic function or type instantiated with one, for a type parameter
  that may not be an error

Each passes when the data reaches only the log, Sentry, `fmt.Errorf`, a
panic, or a comparison that picks a fixed message. ADR 0016's exceptions
still pass. Outside `unsafe`, Go has no other way out of an error. Once data
is out, the check follows it through the function and into the module's
functions it is handed to, and counts any use it has no rule for as one that
takes the data elsewhere. A route the check doesn't know fails the gate
instead of passing it. Decided in the triage of #645 and #646, superseding
ADR 0017.

The log is `log/slog` and Sentry is `sentry-go`. What a call into either
returns carries what it was handed. The check reports code that points
either one at its own handler, writer or callback rather than the process's
output and Sentry's client.

## Consequences

- Data from an error fails even when no person sees it, such as a Sentry
  dedup key built from a status code. ADR 0016 adds a SHA-256 digest of it
  to the data that passes, since a digest can only be compared.
- Idiomatic code can fail the gate where it hands error data to a library
  function the check can't judge from its signature, such as `bytes.Contains`
  on a response body or `slices.Concat` for log key/values. The fix is a
  shape the check reads, such as `strings.Contains` or `append`.
- The check doesn't read unexported fields. fmt prints one without calling
  its `Error` method, and templates and `encoding/json` can't read one.
- The check judges a call outside the module by its signature. A call
  through an interface or a function value may be the module's own code,
  whose writes the check reads where they happen, so it is judged by what it
  returns.
- Lint keeps `unsafe` out of production code.
- The gate runs the check over the bot's packages. The tools under `tools/`
  answer the maintainer, not a member or a panel user.

## Considered options

- **Trace replies and add routes as reviews find them (ADR 0017).** Each fix
  left another route, and failing closed on writes reported 54 places in
  production, none of them real.
- **Remove the check.** The next reply built from an error's text that review
  missed would show a member Discord's raw text, the problem #524 fixed.
