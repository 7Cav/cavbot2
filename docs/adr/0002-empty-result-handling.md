# ADR 0002: Empty-result handling — early return only vs. early return + Sentry

## Status

Accepted (0.7.8 / 0.7.9).

## Decision

When a command's roster/lookup returns zero results:

- **User-supplied input** (`/awol`, `/loa` — user types a position) → tell the
  user "no troopers found" and return. No Sentry.
- **Fixed input** (`/afsm`, `/s6-it-check` — Discord choice list or
  hardcoded position) → tell the user "shouldn't happen, reported" **and**
  `utils.CaptureError`. Tag the fixed-input value (e.g. `department`) so each
  choice fingerprints separately.

## Why

Empty is a plausible user outcome when input is user-supplied. Empty from
fixed input is structurally a bug (API regression, roster drift, fuzzy-match
drift, deleted node).

## How to apply

When adding an empty-result early return, ask: *is empty a legitimate outcome
here?* If no, route a synthetic error through `utils.CaptureError` alongside
the user-facing message.
