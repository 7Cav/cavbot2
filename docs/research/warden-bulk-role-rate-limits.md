# How long bulk role changes take on Discord

Research for [#414](https://github.com/7Cav/cavbot2/issues/414) on the warden panel map [#412](https://github.com/7Cav/cavbot2/issues/412), written 2026-10-02. It feeds the map's open question on whether the panel's purge keeps recreating the role, and the reporting question in [#418](https://github.com/7Cav/cavbot2/issues/418).

The question: re-adding approved collaborators, adding a unit's roster, a paste-box add and a bulk remove each change one role on up to a few hundred members, one Discord call per member. What limits apply to those calls, what does discordgo do when it hits one, how long do 100, 300 and 500 calls take, does purging by recreation still beat removing the role from each holder at that size, and what does recreating a role change besides its ID?

Sources are pinned. Discord's docs are read from the `discord/discord-api-docs` repository at commit `c43598daadbefb8afaba48ca74824a15180a8219` (2026-10-02), and the rendered pages live under `https://docs.discord.com/developers/`. Line numbers for docs are in that commit's `.mdx` files. discordgo is `v0.29.0`, the version `go.mod` pins, read from the module cache (`go.sum` hash `h1:FmWeXFaKUwrcL3Cx65c20bTRW+vOb6k8AnaP+EgjDno=`); links point at the same tag on GitHub. This repo is read at `origin/develop` commit `4436fd6`. Where Discord publishes no number, the number here is marked observed, with who observed it and when.

## Answer

1. Discord documents two layers of limit and publishes a number for only one. The global limit is 50 requests per second per bot. Add Guild Member Role and Remove Guild Member Role each sit in a per-route bucket keyed by guild, and Discord does not publish its size. A bot reads it from the `X-RateLimit-*` headers at runtime, and Discord says not to hard-code it. The one well-sourced measurement is for the add: 10 requests per 10 seconds per guild, in a fixed window, observed in July 2025 by the top contributor to the hikari library. I found no public measurement for the remove. discordgo files the add and the remove under one local bucket, so in this bot they share one budget whatever Discord does server-side. Separately, every 401, 403 and 429 counts toward an invalid request limit of 10,000 per 10 minutes per IP, and crossing it gets a temporary Cloudflare ban.

2. discordgo paces calls before sending them, and by default retries after a 429. Each call takes its bucket's lock, sleeps until the bucket resets if the last response left it at 0 remaining, and holds the lock until the response is back. On a 429 with retry on, it sleeps `retry_after` and sends again, with no cap on how many times. `WithRetryOnRatelimit(false)` changes only that 429 branch: discordgo returns a `*discordgo.RateLimitError` carrying `retry_after` instead of sleeping. The pacing sleep before the send still happens, so the option does not make a call non-blocking, and after a 429 the next call on that bucket sleeps until reset. Neither sleep watches the request's context. discordgo ignores `X-RateLimit-Bucket` and `X-RateLimit-Scope`. It pauses other calls after a global 429 only when that 429 also carries `X-RateLimit-Reset-After`, and Discord's documented global 429 has no such header. The warden code passes no request options, so every warden call uses the session default, retry on.

3. At the observed 10 per 10 seconds, calls run at one per second once the first burst of ten is spent. 100 changes take about 1.5 minutes, 300 take just under 5 minutes, and 500 take about 8 minutes. The assumptions: one role per member, calls sent one after another, a remove that shares the add's limit, nothing else using the guild's member-role bucket, and no 429. Changing Internal and External on the same members doubles the call count and the time. Two bulk actions running at once, say one from the panel and one from a `/warden` command, share the bucket, so each takes about twice as long. Sending the calls from parallel goroutines gains nothing, because discordgo runs calls on one bucket one at a time.

4. Recreation still wins on time at 100, 300 and 500 holders, by a wide margin. A purge costs a fixed handful of calls plus one overwrite write per channel that carries the role, and each write is followed by the 200 ms `wardenOverwriteDelay`. That comes to about 0.3 seconds per channel and nothing per holder. It is about 15 seconds per role at 50 channels, and about 2.5 minutes at Discord's cap of 500 channels, against 1.5 to 8 minutes for per-member removal. Per-member removal is only faster when holders number less than about a third of the channels carrying the role. What per-member removal buys is that nothing else changes: the role keeps its ID, its place in the hierarchy, its look and every reference to it, and the audit log records each holder who lost it. It also reaches only the holders the bot sends, while deleting the role reaches every holder. The reply to the last `/warden purge` already says how many overwrites it re-applied, which is the channel count this estimate needs.

5. `recreateRoleWithChannelOverwrites` copies the name, colour, hoist, mentionable flag and permissions, and re-applies each channel overwrite. Everything else is lost or moves:

   - Position. Create Guild Role takes no position, discord.js documents that a new role lands at position 1 just above `@everyone`, and the code never calls Modify Guild Role Positions. After the first purge a Warden role sits at the bottom of the hierarchy unless someone moves it back by hand. Anyone with Manage Roles can then grant and remove it. Its colour loses to any other coloured role a holder has, and a hoisted Warden role no longer groups holders who have another hoisted role.
   - Icon and unicode emoji, if the guild has `ROLE_ICONS`. discordgo can send both, but copying the icon means downloading the image and uploading it again.
   - Gradient and holographic colours, if the guild has `ENHANCED_ROLE_COLORS`. The code copies only the deprecated single `color`, and discordgo `v0.29.0` has no `colors` field.
   - Settings and references tied to the old role: linked-role requirements, the `IN_PROMPT` flag and onboarding prompt options, AutoMod `exempt_roles`, application command permissions set under Server Settings > Integrations, emoji role restrictions, invites that grant roles, other bots' configuration, and role mentions in old messages. A bot token cannot rewrite command permissions at all; Discord requires a user's Bearer token for that.
   - The audit log gets one role delete instead of one member role update per holder, so it no longer shows who lost the role.
   - The bot's own gateway cache drops the role from the guild's role list but leaves the old ID in each cached member's role list.

## Evidence

### Discord's documented rate limit model

Limits apply per route and globally, keyed by the bot token ([rate-limits.mdx L8][rl-8]). Discord says limits "should not be hard coded" and that apps should parse the response headers instead ([L10-12][rl-10]).

Per-route limits exist for many endpoints, "may include the HTTP method", and some are shared across similar endpoints, which the `X-RateLimit-Bucket` header reveals ([L14][rl-14]). Per-route limits are calculated per top-level resource, and the only top-level resources are channels, guilds and webhooks ([L16][rl-16]). So both member-role routes, `PUT` and `DELETE /guilds/{guild.id}/members/{user.id}/roles/{role.id}`, count per guild, and Edit Channel Permissions, `PUT /channels/{channel.id}/permissions/{overwrite.id}`, counts per channel.

Most responses carry `X-RateLimit-Limit`, `-Remaining`, `-Reset` in epoch seconds, `-Reset-After` in seconds with decimals, and `-Bucket`. A 429 adds `-Global` and `-Scope`, where scope is `user`, `global` or `shared` ([L39-45][rl-39]). On a 429 the client should wait for the `Retry-After` header or the body's `retry_after` ([L49][rl-49]). Discord's example of a global 429 carries `Retry-After`, `X-RateLimit-Global` and `X-RateLimit-Scope` and no `X-RateLimit-Reset-After` ([L104-118][rl-104]), which matters for discordgo below.

The global limit is 50 requests per second per bot ([L122][rl-122]). Interaction endpoints are exempt from it ([L128][rl-128]); the panel makes no interaction calls, so all its calls count.

The invalid request limit is 10,000 per 10 minutes per IP, counting 401, 403 and 429 responses. A 429 with `X-RateLimit-Scope: shared` does not count ([L130-140][rl-130]). A 404 does not count either, so adding the role to a member who left the server costs only the bucket slot.

Add Guild Member Role and Remove Guild Member Role need `MANAGE_ROLES`, return 204, fire a Guild Member Update gateway event, and accept `X-Audit-Log-Reason` ([guild.mdx L1115-1131][g-1115]). Neither page gives a number. Discord has no endpoint that changes a role on many members in one call; the only bulk member endpoint is Bulk Guild Ban ([guild.mdx L1189][g-1189]). Modify Guild Member can set a member's whole `roles` array in one call ([guild.mdx L1050-1074][g-1050]), but it is still one call per member.

Create Guild Role takes `name`, `permissions`, the deprecated `color`, `colors`, `hoist`, `icon`, `unicode_emoji` and `mentionable`, and no position ([guild.mdx L1246-1269][g-1246]). Modify Guild Role Positions is a `PATCH` on `/guilds/{guild.id}/roles` with an array of `{id, position}` ([guild.mdx L1271-1288][g-1271]). Delete Guild Role fires a Guild Role Delete event ([guild.mdx L1319-1326][g-1319]). Edit Channel Permissions needs `MANAGE_ROLES` and returns 204 ([channel.mdx L504-519][c-504]). A guild holds at most 250 roles, error `30005`, and 500 channels, error `30013` ([opcodes-and-status-codes.mdx L205, L210][oc-205]).

### Observed numbers Discord does not publish

Member role add. davfsa, the top contributor to the hikari Python library, reported the headers on `PUT /guilds/<>/members/<>/roles/<>` in [discord-api-docs#7680](https://github.com/discord/discord-api-docs/issues/7680) on 2025-07-11. A normal response carried `x-ratelimit-limit: 10`, `x-ratelimit-remaining: 9` and `x-ratelimit-reset-after: 10.000`. After hitting the limit the response carried `Retry-After: 5` and `x-ratelimit-reset-after: 4.815`. The issue says this route uses a fixed window, so ten more requests are allowed 10 seconds after the first, unlike the sliding windows most routes moved to in 2022. The issue is open with no reply from Discord. This is observed, not documented, and could change.

Member role remove. I found no public measurement of the `DELETE` route's bucket, nor anything saying whether it shares the `PUT` route's Discord bucket. The docs allow either, since per-route limits "may include the HTTP method".

Role creation. In [discord-api-docs#1480](https://github.com/discord/discord-api-docs/issues/1480#issuecomment-610125082) on 2020-04-07, Discord staff member msciotti put the role creation limit at "250 per 48 hours, keyed on guild id". The reporter's library had sat silently waiting on it. That limit is in neither the current docs nor the change log; a 2019 docs PR that would have added a per-guild warning for role creation, [#1073](https://github.com/discord/discord-api-docs/pull/1073), was closed unmerged. In [discord.py#5840](https://github.com/Rapptz/discord.py/issues/5840#issuecomment-696805804) on 2020-09-22, discord.py's author said role edit and create limits are harsh, with a 24 hour wait, to stop bots cycling role colours. Both are six years old. A purge creates one or two roles per war, so neither is close.

Edit Channel Permissions. I found no public measurement. Its bucket is per channel per the docs, and a purge writes each channel once per role.

### discordgo v0.29.0's rate limiter

Bucket keys. Every request goes through `RequestRaw`, which locks the bucket named by the endpoint's bucket ID before sending ([restapi.go L188-193][dg-rest-188]). Each endpoint method picks that ID:

- `GuildMemberRoleAdd` and `GuildMemberRoleRemove` both use `EndpointGuildMemberRole(guildID, "", "")` ([restapi.go L993-1009][dg-rest-993]). One key per guild covers both methods, every member and every role.
- `GuildRoles` (`GET`), `GuildRoleCreate` (`POST`) and `GuildRoleReorder` (`PATCH`) all use `EndpointGuildRoles(guildID)` ([restapi.go L1097-1157][dg-rest-1097]). `GuildRoleDelete` uses `EndpointGuildRole(guildID, "")` ([L1162-1167][dg-rest-1162]).
- `ChannelPermissionSet` uses `EndpointChannelPermission(channelID, "")`, one key per channel ([L2050-2061][dg-rest-2050]).

discordgo reads only `X-RateLimit-Remaining`, `-Reset`, `-Global` and `-Reset-After` ([ratelimit.go L141-144][dg-rl-141]). It never reads `-Bucket`, `-Limit` or `-Scope`, so it cannot tell when Discord groups or splits routes differently from its own keys.

Pacing before the send. A new bucket starts with `Remaining: 1` ([ratelimit.go L46-70][dg-rl-46]). `LockBucketObject` takes the bucket's mutex, sleeps for `GetWaitTime`, then decrements `Remaining` ([L95-104][dg-rl-95]). `GetWaitTime` returns the time to the bucket's reset when `Remaining` is below 1 and the reset is ahead, and otherwise the time left on the global lock ([L73-87][dg-rl-73]). `Release` runs after the response arrives ([restapi.go L235-247][dg-rest-235]), unlocks the mutex on return, and updates the bucket from the headers ([ratelimit.go L122-197][dg-rl-122]). With `Reset-After` present it sets the bucket's reset from it, or sets the global lock instead when `X-RateLimit-Global` is also present ([L151-165][dg-rl-151]). Without `Reset-After` it falls back to `Reset` and the `Date` header plus 250 ms of padding ([L166-185][dg-rl-166]). Then it stores `Remaining` ([L188-194][dg-rl-188]).

Two consequences follow. Calls that share a key run one at a time, since the mutex is held across the round trip. And because the global lock is set only when a response carries both `X-RateLimit-Global` and `X-RateLimit-Reset-After`, a global 429 shaped like Discord's documented example stops only the call that received it.

The 429 branch ([restapi.go L279-298][dg-rest-279]). discordgo decodes the body into `TooManyRequests`, turning the float `retry_after` seconds into a `time.Duration` ([structs.go L1670-1694][dg-structs-1670]). If the body does not decode it returns that decode error. Then:

- With `ShouldRetryOnRateLimit` true, it logs at informational level, emits a `RateLimit` event to any handler, calls `time.Sleep(rl.RetryAfter)`, locks the bucket again, which can mean a second sleep until the bucket's reset, and resends with the same retry sequence number. Nothing caps 429 retries. `MaxRestRetries` only counts retries after a 502 ([L270-278][dg-rest-270]).
- With it false, it returns `&RateLimitError{&RateLimit{TooManyRequests: &rl, URL: urlStr}}` and does not sleep ([L296-297][dg-rest-296]; type at [L85-95][dg-rest-85]). `Release` has already applied the 429's headers, so the bucket stays at 0 remaining until its reset, and the next call on that key sleeps in `LockBucketObject` before it sends.

`WithRetryOnRatelimit(retry)` sets `ShouldRetryOnRateLimit` on that one request's config ([restapi.go L128-133][dg-rest-128]). The session default is true, with `MaxRestRetries` 3 and a 20 second HTTP client timeout ([discord.go L37-46][dg-discord-37]). `WithContext` reaches only the HTTP request ([restapi.go L159-164][dg-rest-159]); the pacing sleep and the 429 sleep are plain `time.Sleep` calls that a cancelled context does not interrupt ([ratelimit.go L98-100][dg-rl-95], [restapi.go L291][dg-rest-279]).

### What this repo does with it

The warden commands reach Discord through `GuildManager`, whose methods drop discordgo's request options ([guild_manager.go L12-15, L73-79][r-gm]). `main.go` creates the session at [L188][r-main] and never changes `ShouldRetryOnRateLimit`, so every warden call retries 429s by default. The temp voice channel manager does the opposite and passes `WithRetryOnRatelimit(false)` on every call but one ([temp_vc.go L435-443][r-tvc]), and the panel's hub page relies on that to show a 429 as a wait ([discord_error_detail.go L13-17][r-ded]). No handler for discordgo's `RateLimit` event is registered anywhere in the repo, so a retried 429 shows up only in discordgo's own log line.

The per-member paths send one call after another. `/warden bulkadd` caps a run at 50 entries ([warden.go L35-42][r-w-35]) and calls `GuildMemberRoleAdd` once per role per resolved member ([L340][r-w-340]). `/warden-bulkadd-internal` calls it once per roster profile that has a linked Discord ID ([warden_bulkadd_internal.go L205-252][r-wbi-205]).

The purge runs in a goroutine after a deferred ephemeral acknowledge ([warden.go L381-384][r-w-381]). `runWardenPurge` resolves the role IDs with one `GuildRoles` call per role, lists the guild's channels once, then recreates each role ([L391-484][r-w-391]). `recreateRoleWithChannelOverwrites` ([L550-604][r-w-550]) fetches the role with another `GuildRoles` call ([L625-639][r-w-625]), collects that role's overwrite on each channel ([L641-662][r-w-641]), creates the new role with name, color, hoist, mentionable and permissions only ([L563-575][r-w-563]), writes each overwrite in channel ID order with `time.Sleep(wardenOverwriteDelay)` after each ([L664-696][r-w-664]; the 200 ms value at [L44-47][r-w-44]), then deletes the old role ([L596][r-w-596]). Per role that is two `GuildRoles` calls, one create, one overwrite write per channel carrying the role, and one delete, plus one `GuildChannels` call per purge. No call sets position, icon, unicode emoji or `colors`.

### Wall-clock estimates for per-member changes

The model, from the observed limit and discordgo's pacing: the first ten calls go out back to back, one round trip each. The tenth response reports 0 remaining, and discordgo sleeps until the window resets 10 seconds after the first call. Repeat. N calls take about `10 × (ceil(N / 10) − 1)` seconds plus ten round trips. At a 100 ms round trip:

| Calls | One role per member | Internal and External on the same members |
|------:|--------------------:|------------------------------------------:|
| 100 | about 91 s, 1.5 min | about 3 min |
| 300 | about 291 s, just under 5 min | about 10 min |
| 500 | about 491 s, a little over 8 min | about 16.5 min |

Assumptions:

- The add limit is still 10 per 10 seconds per guild. That was observed in July 2025 and is not documented.
- The remove has the same limit. In this bot it shares the add's local bucket regardless, so mixed adds and removes draw on one budget.
- Calls go one after another, nothing else in the bot touches the guild's member-role bucket during the run, and no 429 occurs. A 429 that discordgo retries costs at most the rest of the window.
- The round trip barely matters. It moves the total by about one second per ten calls.
- If Discord has moved the route to a sliding window since July 2025, the long-run rate is the same one call per second, so the totals hold within a few seconds.

Every call counts, including a re-add to a member who already holds the role, since the `PUT` is idempotent but still a request. A member who left the server costs a slot and returns 404.

For the `/warden` commands, a single-role run fits about 900 calls inside the 15 minute interaction token. The panel has no interaction token, but a 500-member action at one call per second outlasts any page load, which is #418's subject.

### Purge by recreation against per-member removal

Recreation time per role, at a 100 ms round trip with K channels carrying the role, is about `(3 + K) × 0.1 + K × 0.2` seconds, roughly 0.3 seconds per channel. It does not depend on how many members hold the role.

| Channels carrying the role | Recreation, one role | Per-member removal, 100 / 300 / 500 holders |
|---------------------------:|---------------------:|--------------------------------------------:|
| 20 | about 6 s | 1.5 min / 5 min / 8 min |
| 50 | about 15 s | 1.5 min / 5 min / 8 min |
| 100 | about 30 s | 1.5 min / 5 min / 8 min |
| 500, the guild cap | about 2.5 min | 1.5 min / 5 min / 8 min |

Per-member removal costs about one second per holder, so it only beats recreation when holders number less than about 0.3 times the channels carrying the role. At 100 or more holders that never happens below the 500 channel cap. The overwrite delay is most of the purge's time: each channel has its own bucket and gets written once per role, so the per-route limits never throttle the purge, and the 200 ms sleep holds it under five writes per second against a global limit of 50.

The other differences, as facts rather than a recommendation:

- Reach. Deleting the role takes it from every holder, including members missing from the bot's member cache. Per-member removal takes it only from the IDs the bot sends, which ties it to #413's answer on whether the gateway state holds every member.
- Failure shape. The code makes recreation close to all-or-nothing per role: a failed create leaves the old role untouched, a failed overwrite write deletes the new role, and a failed delete leaves a duplicate the reply names ([warden.go L550-604][r-w-550]). Per-member removal can stop part way with some holders still holding the role, and each removal can be retried alone.
- What holders see. Recreation takes the role from everyone at the moment of the delete. Per-member removal takes it from holders one by one over minutes.
- Audit log and events. Each member role add or remove writes a `MEMBER_ROLE_UPDATE` audit entry and fires Guild Member Update. Recreation logs one `ROLE_CREATE`, one `CHANNEL_OVERWRITE_CREATE` per channel and one `ROLE_DELETE` ([audit-log.mdx L93-107][al-93]), and Guild Role Delete carries only the guild and role IDs ([gateway-events.mdx L958-968][ge-958]).
- Role budget. Recreation creates one or two roles per war against a limit Discord staff put at 250 per 48 hours in 2020. Because discordgo files `GuildRoleCreate` and `GuildRoles` under one key, an exhausted create budget whose headers report 0 remaining would also stall every `GuildRoles` lookup until the reset. That is an inference from discordgo's code and the 2020 reports, and it is far out of reach at one purge per war.
- Re-adding approved collaborators after a purge costs about one second per collaborator whichever way the purge ran.

### What recreating a role changes

Position. Create Guild Role has no position parameter ([guild.mdx L1246-1269][g-1246]). discord.js's `RoleManager.create` sends the create without one, calls `setPosition` afterwards when the caller asked for a position, and documents that the position resets to 1 when none is given ([RoleManager.js L160-229][djs-rm]). `@everyone` is position 0 ([permissions.mdx L110][p-108]). The warden code never reorders ([warden.go L550-604][r-w-550]). Effects of sitting at position 1:

- Who can manage it. Discord's docs state the hierarchy for bots: a bot can grant only roles below its own highest role, and sort only roles below it ([permissions.mdx L108-115][p-108]). The client applies the same rule to members with Manage Roles; that part is client behaviour the API docs do not cover. A Warden role at the bottom can be granted and removed by anyone with Manage Roles.
- Name colour. The docs say only that roles without a colour do not count toward the computed colour ([permissions.mdx L240][p-240]); the client shows the colour of the member's highest coloured role. At the bottom, a Warden role's colour shows only on holders with no other coloured role.
- Member list. The client lists a member under their highest hoisted role, so at the bottom a hoisted Warden role groups only holders with no other hoisted role. Client behaviour, not in the API docs.
- Restoring the position is one Modify Guild Role Positions call ([guild.mdx L1271-1288][g-1271]); discordgo has it as `GuildRoleReorder` ([restapi.go L1147-1157][dg-rest-1097]) but `GuildManager` does not expose it.

Since `/warden purge` has run every war, the live Warden roles probably already sit near the bottom unless managers move them back by hand. I could not check the live guild.

Colour. The code copies `color`, which the docs mark deprecated in favour of `colors` ([guild.mdx L1246-1269][g-1246]). `colors` adds `secondary_color` for a gradient and `tertiary_color` for a holographic style, settable only with the `ENHANCED_ROLE_COLORS` feature ([permissions.mdx L259-273][p-259]). discordgo `v0.29.0`'s `Role` and `RoleParams` have no `colors` field ([structs.go L1378-1416, L1451-1468][dg-structs-1378]). A solid colour survives recreation; a gradient becomes its primary colour.

Icon and unicode emoji. Both need the `ROLE_ICONS` feature ([guild.mdx L1246-1269][g-1246]). discordgo's `Role` carries the icon hash and the emoji, and `RoleParams` accepts `Icon` as base64 image data and `UnicodeEmoji` ([structs.go L1378-1416, L1451-1468][dg-structs-1378]). The warden code passes neither, and copying an icon takes a download of the image from Discord's CDN first.

Hoist, mentionable, permissions and name are copied. Creating a role with permissions the bot lacks fails under the hierarchy rule above, as it does today.

References to the old ID. Each of these stores a role ID, and none follows the role to its new ID:

- Linked roles. The `guild_connections` tag marks a linked role ([permissions.mdx L245-257][p-245]); its requirements are set on the role under Server Settings > Roles > Links ([linked-roles tutorial L151-161][lr-151]). discordgo `v0.29.0`'s `Role` has no `Tags` field, so the bot cannot see whether a role is linked.
- Onboarding. The `IN_PROMPT` role flag means members can pick the role in an onboarding prompt ([permissions.mdx L310-314][p-310]), and prompt options list `role_ids` ([guild.mdx L592-632][g-592]).
- AutoMod. Each rule's `exempt_roles` lists up to 20 role IDs ([auto-moderation.mdx L29][am-29]).
- Application command permissions. Per-command overwrites name a role by ID ([application-commands.mdx L380-388][ac-380]) and can be edited only with a user's Bearer token, never a bot token ([L341-354][ac-341]). They are what Server Settings > Integrations edits.
- Emoji. An emoji's `roles` lists the roles allowed to use it ([emoji.mdx L23][em-23]).
- Invites. An invite can grant roles on accept ([channel.mdx L551][c-551], [invite.mdx L31][inv-31]).
- Messages. A role mention is `<@&ROLE_ID>` ([reference.mdx L286][ref-286]), so old messages that mention a Warden role point at an ID that no longer exists.
- Other bots and integrations. Anything else that stored the ID breaks without notice. Nothing in Discord lists these. The map already settles that this bot stores nothing keyed by a Warden role's ID.

Role count. The create runs before the delete, so the guild needs one free role slot per role being purged, under the cap of 250.

The bot's gateway cache. On Guild Role Delete, discordgo's state removes the role from `guild.Roles` and nothing else ([state.go L432-453][dg-state-432], [L1040-1043][dg-state-1040]). Cached members keep the old ID in `Member.Roles` until a Guild Member Update replaces their entry. The docs list only Guild Role Delete for Delete Guild Role and do not say whether member updates follow it. Per-member removal fires a Guild Member Update for each member ([guild.mdx L1124-1131][g-1115]), which keeps the cache exact. This bears on #413.

## Not verified

- The size of the `DELETE` member-role bucket, and whether Discord shares it with the `PUT` bucket. The headers on one test-guild removal would settle it; I made no live calls.
- Whether the add is still 10 per 10 seconds. The measurement is from July 2025.
- How many channels carry a Warden overwrite. The last purge reply's "re-applied N overwrite(s)" line gives it.
- Where the live Warden roles sit in the hierarchy, and whether they have an icon, a gradient, linked-role requirements, an AutoMod exemption, a command permission, an onboarding option, an invite or another bot's configuration tied to them. That needs a look at the live guild's settings.

[rl-8]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L8
[rl-10]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L10-L12
[rl-14]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L14
[rl-16]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L16
[rl-39]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L39-L45
[rl-49]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L49
[rl-104]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L104-L118
[rl-122]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L122
[rl-128]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L128
[rl-130]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/rate-limits.mdx#L130-L140
[g-592]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/guild.mdx#L592-L632
[g-1050]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/guild.mdx#L1050-L1074
[g-1115]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/guild.mdx#L1115-L1131
[g-1189]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/guild.mdx#L1189
[g-1246]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/guild.mdx#L1246-L1269
[g-1271]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/guild.mdx#L1271-L1288
[g-1319]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/guild.mdx#L1319-L1326
[c-504]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/channel.mdx#L504-L519
[c-551]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/channel.mdx#L551
[oc-205]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/opcodes-and-status-codes.mdx#L205-L210
[p-108]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/permissions.mdx#L108-L115
[p-240]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/permissions.mdx#L240
[p-245]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/permissions.mdx#L245-L257
[p-259]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/permissions.mdx#L259-L273
[p-310]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/topics/permissions.mdx#L310-L314
[am-29]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/auto-moderation.mdx#L29
[ac-341]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/interactions/application-commands.mdx#L341-L354
[ac-380]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/interactions/application-commands.mdx#L380-L388
[em-23]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/emoji.mdx#L23
[inv-31]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/invite.mdx#L31
[ref-286]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/reference.mdx#L286
[al-93]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/audit-log.mdx#L93-L107
[ge-958]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L958-L968
[lr-151]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/tutorials/configuring-app-metadata-for-linked-roles.mdx#L151-L161
[djs-rm]: https://github.com/discordjs/discord.js/blob/3d6121589f9c0d91f7cf4976307e8be07053a277/packages/discord.js/src/managers/RoleManager.js#L160-L229
[dg-rl-46]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/ratelimit.go#L46-L70
[dg-rl-73]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/ratelimit.go#L73-L87
[dg-rl-95]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/ratelimit.go#L95-L104
[dg-rl-122]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/ratelimit.go#L122-L197
[dg-rl-141]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/ratelimit.go#L141-L144
[dg-rl-151]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/ratelimit.go#L151-L165
[dg-rl-166]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/ratelimit.go#L166-L185
[dg-rl-188]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/ratelimit.go#L188-L194
[dg-rest-85]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L85-L95
[dg-rest-128]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L128-L133
[dg-rest-159]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L159-L164
[dg-rest-188]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L188-L193
[dg-rest-235]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L235-L247
[dg-rest-270]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L270-L278
[dg-rest-279]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L279-L298
[dg-rest-296]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L296-L297
[dg-rest-993]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L993-L1009
[dg-rest-1097]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L1097-L1157
[dg-rest-1162]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L1162-L1167
[dg-rest-2050]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/restapi.go#L2050-L2061
[dg-discord-37]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/discord.go#L37-L46
[dg-structs-1378]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/structs.go#L1378-L1468
[dg-structs-1670]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/structs.go#L1670-L1694
[dg-state-432]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L432-L453
[dg-state-1040]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L1040-L1043
[r-gm]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/guild_manager.go#L12-L79
[r-main]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/main.go#L188
[r-tvc]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/temp_vc.go#L435-L443
[r-ded]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/discord_error_detail.go#L13-L17
[r-w-35]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L35-L42
[r-w-44]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L44-L47
[r-w-340]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L340
[r-w-381]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L381-L384
[r-w-391]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L391-L484
[r-w-550]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L550-L604
[r-w-563]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L563-L575
[r-w-596]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L596
[r-w-625]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L625-L639
[r-w-641]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L641-L662
[r-w-664]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden.go#L664-L696
[r-wbi-205]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/warden_bulkadd_internal.go#L205-L252
