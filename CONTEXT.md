# CONTEXT.md — CavBot2

Domain language used throughout the codebase. New terms that need explanation
belong here, not inline in code comments.

## Organization

- **7Cav / 7th Cavalry Gaming Regiment** — the gaming community this bot
  serves. Project lives at `github.com/7Cav/cavbot2`. Member-facing reference
  for org structure and terminology is the [7Cav wiki](https://wiki.7cav.us/).
- **Cav member**: A Discord member who holds a rank role, one of the 29 roles
  on the rank ladder. The two descriptions name one set: every Cav member
  holds a rank role, and every rank-role holder is a Cav member. Member-facing
  copy says "Cav member". The handover rule and the code say "rank role", the
  test the bot applies.
  _Avoid_: rank holder (in copy), trooper (in copy), verified member, Cav.
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

## Temporary voice channels

The bot replaces MEE6's "Temporary Channels" plugin. A member joins a hub, the
bot creates a spawned channel for them, and the spawned channel is deleted the
moment it empties. Hub settings are edited in the panel, never in code.

- **Hub**: A voice channel that, when a member joins it, causes the bot to
  create a spawned channel and move the member into it. Each hub carries its
  own settings. MEE6 calls this "join to create".
  _Avoid_: join-to-create channel, creation trigger, hub voice channel.
- **Spawned channel**: The voice channel a hub creates for the member who
  joined it. Named from the hub's base string plus a per-hub number.
  _Avoid_: temp channel, temp VC, temporary channel, personal channel.
- **Owner**: The occupant a spawned channel belongs to, or nobody. Owning
  is what lets a member rename or lock it, as far as its hub allows each; a
  moderator role does both without owning.
  The owner always holds a rank role. The creator at first, if they hold one;
  after a handover, whoever the handover named. A bot-internal marker that
  grants no Discord permission.
  _Avoid_: interim controller, controller, creator (for the current owner).
- **Handover**: The bot giving ownership of a spawned channel to an occupant.
  When the owner leaves, the highest-ranked occupant with a rank role takes
  over, ties broken by lowest user ID; with no such occupant the channel has
  no owner. When a rank-role holder joins a channel with no owner, they take
  over. A handover is final: a returning creator is an ordinary occupant.
  _Avoid_: hand off, hand back, succession, transfer, loan.
- **Restart sweep**: The bot's check, when it connects, of every spawned
  channel it holds a stored record of against the guild. A recorded channel
  that is gone or empty is deleted with its record. An occupied one is
  tracked again, its owner restored or elected by the handover rule. A
  channel with no record is never touched. It reads one copied snapshot of
  Discord's current cached guild state when it takes `t.mu`, not the state
  in the `GUILD_CREATE` payload.
  _Avoid_: adoption, orphan sweep, recovery, reap, resync.
- **Spawn in flight**: A spawned channel from the moment Discord confirms
  its create until its row write or compensating delete finishes. A restart
  sweep that overlaps this interval leaves the channel alone until that
  sweep also finishes.
  _Avoid_: pending spawn, unsettled channel, protected channel, marked
  channel.
- **Stale voice state**: The bot's record of which channel a member is in,
  or of who is in a spawned channel, at a moment when Discord has already
  reported a change the bot has not yet applied. Discord reports changes in
  order; the bot applies them in no fixed order, so the record can lag.
  Before it creates, moves or deletes, the bot checks Discord's current
  state and goes ahead only if the record it decided on still holds at that
  check.
  _Avoid_: stale event, out-of-order event, late event, race.
- **Ownership notice**: The bot message in a spawned channel's text chat that
  names the current owner, or says there is none. Posted when the channel is
  created and at every handover. It pings nobody.
  _Avoid_: announcement, banner, status line, welcome message.
- **Moderator role**: A Discord role that may rename, lock and unlock any
  spawned channel it covers without owning it, as far as the hub allows
  each, and that no lock keeps out.
  Set per hub, or once for every hub; a hub's moderator roles are the union
  of the two. Grants no Discord permission beyond getting past a lock.
  _Avoid_: staff role, admin role, global moderator, default moderator.
- **Lock**: A spawned channel's state in which only its guests and its hub's
  moderator roles may join. Everyone else still sees the channel, with
  Discord's padlock, and cannot read its text chat. The owner or a moderator
  locks it, on a hub that allows locking. It belongs to the channel, not to
  whoever set it, and lasts until someone unlocks it or the channel is
  deleted.
  _Avoid_: private channel, closed channel, hide (a hidden channel is out of
  sight; a locked one is not).
- **Guest list**: The members a locked channel admits: everyone who has been
  inside it since it locked, however they got in, and everyone let in. Each
  lock starts a new one; unlock clears it. A guest is one member on it.
  _Avoid_: allowlist, whitelist, permit list, invite list.
- **Let in**: To put a member who is outside a locked channel on its guest
  list. Done by a moderator or by whoever locked the channel. It never shows
  a member a channel they could not already see.
  _Avoid_: admit, invite, permit, allow.
- **Lock notice**: The bot message in a locked channel's text chat that
  names who locked it and carries the controls to unlock it and to let
  someone in. Posted at each lock. At unlock it is edited to name who
  unlocked the channel, and loses its controls.
  _Avoid_: lock panel (the panel is the web UI), control panel, tool,
  widget.
- **Knock channel**: A spawned channel whose name starts with 🚦, asking
  members outside to knock before they join. A courtesy the bot does not
  enforce: a knock channel keeps nobody out, where a lock does. The name is
  its only record.
  _Avoid_: soft lock, do not disturb, knocked channel, knock (for the channel
  or its 🚦).
- **Knock**: A member outside a knock channel asking the people inside
  whether they may join. Members do it among themselves; the bot plays no
  part.
  _Avoid_: request to join, let in (a lock's term, and done by the bot).
- **Eligible role**: A live Discord role that is not managed and is not
  `@everyone`. The only kind a moderator picker offers, and the only kind a
  save may add.
  _Avoid_: offered role, valid role, pickable role, selectable role.
- **Unavailable moderator role**: A stored moderator role that is no longer
  eligible, kept with its authority until a person removes it in the panel.
  Two reasons: deleted, when the role is gone from Discord, and managed.
  _Avoid_: legacy role, stale role, orphaned role, ghost role.
- **Permission source**: The per-hub setting that chooses what a spawned
  channel inherits its permissions from, the hub's category or the hub
  channel itself.
  _Avoid_: sync, category sync, synchronize permissions.
- **Register**: To make an existing voice channel a hub through the panel.
  The other way a hub comes to exist is the panel creating the channel itself.
  _Avoid_: adopt (for a hub), import, link, attach.
- **Disabled hub**: A hub that keeps its channel and its settings but spawns
  nothing. A join to it does nothing. Its spawned channels live on until
  empty.
  _Avoid_: paused hub, inactive hub, archived hub, hub off.
- **Broken hub**: A hub whose channel is gone from Discord or has no category.
  Discord's state makes it so, never a panel setting, and the panel reads that
  state fresh each time it shows the hub. A join can reach only the
  no-category kind, and it spawns nothing.
  _Avoid_: orphaned hub, stale hub, dead hub, unhealthy hub.
- **Spawn failure**: A join to an enabled hub that ends with no spawned
  channel for the member. Two kinds: a failure Discord returned on the
  create or the move-into, and a refusal the bot decided itself, which only
  a broken hub causes.
  _Avoid_: create failure, failed create, failed join, refused join.
- **Change log**: The panel's record of who changed which setting, when,
  and from what to what. Each hub's form shows the hub's own entries; the
  guild-wide moderator section shows the entries of its saves, which
  reference no hub. It records panel saves only.
  _Avoid_: audit trail, audit log (that is Discord's), history.
- **Rank ladder**: The rank roles in seniority order, most senior first,
  that the handover rule ranks occupants by.
  _Avoid_: rank list, rank table, seniority list, role ladder.
- **Panel**: cavbot2's web UI at `cavbot2.7cav.us`, signed in through the
  forum. Hub settings are its first page; later pages are out of this
  feature's scope.
  _Avoid_: dashboard, admin UI, settings screen, config.
- **Panel session**: The panel's record that a browser is signed in as one
  forum user. Created at the OAuth callback. Ended by sign-out, by a failed
  group check, or by the forum refusing the token.
  _Avoid_: login, token session, forum session, auth cookie.
- **Pending sign-in**: The panel's record of one sign-in between the redirect
  to the forum and the callback. Consumed by the callback, whatever its
  outcome, or dropped after five minutes.
  _Avoid_: auth request, login attempt, OAuth state, flow.
- **Group check**: The panel's test that the signed-in forum user holds an
  allowlisted forum group, primary or secondary. Runs on every request.
  _Avoid_: allowlist check, permission check, authorisation, role check.
- **Block**: One bordered unit of a panel page, with a gold header rule. The
  unit a page's layout rules bound.
  _Avoid_: card and section (for a page unit; the helpline card is a
  Discord message), widget.
- **Tag**: One selected item as a picker shows it, with its remove control.
  A role tag is a moderator picker's, and shows an unavailable moderator role
  with its reason.
  _Avoid_: chip, pill, badge.
- **Search list**: The list of candidates a picker's add control opens,
  filtered as the person types. A moderator picker's role search offers the
  eligible roles not yet selected. The register picker's channel search
  offers the voice channels that are not hubs.
  _Avoid_: dropdown, popover, menu, combobox.

## External systems

- **7Cav API** (`https://api.7cav.us/api/v1/`) — bearer-auth REST API. All
  milpac/profile traffic goes through `utils.makeAPIRequest[T]`; never roll
  a fresh `resty.Client` for new endpoints.
- **Xenforo MySQL** — read-only access to the forum DB. Connection pool
  deliberately tiny (`SetMaxOpenConns(2)`) because this is a low-rate
  background scan.
- **Bot Postgres**: The bot's own database, the `postgres` service in
  `docker-compose.yml`, reached through `BOT_DB_DSN`. Holds the hubs, which
  the panel edits, and the spawned channel rows, which the runtime writes at
  create and at every handover and deletes with the channel. The runtime loads
  both at startup. The store package (`store/`) is the only code that talks to
  it.

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
- **Abandoned page load**: A panel page load whose connection closed before
  the panel answered and before the page's time budget ran out. Usually the
  browser left, by navigating away, reloading or closing the tab. It is
  expected, not a failure, so it leaves an INFO line and no Sentry event. A
  page still loading when its budget runs out has failed, and is not an
  abandoned page load.
  _Avoid_: browser leaving, client disconnect, cancelled request, timeout.
