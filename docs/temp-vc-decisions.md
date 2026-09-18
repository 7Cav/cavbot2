# Temporary voice channels: decisions, and what they do to PR #232

**Last updated:** 2026-09-18
**Subject:** issue [#100](https://github.com/7Cav/cavbot2/issues/100), PR [#232](https://github.com/7Cav/cavbot2/pull/232), the MEE6 audit (#99), and the wayfinder map [#255](https://github.com/7Cav/cavbot2/issues/255).

## Purpose

PR #232 was written against a spec that turned out to be wrong. The person who configured the MEE6 hubs then answered two rounds of questions, and those answers re-specified a large part of the feature. This file records what is settled, what each answer does to the PR, and what is still open as a ticket on the map.

Do not relitigate anything under "Settled", "Settled while charting", or "Already ruled out". Each row names where it came from.

## State of play

- Both question rounds are answered. Nothing is in flight with the stakeholder.
- The maintainer builds this, not the PR's author. PR #232 is closed; the pull request for [#289](https://github.com/7Cav/cavbot2/issues/289) supersedes it.
- Every ticket under map #255 is closed as of 2026-09-15. The last was [Panel session lifetime and group re-check](https://github.com/7Cav/cavbot2/issues/280). Nothing is left to decide before the spec is written.
- PR #232 was a draft, 3 files, +3677 lines, last pushed 2026-07-26. It forked 20 commits behind `develop`; its pruned code reached `develop` through #299, and the #289 pull request reshapes it onto the store.

## Sources

- Round 1 questions went out on 2026-08-06 and came back on 2026-08-08. Round 2 went out on 2026-08-08 and came back on 2026-08-21. Both were forum conversations with Nex.
- The #99 audit ran on 2026-08-06 and found 14 live hubs. [#262](https://github.com/7Cav/cavbot2/issues/262) counted 17 on 2026-09-15; three were added in six weeks.
- A full code review of PR #232 at `0edfbb1` exists as a private artifact the maintainer holds. Its findings still describe that code, but much of that code is slated for deletion. Check the "Dies" table before acting on any finding.

## Who is who

- **Nex** configured the MEE6 hubs and answered both rounds. Sole respondent and the decider. Thinks in areas and hubs, not code.
- **HWqs** wrote PR #232.
- **Genstaff, S6 HQ, Regimental Technical Aides** are the forum groups that may open the panel.

## Settled

Each row is closed. R1 and R2 are the two question rounds.

| Decision | Provenance |
|---|---|
| The bot holds channel permissions. Members get no Manage Channels overwrite. They may rename. They may not lock a channel or change its user limit. | R1 Q1, Q2: "Bot commands are ideal imo. The less access members have to settings the better." |
| Channel names are area plus number, not person-based. Default form `Arma Voice - 1`. No rank, no username, no nickname. | R1 preamble: "the default will be 'Arma Voice - 1' for example" |
| Numbering is per hub and concurrent, not per user. The second live channel from a hub is `- 2`. A freed lower number is reused. | Follows from the naming form |
| Channels are deleted the instant the last person leaves. No grace period. Keep Alive 0 was deliberate. Immediate deletion also clears the channel's text chat, which matters for courses and private meetings. | R1 Q6: "When the last person leaves the channel should die" |
| Losing a channel to a Discord disconnect is accepted. It only bites if everyone in it drops at once. | Maintainer, 2026-08-08 |
| Ownership is real: only the owner may rename the channel, and the owner must be a Cav member. | R2 Q2: "only the owner can rename the channel, but they have to be at least a cav member to use the /voice-rename command as the owner" |
| The Cav-member half of the gate is Discord-side, through server-settings command permissions like every other cavbot command. The owner half is an in-bot check, since Discord cannot express "is the owner of the channel you are sitting in". | R2 Q2 plus mechanism |
| Rename targets the channel the invoker is sitting in. No channel argument. | Maintainer, 2026-08-08 |
| Succession is by rank role alone when the creator leaves. No status or position tier ahead of it. | R1 Q9, plus maintainer: "rank alone always" |
| Ownership carries no Discord permission. It is a bot-internal marker that gates one command. | Follows from bot-held permissions plus rename being the only owner capability |
| `/voice-clean` is not built. Its only real use was MEE6 desync, used once. Oddities get removed by hand. | R2 Q1: "probably not needed, we can manually remove weird one off channels as needed" |
| `rename` is the entire command surface. | R1 Q7 plus R2 Q1 |
| Moderator roles differ per hub and must be configurable. MP plus the relevant HQ. The variation in MEE6 today was testing noise, but per-hub configurability is wanted. | R1 Q3, Q4, plus maintainer |
| Permission source is a per-hub toggle with two values: inherit the category, or inherit the hub channel's own overwrites. Category is the common case. The hub-channel option exists for a stricter hub that shares a category with looser ones, such as the secure warden hubs. | R2 Q3 |
| Config is live-editable, not a config file or env. Reached through forum sign-in. | R1 Q10, Q11: "somewhere live we can adjust as new companies come up, and or go dark. New Games, etc." |
| If it can be configured in the panel, make it configurable. Apply this to anything not otherwise settled. | Maintainer, 2026-08-08 |
| The panel can rename the hub channel itself. MEE6 makes you create the channel, rename it, refresh, then configure it. Nex also wants hub channels called `X Hub Channel` at rollout; the panel field is how he gets there. | R2 Q5, R1 preamble |
| We transcribe the hubs at cutover, not Nex. 17 at the last count (#262). Current MEE6 state is the intended state. | R2 Q4: "I should have everything updated to what it should be at for the moment" |
| Rename abuse is policed by the Code of Conduct, not the bot. No name filter beyond Discord's own limits. | R1 preamble: "We can police this using the CoC" |
| There are many live hubs, not one: 14 at the audit, 17 by #262, and the count grows. All stay. | #99 audit, 2026-08-06; #262, 2026-09-15 |

## Settled while charting (2026-09-15)

Decisions the maintainer made while charting map #255. These are ours, not Nex's.

| Decision | Reason |
|---|---|
| The panel runs inside the cavbot2 binary as an HTTP server on a new port. | The panel must rename hub channels, and the bot already holds the Discord session. One process, one deploy, one writer. |
| Pages are Go `html/template`, assets in `embed`, plain JS where needed. No Node in the Docker build. | The hub page is a list, a form, and a rename button. |
| Bot operations and validation live in a service layer that the HTML handlers call. | A later move to a JS frontend replaces handlers only. |
| Sign-in is Xenforo 2.3 OAuth2. The forum's custom `user:groups` scope makes `/api/me` return the user's groups. | The forum already runs OAuth for other programs. |
| Panel access is an env var of forum group IDs for Genstaff, S6 HQ, and Regimental Technical Aides. | Follows the `LOA_NODE_IDS` pattern and cannot lock the maintainer out. |
| The store is Postgres in its own container in cavbot2's compose file, with its own volume. | Later tools, analytics first, will use the same database. Keeps the bot's writes off the forum's server. |
| Host is `cavbot2.7cav.us`, through the Cloudflare tunnel and Nginx Proxy Manager. | The server is reachable only through the tunnel. |
| A new branch from `pr-232` rebased onto `develop`, pruned, then several PRs to `develop`. The feature is inert with no hubs and no panel address. One GitHub Release at cutover. | Keeps HWqs's authorship, keeps the engine, and makes early merges safe. |
| Nothing goes to production before cutover. Testing runs on the maintainer's test guild. | "Ship" means a Release, and none happens until the feature is complete. |
| Analytics and later panel pages are out of scope. The panel's sign-in, group check, and layout are shared with them. | The map's destination is the temp VC spec. |

## Already ruled out. Do not re-raise.

- Multi-hub as scope creep. The #100 triage called it YAGNI and a later review repeated that. Both were wrong. Production ran 14 at the audit and 17 by #262. HWqs building a hub table was correct.
- A MEE6 export. Settings move by hand. R2 Q4 settles who does it: we do.
- `/voice-clean`. Dropped outright (R2 Q1). The `GUILD_CREATE` sweep already covers the bot-was-down case, which was the command's only real use.
- `transfer`. Dropped by R1 Q7 ("Ideally would just be clean and rename"), and `clean` has since gone too.
- A per-user channel cap. Not requested. Immediate deletion means the worst abuse is recreating the same channel repeatedly. If kept, it becomes a configurable field, not a hardcoded 4. Dropped by #266: not a hub field.
- Per-hub user limit and bitrate as requirements. The 14 audited hubs sat at MEE6 defaults (unlimited, 64k); the three added since are unchecked and get transcribed as found. Configurable if cheap; nothing depends on them. Both are hub fields by #266.
- The status and position ladder (11 hardcoded role IDs, general staff to discharged). HWqs's addition, never asked for, superseded by "rank alone always". This kills only the tier, not the election.
- Storing config in code. The hardcoded hub table is dead; R1 Q11 requires live editing.
- Renaming the hub channels as a migration step. The panel field that lets Nex rename one is in scope; the bulk rename is his to do through it.
- MEE6's ignored roles, the roles its `/voice-*` commands skip. With rename as the only command there is nothing to be exempt from. Not carried over (#265).
- The Discord audit-log channel. Nobody asked for it, its hardcoded target is not in the live guild, and Loki plus Discord's own audit log carry the record. Dropped by #275.

## What this does to PR #232

### Survives

- The lifecycle engine. Hub-join spawn, occupancy tracking by diffing `VOICE_STATE_UPDATE` into leave plus join, the `GUILD_CREATE` restart sweep (reshaped by #275 to read rows instead of categories), and the `TempVCManager` seam. This is the hard part and none of the answers touch it.
- The hub table as a struct. Its fields change completely, but per-hub settings were the right shape. The values now come from the store, not code.
- `nextChannelIndexLocked`'s smallest-unused-integer logic. Correct algorithm, wrong key. Retarget from owner to hub.
- The election subsystem: `electInterimControllerLocked`, `metaForLocked`, `memberMeta`, `rememberMemberLocked`, `lowestRoleIndex`, `tempVCRankRoles`, and the shape of `reconcileControllerLocked`. R2 Q2 gives ownership a capability, so the handover now moves something visible.
- `outranksForInterim`, minus its `statusIdx` comparison.

### Dies

| What | Why |
|---|---|
| `tempVCOwnerPerms` and the spawn-time `PermissionOverwrites` block | Bot holds permissions (R1 Q2) |
| `controllerOp`, `applyControllerOps`, and the `ChannelPermissionSet` and `ChannelPermissionDelete` seam methods | Ownership grants no Discord permission. `reconcileControllerLocked` keeps its bookkeeping and stops returning ops |
| `splitNick`, `stripLeadingRank`, `resolveRank`, `enforceNameCasing`, `upperFirstLetter`, `rankAbbrevFromRoles`, `composeChannelName`, `canonicalRanks`, `isDroppableNameToken`, `displayNameFromMember`, `tempVCNameSuffix`, plus the nickname table in the PR body and a large test block | Names carry no person |
| `suffixNumber`, `renameForSecondChannel`, `demoteSoleChannelToUnnumbered` | Every channel is numbered from 1. No unsuffixed-first case exists |
| `graceTimers`, `channelGrace`, per-hub `Grace`, `defaultTempVCGrace`, the grace path in `deleteIfStillEmpty` | Immediate delete (R1 Q6) |
| `/voice block`, `/voice permit`, `tempVCBlockDeny`, `voiceSubcommands` | `rename` is the only command |
| Most of `handleChannelUpdate`: the lock, hide, and user-limit auditing | Members can no longer perform those edits |
| `tempVCStatusRoles`, `statusRoleIndex`, `noStatusTier`, the `statusIdx` half of `memberRankMeta`, and the `statusIdx` branch of `outranksForInterim` | Rank alone |
| `tempVCLogChannelID`, `LogChannelID`, `logEvent` and every call site, `channelLabel`, `actorSuffix`, `deleteLogLine` | The audit-log channel is dropped (#275). Create, delete and rename are structured log lines |
| `handleChannelUpdate` | A rename is recorded at the `/voice-rename` handler, attributed to the invoker (#275) |
| The `categories` map and the category walk in `handleGuildCreate` | The sweep reads rows and touches no channel without one (#275). One category can hold two hubs, as Foxhole does |

Deleting the owner overwrite also retires the review's most dangerous unverified lead: that Discord may reject `MANAGE_ROLES` in a channel overwrite without Administrator (error `50013`), which would have broken creation for everyone. No overwrite, no exposure.

### New work

1. Per-hub config in Postgres with the panel as its editing surface. Fields so far: hub channel, hub channel name (writable), category, spawned-name base string, permission source (category or hub channel), moderator roles. The v1 list is settled in [#266](https://github.com/7Cav/cavbot2/issues/266).
2. Permission source. When set to category, create with no explicit overwrites so the channel inherits its parent. When set to hub channel, copy the hub channel's own overwrite list. The PR always passes an explicit overwrite list, so today nothing ever inherits.
3. Moderator roles, a cross-channel authority concept the PR lacks. Settled in [#265](https://github.com/7Cav/cavbot2/issues/265).
4. `/voice-rename`: Discord-gated to Cav members, plus an in-bot owner check, targeting the invoker's current channel. A single command with a single string option. `ApplicationCommandOptionSubCommand` is no longer needed; that recommendation in the 2026-08-06 PR comment is superseded.
5. Per-hub sequential naming from the configured base string.
6. Hub-channel rename from the panel (R2 Q5): a `ChannelEdit` against a channel the bot does not own. A new seam method. No new permission requirement: the bot keeps Administrator (#268).
7. The panel itself: sign-in, group check, layout, the hub page, and the service layer under it. Sign-in, the panel session and the group check are settled in [#280](https://github.com/7Cav/cavbot2/issues/280).
8. Spawn failure handling: the hub chat message, the per-hub last failure in memory, and the broken hub state on the hub list. Settled in [#278](https://github.com/7Cav/cavbot2/issues/278).

## Settled on the map

Decisions made by working map #255's tickets. Each row links the ticket that holds the detail.

| Decision | Provenance |
|---|---|
| The owner always holds a rank role, or the channel has no owner. The creator is owner at create if they hold one. When the owner leaves, the highest-ranked occupant with a rank role takes over, ties to the lowest user ID, and no candidate means no owner. A rank-role holder who joins a channel with no owner takes over. A handover is final. A returning creator is an ordinary occupant. | [#263](https://github.com/7Cav/cavbot2/issues/263) |
| The store holds one row per spawned channel: channel ID, hub, number, owner. Written at create and at every handover, deleted with the channel. The restart sweep restores owner and number from the row. A failed write keeps the channel and captures the failure in Sentry. The channel is tracked in memory only; the #275 rows say what a restart does with it. | [#263](https://github.com/7Cav/cavbot2/issues/263), amended by [#275](https://github.com/7Cav/cavbot2/issues/275) |
| Superseded by #275. The sweep touches no channel without a row, so nothing is adopted. MEE6 channels live at cutover are removed by hand. | [#263](https://github.com/7Cav/cavbot2/issues/263), superseded by [#275](https://github.com/7Cav/cavbot2/issues/275) |
| `/voice-rename` replies are ephemeral. A non-owner is told who the owner is. A channel with no owner cannot be renamed, and that reply is logged at WARN because it means the Discord gate or the rank-role assumption failed. Discord allows two renames per channel per 10 minutes; the bot counts them and refuses the third with the wait time, and never lets discordgo sleep through a 429. | [#264](https://github.com/7Cav/cavbot2/issues/264) |
| An ownership notice is posted in the spawned channel's chat at create and at every handover. It names the owner or says there is none, and pings nobody. The voice channel status line is not used. | [#264](https://github.com/7Cav/cavbot2/issues/264) |
| At the restart sweep, a stored owner who is no longer in the channel counts as having left. The sweep elects by the handover rule. | [#264](https://github.com/7Cav/cavbot2/issues/264) |
| The hub form's v1 fields: hub channel (set at create or register, fixed after), hub channel name (writable, renames the channel on save), category (read from the hub channel's parent, shown read-only, and a hub channel with no parent is refused), spawned-name base string (required, 1 to 90 characters), permission source (`category` or `hub_channel`, default `category`), moderator roles (zero or more of the guild's roles, as role IDs), user limit (0 to 99, default 0), bitrate (default 64000, bounded by the guild's boost tier at save), enabled (default on). No per-user cap. | [#266](https://github.com/7Cav/cavbot2/issues/266) |
| A disabled hub keeps its channel and its settings and ignores joins. Its spawned channels stay tracked and are restored at restart from their rows like any other. Cutover order: register every hub disabled while MEE6 still runs, switch the MEE6 plugin off, then enable the hubs. #275 adds two hand steps around the last one. | [#266](https://github.com/7Cav/cavbot2/issues/266), cutover steps added by [#275](https://github.com/7Cav/cavbot2/issues/275) |
| The panel creates a hub channel from a category and a name, synced to the category, or registers an existing voice channel as a hub. Remove deletes the row and leaves the Discord channel. Spawned channels of a removed hub keep their rows and die when empty. A changed hub channel name renames the channel before the row saves; a refused rename saves nothing and the form shows why. | [#266](https://github.com/7Cav/cavbot2/issues/266) |
| An append-only change log per hub records every panel save: forum user ID and username, time, action, and a JSON diff of the changed fields. The hub page shows the last ten, newest first. The hub list shows each hub's live spawned channel count from the bot's memory. | [#266](https://github.com/7Cav/cavbot2/issues/266) |
| The rank ladder stays in code as abbreviation plus Discord role ID. At startup the bot fetches `/api/v1/milpacs/ranks` and captures any drift in abbreviations or order to Sentry, not a WARN log. The endpoint returns 30 entries: the 29 ranks in the code ladder, same order, plus `Tester` (`rankId` 32, display order 1), which the check excludes (verified 2026-09-15). Election by API rank per occupant was rejected: a network call in every create and handover, and it breaks the rank-role rule. | [#266](https://github.com/7Cav/cavbot2/issues/266) |
| The bot keeps Administrator. The spec names it as the deployment requirement, and the hub form checks no permissions. Dropping it is a separate, guild-wide effort: `/warden` role recreation and the ownership notice depend on it as much as spawning does. The numbers for that effort are on the ticket. | [#268](https://github.com/7Cav/cavbot2/issues/268) |
| At startup the bot fetches its own roles and captures to Sentry when Administrator is missing. Same shape as the rank ladder check. | [#268](https://github.com/7Cav/cavbot2/issues/268) |
| The bot writes at most six bits into any channel overwrite: Manage Channels, Move Members, Mute Members, Deafen Members, Connect, View Channel. The list is a constant in code, never a panel setting. What a moderator role gets is chosen from inside it. | [#268](https://github.com/7Cav/cavbot2/issues/268) |
| The test guild bot `bootybot` mirrors production: Administrator on. | [#268](https://github.com/7Cav/cavbot2/issues/268) |
| A moderator role is a command bypass, as MEE6 defines the field. It passes the owner check on `/voice-rename` for every spawned channel it covers. The bot writes no overwrite for it, so it authors no overwrite of its own at all. The six-bit ceiling from #268 stands and bounds nothing yet. Discord-side moderation stays where it is today, on the categories, and permission source carries it into every spawned channel. | [#265](https://github.com/7Cav/cavbot2/issues/265) |
| Moderator roles are set per hub and once for every hub. A hub's effective set is the union of the two, with no per-hub exclusion. The guild-wide list is a section at the top of the hub page with its own save, change-logged in the same shape as a hub save. Each hub form shows the guild-wide roles read-only, labelled "on every hub", above the hub's own multi-select. | [#265](https://github.com/7Cav/cavbot2/issues/265) |
| A moderator renames the channel they are sitting in, like everyone else. No channel argument. The moderator check runs before the owner check and never reads the owner, so a moderator can rename an ownerless channel and no WARN fires on that path. The rename leaves the owner and the ownership notice untouched. The not-the-owner reply from #264 is unchanged. The Cav-member Discord gate and the two-renames-per-ten-minutes limit apply to moderators as to anyone. | [#265](https://github.com/7Cav/cavbot2/issues/265) |
| The restart sweep reads every stored row and touches no channel without one. A row whose channel is gone is deleted. A channel that is empty is deleted with its row. An occupied channel is tracked again with its owner restored; an absent owner counts as having left (#264). No name parsing and no category map. The bot never deletes a channel it did not create. Only rows hold numbers, which closes the numbering item from the map's fog. `Adoption` leaves `CONTEXT.md`; `Restart sweep` replaces it. | [#275](https://github.com/7Cav/cavbot2/issues/275) |
| A failed row write at spawn keeps the channel tracked in memory. If the bot restarts while that channel lives, it lingers until removed by hand, and its number may be reused meanwhile; Sentry already reported the failed write. The handover write is an upsert, so the channel heals at its first handover. A `CHANNEL_DELETE` handler deletes the row and frees the number. | [#275](https://github.com/7Cav/cavbot2/issues/275) |
| Cutover gains two hand steps for the maintainer, who transcribes the hubs. Before enabling the hubs, delete every empty channel with a `#` name in a hub category; MEE6 desync leftovers exist on the live guild today. After enabling, list the occupied MEE6 channels and delete each by hand once it empties, the same evening. Nobody is dropped from voice. | [#275](https://github.com/7Cav/cavbot2/issues/275) |
| The Discord audit-log channel is dropped, with no panel field in its place. Create and delete already reach Loki as structured log lines; rename joins them. Join and leave get no record. Staff who need to know who renamed what read Discord's own audit log. | [#275](https://github.com/7Cav/cavbot2/issues/275) |
| A rename is recorded at the `/voice-rename` handler: one structured log line with `command`, `discord_id`, `username`, `channel_id`, `before` and `after`, and `X-Audit-Log-Reason` naming the invoker on the edit call. Create and delete carry the same header, the hub and the creator on create, "empty" on delete. The `CHANNEL_UPDATE` handler goes. A rename made in Discord's UI is Discord's audit log's record, not the bot's. | [#275](https://github.com/7Cav/cavbot2/issues/275) |
| The Cav-member gate on `/voice-rename` is a Server Settings restriction naming the 29 rank roles in the code ladder. "Cav member" and "holds a rank role" are one set, as #263 assumed. The maintainer applies the restriction at deploy, before the hubs are enabled; until then the command is visible to every member and a non-member in a spawned channel reaches the no-owner WARN from #264. Discord allows 100 entries per command per guild. No API check per member. | [#279](https://github.com/7Cav/cavbot2/issues/279) |
| When a create fails, the member gets one message in the hub channel's text chat that mentions them, with `AllowedMentions` limited to that one user. The bot never deletes it. No DM, no disconnect. Two texts, copy spec-level: the category or guild cap says the area is full and to wait for a channel to empty; anything else says the channel could not be created and this has been reported. A create that succeeds and a move-into that fails is the same event: the bot deletes the new channel, and the message goes out only when the member is still in the hub. | [#278](https://github.com/7Cav/cavbot2/issues/278) |
| A failed create writes nothing to the store. The bot keeps the last spawn failure per hub in memory, time and cause, and the hub list shows it beside the live spawned count. A successful spawn from that hub or a restart clears it. | [#278](https://github.com/7Cav/cavbot2/issues/278) |
| The category is never stored. The bot reads the hub channel's parent from discordgo's state cache at each spawn, and the panel reads it live at page load, so moving the hub channel in Discord moves spawning with it. A hub whose channel is gone or has no parent is a broken hub. The row stays; the panel derives the state from the guild's channel list at page load and offers Remove only. A join to a no-category hub spawns nothing, makes no API call, and counts as a create failure with cause "no category". The `CHANNEL_DELETE` handler stays spawned-only. Spawned channels of a broken hub keep their rows and die when empty. | [#278](https://github.com/7Cav/cavbot2/issues/278) |
| One create per join. No retry within the join, no later retry, no backoff, no auto-disable. Every failure is one WARN line. The create and move-into calls pass `WithRetryOnRatelimit(false)`, as the rename call does, so a `429` is a failure and never a sleeping handler. The invalid request limit is 10,000 per 10 minutes and a join makes at most three, so a ban needs 55 joins per second on broken hubs. | [#278](https://github.com/7Cav/cavbot2/issues/278) |
| Create failures Discord returns capture to Sentry: the cap, `403`, `429`, `5xx`, transport. Once per streak per hub: the first failure captures, and the next capture waits for a successful spawn from that hub. A refusal the bot makes itself, the no-category case, is a WARN line and the hub list, never a Sentry event. Delete classification is spec-level in [#285](https://github.com/7Cav/cavbot2/issues/285) and landed with [#290](https://github.com/7Cav/cavbot2/issues/290): Unknown Channel (`10003`) means the channel is already gone, so the bot untracks it and deletes the row with no capture; a `429` is a WARN line only; `403`, `5xx` and transport capture once per streak per hub and the channel stays tracked. `commands/temp_vc_errors.go` classifies the spawned channel paths; the warden classifier is untouched. The category cap has no error code of its own: it is a `400` `50035` whose body carries `CHANNEL_PARENT_MAX_CHANNELS`, a shape taken from Discord's docs and not yet seen from a live refusal. Rename classification lands with `/voice-rename`. | [#278](https://github.com/7Cav/cavbot2/issues/278) |
| A panel session lasts as long as its access token: 2 hours from sign-in, then the user signs in again through the forum. No refresh token is stored. The session and the pending sign-in (`state`, PKCE verifier, start time, dropped after 5 minutes) live in memory, keyed by random 128-bit IDs, pruned on a timer. A restart ends every session. No table, no signing key, no env var. | [#280](https://github.com/7Cav/cavbot2/issues/280) |
| The group check runs on every request: one `GET /api/me` with the session's access token. It passes when `user_group_id` or `secondary_group_ids` holds an allowlisted group. A 401 ends the session and the sign-in page says the session ended. A 200 with no allowlisted group, or a 403, ends the session and the sign-in page says the account is not in a group that may use the panel. A transport error or 5xx keeps the session and shows an error page asking to try again. No read-only mode. | [#280](https://github.com/7Cav/cavbot2/issues/280) |
| Two cookies, `__Host-panel_session` and `__Host-panel_signin`: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, no `Domain`, no `Max-Age`, each holding an opaque key only. `Secure` stays on for local runs. Deletion repeats the same attributes with `Max-Age=0`. The panel reads no proxy header; every absolute URL comes from `PANEL_BASE_URL`, and no client IP is logged. `net/http.CrossOriginProtection` wraps the mux, no form tokens. Every state change is a POST; the OAuth callback is the one GET that creates state, defended by `state`. | [#280](https://github.com/7Cav/cavbot2/issues/280) |
| Sign-out is a form button on every page. It ends the panel session and nothing else: no call to the forum's revoke endpoint, so `PANEL_OAUTH_REVOKE_URL` from #261 goes unused and the spec drops it. | [#280](https://github.com/7Cav/cavbot2/issues/280) |

## Open, as tickets on map #255

None. Every ticket is closed as of 2026-09-15.

Backing up the Postgres volume is out of scope for the map and tracked as [#281](https://github.com/7Cav/cavbot2/issues/281).

## Fixes that hold regardless of every answer above

From the 2026-08-06 PR comment. All still apply to whatever survives.

- `temp_vc.go:1125` returns silently when channel creation fails. The member sits in the hub with no signal. #278 names the replacement: one message in the hub chat, a per-hub last failure in memory, and one Sentry capture per streak.
- `dg.SyncEvents` is never set, so discordgo dispatches handlers on unordered goroutines while `handleVoiceStateUpdate` is a sequential diff. Separately, `ownedCount` is read under the lock at `:650`, the lock drops at `:662`, and creation fires at `:1107`, so two fast joins can both pass the cap check. Unreproduced against a live bot. Moot if the cap is dropped, but the ordering hazard is not.
- `logEvent:698` sends raw content with no `AllowedMentions`. Nothing in the repo sets it anywhere. `logEvent` goes with #275, but the ownership notice (#264) names the owner and must ping nobody, so the fix moves there.
- `truncateChannelName:1394` and `nameWithIndex:1405` slice by byte while their comments say character. Largely moot once names stop carrying nicknames.
- No `CHANNEL_DELETE` handler exists, and `classifyDiscordError` (`commands/warden_errors.go:62`) is never called from this file. Read the classifier before wiring it. Its status-class split is generic, but `classifyNotFound` beneath it was written for a role-add and routes every 404 that is not Unknown Member into `SystemFault` plus `ConfigFault`, which pages on-call. A `ChannelDelete` 404 carries Unknown Channel (`10003`) and would land there. Wanted: Unknown Channel untracks quietly; a 403 keeps the channel tracked. With rows (#263, #275) the handler also deletes the row and frees the number.

## Repo conventions this PR trips

- ADR 0006: `NewRegistry()` is the only place commands are declared. `/voice` is registered from `main.go` instead. The fix has precedent: a package-level singleton plus a `Voice()` entry in the registry, the way `LOA()` and `Awol()` read `utils.GlobalLOACache` while `initLOACache()` runs separately in `main.go`. Gateway handlers must stay in `main.go` before `Open()`; the command declaration does not.
- ADR 0001: manual Sentry capture, kept signal-rich. The PR has 14 unclassified capture sites.
- `CONTEXT.md`: new domain terms belong there. The "Temporary voice channels" section now exists.
- `CLAUDE.md`: a test-guild smoke test is a mandatory manual gate for user-facing command changes.
- Land strategy is `pr` against `develop`. A human merges.
- Testing standard: a test must fail exactly when the behaviour is wrong, and assert only caller-observable things. Several tests in the PR are vacuous; the review flags them.
