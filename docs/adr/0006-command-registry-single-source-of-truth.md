# ADR 0006: Command registry is the single source of truth

## Status

Accepted.

## Decision

`commands/registry.go` `NewRegistry()` is the **only** place commands are
declared. On startup `main.go`:

1. Fetches all currently-registered guild commands from Discord.
2. **Deletes any Discord command not present in the registry.**
3. Re-registers every command in the registry.

Commands are registered **per-guild** (not globally), so they appear instantly.

## Why

A divergence between Discord's command list and the in-repo registry is
silently confusing — orphaned commands keep working until someone notices.
The startup sync makes the registry authoritative and removes any manual
cleanup step when a command is removed or renamed.

## How to apply

- Adding a command: write `func MyCmd() Command` returning `{Definition,
  Handler}` and append to the `RegisterCommands(...)` call in
  `NewRegistry()`. Nothing else.
- Removing or renaming a command: delete it from the registry. The next
  startup cleans up the Discord side automatically.
