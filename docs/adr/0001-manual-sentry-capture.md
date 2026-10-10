# ADR 0001: Manual Sentry capture at chosen sites, not a slog bridge

## Status

Accepted (0.7.7).

## Decision

Sentry is wired manually via `utils.CaptureError(msg, err, kv...)` at
chosen call sites — not as a slog handler that auto-forwards every WARN/
ERROR log.

## Why

A blanket slog→Sentry bridge would forward expected, non-actionable noise
(e.g. the LOA refresh job's `"LOA cache refresh failed"` × N nodes on every
dev startup where the forum DB isn't reachable). Manual capture keeps Sentry
signal-rich.

## How to apply

- New genuine internal failure → `utils.CaptureError`.
- New user-facing response from a command (incl. "no troopers found", "no
  LOAs") → `refuse` for a refusal, `replyError` for any other error reply
  (`commands/interaction_ack.go`). Both send through `utils.HandleError`.
  Never wire Sentry into the user-facing path.
- The `utils.Error` vs `utils.HandleError` split is load-bearing; preserve it
  on refactors.
