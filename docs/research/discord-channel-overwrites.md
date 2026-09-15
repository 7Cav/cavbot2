# Discord rules for copied overwrites and category inheritance

Research for [#259](https://github.com/7Cav/cavbot2/issues/259), written 2026-09-15. It feeds the permission-source toggle in `docs/temp-vc-decisions.md` (inherit the category, or copy the hub channel's own overwrites) and the moderator role decision in #265.

The question: what does the Discord API allow when a bot creates a voice channel inside a category, and what does it need to copy the hub channel's permission overwrites onto the new channel?

Sources are pinned. Discord's docs are quoted from the `discord/discord-api-docs` repository at commit `f508da478e57c5f7bc043cf7107561d37f5b42e6` (2026-09-14); the rendered pages live under `https://docs.discord.com/developers/` (the old `discord.com/developers/docs` URLs 301 there). discordgo is `v0.29.0` from the module cache (`go.sum` hash `h1:FmWeXFaKUwrcL3Cx65c20bTRW+vOb6k8AnaP+EgjDno=`). PR #232 is read from the local ref `pr-232` at `0edfbb1`.

## Answer

1. Category inheritance is a server-side copy, and "synced" is a comparison, not a field. Discord's own docs never say what happens when `permission_overwrites` is omitted on create. discord.py documents that "the permissions will be automatically synced to category if no overwrites are provided" and sends an empty array to get that, and two bug reports that Discord staff answered describe creating with only `parent_id` "to inherit permission overwrites" without staff correcting the premise. There is no `permission_synced` field on the channel object. Discord defines a synced channel as one that "has the same permissions and overwrites (or lack thereof) as its parent category", so a channel whose copied list equals the category's shows as synced. Once a bot passes any overwrite list, that list is the whole list: the permission pseudocode reads only `channel.permission_overwrites`, and the workaround Discord staff endorsed for a private category was to fetch the parent's overwrites and send them merged into the create payload. So PR #232's spawned channels, created with one explicit owner overwrite, carry none of their category's restrictions and are de-synced from birth. Changing `parent_id` through Modify Channel leaves the overwrites as they are; the only server-side resync is `lock_permissions: true` on Modify Guild Channel Positions, which discordgo `v0.29.0` does not expose.

2. On create, every bit in every copied `allow` and `deny` must be a permission the bot holds at guild level. Create Guild Channel: "If setting permission overwrites, only permissions your bot has in the guild can be allowed/denied." The check is against the bot's guild-level set (`@everyone` OR its roles; Administrator means all). The bot also needs `MANAGE_CHANNELS` to create at all. On the edit path (Edit Channel Permissions, Modify Channel) the bot needs `MANAGE_ROLES`, and the check widens to "permissions your bot has in the guild or parent channel (if applicable)", with an escape hatch when the bot has a `MANAGE_ROLES` overwrite in the channel. A violation returns HTTP 403 with JSON code `50013` ("You lack permissions to perform that action"). Discord staff's summary of the rule: "You can only grant permissions you yourself have as overwrites in a channel."

3. Yes, on create. The exact rule is "Setting `MANAGE_ROLES` permission in channels is only possible for guild administrators." A deny counts too: the report that prompted the rule failed with `50013` on an `@everyone` deny of Manage Permissions with every non-admin permission granted, and a category-level grant did not help. The rule dates from a security hotfix Discord shipped on 2021-01-23. On the edit path the change log wording is "only possible for guild administrators or users with `MANAGE_ROLES` as a permission overwrite in the channel", so a `MANAGE_ROLES` overwrite for the bot on the channel (or, per the current endpoint text, its parent category) substitutes for Administrator there, but nothing substitutes for it at create time. PR #232's spawn call includes `PermissionManageRoles` in the creator's overwrite, so as written it needs the bot to be a guild administrator.

4. All six named permissions can be granted to a moderator role in a spawned channel's overwrite without Administrator, because none of them is `MANAGE_ROLES`. For each one the bot must hold that same bit at guild level: `MANAGE_CHANNELS` (`1 << 4`), `MOVE_MEMBERS` (`1 << 24`), `MUTE_MEMBERS` (`1 << 22`), `DEAFEN_MEMBERS` (`1 << 23`), `CONNECT` (`1 << 20`), `VIEW_CHANNEL` (`1 << 10`). The README's invite grants View Channels and Manage Channels; Move, Mute, Deafen, and Connect are not on that list and would have to come from the bot's role or from `@everyone`'s guild defaults. Two caveats: `MANAGE_CHANNELS` and `MANAGE_ROLES` are 2FA-flagged, so the application owner needs two-factor auth if the guild has server-wide 2FA; and a moderator role that already has Administrator ignores channel overwrites entirely, so an overwrite for it changes nothing.

5. Discord publishes no per-route numbers for `POST /guilds/{guild.id}/channels` or `DELETE /channels/{channel.id}`. The documented model is per-route buckets keyed by top-level resource (guild for create, channel for delete), read from the `X-RateLimit-*` headers, plus a global cap of 50 requests per second per bot. The documented hard limits a hub can hit are 50 channels per category and 500 per guild (error `30013`). discordgo `v0.29.0` tracks a bucket per endpoint string, sleeps until reset when `X-RateLimit-Remaining` hits zero, and on a 429 sleeps `retry_after` and retries, so a burst of joins queues rather than fails. At 7Cav's join rate (one create per hub join, one delete per empty-out) neither the global limit nor a plausible per-route bucket is in reach; the 50-per-category cap is the limit worth designing for, since hubs that share a category share it. One more caution: `401`, `403`, and `429` responses count toward a 10,000-per-10-minutes invalid request limit that ends in a temporary Cloudflare ban, so a spawn path that fails with `50013` on every join must not retry blindly.

## Evidence

### How Discord computes a member's permissions in a channel

The permissions topic gives pseudocode. `compute_base_permissions` ORs `@everyone`'s permissions with every role the member holds and returns `ALL` for the guild owner or anyone with `ADMINISTRATOR`. `compute_overwrites` returns `ALL` for administrators ("ADMINISTRATOR overrides any potential permission overwrites, so there is nothing to do here"), then applies the `@everyone` overwrite, the union of role overwrites, and the member overwrite, in that order, reading them from `channel.permission_overwrites`. No step looks at the parent category. Source: `developers/topics/permissions.mdx` lines 119-187, heading "Permission Overwrites" (https://docs.discord.com/developers/topics/permissions).

That absence is the whole reason category "inheritance" has to be a copy. If the category's overwrites are not on the channel, they do not apply to it.

### Synced is a comparison, not a field

"Rather than inheritance, permissions are calculated by means of what we call Permission Syncing. If a child channel has the same permissions and overwrites (or lack thereof) as its parent category, the channel is considered 'synced' to the category. Any further changes to a parent category will be reflected in its synced child channels. Any further changes to a child channel will cause it to become de-synced from its parent category, and its permissions will no longer change with changes to its parent category." Source: `permissions.mdx` lines 213-215, heading "Permission Syncing".

The channel object has `permission_overwrites` ("explicit permission overwrites for members and roles") and `parent_id`, and no field containing "sync". Source: `developers/resources/channel.mdx` lines 9-45, heading "Channel Object", "Channel Structure" table (https://docs.discord.com/developers/resources/channel#channel-object). A grep of `channel.mdx` and `guild.mdx` for "sync" finds only the `lock_permissions` row and a thread-list note.

discord.js computes its `permissionsLocked` getter by comparing the channel's overwrite cache against the parent's, key by key, and implements `lockPermissions()` as "edit the channel with the parent's overwrites copied in". Source: `packages/discord.js/src/structures/GuildChannel.js` at commit `c626815bff3c985b78c9932e0e8bfb34b85343f9`, lines 118-154 and 285-289 (https://github.com/discordjs/discord.js/blob/c626815bff3c985b78c9932e0e8bfb34b85343f9/packages/discord.js/src/structures/GuildChannel.js).

discordgo's `Channel` struct matches: `PermissionOverwrites` at `structs.go:409`, `ParentID` at `structs.go:415`, and no synced field anywhere in the file.

### Creating a channel with no overwrites

Discord's docs do not say what the server does when `permission_overwrites` is omitted on create. The field is "the channel's permission overwrites", "optional and nullable", with a note that within each overwrite object `allow` and `deny` "can be omitted or set to `null`, which both default to `"0"`". Source: `developers/resources/guild.mdx` lines 893-933, heading "Create Guild Channel" (https://docs.discord.com/developers/resources/guild#create-guild-channel).

The behaviour comes from three places outside the docs, none of which Discord has contradicted.

discord.py's `create_voice_channel` documents the `category` parameter as "The category to place the newly created channel under. The permissions will be automatically synced to category if no overwrites are provided." Its `_create_channel` builds `perms = []` when no overwrites are given and sends `permission_overwrites=perms`; `http.create_channel` drops only `None` values, so an empty array reaches Discord. There is no client-side copy of the parent. Source: `discord/guild.py` at commit `65232c38702be5844cf2ce865a4777eb1928b5d0`, lines 1370-1401 and 1575-1578 (https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/guild.py); `discord/http.py` lines 1253-1289. So an explicit empty array and an omitted key both produce a synced channel, as far as the most used Python library has observed.

Two `discord-api-docs` issues describe creating with only `name`, `type`, and `parent_id` "to inherit permission overwrites" and then editing overwrites. Discord staff (`yonilerner`) answered both on the ordering problem (below) without disputing the inheritance premise. Sources: https://github.com/discord/discord-api-docs/issues/6357 (2023-08-15, "specifying no permission overwrites so that they are inherited from the category") and https://github.com/discord/discord-api-docs/issues/6573 (2023-12-12, "Create a new channel in private category (to inheriting permissions from category channel)").

discord.js `GuildChannelManager.create()` sends `permission_overwrites: permissionOverwrites?.map(...)`, so an unset option is dropped from the JSON body and no parent copy happens client-side. Source: `packages/discord.js/src/managers/GuildChannelManager.js` at commit `c626815b`, lines 180-225.

I did not run a live create against the test guild; that belongs to the smoke-test gate, not a research ticket. The one thing worth checking there is whether the server's own copy is validated against the bot's permissions the way an explicit list is. Nothing in the docs or the issues suggests it is, and the private-category case in #6573 (an `@everyone` deny of `VIEW_CHANNEL` copied onto the new channel) worked without error, but that bot presumably held `VIEW_CHANNEL` anyway.

### Creating a channel with an explicit overwrite list

When a list is passed it is the whole list. The pseudocode reads only the channel's own overwrites, and the fix Discord staff pointed the #6573 reporter toward was "setting the overrides when creating the channel"; the reporter's working version was "grabbing parent permission with get request, adding external member permissions and putting them together directly in the `/guilds/{guild_id}/channels` payload". If the server merged the category's overwrites into an explicit list, that fetch would have been unnecessary. Source: https://github.com/discord/discord-api-docs/issues/6573, comments of 2023-12-14.

The consequence for PR #232 is direct. `commands/temp_vc.go` on `pr-232` creates with `ParentID: hub.CategoryID` and a single member overwrite (`Allow: tempVCOwnerPerms`), lines 1107-1124. Whatever the category denies to `@everyone` is not on the spawned channel, and since the channel's overwrites differ from the category's it is de-synced from its first second, so later edits to the category's permissions never reach live spawned channels (the "Permission Syncing" rule above). Any design that adds a per-channel overwrite (an owner, a moderator role) accepts both effects and has to copy the category or hub overwrites itself.

### Do the copy in the create call, not after

Editing overwrites right after create races the guild process. Discord staff: "The behavior youre seeing is likely because the guild processed the channel and permission override events in the incorrect order. This likely wont be fixed given the nature of our system." And: "`CHANNEL_CREATE` indicates that the guild process has processed the new channel, whereas the API may respond earlier than that." The two suggested fixes were to set the overrides in the create request or to wait for the `CHANNEL_CREATE` gateway event before the `PUT`. Source: https://github.com/discord/discord-api-docs/issues/6573, comments of 2023-12-13 and 2023-12-14; root cause tracked at https://github.com/discord/discord-api-docs/issues/6268. The failure mode is nasty: the `PUT` returns 200 and writes an audit log entry, the `GET` shows the overwrite, and the guild does not enforce it.

So the copied hub or category overwrites, plus any moderator role overwrite, belong in the same `GuildChannelCreateComplex` payload. That is also why the create-time rules in the next section, not the looser edit-time rules, are the ones that bind.

### Moving a channel or deleting its category

Modify Channel accepts `parent_id` ("id of the new parent category for a channel") and `permission_overwrites` as independent fields and has no `lock_permissions`. Source: `channel.mdx` lines 400-454, heading "Modify Channel", "JSON Params (Guild channel)" (https://docs.discord.com/developers/resources/channel#modify-channel). The docs do not say the overwrites change on a parent change, and the community asked for a `lock_permissions` option on this endpoint precisely because they do not: "yes, i am aware that we can pass the parents overwrites in order to lock". Source: https://github.com/discord/discord-api-docs/issues/1796 (2020-07-11). discord.js implements `lockPermissions` on `edit()` by copying the (new) parent's cached overwrites into `permission_overwrites` before the `PATCH`. Source: `GuildChannelManager.js` at `c626815b`, lines 320-336.

The one server-side resync is on Modify Guild Channel Positions: `lock_permissions` "syncs the permission overwrites with the new parent, if moving to a new category", and "Setting `lock_permissions` additionally requires `MANAGE_ROLES`." An entry that changes `parent_id` needs `MANAGE_CHANNELS` on the channel and on the destination and the bot must be able to view the channel, "Otherwise the request fails with a `403` response and error code `50001` (`Missing Access`)". Only one entry per request may change `parent_id` (error `40009`). Source: `guild.mdx` lines 935-964, heading "Modify Guild Channel Positions" (https://docs.discord.com/developers/resources/guild#modify-guild-channel-positions). discordgo `v0.29.0`'s `GuildChannelsReorder` sends only `id` and `position` (`restapi.go:1066-1082`), so this path needs a raw request.

Deleting a category "does not delete its child channels; they will have their `parent_id` removed and a Channel Update Gateway event will fire for each of them." Source: `channel.mdx` lines 487-490, heading "Delete/Close Channel" (https://docs.discord.com/developers/resources/channel#deleteclose-channel). The docs say nothing about the orphans' overwrites; I would expect them to stay as they were.

### What the bot must hold to set an overwrite

Create Guild Channel: "Requires the `MANAGE_CHANNELS` permission. If setting permission overwrites, only permissions your bot has in the guild can be allowed/denied. Setting `MANAGE_ROLES` permission in channels is only possible for guild administrators." Source: `guild.mdx` line 896, heading "Create Guild Channel".

Edit Channel Permissions (`PUT /channels/{channel.id}/permissions/{overwrite.id}`): "Requires the `MANAGE_ROLES` permission. Only permissions your bot has in the guild or parent channel (if applicable) can be allowed/denied (unless your bot has a `MANAGE_ROLES` overwrite in the channel)." Source: `channel.mdx` lines 504-520, heading "Edit Channel Permissions" (https://docs.discord.com/developers/resources/channel#edit-channel-permissions). Modify Channel repeats the same sentence for its `permission_overwrites` field: "If modifying permission overwrites, the `MANAGE_ROLES` permission is required. Only permissions your bot has in the guild or parent channel (if applicable) can be allowed/denied (unless your bot has a `MANAGE_ROLES` overwrite in the channel)." Source: `channel.mdx` line 426. Delete Channel Permission requires `MANAGE_ROLES` only. Source: `channel.mdx` lines 556-559.

"Has in the guild" is the `compute_base_permissions` set: `@everyone`'s permissions OR every role the bot holds, or everything if any of them carries `ADMINISTRATOR`. Discord staff (`night`) put the rule as "You can only grant permissions you yourself have as overwrites in a channel. Managing channel permissions requires Manage Roles on the server level, but also not denied on the channel level." Source: https://github.com/discord/discord-api-docs/issues/2902, comment of 2021-05-10.

Both `allow` and `deny` are checked ("can be allowed/denied"). For copying a hub channel's overwrites that means: take every overwrite on the hub, OR together all their `allow` and `deny` bits, and every bit in that union must be in the bot's guild-level set, with `MANAGE_ROLES` needing Administrator outright. A hub whose overwrites grant a role something the bot lacks (Priority Speaker, Use Soundboard, whatever an admin clicked) fails the whole create with `50013`. The bot either holds a superset of everything any hub might carry, or the copy masks each overwrite's bits with the bot's own set before sending and the spec says which bits get dropped.

Whether the create endpoint needs `MANAGE_ROLES` at all to include overwrites is not stated; only `MANAGE_CHANNELS` is named. The bot has both today (README, "Permission" table), so it does not matter for cavbot2.

The role hierarchy rules ("A bot can edit roles of a lower position than its highest role, but it can only grant permissions it has to those roles") are about editing role objects, not channel overwrites, and the topic says "Otherwise, permissions do not obey the role hierarchy." Source: `permissions.mdx` lines 108-117, heading "Permission Hierarchy". A role overwrite on a channel is therefore not blocked by the target role sitting above the bot's role, unlike the `/warden` role edits the README warns about.

### `MANAGE_ROLES` in an overwrite

The rule was introduced as a security hotfix on 2021-01-23. Discord staff (`night`): "This evening we deployed a breaking change to the Create Guild Channel endpoint to address a permission escalation issue brought to our attention. Permission overwrites in the guild channel creation endpoint are now validated against the permissions your bot has in the guild. Permission overwrites specified in the request body when creating guild channels will now require your bot to also have the permissions being applied. Setting `MANAGE_ROLES` permission in channel overwrites is only possible for guild administrators or users with `MANAGE_ROLES` as a permission overwrite in the channel." Source: https://github.com/discord/discord-api-docs/issues/2522. The same text is the change log entry "Change to Permission Checking when Creating Channels" dated January 22, 2021 (`developers/change-log.mdx` lines 3966-3973, https://docs.discord.com/developers/change-log). The docs PR that recorded it is https://github.com/discord/discord-api-docs/pull/2521, merged 2021-01-26.

The bug report that surfaced it is the closest thing to a test case. A JDA bot with "every server permission except administrator" created a voice channel with `@everyone: deny "manage permissions"` plus two member overwrites and got `50013`. The reporter: "It's also worth noting that giving the exact same permission in the channel category does not work either." The maintainer's read: "you're creating the channel with a MANAGE_ROLES overwrite; that requires admin as #2521 documents" and "the 'has manage_roles in the channel' text is not there in the create channel section". Source: https://github.com/discord/discord-api-docs/issues/2520 (2021-01-23). So on create, a deny of `MANAGE_ROLES` is as fatal as an allow, and a category-level `MANAGE_ROLES` overwrite for the bot does not lift it.

On the edit path the wording differs between the change log ("guild administrators or users with `MANAGE_ROLES` as a permission overwrite in the channel") and the current endpoint text ("unless your bot has a `MANAGE_ROLES` overwrite in the channel", with "parent channel (if applicable)" added to what counts as held). The strict reading is that guild-level `MANAGE_ROLES` alone does not let a bot write a `MANAGE_ROLES` bit into an overwrite; it needs Administrator or an explicit `MANAGE_ROLES` allow overwrite for itself on the channel (or its category). I could not find a staff statement resolving the two wordings. If any future design writes `MANAGE_ROLES` into an overwrite after create (PR #232 does, for the interim owner, `temp_vc.go:959-960`), that is the case to smoke test, with the bot's role deliberately not an administrator.

The client calls `MANAGE_ROLES` "Manage Permissions" in channel settings. Source: `permissions.mdx` line 106.

### A moderator role on a spawned channel

Bit values, descriptions, and channel-type column from the permissions table (`permissions.mdx` lines 39-70, heading "Bitwise Permission Flags", https://docs.discord.com/developers/topics/permissions#permissions-bitwise-permission-flags):

| Permission | Value | Description | Applies to |
|---|---|---|---|
| `MANAGE_CHANNELS` (2FA) | `0x10` (`1 << 4`) | Allows management and editing of channels | T, V, S |
| `VIEW_CHANNEL` | `0x400` (`1 << 10`) | Allows guild members to view a channel, which includes reading messages in text channels and joining voice channels | T, V, S |
| `CONNECT` | `0x100000` (`1 << 20`) | Allows for joining of a voice channel | V, S |
| `MUTE_MEMBERS` | `0x400000` (`1 << 22`) | Allows for muting members in a voice channel | V, S |
| `DEAFEN_MEMBERS` | `0x800000` (`1 << 23`) | Allows for deafening of members in a voice channel | V |
| `MOVE_MEMBERS` | `0x1000000` (`1 << 24`) | Allows for moving of members between voice channels | V, S |
| `MANAGE_ROLES` (2FA) | `0x10000000` (`1 << 28`) | Allows management and editing of roles | T, V, S |
| `ADMINISTRATOR` (2FA) | `0x8` (`1 << 3`) | Allows all permissions and bypasses channel permission overwrites | |

discordgo's constants agree: `PermissionManageChannels = 1 << 4` (`structs.go:2771`), `PermissionViewChannel = 1 << 10` (`:2786`), `PermissionVoiceConnect = 1 << 20` (`:2690`), `PermissionVoiceMuteMembers = 1 << 22` (`:2696`), `PermissionVoiceDeafenMembers = 1 << 23` (`:2699`), `PermissionVoiceMoveMembers = 1 << 24` (`:2702`), `PermissionManageRoles = 1 << 28` (`:2732`), `PermissionAdministrator = 1 << 3` (`:2768`).

Granting any of the six to a moderator role at create time is the create rule applied bit by bit: the bot's guild-level set must contain the bit. None of the six is `MANAGE_ROLES`, so Administrator is not needed. What a moderator role can then do with them: `MANAGE_CHANNELS` lets it rename, set the user limit and bitrate, and delete the channel; `MOVE_MEMBERS` lets it drag and disconnect occupants; `MUTE_MEMBERS` and `DEAFEN_MEMBERS` are server mute and deafen; `CONNECT` and `VIEW_CHANNEL` let it in and let it see the channel even when the category hides it from `@everyone`. What the six do not give it is the ability to edit the channel's overwrites (lock, hide, block a member); that is `MANAGE_ROLES` and is the one bit a non-admin bot cannot hand out at create.

The 2FA marker: "These permissions require the owner account to use two-factor authentication when used on a guild that has server-wide 2FA enabled." Source: `permissions.mdx` line 102. For bots: "For bots with elevated permissions (permissions with a `*` next to them), we enforce two-factor authentication on the owner's account when added to guilds that have server-wide 2FA enabled." Source: `developers/topics/oauth2.mdx` lines 404-406, heading "Two-Factor Authentication Requirement" (https://docs.discord.com/developers/topics/oauth2#two-factor-authentication-requirement). The bot already carries Manage Roles and Manage Channels, so this is already satisfied or already broken, not a new requirement.

Two implicit-permission rules matter for a locked or hidden spawned channel. "Denying a user or a role `VIEW_CHANNEL` on a channel implicitly denies other permissions on the channel." and "For voice and stage channels, denying the `CONNECT` permission also implicitly denies other permissions such as `MANAGE_CHANNEL`." Source: `permissions.mdx` lines 189-197, heading "Implicit Permissions". A moderator role that is denied `CONNECT` by a copied hub overwrite loses its `MANAGE_CHANNELS` on that channel in practice, so the moderator overwrite has to allow `CONNECT` and `VIEW_CHANNEL` explicitly, and it has to be a separate role-typed overwrite entry with the role's ID, since overwrites are keyed by ID and the last write for an ID wins.

### Error codes

From `developers/topics/opcodes-and-status-codes.mdx`, heading "JSON Error Codes" at line 136 (https://docs.discord.com/developers/topics/opcodes-and-status-codes#json):

| Code | Text | Line | When |
|---|---|---|---|
| `10003` | Unknown channel | 143 | `DELETE /channels/{id}` or `PUT .../permissions/{id}` on a channel someone already deleted by hand; HTTP 404 |
| `50001` | Missing access | 259 | The bot cannot see the resource; documented for parent changes on Modify Guild Channel Positions; HTTP 403 |
| `50013` | You lack permissions to perform that action | 271 | Overwrite bit the bot does not hold, `MANAGE_ROLES` without Administrator, missing `MANAGE_CHANNELS` or `MANAGE_ROLES`; HTTP 403 |
| `30013` | Maximum number of guild channels reached (500) | 210 | Create at the guild cap |
| `40009` | Only one channel can have a parent_id modified at a time | 242 | Modify Guild Channel Positions |
| `20028` | The write action you are performing on the channel has hit the write rate limit | 197 | Channel edits; no number is documented |

HTTP 403 is "The `Authorization` token you passed did not have permission to the resource" and 429 is "You are being rate limited" (same file, lines 114-127, heading "HTTP Response Codes"). Which of `50001` and `50013` comes back for a create whose `parent_id` the bot cannot view is not documented for the create endpoint; the positions endpoint documents `50001` for that case.

discordgo surfaces these as `*discordgo.RESTError` (`restapi.go:54-60`) with `Message *APIErrorMessage` whose `Code int` carries the JSON code (`structs.go:2183-2186`), and names them `ErrCodeUnknownChannel = 10003` (`structs.go:2832`), `ErrCodeMissingAccess = 50001` (`:2922`), `ErrCodeMissingPermissions = 50013` (`:2935`). `commands/warden_errors.go` already branches on `restErr.Message.Code` in this way.

### Rate limits and caps

The rate limits topic (`developers/topics/rate-limits.mdx`, https://docs.discord.com/developers/topics/rate-limits) gives no figures for channel create or delete. What it does say:

- "rate limits should not be hard coded into your app" (line 11). Per-route limits "often account for top-level resources within the path using an identifier, for example, `guild_id` when calling `/guilds/{guild.id}/channels`", and "Top-level resources are currently limited to channels (`channel_id`), guilds (`guild_id`), and webhooks". So create is bucketed per guild and delete per channel. Lines 14-16.
- "All bots can make up to 50 requests per second to our API." Heading "Global Rate Limit", lines 120-128.
- On 429, "rely on the `Retry-After` header or `retry_after` field to determine when to retry the request." Heading "Exceeding A Rate Limit", lines 47-58.
- "IP addresses that make too many invalid HTTP requests are automatically and temporarily restricted from accessing the Discord API. Currently, this limit is 10,000 per 10 minutes. An invalid request is one that results in 401, 403, or 429 statuses." Heading "Invalid Request Limit aka Cloudflare bans", lines 130-143. A spawn path that returns `50013` on every hub join is a 403 per join.

Hard caps that are documented: "each parent category can contain up to 50 channels" (`channel.mdx` line 35, "Channel Structure"; also line 72, `GUILD_CATEGORY` "contains up to 50 channels") and 500 channels per guild (error `30013`). With 14 hubs, any hubs that share a category share the 50.

discordgo `v0.29.0`: `GuildChannelCreateComplex` posts with bucket key `guilds/{guild}/channels` (`restapi.go:1043-1051`), `ChannelDelete` with bucket key `channels/{id}` (`:1660-1669`), `ChannelPermissionSet` with `channels/{id}/permissions/` (`:2050-2062`). `Bucket.Release` reads `X-RateLimit-Remaining`, `X-RateLimit-Reset-After`, `X-RateLimit-Reset`, and `X-RateLimit-Global` after each response (`ratelimit.go:122-193`); `LockBucketObject` sleeps until reset when a bucket has no remaining calls or a global limit is active (`ratelimit.go:73-101`). On a 429, `RequestWithLockedBucket` unmarshals `retry_after`, sleeps it, and retries when `ShouldRetryOnRateLimit` is true (`restapi.go:279-298`), which is the default (`discord.go:42`); a 502 is retried up to `MaxRestRetries`, default 3 (`restapi.go:270-277`, `discord.go:45`). Keys are discordgo's own strings, not the `X-RateLimit-Bucket` header the docs recommend, so deletes of different channels never share a local bucket even if Discord groups them.

### discordgo `v0.29.0` mapping

- `GuildChannelCreateData` (`restapi.go:1027-1038`): `PermissionOverwrites []*PermissionOverwrite` with `json:"permission_overwrites,omitempty"` (line 1035) and `ParentID string` with `omitempty` (line 1036). A nil or empty slice omits the key, which is the "inherit the category" request. There is no way to send an explicit empty array from this struct, and none is needed.
- `GuildChannelCreateComplex(guildID, data)` (`restapi.go:1043-1051`) is `POST /guilds/{guild.id}/channels`. `GuildChannelCreate` (`:1057-1062`) is the name-and-type shortcut.
- `PermissionOverwrite` (`structs.go:519-524`): `ID`, `Type` (`PermissionOverwriteTypeRole = 0`, `PermissionOverwriteTypeMember = 1`, `:514-515`), `Deny` and `Allow` as `int64` with `json:",string"`, which is the string serialisation API v8+ requires (`permissions.mdx` line 16).
- `ChannelPermissionSet(channelID, targetID, targetType, allow, deny)` (`restapi.go:2050-2062`) is `PUT /channels/{channel.id}/permissions/{overwrite.id}`, one overwrite per call. `ChannelPermissionDelete` (`:2065-2069`) is the `DELETE`.
- `ChannelEdit(channelID, *ChannelEdit)` (`restapi.go:1639-1650`) is `PATCH /channels/{channel.id}`; `ChannelEditComplex` (`:1654-1656`) is a deprecated alias. `ChannelEdit.PermissionOverwrites` and `ChannelEdit.ParentID` are both `omitempty` (`structs.go:478`, `:479`), so this struct cannot clear a channel's overwrites to none or null its parent.
- `ChannelDelete(channelID)` (`restapi.go:1660-1669`) is `DELETE /channels/{channel.id}`.
- `GuildChannelsReorder` (`restapi.go:1066-1082`) sends only `id` and `position`; `parent_id` and `lock_permissions` are not reachable.

### What PR #232 does today

On `pr-232` (`0edfbb1`), `commands/temp_vc.go`:

- `tempVCOwnerPerms = PermissionManageChannels | PermissionVoiceMoveMembers | PermissionManageRoles` (line 177), with the comment that without `ManageRoles` "the owner could only rename their channel, not lock or hide it".
- The spawn call (lines 1107-1124) is `GuildChannelCreateComplex` with `ParentID: hub.CategoryID` and a one-entry `PermissionOverwrites` list: the creator as a member overwrite with `Allow: tempVCOwnerPerms`. Under the create rule this needs the bot to be a guild administrator, because of the `MANAGE_ROLES` bit, and it leaves the category's own overwrites off the channel.
- The interim-owner path (lines 959-960) grants the same set through `ChannelPermissionSet`, the edit rule, where the `MANAGE_ROLES` bit is subject to the ambiguous wording above.
- `/voice block` (line 1688) writes a member overwrite denying `PermissionVoiceConnect`; needs `MANAGE_ROLES` and `CONNECT` held.
- Nothing reads the hub channel's `PermissionOverwrites`. The decisions doc already notes this: "The PR always passes an explicit overwrite list, so today nothing ever inherits."

`docs/temp-vc-decisions.md` retires the owner overwrite, which removes the `MANAGE_ROLES`-at-create exposure for owners. It does not remove it for the "copy the hub channel's own overwrites" source: a hub whose overwrites carry `MANAGE_ROLES` for MP or an HQ role would trip the same `50013` on every spawn unless the bot is an administrator or the copy strips that bit.

### Not settled here

- Whether omitting `permission_overwrites` on create is validated against the bot's permissions the way an explicit list is. Untested; no source addresses it.
- Whether guild-level `MANAGE_ROLES` alone lets a bot write a `MANAGE_ROLES` bit into an overwrite on the edit path, or whether that needs Administrator or a channel-level `MANAGE_ROLES` overwrite for the bot. The change log and the endpoint text read differently.
- Which of `50001` and `50013` a create returns when the bot cannot view the `parent_id` category.
- The per-route bucket sizes for channel create and delete. Discord does not publish them and says not to hard-code them.
- Whether orphaned children keep their overwrites when a category is deleted. Docs say only that `parent_id` is cleared.

Each of these is a one-line check on the test guild with the bot's role deliberately stripped of Administrator.

## Sources

Discord docs source, `discord/discord-api-docs` at `f508da478e57c5f7bc043cf7107561d37f5b42e6`:

- `developers/resources/guild.mdx`: "Create Guild Channel" (893-933), "Modify Guild Channel Positions" (935-964). Rendered: https://docs.discord.com/developers/resources/guild
- `developers/resources/channel.mdx`: "Channel Object" (9-45), "Overwrite Object" (313-325), "Modify Channel" (400-454), "Delete/Close Channel" (487-502), "Edit Channel Permissions" (504-520), "Delete Channel Permission" (556-559). Rendered: https://docs.discord.com/developers/resources/channel
- `developers/topics/permissions.mdx`: "Bitwise Permission Flags" (39-106), "Permission Hierarchy" (108-117), "Permission Overwrites" (119-187), "Implicit Permissions" (189-197), "Permission Syncing" (213-215). Rendered: https://docs.discord.com/developers/topics/permissions
- `developers/topics/opcodes-and-status-codes.mdx`: "HTTP Response Codes" (122-127), "JSON Error Codes" (143, 197, 210, 242, 259, 271). Rendered: https://docs.discord.com/developers/topics/opcodes-and-status-codes
- `developers/topics/rate-limits.mdx`: whole file. Rendered: https://docs.discord.com/developers/topics/rate-limits
- `developers/topics/oauth2.mdx`: "Two-Factor Authentication Requirement" (404-406). Rendered: https://docs.discord.com/developers/topics/oauth2
- `developers/change-log.mdx`: "Change to Permission Checking when Creating Channels", January 22, 2021 (3966-3973). Rendered: https://docs.discord.com/developers/change-log

Discord staff statements in the docs repository:

- https://github.com/discord/discord-api-docs/issues/2522, `night`, 2021-01-23, the hotfix announcement.
- https://github.com/discord/discord-api-docs/issues/2520, the JDA report that triggered it, with maintainer replies.
- https://github.com/discord/discord-api-docs/pull/2521, the docs change, merged 2021-01-26.
- https://github.com/discord/discord-api-docs/issues/2902, `night`, 2021-05-10, "You can only grant permissions you yourself have as overwrites in a channel."
- https://github.com/discord/discord-api-docs/issues/6573 and https://github.com/discord/discord-api-docs/issues/6357, `yonilerner`, 2023-12, the create-then-edit race; root cause https://github.com/discord/discord-api-docs/issues/6268.
- https://github.com/discord/discord-api-docs/issues/1796 and https://github.com/discord/discord-api-docs/pull/1776, `lock_permissions` on the positions endpoint only.

Libraries, for observed server behaviour:

- discord.py at `65232c38702be5844cf2ce865a4777eb1928b5d0`: `discord/guild.py` 1370-1401, 1575-1578; `discord/http.py` 1253-1289.
- discord.js at `c626815bff3c985b78c9932e0e8bfb34b85343f9`: `packages/discord.js/src/managers/GuildChannelManager.js` 180-225, 320-336; `packages/discord.js/src/structures/GuildChannel.js` 118-154, 285-289.

Local:

- discordgo `v0.29.0` (`$GOMODCACHE/github.com/bwmarrin/discordgo@v0.29.0`): `restapi.go`, `structs.go`, `ratelimit.go`, `discord.go`, `endpoints.go` at the lines cited above.
- `commands/temp_vc.go` at `pr-232` (`0edfbb1fe60595ff914d6000bc6f1218b9739e9e`), lines 177, 959-960, 1107-1124, 1688.
- `docs/temp-vc-decisions.md` on `origin/docs/100-temp-vc-decisions`, "Permission source" row and "New work" item 2.
- `README.md`, "Permission" table under setup step 5.
