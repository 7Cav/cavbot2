# CONTEXT.md — CavBot2

Domain language used throughout the codebase. New terms that need explanation
belong here, not inline in code comments.

## Organization

- **7Cav / 7th Cavalry Gaming Regiment** — the gaming community this bot
  serves. Project lives at `github.com/7Cav/cavbot2`. Member-facing reference
  for org structure and terminology is the [7Cav wiki](https://wiki.7cav.us/).
- **Departments (AFSM enum)** — the fixed set of departments `/afsm` checks
  for award eligibility: `S1` (Personnel), `S2` (Intelligence), `S3`
  (Operations), `S5` (Public Affairs), `S6` (Information Systems), `S7`
  (Training), `WAG` (Wiki Admin Group), `RTC` (Recruit Training Command),
  `RRD` (Regimental Recruiting Department), `MP` (Military Police), `ODS`
  (Officer Development School), `NCOA` (Non-Commissioned Officer Academy).
  Canonicalized in `commands/afsm.go`.
- **Position** — free-text string like `2/B/1-7`, `Reservist`, or a
  department name (`S1`).
- **Position group** — the milpac's grouping of positions into named units
  (e.g. `D/ACD`, `A/1-7`). `ACD` is a battalion-level group; `D/ACD` is a
  company within it. Exposed by the API's position-group hierarchy and used as
  the unit vocabulary for roster lookups.
- **Regiment time (UTC) / Zulu** — UTC is 7Cav standard time; **Zulu** is its
  member-facing name, written with a `z` suffix (`2300z`). Wherever the bot has
  to decide what calendar day something falls on (e.g. AWOL day-counting), a
  "day" is a **UTC calendar date**. _Avoid_: GMT.

## Member records

- **MILPACS / milpac** — the regiment's personnel record system served by
  `api.7cav.us`; colloquially, "milpac" means one trooper's record.
- **Forum username** — the user's Xenforo handle, also stored on the milpac.
  Used as the lookup key for `GetMilpacByUsername` (the post-Keycloak path).
- **Keycloak ID** — legacy identity field on profile responses; removed in
  0.7.9 after the upstream Keycloak path was retired. Use
  `GetMilpacByUsername` (forum username) instead.
- **Roster** — a department- or position-scoped list returned by
  `GetRosterByFuzzyPositionSearch`. Empty rosters from fixed-input commands
  (`/afsm`, `/s6-it-check`) are structurally a bug, not a user-input issue —
  see ADR 0002.

## Forum + LOA

- **Xenforo forum** — the regiment's web forum. The bot reads its MySQL DB
  (`xf_thread`, `xf_post`) for LOA scanning.
- **LOA (Leave of Absence)** — a forum thread declaring a member away from
  duty for a date range. Filed via a **PAF** and parsed from its Xenforo
  BBCode template (labels around the **Subject**'s username, `Start Date`, and
  `End Date`). The PAF records both a **Submitter** and a **Subject**; the LOA
  belongs to the **Subject** — that is who `/loa` and `/awol` key on, never the
  Submitter. Template drift silently breaks parsing. One thread is exactly
  one LOA — a second LOA always means a new thread, so `ThreadID` uniquely
  identifies an LOA. **Filing an LOA is itself a forum post**, so it resets the
  trooper's last-post clock; this is why a long unexcused gap immediately
  followed by an LOA is rare in practice (the accountable-day model still
  handles it correctly if it occurs).
- **PAF (Personnel Action Form)** — a forum form for filing a personnel
  action. An LOA is filed via an LOA-request PAF, which records both a
  Submitter and a Subject. A PAF's field labels are what the bot parses, so a
  forum-side relabel is a change to the bot's parsing contract, not a cosmetic
  edit.
- **Subject** — the trooper an LOA is *for*; the person going on leave. The
  LOA belongs to the Subject, and it is the only party the bot attributes the
  LOA to. _Avoid_: bare "Username" — the PAF historically carries the Submitter
  and the Subject under the same `Username` label, so "the username" is
  ambiguous.
- **Submitter** — the account that files a PAF. For an LOA this may be the
  Subject themselves (a self-request) or someone acting on their behalf (e.g.
  their squad lead). The bot never attributes the LOA to the Submitter.
  _Avoid_: author, poster.
- **LOA node** — a Xenforo forum section that hosts LOA threads. Production
  scans five (`180,400,540,178,369`); the code default is `180`.
- **LOA cache** — `utils.GlobalLOACache`, the in-process cache populated by a
  15-minute background refresh. Tracks per-cache health via
  `IsHealthy(maxAge) → (bool, lastSuccess)`. `/loa` and `/awol` consult this
  rather than hitting the forum DB synchronously.
- **Incremental refresh** — refreshes walk forward from `lastSyncedPostDate`,
  not a full re-scan. Expired entries (past `EndDate`) are pruned each pass.

## Eligibility / tracker concepts

- **AFSM (Armed Forces Service Medal)** — an award. `/afsm` reports members
  whose milpac record indicates they meet the bar for the medal but haven't
  received it yet, scoped to one of the departments in the AFSM enum above.
- **S6-IT full status** — promotion from probationary to full member of the
  S6 IT team. `/s6-it-check` enumerates eligible members.
- **AWOL** — a trooper who has not posted on the 7Cav forums within the last
  **7 days**. Active membership requires at least one forum post per week
  (typically a roll-call post, but *any* post on the 7Cav forums qualifies a
  trooper as not AWOL). AWOL is a bot-derived signal computed from forum
  activity — it has **nothing to do with the milpac record**. `/awol` lists
  current AWOL candidates for a position, with an "On LOA" indicator sourced
  from the LOA cache.
- **Accountable day** — a UTC calendar date that counts toward a trooper's
  AWOL total. The candidate dates are those strictly after the trooper's last
  forum post up to and including today (`(lastPostDate, today]`); a date is
  **accountable** unless it is covered by an LOA. A trooper is an AWOL
  candidate when their accountable days exceed 7. Because coverage is a set of
  dates, overlapping, adjacent, and future LOAs need no special handling — the
  union of covered dates falls out naturally.
- **Accuracy disclaimer** — because milpac records are user-entered free
  text, parsing can drift. `/afsm` always renders the disclaimer, regardless
  of whether the eligibles list is empty — see ADR 0002.

## Member welfare

- **Helpline card** — the set of crisis and mental-health support resources
  `/helpline` renders, optionally addressed to a member. Every resource on it is
  an **external, independent organisation**; the regiment designates no internal
  crisis contact, because those services are staffed, trained, and continuously
  available in a way a volunteer roster is not. The card's phone numbers and
  dial sequences are an external contract with the same silent-drift hazard as
  PAF labels — a changed number is a correctness change, not a copy edit.

## Warden roles

The `/warden` command family applies and removes a small set of Discord roles by
name. The bot's concern ends at role membership — whatever access a Warden role
grants is configured Discord-side and is out of scope here.

- **Warden role** — a Discord role the `/warden` commands manage by exact name.
  The name is composed as `<base> Internal` / `<base> External`, where the base
  comes from `WARDEN_ROLE_BASE_NAME` (default `Verified Warden`). Matching is
  exact: the configured base has to reproduce the Discord role name character
  for character, and one that doesn't fails every `/warden` subcommand with
  `role not found`. The bot guarantees a member holds (or no longer holds) the
  named role per the command invoked; it ascribes no meaning to what the role
  unlocks.
- **Internal / External** — the two Warden role scopes (`internal`, `external`,
  or `both`). Opaque named roles as far as the bot is concerned. _Avoid_:
  treating these as access tiers in code — the distinction lives in Discord.
- **Validated internal unit** — a position group whose current roster members
  the regiment treats as automatically belonging in the internal Warden role
  (e.g. `D/ACD`). A curated set; not every unit is one.

## External systems

- **7Cav API** (`https://api.7cav.us/api/v1/`) — bearer-auth REST API. All
  milpac/profile traffic goes through `utils.makeAPIRequest[T]`; never roll
  a fresh `resty.Client` for new endpoints.
- **Xenforo MySQL** — read-only access to the forum DB. Connection pool
  deliberately tiny (`SetMaxOpenConns(2)`) because this is a low-rate
  background scan.

## Observability

Two channels, deliberately split: **usage** is answered by the 7Cav Grafana
stack (`metrics.7cav.us`); **failures** are Sentry's alone. They never
overlap — a failure is not telemetry, and a usage metric never carries an
error signal. See ADR 0011.

- **Command telemetry** — the per-invocation usage record for a slash command:
  which command ran, how long it took, and who ran it. Lives in the Grafana /
  Prometheus / Loki stack, derived from the bot's own logs; it deliberately
  holds *no* failure signal (that is Sentry's). _Avoid_: treating it as error
  tracking, or promoting caller identity to a metric label — the caller belongs
  in the log line only, never a Prometheus label. See ADR 0011.
- **`command_invoked` line** — the single structured log line the dispatch
  wrapper emits once per slash-command invocation. Its message marker and field
  keys (`command`, `latency_ms`, `discord_id`, `username`) are a contract
  consumed off-box by the metrics host's log scraper; changing them is a
  parsing-contract change, not a cosmetic edit — the same drift hazard as
  PAF / LOA label wording. See ADR 0011 and `docs/command-telemetry.md`.
- **Sentry** — wired in 0.7.7. Captured manually at `utils.CaptureError`
  call sites — not via a blanket slog bridge. See ADR 0001. Command telemetry
  does not touch Sentry; Sentry does not touch usage metrics.
- **`utils.Error` vs `utils.HandleError`** — load-bearing distinction.
  `utils.Error` is for genuine internal failures (Sentry-eligible).
  `utils.HandleError` is for user-facing responses (often expected outcomes
  like "no troopers found", not Sentry-eligible).
