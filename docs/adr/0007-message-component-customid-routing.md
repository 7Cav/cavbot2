# ADR 0007: Message-component routing via `<command>::<action>::<payload>` CustomID

## Status

Accepted.

## Decision

Commands and message components share one handler in `main.go`, routed by
name:

- `InteractionApplicationCommand` → registry lookup by
  `ApplicationCommandData().Name`.
- `InteractionMessageComponent` → split `CustomID` on `::`, look up the
  **first segment** in the registry.

Stateful component flows use `CustomID` like
`apps_beta_deploy::confirm::<branch>`. Discord caps `CustomID` at 100 chars;
validate at the call site (see `apps_deployer.go`).

## Why

One handler + one routing rule keeps the dispatch table flat. The
`<command>::*` convention means a command's component callbacks land back at
the same registry entry, so state and handler live together.

## How to apply

- New stateful component → `CustomID` is `<command-name>::<action>::<payload>`.
- Validate the assembled `CustomID` length (≤100) before sending. A truncated
  ID silently misroutes.
