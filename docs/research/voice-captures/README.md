# Discord voice sequences captured on the test guild

Captures for [Capture the missing Discord voice sequences on the test guild (#816)](https://github.com/7Cav/cavbot2/issues/816), run from [its checklist](https://github.com/7Cav/cavbot2/issues/816#issuecomment-6273603854). They feed [Wayfinder map: owned voice connection for recorders (#810)](https://github.com/7Cav/cavbot2/issues/810), whose state table takes them as test fixtures. [Collect the Discord voice event sequences we already know (#812)](https://github.com/7Cav/cavbot2/issues/812) listed the scenarios nobody had logged, and [its findings](https://github.com/7Cav/cavbot2/blob/research/discord-voice-sequences/docs/research/discord-voice-sequences.md) cover the rest.

One smoke pass on build `smoke-68c86ca`, which is `develop` at `68c86ca` with a clean tree, on 2026-10-19 from 13:57 to 14:12 EDT, UTC-4. Two recorders were connected, and every step used the first. The starter sat in channel A with music playing into their mic for the whole run. Their tracks hold sound everywhere except from each drop to the end of its gap.

## What the captures show

1. **Discord took the held token every time.** The recorder reconnected with the token from its first join five times, after outages of 1 s, 66 s, 13 s, 68 s and 5 min. The last Identify came 10m07s after Discord issued that token. Nothing was refused, so the close code for a refused token is still unknown.
2. **A silent drop gets nothing from Discord.** The main gateway said nothing about the recorder between the drop and the recorder noticing, which took 12.0 to 16.6 s. Through a 5-minute outage Discord kept the recorder in the channel and sent no voice state.
3. **A moderator's disconnect reaches the recorder as a voice state first.** The recorder's own copy of the `VOICE_STATE_UPDATE` with a null channel came 7 ms after the first copy, and in the same millisecond the current recorder closed its voice socket with 1000. Whether Discord would have followed with 4014 is still unknown, because the recorder closed first.
4. **A moderator's move sends the voice state, then a new voice server with the old token.** The `VOICE_STATE_UPDATE` naming channel B came first. 166 ms later Discord named a different voice server for B, with the same token the recorder held in A. The current recorder left 29 ms after the voice state, before its voice socket heard anything, and the recording ended as `recorder_left`.
5. **A leave then a join gets a new token from the same server.** The leave's voice state came 137 ms after the recorder's opcode 4. The join's voice state came 137 ms after its opcode 4 and the voice server 255 ms after it, with a new token. The main gateway session ID didn't change.
6. **A join into a call where someone is talking runs in about 1 s.** The voice state always came before the voice server, 53 to 118 ms apart. The recorder made the call's MLS group and committed it, so no Welcome came. The full opcode order is under step 1.
7. **One reconnect stalled in DAVE the way [Does the DAVE library cause the silence after a commit race? (#829)](https://github.com/7Cav/cavbot2/issues/829) describes.** In step 3 the recorder sent its commit and Discord never answered it. The stall recovery caught it 3.1 s later, and on the fresh connection Discord answered the commit in 107 ms.
8. **Discord sent none of the closes it chooses on its own.** No 4006, 4009, 4015, no `VOICE_SERVER_UPDATE` with a null endpoint, and no voice gateway close of any kind in the whole run.

## The files

| File | Step | What happens |
|---|---|---|
| [01-join-into-a-talking-call.log](01-join-into-a-talking-call.log) | 1 | `/record start` while the starter talks |
| [02-abrupt-drop-back-at-once.log](02-abrupt-drop-back-at-once.log) | 2 | Abrupt drop, held-token reconnect at once |
| [03-abrupt-drop-60s.log](03-abrupt-drop-60s.log) | 3 | Abrupt drop, voice server unreachable for 60 s, then a DAVE stall |
| [04-silent-drop-back-at-once.log](04-silent-drop-back-at-once.log) | 4 | Silent drop, held-token reconnect once the recorder notices |
| [05-silent-drop-60s.log](05-silent-drop-60s.log) | 5 | Silent drop, voice server unreachable for 60 s |
| [06-silent-drop-5min.log](06-silent-drop-5min.log) | 6 | Silent drop, voice server unreachable for 5 min |
| [08-leave-then-join.log](08-leave-then-join.log) | 8 | `/record stop`, then `/record start` |
| [09-moderator-move.log](09-moderator-move.log) | 9 | The API moves the recorder to channel B |
| [10-moderator-disconnect.log](10-moderator-disconnect.log) | 10 | A third join, then the API disconnects the recorder |

Step 7, a drop the voice server can't see, didn't run. It needs `sudo`, and `sudo` asks for a password on this machine. Step 11 found nothing to capture.

## How to read them

Each file holds the bot log lines of one step, in log order, in the bot's own slog text format. A file starts 2 s before the step's action, or 16 s before for a silent drop so it holds the voice server's last heartbeat answer, and ends 2 s before the next step's action. The silent-drop files overlap the file before them by those 14 s.

The run set `LOG_LEVEL=DEBUG` and `DISCORDGO_LOG_LEVEL=DEBUG`. The lines kept are the ones the checklist names. disgo also logs `sending voice gateway command` with its payload, so the files show what the recorder sends on the voice gateway too, opcodes 0, 1, 3, 26 and 28 among them.

Every line from discordgo's gateway dispatches is rewritten as `session= op= seq= type= data=`, with the event's payload trimmed. A voice state keeps its guild, channel, user, session ID and mute and deafen flags, and loses the member object. Three gateway sessions log the same voice state: the bot's, recorder 1's and recorder 2's. `session` names the one that logged the line, worked out by following each session's own `seq` count. Where two sessions stood at the same `seq`, the line could be either, and `session` names both, such as `recorder-1|recorder-2`.

discordgo's INFO `message=called` next to `opening voice conn` or `Removing voice conn` is the recorder sending gateway opcode 4. Right after a `VOICE_SERVER_UPDATE` it is discordgo's own handler for that event, and nothing goes out.

Discord sends two dispatch types discordgo doesn't know, `VOICE_CHANNEL_START_TIME_UPDATE` and `VOICE_CHANNEL_STATUS_UPDATE`, when a voice channel's first member arrives or its last one leaves. They came when the starter joined channel A before step 1, and for channel B in step 9. The files leave them out.

### Redaction

Each placeholder stands for one value through the whole run, so two equal placeholders were equal values. `<token-1>` is the voice token of the first join, `<token-2>` the second join's, and `<token-3>` the third's. `<session-1>` is recorder 1's main gateway session. `<recorder-1>`, `<recorder-2>`, `<member-1>`, `<bot>`, `<guild>`, `<channel-A>` and `<channel-B>` are user, guild and channel IDs, and `<channel-A-hex>` is channel A's ID in hex, which DAVE uses as the MLS group ID. `<voice-server-1>` is channel A's voice server and `<voice-server-2>` the one Discord named for channel B. `<ip-1>` is the voice server's media address and `<ip-2>` the bot host's. `secret_key` became `<secret-key>`. A binary DAVE message became `<binary seq= op= len=>`, or `<binary op= len=>` when the recorder sent it, since MLS credentials carry user IDs. Hex dumps of MLS data became `<hex len=>`, counted in bytes.

## Each step

Times are seconds from the event named first in each table.

### Step 1: a join into a call where someone is talking

The recorder sent opcode 4 at 13:58:16.132.

| Seconds | Event |
|---:|---|
| 0.000 | Recorder sends opcode 4 for channel A |
| 0.172 | `VOICE_STATE_UPDATE`, recorder in A |
| 0.247 | `VOICE_SERVER_UPDATE`, `<token-1>`, `<voice-server-1>`. Recorder dials it |
| 0.597 | Hello, opcode 8, `heartbeat_interval` 13750. Recorder sends Identify, opcode 0 |
| 0.707 | Ready, opcode 2 |
| 0.813 | Recorder sends Select Protocol, opcode 1. Server sends opcode 11 naming the starter, opcode 5 for their speaking, then 18 and 20 |
| 0.924 | Session Description, opcode 4, `dave_protocol_version` 1 |
| 0.925 | Recorder sends its key package, opcode 26. Server sends opcodes 25 and 27 |
| 0.930 | Recorder sends its commit, opcode 28 |
| 1.039 | Opcode 29, then `epoch activated` |

The joins in steps 8 and 10 ran in the same order, with the voice state 137 and 136 ms after opcode 4, the voice server 255 and 189 ms after it, and `epoch activated` at 1.058 and 0.981 s. Discord sent no opcode 30, 21, 22, 23 or 24 in any of the three, or in any reconnect. dave-go logged `activating epoch immediately for initial transition` every time, reconnects included.

### Step 2: an abrupt drop, back at once

The smoke switch dropped the voice connection at 13:58:46.574.

| Seconds | Event |
|---:|---|
| 0.000 | Drop |
| 0.001 | disgo's read fails with a reset. Recorder closes the voice socket with 1012 and sends opcode 4 for channel A, the one it is in |
| 0.166 | `VOICE_STATE_UPDATE`, recorder in A. No `VOICE_SERVER_UPDATE` follows |
| 0.417 | Recorder reconnects with the token it held, after waiting 250 ms for a voice server |
| 0.756 | Hello. Identify with `<token-1>`, 31 s old |
| 0.867 | Ready |
| 1.081 | Session Description |
| 1.195 | Opcode 29, `epoch activated` |

No gap notice posted, since the gap lasted 1.2 s.

### Step 3: an abrupt drop that lasts, then a DAVE stall

The drop came at 13:59:18.075, with the DNS switch on.

| Seconds | Event |
|---:|---|
| 0.000 | Drop. Recorder closes with 1012 and sends opcode 4 for channel A |
| 0.164 | `VOICE_STATE_UPDATE`, recorder in A |
| 0.416 | Held-token reconnect starts. Every dial fails at DNS, retried after 1, 2, 4, 8 and then every 10 s |
| 10.020 | Gap notice posts |
| 30.417 | `error opening voice gateway`, the open's 30 s limit |
| 59.766 | DNS switch off |
| 65.755 | Hello. Identify with `<token-1>`, 127 s old |
| 65.861 | Ready |
| 66.071 | Session Description. Opcodes 25 and 27 arrive |
| 66.077 | Recorder sends its commit, opcode 28. Nothing answers it |
| 69.165 | `DAVE setup stalled`. Recorder closes with 1012, "starting DAVE over", and dials again |
| 69.504 | Hello. Identify with `<token-1>` |
| 69.612 | Ready |
| 69.827 | Session Description. Recorder commits again |
| 69.936 | Opcode 29, `epoch activated` |
| 69.951 | Gap ends, 1m10s |

### Step 4: a silent drop, back at once

The drop came at 14:00:59.575, with the DNS switch off. The voice server's last heartbeat answer came 4.387 s before it, at 14:00:55.188.

| Seconds | Event |
|---:|---|
| -4.387 | Last heartbeat answer, opcode 6 |
| 0.000 | Drop. Nothing more reaches the recorder, and nothing about it comes over the main gateway |
| 12.364 | `heartbeat answer overdue`, 16.751 s after the last answer. Recorder closes with 1012 and sends opcode 4 for channel A |
| 12.527 | `VOICE_STATE_UPDATE`, recorder in A |
| 12.778 | Held-token reconnect starts |
| 12.866 | Gap notice posts, its start the last heartbeat answer |
| 13.116 | Hello. Identify with `<token-1>`, 176 s old |
| 13.229 | Ready |
| 13.443 | Session Description |
| 13.555 | Opcode 29, `epoch activated` |
| 13.570 | Gap ends, 18 s |

The smoke switch closes the old TCP connection when the recorder gives up, so the voice server saw it end at 12.364, about 0.75 s before the Identify. A real outage might not show it the end at all.

### Step 5: a silent drop that lasts

The drop came at 14:01:45.075, with the DNS switch on. The last heartbeat answer came at 14:01:40.302, 4.773 s before it.

| Seconds | Event |
|---:|---|
| 0.000 | Drop |
| 11.978 | `heartbeat answer overdue`, 16.751 s after the last answer. Opcode 4 for channel A |
| 12.135 | `VOICE_STATE_UPDATE`, recorder in A |
| 12.385 | Held-token reconnect starts. Dials fail at DNS |
| 12.479 | Gap notice posts |
| 42.386 | `error opening voice gateway`, the open's 30 s limit |
| 59.922 | DNS switch off |
| 67.736 | Hello. Identify with `<token-1>`, 276 s old |
| 67.846 | Ready |
| 68.064 | Session Description |
| 68.176 | Opcode 29, `epoch activated`. Gap ends, 1m13s |

### Step 6: a silent drop held 5 minutes

The drop came at 14:03:20.575, with the DNS switch on. The last heartbeat answer came 0.156 s before it.

| Seconds | Event |
|---:|---|
| 0.000 | Drop |
| 16.594 | `heartbeat answer overdue`. Opcode 4 for channel A |
| 16.744 | `VOICE_STATE_UPDATE`, recorder in A |
| 16.845 | Gap notice posts |
| 16.995 | Held-token reconnect starts. Dials fail at DNS |
| 46.996 | `error opening voice gateway` |
| 299.942 | DNS switch off |
| 302.359 | Hello. Identify with `<token-1>`, 606.6 s old |
| 302.466 | Ready |
| 302.677 | Session Description |
| 302.789 | Opcode 29, `epoch activated`. Gap ends, 5m3s |

From the voice state at 16.744 to the Identify, the main gateway sent nothing about the recorder, and Discord kept it in channel A.

### Step 8: a leave, then a join

The leave went out at 14:09:24.201, when `/record stop` ended recording 55.

| Seconds | Event |
|---:|---|
| 0.000 | Recorder sends opcode 4 with a null channel, and disgo drops the voice connection |
| 0.137 | `VOICE_STATE_UPDATE`, recorder in no channel |
| 0.395 | `Recording stopped`, `ended=stopped` |
| 8.138 | `/record start`. Recorder sends opcode 4 for channel A |
| 8.275 | `VOICE_STATE_UPDATE`, recorder in A, `<session-1>` as before |
| 8.393 | `VOICE_SERVER_UPDATE`, `<token-2>`, `<voice-server-1>` |
| 8.750 | Hello. Identify with `<token-2>` |
| 8.864 | Ready |
| 9.080 | Session Description |
| 9.196 | Opcode 29, `epoch activated` |

The 8 s between leave and join is Discord's slash command round trip. The recorder can leave and join again faster than a member can.

### Step 9: a moderator's move

`PATCH /guilds/{guild}/members/<recorder-1>` with `{"channel_id":"<channel-B>"}`, during recording 56. The first copy of the move's voice state came at 14:10:10.214.

| Seconds | Event |
|---:|---|
| 0.000 | `VOICE_STATE_UPDATE`, recorder in B, recorder 2's copy |
| 0.007 | The recorder's own copy |
| 0.029 | Recorder sends opcode 4 with a null channel, and disgo drops the voice connection. Nothing has come over it since the move |
| 0.164 | `VOICE_STATE_UPDATE`, recorder in no channel |
| 0.166 | `VOICE_SERVER_UPDATE`, `<token-2>`, the token it held in A, and `<voice-server-2>`, not A's server |
| 0.450 | `Recording stopped`, `ended=recorder_left` |

The voice server came after the recorder's leave went out, so Discord likely sent it for the move before the leave reached it.

### Step 10: a moderator's disconnect

`PATCH /guilds/{guild}/members/<recorder-1>` with `{"channel_id":null}`, during recording 57. The first copy of the voice state came at 14:11:23.970.

| Seconds | Event |
|---:|---|
| 0.000 | `VOICE_STATE_UPDATE`, recorder in no channel, recorder 2's copy |
| 0.007 | The recorder's own copy. In the same millisecond the recorder closes its voice socket with 1000, "Shutting down" |
| 0.018 | Recorder sends opcode 4 with a null channel |
| 0.369 | `Recording stopped`, `ended=recorder_left` |

No close came from the voice server before the recorder closed, and none could after.
