# Voice recorder prototype (#10). Throwaway.

This is not part of the bot. It is its own Go module, so `go build ./...`, the tests, the linter and the coverage floors at the repo root never see it. Delete it once #10 has an answer.

## The question

Can cavbot2 record a voice channel, one track per speaker, under Discord's DAVE end-to-end encryption, without leaving `bwmarrin/discordgo`?

The approach, from [docs/research/dave-e2ee-libraries.md](../../docs/research/dave-e2ee-libraries.md):

- discordgo keeps the main gateway. `ChannelVoiceJoinManual` sends the voice state update, and discordgo's own voice code stays idle.
- disgo's `voice` package (master, for the #593 padding fix) runs the voice gateway and UDP. The prototype translates discordgo's `VOICE_STATE_UPDATE` and `VOICE_SERVER_UPDATE` events to disgo's types.
- `thomas-vilte/dave-go` does DAVE in pure Go, so no CGO.
- A UDP keepalive every 5 s, because disgo has none and a recorder never sends audio.

Three things to find out:

1. Does the discordgo to disgo wiring join and hold a DAVE voice session?
2. Does DAVE receive keep decrypting when people join and leave mid-recording?
3. Does audio keep arriving after a long silence, with and without the keepalive?

## Run it

macOS or Linux. Needs `DISCORD_TOKEN` and `GUILD_ID` for the **test guild**, in the environment or a `.env` file.

```bash
go run . -env /path/to/.env -channel <voice channel ID>
```

It joins the channel, records until Ctrl-C (or `-duration`), then leaves. Recordings land in `out/PROTOTYPE-<time>/`, one `<user ID>.ogg` per speaker plus `summary.txt`. `out/` is gitignored.

Flags worth knowing:

| Flag | Default | Use |
|---|---|---|
| `-keepalive` | `5s` | `0` turns the keepalive off, for the silence comparison |
| `-duration` | none | stop on a timer instead of Ctrl-C |
| `-status` | `10s` | how often the state table prints |
| `-prime` | off | send five silence frames after joining, if no audio ever arrives |
| `-debug` | off | disgo and dave-go debug logs |

Don't point `-channel` at a "Join to create" hub. The running bot would spawn a temporary channel.

## Unattended test

This runs without anyone talking. It needs two Discord accounts in the test guild.

- **Account A is the speaker.** A separate Chrome window uses `out/speaker-loop.wav` as its microphone, and Chrome loops it. The file is 8 minutes of counting out loud over faint pink noise, so Discord transmits without a break, then 18 minutes of digital silence, so Discord sends nothing. Build it with `./speaker-wav.sh` (macOS).
- **Account B makes the joins and leaves.** Join muted, stay about 20 seconds, leave, wait about 10 seconds, repeat, all while A is counting. Every join and leave makes the recorder process an MLS commit while A talks.

Launch A's Chrome, log in, and join the channel:

```bash
open -na "Google Chrome" --args --user-data-dir="$HOME/.cache/cavbot2-speaker-chrome" --use-fake-device-for-media-stream --use-fake-ui-for-media-stream --disable-features=AudioServiceSandbox --use-file-for-fake-audio-capture="$PWD/out/speaker-loop.wav" https://discord.com/app
```

`--disable-features=AudioServiceSandbox` is required on macOS. Chrome's audio process is sandboxed and cannot open the file. The fake mic then plays silence, and Discord shows "no input detected". The flag unsandboxes only the audio process. `--no-sandbox`, which Chrome's error log suggests, would turn off the sandbox for the whole browser.

In A's Voice & Video settings, turn off noise suppression, echo cancellation and automatic gain control. Chrome's own docs for the flag warn that audio processing distorts a played-back file. Then turn off automatic input sensitivity and drag the threshold near the left, so the noise under the counting keeps the microphone open.

Run the recorder with `-stop-after-silence 15m` and it stops a minute after the audio returns from the silence. Run it once with the keepalive and once with `-keepalive 0`. The second run joins while A is counting, which also tests the bot joining a call in progress.

## Reading the output

Events print as they happen: who is in the call, which SSRC belongs to whom, and every DAVE step (op 21 to 30). A table prints every `-status`:

- **DAVE line.** `ready` and `epoch` come from dave-go. Commits and welcomes should rise by one per join or leave, with 0 failed.
- **frames.** Opus frames that passed both transport and DAVE decryption.
- **DAVE fails.** Frames dave-go could not decrypt for that speaker. A run of failures prints when it starts and when it ends, with its length. That length is the gap a join or leave costs.
- **missing.** RTP sequence numbers that never arrived: network loss plus frames dropped by a failed decrypt.
- **drift.** Track length minus wall-clock time at the speaker's last frame. It should stay near zero. If it grows, Discord's RTP timestamps don't track real time across silence and the per-speaker tracks will not line up.
- **receiver waited on DAVE.** Time the receiver held off reading because dave-go was not ready. Packets queue in the socket meanwhile, so this is delay, not loss.

## What the prototype changes in disgo

It uses disgo's voice package as released on master, with three replacements, each marked in `voice.go`:

- **UDP conn.** disgo's own, plus the keepalive. disgo gives no way to write a raw datagram, so the dialer keeps a duplicate descriptor of the socket.
- **Audio receiver.** Same "read only when DAVE is ready" rule. It sleeps instead of busy-spinning, survives the UDP socket reopening after a voice reconnect, and reads in transport-only sessions where dave-go's `Ready` never turns true.
- **DAVE session.** dave-go wrapped to count decrypt results per speaker.

## Results

### Run 1: 2026-09-24, test guild, one person, keepalive on, 23 minutes

The bot sat in the channel alone for 44 s. One person then joined and left four times, and on the last join stayed silent for 20 minutes before talking again.

**Question 1, the wiring: yes.** The join was accepted with transport `aead_aes256_gcm_rtpsize` and DAVE protocol version 1, with no close 4017. The session held for 23 minutes, and the bot left cleanly on Ctrl-C.

**Question 2, joins and leaves: partly answered.**
- Every join went op 27 (proposals), then op 30 (welcome), and dave-go joined the group at epoch 1: 4 welcomes, 0 failed. Every leave went op 24, then op 21 with transition 0, the sole-member reset.
- The speaker's 2,807 frames decrypted with 0 DAVE failures.
- The first join's speech sits in the track at 46.2 s, against a join at 44.6 s and the SSRC mapping at 46.1 s. It measures -24.9 dB mean and -0.2 dB peak, so it is real audio.
- Not tested: commits (op 29). With one person, every join builds the group from scratch through a welcome, so `commits` stayed at 0. The case that matters is someone joining while another person talks, where the bot has to switch keys mid-stream. That needs two people.

**Question 3, long silence: yes with the keepalive on.** Nothing arrived from 02:05 to 22:18. Then the speaker's audio came through: 359 frames, 0 DAVE failures. 285 keepalives were sent with no errors. The comparison with `-keepalive 0` is not done.

Other findings:
- dave-go never reports ready for a bot that starts out alone. It turns ready only on a welcome, or on the op 21 transition 0 that Discord sends when the last other person leaves. The receiver waited 37 s at the start for that reason. It costs nothing, since there is no audio to decrypt until someone joins.
- 36 frames arrived before op 5 mapped their SSRC to a user, so disgo passed them on as user 0. 35 went through dave-go as passthrough, so they were not DAVE-encrypted. They are probably the Opus silence packets DAVE leaves in the clear, but that was not checked. One failed. Speech sent before op 5 would be lost the same way. It did not happen here.
- 54 sequence numbers were skipped: 2 in the first segment and 52 after the long silence. Run 1 did not separate RTP padding (which disgo skips on purpose) from lost audio. The stats now do.
- One packet arrived out of order and was dropped.
- Transport decrypt errors: 0.

**Prototype bug, fixed after run 1.** A rejoin brings a new SSRC with a new random RTP clock, and the track writer measured the gap against the old one. It inserted two one-hour silences and dropped the last segment, 360 frames, as "late". The frames themselves decrypted fine. The writer now places a new SSRC by wall clock, and prefers the wall clock whenever RTP timestamps disagree with it by more than 2 s.

### Runs 2 and 3: 2026-09-29, unattended test

Account A (`xtn`) was the speaker, in the Discord web client through Chrome's fake microphone. Account B (`SyniRon`, desktop app) joined and left five times while A counted. The recorder ran twice in a row: with the keepalive (28 minutes), then without it (26 minutes).

**Question 2, commits: yes.** B's five joins and five leaves made ten op 29 commits (transitions 1 to 10) while A was talking. dave-go processed every one, reaching epoch 11, with 0 commits or welcomes failed and 0 DAVE decrypt failures on A's stream. The only failures were on frames from B's new SSRC that arrived before op 5 named B: 2 frames in the whole run.

A's lost frames, taken from the 30-second snapshots:

| Window | What was happening | A's frames lost |
|---|---|---|
| 01:09 to 03:30 | A joins, then B joins and leaves five times | 16 of about 7,000 |
| 03:30 to 09:00 | steady counting, nobody joining or leaving | 2 of about 16,500 |
| the minute around each end of the long silence | counting stops, then restarts | 5 to 8 each time |

A commit costs one or two frames, 20 to 40 ms of audio, and never a decrypt failure. Frames arrived at exactly 50 per second while A counted, so Discord transmitted continuously.

**Question 3, long silence: yes, keepalive or not.** With the keepalive, A's audio came back after 17 m 38 s of silence. Without it, after 17 m 58 s in which the bot sent nothing at all, it came back just the same: 23,896 frames, 0 DAVE failures, 0.05% lost. The 13 to 15 minute cut-off Glyphoxa reported did not happen on this network. The keepalive costs one 8-byte packet every 5 s, so keep it anyway: production runs on a different network, and NAT timeouts vary.

**Alignment: yes.** Each of A's tracks is exactly as long as its run (1,690.5 s and 1,558.6 s), and drift at the end was -0.05 s and -0.01 s. That includes 18 minutes of silence on a single SSRC.

**Joining a call in progress: yes.** Run 3 joined while A was counting and opened A's track 0.7 s later.

### Verdict

discordgo for the gateway, plus disgo's voice package from master, plus dave-go, records DAVE voice per speaker in cavbot2 without leaving Go. What the real recorder must handle, all seen in these runs:

- **disgo master, not a release.** v0.19.6 lacks the #593 padding fix. Pin a master pseudo-version until disgo cuts a release.
- **Replace disgo's default receiver.** It busy-spins while DAVE is not ready, stops for good when the UDP socket closes on a voice reconnect, and never reads in a transport-only session.
- **Place tracks by wall clock whenever the SSRC changes.** A rejoin gets a new SSRC with a random RTP base. That was the run 1 bug.
- **Frames that arrive before op 5 have no user.** Buffer them by SSRC and assign them when op 5 arrives, or a speaker's first words after joining are lost.
- **Keep the keepalive.** It wasn't needed here, but it is cheap insurance.

Not covered: several people talking at once, a voice server reconnect, and a real Discord desktop client as the speaker. The speaker here was the web client. The desktop client was only ever the joining-and-leaving account.
