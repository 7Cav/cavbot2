# Voice resume with DAVE

Research for [Which voice gateway client does the replacement use? (#817)][i817], written 2026-10-19. It feeds [Wayfinder map: owned voice connection for recorders (#810)][i810].

When a recorder's voice gateway websocket drops while the recorder stays in the channel on the same voice server, should a voice gateway client of our own send opcode 7 Resume, or open a new websocket and send Identify, opcode 0, with the voice token it already holds? The yardstick is how long the recorder can't hear the call after the drop. This note gathers facts and makes no pick.

## Answer

1. **The MLS group on a drop.** The DAVE whitepaper says the voice gateway proposes removing a member when it disconnects "either intentionally or due to a severed websocket connection", and that removed members "are no longer forwarded that media from the SFU" ([protocol.md lines 238 to 255][p-remove]). That passage is where the comment above `ownDial` in `voice/gateway.go` comes from, and the comment matches it ([gateway.go lines 120 to 132][cb-comment]). Neither the whitepaper nor Discord's docs say when the proposal goes out. They give no grace period and don't say "at once". The only timing they give is that the gateway "may delay a removal proposal to batch multiple proposals" ([line 247][p-remove]). Neither document mentions Resume in connection with DAVE. So no source says whether a resumed client is still a group member with its old epoch secrets, or whether the server replays or re-sends opcodes 21 to 30 after Resumed beyond the generic buffered replay. The documented ways into a group all start from a new key package, opcode 26. A client sends one after a Session Description with a nonzero DAVE version, after opcode 24 with epoch 1, or after flagging a bad commit or welcome with opcode 31 ([protocol.md lines 198 to 206][p-keypkg], [lines 319 to 329][p-recovery]). A resume brings none of those triggers by itself.
2. **What a resume keeps.** Discord's docs don't say. They describe Resume as opcode 7 answered by opcode 9 and nothing else ([voice-connections.mdx lines 506 to 551][dd-resume]). Every library read for this note keeps the UDP socket, `secret_key`, encryption mode, SSRC and DAVE session through a resume. None of them sends Select Protocol or redoes IP discovery after opcode 9. Buffered resume, since v8, re-sends "any messages that the client did not receive" among those that carry a sequence number ([lines 553 to 580][dd-buffered]). JSON messages carry it as a top-level `seq`, binary ones as a 2-byte prefix ([lines 242 to 252][dd-binary]), and the client sends the last one back as `seq_ack` inside `d`. In the one raw voice log found, opcodes 2, 4, 6 and 8 carried no `seq`, so a resume would not replay a Session Description ([disgo#534's log][dg534-log]).
3. **The resume window and the closes.** The docs give no window. The only bound they state is the server's message buffer. A resume fails once the buffer has lost a message the client missed ([line 580][dd-buffered]). 4015 is the only close whose text says to resume ([opcodes-and-status-codes.mdx line 101][dd-close]). A refused resume closes "with the appropriate close event code", "for example, due to an invalid session" ([line 551][dd-resume]). Three logs show 4006 answering a voice Resume, two of them with DAVE on, each within 36 ms of the Resume. No source shows 4009 after a Resume, and no source shows a voice Resume getting opcode 9 with DAVE on.
4. **What the libraries do.** None of the six resets DAVE on a resume or sends a new key package after opcode 9. All six start DAVE over and send opcode 26 when a fresh Identify's Session Description arrives. They disagree on when to resume. discord.js resumes on 4015 and closes below 4000, discord.py on 4015 only, songbird on 4009, 4015 and transport errors, JDA on every server close but 4004, 4006, 4011, 4014, 4015, 4021 and 4022, koe on a list that includes 4009 and 4015, and disgo on its own table. 4015, the one close Discord says to resume after, makes JDA leave and rejoin. 4009 gets a resume from three libraries and a rejoin from the other three. Three of them identify again with the held token on a new websocket without going back to the main gateway. Songbird does so after a failed resume, koe 3.1.0 after a 4006, and disgo after an attempt that got a websocket and failed. Discord's libdave says nothing about reconnects.
5. **Reports.** I found no report, in any of these projects or elsewhere on GitHub, of a voice Resume getting opcode 9 with DAVE on, and none of sound coming back after one. The two DAVE-era refusals both came from disgo, which sends `seq` instead of `seq_ack`. In [disgo#534][dg534] (2026-03-25) an Identify with the same session ID and token 2.1 s after the 4006 was accepted, and DAVE reached opcode 22 Execute Transition 204 ms after that Identify.

## Corrections to what we knew

- disgo's Resume does tag its sequence field `seq` ([gateway_messages.go lines 307 to 312][dg-resume-msg]). It has a second fault. It reads the inbound number from a key named `s` ([lines 12 to 26][dg-msg]), while Discord sends `seq` ([voice-connections.mdx lines 557 to 570][dd-buffered], and every JSON frame in [disgo#534's log][dg534-log]). So disgo never records the number of a JSON message. Its heartbeat's `seq_ack` and its Resume's `seq` carry the last binary DAVE message's number, or 0.
- The code comment's source is the whitepaper's "Member Remove" section. The comment reads as if removal follows a drop at once. The whitepaper doesn't say when, and doesn't say whether a session the client resumes counts as a disconnect.

## Sources

**Docs.** `discord/discord-api-docs` at `f7416a2cf165455f72a31f5ea4e671a4f047f2f7` (2026-10-16): `developers/topics/voice-connections.mdx`, `developers/topics/opcodes-and-status-codes.mdx` and `developers/change-log.mdx`. The DAVE whitepaper is `discord/dave-protocol` `protocol.md` at `1b2b706ad4a562ede5c2ef901f9d77f6dbe79446`, version 1.1.4. Both are the commits the earlier note [Discord voice event sequences we already know][cb-sequences] read.

**Code**, each read at one commit, none built or run:

| Project | Commit | Date |
|---|---|---|
| @discordjs/voice, in discord.js `packages/voice` (package 0.19.2) | `03777df7f7cc372de88fd05cd3999ab59e5c36c0` | 2026-10-14 |
| discord.py | `65232c38702be5844cf2ce865a4777eb1928b5d0` | 2026-09-01 |
| discord-ext-voice-recv | `ac04ea7b0941112e83767cf1c1469b408fa06748` | 2025-06-18 |
| songbird, branch `current` (v0.6.1) | `957e17fcbc687cd88979c90b76bdf79017803b09` | 2026-10-10 |
| serenity-voice-model 0.3.0, which songbird pins | `cef32674d5ad6c539b1ee2ff9dcda39b6d71e18f` | |
| JDA (v6.7.0) | `7c900019ca511a994087320ce98d19cd79a2c4b0` | 2026-09-20 |
| JDAVE (v0.1.8) | `5d3585f52e80fecdfd4f4bdfcd817c9b30f95f67` | 2026-03-29 |
| koe, `master` (tag 3.1.0) | `e9147ed2f486bf5ab6cf9e29fb90331c55974230` | 2026-10-07 |
| disgo, the bot's pin `v0.19.7-0.20260923193941-995ee20fe0d6` | `995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc` | 2026-09-23 |
| dave-go v0.5.1, the bot's pin | `7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5` | 2026-07-15 |
| libdave | `8de72b1f8a2ac3c5a5270755bb8091a62e3c6169` | 2026-09-15 |

disgo's `voice/` is identical at master `2d889db8a812683270d8ee262f00861ce30ebb6f` (2026-10-17). Lavalink's branches pin koe 3.0.0-pre6 and 3.0.0, both commit `7436cf8e`, not 3.1.0, and I say where the two differ.

**Logs.** The only raw voice gateway traffic around a drop that I found is the log attached to [disgo#534][dg534], from disgo v0.19.2 with DAVE on, and the excerpt in [shi-gg/linkdave#83][linkdave83]. Both are user uploads on public trackers. I read them as data.

## What happens to the MLS group when the websocket drops

### What the sources say

- The voice gateway is the MLS group's external sender, so that it can propose "the removal of previous members when they disconnect" ([voice-connections.mdx line 296][dd-extsender]).
- The whitepaper's "Member Remove" section lists two triggers. A member is proposed for removal when "They disconnect from the voice gateway, either intentionally or due to a severed websocket connection" and when "They are about to be re-added with a different key package". The gateway "may delay a removal proposal to batch multiple proposals" ([protocol.md lines 238 to 247][p-remove]).
- A removed member can still decrypt for up to roughly two seconds, but the whitepaper adds "Though removed members may be able to decrypt media still being sent during the short transition period, they are no longer forwarded that media from the SFU. Discord first-party clients display all members of the media session as long as they remain connected to the voice gateway and may be receiving media from the SFU" ([lines 251 to 255][p-remove]).
- A pending member is one "connected to the voice gateway but have not yet been welcomed to the group" ([lines 192 to 196][p-pending]). A client sends a new key package after a Session Description with a nonzero version, after opcode 24 with epoch 1, and when it reports an invalid commit or welcome ([lines 198 to 206][p-keypkg]). Resume and Resumed are not on that list. Discord's docs add that "a new key package must be generated each time pending member is waiting to be added or re-added to the group" ([voice-connections.mdx line 310][dd-keypkg]).
- Members refuse an add proposal for a user who was in an opcode 13 Client Disconnect "and has not reconnected" ([protocol.md line 224][p-add]). By that rule, other members accept a returning user's add proposal only once an opcode 11 Clients Connect has named them again.
- A member that can't process a commit or welcome sends opcode 31. The gateway then proposes its removal, returns it to the pending state, and proposes adding it once a new key package arrives ([lines 319 to 329][p-recovery]; [voice-connections.mdx lines 332 to 334][dd-invalid]).

### What they leave open

- When the removal goes out after a severed websocket. Neither document gives a grace period, says "at once", or says the session must end first.
- Whether the gateway counts a session the client resumes as a disconnect at all.
- Whether a client that resumes is still in the group, with its old epoch secrets usable. If it was removed while away, the only documented ways back are opcode 31 followed by a new key package, or the gateway sending opcode 24 with epoch 1. Nothing says the gateway does the latter for a resumed client. A dave-go change reads it the same way. Since [dave-go#52][davego52] (merged 2026-10-13, after the bot's v0.5.1), a dropped send arms a watchdog, and "On resume nothing else would reset the session, so the watchdog sends invalid_commit_welcome and a new key package after the timeout".
- Whether the server re-sends opcodes 21 to 30 after opcode 9. The docs promise only that missed sequence-numbered messages are re-sent ([voice-connections.mdx line 578][dd-buffered]). Opcode 25 External Sender "may be sent immediately on voice gateway connection or at a later time" ([line 300][dd-extsender]). Nothing says a resume counts as a connection for that purpose.

## What a resume keeps, and how the sequence number travels

### What the docs say

- Opcode 2 Ready carries the SSRC, the UDP address and the encryption modes ([voice-connections.mdx line 102][dd-ready]). The client then opens UDP, runs IP discovery if needed and sends Select Protocol ([line 208][dd-udp]). Opcode 4 Session Description answers with `mode`, `secret_key` and `dave_protocol_version` ([lines 438 to 452][dd-sessdesc]). All of that belongs to the Identify flow. The Resume section mentions none of it ([lines 506 to 551][dd-resume]).
- Version 8 "Added server message buffering, missed messages re-delivered on resume" ([line 25][dd-versions]). Discord added the docs for it in [discord-api-docs#7075][dd7075], merged 2024-08-13. [#7156][dd7156], merged 2024-09-19, made the sequence number required on every server-sent binary message.
- The gateway "includes a sequence number with all messages that may need to be re-sent". The example puts `"seq": 10` at the top level of the message, beside `op` and `d` ([lines 555 to 570][dd-buffered]).
- A v8 client "must include the last sequence number they received under the data `d` key as `seq_ack` in both the Opcode 3 Heartbeat and Opcode 7 Resume payloads". A client that has received none may omit it or send -1 ([lines 572 to 574][dd-buffered]). The Resume example is `server_id`, `session_id`, `token` and `seq_ack` ([lines 511 to 523][dd-resume]).
- Binary messages carry the number as an optional big-endian uint16 ahead of the opcode, sent by the server only ([lines 242 to 252][dd-binary]).
- On success the server answers opcode 9 "and will re-send any messages that the client did not receive" ([line 578][dd-buffered]).

### What the one raw log shows

[disgo#534's log][dg534-log] has 41 inbound voice gateway messages. The JSON ones break down like this:

| Opcodes | Carried `seq` |
|---|---|
| 2 Ready, 4 Session Description, 6 Heartbeat ACK, 8 Hello | no |
| 5, 11, 12, 15, 18, 20, 22 | yes |

The binary DAVE messages (25, 30) carry the 2-byte prefix. The numbering started again at 0 on the new session after the Identify (`{"seq":0,"op":11,...}`). So a Session Description is not something a buffered resume would replay. No log shows opcodes 13, 21 or 24, so whether those carry a number is unseen.

### What the libraries keep through a resume

Every library keeps the UDP socket, transport key, mode and SSRC, and none sends Select Protocol after opcode 9:

- discord.js keeps `connectionData` and the UDP socket, and its Resumed branch only sets the state to Ready ([Networking.ts lines 540 to 545][djs-resumed]).
- discord.py's Resumed handler only logs ([gateway.py lines 1007 to 1008][dpy-resumed]). Each new websocket object starts with `secret_key = None`, so its 4015 path waits for a Session Description that the docs don't promise after a resume. The author of its resume code didn't test it against a server failure. In [discord.py#10210][dpy10210] they wrote "You'll need to find a way to crash the discord voice or something in order to test buffered resume lol, but I guess it should work fine?"
- songbird waits for Hello and Resumed, then swaps in the new socket and keeps the same connection, UDP task and DAVE session ([connection/mod.rs lines 312 to 341][sb-reconnect-inner]).
- JDA's RESUMED handler sets CONNECTED, and Select Protocol only follows Ready ([AudioWebSocket.java lines 533 to 540][jda-resumed]).
- koe's RESUMED handler resets its attempt counter, and `selectProtocol` runs only from Ready ([AbstractMediaGatewayConnection.java lines 307 to 312][koe-resumed]).
- disgo's Resumed sets the gateway ready, and Select Protocol only follows Ready ([gateway.go lines 573 to 577][dg-resumed]).

How each one carries the sequence number:

| Library | Gateway version | Inbound JSON key | Resume field |
|---|---|---|---|
| discord.js | 8 | `seq` ([VoiceWebSocket.ts lines 153 to 155][djs-seq]) | `seq_ack` ([Networking.ts lines 422 to 431][djs-resume]) |
| discord.py | 8 ([gateway.py line 931][dpy-v8]) | `seq` ([line 1000][dpy-seq]) | `seq_ack` ([lines 894 to 905][dpy-resume]) |
| songbird | 4 ([voice-model constants.rs line 4][vm-v4]) | none tracked | none ([connection/mod.rs lines 301 to 307][sb-resume]) |
| JDA | 8 | `seq` ([AudioWebSocket.java line 477][jda-seq]) | `seq_ack` ([lines 660 to 668][jda-resume]) |
| koe | 8 | `seq` since [koe#40][koe40], 2025-10-28, which changed it from `s` ([MediaGatewayV8Connection.java lines 65 to 67][koe-seq]) | `seq_ack` ([lines 49 to 52][koe-resume]) |
| disgo | 8 | `s` ([gateway_messages.go lines 12 to 26][dg-msg]) | `seq` ([lines 307 to 312][dg-resume-msg]) |

Songbird can't replay at all. Its maintainer opened [songbird#247][sb247] on 2024-08-16 for it ("We will require a mechanism to account for this during connection resumption to correctly support message buffer replay"), and it is still open. discord.js and disgo both skip a sequence number of 0, by a truthiness check and a `> 0` check, and Discord numbers the first message of a session 0.

## How long a session stays resumable, and which closes come back

### What the docs say

- No time. The only stated limit is the buffer, "The resume may be unsuccessful if the voice gateway buffer for the session no longer contains a message that has been missed. In this case the session will be closed" ([voice-connections.mdx line 580][dd-buffered]). No close code is named for that case.
- A refused resume closes "with the appropriate close event code", "for example, due to an invalid session", and the client should "follow the Connecting flow", which starts at the main gateway's opcode 4 ([line 551][dd-resume]).
- The close table ([opcodes-and-status-codes.mdx lines 89 to 106][dd-close]):

| Code | Text |
|---|---|
| 4006 | "Session no longer valid. Your session is no longer valid." |
| 4009 | "Session timeout. Your session has timed out." |
| 4014 | "Disconnect individual client (you were kicked, the main gateway session was dropped, etc.). Should not reconnect." |
| 4015 | "The server crashed. Our bad! Try resuming." |
| 4022 | "Disconnect all clients due to call terminated (channel deleted, voice server changed, etc.). Should not reconnect." |

4015 is the only code the docs tie to a resume. They give 4006 and 4009 no instruction.

### What the logs show

Three logs show a voice Resume refused, all with 4006:

| Source | Date | DAVE | Drop | Resume sent | 4006 |
|---|---|---|---|---|---|
| [disgo#534][dg534] | 2026-03-25 | on, per the log's DAVE lines | 1006 "unexpected EOF" at 00:26:38.496 | +135 ms, with `"seq":25` | +161 ms, 26 ms after the Resume |
| [shi-gg/linkdave#83][linkdave83], a disgo user | 2026-06-17 | on, since Discord has required it from 2026-03-01 | 1006 at 05:17:34.464 | +219 ms, with `"seq":6` | +255 ms, 36 ms after the Resume |
| [JDA#2853][jda2853] | 2025-05-22 | before DAVE | 4022 on a moderator's disconnect | JDA's Resume, with `seq_ack` | 20 ms after the Resume |

The two disgo Resumes sent `seq` and no `seq_ack`, so neither tests a well-formed Resume. The JDA one went out after a moderator had taken the bot out of the channel, so its session had already ended. In #534 the DAVE session was already failing before the drop, with 1,836 "failed to DAVE decrypt packet" lines from 00:23:27 on. No source shows 4009 on a resume, and none shows how old a session can be and still resume.

### What #534 shows about the Identify after the refusal

disgo waited 2.0 s, then opened a new websocket and sent Identify with the same `session_id` and `token` ([disgo#534's log][dg534-log]):

| After the Identify | Message |
|---|---|
| 0 | Identify sent, 00:26:40.759 |
| 25 ms | opcode 2 Ready |
| 50 ms | Select Protocol sent, then opcodes 11, 5, 18, 20 and 12 about the members already in the call |
| 82 ms | opcode 4 Session Description, then the key package (26) sent and opcode 25 received |
| 132 ms | opcode 30 Welcome |
| 134 ms | opcode 23 sent |
| 204 ms | opcode 22 Execute Transition |

On the test guild, Hello to `epoch activated` took about 0.44 s through the recorder's own commit. Here a Welcome got there in 204 ms. The issue reported no sound afterwards. [disgo#536][dg536] traced that to disgo's own UDP handling: "The Identify path produces a new Ready → new UDP socket, but the audio sender/receiver goroutines were still reading from the old one." The log ends at the opcode 22, so it can't show sound coming back.

## What the maintained libraries do

| Library | Resumes on | Other closes | Identifies with the held token on a new websocket |
|---|---|---|---|
| @discordjs/voice | 4015 or any code below 4000, only while Ready, one try ([Networking.ts lines 442 to 455][djs-close]) | 4014 stops, and on unreleased main so do 4021 and 4022. Anything else sends opcode 4 for the same channel and builds a new connection that identifies ([VoiceConnection.ts lines 475 to 500][djs-rejoin]) | Not on its own. Unreleased [#11581][djs11581] identifies with the cached token when the rejoin gets only a voice state |
| discord.py | 4015 only. It first re-sends opcode 4 and waits for both voice events, and leaves if the whole resume takes over 30 s ([voice_state.py lines 715 to 732][dpy-4015], [lines 468 to 477][dpy-inner]) | 1000 and 4021 leave. 4014 and 4022 wait for a new voice server, then identify. Anything else, including 4006 and 4009, backs off 0 to 2 s, leaves the channel and rejoins ([lines 734 to 756][dpy-generic]) | No |
| songbird | 4009, 4015, unknown 4xxx, codes outside 4xxx, transport errors ([ws.rs lines 530 to 557][sb-classify], [voice-model close_code.rs lines 52 to 54][vm-resume]) | 4006 and the other known codes end the connection, and the application must rejoin | Yes. A failed Resume falls through to a new connection with the same endpoint, session and token, up to 6 attempts with backoff ([tasks/mod.rs lines 168 to 206][sb-fallback], [connection/mod.rs lines 65 to 81][sb-identify]) |
| JDA | every server close not listed, 4009 included, and client closes other than 1000, with no limit ([AudioWebSocket.java lines 316 to 350][jda-close]) | 4006, 4011, 4015, 4021 leave and rejoin through the main gateway. 4014 and 4022 leave | No. Identify happens only on a new audio connection ([lines 282 to 286][jda-identify]) |
| koe 3.1.0 | 1001, 1006, 4000 to 4003, 4005, 4009, 4012, 4015, 4016, 4020, up to 3 tries ([AbstractMediaGatewayConnection.java lines 461 to 512][koe-close]) | Reported to the application, which must rejoin ([KoeEventListener.java lines 29 to 32][koe-listener]) | Yes, after a 4006 on a session that reached Ready. Added 2026-10-06. Lavalink's koe 3.0.0 passes 4006 to the application instead ([3.0.0 lines 146 to 177][koe300-close]) |
| disgo | codes its table marks Reconnect, plus read errors. The first attempt resumes if it holds an SSRC and a sequence number ([gateway.go lines 545 to 559][dg-choice], [gateway_opcodes.go lines 54 to 245][dg-codes]) | 4006, 4009, 4011 send opcode 4 for a fresh session. 4004, 4014, 4016, 4017, 4021, 4022 leave ([conn.go lines 309 to 344][dg-conn-close]) | Yes. An attempt that got a websocket and failed clears the resume data, so attempts 2 to 5 identify ([gateway.go lines 240 to 247][dg-clear]) |

What each does with DAVE:

- **@discordjs/voice** keeps the same DAVESession object through a resume. While resuming it drops the binary DAVE opcodes 25, 27, 29 and 30 ([Networking.ts lines 585 to 586][djs-binary]) and won't send opcode 26 or 31 ([lines 621 to 624][djs-keypkg]). A new connection creates a new session on the Session Description and sends opcode 26 ([lines 519 to 539][djs-newdave]). The receiver decrypts DAVE only while the voice connection is Ready, and a resume puts it in Connecting ([VoiceReceiver.ts lines 184 to 196][djs-receiver]). [Chronote#374][chronote374] (merged 2026-09-30) patched that locally after a synthetic test showed DAVE frames reaching the Opus decoder during a resume.
- **discord.py** keeps its DAVE state on the connection object, outside the websocket, and opcode 9 touches nothing. A Session Description runs `reinit_dave_session`, which sends opcode 26 ([gateway.py lines 1009 to 1014][dpy-sessdesc], [voice_state.py lines 275 to 291][dpy-reinit]). discord.py has no receive path. discord-ext-voice-recv, the receive extension, has no DAVE decryption at its last commit.
- **songbird** creates its DaveSession and sends opcode 26 only inside a new connection's Session Description handling ([connection/mod.rs lines 376 to 397][sb-dave]). A resume keeps it.
- **JDA with JDAVE.** JDA calls `daveSession.initialize()` on every Hello, resumes included ([AudioWebSocket.java lines 480 to 487][jda-hello]), and JDAVE's `initialize` is empty ([JDaveSession.java lines 88 to 89][jdave-init]). JDA's DaveSession interface has no reset or resume method ([DaveSession.java lines 163 to 173][jda-daveiface]). A main-gateway rejoin destroys the session and builds a new one.
- **koe** keeps its DAVEManager through a resume. On the 4006 Identify it reinitialises the same libdave session and sends opcode 26 from the Session Description ([DAVEManager.java lines 170 to 181][koe-dave]). koe has no receive path.
- **disgo with dave-go v0.5.1** makes no DAVE call on Resume or Resumed. On a Session Description dave-go's `OnSelectProtocolAck` resets all group state and sends a new key package, under the comment "select_protocol_ack always marks a new voice connection" ([session.go lines 547 to 583][davego-ack]).
- **libdave** has no "resume" or "reconnect" anywhere in its code, samples or docs. Its sample session manager initialises on the Session Description and resets only on a transition to protocol version 0 ([DaveSessionManager.ts lines 46 to 49 and 148 to 182][ld-sample]).

Adding DAVE changed no library's resume rules. discord.js added DAVE in #10921 (2025-07-13), discord.py in #10300 (2026-01-07), songbird in #291 (2026-03-28), JDA in #2988 (2026-01-07) and koe in #41 (2026-02-24). None of those diffs touched the close-code rules or the Resume payload. The review threads of the first four never mention resume. I didn't check koe's.

## Reports of voice resume with DAVE

I searched the trackers of discord.js and davey, discord.py and discord-ext-voice-recv, songbird, JDA and JDAVE, koe and Lavalink, disgo, godave, dave-go, libdave and discord-api-docs, and GitHub as a whole for "voice resume DAVE", "seq_ack" and "4006 resume voice". Nothing reports opcode 9 with DAVE on. These are the reports that come closest:

- [disgo#534][dg534], 2026-03-25 to 2026-06-27. Resume after a 1006 refused with 4006, Identify with the held token accepted. Details above. The maintainer wrote on 2026-03-25 that "a 4006 is usually not recoverable and you are required to do a full voice reconnect in those cases".
- [shi-gg/linkdave#83][linkdave83], merged 2026-06-18. The same disgo Resume refused with 4006, 36 ms after it went out.
- [disgo#578][dg578], merged 2026-08-05, by dave-go's author: "I ran into this in production with a `4006` (session no longer valid) right after a `1006`".
- [dave-go#52][davego52], merged 2026-10-13. Adds the watchdog for a resumed connection described above. It reasons from the protocol and reports no capture.
- [Chronote#374][chronote374], merged 2026-09-30. The discord.js receive bug during a resume, from a synthetic test. It says "This reproduction establishes a library bug, not the cause of the two observed production incidents."
- [discord-ext-voice-recv#59][vr59], open since 2026-07-27. A stale transport key after a 0.7 s reconnect. The reporter can't say which path, "I don't have visibility into every reconnect path (RESUME vs full re-identify) that could leave the key stale."
- [JDA#2853][jda2853], 2025-05-22 to 2025-06-07, before DAVE. Resume after a 4022 refused with 4006, then a main-gateway rejoin.

Two DAVE receive failures recover only by a fresh join, and neither involves a resume. They are [songbird#310][sb310] (2026-07-16, "A fresh leave+rejoin re-rolls the dice and usually lands a working session within 1–3 attempts") and [discord.js#11653][djs11653] (2026-10-08, a session that joined through a welcome at transition 0 never recovers from decrypt failures).

## What the sources don't settle

- Whether Discord accepts a well-formed v8 Resume, with `seq_ack`, from a DAVE client after a severed websocket. Every refusal on record either carried disgo's `seq` field or came after the session had ended, and no success is on record.
- Whether a resumed client is still in the MLS group, and whether the SFU forwards it media during and after the gap. Only a capture of a successful resume would show it.
- When the gateway sends the removal proposal after a severed websocket, and whether opcode 13 Client Disconnect goes to the other members for a drop the client resumes.
- How long a session stays resumable, and which close a refused resume brings apart from 4006.
- What Discord sends after opcode 9 besides replayed messages, and whether opcodes 13, 21 and 24 carry a sequence number.
- How long the recorder would be deaf on either path. For the Identify path there are two measurements, the test guild's 0.44 s and #534's 204 ms from Identify to opcode 22. For the Resume path there are none.

<!-- Links -->

[i817]: https://github.com/7Cav/cavbot2/issues/817
[i810]: https://github.com/7Cav/cavbot2/issues/810
[cb-comment]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L120-L132
[cb-sequences]: https://github.com/7Cav/cavbot2/blob/9a02508775e024e0e73924aa336f03f50f6f7d61/docs/research/discord-voice-sequences.md

[dd-versions]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L23-L32
[dd-ready]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L102-L118
[dd-udp]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L208-L225
[dd-binary]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L242-L252
[dd-extsender]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L294-L302
[dd-keypkg]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L308-L310
[dd-invalid]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L330-L334
[dd-sessdesc]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L436-L452
[dd-resume]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L506-L551
[dd-buffered]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L553-L580
[dd-close]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx#L86-L106
[dd7075]: https://github.com/discord/discord-api-docs/pull/7075
[dd7156]: https://github.com/discord/discord-api-docs/pull/7156

[p-pending]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L192-L196
[p-keypkg]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L198-L206
[p-add]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L218-L226
[p-remove]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L238-L255
[p-recovery]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L319-L329

[djs-close]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/Networking.ts#L442-L455
[djs-resume]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/Networking.ts#L422-L431
[djs-newdave]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/Networking.ts#L519-L539
[djs-resumed]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/Networking.ts#L540-L545
[djs-binary]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/Networking.ts#L585-L586
[djs-keypkg]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/Networking.ts#L621-L624
[djs-seq]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/networking/VoiceWebSocket.ts#L153-L155
[djs-rejoin]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/VoiceConnection.ts#L475-L500
[djs-receiver]: https://github.com/discordjs/discord.js/blob/03777df7f7cc372de88fd05cd3999ab59e5c36c0/packages/voice/src/receive/VoiceReceiver.ts#L184-L196
[djs11581]: https://github.com/discordjs/discord.js/pull/11581
[djs11653]: https://github.com/discordjs/discord.js/issues/11653
[chronote374]: https://github.com/Chronote-gg/Chronote/pull/374

[dpy-4015]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_state.py#L715-L732
[dpy-inner]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_state.py#L468-L477
[dpy-generic]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_state.py#L734-L756
[dpy-reinit]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/voice_state.py#L275-L291
[dpy-resume]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L894-L905
[dpy-v8]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L931
[dpy-seq]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L1000
[dpy-resumed]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L1007-L1008
[dpy-sessdesc]: https://github.com/Rapptz/discord.py/blob/65232c38702be5844cf2ce865a4777eb1928b5d0/discord/gateway.py#L1009-L1014
[dpy10210]: https://github.com/Rapptz/discord.py/pull/10210
[vr59]: https://github.com/imayhaveborkedit/discord-ext-voice-recv/issues/59

[sb-classify]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/tasks/ws.rs#L530-L557
[sb-fallback]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/tasks/mod.rs#L168-L206
[sb-identify]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/connection/mod.rs#L65-L81
[sb-resume]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/connection/mod.rs#L301-L307
[sb-reconnect-inner]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/connection/mod.rs#L312-L341
[sb-dave]: https://github.com/serenity-rs/songbird/blob/957e17fcbc687cd88979c90b76bdf79017803b09/src/driver/connection/mod.rs#L376-L397
[vm-resume]: https://github.com/serenity-rs/voice-model/blob/cef32674d5ad6c539b1ee2ff9dcda39b6d71e18f/src/close_code.rs#L52-L54
[vm-v4]: https://github.com/serenity-rs/voice-model/blob/cef32674d5ad6c539b1ee2ff9dcda39b6d71e18f/src/constants.rs#L4
[sb247]: https://github.com/serenity-rs/songbird/issues/247
[sb310]: https://github.com/serenity-rs/songbird/issues/310

[jda-close]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L316-L350
[jda-identify]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L282-L286
[jda-seq]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L477
[jda-hello]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L480-L487
[jda-resumed]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L533-L540
[jda-resume]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/internal/audio/AudioWebSocket.java#L660-L668
[jda-daveiface]: https://github.com/discord-jda/JDA/blob/7c900019ca511a994087320ce98d19cd79a2c4b0/src/main/java/net/dv8tion/jda/api/audio/dave/DaveSession.java#L163-L173
[jdave-init]: https://github.com/MinnDevelopment/jdave/blob/5d3585f52e80fecdfd4f4bdfcd817c9b30f95f67/api/src/main/java/club/minnced/discord/jdave/interop/JDaveSession.java#L88-L89
[jda2853]: https://github.com/discord-jda/JDA/issues/2853

[koe-close]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/internal/gateway/AbstractMediaGatewayConnection.java#L461-L512
[koe-resumed]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/internal/gateway/AbstractMediaGatewayConnection.java#L307-L312
[koe-resume]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/internal/gateway/MediaGatewayV8Connection.java#L49-L52
[koe-seq]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/internal/gateway/MediaGatewayV8Connection.java#L65-L67
[koe-dave]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/internal/DAVEManager.java#L170-L181
[koe-listener]: https://github.com/KyokoBot/koe/blob/e9147ed2f486bf5ab6cf9e29fb90331c55974230/core/src/main/java/moe/kyokobot/koe/KoeEventListener.java#L29-L32
[koe300-close]: https://github.com/KyokoBot/koe/blob/7436cf8ea57ddbc9348e261b1f377c78b6b98530/core/src/main/java/moe/kyokobot/koe/internal/gateway/AbstractMediaGatewayConnection.java#L146-L177
[koe40]: https://github.com/KyokoBot/koe/pull/40

[dg-msg]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L12-L26
[dg-resume-msg]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L307-L312
[dg-clear]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L240-L247
[dg-choice]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L545-L559
[dg-resumed]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L573-L577
[dg-codes]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_opcodes.go#L54-L245
[dg-conn-close]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L309-L344
[dg534]: https://github.com/disgoorg/disgo/issues/534
[dg534-log]: https://github.com/user-attachments/files/26245725/Logs-2026-03-25.09_10_17.txt
[dg536]: https://github.com/disgoorg/disgo/pull/536
[dg578]: https://github.com/disgoorg/disgo/pull/578
[linkdave83]: https://github.com/shi-gg/linkdave/pull/83
[davego-ack]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L547-L583
[davego52]: https://github.com/thomas-vilte/dave-go/pull/52

[ld-sample]: https://github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/samples/typescript/DaveSessionManager.ts#L46-L182
