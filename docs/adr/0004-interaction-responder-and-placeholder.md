# ADR 0004: `InteractionResponder` interface + interaction-response conventions

## Status

Accepted (0.7.6 / PR #74).

## Decision

Commands take an `InteractionResponder` interface, not `*discordgo.Session`
directly. The interface exposes only the 3 hot-path methods actually used
(`InteractionRespond`, `InteractionResponseEdit`, `FollowupMessageCreate`)
and drops the universally-ignored `*Message` return values.

Four response patterns are documented; three are in use:

- **Placeholder** (most commands) — `InteractionRespond` immediately with
  `"Fetching X for Y..."`, then `InteractionResponseEdit` to replace.
- **Deferred edit** (`warden`) — defer an *ephemeral* response
  (`InteractionResponseDeferredChannelMessageWithSource` +
  `MessageFlagsEphemeral`), then fill it in with `InteractionResponseEdit`.
  The `deferEphemeral` / `editEphemeral` / `editEphemeralWithEmbed` helpers in
  `commands/warden.go` wrap this. Warden does **not** use
  `FollowupMessageCreate`.
- **Deferred followup** (`s3aar`) — defer a public response, then post the
  result with `FollowupMessageCreate`. One run can fire several followups
  (debug output, per-step errors), which is why it follows up rather than
  editing a single deferred reply.
- **Button confirmation** — does not defer at all. The slash command replies
  immediately and ephemerally (`InteractionResponseChannelMessageWithSource` +
  `MessageFlagsEphemeral`) with Confirm/Cancel buttons. The button click swaps
  that message via `InteractionResponseUpdateMessage`, and once the confirmed
  action completes the result goes out as a *public* `FollowupMessageCreate`
  (no flags) so the channel can see it.

The placeholder pattern is a deliberate UX choice: echo back the parsed
argument inside ~200ms so typos are visible before the long upstream call
returns.

For the deferred patterns, defer when there's no argument worth echoing back.
An edit replaces the single deferred message in place; a followup posts a new
message. `warden` has one result to show, so it edits; `s3aar` may emit
several, so it follows up.

## Why

The interface keeps test plumbing minimal and forces deliberate choice of
dependencies. The patterns are documented because tests must pin
`InteractionResponse.Type` faithfully — if a command quietly switches between,
say, a placeholder and a defer, the test should catch it.

## How to apply

- New command → take `InteractionResponder`, not `*discordgo.Session`.
- Default to the placeholder pattern; defer only when there's no argument
  worth echoing back (or the work is sub-200ms). Once deferred, either edit
  the deferred reply (`InteractionResponseEdit`) or post a `FollowupMessageCreate`.
  Edit when there's one result message; follow up when you may emit several.
- Use `errors.As` with `*discordgo.RESTError` code 40060 to detect
  "interaction already acknowledged" (wording-proof rather than matching the
  error string).
