---
status: accepted
---

# The error data check reports where data leaves an error

The gate's `tools/errorreply` check (ADR 0016) traced each reply, channel
post and panel answer back to the data it was built from, and each review
found another route it missed (#547, #551, #559, #560, #569, #645, #646), all
in code nobody had written. The check now reports where code takes data out
of an error, which Go allows in only a few ways, and fails closed on any use
of that data it has no rule for. A route it doesn't know fails the gate
instead of passing it. Its package doc lists the ways out and what passes.
Decided in the triage of #645 and #646, superseding ADR 0017.

## Consequences

- Data from an error fails even where no person sees it, such as a Sentry
  dedup key built from a status code. Some idiomatic shapes fail too, such as
  `bytes.Contains` on a response body, and need rewriting into one the check
  reads.
- These are limits, not misses: unexported fields, which fmt prints without
  calling `Error`; `unsafe`, which lint keeps out of production code; what a
  library does beyond its signature; and the tools under `tools/`, which
  answer the maintainer, not a member or a panel user.

## Considered options

- **Trace replies and add routes as reviews find them (ADR 0017).** Each fix
  left another route, and failing closed on writes reported 54 places in
  production, none of them real.
- **Remove the check.** The next reply built from an error's text that review
  missed would show a member Discord's raw text, the problem #524 fixed.
