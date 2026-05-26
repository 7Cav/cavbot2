# ADR 0004: `InteractionResponder` interface + placeholder-vs-Defer convention

## Status

Accepted (0.7.6 / PR #74).

## Decision

Commands take an `InteractionResponder` interface, not `*discordgo.Session`
directly. The interface exposes only the 3 hot-path methods actually used
(`InteractionRespond`, `InteractionResponseEdit`, `FollowupMessageCreate`)
and drops the universally-ignored `*Message` return values.

Two response patterns coexist:

- **Placeholder** (most commands) — `InteractionRespond` immediately with
  `"Fetching X for Y..."`, then `InteractionResponseEdit` to replace.
- **Defer** (`s3aar`, `warden`) — `Defer` then `FollowupMessageCreate`.

The placeholder pattern is a deliberate UX choice: echo back the parsed
argument inside ~200ms so typos are visible before the long upstream call
returns.

## Why

The interface keeps test plumbing minimal and forces deliberate choice of
dependencies. The placeholder/Defer split is documented because tests must
pin `InteractionResponse.Type` faithfully — a placeholder-vs-Defer regression
shouldn't slip past.

## How to apply

- New command → take `InteractionResponder`, not `*discordgo.Session`.
- Default to the placeholder pattern; pick Defer only when there's no
  argument worth echoing back (or the work is sub-200ms).
- Use `errors.As` with `*discordgo.RESTError` code 40060 to detect
  "interaction already acknowledged" (wording-proof rather than matching the
  error string).
