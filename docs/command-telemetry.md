# Command telemetry

How per-command usage — invocation counts, latency, and who ran what — gets
from the bot to Grafana at `metrics.7cav.us`.

The short version: the bot emits **one structured log line per slash-command
invocation**, and the monitoring host's existing Alloy collector turns those
lines into Prometheus metrics. The bot carries no Prometheus client and exposes
no HTTP endpoint. Failures are not part of this signal at all — Sentry owns
those, and already alerts on them. See [ADR 0011](adr/0011-command-telemetry-via-log-derived-metrics.md)
for why it is shaped this way, and `CONTEXT.md` for the glossary terms.

This file is the **collector's half of the contract**. The emitter's half lives
in `commands/telemetry.go`. They are documented together on purpose: a change to
one without the other breaks metric derivation silently.

---

## The `command_invoked` line

Emitted at `INFO`, in logfmt (the bot's slog text handler), once per slash
command. Component/button interactions are deliberately **not** emitted — a
button press continues a command that was already counted.

```
time=2026-08-06T14:22:31.884Z level=INFO msg=command_invoked command=warden latency_ms=843 discord_id=246813579 username=trooper.j opt_command=add opt_flag=internal opt_discordname=Smith.J
```

### Contract keys

These four are always present. Alloy matches on the `msg` marker and reads these
by name; renaming any of them is a parsing-contract change, not a cosmetic edit.

| Key | Type | Notes |
| --- | --- | --- |
| `msg` | string | Always `command_invoked`. The marker Alloy selects on. |
| `command` | string | Top-level command name, as registered. The **only** value promoted to a Prometheus label. |
| `latency_ms` | integer | Handler wall-time in **whole milliseconds**. Divided by 1000 on the collector side to feed a `_seconds` histogram. See the caveat below. |
| `discord_id` | string | Invoking user's Discord snowflake. Loki only — never a metric label. |
| `username` | string | Invoking user's Discord username. Loki only — never a metric label. |

### Drill-down keys

Present only when the interaction carries them. These exist so usage can be
broken down in Loki (warden internal vs external, say) **without** touching the
metric or its cardinality.

| Key | Notes |
| --- | --- |
| `subcommand` | The invoked subcommand path, dotted for a subcommand group (`roles.add`). Absent when the command has no true subcommand. |
| `opt_<name>` | One key per option, e.g. `opt_flag=internal`. The `opt_` prefix keeps an option named `command` — warden has one — from colliding with the contract key. String values are truncated to 64 runes, so a bulk-entry option cannot emit a multi-kilobyte record. |

Option names come from the command definitions, so the `opt_*` key space is
bounded by the registry — but do not promote any of them to a Prometheus label
without checking the value space first. `opt_discordname` is free text.

### Caveat: latency is handler wall-time, which is not always work time

`latency_ms` measures how long the registered handler ran. For most commands
that is the wait the user actually felt, because the placeholder / defer /
followup patterns (ADR 0004) do their upstream work synchronously inside the
handler.

One command breaks that assumption, and its latency panel should be read
accordingly:

- **`/warden purge`** acknowledges, then hands the work to a goroutine
  (`handleWardenPurge`), so the measured latency is roughly the ack, not the
  multi-second purge.

That is a pre-existing structure, not something the instrumentation changed.
Making its latency honest means moving the work back inside the handler (or
counting a completion separately), which is its own change.

### Why caller identity is not a label

`discord_id` and `username` stay in the log line. Promoting either to a
Prometheus label would create one time series per user per command and grow
without bound, and it would put per-user data in a 90-day metrics store. The
question "who used `/warden` the most?" is answered in Loki instead, on demand,
against logs with a shorter retention. This mirrors the cardinality discipline
the NPM and fail2ban blocks on this host already follow (client IP stays in the
line).

---

## Derived metrics

| Declared in Alloy as | Stored in Prometheus as | Type | Labels |
| --- | --- | --- | --- |
| `cavbot2_command_invocations_total` | `loki_process_custom_cavbot2_command_invocations_total` | counter | `command` |
| `cavbot2_command_latency_seconds` | `loki_process_custom_cavbot2_command_latency_seconds` | histogram | `command` |

**Always query the prefixed name.** Alloy namespaces every `stage.metrics`
metric with `loki_process_custom_`, and the prefix is not configurable. Querying
the declared name returns zero rows against a perfectly correct config — this is
verified behaviour on the host, where `npm_requests_total` has no series but
`loki_process_custom_npm_requests_total` has 404k.

Each series also carries `component_id`, `component_path`, `instance` and
`job="alloy"` from the collector. `sum`/`count by (command)` collapses them, but
alert rules need to expect them.

There is deliberately **no `status` or `error` label**. "No results found" is a
successful outcome for the free-input commands; the cases where empty is a
genuine fault already route to Sentry (ADR 0002), and panics reach Sentry via
`RecoverPanic`. Sentry owns the whole failure signal, and per-command error rate
is answerable there through the `command` tag that `CaptureError` and
`RecoverPanic` now set.

### The Sentry `command` tag

`CaptureError` and `RecoverPanic` promote a `command` key/value to a Sentry
**tag**, because Sentry groups and filters on tags, not on the extra context.
The value must be the **registered slash-command name** — `warden`,
`s6-it-check`, `gamertag_search` — since anything else splits one command's
failures across several groups, or (for a component CustomID, which can embed
free user input) gives a bounded dimension an unbounded value space.

Two consequences worth knowing when adding a capture site:

- Pass `"command", "<registered name>"`. If you also want the subcommand,
  pass it separately as `"subcommand"` — that is why `/warden`'s captures no
  longer put its subcommand under `command`.
- Some capture sites still carry no `command` at all — the direct
  `utils.CaptureError` calls in `afsm.go`, `s6_trackers.go`, `awol.go` and
  `star_citizen_joiners.go`. Their events simply go untagged rather than
  wrongly tagged. Adding the key at those sites would widen tag coverage and
  is worth doing, but it was left out of the telemetry change.

### Correction to ADR 0011: these names are *not* drop-in portable

ADR 0011 records that the metric names are "chosen to be drop-in portable to a
native `/metrics` endpoint" later, so that "moving the counter from
Alloy-derived to native reuses the same series names; dashboards do not change."

**That is not true, and the reason is the `loki_process_custom_` prefix above.**
A native endpoint on the bot would expose `cavbot2_command_invocations_total`;
the Alloy-derived series is `loki_process_custom_cavbot2_command_invocations_total`.
Those are different series, so a later swap either renames every dashboard query
or needs a Prometheus recording rule / `metric_relabel_configs` to bridge them.

The decision itself still stands — log-derived metrics remain the right call for
a bot with no HTTP surface — but the portability rationale was written on a
false premise and ADR 0011 should be amended to say so.

---

## Alloy configuration

Written against the live `/etc/compose/monitoring/alloy/config.alloy` on
`7cav-prod` (Alloy **v1.7.1**, Loki 3.4.2), not inferred. The syntax below —
`stage.labels`' `values = {}` form, the `loki.write.default.receiver` reference,
the `container` label — is copied from what that file already does. What is
*not* verified is listed under "Before you apply this".

### The wiring: insert, do not add a parallel reader

There is no cavbot2-specific component on the host today. cavbot2 is picked up
by the catch-all `loki.source.docker "containers"`, which forwards **straight to
the sink** with no processing in between.

So the change is to splice a `loki.process` into that existing path. Adding a
second `discovery.docker` + `loki.source.docker` scoped to cavbot2 would give
two independent readers of the same container's stdout — and because this
pipeline attaches a `command` label, the copies land in *different* Loki
streams, so Loki will not dedupe them. That doubles every cavbot2 log line and
every count.

One line changes in the existing source block:

```alloy
loki.source.docker "containers" {
  host             = "unix:///var/run/docker.sock"
  targets          = discovery.docker.containers.targets
  relabel_rules    = discovery.relabel.containers.rules
  forward_to       = [loki.process.cavbot2.receiver]   // was: loki.write.default.receiver
  refresh_interval = "10s"
}
```

> **Blast radius: this touches every container on the host, not just cavbot2.**
> All container logs now flow through the new component. A `stage.match` scopes
> the processing to cavbot2 and everything else passes through untouched — but
> if the new block fails to load, *all* container logging stops. After
> restarting, check that an unrelated container still has recent lines
> (`{container="prometheus"}`) before trusting the cavbot2 check.

### The block

```alloy
// ---- 4. CAVBOT2 COMMAND TELEMETRY --------------------------------------------
// Tapped off the shared container stream, NOT a second Docker reader.
//
// Sample line (bare logfmt — loki.source.docker reads the Engine API, so the
// json-file driver's {"log":…,"stream":…} wrapper is already stripped and no
// stage.docker / stage.cri is needed):
//
//   time=2026-08-06T14:22:31.884Z level=INFO msg=command_invoked command=warden
//   latency_ms=843 discord_id=246813579 username=trooper.j opt_flag=internal

loki.process "cavbot2" {
  // Match on `container`, which discovery.relabel sets from the Docker name.
  // Do NOT match on service_name or detected_level: Loki 3.x adds those at
  // ingest, so they do not exist inside the Alloy pipeline.
  //
  // The marker is deliberately ASCII. Gating on a "✨ Done!"-style line would
  // put a multi-byte emoji inside a quoted Alloy string inside a LogQL filter,
  // and would also count the wrong thing — see "Why this line" below.
  stage.match {
    selector = "{container=\"cavbot2\"} |= \"command_invoked\""

    // Only what the metrics need. Caller identity is deliberately NOT
    // extracted — it stays in the line body for Loki drill-down.
    stage.logfmt {
      mapping = {
        command    = "",
        latency_ms = "",
      }
    }

    // The line carries whole milliseconds as a bare integer, so float64 parses
    // it directly. sprig/v3 is linked into the v1.7.1 binary, so divf resolves.
    stage.template {
      source   = "latency_seconds"
      template = "{{ divf (float64 .latency_ms) 1000 }}"
    }

    // Bounded label: one per registered command. Never username or discord_id.
    stage.labels {
      values = {
        command = "",
      }
    }

    stage.metrics {
      metric.counter {
        name        = "cavbot2_command_invocations_total"
        description = "cavbot2 slash-command invocations, by command"
        source      = "command"
        action      = "inc"
        // NOT optional, and deliberately not copied from the existing blocks,
        // which omit it. On this host loki_process_custom_fail2ban_events_total
        // is present at roughly 6% of scrapes because its series is reaped
        // between events; every reappearance reads as a counter reset and
        // rate()/increase() go unreliable. cavbot2 runs ~10 invocations/day and
        // /s6-it-check about 3 per MONTH, so a week is not enough headroom.
        max_idle_duration = "720h"
      }

      metric.histogram {
        name        = "cavbot2_command_latency_seconds"
        description = "cavbot2 slash-command handler wall-time in seconds"
        source      = "latency_seconds"
        // Sub-second through well past Discord's 3s ack deadline, which is the
        // reference line worth seeing on the latency panels.
        buckets           = [0.1, 0.25, 0.5, 1, 2, 3, 5, 10, 30]
        max_idle_duration = "720h"
      }
    }
  }

  // Everything — cavbot2 and not — continues to the sink unchanged.
  forward_to = [loki.write.default.receiver]
}
```

### Why this line, and not `✨ Done!`

Worth stating, because gating on the existing house log lines is the obvious
move and it is wrong twice over.

`command=` already appears on `🚀 Starting …`, `Returning …` and `✨ Done!`.
Measured over 30 days on the host: 680 lines carry `command=`, but only 302 are
actual invocations. A counter keyed on "line has a command field" reads **2.25×
high**, and the inflation varies per command — chattier commands inflate more —
so it is not even a constant you could divide out.

`✨ Done!` is the only reliable anchor among those, and it counts *completed*
invocations, dropping any command that errors before its terminal line.

`command_invoked` sidesteps both. It is emitted exactly once per invocation,
from a `defer`, so it fires even when the handler panics — an honest
denominator. It is also the only line carrying a machine-readable duration:
`duration=` appears solely on `API Call Finished` lines, never co-occurs with
`command=`, and is a Go `time.Duration` string whose unit varies with magnitude
(`83.4µs`, `106.548123ms`, `1m30s`), so it cannot be scaled by a flat divide.

One consequence of the registered-name convention: the historical `✨ Done!`
lines carry display names (`Milpac`, `S6ITCheck`), while `command_invoked`
carries registered names (`milpac`, `s6-it-check`). The metric is built fresh
off `command_invoked`, so it is internally consistent — but a LogQL query
spanning both line types needs to expect both spellings.

### Before you apply this

Verified: the receiver name, the `container` label, `stage.labels` syntax, the
Alloy version, sprig availability, and that the line arrives bare.

Still unproven, in rough order of how likely it is to bite:

1. **`stage.match` is used nowhere in this stack.** Both existing pipelines have
   a dedicated source per job, so they never needed to filter. This block
   introduces the pattern; there is no working local example to copy. If the
   counter stays empty, look here first — the fallback is to drop the selector
   and instead `stage.logfmt` unconditionally, then `stage.drop` entries where
   `command` is empty.
2. **`stage.metrics` nested inside `stage.match`.** Both existing counters sit
   at the top level of their `loki.process`. Nesting should be fine but is
   untested here.
3. **`metric.histogram` has never been defined on this host.** Both existing
   derived metrics are counters. Buckets, and whether `max_idle_duration` is
   accepted on a histogram, are unexercised.
4. **That the whole file still parses.** Nothing has been run through
   `alloy fmt` or a config check.

### Applying it

1. Edit `/etc/compose/monitoring/alloy/config.alloy` — add the block, change the
   one `forward_to` line in `loki.source.docker "containers"`.
2. `alloy fmt` the file and run a config check before restarting.
3. **`docker compose restart alloy`.** The config is a single-file bind mount,
   so the inode changes on edit and the container keeps the old one — a reload
   signal will not pick it up.
4. Confirm nothing else broke first:
   `{container="prometheus"}` should still show recent lines in Loki.
5. Then confirm the metric, **using the prefixed name**:

   ```promql
   count by (command) (loki_process_custom_cavbot2_command_invocations_total)
   ```

   Only commands invoked since the restart appear. For an immediate signal, run
   `/zulu` yourself and watch for the row — `/s6-it-check` may take weeks.
6. Confirm the drill-down:
   `{container="cavbot2"} | logfmt | msg="command_invoked" | username="..."`.

Sequencing is low-risk in either order. The bot's line is inert until Alloy
parses it, so the emitter can ship (and Watchtower can deploy it) well before
this block is applied.

For validating the dashboard once it is live, the true 30-day invocation counts
measured from the host were: Awol 82, Warden 74, Milpac 71, Zulu 41, LOA 21,
AFSM 10, S6ITCheck 3 — 302 total. Four registered commands
(`gamertag_search`, `s3aar`, `apps_beta_deploy`, `warden-bulkadd-internal`) were
not invoked at all in that window, so expect at most 7 rows initially.

---

## Starter Grafana dashboard

Import this as a starting point and adjust the datasource UIDs to the host's
Prometheus and Loki. Panels 1 and 2 answer "what gets used" and "what is slow";
panel 3 is the caller drill-down that intentionally has no metric behind it.

```json
{
  "title": "CavBot2 — Command usage",
  "uid": "cavbot2-command-usage",
  "schemaVersion": 39,
  "time": { "from": "now-7d", "to": "now" },
  "panels": [
    {
      "type": "timeseries",
      "title": "Invocations per command",
      "gridPos": { "h": 9, "w": 12, "x": 0, "y": 0 },
      "targets": [
        {
          "expr": "sum by (command) (rate(loki_process_custom_cavbot2_command_invocations_total[1h]))",
          "legendFormat": "{{command}}"
        }
      ]
    },
    {
      "type": "barchart",
      "title": "Total invocations (range)",
      "gridPos": { "h": 9, "w": 12, "x": 12, "y": 0 },
      "targets": [
        {
          "expr": "sum by (command) (increase(loki_process_custom_cavbot2_command_invocations_total[$__range]))",
          "legendFormat": "{{command}}",
          "instant": true
        }
      ]
    },
    {
      "type": "timeseries",
      "title": "Latency p95 per command",
      "description": "Discord's ack deadline is 3s; anything approaching it is worth attention.",
      "gridPos": { "h": 9, "w": 24, "x": 0, "y": 9 },
      "fieldConfig": { "defaults": { "unit": "s" } },
      "targets": [
        {
          "expr": "histogram_quantile(0.95, sum by (command, le) (rate(loki_process_custom_cavbot2_command_latency_seconds_bucket[1h])))",
          "legendFormat": "{{command}}"
        }
      ]
    },
    {
      "type": "logs",
      "title": "Who ran what (Loki drill-down)",
      "description": "Caller identity lives here, not in Prometheus.",
      "gridPos": { "h": 10, "w": 24, "x": 0, "y": 18 },
      "targets": [
        {
          "expr": "{container=\"cavbot2\"} | logfmt | msg=\"command_invoked\""
        }
      ]
    }
  ]
}
```

Access is whatever `metrics.7cav.us` already enforces — XenForo OAuth gated to
forum-staff groups. Nothing here adds a new auth surface.

---

## Drift protection, and what it does not cover

`commands/telemetry_test.go` decodes the emitted line with a real logfmt decoder
and asserts the marker and each contract key **by string literal**, never via
the package constants — a test that referenced the constants would rename in
lockstep with the emitter and stay green while this collector config silently
stopped matching. That is the same drift hazard LOA label parsing has hit
before, guarded the same way.

**What that guard does not do:** it never executes Alloy. It pins the emitter's
half of the contract only. If the block above is edited to read a key the bot
does not emit, or the bot's keys are changed without editing this file, nothing
in CI will fail — the dashboards will just go flat. The two halves are kept in
one document so that changing one puts the other in front of you; that
co-location is the only thing linking them.
