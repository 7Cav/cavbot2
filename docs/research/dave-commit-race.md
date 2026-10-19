# Does the DAVE library cause the silence after a commit race?

Date: 2026-10-19. This answers [#829][i829]. I read every source at the commit or version its link names. I ran nothing against Discord. Later smoke runs overwrote the bot logs behind the stalls, so the figures come from the tickets.

## Question

When the recorder and a member both commit at a new voice server and Discord never answers the recorder's commit, is that silence Discord's, or does dave-go send something Discord ignores? Would dave-go main or golibdave send something different?

## Bottom line

The record points at Discord's voice gateway. It doesn't prove it.

- The protocol gives the gateway no way to turn a commit down except silence. Per [Commit Ordering][p-order], the gateway broadcasts one winning commit per epoch and drops every other commit without a reply. A pending member hears back in only two ways. It gets op 29 when its own commit won, or op 30 when the winning commit added it, as [Initial Group Creation][p-initial] and the [op 29 and op 30 formats][p-op29-30] describe.
- Whichever commit won, the gateway owed the recorder a message. [Voice Gateway Commit Validity][p-validity] makes a commit valid only if it references every unrevoked proposal the gateway broadcast in the epoch. The gateway sent the speaker an add proposal for the recorder. So the speaker's commit, had it won, would have added the recorder and brought op 30. The recorder's commit, had it won, would have come back as op 29. Neither arrived.
- dave-go built this commit with the same code as every other. Discord broadcast the recorder's commit in 4 of the 7 moves on record. The brief on the stall records one op 27 and the log had no WARN, so the commit covered every proposal the recorder had received.
- Neither dave-go main nor golibdave would have sent anything different in the recorded stall. Main's op 28 code is unchanged since v0.5.1. golibdave runs Discord's libdave, which builds a commit of the same shape. golibdave has no timer at all for a commit nobody answers, and neither has Discord's own reference session manager.

So on what the record shows, no library choice spares members this silence. The bot's own stall recovery from [#734][pr734] is the only thing that shortens it.

One way dave-go could still be at fault stays open. Suppose the gateway judged the recorder's commit invalid and then also refused the speaker's later commit for the same epoch. Then the recorder's commit caused the silence. Nothing on record shows the commit was invalid. The protocol doesn't say what the gateway does after an invalid first commit. The last section lists the live evidence that would close this.

Separately, dave-go departs from the protocol in one place that strands a recorder in the same silence. When a proposal batch arrives while its own commit is unconfirmed, dave-go holds the batch. The protocol and libdave commit again. #732's brief names no second op 27, so on the record this didn't cause that stall. The section "Where dave-go departs from the protocol" has the details.

## What the record shows

The Agent Brief on [#732][i732] traced one stall. At the bad move the recorder reached the new voice server, created its MLS group, got the speaker's add proposal in op 27 and sent its commit in op 28. No op 29 and no op 30 came in the 11.3 s before the next move. The speaker's op 12 and op 5 kept arriving, so the connection was up and disgo was reading it. The issue body says the log had no WARN or ERROR line.

The 7 moves that reached a new server split like this:

| First at the new server | Moves | Recorder got op 29 | Recorder got op 30 | No answer |
|---|---|---|---|---|
| Speaker | 3 | 3 | 0 | 0 |
| Recorder | 4 | 1 | 2 | 1 |

A second stall came on a first join, 3.3 s after the recorder committed its proposals, in the smoke pass on [PR #734][pr734]. [#815][i815] ruled out dave-go#51's retry, which fires only when disgo's send fails with `shard is not ready`. That can't happen in a join or a move.

The silence happened on the wire. It isn't a gap in the log. At DEBUG, [disgo logs every inbound voice gateway message][disgo-parse] before parsing it, and #732's "How to observe" says smoke runs log at DEBUG.

The op 29s in the good moves carried the recorder's own commit. The protocol expects a pending member to get back its own commit or a welcome, and tells it to refuse any other commit. dave-go keeps its local epoch only for [an op 29 that matches the commit it sent][dg-op29]. Any other commit takes [the rollback path][dg-reconcile], which processes the winner against the recorder's one-member group. The winner was signed in another client's group and references a proposal the recorder never got, so processing fails. dave-go then logs ERROR, sends op 31 and starts over with a new key package. #732's brief says every DAVE setup that finished in that smoke run did so within 0.7 s of the session description, too soon for a reset and a second add. This is my reading of the code, the protocol and those timings. The logs that would show it directly are gone.

## What the protocol says

The protocol is Discord's DAVE whitepaper, [`protocol.md` at 1b2b706][p], version 1.1.4. [daveprotocol.com][daveprotocol] carries the same text.

[Commit Ordering][p-order] says who picks the winner:

> Multiple committing members may send commit messages at the same time. It is the responsibility of the voice gateway to pick a winning commit and broadcast that to all members.
>
> The voice gateway broadcasts the first commit it receives for a given epoch. The voice gateway will not broadcast a commit message for a past or future epoch. The voice gateway will not broadcast a commit that it determines to be invalid.

[Voice Gateway Commit Validity][p-validity] says what the gateway accepts:

> A commit is only valid when:
>
> - It includes one or more proposal references
> - It references only proposals of the types add and remove
> - It references all unrevoked proposals broadcast in the epoch by the external sender
> - It references and includes no other proposals
> - It does not modify the credential or signature key of the leaf node that produced the commit

[Invalid Groups][p-invalid] adds a case for the first commit:

> The voice gateway may detect an invalid group during its validation of the initial commit and refuse to broadcast that commit.

[Initial Group Creation][p-initial] says why both clients commit at a new server:

> When the voice gateway does not have an established group, receiving a dave_mls_key_package opcode (26) will broadcast the add proposal to all other pending group members.
>
> A client that joins when there is not an established group receives any already sent in-flight add proposals for existing pending members.
>
> Members create a local group containing themselves as the sole member once they receive the necessary context (external sender credential and public key and DAVE protocol version).

And how the race ends:

> The group is established when the voice gateway broadcasts the first commit and associated welcome message(s) via the dave_mls_announce_commit_transition opcode (29) and dave_mls_welcome opcode (30). [...] When the client receives either its own commit or a welcome, it updates its local group state and discards all pending state.

[Proposal Handling][p-176] says when a client must commit:

> If there are one or more cached proposals after processing a dave_mls_proposals opcode (27), committing members must send a commit with an optional welcome message using the dave_mls_commit_welcome opcode (28).

[Client Commit Validity][p-client] says what a client does with a commit before it has a group:

> The client will refuse to process any commit when:
>
> - It does not have an established group (i.e. a group with epoch > 0) unless that commit exactly matches the commit they produced for their initial local group

The protocol names no message for a commit that lost. In this phase the gateway sends [op 29 and op 30][p-op29-30], and [op 31][p-op31] runs the other way, from client to gateway. The gateway answers a pending member whose commit lost with op 30, if the winning commit added it, per [Welcome Handling][p-welcome].

So a committer can get nothing back, by design, for every commit the gateway drops. That covers a commit that came after the winner, one for a past or future epoch, one the gateway judges invalid, and an initial commit for an invalid group. The committer learns its commit lost only from whatever comes next. The protocol describes no case where a pending member gets nothing at all once a winner is broadcast, since the winner must reference the add proposal for every pending member. It also doesn't say what the gateway does when the first commit it receives is invalid. It might take the next valid one, or it might hold the epoch. A dave-go fault would need that gap.

### The race happens in both orders

#732's triage calls only the recorder-first moves a race. By the passages above, the speaker-first moves were races too. A member alone at a new server holds an unestablished group. A group with one member is never established, and [Sole member reset][p-sole] puts an established group that drops to one member back in that state. When the recorder joins, the gateway sends the speaker an add proposal for the recorder. It also hands the recorder the speaker's in-flight add proposal. Then both commit.

[libdave builds a commit from every valid proposal batch][ld-commit], and per its [README][ld-readme] Discord's native clients use libdave. The recorder's log can't show whether the speaker's commit went out. It shows the recorder won all three. A first join with a member already in the channel is the same race, and the #734 stall came on one. So the race happens at every setup where one member was alone, whichever client arrives first.

The 6 races that had a winner fit the in-flight rule. The client that reached the server second won 5 of them, 3 of 3 when the recorder came second and 2 of 3 when the speaker did. The second client gets the first one's add proposal as it joins. The first waits for the gateway to receive the second's key package and broadcast an add for it. With six outcomes, treat this as a hint.

## What dave-go sends

The repo pins v0.5.1 in [go.mod][cb-gomod]. #815 built main at 89a4bbf. Main has moved since. PR 54, which #829 calls still open, merged on 2026-10-19 as 07d9ec3. Between 89a4bbf and 07d9ec3 `session/mls.go` is byte-identical, and the only change to `session/session.go` is PR 54's. Line links below point at v0.5.1 unless they say main.

### Before the commit

- Op 4 runs [`OnSelectProtocolAck`][dg-ack], which drops all MLS state and sends a fresh key package in op 26.
- Op 25 [creates the local one-member group][dg-group]. Its group ID is the channel ID, and its leaf comes from the key package already sent. A code comment explains the choice. "Using a different KP here would make the commit signature unverifiable and the gateway would silently reject it." [disgo sets the channel ID][disgo-chan] before its read loop starts, so the group ID is right.
- On op 27, dave-go [accepts only public add or remove proposals from an external sender][dg-allowed]. [mls-go checks each signature][mlsgo-ext] against the ExternalSenders extension in the group context, which holds only the gateway's sender from op 25. dave-go skips an add for a user it hasn't seen in op 11, and [logs every rejection at WARN][dg-reject].

### Building and sending op 28

[`commitProposalsLocked`][dg-commit] snapshots the group state, commits every pending proposal with mls-go, keeps the commit bytes and the snapshot, and sets the pending epoch. It appends the bare Welcome to the commit, as the [op 28 format][p-op28] asks, and sends the result. v0.5.1 retries a send only on `shard is not ready`, which #815 showed can't happen here. It logs the commit in hex at DEBUG.

### Op 29, op 30 and op 31

- An op 29 that [matches its own commit][dg-op29] keeps the local epoch and sends op 23.
- An op 29 with any other commit [rolls back to the snapshot and processes the winner][dg-reconcile]. If that fails, dave-go sends op 31 and starts over with a new key package.
- An op 30 that arrives after its own commit [rolls back and joins the welcome][dg-op30], [checks the external sender in the joined group][dg-join] and sends op 23. A failed join sends op 31 and starts over.
- dave-go sends op 31 only after a commit or welcome it can't process, or from a watchdog.

### The watchdog for a commit nobody confirms

After every op 28 a goroutine waits for the recovery timeout. That is [15 s][dg-timeouts] unless the integrator passes [`WithRecoveryTimeout`][dg-rto] to [`New`][dg-new]. If the commit's epoch still hasn't activated, [the watchdog][dg-watchdog] sends op 31 with the transition ID it saw at commit time and starts over with a new key package. [Recovery][dg-recovery] tries this up to 3 times. cavbot [passes no options][cb-gateway], so the wait is 15 s. In #732's stall the next move came 11.3 s after the commit, before the watchdog could fire. On `develop`, [#734's stall check][cb-receiver] starts DAVE over after 3 s, so the watchdog never fires there either.

### What main changes

None of these changes what the recorder sends before the silence, or how long it waits after.

| PR | Change | Bearing on the race |
|---|---|---|
| [#47][dgpr47] | Creates the group only once the channel ID is set | None. Its body says "disgo always calls SetChannelID first, so it doesn't hit this" |
| [#49][dgpr49] | Builds the group only from a key package the gateway got | Only matters when the op 26 send failed, which #815 showed can't happen in a join or a move |
| [#36][dgpr36] | Executes only the transition that was prepared | Acts after an answer, so not in a silence |
| [#50][dgpr50] | On op 29, rolls back only when the session sent a commit of its own | The recorder did commit, so [main rolls back][dgm-reconcile] as v0.5.1 does |
| [#52][dgpr52] | Sends once without the session lock, retries only in watchdogs, and counts a recovery attempt when a watchdog fires | [Same op 28][dgm-commit] and the same 15 s wait |
| [#53][dgpr53] | Resets the recovery attempts on op 4 | Only matters after 3 failed recoveries |
| [#54][dgpr54] | Applies #50's rule to op 30 | Same as #50. [The recorder's own commit still rolls back][dgmain-welcome] |

## What libdave and golibdave do

golibdave here is godave master at c382765. It differs from the `golibdave/v0.3.0` tag only by #12, a `Ready` fix, and it [links libdave v1.1.0][gl-release]. libdave's latest release is v1.2.1.

- On op 27, [libdave refuses the whole batch][ld-validate] if any add is for a user it doesn't recognize, and sends no commit. Otherwise it [adds the proposals to the proposal state it keeps for the epoch and commits every cached proposal][ld-commit], with the tree inline in the welcome and no forced path. It does this on every op 27, so a later batch brings a new commit that covers everything so far.
- Its op 28 is the commit followed by the Welcome when the commit adds anyone. dave-go sends the same shape, built by mls-go instead of mlspp.
- On op 29, libdave v1.2.1 throws ["Unexpected commit before welcome"][ld-before-welcome] for any commit that arrives before a welcome, unless the commit is the client's own. [v1.1.0][ld110-commit], which golibdave links, has no such check. It hands the commit to mlspp against the local group, where it fails, since it was signed in another client's group. [On a failure golibdave][gl-op29] sends op 31 and starts over. On an ignored commit it does nothing.
- On op 30 [golibdave joins][gl-op30], or on a failure sends op 31 and a new key package.
- golibdave has no timer. [golibdave.go doesn't import `time`][gl-imports], and Discord's [reference `DaveSessionManager.ts`][ld-sample] puts no timeout around a commit.

So in the recorded race golibdave would have done what dave-go did. It would commit once and wait for op 29 or op 30. After a silence it would wait for a new voice connection.

| Situation | dave-go v0.5.1 | dave-go main | golibdave on libdave v1.1.0 |
|---|---|---|---|
| Op 28 in the recorded stall | Commit and Welcome for the one add | Same code | Same shape, other MLS library |
| Another op 27 while its own commit is unconfirmed | Holds it, sends no new commit | Same | Commits again over every cached proposal |
| A batch with an add for an unknown user | Commits the rest | Same | Sends no commit |
| A revoke batch | Sends no commit after it | Same | Rebuilds the proposal state and commits what is left |
| Op 29 with another client's commit, before a welcome | Op 31, starts over | Same | Op 31, starts over |
| Op 30 after its own commit | Rolls back and joins | Same | Joins |
| No answer to its own commit | Op 31 and a new key package after 15 s, up to 3 times | Same, with attempts counted when a watchdog fires | Waits for a new connection |

## Where dave-go departs from the protocol

These came up while reading the commit path. The record ties none of them to #732's stall, but the first ends in the same silence.

1. dave-go holds a proposal batch that arrives while its own commit is unconfirmed, [in v0.5.1][dg-queue] and [in main][dgm-queue]. [Proposal Handling][p-176] says a committing member must commit whenever cached proposals remain after an op 27, and libdave does. Once the gateway broadcasts another proposal, the recorder's in-flight commit no longer references every proposal in the epoch, so the gateway won't broadcast it. dave-go has nothing else in flight. If no other client's commit wins, the recorder waits for the 15 s watchdog, or on `develop` for #734's 3 s stall check. #732's brief lists op 12 and op 5 as what arrived after the commit and names no second op 27. dave-go would log a queued batch only at DEBUG, so with the log gone this rests on the brief. A second op 27 within 3 s of the recorder's commit, in a future stall's log, would point here.
2. dave-go never commits after a revoke batch, [in v0.5.1][dg-revoke] and [in main][dgm-revoke]. Its comment, written in Spanish, cites the same protocol line and reasons that a pure revoke leaves no new proposals. The protocol asks for a commit whenever cached proposals remain, and [libdave rebuilds the proposal state][ld-revoke] and commits what is left. dave-go commits after every append batch, so proposals stay cached only while a commit is in flight. In practice this matters only inside item 1.
3. When dave-go skips an add for an unknown user, [it commits the rest of the batch][dg-batch]. The gateway has to refuse that commit, since it leaves out a proposal the gateway broadcast. libdave sends nothing. Either way the client hears nothing back for that batch. dave-go's case logs a WARN, and #732's stall had none. [Member Add][p-add] says only that members must refuse such a proposal, so this is a difference more than a departure.

## What the sources don't settle

The open question is what the gateway did with the two commits in the stall. Its code is closed, and the protocol doesn't say what it does after an invalid first commit. This evidence would settle it, cheapest first.

- Keep the bot log of the next stall before another smoke run overwrites it. At DEBUG it already holds [every inbound voice gateway message][disgo-parse] and the recorder's commit in hex, the `commitProposalsLocked: commit hex` line [in v0.5.1][dg-commithex] and [in main][dgm-commithex]. Decode the commit and check its group ID, its epoch, and that its proposal references match the op 27 proposals exactly. Compare it with a commit from a move that got op 29. If it passes, the recorder sent a valid commit and the silence is the gateway's. Count every op 27, op 11 and op 13 in the window too. A second op 27 after the commit points at item 1 above.
- Capture the other client's side of the same stall. If the speaker's client sent a commit and got op 29 for it, the gateway owed the recorder an op 30 and never sent it. If the speaker got op 30 for the recorder's commit, the gateway owed the recorder op 29. If the speaker got neither, the gateway dropped both commits. A Discord desktop client's DAVE log may be out of reach. A second bot account in the call running golibdave would race the recorder the same way, and its log would be ours.
- Count stalls on golibdave against dave-go. This is the expensive route. #815 puts the stall rate near 1 in 20 setups and works out that a one-sided Fisher test needs about 100 setups per build.

Three smaller unknowns:

- Whether the speaker's client committed in the speaker-first moves. The protocol and libdave say it did. The recorder's log can't show it.
- Whether the watchdog's op 31 at 15 s would bring an answer when the gateway has no established group with the recorder in it. #732 saw a fresh connection work. Nothing on record shows an op 31 tried.
- I read libdave and Discord's reference sample, not the Discord desktop client.

## Sources

Tickets

- [#829][i829], the question. [#732][i732], the stall and its Agent Brief. [#815][i815], the dave-go#51 finding and the stall rate. [PR #734][pr734], the first-join stall in its smoke pass.
- [ADR 0014][cb-adr14] names golibdave as the fallback if dave-go breaks. [docs/research/dave-e2ee-libraries.md][cb-research] on `research/dave-e2ee-libraries` covers the libraries in general.

Protocol, `protocol.md` at 1b2b706

- [Proposal Handling][p-176], [Pending member][p-pending], [Member Add][p-add], [Welcome Handling][p-welcome], [Initial Group Creation][p-initial], [Invalid Groups][p-invalid], [Sole member reset][p-sole], [Commit Ordering][p-order], [Voice Gateway Commit Validity][p-validity], [Client Commit Validity][p-client], [Recovery from Invalid Commit or Welcome][p-recovery], [op 28][p-op28], [op 29 and op 30][p-op29-30], [op 31][p-op31], [daveprotocol.com][daveprotocol]

dave-go v0.5.1 at 7a73cea

- `session/session.go`: [OnSelectProtocolAck][dg-ack], [WithRecoveryTimeout][dg-rto], [New][dg-new], [queueing proposals][dg-queue], [batch and commit][dg-batch], [revoke][dg-revoke], [reconcile][dg-reconcile], [op 29][dg-op29], [op 30][dg-op30]
- `session/mls.go`: [timeouts][dg-timeouts], [join and check a welcome][dg-join], [proposal rejections][dg-reject], [proposal sender and type][dg-allowed], [group creation][dg-group], [recovery watchdog][dg-recovery], [commit][dg-commit], [commit hex][dg-commithex], [commit watchdog][dg-watchdog]

dave-go main

- 89a4bbf: [session.go queueing][dgm-queue], [revoke][dgm-revoke], [reconcile][dgm-reconcile], [mls.go commit][dgm-commit], [commit hex][dgm-commithex]
- 07d9ec3: [session.go op 30][dgmain-welcome]
- PRs [#36][dgpr36], [#47][dgpr47], [#49][dgpr49], [#50][dgpr50], [#52][dgpr52], [#53][dgpr53], [#54][dgpr54]
- mls-go v1.6.0: [external sender signature check][mlsgo-ext]

libdave and golibdave

- libdave v1.2.1 at 8de72b1: [README][ld-readme], [revoke][ld-revoke], [proposal validation][ld-validate], [commit][ld-commit], [commit before welcome][ld-before-welcome], [reference session manager][ld-sample]
- libdave v1.1.0 at d687416: [ProcessCommit][ld110-commit]
- godave at c382765: [golibdave.go imports][gl-imports], [op 29][gl-op29], [op 30][gl-op30], [libdave release pin][gl-release]

disgo at 995ee20

- [SetChannelID before the read loop][disgo-chan], [every inbound message logged at DEBUG][disgo-parse]

cavbot2 `develop` at 68c86ca

- [go.mod][cb-gomod], [voice/gateway.go][cb-gateway], [voice/receiver.go][cb-receiver]

[i829]: https://github.com/7Cav/cavbot2/issues/829
[i732]: https://github.com/7Cav/cavbot2/issues/732
[i815]: https://github.com/7Cav/cavbot2/issues/815
[pr734]: https://github.com/7Cav/cavbot2/pull/734
[cb-adr14]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/docs/adr/0014-voice-on-disgo-and-dave-go.md
[cb-research]: https://github.com/7Cav/cavbot2/blob/05782693b0993d94e6ecc6aba79fd57f8f4ee323/docs/research/dave-e2ee-libraries.md
[cb-gomod]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/go.mod#L18
[cb-gateway]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/gateway.go#L77-L79
[cb-receiver]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/voice/receiver.go#L60-L63

[p]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md
[daveprotocol]: https://daveprotocol.com
[p-176]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L164-L180
[p-pending]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L192-L196
[p-add]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L218-L228
[p-welcome]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L230-L236
[p-initial]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L257-L269
[p-invalid]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L271-L277
[p-sole]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L279-L287
[p-order]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L289-L293
[p-validity]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L295-L305
[p-client]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L307-L317
[p-recovery]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L319-L329
[p-op28]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1050-L1068
[p-op29-30]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1070-L1098
[p-op31]: https://github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1100-L1112

[dg-ack]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L547-L583
[dg-rto]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L170-L180
[dg-new]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L198-L203
[dg-queue]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L710-L719
[dg-batch]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L726-L773
[dg-revoke]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L743-L751
[dg-reconcile]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L780-L810
[dg-op29]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L828-L849
[dg-op30]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L859-L927
[dg-timeouts]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L26-L33
[dg-join]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L171-L238
[dg-reject]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L429-L476
[dg-allowed]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L491-L525
[dg-group]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L840-L873
[dg-recovery]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L907-L985
[dg-commit]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L987-L1082
[dg-commithex]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L1016
[dg-watchdog]: https://github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L1084-L1124

[dgm-queue]: https://github.com/thomas-vilte/dave-go/blob/89a4bbff7dd22e748628ecf12befa4a44c0c4261/session/session.go#L808-L817
[dgm-revoke]: https://github.com/thomas-vilte/dave-go/blob/89a4bbff7dd22e748628ecf12befa4a44c0c4261/session/session.go#L841-L849
[dgm-reconcile]: https://github.com/thomas-vilte/dave-go/blob/89a4bbff7dd22e748628ecf12befa4a44c0c4261/session/session.go#L878-L904
[dgm-commit]: https://github.com/thomas-vilte/dave-go/blob/89a4bbff7dd22e748628ecf12befa4a44c0c4261/session/mls.go#L1028-L1149
[dgm-commithex]: https://github.com/thomas-vilte/dave-go/blob/89a4bbff7dd22e748628ecf12befa4a44c0c4261/session/mls.go#L1057
[dgmain-welcome]: https://github.com/thomas-vilte/dave-go/blob/07d9ec38eb061d70f528c10167d9d50b649c9b8c/session/session.go#L951-L976
[dgpr36]: https://github.com/thomas-vilte/dave-go/pull/36
[dgpr47]: https://github.com/thomas-vilte/dave-go/pull/47
[dgpr49]: https://github.com/thomas-vilte/dave-go/pull/49
[dgpr50]: https://github.com/thomas-vilte/dave-go/pull/50
[dgpr52]: https://github.com/thomas-vilte/dave-go/pull/52
[dgpr53]: https://github.com/thomas-vilte/dave-go/pull/53
[dgpr54]: https://github.com/thomas-vilte/dave-go/pull/54
[mlsgo-ext]: https://github.com/thomas-vilte/mls-go/blob/v1.6.0/group/group.go#L3185-L3201

[ld-readme]: https://github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/README.md?plain=1#L3
[ld-revoke]: https://github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L168-L196
[ld-validate]: https://github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L197-L220
[ld-commit]: https://github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L139-L256
[ld-before-welcome]: https://github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L333-L355
[ld-sample]: https://github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/samples/typescript/DaveSessionManager.ts#L76-L114
[ld110-commit]: https://github.com/discord/libdave/blob/d6874165b9a7c8d2cc59712c7aceaa8dffb189b4/cpp/src/mls/session.cpp#L333-L418
[gl-imports]: https://github.com/disgoorg/godave/blob/c38276519ffa3d29911b38689c21b8bb7f7a2bfb/golibdave/golibdave.go#L3-L10
[gl-op29]: https://github.com/disgoorg/godave/blob/c38276519ffa3d29911b38689c21b8bb7f7a2bfb/golibdave/golibdave.go#L160-L185
[gl-op30]: https://github.com/disgoorg/godave/blob/c38276519ffa3d29911b38689c21b8bb7f7a2bfb/golibdave/golibdave.go#L187-L222
[gl-release]: https://github.com/disgoorg/godave/blob/c38276519ffa3d29911b38689c21b8bb7f7a2bfb/libdave/release.txt

[disgo-chan]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L192-L208
[disgo-parse]: https://github.com/disgoorg/disgo/blob/995ee20fe0d6c6349c2d13454c1bd24f2a5d01dc/voice/gateway.go#L635-L645
