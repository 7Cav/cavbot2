# Noticing a silent voice drop sooner

Researched 2026-10-19. This file gathers facts for a design decision and makes no pick.

## The problem

A recorder's network path to the voice server can go dead without a TCP reset. Today the recorder declares a drop once the voice gateway's heartbeat answer is `heartbeat_interval` plus `heartbeatMargin`, 3 s, past the last answer it heard. [repo-overdue] [repo-margin] Drops on the test guild took 12.0 to 16.6 s to notice, with Hello naming 13750 ms. The recorder also sends an 8-byte keepalive on its media socket every 5 s and drops anything shorter than an RTP header that comes back. [repo-keepalive] [repo-readrtp] Silence on the media socket can't be the signal, because a quiet channel sends nothing.

The question is how soon a client of our own could notice. Five sub-questions follow, each with its own section.

## Answers in brief

1. Discord's voice docs only say to send op 3 "every elapsed interval" and that op 6 echoes the nonce. They say nothing about sending early, on demand, or how often is too often. Close code 4021 means "Disconnect due to rate limit exceeded. Should not reconnect" and gives no number. The main gateway docs allow an early heartbeat but ask apps to avoid it "unless necessary", and Discord answers each heartbeat with an ACK. In practice discord.py has heartbeated the voice gateway every 5 s since 2020. In this repo's stack, disgo closes the gateway when an ACK carries a nonce other than the one disgo sent last. [voice-hb] [ops-close] [gw-early] [dpy-hello] [disgo-ack]
2. Unknown for today. Discord documents no UDP keepalive. In March 2023 one user's logs showed one voice server echoing discord.js's 8-byte keepalive within 32 ms and another sending nothing back, and discord.js stopped tracking echoes. Before that it closed its UDP socket after 12 unanswered keepalives, 60 s. No maintained library examined tracks echoes now. [djs-9185-logs] [djs-pr-9202] [djs-old-limit]
3. Discord's docs don't mention WebSocket ping frames. A probe on 2026-10-19 sent 8 pings over two runs to a voice gateway host and got 8 pongs in 3 to 5 ms. The host sits behind Cloudflare, and the probe can't tell whether Cloudflare or the voice server answered. No library examined sends pings to the voice gateway.
4. No. None of discord.js, discord.py, songbird, JDA, koe or discordgo notices a dead voice connection faster than one missed heartbeat ACK. The quickest timer among them is JDA's socket read timeout of `heartbeat_interval` plus 10 s, 23.75 s at this repo's interval. The recorder's own interval plus 3 s is already tighter than every one of them.
5. Yes. gorilla/websocket v1.5.3 has `WriteControl` for pings, `SetPongHandler`, and `SetReadDeadline`. A deadline that passes ends the read with an error, and every later read fails too. In this repo, though, disgo owns the `*websocket.Conn` and its only reader. The recorder holds only the `net.Conn` underneath, through the dialer it hands disgo. [gor-writecontrol] [gor-pong] [gor-deadline] [disgo-listen] [repo-dialer]

## Pinned sources

| Source | Version | Commit |
|---|---|---|
| discord/discord-api-docs | main, 2026-10-16 | `f7416a2cf165455f72a31f5ea4e671a4f047f2f7` |
| discordjs/discord.js, `@discordjs/voice` 0.19.2 | main, 2026-10-14 | `03777df7f7cc372de88fd05cd3999ab59e5c36c0` |
| Rapptz/discord.py | master, 2026-09-01 | `65232c38702be5844cf2ce865a4777eb1928b5d0` |
| serenity-rs/songbird | current, 2026-10-10 | `957e17fcbc687cd88979c90b76bdf79017803b09` |
| FelixMcFelix/discortp, songbird's packet crate | 0.6.0 | `fd897e54ad63a2738961c3205bccd52fc9b53ccb` |
| discord-jda/JDA | master, 2026-09-20 | `7c900019ca511a994087320ce98d19cd79a2c4b0` |
| TakahikoKawasaki/nv-websocket-client, JDA's WebSocket | 2.14 | `3168d4e1f034450b41278f5f73f2c9faeaa5beef` |
| KyokoBot/koe | 3.1.0 | `e9147ed2f486bf5ab6cf9e29fb90331c55974230` |
| KyokoBot/koe, the version Lavalink pins | 3.0.0-pre6 | `7436cf8ea57ddbc9348e261b1f377c78b6b98530` |
| lavalink-devs/Lavalink | master, 2026-08-25 | `3d24006d1eed2bd9b4f5916298cf87ab34408b6f` |
| disgoorg/disgo, this repo's voice gateway | v0.19.7-0.20260923193941-995ee20fe0d6 | `995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc` |
| gorilla/websocket | v1.5.3 | `ce903f6d1d961af3a8602f2842c8b1c3fca58c4d` |
| bwmarrin/discordgo | v0.29.0 | `6e8fa27c7917ea54d8b9ec26f126becae59058d2` |
| websockets/ws, discord.js's WebSocket | 8.21.3 | `c791e707eab3c13dd9a261d2479c3cc4a49a6fed` |
| aio-libs/aiohttp, discord.py's WebSocket | v3.14.5 | `f17c631bddcfd4ec712342db28f1d3381a3fec71` |
| 7Cav/cavbot2 | develop | `68c86cad3f93696a28f2d34696ca36611cfa8e8f` |

## 1. Extra heartbeats on the voice gateway

### What the voice docs say

- A client sends op 3 Heartbeat "at the interval determined in Opcode 8 Hello", and "every elapsed interval" after Hello. The server answers with op 6 Heartbeat ACK, "that contains the previously sent nonce". Since voice gateway v8 the heartbeat also carries `seq_ack`. [voice-hb]
- The opcode table lists op 3 as sent by the client only. It describes Hello's value as "Time to wait between sending heartbeats in milliseconds." [ops-voice]
- The voice close codes include 4020 "Bad request", explained as "You sent a malformed request", and 4021 "Disconnected: Rate Limited", explained as "Disconnect due to rate limit exceeded. Should not reconnect." Neither names heartbeats, and the docs give no limit for the voice gateway. [ops-close]
- Nothing in the voice docs covers sending a heartbeat early, on demand, or in extra. The page opens by saying voice connections "operate in a similar fashion to the Gateway connection" and doesn't say which of the main gateway's rules carry over. [voice-intro]

### What the main gateway docs say

- "You *can* send heartbeats before the `heartbeat_interval` elapses, but you should avoid doing so unless necessary. There is already tolerance in the `heartbeat_interval` that will cover network latency." [gw-early]
- Discord may ask for a heartbeat by sending op 1. The app "should immediately send back another Heartbeat event without waiting the remainder of the current interval", and Discord answers that one with an ACK too. [gw-request]
- A client that gets no ACK between two of its heartbeats has a "failed or 'zombied' connection" and should close with any code but 1000 or 1001, then resume. [gw-zombie]
- Apps may send 120 gateway events per connection every 60 s. Going over disconnects at once. [gw-ratelimit] In 2017 GitHub user night, a public member of Discord's GitHub organization whose GitHub profile reads "Working @discord", answered that all payloads count toward that limit, heartbeats included. [issue-315]

### What libraries do

- discord.py heartbeats the voice gateway every `min(interval, 5.0)` seconds. [dpy-hello] Commit `fa34d35`, dated 2020-04-04, made that change. Its message says it makes heartbeating "a bit more consistent to the official Discord client". [dpy-commit] At 13750 ms that is 2.75 heartbeats per interval. The voice docs give no hint whether Discord's own client does this.
- discord.js, songbird, JDA, koe, discordgo and disgo all heartbeat once per Hello interval. [djs-ws-interval] [sb-ws-loop] [jda-keepalive] [koe-hb] [dgo-hb] [disgo-hb]
- JDA answers a server-sent op 3 with an immediate heartbeat of its own, although the voice docs list op 3 as client-only. [jda-op3]
- discord.py's voice code comments that 4021 means "rate limited, we should not reconnect", and it disconnects on it. [dpy-4021]

### What it means in this repo's stack

- disgo records the nonce of each heartbeat it sends. When an op 6 arrives whose `t` differs, disgo logs "received heartbeat ack with nonce", closes with "heartbeat ACK nonce mismatch" and reconnects. [disgo-sendhb] [disgo-ack]
- disgo's `Gateway` interface has `Send(ctx, op, data)`, which works once the gateway is Ready. [disgo-iface] [disgo-send] A heartbeat sent through it draws an ACK carrying that heartbeat's `t`. disgo closes the gateway unless that `t` equals the nonce disgo itself sent last.
- The recorder already sees every message disgo receives, ACKs included, through `watchEvents`. [repo-watchevents]
- disgo maps 4021 to `Reconnect: false` and `NewConnection: false`. [disgo-4021] The recorder's `getsFreshConnection` returns that `NewConnection` flag, so a 4021 leaves the recorder out of the call. [repo-fresh]

## 2. UDP keepalive echoes

### Discord

- The docs say nothing about a UDP keepalive. The word appears nowhere in the docs repo at the pinned commit. The voice page's UDP sections cover IP discovery and RTP voice packets only. [voice-ipdisc]

### @discordjs/voice today

- It sends an 8-byte buffer every 5 s, with a counter written as an unsigned 32-bit little-endian integer at offset 0 and the last 4 bytes left zero. The first keepalive goes out at once. The counter wraps after 2^32 - 1. [djs-udp-const] [djs-udp-ctor] [djs-udp-keepalive]
- It passes every received datagram on and matches nothing. [djs-udp-onmessage] Its `ping` field carries the doc comment "@deprecated This field is no longer updated as keep alive messages are no longer tracked." [djs-udp-ping]

### @discordjs/voice before March 2023

- `VoiceUDPSocket` kept a list of keepalives in flight. A received 8-byte datagram whose counter matched one of them set `ping` to the time since that keepalive went out. [djs-old-onmessage]
- Once 12 keepalives were waiting, 60 s at the 5 s interval, it logged "UDP socket has not received enough responses from Discord - closing socket" and destroyed the socket. [djs-old-limit] [djs-old-keepalive] `Networking` answered a closed UDP socket by resuming on a new WebSocket. [djs-udpclose]
- On 2023-03-02, a discord.js user posted logs in issue #9185. On `singapore63` no 8-byte datagram came back and the socket closed after 12 keepalives. On `singapore11050` an 8-byte reply arrived 32 ms after each keepalive. [djs-9185-logs]
- PR #9202, merged 2023-03-09, dropped the tracking and kept the sending. Its description reads "While UDP keepAlives aren't documented (and technically not needed either), we need to keep them in order to ensure voice receivers still work", and says the change stopped audio cutting out after one minute. [djs-pr-9202]
- PR #9195, closed unmerged, claimed from a screenshot of a decompiled `discord_voice.node` that "Discord servers only recognize 0x1337cafe as the header prefix". It changed the packet to `0x1337cafe` big-endian followed by the counter at offset 4. [djs-pr-9195] I found no other source for this format and could not check it.

### Other libraries

- songbird sends a 4-byte keepalive holding the SSRC, big-endian, every 5 s. [sb-const] [sb-mixer-ka] [dtp-keepalive] Its constant's comment reads "Passive monitoring of Discord itself shows that these fire every 5 seconds irrespective of outgoing UDP traffic." [sb-const] Its receive task demuxes RTP and RTCP and logs anything else as a failed parse or a packet too small. It tracks no echoes. [sb-udprx]
- JDA sends 9 bytes, `0xC9` then eight zeros, alongside each WebSocket heartbeat, so once per `heartbeat_interval`. If the send throws `NoRouteToHostException` it closes the connection as lost. Nothing reads replies to it. [jda-udp-bytes] [jda-keepalive]
- discord.py sends no UDP keepalive. Only IP discovery and audio leave its socket. [dpy-discover] [dpy-sendpacket] [dpy-audio]
- koe sends no UDP keepalive. A search of koe 3.1.0's core for "keepalive" finds none, and its heartbeat code sends only on the WebSocket. [koe-hb]
- discordgo v0.29.0 sends an 8-byte little-endian 64-bit counter every 5 s. Its receiver skips datagrams shorter than 12 bytes. [dgo-ka-start] [dgo-ka] [dgo-rx]
- This repo sends an 8-byte buffer every 5 s with a little-endian 32-bit counter in the first 4 bytes, the discord.js layout. `readRTP` drops any datagram shorter than 12 bytes or whose second byte isn't `0x78`, so an echo would go unseen. [repo-keepalive] [repo-readrtp] [disgo-rtp]

So the maintained libraries send three different packets, an 8-byte little-endian counter, 4 bytes of SSRC, and 9 bytes of `0xC9` and zeros, and two of them send none. The sources don't say whether a voice server echoes any of them today.

## 3. WebSocket ping frames

- Discord's docs don't mention WebSocket ping or pong frames for either gateway. "Heartbeats are pings" in the main gateway page refers to op 1, not to control frames. [gw-pings]
- RFC 6455 section 5.5.2 says "Upon receipt of a Ping frame, an endpoint MUST send a Pong frame in response, unless it already received a Close frame." It also notes a Ping "may serve either as a keepalive or as a means to verify that the remote endpoint is still responsive." [rfc6455-ping]

### Probe, 2026-10-19

I wrote a small client on gorilla/websocket v1.5.3. It opened a WebSocket, read the first message, sent pings with distinct payloads one second apart, then closed with 1000. It never identified.

| Target | Hello | Pongs | Pong round trip |
|---|---|---|---|
| `wss://c-hel02-2d506172.discord.media/?v=8`, run 1 | `{"op":8,"d":{"v":8,"heartbeat_interval":13750}}` | 3 of 3 | 3 to 5 ms |
| same host, run 2 | same | 5 of 5 | 4 to 5 ms |
| `wss://gateway.discord.gg/?v=10&encoding=json` | op 10, `heartbeat_interval` 41250, `_trace` naming `gateway-prd-arm-us-east1-b-04vh` | 3 of 3 | 157 to 160 ms |

The voice host is the one `voice/dialer_test.go` names. [repo-voicehost] The voice gateway sent Hello unprompted on connect, before any identify.

Both hosts answered the upgrade with `Server: cloudflare` and a `Cf-Ray` header ending in `HEL`. Their A records are in 162.159.0.0/16, which ARIN's RDAP lists as `NET-162-158-0-0-1`, `CLOUDFLARENET`, Cloudflare, Inc. [arin] A TCP connect to either host took about 3 ms. The main gateway's Hello names an origin in us-east1, and its pongs took about 157 ms, so a host beyond Cloudflare's edge answered them. The voice host's pongs took 3 to 5 ms, the same as the edge round trip, so this probe can't tell whether Cloudflare or the voice server answered them.

Limits of the probe. It ran on one voice host, without identifying, while nothing else was flowing. It doesn't show how an identified session treats pings.

### Libraries

No library examined sends pings to the voice gateway or relies on pongs.

- discord.js opens the voice WebSocket with `ws`. Its voice networking files contain no call to `ping`, and liveness comes from heartbeat counting. [djs-ws-import] [djs-ws-interval]
- discord.py opens the voice WebSocket through aiohttp's `ws_connect` without its `heartbeat` option, which defaults to `None`. [dpy-wsconnect] [dpy-voice-connect] [aio-heartbeat]
- songbird's comments say tungstenite and tokio-websockets handle pings and pongs internally. songbird itself sends no ping. [sb-ws-pingpong]
- JDA's audio package contains no call to nv-websocket-client's `setPingInterval`. [jda-audio]
- koe's handler acts on text, binary and close frames only. [koe-frames]
- disgo's voice package sets no ping handler, pong handler or read deadline on the WebSocket, and sends no pings. [disgo-listen] [disgo-dial]

## 4. Detection faster than one missed ACK

The main gateway docs treat a connection as zombied when no ACK arrives between two heartbeats, one missed ACK. [gw-zombie] No library examined notices a dead voice connection sooner than that. The table derives each library's time at this repo's `heartbeat_interval` of 13750 ms, counted from the last ACK or message received before the path died.

| Client | Mechanism | Threshold | At 13750 ms |
|---|---|---|---|
| this repo's recorder | timer from the last ACK | interval + 3 s | 16.75 s |
| disgo alone | at each tick, close if the last heartbeat sent is newer than the last ACK | the tick after an unanswered heartbeat | about 27.5 s |
| discord.js | count heartbeats sent since the last ACK, close at 3 | the fourth tick after the last answered heartbeat | about 55 s, then up to 30 s more for `ws` to give up on the close handshake |
| discord.js before March 2023, UDP | 12 keepalives without an echo | 60 s | 60 s, then a resume |
| discord.py | WebSocket receive times out | 30 s without any message | about 30 s, since ACKs arrive every 5 s |
| discord.py | no ACK, checked every 5 s | 60 s | 60 s |
| JDA | TCP socket read timeout | interval + 10 s without a byte | 23.75 s |
| songbird | none, a nonce mismatch only logs a warning | none | until a send or receive fails |
| koe | none, an ACK only updates `ping` | none | until the channel errors or closes |
| discordgo v0.29.0 | none, a failed heartbeat write ends the loop | none | until a write fails |

Sources for each row:

- Recorder. [repo-overdue] [repo-margin] [repo-watchlocked] [repo-wentsilent]
- disgo. [disgo-hb] [disgo-sendhb]
- discord.js. Each heartbeat it sends adds one to `missedHeartbeats`, and an ACK resets it to 0. At each tick it closes when the count is 3 or more, and still sends a heartbeat. [djs-ws-ack] [djs-ws-send] [djs-ws-interval] `ws` 8.21.3 waits up to `closeTimeout`, 30000 ms by default, for the closing handshake before it destroys the socket. [ws-closetimeout] [ws-closetimer] A WebSocket close with code 4015 or under 4000 makes `Networking` resume. [djs-wsclose]
- discord.js UDP before March 2023. [djs-old-limit] [djs-udpclose]
- discord.py. `poll_event` waits 30 s for a message. On a timeout `_poll_voice_ws` backs off and reconnects. [dpy-poll] [dpy-pollloop] [dpy-reconnect] The voice keepalive handler's limit is `_max_heartbeat_timeout = 60.0`. Only an ACK moves its clock, and it closes with 4000 once the limit passes. [dpy-voice-connect] [dpy-keepalive-run] [dpy-voice-ack]
- JDA. It sets `SO_TIMEOUT` on the WebSocket's raw socket to `keepAliveInterval + 10000` ms. [jda-sotimeout] In nv-websocket-client 2.14 a timed-out read throws `SocketTimeoutException`, which is an `InterruptedIOException`. The reading thread reports `INTERRUPTED_IN_READING`, sends a close frame and stops reading. [nv-readframe]
- songbird. [sb-ws-loop] [sb-ws-ack]
- koe 3.1.0, and Lavalink's pinned 3.0.0-pre6, which does the same. [koe-ack] [koe-hb] [koe6-ack] [koe6-hb] [ll-koe]
- discordgo. [dgo-hb]

discord.py is the only library that sends heartbeats more often than the interval, but its timeouts are 30 s and 60 s. No library examined uses UDP echoes, WebSocket pongs, or an ACK timeout shorter than the interval.

## 5. gorilla/websocket v1.5.3

### What the library offers

- `WriteControl(messageType, data, deadline)` writes a close, ping or pong frame. [gor-writecontrol] `Close` and `WriteControl` may be called concurrently with all other methods. [gor-concurrency]
- `SetPongHandler` sets the handler for pongs from the peer, and the default does nothing. The handler runs from inside `NextReader`, `ReadMessage` and the message reader's `Read`, so an application must be reading for it to fire. [gor-pong] [gor-control] The package docs say "If an application sends ping messages, then the application should set a pong handler to receive the corresponding pong." [gor-control]
- `SetPingHandler`'s default answers each ping with a pong. [gor-ping]
- `SetReadDeadline` sets the deadline on the underlying network connection. "After a read has timed out, the websocket connection state is corrupt and all future reads will return an error." [gor-deadline] Errors from `NextReader` are permanent. [gor-nextreader]
- Each connection supports one concurrent reader and one concurrent writer. `SetReadDeadline`, `SetPongHandler` and `SetPingHandler` count as read methods, so only the reading goroutine should call them. [gor-concurrency]
- The package's chat example shows the pattern. It sets a read deadline `pongWait` ahead, pushes it forward from the pong handler, and sends a ping every 9/10 of `pongWait`. When a pong goes missing the deadline passes, and the read fails. [gor-chat-const] [gor-chat-pong] [gor-chat-ping]

### Who holds what in this repo

- disgo dials the voice WebSocket with the configured `websocket.Dialer` and runs the only read loop, `listen`, on `conn.NextReader()`. [disgo-dial] [disgo-listen] Its `Gateway` interface exposes no `*websocket.Conn`. [disgo-iface]
- When `NextReader` fails with anything but a close frame or `net.ErrClosed`, disgo logs "failed to read next message from voice gateway", closes with 1012 and reconnects. [disgo-readerr]
- The recorder gives disgo its dialer, whose `NetDialContext` is the recorder's own `dialContext`. [repo-dialer-pass] [repo-dialer] So the recorder holds the `net.Conn` under each voice WebSocket, and it already wraps that conn for the smoke drop hook. [repo-dialer]
- Go's `net.Conn` allows its methods to be called from several goroutines at once. A deadline "applies to all future and pending I/O". A read past it returns an error that wraps `os.ErrDeadlineExceeded`. [go-netconn-conc] [go-netconn-deadline]
- gorilla's `NetConn` docs warn that "writing to or reading from this connection directly will corrupt the WebSocket connection." [gor-netconn] Writing a ping frame to the `net.Conn` by hand would go around gorilla.

## Related facts

- The two paths end in different places. The voice WebSocket host resolves to Cloudflare's addresses, per the probe above. The media socket goes to the IP and port that op 2 Ready names. [voice-udp] A probe on the WebSocket tests only the first path.
- The comment on the recorder's keepalive calls discord.js "Discord's own JavaScript library". [repo-keepalive] discord.js lives under the discordjs GitHub organization, not Discord's `discord` organization.

## Not settled

1. Whether Discord's voice servers echo UDP keepalives today, and for which formats. The latest first-hand evidence is from March 2023 and is mixed. To settle it, log every datagram shorter than 12 bytes on the recorder's media socket during a test-guild session.
2. How an identified voice session treats extra op 3 heartbeats, and how many per minute bring a 4021. Discord documents no number. discord.py's 5 s cadence has run since 2020, which suggests at least that rate is tolerated, but that's not a statement from Discord.
3. Who answers a ping sent to a voice gateway host, Cloudflare's edge or the voice server. The probe's round trip can't tell them apart. If the edge answers, a pong would prove the path to Cloudflare and nothing about the voice server behind it.
4. Whether pongs keep coming on an identified session. The probe never identified.
5. Which of the two paths the test-guild drops cut, the WebSocket path to Cloudflare, the UDP path to the media server, or both.

## Method

I read the docs and code at the pinned commits and the GitHub issues and pull requests linked throughout. The disgo and gorilla code came from the Go module cache at the versions in `go.mod`. I ran the probe on 2026-10-19 from a host whose Cloudflare colo is HEL.

[repo-overdue]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L639-L652
[repo-margin]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L717-L722
[repo-watchevents]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L703-L715
[repo-watchlocked]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L728-L761
[repo-wentsilent]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L777-L794
[repo-fresh]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L1107-L1125
[repo-dialer-pass]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L116
[repo-keepalive]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/udp.go#L147-L169
[repo-readrtp]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/udp.go#L215-L218
[repo-dialer]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/dialer.go#L88-L106
[repo-voicehost]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/dialer_test.go#L34-L35

[voice-intro]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx?plain=1#L8
[voice-hb]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx?plain=1#L124-L189
[voice-udp]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx?plain=1#L191-L208
[voice-ipdisc]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx?plain=1#L582-L592
[ops-voice]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx?plain=1#L58-L70
[ops-close]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx?plain=1#L87-L106
[gw-pings]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway.mdx?plain=1#L140
[gw-early]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway.mdx?plain=1#L154
[gw-zombie]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway.mdx?plain=1#L171
[gw-request]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway.mdx?plain=1#L173-L177
[gw-ratelimit]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/events/gateway.mdx?plain=1#L509
[issue-315]: https://github.com/discord/discord-api-docs/issues/315#issuecomment-317280558

[djs-udp-const]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceUDPSocket.ts#L34-L42
[djs-udp-ping]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceUDPSocket.ts#L80-L85
[djs-udp-ctor]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceUDPSocket.ts#L92-L102
[djs-udp-onmessage]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceUDPSocket.ts#L109-L112
[djs-udp-keepalive]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceUDPSocket.ts#L117-L124
[djs-ws-import]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceWebSocket.ts#L5
[djs-ws-ack]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceWebSocket.ts#L157-L161
[djs-ws-send]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceWebSocket.ts#L202-L216
[djs-ws-interval]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceWebSocket.ts#L222-L235
[djs-wsclose]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/Networking.ts#L442-L455
[djs-udpclose]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/Networking.ts#L457-L469
[djs-old-limit]: https://github.com/discordjs/discord.js/blob/dbca93098cb5e92ba8fec169fab8bab875b6ee0f/packages/voice/src/networking/VoiceUDPSocket.ts#L39-L47
[djs-old-onmessage]: https://github.com/discordjs/discord.js/blob/dbca93098cb5e92ba8fec169fab8bab875b6ee0f/packages/voice/src/networking/VoiceUDPSocket.ts#L130-L143
[djs-old-keepalive]: https://github.com/discordjs/discord.js/blob/dbca93098cb5e92ba8fec169fab8bab875b6ee0f/packages/voice/src/networking/VoiceUDPSocket.ts#L148-L165
[djs-9185-logs]: https://github.com/discordjs/discord.js/issues/9185#issuecomment-1452510633
[djs-pr-9202]: https://github.com/discordjs/discord.js/pull/9202
[djs-pr-9195]: https://github.com/discordjs/discord.js/pull/9195
[ws-closetimeout]: https://github.com/websockets/ws/blob/c791e707eab3c13dd9a261d2479c3cc4a49a6fed/lib/websocket.js#L639-L640
[ws-closetimer]: https://github.com/websockets/ws/blob/c791e707eab3c13dd9a261d2479c3cc4a49a6fed/lib/websocket.js#L1309-L1314

[dpy-keepalive-run]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L157-L172
[dpy-voice-ack]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L226-L252
[dpy-voice-connect]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L921-L940
[dpy-hello]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L1015-L1018
[dpy-poll]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L1165-L1177
[dpy-wsconnect]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/http.py#L556-L574
[dpy-discover]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L1109-L1117
[dpy-sendpacket]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_state.py#L608-L609
[dpy-audio]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_client.py#L600-L606
[dpy-pollloop]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_state.py#L667-L674
[dpy-4021]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_state.py#L676-L713
[dpy-reconnect]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_state.py#L736-L756
[dpy-commit]: https://github.com/Rapptz/discord.py/commit/fa34d357a1c28a48210e68e4b7b1f32320e44b9b
[aio-heartbeat]: https://github.com/aio-libs/aiohttp/blob/f17c631bddcfd4ec712342db28f1d3381a3fec71/aiohttp/client.py#L1124-L1134

[sb-const]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/constants.rs#L61-L71
[sb-mixer-ka]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/tasks/mixer/mod.rs#L370-L375
[sb-ws-loop]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/tasks/ws.rs#L109-L136
[sb-ws-ack]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/tasks/ws.rs#L288-L299
[sb-ws-pingpong]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/ws.rs#L160-L194
[sb-udprx]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/tasks/udp_rx/mod.rs#L314-L319
[dtp-keepalive]: https://github.com/FelixMcFelix/discortp/blob/fd897e54ad63a2738961c3205bccd52fc9b53ccb/src/discord.rs#L69-L89

[jda-udp-bytes]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L58
[jda-op3]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L563-L573
[jda-sotimeout]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L768-L782
[jda-keepalive]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L784-L816
[jda-audio]: https://github.com/discord-jda/JDA/tree/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio
[nv-readframe]: https://github.com/TakahikoKawasaki/nv-websocket-client/blob/3168d4e1f034450b41278f5f73f2c9faeaa5beef/src/main/java/com/neovisionaries/ws/client/ReadingThread.java#L330-L415

[koe-ack]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/internal/gateway/AbstractMediaGatewayConnection.java#L299-L302
[koe-hb]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/internal/gateway/AbstractMediaGatewayConnection.java#L393-L403
[koe-frames]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/internal/gateway/AbstractMediaGatewayConnection.java#L603-L626
[koe6-ack]: https://github.com/KyokoBot/koe/blob/7436cf8ea57ddbc9348e261b1f377c78b6b98530/core/src/main/java/moe/kyokobot/koe/internal/gateway/MediaGatewayV8Connection.java#L136-L137
[koe6-hb]: https://github.com/KyokoBot/koe/blob/7436cf8ea57ddbc9348e261b1f377c78b6b98530/core/src/main/java/moe/kyokobot/koe/internal/gateway/MediaGatewayV8Connection.java#L356-L366
[ll-koe]: https://github.com/lavalink-devs/Lavalink/blob/3d24006d1eed2bd9b4f5916298cf87ab34408b6f/settings.gradle.kts#L40-L47

[disgo-iface]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L82-L103
[disgo-dial]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L166-L168
[disgo-send]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L260-L268
[disgo-hb]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L377-L404
[disgo-sendhb]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L406-L426
[disgo-listen]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L475-L482
[disgo-readerr]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L500-L534
[disgo-ack]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L579-L587
[disgo-4021]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_opcodes.go#L193-L199
[disgo-rtp]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/udp_conn.go#L23-L40

[gor-writecontrol]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/conn.go#L411-L413
[gor-nextreader]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/conn.go#L994-L998
[gor-deadline]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/conn.go#L1101-L1107
[gor-ping]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/conn.go#L1151-L1171
[gor-pong]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/conn.go#L1178-L1190
[gor-netconn]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/conn.go#L1192-L1197
[gor-control]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/doc.go#L83-L108
[gor-concurrency]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/doc.go#L122-L134
[gor-chat-const]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/examples/chat/client.go#L20-L24
[gor-chat-pong]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/examples/chat/client.go#L62-L63
[gor-chat-ping]: https://github.com/gorilla/websocket/blob/ce903f6d1d961af3a8602f2842c8b1c3fca58c4d/examples/chat/client.go#L83-L116

[dgo-hb]: https://github.com/bwmarrin/discordgo/blob/6e8fa27c7917ea54d8b9ec26f126becae59058d2/voice.go#L519-L544
[dgo-ka-start]: https://github.com/bwmarrin/discordgo/blob/6e8fa27c7917ea54d8b9ec26f126becae59058d2/voice.go#L660
[dgo-ka]: https://github.com/bwmarrin/discordgo/blob/6e8fa27c7917ea54d8b9ec26f126becae59058d2/voice.go#L668-L696
[dgo-rx]: https://github.com/bwmarrin/discordgo/blob/6e8fa27c7917ea54d8b9ec26f126becae59058d2/voice.go#L847

[go-netconn-conc]: https://github.com/golang/go/blob/022c8636110ebac86a9b88724cda168ed713c21f/src/net/net.go#L120-L123
[go-netconn-deadline]: https://github.com/golang/go/blob/022c8636110ebac86a9b88724cda168ed713c21f/src/net/net.go#L151-L171
[rfc6455-ping]: https://www.rfc-editor.org/rfc/rfc6455#section-5.5.2
[arin]: https://rdap.arin.net/registry/ip/162.159.137.234
