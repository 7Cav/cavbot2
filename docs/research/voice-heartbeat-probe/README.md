# Voice heartbeat answers and keepalive echoes on the test guild

Two probe runs for [Measure voice heartbeat answers and keepalive echoes on the test guild (#830)](https://github.com/7Cav/cavbot2/issues/830). They feed [How soon does the recorder count a silent voice server as a drop? (#831)](https://github.com/7Cav/cavbot2/issues/831) on [Wayfinder map: owned voice connection for recorders (#810)](https://github.com/7Cav/cavbot2/issues/810). [voice-silent-drop-detection.md](https://github.com/7Cav/cavbot2/blob/research/voice-silent-drop-detection/docs/research/voice-silent-drop-detection.md) has the facts gathered before them.

The probe is [tools/voiceprobe/main.go](../../../tools/voiceprobe/main.go) on this branch. It logs in as recorder 1 from the main checkout's `.env` and joins one test guild voice channel, muted and not deafened. It identifies on the voice server with dave-go and runs IP discovery the way the recorder does. It holds the smoke run lock while it runs. Nobody else was in the channel during either run.

## What the runs show

1. **The voice server answered every heartbeat, at 5 s and at 1 s.** In the 35-minute run it answered all 359 heartbeats sent every 5 s and all 299 sent every 1 s. Round trips ran 2.6 to 8.5 ms, with a median of 3.1 ms in both phases. 22 of the 658 took over 5 ms.
2. **Heartbeats every 1 s drew no close.** Five minutes at 60 a minute brought no 4021 and no other close. Hello named 13750 ms, so that is almost 14 heartbeats per interval.
3. **The voice server echoed no keepalive.** The probe sent the recorder's 8-byte keepalive 421 times, and none came back. No 8-byte datagram arrived at all.
4. **The voice server sends an RTCP receiver report every second.** A 52-byte datagram with RTCP packet type 201 came from the media address every 997 to 1005 ms, starting 1 s after Select Protocol and running to the end. That made 2,100 in 35 minutes, and no gap reached 1.5 s. They came with nobody else in the channel and no RTP at all, so the UDP socket isn't silent in a quiet channel. Each report names the probe's own SSRC from Ready as its sender. The current recorder's `readRTP` drops them unread, since their second byte isn't `0x78`.
5. **Nothing else came on the UDP socket.** Apart from the IP discovery answer, every datagram was one of those reports.

## What the runs don't show

- Whether the reports keep their 1 s pace while members talk. The probe was alone in the channel both times, and no RTP arrived.
- Whether the reports need the keepalive. The probe sent one every 5 s throughout. When discord.js stopped tracking echoes in 2023, it kept sending the keepalive, saying voice receivers need it ([discordjs/discord.js#9202](https://redirect.github.com/discordjs/discord.js/pull/9202)).
- What the reports carry past their first 8 bytes. The rest is encrypted, and the probe doesn't decrypt it.
- How other voice servers and other hosts behave. Each run used one voice server, from one host whose Cloudflare colo is HEL.
- How a server with an MLS group in place behaves. With the probe alone in the channel, the voice server sent opcode 25 and no proposals, so no group formed and dave-go never reported ready. The server kept the connection up for 35 minutes all the same.

## The files

| File | Run | Probe |
|---|---|---|
| [01-trial.log](01-trial.log) | 2026-10-19 23:33:50 to 23:34:34 UTC. Heartbeats every 5 s for 30 s, then every 1 s for 10 s. | An earlier build, whose RTCP lines lack `rtcp_type` and `since_last_rtcp_ms`. Otherwise the same as `eb0f40a`. |
| [02-run.log](02-run.log) | 2026-10-19 23:35:17 to 2026-10-20 00:10:21 UTC. Heartbeats every 5 s for 30 minutes, then every 1 s for 5 minutes. | `eb0f40a` |

The full run was:

```bash
go run ./tools/voiceprobe -channel <channel ID>
```

## How to read them

Each line is the probe's slog text output, with `time` in UTC to the millisecond.

- `heartbeat answered` gives the phase, the heartbeat's number in its phase, and `rtt_ms` from the send to the answer. Opcode 3 carried `t`, the send time in Unix milliseconds, and `seq_ack`, the last `seq` the server sent. A heartbeat with no answer by the time the next went out would log `heartbeat still unanswered as the next goes out`, and none did.
- `non-RTP datagram` gives a datagram's length and its first 16 bytes in hex. In `02-run.log`, `since_last_rtcp_ms` is the time since the RTCP datagram before it.
- `voice gateway message` is anything else the voice server sent, by opcode. `voice gateway send` is anything the probe sent apart from heartbeats.
- The keepalive is the recorder's own, from `voice/udp.go`. It is a little-endian 32-bit counter from 0 followed by four zero bytes, sent at once and every 5 s after. An echo would log `keepalive echoed`.
- The lines at the end sum up each phase, the keepalives, and the datagrams by length.

### Redaction

The probe writes placeholders itself, so no token, session ID, user ID, channel ID, host or address reaches the log. A placeholder stands for one value through one file, and each file numbers from 1 again. `<recorder>` is the recorder's user ID, `<channel-A>` the channel, `<session-1>` the recorder's main gateway session, `<token-1>` the voice token and `<voice-server-1>` the endpoint. `<ip-1>` is the voice server's media address and `<ip-2>` the bot host's, as IP discovery gave it. The secret key is `<secret-key>`. Before the commit, a search of this folder found no number 17 to 20 digits long, no IPv4 address and no `"token":"`. A search for 32-character hex strings found only `first_bytes` values, which are datagram bytes.
