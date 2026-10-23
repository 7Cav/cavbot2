# Voice heartbeats, keepalives and RTCP reports on the test guild

Four probe runs. The first two are for [Measure voice heartbeat answers and keepalive echoes on the test guild (#830)](https://github.com/7Cav/cavbot2/issues/830). The last two are for [Do the voice server's RTCP reports keep coming while members talk and without the keepalive? (#837)](https://github.com/7Cav/cavbot2/issues/837). They feed [How soon does the recorder count a silent voice server as a drop? (#831)](https://github.com/7Cav/cavbot2/issues/831) on [Wayfinder map: owned voice connection for recorders (#810)](https://github.com/7Cav/cavbot2/issues/810). [voice-silent-drop-detection.md](https://github.com/7Cav/cavbot2/blob/research/voice-silent-drop-detection/docs/research/voice-silent-drop-detection.md) has the facts gathered before them.

The probe is [tools/voiceprobe/main.go](../../../tools/voiceprobe/main.go) on this branch. It logs in as recorder 1 from the main checkout's `.env` and joins one test guild voice channel, muted and not deafened. It identifies on the voice server with dave-go and runs IP discovery the way the recorder does. It holds the smoke run lock while it runs. Nobody else was in the channel during the first three runs. In the fourth, a person joined from their own Discord account.

## What the first two runs show

1. **The voice server answered every heartbeat, at 5 s and at 1 s.** In the 35-minute run it answered all 359 heartbeats sent every 5 s and all 299 sent every 1 s. Round trips ran 2.6 to 8.5 ms, with a median of 3.1 ms in both phases. 22 of the 658 took over 5 ms.
2. **Heartbeats every 1 s drew no close.** Five minutes at 60 a minute brought no 4021 and no other close. Hello named 13750 ms, so that is almost 14 heartbeats per interval.
3. **The voice server echoed no keepalive.** The probe sent the recorder's 8-byte keepalive 421 times, and none came back. No 8-byte datagram arrived at all.
4. **The voice server sends an RTCP receiver report every second.** A 52-byte datagram with RTCP packet type 201 came from the media address every 997 to 1005 ms, starting 1 s after Select Protocol and running to the end. That made 2,100 in 35 minutes, and no gap reached 1.5 s. They came with nobody else in the channel and no RTP at all, so the UDP socket isn't silent in a quiet channel. Each report names the probe's own SSRC from Ready as its sender. The current recorder's `readRTP` drops them unread, since their second byte isn't `0x78`.
5. **Nothing else came on the UDP socket.** Apart from the IP discovery answer, every datagram was one of those reports.
6. **No MLS group formed while the probe was alone.** The voice server sent opcode 25 and no proposals, so dave-go never reported ready. The server kept the connection up for 35 minutes all the same.

## What the last two runs show

1. **The receiver reports don't need the keepalive.** In `03-alone-no-keepalive.log` the probe sat alone in the channel for 12 minutes and sent nothing on its UDP socket after IP discovery. The voice server sent 722 receiver reports anyway, every 998.9 to 1003.8 ms. IP discovery gave back the UDP socket's own address, so no NAT on the probe's side held a mapping that could expire.
2. **A member talking changes nothing in the receiver reports.** In `04-member-talking.log` a person joined 14 s after the probe and talked. Their RTP came without a break for 11 minutes 16 s, about 50 datagrams a second from one SSRC, 34,188 in all. Then they muted for 1 minute 55 s and left, and the probe stayed alone for 1 minute 38 s more. It sent its keepalive every 5 s throughout. The 52-byte receiver report naming the probe's own SSRC came every 998.6 to 1003.2 ms for the whole run, 902 of them. 676 of its gaps had RTP in them, and no gap reached 1.5 s. dave-go reported ready 0.35 s after the person joined, so an MLS group was in place from then on.
3. **The voice server also sends a sender report for each member's stream.** A 72-byte datagram of RTCP type 200 came every 998.6 to 1003.9 ms, naming the SSRC that opcode 5 gave the person. The first came 0.37 s after their first RTP, and the last 0.7 s before opcode 13 said they had left, 790 in all. It kept coming while they were muted and sent no RTP, 114 of them in that time. The current recorder drops these unread too.
4. **One heartbeat answer was slow while the member talked.** All 179 heartbeats sent every 5 s got an answer, with a median of 4.4 ms. The one sent at 14:36:09 took 228.2 ms. RTP and both reports kept their pace through it.
5. **Nothing else came on the UDP socket.** Apart from the IP discovery answer, every datagram was the person's RTP or one of the two reports.

## What the runs don't show

- Whether a member's RTP keeps coming without the keepalive. The run without it was alone, and the run with a member sent it. When discord.js stopped tracking echoes in 2023, it kept sending the keepalive, saying voice receivers need it, in [discordjs/discord.js#9202](https://redirect.github.com/discordjs/discord.js/pull/9202).
- What the reports do when a voice connection drops silently. No run had a drop.
- How the reports behave with more than one member, or two talking at once. The fourth run had one member.
- What the reports carry past their first 8 bytes. The rest is encrypted, and the probe doesn't decrypt it.
- How other voice servers and other hosts behave. Each run used one voice server, from one host whose Cloudflare colo is HEL. The probe ran on the host itself, outside the Docker NAT that the bot's container sends through.

## The files

| File | Run | Probe |
|---|---|---|
| [01-trial.log](01-trial.log) | 2026-10-19 23:33:50 to 23:34:34 UTC. Heartbeats every 5 s for 30 s, then every 1 s for 10 s. | An earlier build, whose RTCP lines lack `rtcp_type` and `since_last_rtcp_ms`. Otherwise the same as `eb0f40a`. |
| [02-run.log](02-run.log) | 2026-10-19 23:35:17 to 2026-10-20 00:10:21 UTC. Heartbeats every 5 s for 30 minutes, then every 1 s for 5 minutes. | `eb0f40a` |
| [03-alone-no-keepalive.log](03-alone-no-keepalive.log) | 2026-10-20 14:06:36 to 14:18:40 UTC. Alone with no keepalive. Heartbeats every 5 s for 12 minutes. | `c5127bc` |
| [04-member-talking.log](04-member-talking.log) | 2026-10-20 14:31:38 to 14:46:42 UTC. One member talking, then muted, then gone. Keepalive and heartbeats every 5 s for 15 minutes. | `c5127bc` |

The runs after the trial were:

```bash
go run ./tools/voiceprobe -channel <channel ID>                                         # 02
go run ./tools/voiceprobe -channel <channel ID> -slow-for 12m -fast-for 0 -keepalive 0  # 03
go run ./tools/voiceprobe -channel <channel ID> -slow-for 15m -fast-for 0               # 04
```

## How to read them

Each line is the probe's slog text output, with `time` in UTC to the millisecond.

- `heartbeat answered` gives the phase, the heartbeat's number in its phase, and `rtt_ms` from the send to the answer. Opcode 3 carried `t`, the send time in Unix milliseconds, and `seq_ack`, the last `seq` the server sent. A heartbeat with no answer by the time the next went out would log `heartbeat still unanswered as the next goes out`, and none did.
- `non-RTP datagram` gives a datagram's length and its first 16 bytes in hex. Bytes 4 to 8 of an RTCP datagram are the SSRC it names. In `02-run.log`, `since_last_rtcp_ms` is the time since the RTCP datagram before it. In `03` and `04` it is the time since the last RTCP datagram of the same type, and `rtp_since_last_rtcp` counts the RTP that came in between.
- `UDP tally`, in `03` and `04`, comes every 10 s. It counts the RTP and RTCP datagrams since the tally before and gives the time since the last RTCP datagram.
- `first RTP datagram` comes once per SSRC in `03` and `04`, with `rtp_so_far` the RTP count by then. Opcode 5 lines in those files name the member and their SSRC.
- From `f1c05e4` on, `RTP stopped` says an SSRC's RTP has stopped for `-rtp-gap`, 500 ms by default, and `last_rtp` gives the last datagram's time. `RTP resumes` gives the gap when it comes back. A member who mutes or goes quiet logs them too.
- From `f1c05e4` on too, `gateway VOICE_STATE_UPDATE` carries the recorder's `deaf`, `mute`, `self_deaf`, `self_mute` and `suppress`. `sending server deafen` and `server deafen answered` are the bot's `PATCH /guilds/{guild}/members/{recorder}` from `-deafen`, with its `at` after the first heartbeat phase started, the HTTP status and the round trip.
- `IP discovery answered` in `03` and `04` says in `same_as_local_address` whether the address the voice server saw is the UDP socket's own.
- `voice gateway message` is anything else the voice server sent, by opcode. `voice gateway send` is anything the probe sent apart from heartbeats.
- The keepalive is the recorder's own, from `voice/udp.go`. It is a little-endian 32-bit counter from 0 followed by four zero bytes, sent at once and every 5 s after. An echo would log `keepalive echoed`. `keepalive off` means the run sent none.
- The lines at the end sum up each phase, the keepalives, and the datagrams by length. In `03` and `04` they sum up each RTCP type's gaps twice, once for all of them and once for those with RTP in them.

### Redaction

The probe writes placeholders itself, so no token, session ID, user ID, channel ID, host or address reaches the log. A placeholder stands for one value through one file, and each file numbers from 1 again. `<recorder>` is the recorder's user ID, `<user-1>` the person in `04-member-talking.log`, `<channel-A>` the channel, `<session-1>` the recorder's main gateway session, `<token-1>` the voice token and `<voice-server-1>` the endpoint. `<ip-1>` is the voice server's media address and `<ip-2>` the bot host's, as IP discovery gave it. The secret key is `<secret-key>`. Before each commit, a search of this folder found no number 17 to 20 digits long, no IPv4 address and no `"token":"`. A search for 32-character hex strings found only `first_bytes` values, which are datagram bytes.
