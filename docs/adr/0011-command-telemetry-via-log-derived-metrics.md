# ADR 0011: Command telemetry via log-derived metrics, not a `/metrics` endpoint

## Status

Accepted. **Amended 2026-08-06** — the portability rationale below was written
on a false premise; see *Amendment: metric names are not drop-in portable*. The
decision itself is unchanged.

## Context

Issue #101 wants per-command usage — invocation counts, latency, and who is
calling what. The 7Cav production host (the same box cavbot2 runs on) carries a
mature Grafana / Prometheus / Loki / Alloy stack. Two facts decided the shape:

1. **Alloy already ships every container's stdout to Loki**, cavbot2 included,
   labeled `container="cavbot2"`. The bot's logs are already queryable in
   Grafana with no infra change.
2. **The house pattern is logs → Alloy-derived Prometheus metrics.** The
   NPM-access-log and fail2ban blocks parse log lines and promote *bounded*
   labels to Prometheus counters via `stage.metrics`, keeping high-cardinality
   identifiers (client IP) in the log line only. That is precisely the
   count/latency/caller shape #101 needs, cardinality discipline included.

cavbot2 has no inbound HTTP surface today.

## Decision

- **Usage → the Grafana stack. Failures → Sentry (unchanged, ADR 0001).** The
  two channels never overlap. A usage metric carries no error signal; a failure
  is never counted as telemetry. Sentry already emails and pings Discord on
  failure, so no second alerting surface is built.
- **The bot emits one structured logfmt line per slash-command invocation** —
  `msg="command_invoked"` with keys `command`, `latency_ms`, `discord_id`,
  `username` — from a wrapper applied at the registry (ADR 0006), automatic for
  every command. The bot ships **no Prometheus client**.
- **Alloy derives the metrics off-box.** `cavbot2_command_invocations_total{command}`
  (counter) and `cavbot2_command_latency_seconds{command}` (histogram) come from
  a `loki.process "cavbot2"` block on the metrics host. Caller identity
  (`discord_id`, `username`) stays in the log line — queryable in Loki, never a
  Prometheus label. The Alloy block and a starter Grafana dashboard are
  specified in `docs/command-telemetry.md` and applied on the host by hand.
- **Metric names are chosen to be drop-in portable to a native `/metrics`
  endpoint** later, when #98 (the web frontend) brings an HTTP server. Moving
  the counter/histogram from Alloy-derived to native then reuses the same series
  names; dashboards do not change. ⚠️ **Superseded — see the amendment below.**

## Considered options

- **Native `/metrics` in the bot** (the `7cav-api:9090` pattern) — rejected for
  now. It adds an inbound HTTP surface to a bot that has none, and caller
  identity would *still* need the log path (cardinality), so it is strictly more
  work. Deferred to the #98 era, when its HTTP server exists — though the swap
  is **not** as clean as this ADR originally claimed; see the amendment.
- **Loki-only LogQL dashboards** — rejected. No first-class Prometheus metrics,
  weaker for long-range queries and (future) alerting.

## Consequences

- **The `command_invoked` line is a cross-repo contract** — emitted here, parsed
  by Alloy in the monitoring repo. Format drift breaks metric derivation
  *silently*, the same failure mode as PAF / LOA label drift. Guarded two ways:
  a golden test in cavbot2 pins the line's marker and keys, and the Alloy parse
  spec is co-located in `docs/command-telemetry.md` so both halves change
  together.
- **No `status` label on the counter.** "No results" is a *successful* outcome
  for free-input commands; the one case where empty is a genuine error
  (fixed-input, `/afsm`, `/s6-it-check`) already routes to Sentry via ADR 0002,
  and panics already reach Sentry via `RecoverPanic`. A status label would
  duplicate a signal Sentry already owns end to end.
- **Only slash commands are counted.** Component (button) interactions are
  continuations of a command already counted; counting them would double-count
  one logical use.

## Amendment: metric names are not drop-in portable (2026-08-06)

Established when the Alloy block was written against the live host rather than
against this ADR.

**Alloy namespaces every `stage.metrics` metric with `loki_process_custom_`, and
the prefix is not configurable.** A block declaring
`name = "cavbot2_command_invocations_total"` stores
`loki_process_custom_cavbot2_command_invocations_total` in Prometheus. This is
observed behaviour on the host, where `npm_requests_total` has no series at all
while `loki_process_custom_npm_requests_total` has 404k.

So the portability rationale above is wrong. A native `/metrics` endpoint on the
bot would expose the *unprefixed* name — a different series from the one the
dashboards are built on. The later swap therefore costs either a rewrite of
every dashboard query or a Prometheus recording rule / `metric_relabel_configs`
bridging the two names.

What this does **not** change:

- **The decision stands.** Log-derived metrics are still right for a bot with no
  inbound HTTP surface, and the alternative was rejected on the surface-area and
  cardinality arguments, not on portability. Portability was a supporting
  comfort, not the reason.
- **The names stay as they are.** Choosing a name to pre-compensate for the
  prefix would make the Alloy-side series read
  `loki_process_custom_<something-mangled>`, which is worse to live with today
  in exchange for a migration that may never happen.

What it does change: **every query must use the prefixed name.** Querying the
declared name returns zero rows against a perfectly correct config, which reads
as "the instrumentation is broken" rather than "the query is wrong". The series
also carry `component_id`, `component_path`, `instance` and `job="alloy"` from
the collector, which alert rules need to expect.

`docs/command-telemetry.md` carries the working queries.
