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

Two commands break that assumption, and their latency panels should be read
accordingly:

- **`/warden purge`** acknowledges, then hands the work to a goroutine
  (`handleWardenPurge`), so the measured latency is roughly the ack, not the
  multi-second purge.
- **`/apps_beta_deploy`** does its real work in the confirm-button handler,
  which is a component interaction and therefore deliberately uncounted. Its
  measured latency covers only the initial prompt.

Both are pre-existing structures, not something the instrumentation changed.
Making their latency honest means moving the work back inside the handler (or
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

| Metric | Type | Labels |
| --- | --- | --- |
| `cavbot2_command_invocations_total` | counter | `command` |
| `cavbot2_command_latency_seconds` | histogram | `command` |

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

These names are chosen to survive a later move to a native `/metrics` endpoint
on the bot, once #98 brings an HTTP server. Swapping the derivation from
Alloy-side to in-process should reuse the same series names and leave dashboards
untouched.

---

## Alloy configuration

> **Not yet applied, and not verifiable from this repo.** This block was written
> against ADR 0011 and the host's documented logs-to-metrics pattern, not
> against the live `config.alloy`. Before applying it: reconcile the
> `forward_to` target and the `container` label with what the existing cavbot2
> pipeline already uses, and run `alloy fmt` plus a config check. Treat the
> stage names and the receiver reference below as placeholders to match to the
> real file.

Alloy already ships cavbot2's stdout to Loki labeled `container="cavbot2"`, so
this adds a processing stage to an existing pipeline rather than a new source.

```alloy
loki.process "cavbot2_command_telemetry" {
  // Reconcile with the receiver the existing cavbot2 pipeline forwards to.
  forward_to = [loki.write.default.receiver]

  stage.match {
    selector = "{container=\"cavbot2\"} |= \"command_invoked\""

    // Pull only the keys the metrics need. Caller identity is deliberately
    // NOT extracted here — it stays in the line body for Loki drill-down.
    stage.logfmt {
      mapping = {
        msg        = "",
        command    = "",
        latency_ms = "",
      }
    }

    // The line carries milliseconds; the histogram is in seconds so the metric
    // name stays portable to a native /metrics endpoint later.
    stage.template {
      source   = "latency_seconds"
      template = "{{ divf (float64 .latency_ms) 1000 }}"
    }

    // `command` becomes a label on both the Loki stream and the derived
    // metrics. Bounded by the command registry (~11 values today).
    stage.labels {
      values = {
        command = "",
      }
    }

    stage.metrics {
      metric.counter {
        name              = "command_invocations_total"
        prefix            = "cavbot2_"
        description       = "Slash command invocations, counted once per dispatch."
        source            = "msg"
        value             = "command_invoked"
        action            = "inc"
        max_idle_duration = "24h"
      }

      metric.histogram {
        name        = "command_latency_seconds"
        prefix      = "cavbot2_"
        description = "Slash command handler wall-time in seconds."
        source      = "latency_seconds"
        // Spans sub-second to well past Discord's 3s ack deadline, which is the
        // reference line worth seeing on the latency panels.
        buckets = [0.1, 0.25, 0.5, 1, 2, 3, 5, 10, 30]
      }
    }
  }
}
```

### Applying it

1. Edit the monitoring host's `config.alloy`, merging the block above into the
   existing cavbot2 pipeline.
2. Validate before reloading: `alloy fmt config.alloy` and a config check.
3. Reload Alloy (`SIGHUP`, or the container restart the host normally uses).
4. Confirm the series exist in Prometheus:
   `count by (command) (cavbot2_command_invocations_total)` should return one
   row per command that has run since the reload.
5. Confirm the drill-down still works in Loki:
   `{container="cavbot2"} | logfmt | msg="command_invoked" | username="..."`.

Sequencing is low-risk in either order. The bot's line is inert until Alloy
parses it, so the emitter can ship (and Watchtower can deploy it) well before
this block is applied.

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
          "expr": "sum by (command) (rate(cavbot2_command_invocations_total[1h]))",
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
          "expr": "sum by (command) (increase(cavbot2_command_invocations_total[$__range]))",
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
          "expr": "histogram_quantile(0.95, sum by (command, le) (rate(cavbot2_command_latency_seconds_bucket[1h])))",
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
