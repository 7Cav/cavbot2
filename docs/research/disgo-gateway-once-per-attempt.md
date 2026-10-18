# Does disgo's voice gateway hold up when used once per attempt

Research for [Does disgo's voice gateway hold up when used once per attempt?][issue-813]
on [Wayfinder map: owned voice connection for recorders][issue-810].
The map's spec builds a new voice gateway for each connection attempt, uses
it until it closes, and never reopens or resumes it. This note asks whether
disgo's known gateway defects still reach a recorder used that way, and what
a voice gateway client of our own would cover, at what size.
[Which voice gateway client does the replacement use?][issue-817] makes the
choice. This note gives it the facts and makes no pick.

Sources, each pinned:

- disgo at `995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc` (2026-09-23), the
  pseudo-version on `go.mod` line 9, read from the Go module cache. Line
  links point at that commit. disgo master at `2d889db` (2026-10-17) is 7
  commits ahead and changes nothing under `voice/` ([compare][dg-compare]).
- godave v0.3.0 at `511eb1e` (`go.mod` line 10), and dave-go v0.5.1 at
  `7a73cea` (`go.mod` line 18), the DAVE session the recorders use.
- gorilla/websocket v1.5.3, from the module cache.
- Discord's API docs, `discord/discord-api-docs` at `f7416a2` (2026-10-16),
  and Discord's DAVE whitepaper, `discord/dave-protocol` at `1b2b706`
  (2025-08-14).
- Upstream disgo issues and PRs, read with `gh` on 2026-10-18.
- This repo at `origin/develop` `82c3df1`.
- The triage measurements in
  [A recorder's voice connection closed as Hello arrives can leave disgo's heartbeat running, and -race reports it][issue-768]
  and
  [A recorder's voice connection that closes before Ready leaves a disgo goroutine running forever][issue-772].
  I ran no tests for this note. Where a claim comes from reading code rather
  than a measurement, the text says so.

## Answer

The heartbeat that outlives a close still leaks on a once-used gateway, and
cannot reach the recorder. The race sits inside one gateway's life. Hello
starts the heartbeat, and a close that lands before the heartbeat stores its
cancel misses it. On a discarded gateway the leaked heartbeat calls `Send`
each tick, and `Send` refuses, because the status never returns to Ready. It
writes nothing, starts no reconnect and calls no callback. One idle goroutine
per leak stays, and so does the `-race` report. Today's visible harm needs
`dialUntilOpen` to reopen the same gateway, and the new design never does.

The `listen` goroutine that a close before Ready leaves behind still reaches
the recorder, unchanged. Each attempt whose context ends after the voice
server accepts the websocket and before Ready leaves one goroutine blocked
for good, about 7 KB, with no socket and no CPU. Members never notice. A retry
policy that keeps timing out against a server that never sends Ready adds one
per attempt.

The lock-order deadlock between `Send` and `CloseWithCode` still reaches the
recorder. The pinned code has it, master has it, and the upstream fix is an
open PR that conflicts with master. A once-used gateway still closes once,
and that close can land while the heartbeat, Select Protocol or a DAVE message
is being sent. Then the closer, the sender and `listen` block for good, and
`Close`, `Status` and `Send` on that gateway never return. A state machine
that waits on any of them hangs, with the recorder deaf. One that never waits
on a discarded gateway can end the attempt, and leaks those goroutines.

The Resume's `seq` field cannot reach the recorder while the recorder refuses
disgo's own dials, as it does today. disgo tags the Resume's sequence field
`seq`, where Discord's v8 docs ask for `seq_ack`. A fresh gateway always
identifies. disgo sends Resume only from its own reconnect. That reconnect
starts by itself after any drop past Ready, runs on a background context the
caller can't cancel, and has no option to turn it off. The recorder's dialer
refuses its dial, so disgo hands the close over at once. Without that dialer,
every drop after Ready would resume inside the attempt for 15 s or more before
the caller hears of it, and a discarded gateway could come back on its own.

The `-race` reports still fire against a once-used gateway on real sockets.
All three races happen within one gateway's life, and triage reproduced them
with a single Join. No test can close disgo's gateway after Hello under
`-race` and pass, at the pin or on master.

A client of our own covers 21 opcodes and 16 close codes. The recorder sends
7 of the opcodes and only receives the other 14. Dropping resume and disgo's
reconnect removes 165 of `gateway.go`'s 656 lines. The guarantees the state
machine needs add back about 80 to 190. With disgo's message and opcode types
imported, the client comes to about 500 to 650 lines, close to disgo's own
file. Copying the types as well makes it about 1,100 to 1,300.

## How the recorder drives disgo today

The recorder uses disgo's voice manager and `Conn`, and wraps the gateway in
`renewableGateway`, which builds a fresh disgo gateway on demand
([gateway.go:196-214][repo-connopts], [gateway.go:592-598][repo-renewable]).
It builds a new one after a drop ([gateway.go:1087][repo-watchclose]), when
Discord names a later voice server after an open succeeded
([gateway.go:947-957][repo-replace]), when DAVE starts over
([gateway.go:1008-1022][repo-reopen]), and before it leaves to join again
([gateway.go:410][repo-leave-again]).

One reuse is left. `dialUntilOpen` dials again when Discord names another
voice server while a dial is in flight, and `startDial` hands it the same
gateway, since only `replace` swaps it
([gateway.go:900-938][repo-dialuntil], [gateway.go:968-982][repo-startdial]).
That reopen is the harm path in the heartbeat issue's triage.

Three more facts matter below:

- Every gateway built for one disgo `Conn` shares that `Conn`'s DAVE session
  ([conn.go:81-84][dgc-new], [gateway.go:200-208][repo-connopts]).
- The recorder refuses every websocket dial that its own open didn't mark
  ([gateway.go:120-170][repo-owndials]). That came out of
  [A voice outage disgo resumes from gets no gap, and a longer one starts its gap about 15 s late][issue-769].
- The recorder sends no audio and no Speaking. It joins self-muted, and its
  UDP `Write` refuses ([gateway.go:326-328][repo-selfmute],
  [udp.go:40-41][repo-udp-noaudio], [udp.go:269-270][repo-udp-write]).

## The heartbeat that outlives a close

`listen` starts the heartbeat with `go g.heartbeat()` when Hello arrives, then
identifies ([gateway.go:550-563][dg-listen-hello]). The new goroutine stores
its cancel in `heartbeatCancel` ([gateway.go:380-383][dg-hb-store]).
`CloseWithCode` reads that field before it takes any lock
([gateway.go:227-231][dg-close-hb]). A close that runs before the store finds
nil, and nothing cancels the heartbeat. The heartbeat issue's triage measured
it with real Joins: 85 of 248 closes leaked, a close in the first 40 µs after
the server accepted the socket leaked 7 or 8 times in 8, and past about
400 µs almost none did.

One gateway per attempt changes none of this. The race needs one Hello and
one close on the same gateway, and every attempt that reaches Hello has both.

What a leaked heartbeat does on a discarded gateway, from reading the code:

- Each tick it checks whether the last heartbeat went unanswered
  ([gateway.go:392-401][dg-hb-zombie]). `open` set the send time before the
  dial and the heartbeat set the answer time when it started
  ([gateway.go:167][dg-open-dial], [gateway.go:383][dg-hb-store]), so the
  check passes and it calls `sendHeartbeat`.
- `sendHeartbeat` calls `Send` ([gateway.go:406-426][dg-sendhb]). `Send`
  returns `ErrShardNotReady` unless the status is Ready
  ([gateway.go:260-268][dg-send]). A gateway closed before Ready never gets
  there again: its `listen` exits on the next read, and only `listen` sets
  Ready ([gateway.go:565-577][dg-listen-ready]). `sendHeartbeat` returns
  quietly on every error except `ErrGatewayNotConnected`.
- So it writes nothing, never reaches the nil connection in `send`
  ([gateway.go:299-310][dg-send-conn]), starts no reconnect and calls no
  callback. It is bound to its own gateway, so it can't reach the next
  attempt's.
- It logs `sending heartbeat` at DEBUG each tick. The recorder raises a
  closed connection's log level above ERROR today
  ([gateway.go:315-324][repo-close]), so those lines go nowhere.
- It runs until the process exits, and keeps the gateway reachable, along
  with whatever the caller wired into its callbacks. Nobody has measured that
  memory.
- A second `Close` on the same gateway would cancel it, since the field is set
  by then. That read is still a data race.

It writes only if the status turns Ready after the close, which needs `listen`
to be handling a Ready frame as the close lands
([gateway.go:565-571][dg-listen-ready], [gateway.go:249-251][dg-close-status]).
The leak needs the close within about 400 µs of the server accepting the
socket, and a Ready from
Discord needs a round trip after Identify. I infer the two windows don't meet
against Discord. A local test server could answer that fast. Then the
heartbeat's `Send` would pass, `send` would find no connection, and
`sendHeartbeat` would close and start disgo's reconnect. Under the recorder's
dialer that reconnect fails its dial and calls the close handler at once
([gateway.go:341-343][dg-doreconnect], [gateway.go:369-375][dg-reconnect]).

Today's harm comes from the reuse. After a redial, the same gateway opens
again, and a leaked heartbeat runs beside the new connection's own. Both
write `lastNonce`, so an answer to one heartbeat can arrive while the other's
nonce is stored, and the nonce check closes and reconnects mid-recording
([gateway.go:579-587][dg-listen-ack]). The triage read this from the code and
didn't reproduce it. A gateway used once never shares its fields with a live
connection, so that path is gone.

## The listen goroutine a close before Ready leaves

`open` waits for Ready on an unbuffered channel, selecting on its context as
well ([gateway.go:200-220][dg-ready-chan]). When the context ends first,
`open` closes the gateway and returns, and nothing reads the channel again.
`listen`'s next read fails, the status isn't Ready, and it hands the error to
the ready callback ([gateway.go:482-489][dg-listen-notready]), which blocks on
the channel send at line 204 for good.

One gateway per attempt changes nothing here either. Every attempt whose
context ends after the voice server accepts the websocket and before Ready
leaves one goroutine. The triage of the goroutine issue saw 248 of 248
cancelled Joins leak, 20 of 20 in its reproduction and 5 of 5 on a redial.
Each holds about 7 KB, no socket (open file descriptors stayed at 8 across 50
leaks), no CPU and no lock anything else waits on.

In the new design, these paths cancel inside that window: an attempt deadline
that expires while waiting for Ready, a stop or shutdown during a join, and a
new attempt when Discord names another voice server while the old one waits
for Ready. A policy that retries every 10 s against a server that accepts
websockets and never sends Ready would leak 360 goroutines an hour, about
2.5 MB at the triage's 7 KB. That is arithmetic, not a measurement. Every
deploy restarts the bot and frees them.

Two ways around it inside disgo, both from reading:

- A `Close` from another goroutine before Ready doesn't leak, because `open`
  is still selecting and reads the error. But `Open` runs a retry loop with no
  attempt limit, and dials again 1 s later unless its context ends during the
  wait ([gateway.go:144-146][dg-open-api],
  [gateway.go:316-360][dg-doreconnect]). The recorder's dialer lets those
  dials through, since they carry the open's marked context. Cancelling the
  context while `open` still selects can pick the context branch and leak.
- A dialer that refuses a gateway's second dial would end that loop. Its
  error would wrap `ErrGatewayAlreadyConnected`, as the recorder's refusal
  does, and `doReconnect` returns on that error
  ([gateway.go:341-343][dg-doreconnect], [gateway.go:137-140][repo-refused-err]).
  Nobody has tried this.

## The lock-order deadlock between Send and CloseWithCode

The pinned code takes the two locks in opposite orders:

- `Send` takes `statusMu` and holds it until it returns, then takes `connMu`
  inside `send` ([gateway.go:260-268][dg-send],
  [gateway.go:299-310][dg-send-conn]). It reaches `connMu` only when the
  status is Ready.
- `CloseWithCode` takes `connMu` and holds it until it returns, then takes
  `statusMu` to mark the gateway disconnected
  ([gateway.go:233-251][dg-close-conn]). `open` also takes `connMu` before
  `statusMu` ([gateway.go:151-164][dg-open-lock]), but the status is never
  Ready while `open` runs, so a `Send` never waits on `connMu` against it.

A `Send` holding `statusMu` and a `CloseWithCode` holding `connMu` then wait
for each other for good. `Send`'s window is the status check and the marshal
before it takes `connMu` ([gateway.go:270-297][dg-sendinternal]).
`CloseWithCode`'s is the close frame write and the socket close before it
takes `statusMu`.

These paths call `Send` while the gateway is Ready:

- the heartbeat goroutine, every tick ([gateway.go:406-426][dg-sendhb]);
- `listen`, through disgo's `Conn`, for Select Protocol right after Ready
  ([conn.go:252-276][dgc-ready]), and for every DAVE message dave-go sends
  ([conn.go:115-129][dgc-callbacks]). dave-go sends from inside the
  `OnDave*` and `OnSelectProtocolAck` calls `listen` makes
  ([gateway.go:589-628][dg-listen-dave]);
- `SetSpeaking` ([conn.go:155-160][dgc-speaking]), which the recorder never
  calls.

These paths call `CloseWithCode` while another goroutine may be in `Send`:

- the caller, from any goroutine. Today that is `renewableGateway.Close`,
  `replace`, `reopen`, and `wentSilent` from a timer goroutine
  ([gateway.go:1024-1036][repo-gw-close], [gateway.go:947-957][repo-replace],
  [gateway.go:1008-1022][repo-reopen], [gateway.go:777-794][repo-silent]);
- `listen`, after a failed read while Ready or a nonce mismatch, against the
  heartbeat's `Send` ([gateway.go:491-536][dg-listen-reconnect],
  [gateway.go:579-587][dg-listen-ack]);
- the heartbeat, on a missed answer or a failed send, against `listen`'s
  sends ([gateway.go:392-401][dg-hb-zombie], [gateway.go:406-426][dg-sendhb]).

A once-used gateway still has its one close, and that close can land during a
`Send`. So the deadlock can still reach it.

What follows, from reading. `CloseWithCode` closed the socket before it
blocked, so no socket leaks. The closer and the sender stay blocked, and
`listen`'s next read fails and blocks on `statusMu`
([gateway.go:482-489][dg-listen-notready]). `Status` takes `statusMu` and
blocks too. When `listen` is the closer, it never reaches the reconnect or the
close handler, so nothing reports the drop
([gateway.go:529-534][dg-listen-reconnect]). When the sender is a dave-go
callback, dave-go still holds its session lock, which it keeps through every
send and retry ([retry.go:31-39][dv-retry-lock]), and `Decrypt` takes that
same lock ([session.go:432-438][dv-decrypt]). Decryption on that session then
stops for good. Today one DAVE session serves every gateway of a `Conn`, so
the hang would carry into the next gateway.

What the recorder sees today: if `wentSilent`'s close deadlocks, it never
calls the drop handler, no rejoin starts, and the recorder stays deaf until a
restart ([gateway.go:792-793][repo-silent]). In the new design the state
machine can end the attempt only if it never waits on a call into that
gateway, since `Close`, `Status` and `Send` can each block for good. It also
needs a deadline of its own for the case where `listen` deadlocked and
reported nothing. Nobody has measured how often this happens. The window per
send is the few microseconds of a status check and a marshal, the sends are
one heartbeat per interval plus DAVE bursts when members join or leave, and
the close is one per attempt.

[fix(voice): prevent gateway lock order deadlock][dg-pr-588] has been open
since 2026-08-11, with no reviews. GitHub reports it conflicts with master.
Its diff adds `voice/gateway_test.go`, which master gained when
[fix(voice): recover connections with fresh sessions][dg-pr-587] merged on
2026-08-25. That merged PR left `Send` and `CloseWithCode` alone, so the
pinned file has the same two lock orders the PR describes. The PR's fix has
`Send` check the status and release `statusMu`, take `connMu`, then take
`statusMu` again, the same order as the close and open paths.

## The Resume's seq field, and disgo's own reconnect

### The defect

disgo records the sequence number of each server message whose number is
above zero ([gateway.go:545-547][dg-listen-seq]). For a JSON message that
number comes from the key `s` ([gateway_messages.go:12-18][dgm-message],
[gateway_messages.go:20-26][dgm-json-seq]). For a binary message it comes
from the 2-byte header ([gateway_messages.go:128-183][dgm-binary]).

Discord's v8 docs show the number as a top-level `seq` on the message, and
require the last one received as `seq_ack` in both Heartbeat and Resume. A
client that has received none may omit `seq_ack` or send -1
([voice-connections.mdx:553-580][dd-buffered]).

disgo's Heartbeat tags its field `seq_ack`, which matches
([gateway_messages.go:223-226][dgm-heartbeat]). The Resume names its field
`SeqAck` but tags it `seq`
([gateway_messages.go:307-312][dgm-resume]):

```go
SeqAck    int          `json:"seq"`
```

`resume` fills it from the recorded number
([gateway.go:454-473][dg-resume]). So disgo's Resume carries `seq` and no
`seq_ack`. From the docs I infer that a server reads it as a client that
received nothing, so it can't replay only what the client missed. If Discord
sends `seq` in JSON as its docs show, disgo also never records a JSON
message's number, and both the heartbeat's `seq_ack` and the choice between
Identify and Resume run on the last binary number, or zero. Nobody has
captured Discord's traffic to confirm either.

### Whether a gateway that never resumes can reach it

A fresh gateway can't send Resume on its first Hello. `listen` resumes only
when both the SSRC and the sequence number are non-zero
([gateway.go:550-563][dg-listen-hello]). Both start at zero, and `open` clears
them when the session changes ([gateway.go:157-160][dg-open-reset]). The SSRC
turns non-zero at Ready. The sequence number turns non-zero at the first
numbered message, and DAVE's binary messages all carry one
([voice-connections.mdx:242-252][dd-binary]).

disgo sends Resume from its own reconnect, which the caller never asks for.
These paths start it, each only after Ready:

- a failed read or a close code disgo's table marks to resume
  ([gateway.go:491-536][dg-listen-reconnect]);
- a heartbeat that got no answer by the next tick
  ([gateway.go:392-401][dg-hb-zombie]);
- a heartbeat sent with no connection ([gateway.go:406-426][dg-sendhb]);
- a heartbeat answer with the wrong nonce
  ([gateway.go:579-587][dg-listen-ack]).

Each closes with 1012, which keeps the SSRC and the sequence number, since
only 1000 and 1001 clear them ([gateway.go:233-247][dg-close-conn]). Each then
starts `reconnect`, which runs `doReconnect` on `context.Background()` with
up to 5 tries ([gateway.go:369-375][dg-reconnect]). The tries wait 0, 1, 2, 4
and 8 s ([gateway.go:316-367][dg-doreconnect]). A try that connects gets
Hello and sends Resume, with `seq`. The close handler hears of it only after
the fifth try fails, on a close code disgo won't resume from, or on
`ErrGatewayAlreadyConnected` ([gateway.go:341-351][dg-doreconnect]). The
close observer runs in its own goroutine before the reconnect starts, and is
the only earlier signal ([gateway.go:524-526][dg-listen-reconnect]).

The caller can't turn this off through disgo. The gateway config holds a
logger, a dialer and the close observer, nothing else
([gateway_config.go:16-20][dg-config]). A `Close` doesn't stop the loop
either, from reading: the loop runs on a background context, and a close
during its wait finds no connection and changes nothing the loop checks. The
next try dials again, so a gateway the caller discarded can come back,
connect to the old voice server with the old session, and dispatch events to
the caller's handler.

The recorder's dialer is what turns it off today. A dial without the mark
from the recorder's own open fails with an error wrapping
`ErrGatewayAlreadyConnected` ([gateway.go:134-170][repo-owndials]).
`doReconnect` returns on its first try, and `reconnect` hands the error to
the close handler at once. With that dialer a once-used gateway never sends
Resume, and every drop past Ready reaches the recorder as a close. The
recorder's comment on it says nobody has seen a resume bring audio back
([gateway.go:120-132][repo-resume-note]). An upstream report shows a 1006,
then disgo's reconnect, then a 4006 on its first try
([No audio received after voice gateway connection interrupted][dg-issue-534]).
It can't tell whether the field name caused the refusal.

## Other close paths found while reading

None of these has been seen. Each follows from the code.

- A close from another goroutine in the instructions between `open`
  releasing `connMu` and starting `listen` hands `listen` a nil connection
  ([gateway.go:192-202][dg-open-conn]). gorilla's `NextReader` reads a field
  of its receiver first ([conn.go:998-1003][gw-nextreader]), so `listen`
  panics. The panic is in a disgo goroutine with no recover, so the process
  exits. ADR 0014 has the voice goroutines recover
  ([0014:52-53][repo-adr14-recover]), but the recorder can't wrap disgo's own.
- A close that lands while `listen` handles Ready can leave the status Ready
  on a gateway with no connection ([gateway.go:565-571][dg-listen-ready]).
  If `open` still waits, it reads that Ready and returns nil for a closed
  gateway ([gateway.go:209-220][dg-open-select]).
- Messages a closed gateway's `listen` already read still reach the event
  handler ([gateway.go:631][dg-listen-event]). Today the DAVE callbacks go
  through `renewableGateway`, which sends on whichever gateway is current
  ([conn.go:115-129][dgc-callbacks], [gateway.go:1040-1042][repo-gw-send]).
  A state machine needs to drop events and callbacks by gateway.

## Whether -race still fires

Yes. The triage of the heartbeat issue reports three races, and none of them
needs a second open:

- `heartbeat` writes `heartbeatCancel` and `CloseWithCode` reads it, with no
  lock between ([gateway.go:380-381][dg-hb-store],
  [gateway.go:227-231][dg-close-hb]). Any test that closes after Hello has
  both accesses. The triage reproduced it with a single Join, on the first
  `-count=20` run.
- `open` reads `g.conn` to start `listen` after releasing `connMu`, and
  `CloseWithCode` sets it to nil ([gateway.go:192-202][dg-open-conn],
  [gateway.go:233-247][dg-close-conn]). It needs no Hello, only a close from a
  goroutine other than `open`'s while `Open` runs. It failed 1 of 900 `-race`
  iterations on develop. From reading, a test whose close comes after `Open`
  returns, handed over through a channel, orders the two accesses and
  wouldn't report it.
- `listen` reads the SSRC and sequence number on Hello, and `CloseWithCode`
  zeroes them ([gateway.go:550-563][dg-listen-hello],
  [gateway.go:243-247][dg-close-conn]). The write happens only for codes 1000
  and 1001. `Close` uses 1000, and so does `open`'s close when its context
  ends ([gateway.go:223-225][dg-close], [gateway.go:209-212][dg-open-select]).
  A `CloseWithCode` with 1012 skips it.

Reading the code turns up three more unguarded pairs between the heartbeat
and `listen`. `lastNonce` is written at [line 409][dg-sendhb] and read at
[581][dg-listen-ack], `lastHeartbeatReceived` written at
[587][dg-listen-ack] and read at [393][dg-hb-zombie], and the sequence number
written at [546][dg-listen-seq] and read at [415][dg-sendhb]. A test that runs through a heartbeat
and its answer would expose them, unless something else in the test orders
them, such as a logger lock both goroutines take.
[fix: guard gateway & voice shared state against data races][dg-pr-559]
guards all of these except `g.conn`.

The deadlock is not a data race, so `-race` doesn't report it. A test that
hits it hangs until `go test`'s timeout, 10 minutes by default
([testing flags][go-testflags]).

## Upstream state

| Item | State | Covers |
|---|---|---|
| [fix: guard gateway & voice shared state against data races][dg-pr-559] | Open since 2026-06-27, no reviews, conflicts with master | Guards `heartbeatCancel`, the heartbeat times, `lastNonce`, SSRC and sequence number. The heartbeat issue's triage applied it: the leak stayed at 80 of 248, and the `g.conn` race still failed 2 of 3 runs. |
| [fix(voice): prevent gateway lock order deadlock][dg-pr-588] | Open since 2026-08-11, no reviews, conflicts with master | The `Send` and `CloseWithCode` lock orders. |
| [voice: v8 sequence fields named differently from the docs (Resume sends `seq`, messages read from `s`)][dg-issue-626] | Filed 2026-10-17 from this repo's triage, closed by its author the same day without a fix | The Resume's `seq` and the `s` key. |
| [Data race in gateway implementation][dg-issue-494] | Open since 2025-11-17 | The same `heartbeatCancel` race in disgo's main gateway, not the voice one. |
| [fix: resolve data race in gateway heartbeat handling][dg-pr-529] | Closed unmerged 2026-03-21 | A fix for the main gateway's race. A maintainer called its author a spam account. |
| [No audio received after voice gateway connection interrupted][dg-issue-534] | Closed 2026-06-27 | A 4006 on disgo's first reconnect try. A maintainer said a 4006 needs a full voice reconnect, and that disgo gave no way to hook the close. |

I found nothing upstream on the ready channel or the `listen` goroutine,
searching issues and PRs for "ready channel", "goroutine", "leak" and
"deadlock". disgo master's `voice/gateway.go`, `gateway_messages.go` and
`gateway_opcodes.go` match the pin ([compare][dg-compare]).

## What a client of our own has to cover

### Handshake and heartbeat

1. Dial `wss://<endpoint>?v=8`. Version 8 adds the server's message buffer
   and replay on resume ([voice-connections.mdx:20-32][dd-versions],
   [gateway.go:166][dg-open-dial]).
2. The server sends op 8 Hello with `heartbeat_interval` in milliseconds
   ([voice-connections.mdx:124-138][dd-heartbeat]). disgo decodes it as a
   float ([gateway_messages.go:209-211][dgm-hello]).
3. The client sends op 0 Identify with `server_id`, `user_id`, `session_id`,
   `token` and `max_dave_protocol_version`. Version 0, or no field, means no
   DAVE ([voice-connections.mdx:82-100][dd-identify],
   [voice-connections.mdx:254-262][dd-dave-version]). Discord has supported
   only end-to-end encrypted calls since 2026-03-01
   ([voice-connections.mdx:227-240][dd-dave]).
4. The server sends op 2 Ready with `ssrc`, `ip`, `port` and `modes`. Its
   `heartbeat_interval` field is wrong and should be ignored
   ([voice-connections.mdx:102-122][dd-ready]).
5. The client opens UDP, runs IP discovery, and sends op 1 Select Protocol
   with `protocol: "udp"` and `data` holding `address`, `port` and `mode`
   ([voice-connections.mdx:191-225][dd-select],
   [voice-connections.mdx:582-592][dd-ip]). It must support
   `aead_xchacha20_poly1305_rtpsize` and should prefer
   `aead_aes256_gcm_rtpsize` ([voice-connections.mdx:410-436][dd-modes]).
6. The server sends op 4 Session Description with `mode`, `secret_key`
   (32 bytes) and `dave_protocol_version`
   ([voice-connections.mdx:438-452][dd-session]).
7. From Hello on, the client sends op 3 Heartbeat every interval, with `t`, an
   integer nonce, and `seq_ack`, the last sequence number received. The server
   answers with op 6 Heartbeat ACK carrying the same `t`
   ([voice-connections.mdx:140-179][dd-ack]). `seq_ack` may be omitted or -1
   before any numbered message, and the number wraps
   ([voice-connections.mdx:553-580][dd-buffered]). The voice docs name no
   jitter. disgo sends the first heartbeat one interval after Hello, sends
   none until Ready, and notices a missing answer at the next tick
   ([gateway.go:377-404][dg-heartbeat], [gateway.go:260-268][dg-send]).
8. JSON messages Discord may replay carry a top-level sequence number, and
   binary ones carry it in the header
   ([voice-connections.mdx:553-580][dd-buffered],
   [voice-connections.mdx:242-252][dd-binary]). A client that never resumes
   needs it only for `seq_ack`.

### Opcodes

The opcode list is Discord's table
([opcodes-and-status-codes.mdx:56-84][dd-opcodes]). The DAVE payload layouts
come from the whitepaper, which Discord's docs name as the exact format
([voice-connections.mdx:238][dd-dave], [protocol.md:858-862][wp-opcodes]).

| Op | Name | Sent by | Frame | Payload | Recorder | Docs | disgo |
|---|---|---|---|---|---|---|---|
| 0 | Identify | client | JSON | `server_id`, `user_id`, `session_id`, `token`, `max_dave_protocol_version` | sends | [vc:82-100][dd-identify] | [msg:190-196][dgm-identify] |
| 1 | Select Protocol | client | JSON | `protocol`, `data.address`, `data.port`, `data.mode` | sends | [vc:191-225][dd-select] | [msg:244-255][dgm-select] |
| 2 | Ready | server | JSON | `ssrc`, `ip`, `port`, `modes` | receives | [vc:102-122][dd-ready] | [msg:200-205][dgm-ready] |
| 3 | Heartbeat | client | JSON | `t`, `seq_ack` | sends | [vc:140-155][dd-heartbeat] | [msg:223-226][dgm-heartbeat] |
| 4 | Session Description | server | JSON | `mode`, `secret_key`, `dave_protocol_version` | receives | [vc:438-452][dd-session] | [msg:230-234][dgm-session] |
| 5 | Speaking | both | JSON | `speaking` flags, `delay`, `ssrc`; disgo also reads `user_id` | receives only | [vc:468-500][dd-speaking] | [msg:289-305][dgm-speaking] |
| 6 | Heartbeat ACK | server | JSON | `t` | receives | [vc:167-179][dd-ack] | [msg:316-318][dgm-ack] |
| 7, 9 | Resume, Resumed | | | | dropped | [vc:506-551][dd-resume] | [msg:307-312][dgm-resume] |
| 8 | Hello | server | JSON | `heartbeat_interval` | receives | [vc:124-138][dd-heartbeat] | [msg:209-211][dgm-hello] |
| 11 | Clients Connect | server | JSON | `user_ids` | receives | [wp:892-905][wp-clients] | [msg:322-326][dgm-clients] |
| 13 | Client Disconnect | server | JSON | `user_id` | receives | [wp:907-918][wp-clients] | [msg:336-340][dgm-clients] |
| 21 | DAVE Prepare Transition | server | JSON | `protocol_version`, `transition_id` | receives | [wp:920-934][wp-21] | [msg:342-347][dgm-dave-json] |
| 22 | DAVE Execute Transition | server | JSON | `transition_id` | receives | [wp:936-949][wp-22-24] | [msg:349-353][dgm-dave-json] |
| 23 | DAVE Transition Ready | client | JSON | `transition_id` | sends | [wp:951-963][wp-22-24] | [msg:355-359][dgm-dave-json] |
| 24 | DAVE Prepare Epoch | server | JSON | `protocol_version`, `epoch` | receives | [wp:965-978][wp-22-24] | [msg:361-366][dgm-dave-json] |
| 25 | DAVE MLS External Sender | server | binary | sequence, op, external sender credential and key | receives | [wp:980-1003][wp-25] | [msg:368-370][dgm-dave-bin] |
| 26 | DAVE MLS Key Package | client | binary | op, MLS key package message | sends | [wp:1005-1018][wp-26] | [msg:372-374][dgm-dave-bin] |
| 27 | DAVE MLS Proposals | server | binary | sequence, op, operation type, proposals or proposal refs | receives | [wp:1020-1048][wp-27] | [msg:376-378][dgm-dave-bin] |
| 28 | DAVE MLS Commit Welcome | client | binary | op, MLS commit, Welcome when the commit adds members | sends | [wp:1050-1068][wp-28] | [msg:380-382][dgm-dave-bin] |
| 29 | DAVE MLS Announce Commit Transition | server | binary | sequence, op, `transition_id` (uint16), MLS commit | receives | [wp:1070-1083][wp-29-30] | [msg:384-389][dgm-dave-bin] |
| 30 | DAVE MLS Welcome | server | binary | sequence, op, `transition_id` (uint16), Welcome | receives | [wp:1085-1098][wp-29-30] | [msg:391-396][dgm-dave-bin] |
| 31 | DAVE MLS Invalid Commit Welcome | client | JSON | `transition_id` | sends | [wp:1100-1112][wp-31] | [msg:398-402][dgm-dave-json] |

In the table, `vc` is `voice-connections.mdx`, `wp` is the whitepaper's
`protocol.md`, and `msg` is disgo's `gateway_messages.go`.

The recorder never encrypts, but it still has to join the MLS group to
decrypt anyone. So it sends key packages, commits, transition readiness and
invalid commit flags like any member
([voice-connections.mdx:304-340][dd-join],
[protocol.md:164-206][wp-proposal]). Op 11 and op 13 feed the DAVE session,
which refuses an add proposal for a user it wasn't told about
([protocol.md:218-228][wp-member-add]). The recorder needs op 5 only to map
an SSRC to a member: disgo's `Conn` keeps that map from the server's Speaking
messages, and the recorder's receiver reads it
([conn.go:286-289][dgc-ssrcs], [receiver.go:286-289][repo-recv-ssrc]). The
docs show only the client's Speaking payload, so the server's `user_id` field
is known from disgo's struct and the recorder working, not from the docs.

The docs list no ops 10, 12 or 14 to 20. disgo names 14 and keeps a struct
for a one-client connect with codec fields, so it has met some of them
([gateway_opcodes.go:3-38][dgo-opcodes],
[gateway_messages.go:328-332][dgm-clients]). A client should pass over any
opcode it doesn't know, as disgo's decoders do
([gateway_messages.go:113-116][dgm-unknown],
[gateway_messages.go:170-173][dgm-binary]).

### DAVE binary framing

A server binary frame is a big-endian uint16 sequence number, a one-byte
opcode, then the payload. A client binary frame has no sequence number, only
the opcode byte and the payload
([voice-connections.mdx:242-252][dd-binary], [protocol.md:1005-1018][wp-26],
[protocol.md:1050-1068][wp-28]). Variable-length fields use MLS's length
headers ([protocol.md:858-862][wp-opcodes]). Ops 25, 27, 29 and 30 arrive as
binary, and the client sends 26 and 28 as binary. Ops 21 to 24 and 31 are
JSON.

disgo decodes server frames in `UnmarshalBinary`
([gateway_messages.go:128-183][dgm-binary]). It encodes client frames inline
in the unexported `sendInternal` ([gateway.go:270-297][dg-sendinternal]).
`Opcode.IsBinary` also lists 21, which the docs mark as JSON
([gateway_opcodes.go:40-52][dgo-isbinary]). The client never sends 21, so
that slip changes nothing.

### Close codes

Discord's voice table lists 16 codes
([opcodes-and-status-codes.mdx:86-106][dd-closecodes]). disgo's table adds
1000 and 4000, and gives each code two flags of its own: `Reconnect` means
resume with the same session, and `NewConnection` means start a fresh voice
connection ([gateway_opcodes.go:54-245][dgo-closecodes],
[conn.go:309-344][dgc-close-handler]).

| Code | Name in Discord's docs | What the docs add | disgo `Reconnect` | disgo `NewConnection` |
|---|---|---|---|---|
| 1000 | not listed | | true, named "Heartbeat timeout" | true |
| 4000 | not listed | | true | true |
| 4001 | Unknown opcode | | true | true |
| 4002 | Failed to decode payload | | true | true |
| 4003 | Not authenticated | | true | true |
| 4004 | Authentication failed | the token was wrong | false | false |
| 4005 | Already authenticated | | true | true |
| 4006 | Session no longer valid | | false | true |
| 4009 | Session timeout | | false | true |
| 4011 | Server not found | | false | true |
| 4012 | Unknown protocol | | true | true |
| 4014 | Disconnected | kicked, or the main gateway session dropped; don't reconnect | false | false |
| 4015 | Voice server crashed | try resuming | true | true |
| 4016 | Unknown encryption mode | | false | false |
| 4017 | E2EE/DAVE protocol required | | false | false |
| 4020 | Bad request | | true | true |
| 4021 | Disconnected: Rate Limited | don't reconnect | false | false |
| 4022 | Disconnected: Call Terminated | channel deleted or voice server changed; don't reconnect | false | false |

A resume that fails closes with one of these codes, and the docs then send the
client back through a fresh connect
([voice-connections.mdx:506-551][dd-resume]). For a client that never
resumes, 4015's advice becomes a fresh connection. The recorder already reads
4022 with a surviving channel as a call that moved, and 4004, 4006 or 4009
before Ready as a refused token ([gateway.go:1050-1092][repo-watchclose],
[gateway.go:1127-1144][repo-refuses]).

### What the DAVE session needs from the client

godave v0.3.0 defines what any DAVE session expects
([godave.go:7-20][gv-callbacks], [godave.go:34-103][gv-session]). The client
implements four callbacks, the way disgo's `Conn` does
([conn.go:115-129][dgc-callbacks]):

| Callback | Op | Frame |
|---|---|---|
| `SendMLSKeyPackage` | 26 | binary |
| `SendMLSCommitWelcome` | 28 | binary |
| `SendReadyForTransition` | 23 | JSON |
| `SendInvalidCommitWelcome` | 31 | JSON |

The client calls the session as disgo's `listen` does
([gateway.go:192][dg-open-conn], [gateway.go:434-440][dg-identify],
[gateway.go:589-628][dg-listen-dave]):

| When | Session call |
|---|---|
| before the dial | `SetChannelID` |
| building Identify | `MaxSupportedProtocolVersion` |
| op 4 | `OnSelectProtocolAck(dave_protocol_version)` |
| op 11 | `AddUser` for each of `user_ids` |
| op 13 | `RemoveUser(user_id)` |
| op 21 | `OnDavePrepareTransition(transition_id, protocol_version)` |
| op 22 | `OnDaveExecuteTransition(transition_id)` |
| op 24 | `OnDavePrepareEpoch(epoch, protocol_version)` |
| op 25 | `OnDaveMLSExternalSenderPackage`, with the bytes after the sequence and op |
| op 27 | `OnDaveMLSProposals`, with the bytes after the sequence and op |
| op 29 | `OnDaveMLSPrepareCommitTransition(transition_id, commit)` |
| op 30 | `OnDaveMLSWelcome(transition_id, welcome)` |

godave names op 22's parameter `protocolVersion`
([godave.go:86-87][gv-exec]). disgo passes the transition ID, and dave-go
takes a transition ID ([session.go:619][dv-exec]). The UDP side already
calls the rest: `AssignSsrcToCodec` after IP discovery, `Decrypt` and
`MaxDecryptedFrameSize` per frame, `Ready` to hold frames, and `Close` on
discard ([udp.go:91-106][repo-udp-ssrc],
[receiver.go:323-327][repo-recv-decrypt], [receiver.go:215-225][repo-recv-hold],
[conn.go:370-378][dgc-close]).

dave-go adds one contract. It retries a callback whose error text contains
`shard is not ready`, up to 6 sends over about 12.5 s, and holds its session
lock while it sleeps ([retry.go:8-29][dv-retry-consts],
[retry.go:31-90][dv-retry-lock]). Any other error ends the send at once.
disgo's `Send` returns that text before Ready ([errors.go:12-14][dg-errors]).
A client of our own that wants the retry has to return an error with the same
text. dave-go sends the key package from inside `OnSelectProtocolAck`
([session.go:547-549][dv-ack], [mls.go:160-165][dv-kp]), so a retry stalls
whichever goroutine called it. In disgo that is the socket reader.

## Size of a client of our own

### What goes

I counted lines in the pinned `gateway.go` with `awk` and sorted each range
by reading it. These ranges exist only for resume or for disgo's own
reconnect, 165 lines in all, 145 of them code:

| Lines | What | Count |
|---|---|---:|
| [27-30][dg-consts] | reconnect delay and try limit | 4 |
| [43][dg-status] | `StatusResuming` | 1 |
| [157-160][dg-open-reset] | keep or clear resume data on open | 4 |
| [243-247][dg-close-conn] | clear resume data on a clean close | 5 |
| [316-360][dg-doreconnect] | `doReconnect` | 45 |
| [362-367][dg-delay] | `nextReconnectDelay` | 6 |
| [369-375][dg-reconnect] | `reconnect` | 7 |
| [393-399][dg-hb-zombie] | missed answer, then reconnect | 7 |
| [417-423][dg-sendhb] | failed heartbeat, then reconnect | 7 |
| [454-473][dg-resume] | `resume` | 20 |
| [491-536][dg-listen-reconnect] | `listen`'s reconnect decision | 46 |
| [555-559][dg-listen-hello] | choose Identify or Resume | 5 |
| [573-577][dg-listen-ready] | Resumed | 5 |
| [583-585][dg-listen-ack] | nonce mismatch, then reconnect | 3 |

Four of these come back smaller, as a report to the state machine: the missed
answer, the failed heartbeat, the read error and the nonce mismatch. That is
about 20 lines.

### What stays

The other 491 lines, 368 of them code: the dial and the start of `listen`,
the close, the send path, the heartbeat loop, Identify, `listen`'s dispatch
including the DAVE calls, `parseMessage`, and the types. About 90 code lines
are the package header, types, interface and struct
([gateway.go:1-138][dg-header]), and some of those would shrink, since the
state machine would be the only caller.

### What we add

These are my estimates from the shape of each fix:

- A context made when the gateway is built and cancelled by close, which the
  heartbeat and `listen` derive from. A close before the heartbeat starts
  then still stops it. 10 to 20 lines.
- Server events delivered on one buffered channel the state machine reads,
  or at least `make(chan error, 1)` for Ready, so `listen` never blocks on a
  reader that left. The goroutine issue's triage tested the one-line version:
  0 of 100 cancelled Joins leaked. 1 to 30 lines.
- One lock order, as the deadlock PR does with 20 lines added and 6 removed,
  or one goroutine that owns all writes. 15 to 50 lines.
- Read deadlines and Hello, Ready and Session Description timers that report
  to the state machine. Some of this may live in the state machine instead.
  30 to 60 lines.
- Heartbeats from Hello rather than from Ready, the sequence number read from
  `seq`, and exactly one dial per gateway. 5 to 10 lines.
- The 20 lines of reports above.

That is about 80 to 190 lines added. Taking 491, adding 80 to 190 and
trimming 30 to 60 lines of types gives about 500 to 650 lines, with disgo's
message and opcode types imported. Copying the types instead adds about 380
lines of messages, after dropping Resume, Resumed and the decode cases for
messages only a client sends, and about 200 to 245 of opcodes and close
codes, for about 1,100 to 1,300 in all. Neither figure counts tests. disgo's
gateway has 38 lines of them ([gateway_test.go][dg-gw-test]). Neither counts
the gateway-facing parts of disgo's `Conn`, about 75 lines
([conn.go:115-129][dgc-callbacks], [conn.go:250-307][dgc-handle]), which the
state machine would take over if disgo's `Conn` went too.

### Reusing disgo's pieces as plain types

Importing `github.com/disgoorg/disgo/voice` for its types starts nothing.
The package has no `init`, its package variables are plain values, and
goroutines start only once code calls `NewGateway` and opens it, or
`NewConn`, or `NewManager`. Its imports are `disgo/discord`, `disgo/gateway`,
godave, `disgoorg/json`, snowflake and gorilla/websocket, all in `go.mod`
already. Reuse keeps ADR 0014's master pin for those types
([0014:5-16][repo-adr14-pin]).

- The opcode constants are reusable as they are
  ([gateway_opcodes.go:3-52][dgo-opcodes]). `IsBinary` lists 21 by mistake,
  which doesn't matter for sending.
- `GatewayMessage` with `UnmarshalJSON` and `UnmarshalBinary` decodes without
  touching any gateway ([gateway_messages.go:12-183][dgm-message]). It reads
  JSON sequence numbers from `s`, so a client that wants Discord's documented
  `seq` reads it itself.
- The message structs are reusable
  ([gateway_messages.go:190-410][dgm-identify]). The Resume struct carries the
  wrong tag, and a client that never resumes doesn't use it.
- The encryption mode constants and `ChooseEncryptionMode` are pure
  ([gateway_messages.go:257-287][dgm-modes]). The recorder already uses
  disgo's encrypter ([udp.go:168-174][repo-udp-key]).
- `GatewayCloseEventCode`, its table and `GatewayCloseEventCodeByCode` are
  reusable ([gateway_opcodes.go:54-245][dgo-closecodes]). The two flags
  encode disgo's policy, so a state machine would read the code and decide
  for itself. The table is an exported map anything in the process can
  change.
- The client-side binary encoding is not reusable. It sits inside the
  unexported `sendInternal`, and is about 15 lines to write
  ([gateway.go:270-287][dg-sendinternal]).

## Open questions

- Does Discord send a JSON message's sequence number as `seq`, as its docs
  show, or `s`, as disgo reads it? A capture on the test guild settles it. A
  client that never resumes needs it only for `seq_ack`.
- What does a voice server do with a heartbeat whose `seq_ack` is stale or 0?
  The docs don't say.
- How often does the deadlock happen in production? Nobody has measured it.
- How much memory does a leaked heartbeat keep reachable in the
  replacement's wiring? The heartbeat issue's triage counted leaks, not bytes.

[issue-768]: https://github.com/7Cav/cavbot2/issues/768
[issue-769]: https://github.com/7Cav/cavbot2/issues/769
[issue-772]: https://github.com/7Cav/cavbot2/issues/772
[issue-810]: https://github.com/7Cav/cavbot2/issues/810
[issue-813]: https://github.com/7Cav/cavbot2/issues/813
[issue-817]: https://github.com/7Cav/cavbot2/issues/817
[dg-pr-559]: https://github.com/disgoorg/disgo/pull/559
[dg-pr-587]: https://github.com/disgoorg/disgo/pull/587
[dg-pr-588]: https://github.com/disgoorg/disgo/pull/588
[dg-issue-626]: https://github.com/disgoorg/disgo/issues/626
[dg-issue-494]: https://github.com/disgoorg/disgo/issues/494
[dg-pr-529]: https://github.com/disgoorg/disgo/pull/529
[dg-issue-534]: https://github.com/disgoorg/disgo/issues/534
[dg-compare]: https://github.com/disgoorg/disgo/compare/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc...2d889db8a812683270d8ee262f00861ce30ebb6f
[dg-header]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L1-L138
[dg-consts]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L24-L30
[dg-status]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L35-L47
[dg-open-api]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L144-L146
[dg-open-lock]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L151-L164
[dg-open-reset]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L157-L160
[dg-open-dial]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L166-L168
[dg-open-conn]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L192-L202
[dg-ready-chan]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L200-L220
[dg-open-select]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L209-L220
[dg-close]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L223-L225
[dg-close-hb]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L227-L231
[dg-close-conn]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L233-L251
[dg-close-status]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L249-L251
[dg-send]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L260-L268
[dg-sendinternal]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L270-L297
[dg-send-conn]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L299-L310
[dg-doreconnect]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L316-L360
[dg-delay]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L362-L367
[dg-reconnect]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L369-L375
[dg-heartbeat]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L377-L404
[dg-hb-store]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L380-L383
[dg-hb-zombie]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L392-L401
[dg-sendhb]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L406-L426
[dg-identify]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L428-L452
[dg-resume]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L454-L473
[dg-listen-notready]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L482-L489
[dg-listen-reconnect]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L491-L536
[dg-listen-seq]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L545-L547
[dg-listen-hello]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L550-L563
[dg-listen-ready]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L565-L577
[dg-listen-ack]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L579-L587
[dg-listen-dave]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L589-L628
[dg-listen-event]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L631
[dg-config]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_config.go#L16-L20
[dg-gw-test]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_test.go
[dg-errors]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/discord/errors.go#L12-L14
[dgm-message]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L12-L18
[dgm-json-seq]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L20-L26
[dgm-unknown]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L113-L116
[dgm-binary]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L128-L183
[dgm-identify]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L190-L196
[dgm-ready]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L200-L205
[dgm-hello]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L209-L211
[dgm-heartbeat]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L223-L226
[dgm-session]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L230-L234
[dgm-select]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L244-L255
[dgm-modes]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L257-L287
[dgm-speaking]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L289-L305
[dgm-resume]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L307-L312
[dgm-ack]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L316-L318
[dgm-clients]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L322-L340
[dgm-dave-json]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L342-L366
[dgm-dave-bin]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_messages.go#L368-L402
[dgo-opcodes]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_opcodes.go#L3-L38
[dgo-isbinary]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_opcodes.go#L40-L52
[dgo-closecodes]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway_opcodes.go#L54-L245
[dgc-new]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L81-L84
[dgc-callbacks]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L115-L129
[dgc-speaking]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L155-L160
[dgc-handle]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L250-L307
[dgc-ready]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L252-L276
[dgc-ssrcs]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L286-L289
[dgc-close-handler]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L309-L344
[dgc-close]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/conn.go#L370-L378
[gv-callbacks]: https://github.com/disgoorg/godave/blob/511eb1ead91982515227cf7a848610ea72312168/godave.go#L7-L20
[gv-session]: https://github.com/disgoorg/godave/blob/511eb1ead91982515227cf7a848610ea72312168/godave.go#L34-L103
[gv-exec]: https://github.com/disgoorg/godave/blob/511eb1ead91982515227cf7a848610ea72312168/godave.go#L86-L87
[dv-retry-consts]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/retry.go#L8-L29
[dv-retry-lock]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/retry.go#L31-L90
[dv-decrypt]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L432-L438
[dv-ack]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L547-L549
[dv-exec]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L619
[dv-kp]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L160-L165
[gw-nextreader]: https://github.com/gorilla/websocket/blob/v1.5.3/conn.go#L998-L1003
[go-testflags]: https://pkg.go.dev/cmd/go#hdr-Testing_flags
[dd-versions]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L20-L32
[dd-identify]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L82-L100
[dd-ready]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L102-L122
[dd-heartbeat]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L124-L155
[dd-ack]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L140-L179
[dd-select]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L191-L225
[dd-dave]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L227-L240
[dd-binary]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L242-L252
[dd-dave-version]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L254-L262
[dd-join]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L304-L340
[dd-modes]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L410-L436
[dd-session]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L438-L452
[dd-speaking]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L468-L500
[dd-resume]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L506-L551
[dd-buffered]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L553-L580
[dd-ip]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/voice-connections.mdx#L582-L592
[dd-opcodes]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx#L56-L84
[dd-closecodes]: https://github.com/discord/discord-api-docs/blob/f7416a2cf165455f72a31f5ea4e671a4f047f2f7/developers/topics/opcodes-and-status-codes.mdx#L86-L106
[wp-proposal]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L164-L206
[wp-member-add]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L218-L228
[wp-opcodes]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L858-L862
[wp-clients]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L892-L918
[wp-21]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L920-L934
[wp-22-24]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L936-L978
[wp-25]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L980-L1003
[wp-26]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1005-L1018
[wp-27]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1020-L1048
[wp-28]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1050-L1068
[wp-29-30]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1070-L1098
[wp-31]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1100-L1112
[repo-resume-note]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L120-L132
[repo-owndials]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L120-L170
[repo-refused-err]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L137-L140
[repo-connopts]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L196-L214
[repo-close]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L315-L324
[repo-selfmute]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L326-L328
[repo-leave-again]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L404-L434
[repo-renewable]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L592-L598
[repo-silent]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L777-L794
[repo-dialuntil]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L900-L938
[repo-replace]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L947-L957
[repo-startdial]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L968-L982
[repo-reopen]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L1008-L1022
[repo-gw-close]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L1024-L1036
[repo-gw-send]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L1040-L1042
[repo-watchclose]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L1050-L1092
[repo-refuses]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/gateway.go#L1127-L1144
[repo-udp-noaudio]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/udp.go#L40-L41
[repo-udp-ssrc]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/udp.go#L91-L106
[repo-udp-key]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/udp.go#L168-L174
[repo-udp-write]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/udp.go#L269-L270
[repo-recv-hold]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/receiver.go#L215-L225
[repo-recv-ssrc]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/receiver.go#L286-L289
[repo-recv-decrypt]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/voice/receiver.go#L323-L327
[repo-adr14-pin]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/docs/adr/0014-voice-on-disgo-and-dave-go.md?plain=1#L5-L16
[repo-adr14-recover]: https://github.com/7Cav/cavbot2/blob/82c3df153f7e710c730c9d86abd150e6549a0432/docs/adr/0014-voice-on-disgo-and-dave-go.md?plain=1#L52-L53
