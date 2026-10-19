# Gateway session reconnects captured on the test guild

Captures for [Capture the recorder's own gateway session reconnecting during a call (#821)](https://github.com/7Cav/cavbot2/issues/821), run from [its checklist](https://github.com/7Cav/cavbot2/issues/821#issuecomment-6278286818). They feed [Wayfinder map: owned voice connection for recorders (#810)](https://github.com/7Cav/cavbot2/issues/810), whose state table takes them as test fixtures. [The voice captures](https://github.com/7Cav/cavbot2/tree/research/voice-captures/docs/research/voice-captures) from [Capture the missing Discord voice sequences on the test guild (#816)](https://github.com/7Cav/cavbot2/issues/816) cover the drops, moves and joins on the voice side.

One smoke pass on build `smoke-6c3f925`, this branch at `6c3f925` with a clean tree, on 2026-10-19 from 18:45 to 18:52 EDT, UTC-4. The build is `develop` at `68c86ca` plus the smoke hook `SMOKE_GATEWAY_RECONNECT_FILE`, which makes every recorder's gateway session close and log in again. A file saying `resume` closes with 1012 and the login resumes. One saying `identify` closes with 1000, which ends the session at Discord, so the login after it identifies anew. Two recorders were connected, and recorder 1 recorded both times. Recorder 2 sat in no call and reconnected beside recorder 1 at every step. The member sat in channel A with music playing into their mic.

## What the captures show

1. **A resume changes nothing on the voice side.** Recorder 1's gateway session resumed after 1 s offline and after 45 s offline, and its voice connection heard nothing of either. The voice server answered every voice heartbeat, one every 13.75 s, while the gateway session was gone, and Discord took the resume 46.3 s after the close. Step 8's voice connection got its heartbeat answered 3 s into a 30 s outage too, until the moderator took the recorder out.
2. **Ending the gateway session takes the recorder out of the call.** Discord removed recorder 1 from channel A 266 ms after its close with 1000, before the session logged in again. Only the bot's own session got that `VOICE_STATE_UPDATE` with a null channel. The recorder's old session was closing, and its new one got no voice state for it at all, only a `GUILD_CREATE` whose voice states left it out. The current runtime read the removal as the recorder leaving and stopped the recording.
3. **A moderator's disconnect reaches the voice socket as 4014 while the gateway session is offline.** With recorder 1's gateway session down, the API took it out of A. Its voice socket got close 4014, `Disconnected.`, 5 ms after the bot's session saw the voice state. The recorder's own copy of that voice state came 25 s later, on the resume.
4. **Discord replays what a session missed before `RESUMED`.** The resume after step 8's outage got the missed `VOICE_STATE_UPDATE` first and `RESUMED` 3 ms after it. discordgo warns that the first packet wasn't `READY` or `RESUMED`, and hands the event on anyway.
5. **A network blip that breaks both connections cost 3.2 s.** The voice drop came while recorder 1's gateway session was closing for its resume. The current recorder sends opcode 4 before it reconnects, and that waited for the gateway. It went out 2.0 s after the drop, 0.6 s after `RESUMED`. Discord answered with the voice state alone, and the voice server took the held token and the session ID from before. DAVE was back 3.2 s after the drop.
6. **Every voice connection to the call got the same `media_session_id`.** The first join, the reconnect in step 4 and the second join all saw `<media-session-1>`, so it names the call, not the connection.
7. **Discord chose no close of its own.** Besides step 8's 4014, no 4006, 4009, 4015 and no `VOICE_SERVER_UPDATE` with a null endpoint came.

Two things the captures can't show, because the current recorder acts first:

- Whether a voice server takes the held token while the gateway session it belongs to is offline. The current recorder sends opcode 4 before it identifies on the voice gateway, and opcode 4 waits for the gateway session.
- What a voice socket gets after the gateway session ends. In step 5 nothing came in the 101 ms before the runtime closed the socket itself.

## The files

| File | Step | What happens |
|---|---|---|
| [01-join.log](01-join.log) | 1 | `/record start` while the member's music plays |
| [02-resume-at-once.log](02-resume-at-once.log) | 2 | Gateway close with 1012, resume at once |
| [03-resume-after-45s-offline.log](03-resume-after-45s-offline.log) | 3 | Gateway close with 1012, 45 s offline, resume |
| [04-network-blip.log](04-network-blip.log) | 4 | Gateway close with 1012 and an abrupt voice drop together |
| [05-identify-at-once.log](05-identify-at-once.log) | 5 | Gateway close with 1000, Op 9, identify |
| [08-moderator-disconnect-while-offline.log](08-moderator-disconnect-while-offline.log) | 8 | A second `/record start`, then a gateway close with 1012, 30 s offline, with the API taking the recorder out 6 s in |

Steps 6 and 7 didn't run. Step 6 needed the voice connection from before step 5, and step 5 ended it. Step 7 asked whether Discord takes the recorder out of the call while its account has no gateway session. Step 5 answered it. Discord does so 266 ms after the close, before any login, so a longer time offline would add nothing. Step 9 found no close Discord chose on its own.

## How to read them

Each file holds the bot log lines of one step, in log order, in the bot's own slog text format. A file starts 2 s before the step's action and ends 2 s before the next step's. The last ends at shutdown.

The run set `LOG_LEVEL=DEBUG` and `DISCORDGO_LOG_LEVEL=DEBUG`. The lines kept are the ones the checklist names. disgo also logs `sending voice gateway command` with its payload, so the files show what the recorder sends on the voice gateway, heartbeats and Identify among them.

Every discordgo dispatch line is rewritten as `session= op= seq= type= data=`, with the payload trimmed. A voice state keeps its guild, channel, user, session ID and mute and deaf flags. A `READY` keeps its version, session ID, resume URL and user ID. A `GUILD_CREATE` keeps its voice states, and a Hello its heartbeat interval. Discord's `_trace` is left out.

Three gateway sessions log: the bot's, recorder 1's and recorder 2's. `session` names the one that logged each line. A `READY` names its user. Only recorder 1 was ever in a call, so only it got `VOICE_SERVER_UPDATE`. A recorder asks for guild and voice state events alone, so message and interaction events are the bot's. Each session gets one copy of each event, and numbers its dispatches on from its last. A connection's Hello and its `READY` or `RESUMED` name the same gateway node in `_trace`, which places each Hello and the resume or identify sent with it. discordgo's other lines take the session of the hook line in the same millisecond, or of the close a second after it. Where both recorders stood at the same number, `session` names both, as `recorder-1|recorder-2`.

`message=called` next to `opening voice conn` is recorder 1 sending gateway opcode 4. In step 4 the first `opening voice conn` has none, because the gateway session was closing and the update couldn't go out.

### Redaction

Each placeholder stands for one value through the whole run, so two equal placeholders were equal values.

- `<token-1>` is the voice token of the first join and `<token-2>` the second join's.
- `<session-1>` is recorder 1's first gateway session, and `<session-3>` its session after the identify. `<session-2>` is recorder 2's session after the identify, and `<session-4>` the member's. A voice state's `session_id` is the gateway session of that user, so a `READY` and a voice state naming one session share its placeholder.
- `<media-session-1>` is the `media_session_id` the voice server sends in its Session Description.
- `<recorder-1>`, `<recorder-2>`, `<member-1>`, `<guild>` and `<channel-A>` are user, guild and channel IDs. `<channel-A-hex>` is channel A's ID in hex, which DAVE uses as the MLS group ID.
- `<voice-server-1>` is channel A's voice server. `<ip-1>` is its media address, `<ip-2>` the bot host's, and `<ip-3>` and `<ip-4>` the two ends of the voice socket step 4 dropped.
- `secret_key` became `<secret-key>`, and each `resume_gateway_url` `<resume-gateway-url>`.
- A binary DAVE message became `<binary seq= op= len=>`, or `<binary op= len=>` when the recorder sent it, since MLS credentials carry user IDs. Hex values of MLS data became `<hex len=>`, counted in bytes.
- discordgo's `Identify Packet` lines, which carry the bot tokens, and its `First Packet` lines are left out. Its `Expected READY/RESUMED` lines keep only the opcode, number and type of what came instead.

## Each step

Times are seconds from the event named first in each table.

### Step 1: a join

The recorder sent opcode 4 at 18:45:35.338. The order matches the joins in the voice captures.

| Seconds | Event |
|---:|---|
| 0.000 | Recorder sends opcode 4 for channel A |
| 0.162 | `VOICE_STATE_UPDATE`, recorder in A with `<session-1>`. The bot's session gets it at 0.175 |
| 0.245 | `VOICE_SERVER_UPDATE`, `<token-1>`, `<voice-server-1>`. Recorder dials it |
| 0.633 | Hello, opcode 8. Recorder sends Identify, opcode 0 |
| 0.744 | Ready, opcode 2 |
| 0.851 | Recorder sends Select Protocol, opcode 1. Server sends opcodes 11, 5, 18 and 20 |
| 0.964 | Session Description, opcode 4 |
| 0.965 | Recorder sends its key package, opcode 26. Server sends 15, 25 and 27 |
| 0.969 | Recorder sends its commit, opcode 28 |
| 1.079 | Opcode 29, then `epoch activated` |

### Step 2: a resume at once

Recorder 1's close frame went out at 18:45:58.564.

| Seconds | Event |
|---:|---|
| 0.000 | Gateway close with 1012. Recorder 2's follows 30 ms later |
| 1.000 | discordgo closes the socket, emits Disconnect and dials the gateway |
| 1.204 | Hello. Recorder sends Resume |
| 1.365 | `RESUMED`, with no dispatch replayed |
| 4.909 | Voice heartbeat, answered 110 ms later, as are those at 18.658 and 32.408 |

Nothing reached the voice connection, and the recording got no gap.

### Step 3: a resume after 45 s offline

Recorder 1's close frame went out at 18:46:42.313.

| Seconds | Event |
|---:|---|
| 0.000 | Gateway close with 1012 |
| 1.000 | discordgo closes the socket |
| 2.410 | Voice heartbeat, answered 111 ms later. The ones at 16.160, 29.910 and 43.659 are answered too |
| 46.001 | The hook's 45 s are up. discordgo dials the gateway |
| 46.183 | Hello. Recorder sends Resume |
| 46.329 | `RESUMED`, with no dispatch replayed |
| 57.409 | Next voice heartbeat, answered |

The recording got no gap. The gateway watch reports a session offline after 60 s, so it stayed quiet.

### Step 4: a network blip

Recorder 1's close frame went out at 18:48:09.063, and the voice drop came 33 ms later.

| Seconds | Event |
|---:|---|
| 0.000 | Gateway close with 1012 |
| 0.033 | Voice drop. disgo's read fails with a reset. The recorder closes the voice socket, and refuses disgo's resume |
| 0.034 | `opening voice conn`. Opcode 4 can't go out while the gateway session closes |
| 1.000 | discordgo closes the gateway socket |
| 1.201 | Hello. Recorder sends Resume |
| 1.361 | `RESUMED`, with no dispatch replayed |
| 2.002 | Recorder sends opcode 4 for channel A |
| 2.182 | `VOICE_STATE_UPDATE`, recorder in A with `<session-1>`. No `VOICE_SERVER_UPDATE` follows |
| 2.432 | Recorder reconnects with the token it held, after waiting 250 ms for a voice server |
| 2.784 | Hello. Identify with `<token-1>` and `<session-1>` |
| 2.894 | Ready |
| 3.229 | `epoch activated` |

No gap notice posted, since the gap lasted 3.2 s.

### Step 5: an identify at once

Recorder 1's close frame went out at 18:48:32.063.

| Seconds | Event |
|---:|---|
| 0.000 | Gateway close with 1000. Recorder 2's follows 31 ms later |
| 0.266 | The bot's session gets a `VOICE_STATE_UPDATE` with recorder 1 in no channel, for `<session-1>` |
| 0.367 | The runtime takes recorder 1 out of the call, and its voice connection is removed. Nothing reached the voice socket before then |
| 1.000 | discordgo closes the gateway socket |
| 1.209 | Hello. Recorder sends Resume for `<session-1>` |
| 1.368 | Op 9 with `false`. discordgo identifies at once |
| 1.481 | `Recording stopped`, `recorder_left` |
| 1.686 | `READY` with `<session-3>`. Recorder 2's came at 1.673 with `<session-2>` |
| 1.687 | `GUILD_CREATE`. Its voice states hold the member in A, and no recorder |

### Step 8: a moderator's disconnect while the recorder is offline

A second `/record start` came first. The recorder sent opcode 4 at 18:50:31.758, and the voice state named `<session-3>`. The voice server came at 0.307 with `<token-2>`, Identify went out at 0.658, and `epoch activated` came at 1.097.

Recorder 1's close frame went out at 18:50:56.563.

| Seconds | Event |
|---:|---|
| 0.000 | Gateway close with 1012 |
| 1.000 | discordgo closes the socket |
| 3.354 | Voice heartbeat, answered 108 ms later |
| 6.103 | The bot's session gets a `VOICE_STATE_UPDATE` with recorder 1 in no channel, for `<session-3>`. The API call returned 34 ms later |
| 6.108 | The voice socket gets close 4014, `Disconnected.` disgo closes its side with 1012 and the voice connection is removed |
| 6.574 | `Recording stopped`, `recorder_left` |
| 31.001 | The hook's 30 s are up. discordgo dials the gateway |
| 31.188 | Hello. Recorder sends Resume |
| 31.334 | The missed `VOICE_STATE_UPDATE` comes, recorder 1 in no channel, for `<session-3>` |
| 31.337 | `RESUMED` |
