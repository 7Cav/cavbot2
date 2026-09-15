# Temporary voice channels: decisions, and what they do to PR #232

**Last updated:** 2026-09-15
**Subject:** issue [#100](https://github.com/7Cav/cavbot2/issues/100), PR [#232](https://github.com/7Cav/cavbot2/pull/232), the MEE6 audit (#99), and the wayfinder map [#255](https://github.com/7Cav/cavbot2/issues/255).

## Purpose

PR #232 was written against a spec that turned out to be wrong. The person who configured the MEE6 hubs then answered two rounds of questions, and those answers re-specified a large part of the feature. This file records what is settled, what each answer does to the PR, and what is still open as a ticket on the map.

Do not relitigate anything under "Settled", "Settled while charting", or "Already ruled out". Each row names where it came from.

## State of play

- Both question rounds are answered. Nothing is in flight with the stakeholder.
- The maintainer builds this, not the PR's author. PR #232 stays open as the base until a superseding PR exists.
- The remaining decisions are tickets under map #255. Nothing else blocks the spec.
- PR #232 is a draft, 3 files, +3677 lines, last pushed 2026-07-26. It forked 20 commits behind `develop`. The `main.go` anchors it patches moved (`NewRegistry()` is at line 126, `StartJoinerReportScheduler` at 193), and #245 added gofmt enforcement to CI.

## Sources

- Round 1 questions went out on 2026-08-06 and came back on 2026-08-08. Round 2 went out on 2026-08-08 and came back on 2026-08-21. Both were forum conversations with Nex.
- The #99 audit ran on 2026-08-06 and found 14 live hubs.
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
| We transcribe the 14 hubs at cutover, not Nex. Current MEE6 state is the intended state. | R2 Q4: "I should have everything updated to what it should be at for the moment" |
| Rename abuse is policed by the Code of Conduct, not the bot. No name filter beyond Discord's own limits. | R1 preamble: "We can police this using the CoC" |
| There are 14 live hubs, not one. All 14 stay. | #99 audit, 2026-08-06 |

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

- Multi-hub as scope creep. The #100 triage called it YAGNI and a later review repeated that. Both were wrong. Production runs 14. HWqs building a hub table was correct.
- A MEE6 export. Settings move by hand. R2 Q4 settles who does it: we do.
- `/voice-clean`. Dropped outright (R2 Q1). The `GUILD_CREATE` sweep already covers the bot-was-down case, which was the command's only real use.
- `transfer`. Dropped by R1 Q7 ("Ideally would just be clean and rename"), and `clean` has since gone too.
- A per-user channel cap. Not requested. Immediate deletion means the worst abuse is recreating the same channel repeatedly. If kept, it becomes a configurable field, not a hardcoded 4.
- Per-hub user limit and bitrate as requirements. All 14 hubs sit at MEE6 defaults (unlimited, 64k). Configurable if cheap; nothing depends on them.
- The status and position ladder (11 hardcoded role IDs, general staff to discharged). HWqs's addition, never asked for, superseded by "rank alone always". This kills only the tier, not the election.
- Storing config in code. The hardcoded hub table is dead; R1 Q11 requires live editing.
- Renaming the 14 hub channels as a migration step. The panel field that lets Nex rename one is in scope; the bulk rename is his to do through it.

## What this does to PR #232

### Survives

- The lifecycle engine. Hub-join spawn, occupancy tracking by diffing `VOICE_STATE_UPDATE` into leave plus join, the `GUILD_CREATE` restart sweep with adopt-or-reap, and the `TempVCManager` seam. This is the hard part and none of the answers touch it.
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

Deleting the owner overwrite also retires the review's most dangerous unverified lead: that Discord may reject `MANAGE_ROLES` in a channel overwrite without Administrator (error `50013`), which would have broken creation for everyone. No overwrite, no exposure.

### New work

1. Per-hub config in Postgres with the panel as its editing surface. Fields so far: hub channel, hub channel name (writable), category, spawned-name base string, permission source (category or hub channel), moderator roles, and whatever the hub form ticket adds.
2. Permission source. When set to category, create with no explicit overwrites so the channel inherits its parent. When set to hub channel, copy the hub channel's own overwrite list. The PR always passes an explicit overwrite list, so today nothing ever inherits.
3. Moderator roles, a cross-channel authority concept the PR lacks.
4. `/voice-rename`: Discord-gated to Cav members, plus an in-bot owner check, targeting the invoker's current channel. A single command with a single string option. `ApplicationCommandOptionSubCommand` is no longer needed; that recommendation in the 2026-08-06 PR comment is superseded.
5. Per-hub sequential naming from the configured base string.
6. Hub-channel rename from the panel (R2 Q5): a `ChannelEdit` against a channel the bot does not own. A new seam method and a new bot permission requirement.
7. The panel itself: sign-in, group check, layout, the hub page, and the service layer under it.

## Open, as tickets on map #255

- [Who owns a spawned channel after adoption or the creator's return](https://github.com/7Cav/cavbot2/issues/263)
- [What a member sees of ownership](https://github.com/7Cav/cavbot2/issues/264)
- [What authority a moderator role carries](https://github.com/7Cav/cavbot2/issues/265)
- [The hub form: v1 fields, hub creation, and an audit trail](https://github.com/7Cav/cavbot2/issues/266)

## Fixes that hold regardless of every answer above

From the 2026-08-06 PR comment. All still apply to whatever survives.

- `temp_vc.go:1125` returns silently when channel creation fails. The member sits in the hub with no signal.
- `dg.SyncEvents` is never set, so discordgo dispatches handlers on unordered goroutines while `handleVoiceStateUpdate` is a sequential diff. Separately, `ownedCount` is read under the lock at `:650`, the lock drops at `:662`, and creation fires at `:1107`, so two fast joins can both pass the cap check. Unreproduced against a live bot. Moot if the cap is dropped, but the ordering hazard is not.
- `logEvent:698` sends raw content with no `AllowedMentions`. Nothing in the repo sets it anywhere.
- `truncateChannelName:1394` and `nameWithIndex:1405` slice by byte while their comments say character. Largely moot once names stop carrying nicknames.
- No `CHANNEL_DELETE` handler exists, and `classifyDiscordError` (`commands/warden_errors.go:62`) is never called from this file. Read the classifier before wiring it. Its status-class split is generic, but `classifyNotFound` beneath it was written for a role-add and routes every 404 that is not Unknown Member into `SystemFault` plus `ConfigFault`, which pages on-call. A `ChannelDelete` 404 carries Unknown Channel (`10003`) and would land there. Wanted: Unknown Channel untracks quietly; a 403 keeps the channel tracked.

## Repo conventions this PR trips

- ADR 0006: `NewRegistry()` is the only place commands are declared. `/voice` is registered from `main.go` instead. The fix has precedent: a package-level singleton plus a `Voice()` entry in the registry, the way `LOA()` and `Awol()` read `utils.GlobalLOACache` while `initLOACache()` runs separately in `main.go`. Gateway handlers must stay in `main.go` before `Open()`; the command declaration does not.
- ADR 0001: manual Sentry capture, kept signal-rich. The PR has 14 unclassified capture sites.
- `CONTEXT.md`: new domain terms belong there. The "Temporary voice channels" section now exists.
- `CLAUDE.md`: a test-guild smoke test is a mandatory manual gate for user-facing command changes.
- Land strategy is `pr` against `develop`. A human merges.
- Testing standard: a test must fail exactly when the behaviour is wrong, and assert only caller-observable things. Several tests in the PR are vacuous; the review flags them.
