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

Each passes when the data reaches only the log, Sentry, `fmt.Errorf`, a
panic, or a comparison that picks a fixed message. ADR 0016's exceptions
still pass. Outside `unsafe`, Go has no other way out of an error, so the
check follows no routes and has none to miss. Decided in the triage of #645
and #646, superseding ADR 0017.

## Consequences

- Data from an error fails even when no person sees it, such as a Sentry
  dedup key built from a status code. A SHA-256 digest of it passes, since
  a digest can only be compared.
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
