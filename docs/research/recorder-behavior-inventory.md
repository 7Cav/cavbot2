# What the current recorder's voice connection handles

Research for [Inventory what the current recorder handles (#811)](https://github.com/7Cav/cavbot2/issues/811), written 2026-10-18. It feeds [Wayfinder map: owned voice connection for recorders (#810)](https://github.com/7Cav/cavbot2/issues/810), whose spec replaces the recorder's voice connection handling with one state machine we own.

The question: which behaviors of the recorder's voice connection must the replacement keep? For each one, the trigger, what the recorder does, what members see, and the test or smoke check that covers it today.

## Answer

1. **Thirty-nine behaviors, plus two smoke hooks.** They fall into recorder sessions (S), join and leave (J), what the recorder hears (H), gaps (G), drops (D), moves (M), DAVE (V), the recorder's own leave (L) and logs (O). The smoke hooks (K) matter because three smoke checks can't run without them. Each entry below gives the trigger, what the recorder does, what members see, and what covers it.
2. **What members get from them.** A reconnect never ends a recording, since the runtime sees one connection from join to leave (J5). The recorder tells the recording at once when it stops hearing and when it hears again, and dates the stop from the drop (G1). A gap past 10 s gets a notice and a Sentry event, and one of 10 minutes ends the recording (G2, G3). Every recovery identifies afresh and never resumes, because no resume has brought audio back (D1, D3, M1). The recorder follows the voice server Discord named last and never goes back (M2 to M4). It has no time limit of its own while it tries (D4, D6, M5). A DAVE setup that stalls starts over within about 3 s (V1).
3. **Sixteen behaviors only a fake has covered.** S4, S5, J2, J3, H5, H6, D2, D4, D5, D6, D7, M3, M4, M6, L1 and O2. Four more have a part only a fake has covered: J4's stop during a rejoin or a stall, H4's burst report, M5's zone-nameserver fallback and M7's 4022. The one that matters most is D4 with D5. They are the same-server drop recovery that merged today with its smoke pass owed, so no log yet shows the recorder getting its call back after a drop on the same voice server. D2's closes and D6's gateway outage have never happened during a pass.
4. **How the runtime tells the recorder's own leave from a moderator's disconnect.** `Conn.OwnLeaves` counts the leaves the recorder makes to join again, counted before each goes out. For 10 s after the runtime reads the count go up, the recorder's absence from the channel doesn't stop the recording (L1). Any other absence stops it as `recorder_left` (L2). Only D5 makes such a leave. No issue states the rule. It came with #809. L3 lists what the 10 s window costs and the races no test pins.

The section at the end lists what today's recorder leaves open.

## Sources

**Code.** `voice/` and the recording runtime in `commands/recording*.go` on `develop` at `82c3df1`, which is [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809). Where an entry says what disgo does on its own, it is disgo at the pinned `v0.19.7-0.20260923193941-995ee20fe0d6`, `voice/conn.go` and the close table in `voice/gateway_opcodes.go`.

**Tests.** `voice/*_test.go`, the recording runtime's `commands/record_test.go`, `commands/recording_gap_test.go`, `commands/recording_notice_test.go` and `commands/recording_recovery_test.go`, and `utils/gateway_watch_test.go`. All of them pass under `go test -race -count=1` at `82c3df1`. None of them talks to Discord:

- Most of `voice/gateway_test.go` runs disgo's own voice manager and connection, built as the recorder builds them, over a scripted stand-in for disgo's voice gateway (`answeringGateway`). The test plays Discord around it: the answer to each voice state update, the voice servers, and the channel lookup.
- `voice/drop_test.go` runs disgo's real voice gateway over real sockets against a voice server on localhost that the test scripts.
- `voice/receiver_test.go` reads a fake voice connection. `voice/udp_test.go` reads a real UDP socket fed by the test.
- The runtime's tests use a fake recorder (`fakeVoiceConn`) and a fake state cache.

**Smoke checks.** The smoke checks in each voice PR's body and the passes posted on it, read with the same filter `.github/scripts/fetch-issue.sh` applies, so only text by people who can push to the repo counts. The maintainer wrote every check and pass used here. Each build is `develop` at that commit with a clean tree, and every run keeps Sentry off, so a pass reads the ERROR log line in place of the Sentry event. An entry cites a check as the PR's number and the check's number, linked to the pass. #751, #760 and #780 have no smoke checks.

| PR | Smoke passes |
|---|---|
| [feat(voice): recorder accounts connect at startup (#571)](https://github.com/7Cav/cavbot2/pull/571) | [`smoke-98750e0`](https://github.com/7Cav/cavbot2/pull/571#issuecomment-6173834125) |
| [feat(voice): a recorder joins on /record start and leaves on /record stop (#572)](https://github.com/7Cav/cavbot2/pull/572) | [`smoke-98750e0`](https://github.com/7Cav/cavbot2/pull/572#issuecomment-6176550615) |
| [feat(voice): one track per speaker (#573)](https://github.com/7Cav/cavbot2/pull/573) | [`smoke-98750e0`](https://github.com/7Cav/cavbot2/pull/573#issuecomment-6176551167) |
| [feat(voice): recording notice and automatic stops (#574)](https://github.com/7Cav/cavbot2/pull/574) | [`smoke-98750e0`](https://github.com/7Cav/cavbot2/pull/574#issuecomment-6176551689) |
| [feat(recording): close a recording cut short by a restart (#591)](https://github.com/7Cav/cavbot2/pull/591) | [`smoke-98750e0`](https://github.com/7Cav/cavbot2/pull/591#issuecomment-6174702466) |
| [fix(voice): report a gateway session that stays offline to Sentry (#654)](https://github.com/7Cav/cavbot2/pull/654) | [`smoke-98750e0`](https://github.com/7Cav/cavbot2/pull/654#issuecomment-6176554255), [`smoke-e75355c`](https://github.com/7Cav/cavbot2/pull/654#issuecomment-6179821483) |
| [fix(voice): keep recording when the call moves to another voice server (#728)](https://github.com/7Cav/cavbot2/pull/728) | [`smoke-e75355c`](https://github.com/7Cav/cavbot2/pull/728#issuecomment-6179685113), [`smoke-df64b83`](https://github.com/7Cav/cavbot2/pull/728#issuecomment-6183158042) |
| [fix(gateway): stop a session whose token Discord refuses, and report it (#729)](https://github.com/7Cav/cavbot2/pull/729) | [`smoke-e75355c`](https://github.com/7Cav/cavbot2/pull/729#issuecomment-6179820439) |
| [fix(voice): start DAVE over when its setup stalls mid-recording, and report it (#734)](https://github.com/7Cav/cavbot2/pull/734) | [`smoke-df64b83`](https://github.com/7Cav/cavbot2/pull/734#issuecomment-6183157755) |
| [fix(voice): follow a moved call whose server Discord named before the rejoin (#735)](https://github.com/7Cav/cavbot2/pull/735) | [`smoke-df64b83`](https://github.com/7Cav/cavbot2/pull/735#issuecomment-6183157450) |
| [fix(voice): keep recording through a close disgo answers with a fresh connection (#753)](https://github.com/7Cav/cavbot2/pull/753) | [`smoke-68f77d4`](https://github.com/7Cav/cavbot2/pull/753#issuecomment-6228935419) |
| [fix(voice): keep recording through a move to a voice server the recorder can't reach yet (#759)](https://github.com/7Cav/cavbot2/pull/759) | [`smoke-c5f9692`](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024) |
| [fix(voice): a dropped voice connection starts its gap at once, and the recorder rejoins instead of resuming (#781)](https://github.com/7Cav/cavbot2/pull/781) | [`smoke-c5f9692`](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480) |
| [fix(voice): a recorder dropped on the same voice server reconnects (#809)](https://github.com/7Cav/cavbot2/pull/809) | none yet, `needs-smoke` |

**Issues.** The spec, [Voice recording v1: record voice channels in-house (spec for #10) (#381)](https://github.com/7Cav/cavbot2/issues/381), its slices #384 to #394, and every closed and open issue on the recorder's connection: #617, #624, #626, #627, #681, #726, #727, #730, #731, #732, #733, #736, #738, #767, #768, #769, #772, #786 and #805, read with `fetch-issue.sh`. Each fixed issue holds the maintainer's brief, and an entry quotes a brief where it sets a rule the code alone doesn't explain.

## How to read an entry

Each entry has an ID the spec can cite. Its lines say:

- Trigger: what happens in Discord or in the bot.
- Recorder: what the recorder does, with the numbers the code holds.
- Members: what someone in the channel or its chat sees.
- Tests: the tests that pin it.
- Smoke: the smoke checks that have seen it against Discord, and the build they ran on.

An entry marked **Fake only** has been covered by tests against a fake and by no smoke pass against Discord.

Two terms come from the code. Discord **names** a voice server when it sends a `VOICE_SERVER_UPDATE` with an endpoint and a token for the call. The recorder **rejoins** when it sends a voice state update for the channel it is already in, to get a fresh voice connection there, which Discord doesn't show members as a leave.

## Recorder sessions

### S1. One gateway session per recorder token

- Trigger: the bot starts with `RECORDER_TOKENS` set.
- Recorder: opens one discordgo session per comma-separated token, asking only for the Guilds and GuildVoiceStates intents, neither privileged. A session counts as open once READY names its user. A token that fails to open goes to Sentry as "Recorder failed to connect", named by its place in the list from 1 and never by its text, and the recorders on either side of it still connect. A session that opened without READY is closed for good, so a failed recorder logs in no more until the bot restarts. With no token the log gets a WARN "RECORDER_TOKENS not set, recording off" and nothing opens. `smoke up` counts these three lines, one per token before "Bot is now running", to say whether recording checks can run ([#681](https://github.com/7Cav/cavbot2/issues/681)), so the replacement keeps them word for word.
- Members: each recorder account shows online. With no recorder, `/record start` is refused as recording off.
- Tests: `TestEachTokenConnectsOneRecorder`, `TestABadTokenIsReportedAndTheOtherRecordersConnect`, `TestNoTokenConnectsNoRecorder`, `TestEachRecorderOpensAsItsPlaceInTheList`. `OpenGateway` itself has no unit test, since only it needs Discord.
- Smoke: [#571](https://github.com/7Cav/cavbot2/pull/571#issuecomment-6173834125) checks 2, 3 and 5 on `smoke-98750e0`: both recorders logged `Recorder connected`, a bad second token logged `Recorder failed to connect` with `close 4004: Authentication failed` while recorder 1 stayed online, and an empty `RECORDER_TOKENS` logged recording off once. [#729](https://github.com/7Cav/cavbot2/pull/729#issuecomment-6179820439) check 2 on `smoke-e75355c` saw a stale token fail at startup and log in no more for the 20 minutes the run was up. That was before #751 closed a failed session for good, and #751 has no smoke check.

### S2. A recorder's gateway outage reaches Sentry, and a refused token stops its logins

- Trigger: a recorder's gateway session disconnects, other than at shutdown, and gets no READY or RESUMED for a minute (`utils.GatewayOfflineAfter`). Or Discord closes it with 4004 after an identify.
- Recorder: one Sentry event per outage, naming the recorder by its place and its user ID, never its token, and an INFO "Gateway session back online" when a reported outage ends. An outage that ends within the minute sends nothing. discordgo keeps retrying meanwhile. A 4004 stops the logins and logs "Discord refused the gateway session's token" once.
- Members: nothing directly. A recording whose recorder's gateway is down hears the gap of D6.
- Tests: `TestARecorderOfflineIsNamedByItsPlaceAndUserIDNeverItsToken`, `TestRecorderCloseSendsNoOutage`, and `utils/gateway_watch_test.go`, which plays Discord's Op 9 then 4004 against a scripted gateway.
- Smoke: [#654](https://github.com/7Cav/cavbot2/pull/654#issuecomment-6179821483) check 1 and [#729](https://github.com/7Cav/cavbot2/pull/729#issuecomment-6179820439) check 1, both on `smoke-e75355c`: a reset token got a 4003, an Op 9 and a 4004 within 1.5 s, one refused-token line, no more logins, and the offline ERROR 61 s after the drop, with none more in 10 minutes. Every smoke run keeps Sentry off, so the ERROR line stands in for the event. The [first run](https://github.com/7Cav/cavbot2/pull/654#issuecomment-6176554255), on `smoke-98750e0`, found the logins never stopping, which became #727.

### S3. Shutdown takes every recorder out of voice

- Trigger: SIGTERM.
- Recorder: the runtime closes every running recording's tracks first and leaves its row open. Then each recorder session's outage watch stands down, its voice manager closes every connection, which sends the leave with a 5 s bound, and the session closes. Sentry hears of no outage.
- Members: the recorder leaves the channel. The recording notice stays as it is until the next process edits it to say the recording was cut short.
- Tests: `TestRecorderCloseLeavesVoice`, `TestCloseClosesEveryRecorderSession`, `TestRecorderCloseSendsNoOutage`.
- Smoke: [#571](https://github.com/7Cav/cavbot2/pull/571#issuecomment-6173834125) check 4 on `smoke-98750e0`: the recorder showed offline a few seconds after `smoke down`. [#591](https://github.com/7Cav/cavbot2/pull/591#issuecomment-6174702466) check 1 on the same build found the recorder out of voice after a restart mid-recording.

### S4. A recorder the last process left in voice is taken out at startup

- Trigger: startup recovery finds a recorder in a voice channel that no recording of this process holds.
- Recorder: the bot's own session disconnects it. One that won't go reaches Sentry as "Recorder not disconnected at recovery".
- Members: the recorder leaves the channel after the restart.
- Tests: `TestRecordingRecoveryLeavesNoRecorderInVoice`.
- Smoke: None that tells it apart from S3. [#591](https://github.com/7Cav/cavbot2/pull/591#issuecomment-6174702466) check 1 on `smoke-98750e0` found the recorder out of voice after a restart, but the shutdown's own leave does that. Only a crash leaves a recorder in voice, and no pass has crashed the bot mid-recording. **Fake only.**

### S5. A voice state update goes out only while the recorder's gateway is connected

- Trigger: a join, leave or rejoin while the recorder's gateway session is down, or after its logins stopped.
- Recorder: the update returns an error instead of writing to a closed websocket. A discordgo write to none panics while holding the session's write lock, which would hold up every later write, heartbeats included. A panic inside the update comes back as an error, since disgo calls it from its own goroutines. A join through a session whose logins stopped fails as a join Discord never completes.
- Members: the starter gets `/record start`'s ❌ failed reply.
- Tests: `TestRecorderVoiceStateUpdateWithTheGatewayDownIsAnError`, `TestRecorderJoinWithItsGatewayStoppedFails`.
- Smoke: None. #751 has no smoke check, since Discord can't be made to refuse an identify on demand. **Fake only.**

## Join and leave

### J1. The recorder joins self-muted, never self-deafened, with DAVE

- Trigger: `/record start`, which reserves the first free recorder in configured order.
- Recorder: sends a voice state update naming the channel with `self_mute` true and `self_deaf` false, since Discord sends a deafened member no audio. disgo connects with a dave-go DAVE session, so a channel that requires end-to-end encryption lets the recorder in. The join returns once the session description arrives, within 30 s (`recordingJoinTimeout`), while the command's reply waits deferred.
- Members: the recorder appears in the channel, muted, and then the recording notice posts.
- Tests: `TestRecorderJoinOpensUndeafenedAndLeaveCloses`, `TestJoinGoesThroughTheNamedRecorder`, `TestRecordStartJoinsARecorderAndWritesItsRow`, `TestVoiceStateEventCarriesWhatDisgoConnectsOn`.
- Smoke: [#572](https://github.com/7Cav/cavbot2/pull/572#issuecomment-6176550615) check 1 on `smoke-98750e0`: the recorder joined a DAVE call self-muted and not deafened, with no `Recorder failed to join` and no 4017, and stayed 2 minutes. Check 3 saw the second recorder join a locked spawned channel.

### J2. A join that fails leaves again and frees the recorder

- Trigger: Discord doesn't complete the join within 30 s, or the join errors.
- Recorder: closes the connection, which sends a leave with a 5 s bound, so it isn't left half in the channel. Sentry hears "Recorder failed to join". The recorder is free for the next start.
- Members: the starter gets the ❌ failed reply. No notice posts and no row is written.
- Tests: `TestRecorderJoinThatTimesOutLeavesAgain`, `TestRecordStartJoinFailureIsReportedAndFreesTheRecorder`.
- Smoke: None. **Fake only.**

### J3. Frames reach the recording only once its row is written

- Trigger: a join that succeeded.
- Recorder: the runtime posts the notice, writes the row, and only then calls `Receive`. Frames that arrived before wait in the UDP socket. A notice that won't post, or a row that won't write, has the recorder leave, and the notice goes with it.
- Members: nothing unless the start fails, when the recorder leaves and the starter gets the ❌ failed reply.
- Tests: `TestRecordingNoticeThatFailsToPostFailsTheStart`, `TestRecordStartRowWriteFailureLeavesNoRecorderOrNoticeInTheChannel`.
- Smoke: None. **Fake only.**

### J4. A leave stops the frames first, and nothing follows it

- Trigger: any stop: a member's `/record stop` or Stop button, the 6-hour cap, an empty channel, low disk, the recorder leaving (L2), the 10-minute gap limit (G3) or the channel's delete (M7).
- Recorder: stops the receiver, so no frame is handed over after `Leave` returns, then sends the leave and closes the voice gateway, UDP socket and DAVE session, with a 5 s bound. From then on the connection sends no voice state update but that one leave, since a later leave names no channel and would take the recorder out of a recording started since. A rejoin under way ends, and so does any dialing. The connection's log goes above ERROR, so what disgo logs about the abandoned work never reaches the bot log. The recorder is free at once, and a recording started on it while an abandoned rejoin still waits keeps it in.
- Members: the recorder leaves, and the recording notice is edited to say the channel is no longer being recorded. A running gap's notice is edited to say the recording stopped before the recorder got back.
- Tests: `TestRecorderJoinOpensUndeafenedAndLeaveCloses`, `TestRecorderStoppedDuringARecoveryLeavesAtOnceAndReportsNothingMore`, `TestRecorderStoppedWhileARejoinWaitsSendsNothingMore`, `TestRecorderStoppedWhileARejoinTriesAServerItCantReachLogsNoError`, `TestRecorderStoppedDuringTheChannelLookupSendsNoRejoin`, `TestRecorderStoppedRightAfterAStateOnlyAnswerOpensNothing`, `TestRecorderStartedWhileAnAbandonedRejoinWaitsStaysIn`, `TestRecordingStopDuringAGapEditsItsNotice`, `TestRecordingStopsByItself`.
- Smoke: [#572](https://github.com/7Cav/cavbot2/pull/572#issuecomment-6176550615) check 2 and [#574](https://github.com/7Cav/cavbot2/pull/574#issuecomment-6176551689) checks 4 and 5 on `smoke-98750e0` saw the recorder leave at a member's stop and at an empty channel. [#759](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024) check 3 on `smoke-c5f9692` saw a stop during a gap edit the gap notice to ⏹️. A stop during a rejoin or a stall has never run: [#735](https://github.com/7Cav/cavbot2/pull/735#issuecomment-6183157450) check 2 found no rejoin to stop, and [#734](https://github.com/7Cav/cavbot2/pull/734#issuecomment-6183157755) check 3 found no stall long enough. Those parts are **fake only.**

### J5. The recording sees one connection from join to leave

- Trigger: anything the recorder does to get the call back: a rejoin after a drop or a move, a fresh connection after a DAVE stall, a leave and join for a refused token.
- Recorder: keeps the same `Conn` and the same `Listener` throughout. The runtime hears of it only as a gap, and through `OwnLeaves` for a leave of its own (L1). The spec in [#381](https://github.com/7Cav/cavbot2/issues/381) puts it as "Voice reconnects are absorbed here and never reach the runtime."
- Members: one recording, one notice and one track per speaker through every reconnect. Only what was said during the gap is missing.
- Tests: drop and move tests that check the recorder never leaves and hears the channel again through the connection it joined with, such as `TestRecorderKeepsRecordingThroughACloseThatGetsAFreshConnection`, `TestRecorderIdentifiesWithTheTokenItHeldAfterADropOnTheSameVoiceServer` and `TestRecorderConnectsOnceTheServerItsCallMovedToIsReachable`.
- Smoke: [#728](https://github.com/7Cav/cavbot2/pull/728#issuecomment-6183158042) check 1 and [#753](https://github.com/7Cav/cavbot2/pull/753#issuecomment-6228935419) check 2 found one track with words after each move.

## What the recorder hears

### H1. A frame is one speaker's Opus packet with both encryptions off

- Trigger: a voice packet arrives on the UDP socket.
- Recorder: its own UDP connection takes the transport encryption off, with the header extension and any RTP padding, and leaves DAVE's on. The receiver takes DAVE's off as the speaker the SSRC maps to, and hands over the user ID, SSRC, RTP timestamp and Opus payload. A datagram that isn't a voice packet, or carries no media, is skipped. A speaker who rejoins comes back on a new SSRC.
- Members: one track per speaker.
- Tests: `TestUDPConnReadsAPacketAsDiscordsServerSendsIt`, and every receiver test.
- Smoke: [#573](https://github.com/7Cav/cavbot2/pull/573#issuecomment-6176551167) checks 1 and 4 on `smoke-98750e0`: two people talking at once gave two tracks, each with only its own speaker, aligned within 20 ms, and a speaker on the desktop app reached their track.

### H2. Frames that arrive before op 5 are held

- Trigger: a speaker's first packets arrive before op 5 maps their SSRC to them.
- Recorder: holds up to 250 frames per SSRC, 5 s of audio, dropping the oldest first. Once the SSRC maps they go over as that speaker, in order, ahead of what follows, also when nothing follows.
- Members: words said while joining are in the speaker's track.
- Tests: `TestReceiverHoldsFramesUntilTheirSSRCMaps`, `TestReceiverHandsOverHeldFramesOnceTheSSRCMapsWithNothingAfter`.
- Smoke: [#573](https://github.com/7Cav/cavbot2/pull/573#issuecomment-6176551167) check 6 on `smoke-98750e0`: each of five members who spoke as they joined had their speech start in the track 0.3 to 0.8 s after the join. Nothing logged whether frames were held.

### H3. A keepalive leaves the UDP socket every 5 s

- Trigger: the UDP socket opens.
- Recorder: sends an 8-byte little-endian counter at once and every 5 s after, as Discord's own JavaScript library does. A recorder never sends audio, so without it nothing would leave the socket through a long silence, and a NAT can stop delivering audio then. IP discovery has 5 s. A read waits at most 100 ms, so the receiver looks at its held frames and its stop that often.
- Members: audio still arrives after a long silence.
- Tests: `TestUDPConnSendsAKeepaliveOnceOpen`, `TestUDPConnReadTimesOutWithNothingSent`.
- Smoke: [#573](https://github.com/7Cav/cavbot2/pull/573#issuecomment-6176551167) check 3 on `smoke-98750e0`: after 16 min 42 s of silence the speaker's words came back 0.3 s after the unmute. The pass can't say whether the keepalive did that or no NAT was in the way.

### H4. A burst of failures reaches Sentry once

- Trigger: 25 frames in a row from one speaker that DAVE can't decrypt, or 25 datagrams in a row the UDP connection can't read.
- Recorder: one Sentry event per burst, "DAVE decrypt failures" naming the speaker or "Voice packets unreadable". When that speaker decrypts again, an INFO "DAVE decrypt recovered" says how many frames were lost.
- Members: that stretch of the speaker's audio is missing. No notice posts.
- Tests: `TestReceiverReportsABurstOfDAVEFailuresOnce`, `TestReceiverReportsABurstOfUnreadablePacketsOnce`.
- Smoke: [#573](https://github.com/7Cav/cavbot2/pull/573#issuecomment-6176551167) check 2 on `smoke-98750e0` saw no `DAVE decrypt failures` while a member joined and left five times. No pass has seen a burst reported, so the reporting is **fake only.**

### H5. A panic while a frame is handed over costs that frame alone

- Trigger: a bug in the code writing tracks panics.
- Recorder: recovers it, reports it to Sentry, and reads on, so it can't take temp VC and every command down.
- Members: nothing but the lost frame.
- Tests: `TestReceiverRecoversAPanicInTheFrameHandler`.
- Smoke: None, and no smoke check could make one. **Fake only.**

### H6. A transport-only call is no stall

- Trigger: a call with DAVE protocol version 0, such as a stage channel.
- Recorder: dave-go's session never turns ready there and holds no frame, so no stall and no gap start.
- Members: the recording hears the call.
- Tests: `TestReceiverNeverCountsATransportOnlyCallAStall`.
- Smoke: None. [#572](https://github.com/7Cav/cavbot2/pull/572#issuecomment-6176550615) check 4 and [#573](https://github.com/7Cav/cavbot2/pull/573#issuecomment-6176551167) check 7 were waived, since the test guild isn't a Community server and can't have a stage channel. **Fake only.**

## Gaps

### G1. The recorder tells the recording when it stops hearing the channel, and from when

- Trigger: the recorder isn't connected at the voice server Discord named last, or DAVE holds every frame. Connected means a session description arrived after the last dial started, and that dial went to the server named last with its token.
- Recorder: hands the recording `GapStarted(since)` once and `GapEnded()` once, in turn. `since` is when the connection was lost when the recorder knows it: the close, or for a voice server gone silent its last message. Otherwise it is now.
- Members: every track is silent through the gap, whether or not anyone spoke.
- Tests: `TestRecorderStartsAGapWithin1sOfACloseDisgoResumesFrom`, `TestRecorderStartsTheGapOfASilentVoiceServerAtItsLastMessage`, `TestRecorderStartsTheGapOfAMovedCallAtTheClose`, `TestRecorderConnectsOnceTheServerItsCallMovedToIsReachable`, `TestReceiverToldOfAGapWhileDAVEHoldsFrames`, `TestRecordingGapToldLateCountsFromItsStart`.
- Smoke: [#759](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024) checks 1 and 2 on `smoke-c5f9692`: the gap notice named the second Discord named the new server, and the info file's gap matched the track's silent stretch, 86.6 s. [#781](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480) check 3 on the same build had a silent drop's notice name the second of the last heartbeat answer, 0.7 s before the smoke hook's WARN.

### G2. A gap past 10 s is reported

- Trigger: a gap reaches 10 s (`voice.GapReportedAfter`), counted from its start.
- Recorder: Sentry hears "Recorder can't hear the channel" once, naming the recorder, the channel and the voice server it is trying, and the bot log has it at ERROR. A DAVE stall's report counts as its gap's, so a stall past 10 s sends nothing more.
- Members: a gap notice posts in the channel's chat, pinging nobody: "⚠️ The recorder can't hear this channel since" the gap's start. When the gap ends it is edited to "✅ The recorder is back" with what it missed and for how long, and the info file lists the gap. A second gap gets a notice of its own. A gap under 10 s posts nothing and isn't listed. A notice Discord refuses changes nothing for the recording.
- Tests: `TestRecorderReportsAGapThatReaches10s`, `TestRecorderReportsNothingOfAGapUnder10s`, `TestRecorderReportsAStallPast10sOnlyAsAStall`, `TestRecordingGapPast10sPostsAGapNotice`, `TestRecordingGapUnder10sPostsNothing`, `TestRecordingGapNoticeIsEditedWhenItsGapEnds`, `TestRecordingInfoFileListsEachGapPast10s`, `TestRecordingGoesOnWhenItsGapNoticeIsRefused`.
- Smoke: [#759](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024) checks 1 and 2 on `smoke-c5f9692`: one ⚠️ about 10 s after the move, pinging nobody, the ERROR once naming the last server, the same message edited to ✅ "missed 22:49:29 to 22:50:55 (1m26s)" once the server was reachable, and one `Gap:` line. Check 5 saw a move with no gap notice.

### G3. A gap of 10 minutes ends the recording

- Trigger: a gap reaches 10 minutes (`recordingGapLimit`) from its start, also when the recorder told of it late.
- Recorder: has no limit of its own and keeps trying until the stop. The runtime stops the recording as `recorder_left`, which has the recorder leave.
- Members: the recording notice says the channel is no longer being recorded, and the gap notice is edited to "❌ The recorder couldn't get back to this call, so the recording stopped" with what it missed from when. The info file says the recorder left the channel.
- Tests: `TestRecordingGapThatReaches10MinutesEndsTheRecording`, `TestRecordingGapToldLateEndsTheRecording10MinutesFromItsStart`, `TestRecorderWaitsForItsGatewayUntilTheRecordingStops`.
- Smoke: [#759](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024) checks 3 and 4 on `smoke-c5f9692`: a stop during a gap edited its notice to ⏹️, and a gap left for 10 minutes logged `Recording gap reached the limit, recording stopped` with `lasted=10m0s`, ended `recorder_left`, and edited the gap notice to ❌.

## Drops

What the recorder does after its voice gateway closes, by close code:

| The voice gateway | disgo alone would | The recorder |
|---|---|---|
| closes with 1000, 4000 to 4003, 4005, 4012, 4015, 4020 or a code disgo doesn't know, or its read fails with no close | resume, five tries over about 15 s | refuses the resume and rejoins at once (D1) |
| closes with 4006, 4009 or 4011 | open a fresh connection in the same channel, for up to 30 s | rejoins, with no time limit (D2), except a held token refused during a rejoin (D5) |
| hears no heartbeat answer | notice at the second unanswered heartbeat | counts the connection dropped after the interval plus 3 s and rejoins (D3) |
| closes with 4022 | leave | rejoins when the channel is still there (M1), leaves when it is gone (M7) |
| closes with 4004, 4014, 4016, 4017 or 4021 | leave | leaves, and Sentry hears of a lost connection (D7), except a held token refused during a rejoin (D5) |

### D1. A close disgo would resume from gets a rejoin within a second

- Trigger: the voice gateway closes with a code disgo resumes from, such as 4015, or its read fails with no close frame.
- Recorder: refuses every dial a voice gateway makes outside the recorder's own open (`ownDialsOnly`), so disgo's resume fails at its first try and hands the close over at once. The gap starts within 1 s. The recorder closes the UDP socket, rejoins through a new gateway, and identifies. No Resume reaches any voice server. The code's reasons: a resume never brought audio back on the test guild, disgo sends the Resume's sequence as `seq` where Discord's v8 docs say `seq_ack`, and DAVE removes a member cut off from the voice gateway from the call's encryption group, so the SFU stops forwarding them media.
- Members: a gap. The recorder stays in the channel's member list.
- Tests: `TestRecorderStartsAGapWithin1sOfACloseDisgoResumesFrom`, `TestRecorderIdentifiesAfterACloseDisgoResumesFrom`, on disgo's real gateway.
- Smoke: [#781](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480) check 1 on `smoke-c5f9692`, twice: after an abrupt drop disgo logged `failed to read next message`, no Resume went out, the recorder stayed in the channel and rejoined at once, and the ⚠️ posted 10 s on. Discord answered the rejoin with voice states alone and the recorder never identified, which became #786 and is D4's case. The smoke hook made the drop (K1), so no pass has seen a voice server drop the connection.

### D2. 4006, 4009 and 4011 get a rejoin with no time limit

- Trigger: the voice gateway closes with 4006, 4009 or 4011 outside a held-token rejoin.
- Recorder: rejoins as it joined, self-muted and never self-deafened, through a new gateway that identifies. It never leaves the channel, and Sentry hears nothing.
- Members: a gap.
- Tests: `TestRecorderKeepsRecordingThroughACloseThatGetsAFreshConnection`.
- Smoke: [#753](https://github.com/7Cav/cavbot2/pull/753#issuecomment-6228935419) check 3 on `smoke-68f77d4` watched for these closes, and none came. **Fake only.**

### D3. A voice server that goes silent counts as a drop

- Trigger: while connected, the voice server sends no heartbeat answer for the interval its Hello named plus 3 s (`heartbeatMargin`).
- Recorder: closes the gateway with 1012 "heartbeat answer overdue" and rejoins as in D1. The gap starts at the last message the server sent, of any kind. disgo's own check would notice only at the heartbeat after the unanswered one, two intervals on.
- Members: a gap whose notice, info file line and 10-minute limit count from the server's last message, up to the interval plus 3 s before the recorder noticed.
- Tests: `TestRecorderStartsTheGapOfASilentVoiceServerAtItsLastMessage`.
- Smoke: [#781](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480) check 3 on `smoke-c5f9692`: with a 13.75 s interval, `heartbeat answer overdue` came 16.75 s after the last answer, and no Resume went out. The smoke hook's silent mode made the drop (K1), and the rejoin then stuck as in D1.

### D4. A rejoin on the same voice server identifies with the token the recorder held

- Trigger: a rejoin after D1, D2 or D3 when the call stayed on its server. Discord answers it with the recorder's `VOICE_STATE_UPDATE` alone and names no server.
- Recorder: disgo throws away the server named before a join starts, so the recorder keeps the last one named. When its own voice state arrives with a channel and no server follows within 250 ms (`answerWait`), it hands disgo the held server and token again, logs INFO "Recorder reconnecting with the voice server token it held", and identifies there. It keeps dialing a server it can't reach, with no limit, and identifies there within 10 s of the server becoming reachable.
- Members: the gap ends.
- Tests: `TestRecorderIdentifiesWithTheTokenItHeldAfterADropOnTheSameVoiceServer`, `TestRecorderKeepsDialingAVoiceServerItCantReachAfterADrop`.
- Smoke: None. #809 merged with its smoke pass owed and still carries `needs-smoke`. Its checks rerun [#781](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480)'s checks 1 to 3 and log which way the reconnect went. **Fake only.**

### D5. A held token the voice server refuses gets one leave and a fresh join

- Trigger: during a rejoin with the held token, the voice server closes the connection before its Ready with 4004, 4006 or 4009. Nobody has seen which one Discord sends for a token it no longer takes.
- Recorder: logs WARN "Voice server refused the token the recorder held, so it leaves and joins again" and never dials that server with that token again. It counts an own leave (`OwnLeaves` goes up) before the leave goes out, sends the leave through a new gateway, waits for Discord's answer, a voice state with no channel, and joins again. Discord answers a fresh join with a voice server and a new token. It does this once per held token. A refusal of a token Discord named since gets no second leave. A close before Ready for any other reason, such as 4014 when a moderator kicks the recorder, isn't a refusal, and the recorder stays out.
- Members: the recorder leaves the channel and comes back, which members see in the member list and hear as Discord's leave and join sounds. The recording runs on through it (L1).
- Tests: `TestRecorderLeavesAndJoinsAgainWhenTheVoiceServerRefusesTheTokenItHeld`, `TestRecorderLeavesOnlyOnceForARefusedToken`, `TestRecorderCountsItsOwnLeaveBeforeDiscordHearsIt`, `TestRecorderStaysOutWhenItsVoiceServerClosesForAKick`, and `TestRecorderLeavesAndJoinsAgainWhenAVoiceServerRefusesItsHeldToken` on disgo's real gateway.
- Smoke: None. No smoke hook can make a voice server refuse a token, so #809's pass will see this only if Discord refuses the held token by itself. **Fake only.**

### D6. A rejoin waits out the recorder's own gateway outage

- Trigger: the voice connection drops while the recorder's own gateway session is down, so the rejoin's voice state update can't go out.
- Recorder: sends it again every second (`rejoinRetry`), with no limit, and rejoins once the gateway is back. The recorder never left, so the recording runs on. When Discord took the recorder out meanwhile, which the session hears as it resumes, the recorder stays out and sends no join. It keeps waiting past the 10-minute gap limit, so the recording's stop at the limit is the last voice state update it sends.
- Members: a gap, its notice at 10 s, and the recording's end at 10 minutes if the gateway is still down.
- Tests: `TestRecorderRejoinsOnceItsGatewayIsBack`, `TestRecorderTakenOutWhileItsGatewayIsDownStaysOut`, `TestRecorderWaitsForItsGatewayUntilTheRecordingStops`.
- Smoke: None. [#781](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480) lists it as unreachable live, and [Capture the recorder's own gateway session reconnecting during a call (#821)](https://github.com/7Cav/cavbot2/issues/821) is the ticket to capture it. **Fake only.**

### D7. A close disgo leaves on is a lost connection

- Trigger: the voice gateway closes with 4004, 4014, 4016, 4017 or 4021 outside a held-token rejoin. Or Discord takes the recorder out while a rejoin waits or a moved call's channel lookup runs, with a voice state naming no channel that isn't the answer to an own leave.
- Recorder: disgo sends a leave, and any rejoin under way ends. Sentry hears "Voice connection lost mid-recording" once with the close that started it, unless the recorder closed the connection first for a stop.
- Members: the runtime sees the recorder out of the channel and stops the recording (L2).
- Tests: `TestRecorderReportsAVoiceConnectionDisgoGivesUpOn` (4004), `TestRecorderReportsAMovedCallItCouldNotRejoin`, `TestRecorderReportsBeingTakenOutWhileItTriesToReachTheCall`.
- Smoke: [#574](https://github.com/7Cav/cavbot2/pull/574#issuecomment-6176551689) check 6 on `smoke-98750e0`, before #728 wrapped disgo's close handler, saw a moderator's drag get a 4014 that disgo logged, and no lost-connection event. No pass since has seen one of these closes, so on today's code it is **fake only.**

## Moves

### M1. A 4022 in a channel that is still there is a move

- Trigger: Discord ends the call with 4022, "Call terminated", as a region change on the channel makes it do.
- Recorder: the gap starts at the close. It asks Discord's API whether the channel still exists, with a 5 s bound, because the channel's delete can reach its session after the 4022. Only an Unknown Channel answer counts as gone, and any other failure leaves the rejoin to find out. It never dials the server the call left again, rejoins as it joined, and identifies at the new server through a new gateway, since the old one would resume a session the new server never had. Sentry hears nothing.
- Members: a gap, which ended within 2.7 s on the test guild.
- Tests: `TestRecorderRejoinsWhenItsCallMovesToAnotherVoiceServer`, `TestRecorderIdentifiesAtTheVoiceServerItsCallMovedTo`, `TestRecorderStartsTheGapOfAMovedCallAtTheClose`.
- Smoke: [#728](https://github.com/7Cav/cavbot2/pull/728#issuecomment-6183158042) check 1, second run, on `smoke-df64b83`: in 20 moves the recorder identified 0.56 to 1.68 s after the 4022, DAVE set up 0.92 to 2.12 s after it, and sound was back within 2.7 s. [#753](https://github.com/7Cav/cavbot2/pull/753#issuecomment-6228935419) check 2 on `smoke-68f77d4` and [#759](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024) check 5 on `smoke-c5f9692` saw moves with no gap notice and words after each. A channel lookup that takes its full 5 s is unreachable live.

### M2. A move whose new server Discord named before the rejoin

- Trigger: Discord names the new server before the recorder's rejoin starts, while the old gateway is still open or during the channel lookup, and then answers the rejoin with the voice state alone.
- Recorder: disgo throws away a server named before its join starts. The recorder keeps it and hands it back 250 ms after the voice state, as in D4, and identifies there with the token Discord gave for it.
- Members: a gap.
- Tests: `TestRecorderFollowsAMoveWhoseServerDiscordNamedBeforeTheRejoin`.
- Smoke: [#735](https://github.com/7Cav/cavbot2/pull/735#issuecomment-6183157450) check 1 on `smoke-df64b83`: in 8 of 20 moves the server update came 0.11 to 0.19 s after the 4022, before the rejoin, and the recorder identified 0.78 to 1.68 s after the 4022, with no lost connection and no stop.

### M3. The server named last wins, and the recorder never goes back

- Trigger: Discord names more than one server, as when it moves a call twice, or sends a `VOICE_SERVER_UPDATE` with no endpoint first.
- Recorder: opens one gateway, at the server named last. It never dials the server a moved call left, even when Discord's voice-state-only answer comes before the new server. Once a later server is named it never connects to an earlier one, even when the earlier one becomes reachable first.
- Members: a gap.
- Tests: `TestRecorderFollowsAMoveToTheServerDiscordNamedLast`, `TestRecorderNeverDialsTheServerAMovedCallLeft`, `TestRecorderNeverGoesBackToAServerDiscordNamedBefore`.
- Smoke: None on today's code. The body of #736, on `smoke-df64b83`, saw Discord name two more servers while disgo retried one it couldn't reach, before #759 had the recorder follow them. **Fake only.**

### M4. A server named mid-dial or mid-connect replaces the one being reached

- Trigger: Discord names a new server while the recorder dials an earlier one, or as its connection there completes, between the gateway opening and the session description.
- Recorder: ends the dial and dials the new server. A connection that completed at a server Discord has replaced is closed with 1012 "Discord named a later voice server", and a new gateway identifies at the server named last. A close handed over later by a gateway the recorder left behind changes nothing and starts no gap.
- Members: a gap.
- Tests: `TestRecorderConnectsWhereDiscordNamesWhileItTriesAnEarlierServer`, `TestRecorderMovesOnFromAServerDiscordReplacedAsItConnected`, `TestRecorderIgnoresACloseFromAGatewayItLeftBehind`.
- Smoke: None on today's code. The same #736 body is the only log of Discord naming a server mid-dial. **Fake only.**

### M5. A voice server the recorder can't reach yet

- Trigger: the host's resolver answers "no such host" for the new server's hostname. A resolver that asked before the record existed keeps that answer for the 30 minutes discord.media's SOA allows.
- Recorder: asks the zone's own nameservers, found through its NS records, and connects at the address they give, with the hostname kept for TLS and Host. It asks both again on every dial. It keeps dialing past the 30 s disgo gives an open, with no limit, and stays in the channel. disgo's ERROR for each failed dial reaches the bot log at WARN, and the bot logs no ERROR of its own while nothing is lost.
- Members: a gap, its notice at 10 s, the notice's edit once the server is reachable, or the recording's end at 10 minutes.
- Tests: `TestVoiceDialerConnectsWhereTheZonesNameserversSay`, `TestVoiceDialerAsksAgainOnEachDial`, `TestRecorderStaysInWhileItCantReachTheServerItsCallMovedTo`, `TestRecorderConnectsOnceTheServerItsCallMovedToIsReachable`, `TestRecorderLogsNoErrorWhileItTriesToReachTheServerItsCallMovedTo`, `TestRecorderLogsAFailedDialOfTheVoiceServerAtWarn`.
- Smoke: [#759](https://github.com/7Cav/cavbot2/pull/759#issuecomment-6242899024) checks 1, 2 and 4 on `smoke-c5f9692`, with the DNS switch (K2): the recorder stayed in past 30 s, identified 9 s after the switch's file went, and a gap left for 10 minutes ended the recording. The switch fails both lookups, so the zone-nameserver fallback is unreachable live, and that part is **fake only.**

### M6. A voice server update with no endpoint is passed over

- Trigger: Discord sends a `VOICE_SERVER_UPDATE` with a null endpoint while it moves the voice server.
- Recorder: hands disgo no endpoint, and waits for the next update.
- Members: nothing beyond the move's gap.
- Tests: `TestVoiceServerEventCarriesTheEndpointOrNone`, and the null-endpoint case of `TestRecorderNeverDialsTheServerAMovedCallLeft`.
- Smoke: None. [Collect the Discord voice event sequences we already know (#812)](https://github.com/7Cav/cavbot2/issues/812) found no logged `VOICE_SERVER_UPDATE` with a null endpoint. **Fake only.**

### M7. A 4022 in a deleted channel ends the recording

- Trigger: someone deletes the channel being recorded.
- Recorder: the channel lookup answers Unknown Channel, so no rejoin, and disgo leaves. On the bot's own session the `CHANNEL_DELETE` stops the recording as `recorder_left`.
- Members: the recording stops. The notice went with the channel, so the starter gets the link by DM.
- Tests: `TestRecorderLeavesWhenItsChannelIsDeleted`, and the "the channel is deleted" case of `TestRecordingStopsByItself`.
- Smoke: [#574](https://github.com/7Cav/cavbot2/pull/574#issuecomment-6176551689) check 7 on `smoke-98750e0` ended `recorder_left`. [#728](https://github.com/7Cav/cavbot2/pull/728#issuecomment-6179685113) check 2 ran twice, on `smoke-e75355c` and `smoke-df64b83`, and ended `empty` within 1.65 s: the delete took the speakers out of voice first, and no 4022 reached the recorder. The 4022 path is **fake only.**

## DAVE

### V1. A DAVE setup that stalls starts over on a fresh connection

- Trigger: DAVE holds every frame for more than 3 s (`stallAfter`) since its setup started or since the last fresh connection. Every setup seen on the test guild finished within 0.7 s of the session description.
- Recorder: Sentry hears "DAVE setup stalled" once per stall, naming the channel, and that counts as the gap's report. The recorder closes the gateway with 1012 "starting DAVE over" and opens a new one with the state it last opened with, at the server Discord named last. The new gateway identifies, so the voice server starts afresh with a new UDP socket and a session description that starts DAVE over. Each open has 5 s, and a failed one logs WARN "Fresh voice connection failed". It tries again at most every 3 s until DAVE passes frames or the recording stops, then logs INFO "DAVE setup recovered" with how long it couldn't hear. It never leaves the channel, and follows a call that moves during a stall. A setup that holds frames under 3 s is left alone.
- Members: a gap, and its notice at 10 s.
- Tests: `TestReceiverToldOfAGapWhileDAVEHoldsFrames`, `TestReceiverRetriesAStallAtMostEvery3sAndReportsItOnce`, `TestReceiverLeavesAShortDAVESetupAlone`, `TestReceiverHearsTheSpeakerAgainOnceDAVESetsUpAfterAStall`, `TestRecorderStartsDAVEOverWhenItsSetupStalls`, `TestRecorderStartsDAVEOverAtTheServerDiscordNamedLast`, `TestRecorderFollowsACallThatMovesDuringAStall`, `TestRecorderStoppedDuringARecoveryLeavesAtOnceAndReportsNothingMore`.
- Smoke: [#734](https://github.com/7Cav/cavbot2/pull/734#issuecomment-6183157755) check 1 on `smoke-df64b83`: a first join stalled, the ERROR came 3.3 s after the MLS commit, the fresh connection identified 0.34 s later, DAVE set up 0.78 s after the stall line, and the INFO said `unheard=3.81s`. None of 21 moves stalled, so recovery after a move is unexercised, and check 3, a stop during a stall, never ran. [Does dave-go past dave-go#51 end the DAVE stalls? (#815)](https://github.com/7Cav/cavbot2/issues/815) plans 20 more joins and moves.

## The recorder's own leave, and a moderator's disconnect

The recording runtime watches the recorder through the bot's own gateway session, not the recorder's. It can't see why the recorder left the channel, only that the bot's state cache no longer shows it there. `Conn.OwnLeaves` is how the voice adapter tells it which absences are the recorder's own.

### L1. An own leave holds off the recorder-left stop for 10 s

- Trigger: the recorder leaves the channel on its own to join it again. Today only D5 does that.
- Recorder: `OwnLeaves` goes up just before the leave goes out, so by the time Discord, and then the bot's cache, shows the recorder gone, the count already says so. The count never goes down.
- Runtime: every voice state event in the guild on the bot's own session runs `checkChannels`, and so does each start once its recording runs. When it reads `OwnLeaves` higher than last time, it notes the time and sets a recheck for 10 s on (`ownLeaveGrace`). For those 10 s the recorder's absence from the channel doesn't stop the recording. The recheck judges the channel then, with or without another voice event, and a recorder still out stops it as L2 does.
- Members: the recorder leaves and comes back, and the recording runs on.
- Where the rule comes from: no issue states it. It came with #809, for D5's leave. The spec in [#381](https://github.com/7Cav/cavbot2/issues/381) counts any leave "for any reason" as the recorder leaving, and [#786](https://github.com/7Cav/cavbot2/issues/786) asks that the leave and join after a refused identify keep the recording, which is what the grace does.
- Tests: `TestRecorderCountsItsOwnLeaveBeforeDiscordHearsIt` on the voice side, and `TestRecordingGoesOnWhileItsRecorderLeavesToJoinAgain` on the runtime's, with the leave shown at once, shown only after a voice event read the recorder still in, and the recorder out past the grace.
- Smoke: None. Only D5 makes an own leave, and no pass has seen D5. **Fake only.**

### L2. The recorder out of its channel otherwise stops the recording

- Trigger: once the cache has shown the recorder in the channel, a check finds it out of the channel or in another, outside an own leave's 10 s. That covers a moderator's disconnect, a moderator's move, and a lost connection (D7). The cache can show the recorder in the channel only some time after its join returns, so until it has, an absence counts for nothing.
- Runtime: stops the recording as `recorder_left`, which has the recorder leave. That is the spec's rule in [#381](https://github.com/7Cav/cavbot2/issues/381), "The recorder leaves the channel for any reason, including being moved out or the channel being deleted", and [#769](https://github.com/7Cav/cavbot2/issues/769) adds "A moderator's Disconnect looks the same."
- Members: the recording notice says the channel is no longer being recorded, and the info file says the recorder left the channel. The recorder stays out.
- Tests: the "the recorder is moved to another channel" case of `TestRecordingStopsByItself`, `TestRecordingGoesOnBeforeTheCacheShowsTheRecorder`, and `TestRecorderStaysOutWhenItsVoiceServerClosesForAKick` for the voice side of a kick during a rejoin. No test plays a moderator's disconnect of a connected recorder at the voice adapter.
- Smoke: [#574](https://github.com/7Cav/cavbot2/pull/574#issuecomment-6176551689) check 6 on `smoke-98750e0`: a moderator dragged the recorder to another channel, the recording ended `recorder_left`, and disgo logged a 4014 with no lost-connection event. [#781](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480) check 4 on `smoke-c5f9692`: the bot disconnected the recorder through the API, and the recording ended `recorder_left` with no rejoin.

### L3. What the 10 s window costs

These follow from the code, and no test pins them:

- A moderator who disconnects the recorder within 10 s of an own leave isn't heard until the recheck, so that recording stops up to 10 s late, writing nothing meanwhile.
- A moderator's disconnect mid-call reaches the recorder's session as its voice state with no channel and a 4014 close, in an order nobody has logged (#812). When the voice state comes first, disgo closes the gateway itself and Sentry hears nothing. When the 4014 comes first, disgo leaves, and Sentry hears "Voice connection lost mid-recording" unless the runtime's stop closed the connection first.
- A moderator's move puts the recorder's connection in the new channel, as disgo follows the recorder's voice state, until the runtime's stop has it leave. Nothing keeps frames from that channel out of the recording in between.
- A 4022 in a deleted channel (M7) races the same way: Sentry hears of a lost connection unless the `CHANNEL_DELETE` stop closed the connection first.

## Logs

### O1. The bot log's ERROR means the recorder lost something

- Trigger: disgo logs an ERROR on a recorder's voice connection, its gateway or its UDP socket, as it does for every dial, open or close that fails while the recorder keeps trying.
- Recorder: hands each such record on at WARN. With the bot log at ERROR they don't appear. A loss gets an ERROR of the bot's own, with its Sentry event.
- Members: nothing.
- Tests: `TestRecorderLogsAFailedDialOfTheVoiceServerAtWarn`, `TestRecorderLogsACallItsVoiceServerEndedAtWarn`, `TestRecorderLeavesAFailedDialOutOfABotLogAtError`, `TestRecorderLogsNoErrorWhileItTriesToReachTheServerItsCallMovedTo`.
- Smoke: [#781](https://github.com/7Cav/cavbot2/pull/781#issuecomment-6242900480) check 1 on `smoke-c5f9692` read disgo's `failed to read next message` at WARN, on a build with #780 in it. #780 has no smoke checks of its own.

### O2. A connection the recorder closed logs nothing more

- Trigger: the recorder closes a connection for a stop, a failed join or a shutdown, while disgo still has work in flight on it.
- Recorder: sets that connection's log above every level disgo logs at, so none of the abandoned work reaches the bot log.
- Members: nothing.
- Tests: `TestRecorderStoppedWhileARejoinTriesAServerItCantReachLogsNoError`.
- Smoke: None. #760 has no smoke checks. **Fake only.**

### O3. The lines smoke checks read

- Trigger: the bot log at DEBUG.
- Recorder: a connection the recorder hasn't closed logs disgo's "opening voice conn" and "opening voice gateway connection" word for word, which the move check in [fix(voice): follow a moved call whose server Discord named before the rejoin (#735)](https://github.com/7Cav/cavbot2/pull/735) reads. The INFO and WARN lines of D4, D5 and V1 and the smoke hook's WARN are the others the passes quote.
- Members: nothing.
- Tests: `TestRecorderVoiceConnectionLogsReachTheBotLogAtDebug`.
- Smoke: [#735](https://github.com/7Cav/cavbot2/pull/735#issuecomment-6183157450) check 1 on `smoke-df64b83` read the DEBUG lines to time 20 moves. The INFO line of D4 and the WARN of D5 have appeared in no pass.

## Smoke hooks

The smoke checks for D1, D3 and M5 can't run without these, so the replacement needs a way to do what they do.

### K1. `SMOKE_VOICE_DROP_FILE` drops every live voice connection

- Trigger: the file it names appears, checked every 250 ms.
- Recorder: every voice gateway connection the recorders hold then drops. With the file reading `silent`, the UDP sockets drop too, and nothing passes either way, so the recorder notices only through D3. With anything else the gateway connections break abruptly, and their next read fails as a reset, which disgo resumes from (D1). Connections made while the file stays don't drop until it appears again, so the recorder can rejoin. The bot log gets a WARN "Smoke hook dropping voice connections".
- Tests: `TestSmokeDropFailsALiveConnectionsReadAsTheNetworkDoes`.

### K2. `SMOKE_VOICE_DNS_FAIL_FILE` makes every voice server unreachable

- Trigger: the file it names exists.
- Recorder: every lookup of a voice server answers "no such host", from the host's resolver and the zone's nameservers alike. Once the file is gone the recorder connects.
- Tests: `TestSmokeDNSFailMakesVoiceServersUnreachableWhileItsFileExists`.

## What today's recorder leaves open

- [#805](https://github.com/7Cav/cavbot2/issues/805), open: after a resumable drop and a rejoin that fails, "Voice connection lost mid-recording" reaches Sentry carrying `voice: the recorder rejoins rather than resumes: gateway is already connected` in place of the drop's close code, since D1 swaps the close for the refused resume.
- [#768](https://github.com/7Cav/cavbot2/issues/768), open and blocked on disgo: a connection closed as Hello arrives can leave disgo's heartbeat running, and `-race` reports it. Triage saw it in 85 of 248 cancelled joins.
- [#772](https://github.com/7Cav/cavbot2/issues/772), open and blocked on disgo: a close between the websocket accept and Ready leaves a disgo goroutine running forever, about 7 KB each. A join timeout, a shutdown during a join, a stop during a rejoin and a redial can all land there.
- The races in L3, which no test pins.
- #781 and #809 still carry `needs-smoke`. Their pass is the first that can show D4, and D5 if Discord refuses the held token.
- [Recovery policy for a recorder that loses its call (#814)](https://github.com/7Cav/cavbot2/issues/814) is deciding what the replacement should do where this inventory only says what today's recorder does: it leaves and joins again once for a refused token (D5), stops on any absence outside the grace (L2), keeps trying for 10 minutes (G3), and ends a gap once it is connected at the server named last with DAVE passing frames (G1).
