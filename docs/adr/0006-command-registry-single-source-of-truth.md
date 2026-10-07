# ADR 0006: Command registry is the single source of truth

## Status

Accepted.

## Decision

`commands/registry.go` `NewRegistry()` is the **only** place commands are
declared. On startup `main.go` syncs the guild with `Registry.Sync`:

1. Fetches all currently-registered guild commands from Discord, to name the
   ones it removes in the log.
2. Sends every command in the registry in one bulk overwrite. Discord keeps
   the ID of each command whose name it already holds, creates the new ones,
   and **deletes any Discord command not present in the registry.**

Commands are registered **per-guild** (not globally), so they appear instantly.

## Why

A divergence between Discord's command list and the in-repo registry is
silently confusing — orphaned commands keep working until someone notices.
The startup sync makes the registry authoritative and removes any manual
cleanup step when a command is removed or renamed.

Each start sends one request rather than one per command. Discord paced
per-command creates to about a minute per start, and the panel waited on them
(#470). Discord keys a
command's permission overrides by its ID, so a sync must keep IDs. The bulk
overwrite does, including for a command whose definition changed, as checked
on the test guild.

## How to apply

- Adding a command: write `func MyCmd() Command` returning `{Definition,
  Handler}` and append to the `RegisterCommands(...)` call in
  `NewRegistry()`. Nothing else.
- Removing or renaming a command: delete it from the registry. The next
  startup cleans up the Discord side automatically.
