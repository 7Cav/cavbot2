# ADR 0006: Command registry is the single source of truth

## Status

Accepted. **Amended 2026-10-04** (#438). A rename can keep the old name
registered for a set window, so its command ID and the Integrations overrides
Discord keys by it survive the release. `NewRegistry()` still declares every
command: it registers a copy of each renamed command under its old name from
the table in `commands/renamed_commands.go`, until a cleanup release deletes
the row and the startup sync deletes the command.

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
