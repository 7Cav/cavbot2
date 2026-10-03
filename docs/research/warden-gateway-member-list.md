# What the gateway state holds for guild members

Research for [#413](https://github.com/7Cav/cavbot2/issues/413) on the
warden-page map, [#412](https://github.com/7Cav/cavbot2/issues/412). The
question: can the warden page list every Warden role holder, flag an Internal
holder with no rank role, and resolve pasted usernames from the bot's gateway
state, with no Discord call on page load?

Sources, each pinned:

- discordgo v0.29.0, the version in `go.mod` line 7, read from the Go module
  cache. It is still discordgo's latest release.
- Discord's API docs, from the `discord/discord-api-docs` repo at commit
  `c43598d` (2026-10-02). Line links point at that commit.
- This repo at `origin/develop` `4436fd6`.

## Answer

Yes, if the bot asks Discord for the full member list after every
`GUILD_CREATE` for the guild. It never asks today, so the state holds only a
fraction of the members, and the page could not list every holder from it.

With the full list in the state, every field the page shows is there, and a
page load scans 30,000 members in a few milliseconds. Between each
`GUILD_CREATE` and the last chunk of the reply, the state holds a partial
list. Memory is about 0.6 to 0.85 KB per member, or 17 to 24 MiB of live heap
at 30,000 members.

## What the state holds today

Member tracking is on. `discordgo.New` builds the state with `NewState`, which
sets `TrackMembers: true` ([discord.go:36][dg-new], [state.go:58-77][dg-newstate]).
`main.go` keeps that state and adds `IntentsGuildMembers` to the default
intents, but not `IntentsGuildPresences` ([main.go:188-196][repo-intents],
[structs.go:3044-3064][dg-intents]). Nothing in the repo calls
`RequestGuildMembers` or handles `GuildMembersChunk`.

discordgo adds a member to the state on these events only
([state.go:954-1185][dg-oninterface]):

| Event | What it does to the member set | Source |
|---|---|---|
| `READY` | Replaces the guild list with Discord's unavailable placeholders and builds an empty member map for each. | [state.go:911-952][dg-onready] |
| `GUILD_CREATE` | Rebuilds the guild's member map from the payload's `members`. | [state.go:89-113][dg-guildadd] |
| `GUILD_MEMBER_ADD` | Adds the member, adds 1 to `MemberCount`. | [state.go:982-994][dg-oninterface] |
| `GUILD_MEMBER_UPDATE` | Adds the member, or overwrites the cached one. | [state.go:995-1005][dg-oninterface] |
| `GUILD_MEMBER_REMOVE` | Removes the member, subtracts 1 from `MemberCount`. | [state.go:1006-1018][dg-oninterface] |
| `GUILD_MEMBERS_CHUNK` | Adds or overwrites each member in the chunk. | [state.go:1019-1031][dg-oninterface] |
| `PRESENCE_UPDATE` | Adds a bare member for a user coming online. | [state.go:1155-1180][dg-oninterface] |

Three of these never fill the list here:

- Without `GUILD_PRESENCES`, a `GUILD_CREATE` carries only the bot and the
  users in voice channels, whatever the guild's size
  ([gateway-events.mdx:776][dd-guildcreate-warning]). The `large_threshold`
  of 250 that discordgo sends ([discord.go:56][dg-new]) only matters with
  that intent ([gateway-events.mdx:196][dd-rgm]).
- `PRESENCE_UPDATE` needs `GUILD_PRESENCES` ([gateway.mdx:372-373][dd-intents-presences]),
  so the bot never receives it.
- No chunk request is ever sent, so no `GUILD_MEMBERS_CHUNK` arrives.

Voice joins, messages and interactions add no one. `voiceStateUpdate` touches
only `VoiceStates` ([state.go:836-866][dg-voice]), and messages are cached
only when `MaxMessageCount` is non-zero, which it is not by default
([state.go:1112-1115][dg-oninterface]).

So the state holds the bot, whoever was in voice at the last `GUILD_CREATE`,
and every member who joined or whose member or user object changed since.
A member who got a Warden role before the last `GUILD_CREATE` and has not
changed since is missing. `Guild.MemberCount` does hold the full count. Discord
sends it in `GUILD_CREATE` ([gateway-events.mdx:765][dd-guildcreate]) and
discordgo adjusts it on each add and remove.

`GuildSnapshot` copies channels, roles and the boost tier, and no members
([temp_vc.go:418-433][repo-snapshot], [temp_vc.go:612-644][repo-guilddata]).
`GuildMemberRanks` reads `g.Members` from the `GUILD_CREATE` payload, which is
the partial list above ([temp_vc.go:252-272][repo-ranks]).

## What it takes to hold every member

### Filling the list

The gateway sends the rest of the members only on request, through opcode 8, Request
Guild Members, with `query: ""` and `limit: 0`. Discord replies with
`GUILD_MEMBERS_CHUNK` events of up to 1,000 members each
([gateway-events.mdx:196][dd-rgm]). Asking for the whole list needs the
`GUILD_MEMBERS` intent, which the bot has ([gateway-events.mdx:201][dd-rgm-limits]).
Each chunk carries `chunk_index`, `chunk_count` and the request's `nonce`, so
the bot can tell when the reply is complete
([gateway-events.mdx:916-932][dd-chunk]).

discordgo sends the request with `Session.RequestGuildMembers(guildID, "", 0,
nonce, false)` ([wsapi.go:461-463][dg-rgm], [wsapi.go:533-547][dg-rgm-send])
and applies each chunk to the state on its own
([state.go:1019-1031][dg-oninterface]).

Two limits apply to the request:

- Since 1 October 2025, Discord allows one full-list request per guild per
  bot every 30 seconds. A request inside that window gets a `RATE_LIMITED`
  dispatch with `retry_after`. The limit covers the request, not the chunks
  that answer it ([change-log.mdx:1737-1790][dd-changelog]).
- A connection may send 120 gateway events per 60 seconds
  ([gateway.mdx:509][dd-sendlimit]). One request per `GUILD_CREATE` is far
  below that.

discordgo v0.29.0 has no type for `RATE_LIMITED`. It logs the dispatch as an
unknown event at warning level and passes it only to handlers of the raw
`*discordgo.Event` ([wsapi.go:656-677][dg-onevent]).

discordgo serializes `guild_id` as a one-element array, with a "TODO:
Deprecated" note on the field ([wsapi.go:438-446][dg-rgm-struct]). The docs
type `guild_id` as a single snowflake and allow one guild per request
([gateway-events.mdx:202][dd-rgm-limits], [gateway-events.mdx:215][dd-rgm-struct]).
I found no Discord statement on whether the array form is still accepted. A
request on the test guild would settle it.

### Keeping it current

With `GUILD_MEMBERS`, the bot receives `GUILD_MEMBER_ADD`,
`GUILD_MEMBER_UPDATE` and `GUILD_MEMBER_REMOVE` ([gateway.mdx:336-340][dd-intents-members]).
discordgo applies all three to the state already (table above). An update
fires on a member change and also when the member's user object changes
([gateway-events.mdx:894][dd-memberupdate]), so a role grant, a nickname
change and a username change each reach the state.

Two event details affect a cached list:

- `GUILD_MEMBER_ADD` "may also be sent for users who are already members"
  ([gateway-events.mdx:863][dd-memberadd]). discordgo overwrites the cached
  member in that case, but still adds 1 to `MemberCount`
  ([state.go:982-994][dg-oninterface]). The count can drift above the list.
- `GUILD_ROLE_DELETE` carries only `guild_id` and `role_id`
  ([gateway-events.mdx:958-968][dd-roledelete]). discordgo removes the role
  from `guild.Roles` and leaves its ID in each cached member's `Roles`
  ([state.go:432-454][dg-roleremove]). The docs say nothing about member
  updates following a role delete. A purge that deletes and recreates a
  Warden role can leave the old role's ID on cached members.

The docs do not say how chunk contents are ordered against member events
that arrive while the chunks are in flight. discordgo overwrites the whole
cached member with whatever arrives last, keeping only the old `JoinedAt`
when the new one is zero ([state.go:309-332][dg-memberadd]).

### When the list resets

| What happens | Effect on the member list | Source |
|---|---|---|
| Fresh session: first start, or any re-identify | `READY` empties the list. The guild's `GUILD_CREATE` then refills it with the bot and voice users only. | [state.go:935-944][dg-onready], [gateway-events.mdx:776][dd-guildcreate-warning] |
| Outage: `GUILD_DELETE` then `GUILD_CREATE` | `GuildRemove` drops the guild. The new `GUILD_CREATE` rebuilds the member map from its own partial `members`, dropping every chunked member. | [state.go:153-178][dg-guildremove], [state.go:107-113][dg-guildadd] |
| Successful resume | No `READY` and no `GUILD_CREATE`. Discord replays the missed events in order, then sends `RESUMED`. The list survives and catches up. | [gateway.mdx:248][dd-resuming], [gateway.mdx:267][dd-resuming-replay] |

So a complete list needs a fresh request after every `GUILD_CREATE` for the
guild, not only at process start. Each of those requests is subject to the
30-second limit.

How often the re-identify path runs depends on discordgo's reconnect logic:

- discordgo resumes after a read error, a missed heartbeat ACK or an opcode 7
  Reconnect, using the stored session ID
  ([wsapi.go:127-152][dg-open-resume], [wsapi.go:218-238][dg-listen], [wsapi.go:300-308][dg-heartbeat],
  [wsapi.go:609-614][dg-op7]).
- It answers opcode 9 Invalid Session by identifying again, which starts a
  fresh session ([wsapi.go:618-629][dg-op9]). Discord sends opcode 9 when a
  resume fails ([gateway.mdx:273][dd-invalid]).
- discordgo resumes against the URL it first connected to. Its `Ready` struct
  has no `resume_gateway_url` field ([events.go:38-46][dg-ready]). Discord
  says an app that skips `resume_gateway_url` "will experience disconnects at
  a higher rate than normal" ([gateway.mdx:261][dd-resume-url]).

## Memory and startup cost

### Method

I filled a discordgo v0.29.0 `State` with synthetic members and measured it.
The program sent each 1,000-member chunk the way discordgo receives one with
`Identify.Compress` on: a zlib-compressed `GUILD_MEMBERS_CHUNK` payload,
inflated, decoded into `discordgo.Event`, unmarshalled into
`GuildMembersChunk` and passed to `State.OnInterface`. It read
`runtime.MemStats.HeapAlloc` after two forced GCs before and after. Go 1.27.1,
`TZ=UTC` as in the Alpine image, on an Apple M3. The production host's CPU is
not known, so treat the times as an order of magnitude.

Assumed per member:

- User: an 18 or 19 digit ID, a username of 6 to 16 characters, a global name
  on 70% of members (6 to 20 characters), an avatar hash on 80%,
  discriminator `"0"`.
- Member: a nickname on 60% (8 to 24 characters), a join date, and a role
  count spread evenly from 0 to twice the mean. Two runs: a mean of 6 roles
  and a mean of 15, drawn from 150 guild roles. 5% hold the Warden role.
- The payload also carries null user fields such as `banner`,
  `accent_color`, `avatar_decoration_data`, `collectibles` and
  `primary_guild` ([user.mdx:30-58][dd-user]). discordgo has no struct field
  for the last three, so they cost wire bytes and decode time but no
  retained memory.

### Results

Retained heap came to about 440 B per member plus 27 B per role ID the member
holds: 600 B at 6 roles, 845 B at 15. The figure covers the `Member` and
`User` structs, their strings, the role ID slice, the member map entry and
the `guild.Members` slot. Go's GC lets the heap grow to about twice the live
heap before collecting, at the default `GOGC=100`
([GC guide](https://go.dev/doc/gc-guide)), so the last column doubles the
15-role figure.

| Members | Chunks | Live heap, 6 roles | Live heap, 15 roles | Heap ceiling, 15 roles | Wire, zlib | Local apply time |
|---:|---:|---:|---:|---:|---:|---:|
| 5,000 | 5 | 2.8 MiB | 4.0 MiB | about 8 MiB | 0.5 to 0.6 MiB | 30 ms |
| 15,000 | 15 | 8.6 MiB | 12.1 MiB | about 24 MiB | 1.4 to 1.7 MiB | 78 to 96 ms |
| 30,000 | 30 | 17.2 MiB | 24.2 MiB | about 48 MiB | 2.9 to 3.4 MiB | 158 to 193 ms |

Uncompressed, a member is 650 to 845 B of JSON. The zlib figures come from
synthetic names and IDs, so the live ratio may differ.

### Startup time

The local cost is about 5 to 6.5 ms per chunk to inflate, decode and apply.
The state part alone, `OnInterface`, took 4.6 ms for all 30 chunks at 30,000
members. discordgo processes gateway messages one at a time on its listen
goroutine and updates the state before any handler runs
([wsapi.go:210-260][dg-listen], [event.go:189-240][dg-handleevent]). Other
events wait behind at most one chunk.

Discord does not document how fast it sends the chunks, so the wall time from
request to last chunk is unknown. It can only be measured on the live guild.

Chunks do not hold up the bot's existing startup. `Session.Open` returns
after reading the one `READY` or `RESUMED` message and starting the listen
goroutine ([wsapi.go:170-200][dg-open-ready]). `GUILD_CREATE` and any chunks
arrive after that, on the listen goroutine, while `main.go` goes on to
register commands ([main.go:251-298][repo-open]).

## Member fields kept for display

All five fields the page shows are in the cached member
([structs.go:1558-1601][dg-member], [user.go:44-103][dg-user]):

| Field | Where in discordgo | Discord's field |
|---|---|---|
| Username | `Member.User.Username` | `user.username`, "not unique across the platform" ([user.mdx:38][dd-user]) |
| Global name | `Member.User.GlobalName` | `user.global_name` ([user.mdx:40][dd-user]) |
| Server nickname | `Member.Nick` | `nick` ([guild.mdx:382][dd-member]) |
| Avatar | `Member.Avatar` (guild avatar hash), `Member.User.Avatar` | `avatar`, `user.avatar` |
| Join date | `Member.JoinedAt` | `joined_at` |

discordgo also has helpers for display. `Member.DisplayName()` returns the
nickname, then the global name, then the username
([structs.go:1642-1647][dg-displayname], [user.go:157-162][dg-user-displayname]).
`Member.AvatarURL(size)` returns the guild avatar, then the user's avatar,
then Discord's default avatar ([structs.go:1614-1622][dg-avatarurl],
[user.go:127-154][dg-user-avatarurl]).

`GUILD_MEMBER_UPDATE` sends `joined_at` as nullable. discordgo keeps the
cached join date when an update's is zero ([state.go:327-329][dg-memberadd]).

## A page load inside the 10 s budget

The hub page's budget is `hubPageBudget`, 10 s, set as one context deadline
over the page's reads ([panel.go:60-68][repo-budget], [panel.go:411][repo-budget-ctx]).
`readGuild` reads the gateway state through `GuildData` once per page load,
polling every 100 ms while the guild's data is still on its way
([hubs.go:576-617][repo-readguild]).

Measured reads, each under one `State.RLock`, median of 15 runs on the M3:

| Read | 5,000 members | 30,000 members |
|---|---:|---:|
| Find the Warden holders and check each for a rank role, copying each holder | 0.3 to 0.8 ms | 2.1 to 5.3 ms |
| Copy every member's `Member` and `User` structs | 0.1 to 0.3 ms | 1.2 to 1.5 ms |
| Build a lower-cased index of usernames, global names and nicknames | 0.7 to 0.8 ms | 5.4 to 6.0 ms |
| Resolve 200 pasted names by scanning the list once per name | 5.2 to 5.3 ms | 42 ms |

The slowest read, 42 ms, uses less than 0.5% of the budget. A pasted
Discord ID needs no scan. `State.Member` looks it up in the member map
([state.go:384-402][dg-member-get]).

Two facts about locking apply to any member read:

- The gateway handlers write members in place. `memberAdd` copies the new
  member over the cached one, and `MemberRemove` shifts `guild.Members`
  ([state.go:330][dg-memberadd], [state.go:373-376][dg-memberremove]).
  `GuildData` copies channels and roles under `State.RLock` for the same
  reason ([temp_vc.go:607-644][repo-guilddata]).
- `State.Member` and `State.Guild` take `State.RLock` themselves. Go forbids
  taking a read lock recursively ([sync.RWMutex](https://pkg.go.dev/sync#RWMutex)),
  which the repo already notes ([temp_vc.go:565-569][repo-rlock-note]).

Two caveats bound the answer:

- The list is partial from each `GUILD_CREATE` until its last chunk lands,
  and the state itself does not record whether the chunks are complete.
  `chunk_index`, `chunk_count` and the nonce are the only completion signal
  ([gateway-events.mdx:916-932][dd-chunk]).
- discordgo changes `MemberCount` on `GUILD_MEMBER_ADD` and
  `GUILD_MEMBER_REMOVE` without taking the state's write lock
  ([state.go:984-989][dg-oninterface], [state.go:1008-1013][dg-oninterface]).
  A read of `MemberCount` under `RLock` races with those writes.

## Open questions

- How long Discord takes to deliver all chunks for the live guild. The docs
  give no rate, so it needs a measurement once the member-count ticket has
  the live count.
- Whether Discord still accepts discordgo's array-form `guild_id` in opcode 8.
  One request on the test guild settles it.
- Whether Discord sends `GUILD_MEMBER_UPDATE` to each holder when a role is
  deleted. If not, cached members keep the deleted Warden role's ID after
  each purge. The docs are silent; a role delete on the test guild settles it.

[dg-new]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/discord.go#L32-L64
[dg-newstate]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L58-L77
[dg-intents]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/structs.go#L3044-L3064
[dg-oninterface]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L954-L1185
[dg-onready]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L911-L952
[dg-guildadd]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L89-L150
[dg-guildremove]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L153-L178
[dg-memberadd]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L309-L332
[dg-memberremove]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L349-L381
[dg-member-get]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L384-L402
[dg-roleremove]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L432-L454
[dg-voice]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/state.go#L836-L866
[dg-rgm]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L454-L463
[dg-rgm-send]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L533-L547
[dg-rgm-struct]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L438-L446
[dg-onevent]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L651-L680
[dg-open-resume]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L124-L152
[dg-listen]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L210-L260
[dg-heartbeat]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L300-L308
[dg-open-ready]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L170-L200
[dg-op7]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L607-L614
[dg-op9]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/wsapi.go#L616-L629
[dg-handleevent]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/event.go#L164-L240
[dg-ready]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/events.go#L37-L46
[dg-member]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/structs.go#L1556-L1601
[dg-displayname]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/structs.go#L1640-L1647
[dg-avatarurl]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/structs.go#L1609-L1622
[dg-user]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/user.go#L43-L103
[dg-user-displayname]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/user.go#L156-L162
[dg-user-avatarurl]: https://github.com/bwmarrin/discordgo/blob/v0.29.0/user.go#L123-L154
[dd-rgm]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L194-L196
[dd-rgm-limits]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L198-L208
[dd-rgm-struct]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L211-L223
[dd-guildcreate]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L757-L774
[dd-guildcreate-warning]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L775-L777
[dd-memberadd]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L857-L870
[dd-memberupdate]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L888-L914
[dd-chunk]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L916-L932
[dd-roledelete]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway-events.mdx#L958-L968
[dd-changelog]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/change-log.mdx#L1737-L1790
[dd-intents-members]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway.mdx#L336-L340
[dd-intents-presences]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway.mdx#L372-L373
[dd-resuming]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway.mdx#L246-L255
[dd-resume-url]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway.mdx#L259-L261
[dd-resuming-replay]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway.mdx#L267
[dd-invalid]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway.mdx#L273
[dd-sendlimit]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/events/gateway.mdx#L503-L511
[dd-user]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/user.mdx#L30-L58
[dd-member]: https://github.com/discord/discord-api-docs/blob/c43598daadbefb8afaba48ca74824a15180a8219/developers/resources/guild.mdx#L374-L397
[repo-intents]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/main.go#L188-L196
[repo-open]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/main.go#L251-L298
[repo-snapshot]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/temp_vc.go#L418-L433
[repo-guilddata]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/temp_vc.go#L607-L644
[repo-ranks]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/temp_vc.go#L251-L272
[repo-rlock-note]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/commands/temp_vc.go#L565-L569
[repo-budget]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/panel/panel.go#L60-L68
[repo-budget-ctx]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/panel/panel.go#L405-L414
[repo-readguild]: https://github.com/7Cav/cavbot2/blob/4436fd6890624cdc6dbc866226675687b591fb98/panel/hubs.go#L576-L617
