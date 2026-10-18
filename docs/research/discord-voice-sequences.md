# Discord voice event sequences we already know

Research for [Collect the Discord voice event sequences we already know (#812)](https://github.com/7Cav/cavbot2/issues/812), written 2026-10-18. It feeds [Wayfinder map: owned voice connection for recorders (#810)](https://github.com/7Cav/cavbot2/issues/810), whose spec needs real Discord event sequences for its states, deadlines and test fixtures.

The question: for each way a recorder can lose or change its call, what does Discord send it, in what order, and how far apart? And does Discord accept the voice token the recorder held when it identifies again after a drop, and for how long?

## Answer

1. **A move to another voice server is well recorded.** About 40 moves across five builds, each made by changing the channel's `rtc_region`. Discord closes the voice gateway with 4022 within a second of the change and sends one `VOICE_SERVER_UPDATE` for the new server. In 9 of 29 counted moves that update came 0.11 to 0.19 s after the close, before the recorder's rejoin went out, and Discord then answered the rejoin with a `VOICE_STATE_UPDATE` alone. In the rest it came after the rejoin started, at 0.53 s in the one such move timed to the millisecond. The recorder identified at the new server 0.56 to 1.68 s after the close, DAVE was set up 0.92 to 2.12 s after it, and sound was back within 2.7 s.
2. **A drop on the same voice server is recorded only from the rejoin on.** The bot's own smoke switch made every drop on record, so no log shows what a voice server sends when a connection really drops. What the logs do show is that Discord answers a voice state update for the channel the recorder is already in with `VOICE_STATE_UPDATE` alone, within 150 ms, and no `VOICE_SERVER_UPDATE`, so no new token. Discord kept the recorder in the channel with no voice connection for as long as anyone waited, up to 10 minutes.
3. **A join into a call where members are already talking is recorded in outline.** The join's `VOICE_SERVER_UPDATE` came within 0.5 s. On one build every DAVE setup that finished did so within 0.7 s of the session description. On another, one first join stalled after the recorder's commit and needed a second Identify. Nobody has written down the opcode order.
4. **A moderator's disconnect is recorded only by its outcome.** The recording ended and the recorder stayed out, with no log lines in the thread.
5. **The held token.** The logs show Discord accepting a token the recorder held in three cases: a second Identify on the same server with the same session about 4 s after the token was issued, after the recorder closed its own connection; an Identify with a token issued before the recorder's rejoin, 0.6 to 1.5 s old; and a first Identify about 85 s after the token was issued. No log shows a refusal, and none shows an Identify with the held token after a real drop. That case is what [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809) does, and it merged on 2026-10-18 with its smoke pass still to run. Discord's docs say nothing about how long a voice token lasts.
6. **No recorded sequence** exists for closes 4006, 4009 and 4015, a moderator's disconnect (4014) or move, the recorder's own gateway session reconnecting, a leave followed by a join, a real network drop and the held-token Identify after it, a `VOICE_SERVER_UPDATE` with a null endpoint, and the opcode order of a join into a talking call. The last section lists what the capture ticket needs for each.

## Sources

**Logs.** Every live sequence here comes from a smoke pass on the test guild, posted by the maintainer's account in an issue or PR thread. I read each thread with `.github/scripts/fetch-issue.sh`, which keeps only what people who can push to the repo wrote. A log line in a thread is a summary written from the bot log, not the raw log, so each timing carries the precision its thread gives it. The ticket's starting threads were these four, plus the PR that fixed the last:

- [fix(voice): follow a moved call whose server Discord named before the rejoin (#735)](https://github.com/7Cav/cavbot2/pull/735)
- [fix(voice): keep recording through a move to a voice server the recorder can't reach yet (#759)](https://github.com/7Cav/cavbot2/pull/759)
- [fix(voice): a dropped voice connection starts its gap at once, and the recorder rejoins instead of resuming (#781)](https://github.com/7Cav/cavbot2/pull/781)
- [A recorder whose voice connection drops on the same voice server never reconnects (#786)](https://github.com/7Cav/cavbot2/issues/786), fixed by [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809)

They point back at earlier smoke passes with more detail, and I followed each of those links. Every claim below cites the thread or comment it comes from.

The smoke builds, oldest first, are `smoke-98750e0`, `smoke-e75355c`, `smoke-df64b83`, `smoke-68f77d4` and `smoke-c5f9692`. Each is `develop` at that commit with a clean tree. No smoke pass has run on a build with the fix from [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809).

**Docs.** Discord's docs are quoted from `discord/discord-api-docs` at commit `f7416a2cf165455f72a31f5ea4e671a4f047f2f7`, committed 2026-10-16, with line links into `developers/topics/voice-connections.mdx`, `developers/topics/opcodes-and-status-codes.mdx`, `developers/events/gateway-events.mdx`, `developers/events/gateway.mdx` and `developers/resources/guild.mdx`. The DAVE whitepaper is `discord/dave-protocol` `protocol.md` at commit `1b2b706ad4a562ede5c2ef901f9d77f6dbe79446`, version 1.1.4.

**Code.** To say what a bot log line means on the wire, I read disgo at the pinned `v0.19.7-0.20260923193941-995ee20fe0d6` and dave-go `v0.5.1` from the module cache, and `voice/` on `develop` at `82c3df1`.

### What the bot log lines mean

| Bot log line | Logged by | What happened on the wire |
|---|---|---|
| `opening voice conn` | disgo `voice/conn.go:347` | the recorder is about to send a voice state update, main gateway opcode 4, naming the channel, for a join or a rejoin |
| `new VoiceStateUpdate` | disgo `voice/manager.go:66` | a `VOICE_STATE_UPDATE` dispatch reached the recorder. The line names the guild but not the user, and the recorder's session has the `GUILD_VOICE_STATES` intent, so other members' voice states log it too |
| `new VoiceServerUpdate` | disgo `voice/manager.go:76` | a `VOICE_SERVER_UPDATE` dispatch reached the recorder, with a voice server and a token |
| `opening voice gateway connection` | disgo `voice/gateway.go:149` | the recorder dials the voice server's websocket |
| `sending Identify command` | disgo `voice/gateway.go:432` | voice gateway opcode 0 |
| `received voice gateway message` with op 4 | disgo | Session Description, after which audio can flow |
| `voice gateway close received` with `code=` | disgo `voice/gateway.go:506` | the voice server closed the websocket with that code |
| `failed to read next message from voice gateway` | disgo | a websocket read failed with no close frame |
| `mls proposals processed and committed` | dave-go | the recorder sent its MLS commit, DAVE opcode 28 |
| `epoch activated` | dave-go `session/mls.go:780` | DAVE is set up and frames decrypt |

## A drop on the same voice server, abrupt and silent

### What the logs show

From the smoke pass on [fix(voice): a dropped voice connection starts its gap at once, and the recorder rejoins instead of resuming (#781)](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480) and the table in [A recorder whose voice connection drops on the same voice server never reconnects (#786)](https://github.com/7Cav/cavbot2/issues/786), build `smoke-c5f9692`, four drops. Discord did not drop these connections. The smoke switch `SMOKE_VOICE_DROP_FILE` broke them on the bot's side, so the logs can't say what the voice server sent on the old connection, or whether it noticed.

1. **The drop.** An abrupt drop logged disgo's `failed to read next message from voice gateway` at once. A silent drop logged nothing until the recorder gave up on the server. With Hello's `heartbeat_interval` at 13.75 s, `closing voice gateway connection` with `message="heartbeat answer overdue"` came 16.75 s after the last heartbeat answer. That 16.75 s is the recorder's own rule, the interval plus 3 s, not Discord's.
2. **The rejoin.** The recorder sent a voice state update for the channel it was already in. Discord answered with two `new VoiceStateUpdate` lines within 150 ms. No `new VoiceServerUpdate` followed, in all four drops.
3. **Silence.** Nothing more came from Discord. The recorder stayed in the channel's member list, deaf, for 1m53s, 36 s, 51 s and 33 s, until the maintainer stopped the recording or changed the channel's region.
4. **A region change.** It ended each stuck gap. Within 2 s the log had `new VoiceServerUpdate`, then `sending Identify command`, and the gap ended. The thread lists no 4022 before it, which fits a recorder holding no voice connection for Discord to close.

In the same pass, the join at the start of each of the six recordings got its `new VoiceServerUpdate` within 0.5 s of `opening voice conn`. So Discord answers a fresh join with a voice server, and a rejoin into the same channel without one.

Discord also leaves a recorder with no voice connection in the channel for a long time. In the smoke pass on [fix(voice): keep recording through a move to a voice server the recorder can't reach yet (#759)](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024), check 4 on `smoke-c5f9692`, the recorder could not reach its new voice server for 10 minutes and stayed in the channel until the bot's own 10-minute limit took it out.

The logs leave two things open. The `new VoiceStateUpdate` line doesn't name the user, so nobody knows whether both lines in step 2 were the recorder's own voice state. And none of these drops ran with the fix from [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809), which identifies at the held server with the held token 250 ms after the voice-state-only answer. Its smoke checks record whether Discord takes that token.

### What the docs say

- A client whose connection "has been severed" should open a new websocket and send Resume, opcode 7, with `server_id`, `session_id`, `token` and, since v8, `seq_ack` ([voice-connections.mdx lines 508 to 537](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L508-L537)). The server answers a good resume with opcode 9 Resumed and resends the messages the client missed ([lines 539 to 578](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L539-L578)).
- A failed resume closes with a close code, and the client should then "follow the Connecting flow to reconnect", which starts again at opcode 4 ([line 551](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L551)). A resume also fails once the server's buffer has lost a message the client missed ([line 580](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L580)). No time window is given for either.
- The docs say a voice state update gets "_two_ events" in reply, and a library "must properly wait for both" ([line 55](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L55)). The logs above contradict this for an update naming the channel the client is already in. Discord sent the voice state alone.
- The heartbeat interval comes from Hello. The docs' example is 41250 ms ([lines 126 to 140](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L126-L140)), and the test guild sent 13750 ms. The voice docs give no rule for a missed heartbeat answer and no close code for one. The rule for a "zombied" connection is written for the main gateway only ([gateway.mdx line 171](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway.mdx#L171)).
- The DAVE whitepaper says the voice gateway proposes removing a member from the MLS group when it disconnects "due to a severed websocket connection", and that removed members are "no longer forwarded that media from the SFU" ([protocol.md lines 238 to 255](https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md#L238-L255)). That is why the recorder rejoins instead of resuming, as the comment above `ownDialsOnly` in `voice/gateway.go` says.

## A move to another voice server, closed with 4022

### What the logs show

Discord closes the recorder's voice gateway with 4022 within a second of a region change, and a region the call already uses moves nothing ([A region change on a channel being recorded ends the recording (#726)](https://github.com/7Cav/cavbot2/issues/726), `smoke-98750e0`). Then Discord sends one `VOICE_SERVER_UPDATE` for the new server, in one of two orders against the recorder's rejoin.

**Order A: the voice server after the rejoin starts.** This was 7 of 8 moves on `smoke-e75355c`, from [A recorder never reaches the new voice server when Discord names it before the rejoin starts (#733)](https://github.com/7Cav/cavbot2/issues/733), and 13 of 21 on `smoke-df64b83`, from [the second smoke pass on fix(voice): keep recording when the call moves to another voice server (#728)](https://github.com/7Cav/cavbot2/pull/728#issuecomment-6183158042).

| After the 4022 | What happened |
|---|---|
| 0 | `voice gateway close received`, `code=4022` |
| about 0.2 s | the recorder's rejoin, once Discord's API has said the channel still exists |
| about 0.5 s | `new VoiceServerUpdate` naming the new server |
| 0.56 to 1.68 s | `sending Identify command` at the new server |
| 0.92 to 2.12 s | `epoch activated` with the recorder's `user_id` |
| within 2.7 s | sound back in the speaker's track |

The 0.2 s and 0.5 s rows come from the one move that [A recording stops when its call moves to a voice server whose hostname doesn't resolve yet (#736)](https://github.com/7Cav/cavbot2/issues/736) times to the millisecond: rejoin at +0.216 s, server at +0.531 s. The last three rows cover all 20 moves on `smoke-df64b83` that reached their server, in both orders.

Other builds agree. On `smoke-e75355c`, the moves that reached their server logged `epoch activated` 0.9 to 1.7 s after the 4022 ([A recorder can hear nobody after its call moves to another voice server, and nothing reports it (#732)](https://github.com/7Cav/cavbot2/issues/732)). On `smoke-68f77d4`, two moves each logged a new `sending Identify command` within a second of the 4022 ([the smoke pass on fix(voice): keep recording through a close disgo answers with a fresh connection (#753)](https://github.com/7Cav/cavbot2/pull/753#issuecomment-6228935419)). On `smoke-c5f9692`, a move to us-west logged `new VoiceServerUpdate` 2 s after the API call and `sending Identify command` 1 s after that ([the smoke pass on fix(voice): keep recording through a move to a voice server the recorder can't reach yet (#759)](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024), check 5).

**Order B: the voice server before the rejoin starts.** This was 1 of 8 moves on `smoke-e75355c` and 8 of 21 on `smoke-df64b83`. Recording 40's fifth move on `smoke-e75355c`, from [A recorder never reaches the new voice server when Discord names it before the rejoin starts (#733)](https://github.com/7Cav/cavbot2/issues/733):

| Time (UTC) | After the 4022 | Bot log |
|---|---|---|
| 22:08:40.791 | 0 | `voice gateway close received`, `code=4022` |
| 22:08:40.953 | 0.162 s | `new VoiceServerUpdate` |
| 22:08:41.015 | 0.224 s | `opening voice conn`, the rejoin |
| 22:08:41.157 | 0.366 s | `new VoiceStateUpdate`, and no `new VoiceServerUpdate` after it |

On that build disgo threw the early server away, so no gateway opened. `smoke-df64b83` has the fix for that, and [its smoke pass on fix(voice): follow a moved call whose server Discord named before the rejoin (#735)](https://github.com/7Cav/cavbot2/pull/735#issuecomment-6183157450) timed all 8 such moves. Each had `new VoiceServerUpdate` 0.11 to 0.19 s after the 4022, then the rejoin's `new VoiceStateUpdate`, `opening voice gateway connection`, and `sending Identify command` 0.78 to 1.68 s after the 4022. Sound came back 1.3 s after the 4022 in the one recording measured.

**A call that moves while the recorder can't reach it.** Recording 43's fourth move on `smoke-df64b83`, from [A recording stops when its call moves to a voice server whose hostname doesn't resolve yet (#736)](https://github.com/7Cav/cavbot2/issues/736): the 4022 at 02:25:49.065, the rejoin at +0.216 s, `new VoiceServerUpdate` at +0.531 s, then "no such host" on every dial. The maintainer moved the call twice more. Each move brought a `new VoiceServerUpdate`, at +13.6 s and +26.2 s, with no voice state alongside, as [the triage comment](https://github.com/7Cav/cavbot2/issues/736#issuecomment-6220976540) says. The issue's table lists no 4022 for those moves, which fits a recorder with no voice connection open.

**DAVE after a move.** [The triage comment on A recorder can hear nobody after its call moves to another voice server, and nothing reports it (#732)](https://github.com/7Cav/cavbot2/issues/732#issuecomment-6180413515) read the DAVE opcodes of the 7 moves that reached a new server on `smoke-e75355c`:

- In the 3 moves where the speaker reached the new server first, the recorder got opcode 29.
- In the 4 where the recorder got there first, both clients raced to commit. The recorder got opcode 29 once, opcode 30 twice, and no answer once.
- In that last move the recorder got the speaker's add proposal in opcode 27, sent its commit in opcode 28, and heard nothing back for 11.3 s, though the speaker's opcodes 12 and 5 kept arriving.
- Every DAVE setup in that run that did finish, 9 of them across first joins and moves, finished within 0.7 s of the session description.

### What the docs say

- `VOICE_SERVER_UPDATE` is "sent when initially connecting to voice, and when the current voice instance fails over to a new server" ([gateway-events.mdx line 1542](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway-events.mdx#L1542)). It carries `token`, `guild_id` and `endpoint`, and no channel.
- A null endpoint means the voice server "has gone away and is trying to be reallocated". The client should disconnect and wait for a new server ([lines 1544 to 1546](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway-events.mdx#L1544-L1546)). No thread logs a null endpoint.
- 4022 is "Disconnected: Call Terminated", sent to all clients for "channel deleted, voice server changed, etc.", with "Should not reconnect" ([opcodes-and-status-codes.mdx line 106](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx#L106)). It was split out of 4014 by [More voice disconnect reasons (discord-api-docs PR 7582)](https://github.com/discord/discord-api-docs/pull/7582), merged 2025-06-06 as commit `ea32903a52`.
- The docs don't link `rtc_region` to a new voice server. They don't say whether the 4022 or the `VOICE_SERVER_UPDATE` comes first, and they don't say whether a client should identify or resume at the new server.
- In a DAVE group change, existing members get opcode 29 and new members get opcode 30, and each answers with opcode 23 once ready ([voice-connections.mdx lines 288 to 292](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L288-L292)). Opcode 12, which kept reaching the recorder about the speaker in the stalled move above, isn't in Discord's opcode table, which lists 11 and 13 and nothing between ([opcodes-and-status-codes.mdx lines 60 to 85](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx#L60-L85)).

### Guesses

- I think Discord sends the new server's `VOICE_SERVER_UPDATE` by itself on every move, whether or not the recorder rejoins, and Orders A and B are the same message racing the rejoin. In Order B it arrives before any rejoin goes out, Discord answers that rejoin with the voice state alone, and later moves with no rejoin at all still bring one. No log shows a move that brought two `VOICE_SERVER_UPDATE`s.
- The docs' "Should not reconnect" for 4022 reads as "don't resume at the old server". The logs show a client staying in the call by identifying at the new one.
- A recorder that arrived second getting opcode 29 rather than 30 doesn't match the docs' rule, unless it had already formed its own group by then. The thread doesn't say which, and the capture ticket can check it.
- Nobody has logged whether Discord sends a `VOICE_STATE_UPDATE` of its own on a move, before the rejoin's.

## Closes 4006, 4009 and 4015

### What the logs show

None. No smoke pass has seen any of the three. [The smoke pass on fix(voice): keep recording through a close disgo answers with a fresh connection (#753)](https://github.com/7Cav/cavbot2/pull/753#issuecomment-6228935419) watched for 4006, 4009 and 4011 through a whole run on `smoke-68f77d4` and saw none. [A recording stops when disgo reopens the recorder's voice connection after a 4006, 4009 or 4011 (#731)](https://github.com/7Cav/cavbot2/issues/731) says Discord sends these closes when something goes wrong on its side, so a test guild can't trigger them on demand. Every claim about them in the repo comes from disgo's close table and from tests against fake voice servers.

### What the docs say

From the voice close code table ([opcodes-and-status-codes.mdx lines 89 to 106](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx#L89-L106)):

| Code | Name | Text | Guidance |
|---|---|---|---|
| 4004 | Authentication failed | "The token you sent in your identify payload is incorrect." | none |
| 4006 | Session no longer valid | "Your session is no longer valid." | none |
| 4009 | Session timeout | "Your session has timed out." | none |
| 4015 | Voice server crashed | "The server crashed. Our bad!" | "Try resuming" |

4015 is the only voice close the docs say to resume after. They give 4006 and 4009 no instruction, so the one documented path is the failed-resume rule, which sends the client back through opcode 4. The docs don't connect 4009 to missed heartbeats. I'd guess it is the close for one.

## A moderator disconnecting the recorder (4014), or moving it to another channel

### What the logs show

Only a disconnect's outcome. In [the smoke pass on fix(voice): a dropped voice connection starts its gap at once, and the recorder rejoins instead of resuming (#781)](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480), check 4 on `smoke-c5f9692`, `PATCH /guilds/{guild}/members/<recorder>` with `{"channel_id":null}` ended the recording with `ended=recorder_left`, and the recorder stayed out. The thread gives no log lines: no close code, no voice state updates, no timings. The 4014 in the code and the issues comes from Discord's close table, not from a log.

No log shows a moderator moving the recorder to another channel.

### What the docs say

- Modify Guild Member with `channel_id` null "will force the target user to be disconnected from voice", and the call fires `GUILD_MEMBER_UPDATE` ([guild.mdx line 1053](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/resources/guild.mdx#L1053)). A `channel_id` moves a member who is connected to voice, which needs `MOVE_MEMBERS` and connect permission in the target ([line 1056](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/resources/guild.mdx#L1056)).
- `VOICE_STATE_UPDATE` is "Sent when someone joins/leaves/moves voice channels" ([gateway-events.mdx line 1538](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway-events.mdx#L1538)).
- 4014 is "Disconnect individual client (you were kicked, the main gateway session was dropped, etc.). Should not reconnect." ([opcodes-and-status-codes.mdx line 100](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx#L100)).
- When a client changes channel itself, "The `token` will be changed and you cannot re-use the previous session" ([voice-connections.mdx line 75](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L75)). The docs don't say whether a move someone else makes follows the same rule, sends a new `VOICE_SERVER_UPDATE`, or closes the old socket with a code.

My guess for a disconnect is a `VOICE_STATE_UPDATE` with a null channel and a 4014 close, in an order nobody has logged. For a move, I'd guess a `VOICE_STATE_UPDATE` with the new channel, a `VOICE_SERVER_UPDATE` with a new token, and a close on the old socket. Both are unverified.

## The recorder's own gateway session reconnecting

### What the logs show

Nothing with a call in progress. The only recorder gateway sequence on record is a token reset with no recording running. On `smoke-e75355c`, discordgo logged `close 4003: Not authenticated`, its resume got opcode 9 Invalid Session, and `close 4004: Authentication failed` came 1.4 s after the 4003 ([the smoke pass on fix(gateway): stop a session whose token Discord refuses, and report it (#729)](https://github.com/7Cav/cavbot2/pull/729#issuecomment-6179820439), check 1). That says nothing about a voice connection. A smoke run can't take a recorder's gateway down, as [fix(voice): a dropped voice connection starts its gap at once, and the recorder rejoins instead of resuming (#781)](https://github.com/7Cav/cavbot2/pull/781) notes under "Not reachable live".

### What the docs say

Only 4014's text touches this: a voice client is disconnected when "the main gateway session was dropped". The main gateway's resume section never mentions voice ([gateway.mdx lines 248 to 267](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway.mdx#L248-L267)). No text says whether a main gateway resume keeps the voice session, or whether a new identify, with its new `session_id`, ends it, and with which close.

I'd guess a resume keeps voice up, and a new identify is the "session was dropped" case, which means a 4014 and a fresh join. The docs state neither.

## A leave followed by a join

### What the logs show

No timed sequence. A `/record start` in another channel 53 s after a stop joined and recorded normally ([A recorder never reaches the new voice server when Discord names it before the rejoin starts (#733)](https://github.com/7Cav/cavbot2/issues/733), `smoke-e75355c`), and so did one 2.8 minutes after a lost recording ([A recording stops when its call moves to a voice server whose hostname doesn't resolve yet (#736)](https://github.com/7Cav/cavbot2/issues/736), `smoke-df64b83`). Neither thread times the leave's answer or the join's.

The leave-and-join path that [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809) added, for a voice server that refuses the held token, has never run live. Its smoke checks call it unreachable on demand.

The join half is better known. A fresh join gets its `VOICE_SERVER_UPDATE` within 0.5 s, from six joins on `smoke-c5f9692` in [A recorder whose voice connection drops on the same voice server never reconnects (#786)](https://github.com/7Cav/cavbot2/issues/786), while a join into the channel the recorder is already in gets the voice state alone.

### What the docs say

Opcode 4 with a null `channel_id` disconnects ([gateway-events.mdx lines 288 to 314](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway-events.mdx#L288-L314)). A join gets both events, and a client "should never cache or save" what they carry ([voice-connections.mdx lines 38 to 55](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L38-L55)). A bot joining a full channel gets neither event ([lines 77 to 79](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L77-L79)), which a leave and join would hit if a member took the seat in between. The docs don't say how soon after a leave a join gets a new token.

## A join into a call where members are already talking

Every smoke recording starts this way. The starter is in the channel with audio playing into their mic when the recorder joins.

### What the logs show

- The join's `VOICE_SERVER_UPDATE` came within 0.5 s of `opening voice conn` in all six recordings on `smoke-c5f9692` ([A recorder whose voice connection drops on the same voice server never reconnects (#786)](https://github.com/7Cav/cavbot2/issues/786)). No thread gives the order of the voice state and the voice server on a first join.
- On `smoke-e75355c`, every DAVE setup that finished did so within 0.7 s of the session description, first joins included ([the triage comment on A recorder can hear nobody after its call moves to another voice server, and nothing reports it (#732)](https://github.com/7Cav/cavbot2/issues/732#issuecomment-6180413515)).
- On `smoke-df64b83`, recording 44's first join stalled ([the smoke pass on fix(voice): start DAVE over when its setup stalls mid-recording, and report it (#734)](https://github.com/7Cav/cavbot2/pull/734#issuecomment-6183157755)). The recorder committed its MLS proposals, nothing answered, and it logged `DAVE setup stalled` 3.3 s later. It closed its gateway, identified again 0.34 s after that, and logged `epoch activated` 0.78 s after the stall line. The track is silent for its first 3.9 s.

Nobody has written down when the server sends opcodes 11 and 5 and the DAVE opcodes to a recorder joining people already in the call. The bot log at DEBUG has it, so the capture ticket can take it from there.

A related case runs the other way, a member joining a channel the recorder is already in and speaking at once. On `smoke-98750e0` that member's sound started in their track 0.3 to 0.8 s after each of 5 joins ([the smoke pass on feat(voice): one track per speaker (#573)](https://github.com/7Cav/cavbot2/pull/573#issuecomment-6176551167), check 6).

### What the docs say

The order a joining client sees, where the docs give one:

1. Hello, opcode 8. The current page doesn't say when it arrives; only the heartbeat section mentions it ([voice-connections.mdx line 140](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L140)).
2. Ready, opcode 2, after the client's Identify with `max_dave_protocol_version` ([lines 84 to 121](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L84-L121)).
3. Session Description, opcode 4, with `dave_protocol_version`, after the client's Select Protocol ([lines 208 to 225](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L208-L225) and [438 to 452](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L438-L452)).
4. Opcode 25, the MLS external sender, which "may be sent immediately on voice gateway connection or at a later time" ([line 300](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L300)). The whitepaper says it comes before the member is added ([protocol.md line 158](https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md#L158)).
5. The client sends its key package, opcode 26. Members already in the call get opcode 27 add proposals and answer with opcode 28.
6. The joiner gets opcode 30 Welcome, and joining "always occurs after receiving" it, except for the call's first group ([line 306](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L306)).
7. The joiner sends opcode 23, and the gateway sends opcode 22 to execute the transition ([lines 268 and 292](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L268)).

Opcode 11 says clients have connected, and members refuse an add proposal for a user not seen in an earlier opcode 11 ([protocol.md line 224](https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md#L224)). The docs don't say where opcode 11 and the server's opcode 5 Speaking fall against the steps above.

## Does Discord accept the voice token the recorder held?

### What the logs show

Yes, in every case on record, and no log shows a refusal. Three kinds of reuse:

1. **A second Identify with the same session, on the same server.** Recording 44's first join on `smoke-df64b83` ([the smoke pass on fix(voice): start DAVE over when its setup stalls mid-recording, and report it (#734)](https://github.com/7Cav/cavbot2/pull/734#issuecomment-6183157755)). The recorder closed its own voice gateway, with code 1012 per the PR's summary, and identified 0.34 s later with the session ID and token it already held, sending no voice state update. DAVE was set up 0.44 s after that Identify, and the speaker's audio came back. The PR's Merge Danger names this reuse as the thing nobody had tried. By my estimate the token was about 4 s old, since the track is silent for its first 3.9 s and a fresh join's `VOICE_SERVER_UPDATE` comes within 0.5 s.
2. **A token issued before a rejoin.** The 8 Order B moves on `smoke-df64b83` ([the smoke pass on fix(voice): follow a moved call whose server Discord named before the rejoin (#735)](https://github.com/7Cav/cavbot2/pull/735#issuecomment-6183157450)). Discord named the new server 0.11 to 0.19 s after the 4022. The recorder then sent its rejoin, Discord answered with the voice state alone, and the recorder identified with the earlier token 0.78 to 1.68 s after the 4022. Every one connected. So a voice state update for the same channel does not void the token Discord sent last.
3. **A token used 85 s after it was issued.** Check 2 on `smoke-c5f9692` ([the smoke pass on fix(voice): keep recording through a move to a voice server the recorder can't reach yet (#759)](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024)). The call moved at 22:49:29 and the smoke switch failed every lookup of the new server. When the maintainer lifted the switch, the recorder identified, and the gap notice turned ✅ at 22:50:55, after 1m26s. The token had never been used, since no dial got through. The thread doesn't list every `VOICE_SERVER_UPDATE` in the gap, so if Discord sent another one, the token was younger than 85 s.

What the logs don't show:

- An Identify with the held token after a real drop, where the voice server lost the connection without a clean close. That is the path [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809) takes. Its smoke checks record which way each reconnect goes, held token or leave and join.
- A refusal of any kind, so nobody knows which close Discord sends for a token it no longer takes. The recorder treats 4004, 4006 and 4009 before Ready as a refusal, as `refusesIdentify` in `voice/gateway.go` says.
- Any upper limit. The oldest token accepted was about 85 s old, and that one had never been used.

Since Discord answers a rejoin into the same channel with no `VOICE_SERVER_UPDATE`, the held token is the only one a recorder has after a drop on the same server. Getting a new one takes a leave and a join, or a move.

### What the docs say

Nothing about how long a voice token lasts, or about using one for a new Identify after a websocket drop. The `token` field reads only "Voice connection token" ([gateway-events.mdx line 1553](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway-events.mdx#L1553)). What the docs do say:

- Resume carries `token`, so reusing it for a resume is documented ([voice-connections.mdx lines 511 to 537](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L511-L537)).
- A failed resume sends the client back through the Connecting flow, which starts at opcode 4 ([line 551](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L551)).
- A channel change gets a new token, and the previous session can't be reused ([line 75](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L75)).
- Clients "should never cache or save" the results of opcode 4 ([line 38](https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L38)).

Read together, the docs expect a client that can't resume to ask for a new token, and they don't say an old one is refused. The logs show the old one working for at least 85 s.

## Scenarios with no recorded sequence

The capture ticket, [Capture the missing Discord voice sequences on the test guild (#816)](https://github.com/7Cav/cavbot2/issues/816), works from this list. Each entry says what is missing.

1. **Closes 4006, 4009 and 4015.** Nothing at all. Discord sends them on its own schedule, so the capture can only watch for them.
2. **A moderator disconnecting the recorder.** The outcome is known. Nobody has logged whether the voice socket gets 4014, the `VOICE_STATE_UPDATE` with a null channel, their order, or the gap between them.
3. **A moderator moving the recorder to another channel.** Nothing at all.
4. **The recorder's own gateway session reconnecting during a call.** Nothing at all, for a resume or for a new identify. A smoke run can't do this today.
5. **A leave followed by a join.** Nobody has timed the leave's `VOICE_STATE_UPDATE` and the join's two events after it, or checked whether the new token differs from the old.
6. **A real drop on the same voice server, abrupt and silent.** Discord's answer to the rejoin is known. Nobody has logged what the voice server sends, if anything, when the network drops rather than the bot, or whether it takes the held token on the Identify after. The second half is the smoke pass still owed on [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809), and it also needs the close code if the server refuses.
7. **A join into a call where members are already talking.** Timings are known. Nobody has logged the order of the voice state and the voice server on a first join, or the voice gateway opcode order from Hello through opcodes 11, 5 and the DAVE opcodes to `epoch activated`.
8. **A `VOICE_SERVER_UPDATE` with a null endpoint.** Discord's docs describe it, the code handles it, and no thread logs one.

The 4022 move needs no new capture. A move whose rejoin answer brings a second `VOICE_SERVER_UPDATE` after an early one would test the first guess under the move section, if a capture run happens to catch one.
