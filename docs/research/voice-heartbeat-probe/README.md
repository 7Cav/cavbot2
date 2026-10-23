# Voice heartbeats, keepalives, RTCP reports and a server deafen on the test guild

Six probe runs for [Wayfinder map: a recording stops when its recorder cannot hear (#810)](https://github.com/7Cav/cavbot2/issues/810). The first two are for [Measure voice heartbeat answers and keepalive echoes on the test guild (#830)](https://github.com/7Cav/cavbot2/issues/830). The next two are for [Do the voice server's RTCP reports keep coming while members talk and without the keepalive? (#837)](https://github.com/7Cav/cavbot2/issues/837). Those four feed [How soon does the recorder count a silent voice server as a drop? (#831)](https://github.com/7Cav/cavbot2/issues/831). The last two are for [Capture what a moderator's server deafen does to the recorder's audio (#844)](https://github.com/7Cav/cavbot2/issues/844). [voice-silent-drop-detection.md](https://github.com/7Cav/cavbot2/blob/research/voice-silent-drop-detection/docs/research/voice-silent-drop-detection.md) has the facts gathered before them.

The probe is [tools/voiceprobe/main.go](../../../tools/voiceprobe/main.go) on this branch. It logs in as recorder 1 from the main checkout's `.env` and joins one test guild voice channel, muted and not deafened. It identifies on the voice server with dave-go and runs IP discovery the way the recorder does. It holds the smoke run lock while it runs. Nobody else was in the channel during the first three runs. In the fourth, a person joined from their own Discord account. In the last two, that person was in the channel before the probe joined, with music playing into their mic.

## What the first two runs show

1. **The voice server answered every heartbeat, at 5 s and at 1 s.** In the 35-minute run it answered all 359 heartbeats sent every 5 s and all 299 sent every 1 s. Round trips ran 2.6 to 8.5 ms, with a median of 3.1 ms in both phases. 22 of the 658 took over 5 ms.
2. **Heartbeats every 1 s drew no close.** Five minutes at 60 a minute brought no 4021 and no other close. Hello named 13750 ms, so that is almost 14 heartbeats per interval.
3. **The voice server echoed no keepalive.** The probe sent the recorder's 8-byte keepalive 421 times, and none came back. No 8-byte datagram arrived at all.
4. **The voice server sends an RTCP receiver report every second.** A 52-byte datagram with RTCP packet type 201 came from the media address every 997 to 1005 ms, starting 1 s after Select Protocol and running to the end. That made 2,100 in 35 minutes, and no gap reached 1.5 s. They came with nobody else in the channel and no RTP at all, so the UDP socket isn't silent in a quiet channel. Each report names the probe's own SSRC from Ready as its sender. The current recorder's `readRTP` drops them unread, since their second byte isn't `0x78`.
5. **Nothing else came on the UDP socket.** Apart from the IP discovery answer, every datagram was one of those reports.
6. **No MLS group formed while the probe was alone.** The voice server sent opcode 25 and no proposals, so dave-go never reported ready. The server kept the connection up for 35 minutes all the same.

## What runs 03 and 04 show

1. **The receiver reports don't need the keepalive.** In `03-alone-no-keepalive.log` the probe sat alone in the channel for 12 minutes and sent nothing on its UDP socket after IP discovery. The voice server sent 722 receiver reports anyway, every 998.9 to 1003.8 ms. IP discovery gave back the UDP socket's own address, so no NAT on the probe's side held a mapping that could expire.
2. **A member talking changes nothing in the receiver reports.** In `04-member-talking.log` a person joined 14 s after the probe and talked. Their RTP came without a break for 11 minutes 16 s, about 50 datagrams a second from one SSRC, 34,188 in all. Then they muted for 1 minute 55 s and left, and the probe stayed alone for 1 minute 38 s more. It sent its keepalive every 5 s throughout. The 52-byte receiver report naming the probe's own SSRC came every 998.6 to 1003.2 ms for the whole run, 902 of them. 676 of its gaps had RTP in them, and no gap reached 1.5 s. dave-go reported ready 0.35 s after the person joined, so an MLS group was in place from then on.
3. **The voice server also sends a sender report for each member's stream.** A 72-byte datagram of RTCP type 200 came every 998.6 to 1003.9 ms, naming the SSRC that opcode 5 gave the person. The first came 0.37 s after their first RTP, and the last 0.7 s before opcode 13 said they had left, 790 in all. It kept coming while they were muted and sent no RTP, 114 of them in that time. The current recorder drops these unread too.
4. **One heartbeat answer was slow while the member talked.** All 179 heartbeats sent every 5 s got an answer, with a median of 4.4 ms. The one sent at 14:36:09 took 228.2 ms. RTP and both reports kept their pace through it.
5. **Nothing else came on the UDP socket.** Apart from the IP discovery answer, every datagram was the person's RTP or one of the two reports.

## What runs 05 and 06 show

The bot changed the recorder's server deafen with `PATCH /guilds/{guild}/members/{recorder}`, and Discord answered 200 each time. The music kept the member's RTP flowing, so the probe logged no `RTP stopped` apart from the deafens.

1. **A server deafen stops the member's RTP at the recorder.** In `05-deafen-mid-call.log` the member's RTP came for 2 minutes without a break, about 50 datagrams a second. The bot sent the deafen at 22:22:12.557. The recorder's voice state said `deaf=true` at 22:22:12.817, and the member's last RTP datagram came in that same millisecond. None came for the next 122.1 s, until the undeafen. The second deafen went the same way. Its voice state came at 22:26:13.515 and the last RTP 64 ms later, and no RTP came in the 62 s before the probe left.
2. **The member's sender reports stop with the RTP, and the receiver reports don't.** The 72-byte sender report for the member's SSRC came every second up to 22:22:12.231, then not again until 22:24:15.357, 0.44 s after the RTP came back. After the second deafen none came. The 52-byte receiver report naming the probe's own SSRC kept its pace through every change, 422 of them 997.8 to 1005.2 ms apart, and no gap reached 1.5 s. A check that counts any datagram from the voice server as hearing it would never start a gap for a deafened recorder.
3. **The voice gateway sends nothing at a deafen or an undeafen.** Once DAVE was ready, the voice server sent only heartbeat answers in both runs. No opcode 5, no opcode 13 and no DAVE opcode came at any of the four changes. Apart from the media stopping, the voice state on the recorder's own gateway session is the only sign of a deafen.
4. **The voice state and the media change together.** At the two deafens the last RTP came 0 and 64 ms after the voice state. At the two undeafens the RTP came back first, 432 and 21 ms before the voice state said `deaf=false`. Three times the voice state came 22 to 54 ms before Discord answered the bot's request, and once 347 ms after. The late one was the undeafen in `05`, whose request took 2,441.8 ms to answer.
5. **Discord keeps a server deafen after the recorder leaves.** Run 05 left the recorder deafened, and its last voice state, with no channel, still said `deaf=true`. In `06-join-deafened.log` it joined 2 minutes 51 s later, and the join's first voice state said `deaf=true`. The rest of the join looked like any other. Opcode 11 named the member, opcode 5 gave their SSRC with `speaking=1`, and dave-go reported ready 451 ms after the voice gateway opened. Then for 2 minutes no RTP and no sender report came, while the receiver reports kept their 1 s pace. The first RTP came 373 ms after the bot sent the undeafen, and the first sender report 0.47 s after that.
6. **Heartbeat answers were slower in these two runs.** All 130 heartbeats got an answer, in 106.2 to 112.5 ms, where runs 02 and 04 had medians of 3.1 and 4.4 ms. The log redacts the endpoint, so it can't tell a different voice server from a different route.

## What the runs don't show

- Whether a member's RTP keeps coming without the keepalive. The run without it was alone, and the run with a member sent it. When discord.js stopped tracking echoes in 2023, it kept sending the keepalive, saying voice receivers need it, in [discordjs/discord.js#9202](https://redirect.github.com/discordjs/discord.js/pull/9202).
- What the reports do when a voice connection drops silently. No run had a drop.
- How the reports and a deafen behave with more than one member, or two talking at once. Runs 04 to 06 had one member each.
- What the voice server sends a deafened recorder when a member joins or leaves. Nobody came or went while it was deafened.
- What a server mute of the recorder does, and whether a moderator's click in the Discord client sends anything the bot's request doesn't. The runs only deafened, and only through the bot.
- What the reports carry past their first 8 bytes. The rest is encrypted, and the probe doesn't decrypt it.
- How other voice servers and other hosts behave. Each run used one voice server, from one host whose Cloudflare colo is HEL. The probe ran on the host itself, outside the Docker NAT that the bot's container sends through.

## The files

| File | Run | Probe |
|---|---|---|
| [01-trial.log](01-trial.log) | 2026-10-19 23:33:50 to 23:34:34 UTC. Heartbeats every 5 s for 30 s, then every 1 s for 10 s. | An earlier build, whose RTCP lines lack `rtcp_type` and `since_last_rtcp_ms`. Otherwise the same as `eb0f40a`. |
| [02-run.log](02-run.log) | 2026-10-19 23:35:17 to 2026-10-20 00:10:21 UTC. Heartbeats every 5 s for 30 minutes, then every 1 s for 5 minutes. | `eb0f40a` |
| [03-alone-no-keepalive.log](03-alone-no-keepalive.log) | 2026-10-20 14:06:36 to 14:18:40 UTC. Alone with no keepalive. Heartbeats every 5 s for 12 minutes. | `c5127bc` |
| [04-member-talking.log](04-member-talking.log) | 2026-10-20 14:31:38 to 14:46:42 UTC. One member talking, then muted, then gone. Keepalive and heartbeats every 5 s for 15 minutes. | `c5127bc` |
| [05-deafen-mid-call.log](05-deafen-mid-call.log) | 2026-10-23 22:20:12 to 22:27:15 UTC. One member playing music. The bot deafened the recorder at 2 minutes, undeafened it at 4 and deafened it again at 6. Keepalive and heartbeats every 5 s for 7 minutes. | `f1c05e4` |
| [06-join-deafened.log](06-join-deafened.log) | 2026-10-23 22:30:06 to 22:34:10 UTC. The recorder joined still deafened from `05`, with the same member playing music, and the bot undeafened it at 2 minutes. Keepalive and heartbeats every 5 s for 4 minutes. | `f1c05e4` |

The runs after the trial were:

```bash
go run ./tools/voiceprobe -channel <channel ID>                                                              # 02
go run ./tools/voiceprobe -channel <channel ID> -slow-for 12m -fast-for 0 -keepalive 0                       # 03
go run ./tools/voiceprobe -channel <channel ID> -slow-for 15m -fast-for 0                                    # 04
go run ./tools/voiceprobe -channel <channel ID> -slow-for 7m -fast-for 0 -deafen '2m=true,4m=false,6m=true'  # 05
go run ./tools/voiceprobe -channel <channel ID> -slow-for 4m -fast-for 0 -deafen '2m=false'                  # 06
```

## How to read them

Each line is the probe's slog text output, with `time` in UTC to the millisecond.

- `heartbeat answered` gives the phase, the heartbeat's number in its phase, and `rtt_ms` from the send to the answer. Opcode 3 carried `t`, the send time in Unix milliseconds, and `seq_ack`, the last `seq` the server sent. A heartbeat with no answer by the time the next went out would log `heartbeat still unanswered as the next goes out`, and none did.
- `non-RTP datagram` gives a datagram's length and its first 16 bytes in hex. Bytes 4 to 8 of an RTCP datagram are the SSRC it names. In `02-run.log`, `since_last_rtcp_ms` is the time since the RTCP datagram before it. From `03` on it is the time since the last RTCP datagram of the same type, and `rtp_since_last_rtcp` counts the RTP that came in between.
- `UDP tally`, from `03` on, comes every 10 s. It counts the RTP and RTCP datagrams since the tally before and gives the time since the last RTCP datagram.
- `first RTP datagram` comes once per SSRC from `03` on, with `rtp_so_far` the RTP count by then. Opcode 5 lines in those files name the member and their SSRC.
- From `f1c05e4` on, `RTP stopped` says an SSRC's RTP has stopped for `-rtp-gap`, 500 ms by default, and `last_rtp` gives the last datagram's time. `RTP resumes` gives the gap when it comes back. A member who mutes or goes quiet logs them too.
- From `f1c05e4` on too, `gateway VOICE_STATE_UPDATE` carries the recorder's `deaf`, `mute`, `self_deaf`, `self_mute` and `suppress`. `sending server deafen` and `server deafen answered` are the bot's `PATCH /guilds/{guild}/members/{recorder}` from `-deafen`, with its `at` after the first heartbeat phase started, the HTTP status and the round trip.
- `IP discovery answered`, from `03` on, says in `same_as_local_address` whether the address the voice server saw is the UDP socket's own.
- `voice gateway message` is anything else the voice server sent, by opcode. `voice gateway send` is anything the probe sent apart from heartbeats.
- The keepalive is the recorder's own, from `voice/udp.go`. It is a little-endian 32-bit counter from 0 followed by four zero bytes, sent at once and every 5 s after. An echo would log `keepalive echoed`. `keepalive off` means the run sent none.
- The lines at the end sum up each phase, the keepalives, and the datagrams by length. From `03` on they sum up each RTCP type's gaps twice, once for all of them and once for those with RTP in them.

### Redaction

The probe writes placeholders itself, so no token, session ID, user ID, channel ID, host or address reaches the log. A placeholder stands for one value through one file, and each file numbers from 1 again. `<recorder>` is the recorder's user ID, `<user-1>` the person in `04`, `05` and `06`, `<channel-A>` the channel, `<session-1>` the recorder's main gateway session, `<token-1>` the voice token and `<voice-server-1>` the endpoint. `<ip-1>` is the voice server's media address and `<ip-2>` the bot host's, as IP discovery gave it. The secret key is `<secret-key>`. Before each commit, a search of this folder found no IPv4 address, no `"token":"`, and no number 17 to 20 digits long outside `first_bytes`. Inside it, 31 receiver reports in `06-join-deafened.log` match. Their bytes start `81c9000700000646`, which holds the recorder's SSRC 1606 in hex, and the digits run on into the encrypted bytes after it. None of them holds the guild, channel, recorder or bot ID. A search for 32-character hex strings found only `first_bytes` values, which are datagram bytes.
