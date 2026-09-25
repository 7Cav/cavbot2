# DAVE E2EE voice support in Discord libraries

Date: 2026-09-24. Every claim below was checked against source code at a tag or commit, release notes, a package registry, official docs or an issue thread on that date. Nothing was run against a live Discord voice channel.

## Question

Which Discord libraries, in any language, can join a DAVE-enforced voice channel and decrypt other users' audio per speaker, in a tagged release, with correct MLS group handling?

cavbot2 wants a Craig-style recorder. The bot joins a voice channel and writes one track per speaker. cavbot2 depends on `github.com/bwmarrin/discordgo v0.29.0` ([go.mod](../../go.mod)).

## Bottom line

No Go library clears the bar in a tagged release. disgo comes closest. Its latest release, v0.19.6 from 2026-06-07, drops 3.7 to 11% of received DAVE frames because it tests the RTP padding bit with the wrong mask ([disgo#593](https://github.com/disgoorg/disgo/issues/593)). The fix landed on master on 2026-08-25 and has not been released.

The most proven recording stacks are in JavaScript. Craig runs on Dysnomia with `@snazzah/davey` ([Craig package.json](https://github.com/CraigChat/craig/blob/master/apps/bot/package.json)). `@discordjs/voice` 0.19.2 uses the same davey core. JDA with JDAVE, songbird 0.6.0, NetCord 1.0.0-beta.23 and D++ 10.1.6 also ship per-user receive decryption with full MLS commit handling. Each has at least one open bug that matters for recording.

bwmarrin/discordgo cannot do voice at all today. v0.29.0 still selects `xsalsa20_poly1305` ([voice.go at v0.29.0](https://github.com/bwmarrin/discordgo/blob/v0.29.0/voice.go)), a transport mode Discord removed on 2024-11-18. Its DAVE PR [#1704](https://github.com/bwmarrin/discordgo/pull/1704) has had no review since it was opened on 2026-03-21.

## Criteria

Each library was assessed on seven points.

1. Join. Connects to a non-stage voice channel without close code 4017.
2. Send. Transmits DAVE-encrypted Opus frames.
3. Receive. Decrypts other users' DAVE frames per SSRC or user. This is the one that matters for recording. Many libraries have no receive API at all.
4. MLS group changes. Processes proposals (op 27), commits (op 29), welcomes (op 30) and transitions (ops 21, 22, 24). Some forks instead answer every commit with `invalid_commit_welcome` (op 31) to force a fresh welcome. That leaves a decrypt gap on every join or leave.
5. Released. DAVE ships in a tagged release on the registry, not only on a branch, fork, nightly or open PR.
6. Maintained. Recent commits and releases, open DAVE bugs, maintainer response.
7. Backend. Discord's libdave (C++), `@snazzah/davey` (Rust), a binding to either, or a separate MLS implementation. Native build needs.

## Summary tables

"Full" in the MLS column means the library applies commits and welcomes, and sends op 31 only after processing actually fails. Dates are release dates.

### A. Released, receive decryption works, and someone records with it

| Library | Language | Latest release | Receive decrypt | MLS group changes | Backend, native deps | Recording evidence |
|---|---|---|---|---|---|---|
| [Dysnomia](https://github.com/projectdysnomia/dysnomia) | JavaScript | 0.2.4, 2026-03-15 | Yes. Each packet carries user ID, RTP timestamp and sequence. | Full, via davey | `@snazzah/davey`, prebuilt napi binaries | Craig, in production |
| [@discordjs/voice](https://github.com/discordjs/discord.js/tree/main/packages/voice) | TypeScript | 0.19.2, 2026-03-17 | Yes. Per-user stream of bare Opus buffers. No RTP timestamp in the stable release. | Full, via davey | `@snazzah/davey`, prebuilt napi binaries | Official recorder example; Pandora |
| [JDA](https://github.com/discord-jda/JDA) + [JDAVE](https://github.com/MinnDevelopment/jdave) | Java 25 | JDA 6.7.0, 2026-09-20; JDAVE 0.1.8, 2026-03-29 | Yes. Per-user PCM, or per-user Opus with RTP timestamps. Open JDAVE #32 logs decrypt failures. | Full, via libdave | libdave through Java FFM; prebuilt Linux x64/arm64, Windows x64, macOS | wisper-transcribe (author confirmed); Together-Java transcriber |
| [songbird](https://github.com/serenity-rs/songbird) | Rust | 0.6.0, 2026-04-05 | Yes, per SSRC. Drops packets until op 5 maps the SSRC. Decoded-PCM path is disputed. | Full, via davey | davey (pure Rust); libopus | EchoScribe, call-scribe |
| [NetCord](https://github.com/NetCordDev/NetCord) | C# | 1.0.0-beta.23, 2026-09-24 (every NetCord release is a prerelease) | Yes. Raw decrypted Opus per packet plus an SSRC-to-user map. | Full, via libdave | libdave via P/Invoke; you supply the shared library | One third-party README claim, unverified |
| [D++](https://github.com/brainboxdotcc/DPP) | C++17 | 10.1.6, 2026-08-05 | Yes. Per-user 48 kHz PCM. | Full, but the op 31 recovery JSON is malformed | In-tree libdave port from 2024-11, mlspp, OpenSSL, libopus | discord-voice-recorder on 10.1.4 |

### B. Released, receive decryption in the code, with a blocking or unverified problem

| Library | Language | Latest release | Receive decrypt | MLS group changes | Backend, native deps |
|---|---|---|---|---|---|
| [disgo](https://github.com/disgoorg/disgo) + [godave](https://github.com/disgoorg/godave) or [dave-go](https://github.com/thomas-vilte/dave-go) | Go | disgo v0.19.6, 2026-06-07; godave v0.3.0, 2026-07-16; dave-go v0.5.1, 2026-07-15 | Partial in the release (#593, 3.7 to 11% loss). Fixed on unreleased master. Per user ID. | Full, in the backend | golibdave: CGO plus libdave v1.1.0 shared library. dave-go: pure Go. |
| [darui3018823/dgo](https://github.com/darui3018823/dgo) | Go | v1.1.0, 2026-08-30 | In code, per SSRC. Receive is not among the flows the author verified live. Drops receiver keys at commit time. | Full since v1.0.0 | Pure Go on thomas-vilte/mls-go; Go 1.26.6+ |
| [Discord.Net](https://github.com/discord-net/Discord.Net) | C# | 3.20.1, 2026-06-07 | Per-user decoded PCM. Open #3281, MLS proposal failure then no audio. | Full in design; binding bugs described below | libdave via P/Invoke; you supply it |
| [DisCatSharp.Voice](https://github.com/Aiko-IT-Systems/DisCatSharp) | C# | 10.7.0, 2026-03-20; 10.7.1-nightly-032, 2026-09-12 | Yes, per user, PCM and Opus | Broken transition lifecycle in 10.7.0. Fixed only in nightlies from 10.7.1-nightly-025. | libdave, natives bundled for 5 platforms |
| [Discord4J](https://github.com/Discord4J/Discord4J) | Java 8 | 3.3.3, 2026-08-19 | In code. No public report of it working. Receive API is deprecated. | Full, via libdave-jvm | libdave-jvm JNI natives |
| [py-cord](https://github.com/Pycord-Development/pycord) | Python | 2.8.1, 2026-07-25 | Partial. Docs warn recording may not work. Working fix is open PR #3159. | Full, via davey | davey wheels |
| [DiscordPHP-Voice](https://github.com/discord-php/DiscordPHP-Voice) | PHP 8.3 | 8.1.1, 2026-06-18 | In code, with built-in per-user WAV/OGG recording. No field reports. | Full, via libdave | ext-ffi, libdave v1.1.1 by default |
| [EDA](https://github.com/qoyri/EDA) | Elixir | 0.4.1, 2026-09-21; 0.5.0-beta.3, 2026-09-23 | In code. Receive recovery fixes exist only in the 0.5 betas. | Full, via davey | Rustler NIF, built from source on 0.4.1 |
| [cartridge-gg/discordgo](https://github.com/cartridge-gg/discordgo) | Go | v0.29.1-dave.25 tag, 2026-05-06 | Yes. Cannot send DAVE audio. Dormant. | Full, via libdave | CGO, libdave built from source with vcpkg |
| [kirill-scherba/discordgo](https://github.com/kirill-scherba/discordgo) | Go | v0.29.1-dave.26 tag, 2026-08-17 | Inherited from cartridge. Adds send. One-person fork. | Full, via libdave | Same as cartridge |
| [discord-go/discord.go](https://github.com/discord-go/discord.go) | Go | v0.14.2-blend, 2026-09-20 | In code. Logs every received packet with `log.Printf`. 7 weeks old. | Full, via dave-go | Pure Go |
| [filispeen/discord.lua](https://github.com/filispeen/discord.lua) | Lua | v1.2.1, 2026-09-23 | In code, with per-user recording sinks | Full, via libdave | LuaJIT FFI, bundled libdave v1.1.1 |
| [DCC](https://github.com/Feralthedogg/DCC) | C11 | v2.1.0, 2026-09-08 | In code. Rebuilds all decryptors on every commit. | Full, via libdave | libdave loaded with dlopen |
| [Flight](https://github.com/debaucheryparty/Flight) | Swift | 1.0.0, 2026-07-01 | In code, per-user PCM callback | Full, via libdave | Vendored libdave, mlspp, opus, sodium |
| [@slipher/voice](https://github.com/tiramisulabs/extra/tree/main/packages/voice) | TypeScript | 0.1.0, 2026-09-22 | In code, per-user async iterator with RTP fields | Full in code | Own pure-TS MLS on @noble; no native code |
| [@hedystia/discord](https://github.com/Hedystia/Client) | TypeScript | 2.2.0, 2026-08-16 | In code. No failure tolerance. | Full, via davey | `@snazzah/davey` |
| [cacophony](https://github.com/khoek/cacophony) | Rust | 0.3.0, 2026-09-07 | In code. Queues frames that arrive before the SSRC mapping and retries them. | Full, with staged sender switch | Own `dave` crate on OpenMLS 0.9; AGPL-3.0-only |

The libraries from discord.lua down are each under four months old, single-maintainer, and have no public field reports.

### C. Released DAVE, but no receive decryption

| Library | Language | Latest release | Why no receive |
|---|---|---|---|
| [discord.py](https://github.com/Rapptz/discord.py) | Python | 2.7.1, 2026-03-03 | No receive API by design. Add-on `discord-ext-voice-recv` on PyPI has no DAVE decrypt. |
| [disnake](https://github.com/DisnakeDev/disnake) | Python | 2.12.1, 2026-07-22 | Source says decryption is not implemented |
| [nextcord](https://github.com/nextcord/nextcord) | Python | 3.2.0, 2026-05-21 | Send only. Also never dispatches op 24 ([#1294](https://github.com/nextcord/nextcord/issues/1294)). |
| [Lavalink](https://github.com/lavalink-devs/Lavalink) / [Koe](https://github.com/KyokoBot/koe) | Java | 4.2.2, 2026-03-06 / 3.0.0, 2026-07-01 | Playback servers, send only |
| [discord-voip](https://github.com/Androz2091/discord-player/tree/master/packages/discord-voip) | TypeScript | 7.2.0, 2026-03-01 | Fork of @discordjs/voice with the receiver removed |
| [MeowLib](https://github.com/maukkis/MeowLib), [discusy](https://github.com/usyless/discusy) | C++ | v0.7.0 tag, 2026-03-06; v1.0.1, 2026-09-22 | Send only |
| [oto](https://github.com/rayan6ms/oto), [lavende](https://github.com/debaucheryparty/lavende) | Rust | v1.0.0, 2026-09-04; archived | Send only |

### D. DAVE only in a fork, open PR, branch or nightly

| Library | Language | Where DAVE lives | Receive | MLS group changes |
|---|---|---|---|---|
| [bwmarrin/discordgo](https://github.com/bwmarrin/discordgo) | Go | Open PR #1704, no reviews | Yes in the PR | Re-welcome workaround |
| [yeongaori/discordgo-fork](https://github.com/yeongaori/discordgo-fork) | Go | Fork, no tags, last commit 2026-09-13 | Yes, per SSRC | Re-welcome workaround. `HandleCommit` is a no-op. |
| A-Murchison, JustinCarmony, Vinicamilotti discordgo forks | Go | Forks of the above, older | Yes | Re-welcome workaround |
| [0x00-sys/discordgo](https://github.com/0x00-sys/discordgo) | Go | Fork, untagged | Plumbing only, you write the DAVE backend | Delegated |
| [darui3018823/discord.go](https://github.com/darui3018823/discord.go) | Go | Untagged, 3 weeks old | Yes | Full |
| [discord-ext-voice-recv](https://github.com/imayhaveborkedit/discord-ext-voice-recv) | Python | Open PRs #54, #56, #58, #62; zacker150 fork | Yes in the PRs | Via discord.py |
| [py-cord PR #3159](https://github.com/Pycord-Development/pycord/pull/3159) | Python | Open, locked, changes requested | Yes, field reports | Full |
| [nostrum](https://github.com/Kraigie/nostrum) | Elixir | master only; last Hex release 0.10.4, 2025-03-02 | Yes on master | Full |
| [Kord](https://github.com/kordlib/kord) | Kotlin | Open PR #1063 | Yes in the PR | Full |
| [discordrb](https://github.com/shardlab/discordrb) | Ruby | Open PR #453 | No receive API | Full |
| [DSharpPlus.Voice](https://github.com/DSharpPlus/DSharpPlus) | C# | 5.0.0 nightlies only | Receive path passes SSRC where a user ID is expected | Resets MLS state on every op 22 |
| [Disqord](https://github.com/Quahu/Disqord) | C# | MyGet nightlies only | Yes | Initializes the group with the guild ID; re-keys on ignored commits |
| [ekizu](https://github.com/115jon/ekizu), [guildy](https://github.com/monofuel/guildy) | C++, Nim | main branch, dormant | Yes in code | Full / decryptors never re-keyed |

### E. No DAVE, so no join on non-stage channels

bwmarrin/discordgo v0.29.0 (also still on removed xsalsa modes), [arikawa](https://github.com/diamondburned/arikawa) v3.6.0 (xsalsa, voice gateway v4), [disgord](https://github.com/andersfylling/disgord) (archived), [Eris](https://github.com/abalabahaha/eris) 0.18.0 (xsalsa only, [#1609](https://github.com/abalabahaha/eris/issues/1609)), detritus (dead since 2021), discordeno and Harmony (no voice client), [interactions.py](https://github.com/interactions-py/interactions.py) (has a Recorder, no DAVE), hikari (no voice transport), DSharpPlus 4.5.3 VoiceNext (xsalsa only), Remora.Discord (no voice client), Javacord (retired), AckCord, Discordia (AEAD transport, no DAVE), dimscord and sleepy-discord and discordcr (xsalsa only), Concord (voice removed), nyxx and DiscordBM (no voice transport), discord-haskell-voice ([#65](https://github.com/yutotakano/discord-haskell-voice/issues/65) open), discljord (no voice), discord-zig (opcode enums only).

## Go

### bwmarrin/discordgo (cavbot2's dependency)

Not usable for voice. The latest release is v0.29.0 from 2025-05-24. Its `voice.go` line 649 sends mode `xsalsa20_poly1305` ([v0.29.0 voice.go](https://github.com/bwmarrin/discordgo/blob/v0.29.0/voice.go)), removed by Discord on 2024-11-18. Master switched to `aead_aes256_gcm_rtpsize` in [#1677](https://github.com/bwmarrin/discordgo/pull/1677) on 2025-12-29, but that is unreleased and master has no DAVE code. The last master commit is 2026-02-14.

DAVE PR [#1704](https://github.com/bwmarrin/discordgo/pull/1704) comes from yeongaori's fork. It has 0 reviews and 0 comments since 2026-03-21, and is now behind the fork it came from. Issue [#1697](https://github.com/bwmarrin/discordgo/issues/1697) sends users to forks. `VoiceConnection.OpusRecv` exists, so the receive API shape is there. The decryption is not.

### disgoorg/disgo with godave or dave-go

The strongest Go option, and still not releasable for recording as of today.

- Receive API. `OpusFrameReceiver.ReceiveOpusFrame(userID, packet)` delivers frames keyed by Discord user ID, with `UserFilterFunc` and `CleanupUser` ([audio_receiver.go](https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/audio_receiver.go)). `ReadPacket` does transport AEAD decryption, then calls `daveSession.Decrypt(userID, ...)` ([udp_conn.go](https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/udp_conn.go)).
- MLS. The voice gateway hands ops 11, 13, 21, 22, 24, 25, 27, 29 and 30 to a pluggable `godave.Session` ([gateway.go](https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go)). Both backends apply commits and send op 31 only on failure.
- The release bug. v0.19.6 tests `receiveBuffer[0] & 0x04` for padding, which is the CSRC-count bit, and does so before transport decryption ([v0.19.6 udp_conn.go](https://github.com/disgoorg/disgo/blob/v0.19.6/voice/udp_conn.go), lines 316 and 364). Padded packets then fail DAVE decryption. Issue #593 measured 3.66 to 4.96% loss from desktop senders and 11.08% from mobile. Commit 63fa748123 fixed it on 2026-08-25. Master is 50 commits ahead of v0.19.6 with several other voice fixes ([compare](https://github.com/disgoorg/disgo/compare/v0.19.6...master)).
- No UDP keepalive. There is no keepalive in `voice/` on master. The Glyphoxa project reports that a silent bot stopped receiving RTP after about 13 to 15 minutes and replaced disgo's `UDPConn` with one that sends a keepalive every 5 s ([Glyphoxa ADR-0064](https://github.com/MrWong99/Glyphoxa/blob/main/docs/adr/0064-voice-media-path-liveness.md)). No disgo issue tracks this. A recorder never speaks, so this applies directly.
- Other open items. [#595](https://github.com/disgoorg/disgo/issues/595) (nil UDP conn panic if a receiver is registered before `Open`). Source reading found a busy-spin in `defaultAudioReceiver` on master while DAVE is not ready. `Conn.DAVE()` exists only on master.
- Maintenance. Active, with weekly commits. topi314 fixed #593 two days after the report. No release in 3.5 months.

Backends:

- [godave/golibdave](https://github.com/disgoorg/godave) v0.3.0 (2026-07-16) wraps libdave through CGO (`#cgo pkg-config: dave`). It pins libdave v1.1.0 in `libdave/release.txt`. Upstream is at v1.2.1. When a user has no decryptor, `Decrypt` returns `copy(frame, decryptedFrame)`, copying in the wrong direction, so the output buffer is not filled ([golibdave.go line 104](https://github.com/disgoorg/godave/blob/f34f98224dafa0d6f89d2c36baff473ab34ec999/golibdave/golibdave.go)). PR [#5](https://github.com/disgoorg/godave/pull/5), a key-ratchet race during epoch transitions, has been open since 2026-02-23. Used for receive by [mumble-discord-bridge](https://github.com/Stieneee/mumble-discord-bridge) on disgo v0.19.3.
- [thomas-vilte/dave-go](https://github.com/thomas-vilte/dave-go) v0.5.1 (2026-07-15) is pure Go on the same author's `mls-go`. `Decrypt` tries the active, pending and retained previous epochs per sender, and commits nonce and ratchet state only after GCM authentication succeeds ([session.go](https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go)). No CGO. Single author, no commits since 2026-07-15, README says no third-party audit. Glyphoxa uses disgo plus dave-go in production for per-speaker transcription ([ADR-0006](https://github.com/MrWong99/Glyphoxa/blob/main/docs/adr/0006-dave-mls-no-mid-session-migration.md)).

### darui3018823/dgo

A hard fork of discordgo with the package renamed to `dgo`. v1.1.0 from 2026-08-30 is on the Go proxy. Since v1.0.0 (2026-08-01), `HandleCommit` calls `group.ProcessCommit` and proposals and welcomes go through `thomas-vilte/mls-go` ([dave.go at v1.1.0](https://github.com/darui3018823/dgo/blob/v1.1.0/dave.go)). Op 31 is sent only from `recoverDAVEGroup`. Receive keeps discordgo's `OpusRecv chan *Packet`, decrypts per SSRC with a replay window, and ships a per-SSRC Ogg recording example ([examples/voice_receive](https://github.com/darui3018823/dgo/tree/v1.1.0/examples/voice_receive)).

Caveats. The [v1.0.0 notes](https://github.com/darui3018823/dgo/releases/tag/v1.0.0) list live-verified flows that are all send-side. Receivers are discarded inside `HandleCommit` with no previous-epoch key kept, so frames still encrypted under the old key during a transition are probably dropped. That comes from reading the code and was not measured. One maintainer, 2 stars, GitHub issues disabled. Needs Go 1.26.6 or newer. Of all Go options it is the smallest code change from discordgo, since the API is kept.

### yeongaori/discordgo-fork and its descendants

The most used discordgo DAVE fork. It is consumed with a `replace` directive because `go.mod` still says `github.com/bwmarrin/discordgo`. It has no tags. It decrypts per SSRC. Its MLS layer only builds key packages and processes welcomes. `HandleCommit` is a no-op, op 27 proposals are logged and ignored, and every op 29 triggers `invalid_commit_welcome` plus a fresh key package ([dave.go](https://github.com/yeongaori/discordgo-fork/blob/94d3e03d65d14a4a6876ccfb973c0232d1a17b90/dave.go), [voice.go](https://github.com/yeongaori/discordgo-fork/blob/94d3e03d65d14a4a6876ccfb973c0232d1a17b90/voice.go)). Expect a decrypt gap on every join or leave. Two recording bots vendor it anyway ([dndscribe](https://github.com/joejenniges/dndscribe) inserts silence for gaps). YAGPDB vendors it for soundboard playback.

### Other Go entries

- [cartridge-gg/discordgo](https://github.com/cartridge-gg/discordgo) binds libdave, processes commits, and was built "so our bots can listen to voice channels". Its sender still seals with secretbox, so it cannot send. Build requires the libdave submodule, vcpkg and manual `CGO_LDFLAGS`. Dormant since 2026-05-06. [kirill-scherba/discordgo](https://github.com/kirill-scherba/discordgo) adds send in commit 65885e8.
- [0x00-sys/discordgo](https://github.com/0x00-sys/discordgo) has DAVE plumbing and a `VoiceDAVESession` interface close to `godave.Session`, but ships no backend. With no backend it advertises version 0 and gets 4017.
- [arikawa](https://github.com/diamondburned/arikawa) uses xsalsa20_poly1305 and voice gateway v4. The AEAD PR #493 was closed unmerged.

## JavaScript and TypeScript

### @discordjs/voice

DAVE arrived in 0.19.0 on 2025-08-17. `@snazzah/davey` became a hard dependency in 0.19.1 on 2026-03-09. 0.19.2, published 2026-03-17, strips RTP padding before DAVE decryption ([release notes](https://github.com/discordjs/discord.js/releases/tag/%40discordjs%2Fvoice%400.19.2), [#11449](https://github.com/discordjs/discord.js/pull/11449)). Before that, receive failed with `UnencryptedWhenPassthroughDisabled` ([#11419](https://github.com/discordjs/discord.js/issues/11419)).

At the 0.19.2 tag, `VoiceReceiver.parsePacket` decrypts the transport layer, strips padding and the header extension, then calls `daveSession.decrypt(packet, userId)` ([VoiceReceiver.ts at 0.19.2](https://github.com/discordjs/discord.js/blob/%40discordjs/voice%400.19.2/packages/voice/src/receive/VoiceReceiver.ts)). `connection.receiver.subscribe(userId)` returns a per-user stream. `DAVESession.ts` processes proposals, commits and welcomes, and sends op 31 only from `recoverFromInvalidTransition`, after a processing error or after more consecutive decrypt failures than `decryptionFailureTolerance` (default 36) ([DAVESession.ts](https://github.com/discordjs/discord.js/blob/dbb7490627f5102db53acb55c809361d1ac04659/packages/voice/src/networking/DAVESession.ts)).

Caveats. The stable release pushes bare Opus buffers with no RTP timestamp or sequence. The `AudioPacket` type with those fields was merged to main on 2026-06-27 ([#11432](https://github.com/discordjs/discord.js/pull/11432)) and exists only in `1.0.0-dev` builds that need Node 24.17. There has been no voice release in six months. Maintainers call receive best-effort because Discord does not document it ([#11387](https://github.com/discordjs/discord.js/issues/11387)). Issue #11441 claimed 34% loss during MLS transitions on 0.19.0. Maintainers closed it without reproducing it, and nobody re-measured on 0.19.2. Oceanic.js delegates all voice to this package.

### Dysnomia and Craig

[Dysnomia](https://github.com/projectdysnomia/dysnomia) is an Eris fork. DAVE shipped in v0.2.4 on 2026-03-15. At that tag, `VoiceConnection.js` calls `processProposals`, `processCommit` and `processWelcome`, sends op 31 only from `recoverFromInvalidTransition`, and in the UDP handler calls `daveSession.decrypt(userID, AUDIO, data)` before emitting `("data", data, userID, timestamp, sequence)` ([VoiceConnection.js at v0.2.4](https://github.com/projectdysnomia/dysnomia/blob/v0.2.4/lib/voice/VoiceConnection.js), lines 317 to 365 and 1011 to 1056). Per-packet user ID and RTP timestamp is what a multi-track recorder needs for sync.

[Craig](https://github.com/CraigChat/craig) depends on a pinned Dysnomia git commit (00d8982d, 2026-09-08) and `@snazzah/davey ^0.1.12`, and added E2EE support on 2025-09-24 (commit [b96693a9](https://github.com/CraigChat/craig/commit/b96693a9)). Snazzah wrote davey, Dysnomia's DAVE code and much of discord.js's DAVE code.

Caveats. Only two releases since April 2025. The API is Eris-style. If proposal processing fails before any transition, the library disconnects instead of recovering. Voice code on `dev` after v0.2.4 is lint and reordering only.

### Newer JS options

- [@slipher/voice](https://github.com/tiramisulabs/extra/tree/main/packages/voice) 0.1.0, published 2026-09-22, is a Seyfert plugin with its own pure-TypeScript MLS and DAVE code on `@noble/*`. It has a per-user receive iterator and a WAV recorder example in the docs. No interop tests against libdave or davey, and no users yet.
- [@ovencord/voice](https://github.com/ovencord/ovencord/tree/main/packages/voice) 0.20.0 is a Bun port of pre-0.19.2 discord.js receive code. It lacks the padding fix and passes still-encrypted packets through on failure.
- Discord's libdave has JS/WASM bindings with a `Decryptor`, but the package `@discordapp/libdave` is private and not on npm ([js/package.json](https://github.com/discord/libdave/blob/main/js/package.json)).

## Python

Every major discord.py-family library ships DAVE join and send in a release with full MLS processing. None ships reliable DAVE receive.

- [discord.py](https://github.com/Rapptz/discord.py) 2.7.0 (2026-02-27) added DAVE via davey ([#10300](https://github.com/Rapptz/discord.py/pull/10300)). It has no receive API. The receive RFC [#1094](https://github.com/Rapptz/discord.py/issues/1094) has been open since 2018.
- [discord-ext-voice-recv](https://github.com/imayhaveborkedit/discord-ext-voice-recv) 0.5.2a179 on PyPI has no DAVE code. Received audio decodes as a corrupted stream ([#61](https://github.com/imayhaveborkedit/discord-ext-voice-recv/issues/61)). Four community PRs add the one missing call, `dave_session.decrypt(user_id, MediaType.audio, payload)`. None is merged and the maintainer has not committed since 2025-06-18. The [zacker150 fork](https://github.com/zacker150/discord-ext-voice-recv) adds hardened receive and needs a pinned discord.py fork.
- [py-cord](https://github.com/Pycord-Development/pycord) has the best-shaped recording API in Python (per-user Sinks). v2.8.1's `reader.py` calls `dave.decrypt` when the SSRC is mapped and substitutes Opus silence on failure ([reader.py at v2.8.1](https://github.com/Pycord-Development/pycord/blob/v2.8.1/discord/voice/receive/reader.py)). The v2.8.1 docs warn recording may not work because of DAVE. The working receive code is PR [#3159](https://github.com/Pycord-Development/pycord/pull/3159). As of today it is open, not a draft, labelled "hold: testing", review decision CHANGES_REQUESTED, milestone 2.9.0rc1, and locked as "too heated". Issue [#3388](https://github.com/Pycord-Development/pycord/issues/3388) reports the UDP keepalive sleeps 5000 seconds, so receive stops after a few minutes.
- [disnake](https://github.com/DisnakeDev/disnake) 2.12.x and [nextcord](https://github.com/nextcord/nextcord) 3.2.0 use DisnakeDev's [dave.py](https://github.com/DisnakeDev/dave.py) libdave binding and are send-only by their own source comments.
- davey on PyPI carries an open conflict. Issue [#20](https://github.com/Snazzah/davey/issues/20) says the 0.1.6 wheel fails every remote decrypt with `NoValidCryptorFound` while a local build works. The maintainer says all artifacts come from the latest commit.

## Rust

### songbird

The standard voice library for serenity and twilight. v0.6.0 (2026-04-05) includes DAVE from [#291](https://github.com/serenity-rs/songbird/pull/291) through the `davey` crate. `ws.rs` handles ops 21, 22, 24, 25, 27, 29 and 30 and sends op 31 only on failure, followed by a session reinit ([ws.rs](https://github.com/serenity-rs/songbird/blob/5d46185511316b8efb3c65a1104e149e4df557cd/src/driver/tasks/ws.rs)). With the `receive` feature, `udp_rx` decrypts the transport layer, looks up the user for the SSRC and calls `dave_session.decrypt(user_id, AUDIO, body)` ([udp_rx/mod.rs at v0.6.0](https://github.com/serenity-rs/songbird/blob/v0.6.0/src/driver/tasks/udp_rx/mod.rs), lines 192 to 250). Events: `VoiceTick` (per-SSRC decoded PCM every 20 ms), `RtpPacket`, `SpeakingStateUpdate`.

Caveats:

- Packets are dropped silently when the SSRC has no user yet (mapping comes only from op 5), when the session is not ready, or when no decryptor exists. There is no buffering.
- After DAVE decryption the plaintext is shorter than the ciphertext. `udp_rx` records the new tail offset, but `SsrcState::get_voice_tick` recomputes the Opus slice from the transport cipher's prefix and suffix only ([ssrc_state.rs at v0.6.0](https://github.com/serenity-rs/songbird/blob/v0.6.0/src/driver/tasks/udp_rx/ssrc_state.rs), lines 84 to 96). The decoder therefore sees trailing bytes. The nymph-ai fork says this made every DAVE packet fail Opus decode ([commit 31b702f](https://github.com/nymph-ai/songbird/commit/31b702f57df6cb9e0aa1ca1aafb39463d6213581)). Other users report working PCM. `RtpPacket` events carry the correct offsets either way.
- Issue [#310](https://github.com/serenity-rs/songbird/issues/310), opened 2026-07-16, describes three silent per-join failures: receive stalls after the bot's own MLS transition, plaintext send before the session is ready, and one member undecryptable after an Append commit the bot made. It has no comments.
- No commits since 2026-04-08. The docs.rs build of 0.6.0 failed.

Recording users: [EchoScribe](https://github.com/Tromador/EchoScribe) and [call-scribe](https://github.com/HazyForge/call-scribe) on songbird 0.6 with `receive`.

### Other Rust

- [cacophony](https://github.com/khoek/cacophony) 0.3.0 (2026-09-07) is the most careful design read. It uses its own OpenMLS 0.9 DAVE crate, switches the sender key only after the transition executes, and queues frames that arrive before the SSRC mapping or the session is ready ([receive.rs](https://github.com/khoek/cacophony/blob/8e12a1cfda7794d326da6291d34b69d006e96cd3/cacophony/src/connection/receive.rs)). Zero stars, one author, AGPL-3.0-only.
- [discordrs](https://github.com/Ingwannu/discord.rs) 2.0.2 records DAVE state but leaves the MLS state machine to the application.
- [tiny-discord-recorder](https://github.com/JacobLinCool/tiny-discord-recorder) is a one-commit recorder. It maps SSRCs by trying each member's ratchet when Speaking events do not arrive, a useful reference.

## JVM

### JDA with JDAVE or libdave-jvm

JDA 6.3.0 (2026-01-11) added a pluggable `DaveSessionFactory`. The default factory advertises protocol version 0, so you must add one. At v6.7.0, `DaveCryptoAdapter` calls `daveSession.decrypt(AUDIO, userId, ...)` after transport decryption ([DaveCryptoAdapter.java at v6.7.0](https://github.com/discord-jda/JDA/blob/v6.7.0/src/main/java/net/dv8tion/jda/internal/audio/DaveCryptoAdapter.java), line 77). `AudioReceiveHandler` offers `handleUserAudio` (per-user PCM), `handleEncodedAudio` (per-user Opus with sequence and timestamp) and a combined mix. Packets from SSRCs not yet mapped by op 5 are dropped. JDA's pinned issue [#904](https://github.com/discord-jda/JDA/issues/904) is titled "Discord does not support voice receive".

[JDAVE](https://github.com/MinnDevelopment/jdave) 0.1.8 (2026-03-29) is by the JDA maintainer. It binds libdave through the Java 25 FFM API and downloads prebuilt natives from his libdave fork v1.1.1. `DaveSessionManager` processes commits and welcomes and sends op 31 only when libdave reports failure ([DaveSessionManager.java](https://github.com/MinnDevelopment/jdave/blob/5d3585f52e80fecdfd4f4bdfcd817c9b30f95f67/api/src/main/java/club/minnced/discord/jdave/manager/DaveSessionManager.java)). No commits since 2026-03-29.

Issue [#32](https://github.com/MinnDevelopment/jdave/issues/32) is open. The reporter says every received sound packet logs "Decrypt failed with error code FAILURE" while the bot keeps working. The maintainer suspects libdave. The last report is on 0.1.8, 2026-05-10. Against that, the wisper-transcribe author closed [issue #39](https://github.com/brandonfhall/wisper-transcribe/issues/39) on 2026-06-15 saying JDA 6.3.0 plus JDAVE 0.1.8 "receives/decrypts E2EE voice audio end-to-end". That bot is Python with a Java sidecar for voice. Whether #32 means lost audio is unknown.

[libdave-jvm](https://github.com/KyokoBot/libdave-jvm) is the JNI alternative for Java 8 and newer, with musl and Windows arm64 natives on Maven Central (0.1.2 and 0.1.3, 2026-03-06). It vendors libdave at commit 52cd56dc from 2026-01-30. No public confirmation of receive through its JDA adapter.

### Discord4J, Kord

- [Discord4J](https://github.com/Discord4J/Discord4J) 3.3.2 (2026-04-02) added DAVE on libdave-jvm. `PacketTransformer` decrypts per user. The PR body says it was written with Codex and tested on a bot that plays sounds ([#1344](https://github.com/Discord4J/Discord4J/pull/1344)). `AudioReceiver` is deprecated.
- [Kord](https://github.com/kordlib/kord) main sends `SUPPORTED_DAVE_PROTOCOL_VERSION = 0`. DAVE with receive exists only in open PR [#1063](https://github.com/kordlib/kord/pull/1063).

## .NET

All serious .NET options P/Invoke libdave's C API.

### NetCord

DAVE since 1.0.0-alpha.461 (2026-02-09, PR [#168](https://github.com/NetCordDev/NetCord/pull/168)). Latest is 1.0.0-beta.23, released 2026-09-24 on GitHub and NuGet. At that tag, `VoiceClient.DaveSession.cs` processes commits with ignored, failed and success branches and sends op 31 only on failure ([VoiceClient.DaveSession.cs](https://github.com/NetCordDev/NetCord/blob/1.0.0-beta.23/NetCord/Gateway/Voice/VoiceClient.DaveSession.cs), lines 173 to 218). `VoiceClient.cs` reads transition IDs big-endian, strips padding and extension, and calls `decryptor.Decrypt(...)` per SSRC before raising `VoiceReceive` ([VoiceClient.cs](https://github.com/NetCordDev/NetCord/blob/1.0.0-beta.23/NetCord/Gateway/Voice/VoiceClient.cs), lines 281, 782, 819). The docs include a `/record` guide. You supply libdave yourself. No open DAVE issues. Very active, one lead maintainer. Every NetCord release is a prerelease, so expect API changes.

### Discord.Net

3.19.0 (2026-03-03) added libdave support. 3.20.1 is the latest. Per-user decoded PCM via `GetStreams()` and `StreamCreated`. Two binding problems confirmed at the 3.20.1 tag:

- `ToCString` allocates 21 bytes with `NativeMemory.Alloc`, which does not zero memory, and never writes a NUL after the digits ([Utils.cs at 3.20.1](https://github.com/discord-net/Discord.Net/blob/3.20.1/tools/Discord.Net.Dave/Utils.cs)). These strings go to libdave as user IDs. Open issue [#3281](https://github.com/discord-net/Discord.Net/issues/3281) reports "Unexpected user ID in add proposal" followed by endless "Malformed Frame".
- `DaveSessionManager` reads op 29 and op 30 transition IDs with `BitConverter.ToUInt16`, which is little-endian on x86 and ARM ([DaveSessionManager.cs](https://github.com/discord-net/Discord.Net/blob/3.20.1/src/Discord.Net.WebSocket/Audio/DaveSessionManager.cs), lines 119 and 125). NetCord and discord.js read big-endian. Runtime impact untested.

### DisCatSharp, DSharpPlus, Disqord

- DisCatSharp.Voice 10.7.0 (2026-03-20) ships DAVE with bundled libdave natives and a receive event carrying user, PCM and Opus. PR [#882](https://github.com/Aiko-IT-Systems/DisCatSharp/pull/882), merged 2026-08-14, says the original code dropped op 30's transition ID and sent op 23 after op 22, which "could leave the sender and receiver transforms on different epochs". The fix is only in 10.7.1 nightlies. The latest stable on NuGet is still 10.7.0.
- DSharpPlus.Voice exists only as 5.0.0 nightlies since 2026-09-10. Source reading found SSRC passed where a user ID is expected in the receive path, and a full MLS reset on every op 22.
- Disqord's DAVE is only on its MyGet nightly feed. It initializes the MLS group with the guild ID and re-keys on every ignored commit.

## C and C++

### D++

DAVE was opt-in from v10.0.32 (2024-10-12) and on by default from v10.1.4 (2025-12-17). v10.1.6 is the latest (2026-08-05), also in vcpkg. It carries its own C++ port of libdave, last synced 2024-11-21 ([commits](https://github.com/brainboxdotcc/DPP/commits/master/src/dpp/dave)). The courier thread decrypts per user and decodes to PCM for `on_voice_receive` ([courier_loop.cpp](https://github.com/brainboxdotcc/DPP/blob/786ceb2e24f286d4e7f4b6b4a5dd934effdee6a4/src/dpp/voice/enabled/courier_loop.cpp)). There is an official `record_user` example.

Caveats:

- `recover_from_invalid_commit_welcome()` builds `{"d", {"transition_id", N}}` with nlohmann::json, which serializes as `"d": ["transition_id", N]`, an array, not an object ([handle_frame.cpp at v10.1.6](https://github.com/brainboxdotcc/DPP/blob/v10.1.6/src/dpp/voice/enabled/handle_frame.cpp), lines 546 to 554). Recovery after a failed commit may not work. How Discord reacts is unknown.
- A transport decryption failure executes `return;` inside the courier loop, which ends that thread (source reading, untested). Open PR [#1635](https://github.com/brainboxdotcc/DPP/pull/1635) fixes an out-of-bounds read in the same loop.
- The port misses upstream libdave fixes, for example the CryptorManager expiry fix in [48e6232](https://github.com/discord/libdave/commit/48e6232e0884e4774576184b9031c1a9ac19a604).
- A third-party per-user recorder on 10.1.4, [discord-voice-recorder](https://github.com/YousofHajHasan/discord-voice-recorder), sends silence every 500 ms until the first packet arrives.

### DCC, MeowLib, discusy

[DCC](https://github.com/Feralthedogg/DCC) (C11, DAVE since v1.5.0 on 2026-07-12) loads libdave at runtime and decrypts per user, but destroys and recreates every decryptor on each commit. MeowLib and discusy are send-only.

## Other languages

- Elixir. [EDA](https://github.com/qoyri/EDA) 0.4.1 on Hex (2026-09-21) processes commits via davey and decrypts per user. The [0.5.0-beta.2 notes](https://github.com/qoyri/EDA/releases/tag/v0.5.0-beta.2) fix received audio that reached consumers still encrypted and sessions that never recovered. [nostrum](https://github.com/Kraigie/nostrum) has DAVE plus receive only on master ([#719](https://github.com/Kraigie/nostrum/pull/719), 2026-03-09). Its last Hex release is 0.10.4 from 2025-03-02. On master a failed decrypt passes ciphertext through silently.
- PHP. DiscordPHP-Voice 8.1.1 has per-user recording built in via `record(RecordingFormat::WAV|OGG, fn(userId) => path)` ([ManagesRecording.php](https://github.com/discord-php/DiscordPHP-Voice/blob/main/src/Discord/Voice/Concerns/ManagesRecording.php)). The DAVE PR was authored by the Copilot bot. The repo has 3 stars.
- Ruby. discordrb has DAVE only in open PR [#453](https://github.com/shardlab/discordrb/pull/453), and no receive API at all.
- Lua. Discordia has AEAD transport but no DAVE and no receive. filispeen/discord.lua is covered in table B.
- Swift, Nim, Zig, Haskell, Dart, Crystal, Clojure. Flight and guildy are covered above. libdave-swift requires macOS 26 on Apple Silicon. Nothing else in these languages has working DAVE.

## Core implementations

Almost every library above wraps one of two engines, libdave or davey.

| Core | Language | Latest | How it does MLS | Used by |
|---|---|---|---|---|
| [discord/libdave](https://github.com/discord/libdave) | C++ with a C API and private WASM bindings | v1.2.1, 2026-09-22 | Reference implementation on mlspp | JDAVE, libdave-jvm (JDA, Discord4J, Koe), golibdave (disgo), cartridge-gg/discordgo, NetCord, Discord.Net, DisCatSharp, Disqord, dave.py (disnake, nextcord), DiscordPHP-Voice, discord.lua, DCC, Flight, discusy |
| [Snazzah/davey](https://github.com/Snazzah/davey) | Rust, bindings for Node and Python | npm 0.1.12, PyPI 0.1.6, crates 0.1.4, all 2026-06-22 | Independent, on OpenMLS 0.8.1, modelled on libdave | @discordjs/voice, Dysnomia/Craig, songbird, discord.py, py-cord, nostrum and EDA (via NIFs) |
| D++ in-tree port | C++ | synced 2024-11-21 | Copy of libdave on mlspp | D++, libkoana (DSharpPlus) |
| [dave-go](https://github.com/thomas-vilte/dave-go) on [mls-go](https://github.com/thomas-vilte/mls-go) | Go | v0.5.1, 2026-07-15 | Pure Go RFC 9420 | disgo (optional), discord-go/discord.go, dgo uses mls-go directly |
| cacophony `dave` | Rust | 0.3.0, 2026-09-07 | OpenMLS 0.9 | cacophony |
| @slipher/voice | TypeScript | 0.1.0, 2026-09-22 | Own pure-TS MLS | Seyfert |
| yeongaori `mls/` | Go | untagged | Welcome-only, no commit processing | yeongaori fork family, PR #1704 |

libdave v1.2.0 (2026-08-26) restored strict welcome validation. v1.2.1 (2026-09-22) added precise commit and welcome errors, early missing-nonce tracking and a CryptorManager expiry fix ([releases](https://github.com/discord/libdave/releases)). Most bindings pin older code. godave pins v1.1.0. JDAVE, the ESCd NuGet package, DiscordPHP-Voice and discord.lua use v1.1.1. libdave-jvm vendors a 2026-01-30 commit. DisCatSharp's bundled natives date from 2026-03-07. davey's last release predates both v1.2 releases, so its parity with them is unknown. libdave issue [#18](https://github.com/discord/libdave/issues/18), a `std::bad_optional_access` thrown from `Session::ProcessCommit`, is still open.

## Discord platform facts

- The developer docs say "we will only support E2EE calls starting on March 1st, 2026" for DMs, group DMs, voice channels and Go Live ([voice-connections.mdx](https://github.com/discord/discord-api-docs/blob/main/developers/topics/voice-connections.mdx)).
- The status incident "A/V E2EE Enforcement for Non-stage Voice Calls" was posted 2026-03-02 12:02 PST. It says DAVE-capable clients "are now required to connect to non-stages voice calls" and that out-of-date clients are rejected with close code 4017. It was resolved 2026-03-11 ([discordstatus incident k22tny62jcw3](https://discordstatus.com/incidents/k22tny62jcw3)).
- Close codes ([opcodes-and-status-codes.mdx](https://github.com/discord/discord-api-docs/blob/main/developers/topics/opcodes-and-status-codes.mdx)): 4016 unknown encryption mode, 4017 "E2EE/DAVE protocol required", 4021 rate limited, 4022 call terminated. The docs say not to reconnect after 4021 or 4022.
- Transport. All xsalsa20 modes and plain `aead_aes256_gcm` were discontinued on 2024-11-18. The supported modes are `aead_aes256_gcm_rtpsize` and `aead_xchacha20_poly1305_rtpsize`. `xsalsa20_poly1305_lite_rtpsize` is also listed as deprecated.
- Negotiation. Identify carries `max_dave_protocol_version`. Sending 0 or omitting it means no DAVE support. The current protocol version is 1 ([version.cpp](https://github.com/discord/libdave/blob/main/cpp/src/version.cpp)). The spec is at [daveprotocol.com](https://daveprotocol.com).
- Stage channels are still exempt. The status incident is scoped to non-stage calls. Discord's blog post of 2026-05-18 says "Stage channels are the one exception" and that once fallback code is removed "it will not be possible to fall back to unencrypted connections" ([blog](https://discord.com/blog/every-voice-and-video-call-on-discord-is-now-end-to-end-encrypted)). Nothing says the stage exemption is permanent.
- Voice receive is not documented by Discord. Library maintainers describe it as unsupported: discord.js calls it "it may work", Discord4J's `AudioReceiver` deprecation text says Discord "does not officially support bots receiving audio", and JDA pins #904.

## Implications for cavbot2

cavbot2 on discordgo v0.29.0 cannot connect to any voice channel today, stage or not. A recording feature means replacing the voice layer whichever path is chosen. Nothing below was tested.

### Stay in Go

1. Keep discordgo for the gateway, commands and REST. Use disgo's `voice` package only for voice. `voice.NewManager` takes a `StateUpdateFunc` and exposes `HandleVoiceStateUpdate` and `HandleVoiceServerUpdate` ([manager.go](https://github.com/disgoorg/disgo/blob/master/voice/manager.go)). discordgo v0.29.0 has `ChannelVoiceJoinManual`, which sends the voice state update without opening its own voice connection. Wiring discordgo events into disgo's manager looks possible from the APIs, but I found no project doing it. What it needs:
   - Pin a disgo master pseudo-version for the #593 padding fix, since no release has it.
   - Add a UDP keepalive through `voice.WithUDPConnCreateFunc`, as Glyphoxa did.
   - Choose a backend. dave-go keeps the build pure Go and the Docker image unchanged. golibdave needs CGO, pkg-config and the libdave v1.1.0 shared library in the image, and has the passthrough copy bug.
   - Accept pre-1.0 disgo API changes and unreleased code in production.
2. Replace discordgo with dgo v1.1.0. Mostly an import and package rename, plus the behavior changes in its `docs/Migration.md`. Pure Go, tagged, full MLS processing. Receive is not live-verified, there is no public issue tracker, and the commit-time receiver reset may cause short gaps at each join or leave.
3. Use the yeongaori fork through a `replace` directive. Smallest diff for discordgo code, but every membership change forces a re-welcome and a decrypt gap.

### Keep the Go bot, add a recorder sidecar

Run a separate recorder process on a proven stack and keep cavbot2's Go code. wisper-transcribe does this with a Java JDA plus JDAVE sidecar next to a Python bot. Candidates are Dysnomia plus davey (Craig's stack) or @discordjs/voice 0.19.2 on Node, or JDA plus JDAVE on Java 25. Costs are a second runtime in the Docker image and a way for the Go bot to start and stop recordings. I did not check whether a sidecar can share cavbot2's bot token and gateway session or needs its own bot application.

### Move off Go

- TypeScript with discord.js and @discordjs/voice, or Dysnomia. Most proven receive path. Stable @discordjs/voice has no RTP timestamps, so multi-track sync needs arrival time or the 1.0 dev build. Dysnomia has timestamps but fewer releases and an Eris-style API.
- Rust with serenity and songbird 0.6.0. Released and used by recorders, but #310 has no maintainer response and the VoiceTick decode question should be settled with a test. Using `RtpPacket` plus your own Opus decoder avoids the second issue.
- C# with NetCord. Cleanest libdave integration read, active maintainer, but prerelease-only versions and a libdave binary to ship.
- Java with JDA and JDAVE. Released, confirmed by one recording bot, but needs Java 25, JDAVE has not changed since March, and #32 is unexplained.

A full move also means porting the milpacs API client, the Xenforo MySQL scan, the Postgres store and the coverage-gated test suite.

### A test that would settle most of this

Per the repo's review gates, smoke-test the chosen stack on the test guild. Have 2 or more people speak, have members join and leave mid-recording, and leave the bot silent for 20 minutes or more. Count decrypt failures per speaker and check for gaps at each membership change and for receive stopping on a silent bot.

## Spot-check notes

These are the ranking claims I re-checked against live sources on 2026-09-24, with changes relative to the research agents' findings.

- Enforcement date. The docs say March 1, 2026. The status incident went up 2026-03-02 12:02 PST. Both are cited above. The status incident is a primary source the agents had not read.
- Stage exemption confirmed from the status incident title and the 2026-05-18 blog post. The Discord support article returned HTTP 403 and was not read.
- disgo v0.19.6. Confirmed the 0x04 mask. Added that v0.19.6 also tests and strips padding before transport decryption, on ciphertext.
- golibdave passthrough `copy(frame, decryptedFrame)` confirmed at golibdave/v0.3.0 line 104.
- JDAVE #32. Added that the reporter says every received sound packet logs the warning. That conflicts with wisper-transcribe's report that receive works. Left unresolved.
- wisper-transcribe. Added that it is a Python bot using a Java sidecar for DAVE receive.
- py-cord #3159. Confirmed not a draft, CHANGES_REQUESTED, "hold: testing", milestone 2.9.0rc1, locked "too heated". wisper-transcribe's issue calls it a draft. That is wrong as of today.
- Confirmed at tags: @discordjs/voice 0.19.2 receive path, Dysnomia v0.2.4 DAVE handling and receive, songbird v0.6.0 receive and the VoiceTick offset code, JDA v6.7.0 decrypt call, NetCord 1.0.0-beta.23 DAVE session and receive, D++ v10.1.6 op 31 JSON, Discord.Net 3.20.1 `ToCString` and `BitConverter` use, dgo v1.1.0 `ProcessCommit`.
- Confirmed release versions and dates on GitHub and registries: @discordjs/voice, Dysnomia, songbird (crates.io), JDA and JDAVE (Maven Central), NetCord and DisCatSharp (NuGet), disgo, godave, dave-go, dgo, libdave, davey, py-cord, DiscordPHP-Voice, EDA and nostrum (Hex), Discord4J (Maven Central), D++.
- Added the disgo `voice.Manager` API facts and discordgo's `ChannelVoiceJoinManual` for the hybrid Go option.

## Unknowns and unverified claims

- No library was tested against a live DAVE voice channel. Every verdict comes from source, release notes, registries and issue threads.
- Receive loss during MLS transitions for @discordjs/voice 0.19.2 and Dysnomia 0.2.4 has never been measured publicly. The 34% claim in discord.js #11441 is unreproduced.
- Whether disgo's missing UDP keepalive stops inbound RTP to a silent bot after 13 to 15 minutes. Only Glyphoxa reports it. py-cord #3388 points the same way for a different library.
- Whether JDAVE #32 warnings mean lost audio.
- Whether songbird's VoiceTick PCM works for DAVE packets. Conflicting reports.
- songbird #310's claim that `max_dave_protocol_version: None` still downgraded calls in July 2026. It conflicts with the 4017 enforcement and is unverified.
- How long the decrypt gaps are for yeongaori's re-welcome path, dgo's commit-time reset and DCC's decryptor rebuild.
- Whether Discord accepts D++'s malformed op 31 payload.
- Whether davey needs changes to match libdave v1.2.0 and v1.2.1, and whether golibdave works against libdave v1.2.x.
- davey #20, the PyPI wheel decrypt failure, is disputed by the maintainer.
- When disgo releases the #593 fix, when @discordjs/voice ships 1.0, whether py-cord merges #3159, and whether nostrum ships DAVE to Hex.
- Universal.Discord.Voice and DiscSharp on NuGet claim DAVE, but their source repositories return 404, so the claims were not checked.
- Whether a recorder sidecar can share a bot token with cavbot2.
- The NetCord user report in [discord-summary-bot](https://github.com/stackoverworld/discord-summary-bot) is the author's claim only.

## Sources

Platform

- Discord voice connections docs, E2EE section: https://github.com/discord/discord-api-docs/blob/main/developers/topics/voice-connections.mdx
- Voice close codes: https://github.com/discord/discord-api-docs/blob/main/developers/topics/opcodes-and-status-codes.mdx
- Status incident, 2026-03-02: https://discordstatus.com/incidents/k22tny62jcw3
- Discord blog, 2026-05-18: https://discord.com/blog/every-voice-and-video-call-on-discord-is-now-end-to-end-encrypted
- DAVE protocol whitepaper: https://daveprotocol.com
- libdave releases and C API: https://github.com/discord/libdave/releases, https://github.com/discord/libdave/blob/main/cpp/includes/dave/dave.h

Go

- discordgo v0.29.0 voice.go: https://github.com/bwmarrin/discordgo/blob/v0.29.0/voice.go
- discordgo DAVE PR #1704: https://github.com/bwmarrin/discordgo/pull/1704
- disgo v0.19.6 udp_conn.go: https://github.com/disgoorg/disgo/blob/v0.19.6/voice/udp_conn.go
- disgo #593: https://github.com/disgoorg/disgo/issues/593
- disgo v0.19.6...master: https://github.com/disgoorg/disgo/compare/v0.19.6...master
- golibdave v0.3.0: https://github.com/disgoorg/godave/blob/f34f98224dafa0d6f89d2c36baff473ab34ec999/golibdave/golibdave.go
- dave-go session.go: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go
- dgo v1.1.0 dave.go: https://github.com/darui3018823/dgo/blob/v1.1.0/dave.go
- yeongaori fork dave.go: https://github.com/yeongaori/discordgo-fork/blob/94d3e03d65d14a4a6876ccfb973c0232d1a17b90/dave.go
- Glyphoxa ADR-0064: https://github.com/MrWong99/Glyphoxa/blob/main/docs/adr/0064-voice-media-path-liveness.md

JavaScript

- @discordjs/voice 0.19.2 release: https://github.com/discordjs/discord.js/releases/tag/%40discordjs%2Fvoice%400.19.2
- npm registry: https://registry.npmjs.org/@discordjs%2Fvoice
- DAVESession.ts: https://github.com/discordjs/discord.js/blob/dbb7490627f5102db53acb55c809361d1ac04659/packages/voice/src/networking/DAVESession.ts
- Dysnomia v0.2.4 VoiceConnection.js: https://github.com/projectdysnomia/dysnomia/blob/v0.2.4/lib/voice/VoiceConnection.js
- Craig bot package.json: https://github.com/CraigChat/craig/blob/master/apps/bot/package.json
- davey session.rs: https://github.com/Snazzah/davey/blob/a1e2e741bea06bc3b7167a5c3792844b8975993c/davey/src/session.rs

Python

- discord.py PR #10300: https://github.com/Rapptz/discord.py/pull/10300
- py-cord v2.8.1 reader.py: https://github.com/Pycord-Development/pycord/blob/v2.8.1/discord/voice/receive/reader.py
- py-cord PR #3159: https://github.com/Pycord-Development/pycord/pull/3159
- py-cord #3388: https://github.com/Pycord-Development/pycord/issues/3388
- davey #20: https://github.com/Snazzah/davey/issues/20

Rust

- songbird v0.6.0 release: https://github.com/serenity-rs/songbird/releases/tag/v0.6.0
- songbird udp_rx and ssrc_state at v0.6.0: https://github.com/serenity-rs/songbird/blob/v0.6.0/src/driver/tasks/udp_rx/mod.rs, https://github.com/serenity-rs/songbird/blob/v0.6.0/src/driver/tasks/udp_rx/ssrc_state.rs
- songbird #310: https://github.com/serenity-rs/songbird/issues/310

JVM

- JDA v6.3.0 release: https://github.com/discord-jda/JDA/releases/tag/v6.3.0
- JDA Maven metadata: https://repo1.maven.org/maven2/net/dv8tion/JDA/maven-metadata.xml
- JDAVE Maven metadata: https://repo1.maven.org/maven2/club/minnced/jdave-api/maven-metadata.xml
- JDAVE #32: https://github.com/MinnDevelopment/jdave/issues/32
- wisper-transcribe #39: https://github.com/brandonfhall/wisper-transcribe/issues/39
- Discord4J PR #1344: https://github.com/Discord4J/Discord4J/pull/1344

.NET

- NetCord releases: https://github.com/NetCordDev/NetCord/releases
- NetCord NuGet: https://www.nuget.org/packages/NetCord
- Discord.Net #3281: https://github.com/discord-net/Discord.Net/issues/3281
- DisCatSharp PR #882: https://github.com/Aiko-IT-Systems/DisCatSharp/pull/882

C++

- D++ v10.1.6 handle_frame.cpp: https://github.com/brainboxdotcc/DPP/blob/v10.1.6/src/dpp/voice/enabled/handle_frame.cpp
- D++ releases: https://github.com/brainboxdotcc/DPP/releases
