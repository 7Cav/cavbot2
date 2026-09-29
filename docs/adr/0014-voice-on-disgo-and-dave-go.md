# Voice runs on disgo's voice package and dave-go, inside the bot

Status: accepted. Decided in the grilling of #10, before any implementation.

Discord enforces DAVE end-to-end encryption on every non-stage voice call, and
no discordgo release can join voice at all. The bot keeps discordgo for the
gateway, the commands and everything else it does today, and hands voice to
disgo's `voice` package, pinned to a master commit, with
`thomas-vilte/dave-go` as the DAVE session. discordgo sends the voice state
update and forwards its `VOICE_STATE_UPDATE` and `VOICE_SERVER_UPDATE` events
to disgo. Recording is done by recorders: dedicated accounts, each its own
Discord application, whose gateway sessions run in the bot process beside
the main one. The main bot never joins voice. The throwaway prototype on
`chore/10-voice-recorder-prototype` measured this stack on the test guild:
10 MLS commits during speech with no decrypt failures, and tracks within
0.05 s of wall-clock time over 28 minutes.

## Considered options

- **Move the whole bot to disgo.** Every command would be rewritten for one
  feature.
- **A discordgo DAVE fork** (yeongaori and its descendants). They answer every
  commit with a re-welcome, so each join or leave costs a decrypt gap, and
  none has a tag.
- **darui3018823/dgo.** The smallest API change from discordgo, but its receive
  path is unverified and it drops receiver keys at commit time.
- **disgo v0.19.6.** It loses 4 to 11% of received frames to disgo#593. The fix
  is on master only.
- **golibdave instead of dave-go.** It wraps Discord's own libdave, but needs
  CGO and a shared library in the image, pins libdave v1.1.0, and has a
  `Decrypt` copy bug. disgo takes either through `godave.Session`, so it stays
  the fallback.
- **The main bot as the recorder.** No extra application, but only one
  recording at a time ever, since an account is in one voice channel per
  guild.
- **Recorders in a separate process.** Crashes and deploys would stay apart,
  at the cost of a second service. A deploy that waits for recordings to
  finish covers deploys instead.

## Consequences

- The build stays CGO-free.
- Another simultaneous recording is another recorder application and token,
  with no code change.
- Every recorder holds Administrator, like the main bot. Its token sits in the
  same environment as the main bot's, so least privilege would protect
  nothing.
- Move off the master pin to the first disgo release that carries the #593
  fix.
- disgo's default audio receiver is replaced. It busy-spins while DAVE is not
  ready and stops for good after a voice reconnect.
- A panic in the voice code would take down temp VC and every command with
  it, so the voice goroutines recover.
- Temp VC ignores every recorder: no spawn from a hub, no occupancy, no guest
  entry.
- dave-go has one author and no audit. If it goes stale or breaks on a
  protocol change, swap in golibdave.
