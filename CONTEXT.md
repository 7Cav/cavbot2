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
  duty for a date range. Parsed from a specific Xenforo BBCode template
  (yellow `[COLOR=rgb(213, 185, 0)]` labels around `Username`, `Start Date`,
  `End Date`). Template drift silently breaks parsing.
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
- **AWOL** — a trooper flagged absent without leave in their milpac. `/awol`
  lists current AWOLs for a position, with an "On LOA" indicator sourced
  from the LOA cache.
- **Accuracy disclaimer** — because milpac records are user-entered free
  text, parsing can drift. `/afsm` always renders the disclaimer, regardless
  of whether the eligibles list is empty — see ADR 0002.

## External systems

- **7Cav API** (`https://api.7cav.us/api/v1/`) — bearer-auth REST API. All
  milpac/profile traffic goes through `utils.makeAPIRequest[T]`; never roll
  a fresh `resty.Client` for new endpoints.
- **Xenforo MySQL** — read-only access to the forum DB. Connection pool
  deliberately tiny (`SetMaxOpenConns(2)`) because this is a low-rate
  background scan.
- **GitHub Apps API** — `utils.GithubAuth(clientID, pem)` mints an
  installation token. Only consumer right now is `/apps_beta_deploy`,
  dispatching a workflow on `7Cav/adr`.

## Observability

- **Sentry** — wired in 0.7.7. Captured manually at `utils.CaptureError`
  call sites — not via a blanket slog bridge. See ADR 0001.
- **`utils.Error` vs `utils.HandleError`** — load-bearing distinction.
  `utils.Error` is for genuine internal failures (Sentry-eligible).
  `utils.HandleError` is for user-facing responses (often expected outcomes
  like "no troopers found", not Sentry-eligible).
