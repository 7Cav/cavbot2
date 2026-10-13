---
status: accepted
---

# The error data check reports where data leaves an error

The gate's `tools/errorreply` check (ADR 0016) traced each reply, channel
post and panel answer back to the data it was built from, and each review
found another route it missed (#547, #551, #559, #560, #569, #645, #646), all
in code nobody had written. The check now reports where code takes data out
of an error, and fails closed on any use of that data it has no rule for. Its
package doc lists the ways out it reads and what passes. Decided while
fixing #645 and #646, superseding ADR 0017.

The check guards against the shapes the bot's code writes, and is no proof
that error data never reaches a person. A way past it counts as a bug only
when code on develop, or in the pull request under review, takes it. A probe
written to get past the check is not a finding, and nobody files an issue
for one. A leak in code nobody has written costs less than another round of
closing routes.

## Consequences

- Data from an error fails even where no person sees it, such as a Sentry
  dedup key built from a status code. Some idiomatic shapes fail too, such as
  `bytes.Contains` on a response body, and need rewriting into one the check
  reads.
- A SHA-256 digest of an error's text passes, since it can only be compared.
  The failed start keeps one on disk to tell a repeat from a new failure, so
  a restart loop sends Sentry one event an hour instead of one a minute.
- The check knows of these ways past it and accepts them:
  - An unexported field. fmt prints one by reflection without calling
    `Error`, so an error in it shows its data unless the error is a pointer.
  - `unsafe`, which lint keeps out of production code.
  - What a library does beyond its signature.
  - The tools under `tools/`, which answer the maintainer, not a member or a
    panel user.

## Considered options

- **Trace replies and add routes as reviews find them (ADR 0017).** Each fix
  left another route, and failing closed on writes reported 54 places in
  production, none of them real.
- **Remove the check.** The next reply built from an error's text that review
  missed would show a member Discord's raw text, the problem #524 fixed.
