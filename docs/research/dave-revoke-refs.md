# Does the fork read a revoke op 27 the way Discord sends it?

Date: 2026-10-20. This answers [#839][i839], which came out of [#838][i838]. I read every source at the commit its link names. I also ran two small offline checks, described under "What I ran". Nothing ran against Discord.

## Question

When Discord's voice gateway revokes proposals in op 27, operation type 1, does each ref in the vector carry its own length prefix, as protocol.md defines it? Or is each one a bare 32-byte hash, the way dave-go reads it?

## Answer

Discord sends each ref with its own length prefix. One ref is 33 bytes, `0x20` and then the SHA-256 hash. The fork reads the bare form, so it refuses nearly every revoke Discord sends. It needs a change.

I'm confident of this, but the wire hasn't shown it. No capture holds a revoke, and the gateway's code is closed. The answer rests on the code Discord ships to its clients and on Discord's own spec.

- Discord's native clients use libdave, and libdave reads the refs as a vector of mlspp `bytes`. mlspp reads every `bytes` as a varint length and then that many bytes. A bare 32-byte hash can't parse as a 32-byte ref, since the length and the hash need 33 bytes. So libdave refuses a bare one-ref revoke, reports an MLS failure and sends no commit. That reader hasn't changed since libdave's 1.0 commit in September 2024. Had the gateway sent bare refs, no Discord client could take in a revoke.
- protocol.md writes its binary opcodes in the TLS presentation language with MLS's `<V>` vectors. Op 27's `opaque HashReference<V>` and `HashReference ProposalRef` are copied word for word from RFC 9420. There `ProposalRef` is also the type a commit cites a proposal by, and mlspp, mls-go and OpenMLS all write it with its prefix. The copy in 7Cav/dave-go is byte-identical to Discord's.
- davey, the Rust library under discord.js voice, decodes each ref with OpenMLS's `ProposalRef`, which is a varint-prefixed byte string. This is secondary support. davey talks to the real gateway but isn't Discord's code.

The fork at 5076be6 checks that the vector body is a multiple of 32 bytes and cuts it into 32-byte refs. A prefixed revoke of n refs has a 33n-byte body, which passes that check only when n is a multiple of 32. For any other count the fork returns an error. At 32 refs it would cut them at the wrong offsets, and mls-go skips refs it doesn't hold without an error. Either way the withdrawn proposals stay cached.

The cost today depends on whether some other commit can win. After the error the fork commits again over everything still cached, the withdrawn proposal included, and the gateway drops that commit without a reply. If a member's Discord client commits over the rest and wins, the recorder processes that commit and loses nothing but one wasted op 28. If no commit can win, as when the revoke withdrew the only proposal in flight, nothing answers the recorder. After 15 s its watchdog sends op 31, wipes its MLS state and asks to be added again. Read correctly, the same revoke leaves nothing cached and the recorder sends nothing.

## What the captures hold

No revoke. The three capture branches hold 13 op 27s, and every one is an append.

- dave-go logged 12 of them with `operation_type=0` and one proposal each, 9 in voice-captures and 3 in gateway-reconnect-captures. [Step 1 of voice-captures][cap-vc-01] is typical.
- The 13th is in [voice-heartbeat-probe's `04-member-talking.log`][cap-hb-04]. The probe logged only its length, 502 bytes. The probe sent a commit 4 ms later and then got op 29, which is what follows an append.
- dave-commit-race.md holds no capture of its own. It cites #732's brief, which records one op 27, an add.

That fits #839's guess. A revoke needs the gateway to withdraw a proposal before any commit covers it, and in these runs op 29 came about 0.1 s after each op 27.

The appends do show the outer framing, which both readers already agree on. In step 1 the op 27 is 499 bytes. Without the 2-byte sequence number and the opcode, the payload is the 496 bytes dave-go logs as `total_size=496`. That is one operation-type byte, a 2-byte varint holding 493, and 493 bytes of one MLSMessage with no length of its own. So the gateway writes a `<V>` vector as a varint byte count and then the elements back to back, each in its own encoding. mlspp's vector writer does the same. An MLSMessage carries no outer length. A `bytes` element does, and an append has no `bytes` element to show it. So the appends don't settle the question by themselves.

## libdave

libdave's [README][ld-readme] says Discord's native clients use it. At v1.2.1, 8de72b1, all its flavors handle op 27 in one C++ function.

- `Session::ProcessProposals` [reads a bool and then the refs][ld-read], `std::vector<::mlspp::bytes_ns::bytes> refs; inStream >> refs;`. The bool is the operation type. mlspp writes `true` as `0x01` and [refuses any byte but 0 and 1][m-bool].
- It [looks each ref up in its proposal queue][ld-match]. An unknown ref logs "Cannot revoke unrecognized proposal ref" and returns no commit. A parse error [lands in the catch][ld-catch], which logs "Failed to parse MLS proposals" and also returns no commit. Both call the session's [MLS failure callback][ld-track].
- The queue holds, in a [`bytes` field][ld-queued], [the ref mlspp computes][ld-ref] for each proposal. DAVE's [ciphersuite][ld-suite] is P256_AES128GCM_SHA256_P256, so every ref is 32 bytes.
- The WASM build [binds `ProcessProposals`][ld-wasm-reg] and [passes the bytes straight through][ld-wasm]. So does [the C API][ld-capi], which golibdave [calls][gl-op27]. The JS package [re-exports the WASM module][ld-js] and adds only verification helpers. Discord's [TypeScript sample][ld-sample] hands the op 27 payload to the WASM `ProcessProposals` unchanged. No flavor has a second revoke reader.
- No libdave test builds a revoke. Its test external sender [writes only appends][ld-test].
- The reader is the same in [the 1.0 commit][ld-initial] of 2024-09-17 and in [v1.1.0][ld-110], the version golibdave links.

## mlspp

libdave pins mlspp at [1cc50a1][ld-mlspp]. Four pieces there give the encoding.

- `bytes` is a struct whose one field is a `std::vector<uint8_t>`, [declared `TLS_SERIALIZABLE(_data)`][m-bytes] with no traits. The [struct reader][m-struct] reads that field with the plain vector reader.
- The [vector writer][m-vecw] writes a varint byte count and then each element in its own encoding. The [vector reader][m-vecr] reads the varint, takes that many bytes, and decodes elements until they run out.
- So a `bytes` on the wire is a varint and then the bytes. A `std::vector<bytes>` is a varint byte count and then each `bytes` with its own varint. The [varint][m-varint] is RFC 9420's, with minimal encoding enforced.
- mlspp's own [`HashReference` and `ProposalRef` are `bytes`][m-ref], under a comment quoting RFC 9420's `opaque HashReference<V>`. Its [RefHash][m-refhash] writes RFC 9420's `opaque label<V>` and `opaque value<V>` the same way, as `w << label << value`.

For one ref libdave's encoding is `01 21 20` and the 32-byte hash. For two it's `01 40 42 20 <hash> 20 <hash>`, where `40 42` is the 2-byte varint for 66.

## Discord's protocol

Discord's [protocol.md at 1b2b706][p-op27] defines op 27 like this.

```
opaque HashReference<V>;
HashReference ProposalRef;
...
    case revoke:
      ProposalRef proposal_refs<V>;
```

[Line 7][p-lang] says the document uses the TLS presentation language with RFC 9420's [variable-size vector length headers][rfc-212]. That section says vectors "are encoded as a sequence of encoded elements prefixed with a length". An element of type `opaque HashReference<V>` encodes with its own length. RFC 9420 [§5.2][rfc-52] defines a ref with those same two lines, and [§12.4][rfc-124] has a commit cite proposals as `ProposalRef reference`. mls-go [writes a commit's refs][mg-commit] with `WriteVLBytes(por.ProposalRef)`.

[The copy in 7Cav/dave-go][f-protocol] is byte-identical to Discord's at 1b2b706. I diffed the two files. That commit, from 2025-08-14, is Discord's latest change to protocol.md.

## Secondary support from davey and discord.js

discord.js [depends on `@snazzah/davey`][djs-pkg] for voice. Its [op 27 handler][djs-net] calls [`processProposals`][djs-dave] with the operation-type byte split off. davey [unwraps the outer vector][dv-process] and then [decodes the refs one at a time][dv-revoke] with `ProposalRef::tls_deserialize_bytes`. It [pins OpenMLS 0.8.1][dv-cargo], whose [`ProposalRef` is a `HashReference` holding a `VLBytes`][om-ref], a varint-prefixed byte string. davey has read revokes this way since it [added them][dv-added] in February 2025.

So a second, independent client that runs against Discord's gateway reads the prefixed form too. It isn't Discord's code, so it backs the answer without deciding it.

## The fork

### How it reads a revoke

At 5076be6 on `take-in-multi-proposal-batches`, [`processProposalBatchLocked`][f-dispatch] hands operation type 1 to [`processRevokeProposalsLocked`][f-revoke]. That reads the outer varint, [refuses a body that isn't a multiple of 32][f-mod32], and cuts the rest into 32-byte refs. Its comment cites mls-go's hash size, which is right for the hash and silent about the prefix.

Thomas Vilte wrote it upstream in [bdfabf3][u-commit], "feat(session): implement DAVE revoke proposal processing". The same reader is in [upstream main at 208d8c1][u-main], which is also 7Cav/dave-go's `main`, and in [v0.5.1][u-051], which cavbot2's `develop` [pins][cb-gomod].

I fed the two bodies mlspp wrote to the fork's reader, and it refused both. It returned "revoke body 33 bytes not multiple of 32" for one ref and "revoke body 66 bytes not multiple of 32" for two.

### Why no test caught it

The fork's tests build revokes with [`buildRevokeBatch`][f-build], which wraps the refs in one varint and gives no ref a length of its own. [`revoke_test.go`][f-cases] expects a 33-byte body to fail. The fake gateway [revokes a pending member's add when it disconnects][f-disconnect], through [`revokeLocked`][f-revokelocked], which calls the same builder. Reader and builder share one mistake, so every test passes.

### What a refused revoke does today

This is the code at 5076be6. Take the usual case, where the recorder has already committed over the proposal being withdrawn and is waiting for op 29.

1. [`handleProposalBatchLocked`][f-handle] sees the waiting commit. [`batchOnPreCommitEpochLocked`][f-precommit] counts any revoke as signed for that commit's epoch, so the recorder rolls back to its pre-commit state. The withdrawn proposal is back in the cache with everything else that commit covered.
2. The revoke fails as above. The code [logs `failed to process proposals` at ERROR and carries on][f-continue].
3. Proposals are still cached, so it [commits over all of them][f-cached], the withdrawn proposal included, and sends op 28. [`commitProposalsLocked`][f-commit] arms a [watchdog][f-watchdog] for that commit.
4. The commit cites a proposal the gateway revoked. By [Voice Gateway Commit Validity][p-validity] it's invalid, and by [Commit Ordering][p-order] the gateway drops it without a reply.

What happens next depends on whether another commit can win.

- If a member's Discord client commits over the remaining proposals and wins, op 29 brings that commit. [`reconcileCompetingCommitLocked`][f-reconcile] rolls back and processes it, and mls-go [empties the cache when it merges a commit][mg-merge]. The withdrawn proposal is gone and the recorder is back in step. The cost is one wasted op 28 and an ERROR line.
- If no commit can win, op 29 never comes. That happens when the revoke withdrew the only proposal in flight, since a valid commit needs at least one ref. After [15 s][f-timeout] the watchdog runs [`recoverAfterTimeout`][f-recover]. It sends op 31, [drops the MLS state][f-invalidate], marks the session degraded and sends a new key package. Under [Recovery from Invalid Commit or Welcome][p-recovery], the gateway then proposes removing the recorder, and once the new key package arrives, proposes adding it back.

With the prefixed form read correctly, that second case stops at step 2. The revoke removes the proposal, nothing is left cached, and the fork [logs that it skips the commit][f-cached]. The rollback already cleared the waiting commit, so the old watchdog finds nothing to recover.

On `develop`, v0.5.1 [returns after the error][u-051-return] without committing. The withdrawn proposal stays cached until the next append, whose commit cites it, and the gateway drops that commit too.

Revokes should be rare. The protocol's [example][p-handling] is a pending member who no longer needs adding, which means that member leaves between its add proposal and the first valid commit. In the captures that gap was about 0.1 s. Commit races and stalls like the one in [dave-commit-race.md][r829] stretch it, so the costly case is likeliest when a commit race has already held up the epoch.

### What would have to change

This is what the reading code and the tests would have to do. It isn't a design, and I changed nothing.

- The reader has to take the body as a run of `opaque<V>` elements. For each, it reads a varint with the existing `readTLSVectorLength`, checks the element fits inside the body, and takes those bytes as one ref. A body that ends partway through an element is malformed. A length check of 32 bytes per ref is optional, since libdave has none and a ref of any other length matches nothing. `maxRevokeRefs` would count elements, not 32-byte slices.
- `buildRevokeBatch` has to give each ref its own `writeVLBytes` before wrapping the vector. The fake gateway calls that builder, so it would follow.
- The revoke tests need at least one fixture that didn't come from the fork's own builder, or reader and builder can drift together again. The bytes mlspp wrote under "What I ran" would do.
- The comments on `proposalRefSize`, `processRevokeProposalsLocked` and `buildRevokeBatch` describe the bare form and would change with the code.

One more difference sits outside this question. When a revoke names a ref the client doesn't hold, libdave refuses the whole batch and sends no commit. mls-go's [`RevokeProposals`][mg-revoke] skips that ref, and the fork commits whatever is left. A fixed reader would still differ from libdave there.

## What I ran

- I built mlspp's TLS codec at 1cc50a1, only `lib/tls_syntax` and `lib/bytes`, with a short driver. It encoded `true` and a `std::vector<bytes>` the way libdave's writer would, and decoded bodies the way `ProcessProposals` does. One ref came out as 35 bytes, `01 21 20` and the hash. Two refs came out as 69 bytes, `01 40 42 20 <hash> 20 <hash>`. The prefixed one-ref body read back as one 32-byte ref. Two bare one-ref bodies, with first hash bytes `0x05` and `0xa0`, both threw "Vector is longer than remaining data".
- I fed the 35-byte and 69-byte bodies to the fork's `processRevokeProposalsLocked` at 5076be6 in a throwaway test. It refused both, with the errors quoted above.

Neither the driver nor the test is committed anywhere.

## What the sources don't settle

- No real revoke has been captured. One would settle this alone, and the existing DEBUG logs are enough to read it. A one-ref revoke arrives as a 38-byte op 27 in the prefixed form and a 37-byte one in the bare form, and dave-go logs `total_size=35` against `34`. The fork would also log "revoke body 33 bytes" if the refs are prefixed. Drawing one means a member leaving before a commit covers their add. A second bot account that sends its key package and drops at once, while the recorder sits in the channel, might do it.
- Whether the gateway ever sends 32 or more refs in one revoke. Only then would the fork's check pass, and it would then skip the refs without an error.
- What the gateway does after an invalid first commit in an epoch. dave-commit-race.md left this open too, and it matters for the costly case above.
- Whether a recording loses audio while the gateway removes and re-adds the recorder after op 31. I didn't trace that.
- I read libdave and its sample, not a Discord desktop client.

## Sources

Tickets and earlier research

- [#839][i839], the question. [#838][i838], where it came up. [dave-commit-race.md][r829] on `research/dave-commit-race`.
- Captures: [`research/voice-captures`][cap-vc] at faf98f1, [`research/gateway-reconnect-captures`][cap-gr] at 8a8bac1, [`research/voice-heartbeat-probe`][cap-hb] at c30a7c7.

7Cav/dave-go at 5076be6, on `take-in-multi-proposal-batches`

- `session/mls.go`: [operation type dispatch][f-dispatch], [revoke reader][f-revoke], [multiple-of-32 check][f-mod32], [revoke counted on the commit's epoch][f-precommit], [commit][f-commit], [commit watchdog][f-watchdog], [15 s timeout][f-timeout], [recovery][f-recover], [MLS state reset][f-invalidate]
- `session/session.go`: [batch with a waiting commit][f-handle], [error logged, processing continues][f-continue], [commit over cached proposals][f-cached], [competing commit][f-reconcile]
- Tests: [`buildRevokeBatch`][f-build], [revoke wire cases][f-cases], [`writeVLBytes`][f-vlbytes], [fake gateway disconnect][f-disconnect], [fake gateway revoke][f-revokelocked]
- [protocol.md op 27][f-protocol]

Upstream dave-go and mls-go

- dave-go [bdfabf3][u-commit], [main at 208d8c1][u-main], [v0.5.1 reader][u-051], [v0.5.1 return on error][u-051-return]. cavbot2 `develop` at 68c86ca [pins v0.5.1][cb-gomod].
- mls-go v1.6.0 at 9d524c5: [commit refs written with a prefix][mg-commit], [`RevokeProposals`][mg-revoke], [cache emptied on merge][mg-merge]

libdave v1.2.1 at 8de72b1

- [README][ld-readme], [`QueuedProposal`][ld-queued], [revoke read][ld-read], [ref lookup][ld-match], [ref computed][ld-ref], [parse failure][ld-catch], [failure callback][ld-track], [ciphersuite][ld-suite], [WASM binding][ld-wasm], [WASM registration][ld-wasm-reg], [C API][ld-capi], [JS re-export][ld-js], [TypeScript sample][ld-sample], [test external sender][ld-test], [mlspp pin][ld-mlspp]
- [1.0 commit][ld-initial], [v1.1.0][ld-110]
- godave at c382765: [golibdave op 27][gl-op27]

mlspp at 1cc50a1

- [`HashReference` and `ProposalRef`][m-ref], [RefHash][m-refhash], [`bytes`][m-bytes], [vector writer][m-vecw], [vector reader][m-vecr], [struct reader][m-struct], [varint][m-varint], [bool][m-bool]

Protocol

- Discord's protocol.md at 1b2b706: [presentation language][p-lang], [Proposal Handling][p-handling], [Commit Ordering][p-order], [Voice Gateway Commit Validity][p-validity], [Recovery from Invalid Commit or Welcome][p-recovery], [op 27][p-op27]
- RFC 9420: [§2.1.2][rfc-212], [§5.2][rfc-52], [§12.4][rfc-124]

Secondary

- davey at 73bcec2: [`process_proposals`][dv-process], [revoke refs][dv-revoke], [OpenMLS pin][dv-cargo], [revoke support added][dv-added]
- OpenMLS 0.8.1 at 47dbede: [`HashReference`][om-ref]
- discord.js at 5050a3b: [davey dependency][djs-pkg], [op 27 handler][djs-net], [`processProposals`][djs-dave]

[i839]: https://github.com/7Cav/cavbot2/issues/839
[i838]: https://github.com/7Cav/cavbot2/issues/838
[r829]: https://github.com/7Cav/cavbot2/blob/da824d9ef1843ae392ceee5fc37241269e9227ad/docs/research/dave-commit-race.md
[cap-vc]: https://github.com/7Cav/cavbot2/blob/faf98f1c49846604f6290389f4e539fdf13150cc/docs/research/voice-captures/README.md
[cap-vc-01]: https://github.com/7Cav/cavbot2/blob/faf98f1c49846604f6290389f4e539fdf13150cc/docs/research/voice-captures/01-join-into-a-talking-call.log#L27-L32
[cap-gr]: https://github.com/7Cav/cavbot2/tree/8a8bac1d6b655e00fe53e5a21f9c2662d22c7d12/docs/research/gateway-reconnect-captures
[cap-hb]: https://github.com/7Cav/cavbot2/tree/c30a7c7f89620acf0384faa8fb032bc96a303cfd/docs/research/voice-heartbeat-probe
[cap-hb-04]: https://github.com/7Cav/cavbot2/blob/c30a7c7f89620acf0384faa8fb032bc96a303cfd/docs/research/voice-heartbeat-probe/04-member-talking.log#L38-L40
[cb-gomod]: https://github.com/7Cav/cavbot2/blob/68c86cad3f93696a28f2d34696ca36611cfa8e8f/go.mod#L18

[f-dispatch]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L442-L460
[f-revoke]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L1256-L1310
[f-mod32]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L1285-L1288
[f-precommit]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L300-L310
[f-commit]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L1133-L1220
[f-watchdog]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L1222-L1251
[f-timeout]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L27-L29
[f-recover]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L1101-L1131
[f-invalidate]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/mls.go#L1010-L1024
[f-handle]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/session.go#L834-L873
[f-continue]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/session.go#L884-L889
[f-cached]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/session.go#L895-L915
[f-reconcile]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/session.go#L934-L962
[f-build]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/revoke_test.go#L9-L14
[f-cases]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/revoke_test.go#L37-L48
[f-vlbytes]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/sole_member_test.go#L19-L34
[f-disconnect]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/fake_gateway_test.go#L297-L316
[f-revokelocked]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/session/fake_gateway_test.go#L872-L887
[f-protocol]: https://github.com/7Cav/dave-go/blob/5076be60bacdaaba8a634d71aedacabcce428ffa/protocol.md?plain=1#L1020-L1048

[u-commit]: https://redirect.github.com/thomas-vilte/dave-go/commit/bdfabf37d352837797506e42b86542520eb2466e
[u-main]: https://redirect.github.com/thomas-vilte/dave-go/blob/208d8c19a697a78927fa0e6bbfd9cc574a260fae/session/mls.go#L1164-L1205
[u-051]: https://redirect.github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/mls.go#L1142-L1183
[u-051-return]: https://redirect.github.com/thomas-vilte/dave-go/blob/7a73ceacb28b3e1f1d01fcf5e777b592eb779cd5/session/session.go#L732-L737
[mg-commit]: https://redirect.github.com/thomas-vilte/mls-go/blob/9d524c5b1de7e0954b1ea976e9744460b42f5619/group/commit.go#L409-L413
[mg-revoke]: https://redirect.github.com/thomas-vilte/mls-go/blob/9d524c5b1de7e0954b1ea976e9744460b42f5619/client.go#L1601-L1640
[mg-merge]: https://redirect.github.com/thomas-vilte/mls-go/blob/9d524c5b1de7e0954b1ea976e9744460b42f5619/group/group.go#L2338-L2341

[ld-readme]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/README.md?plain=1#L3
[ld-queued]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L32-L35
[ld-read]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L159-L170
[ld-match]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L172-L196
[ld-ref]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L213
[ld-catch]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L257-L261
[ld-track]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/session.cpp#L23-L26
[ld-suite]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/mls/parameters.cpp#L7-L11
[ld-wasm]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/bindings_wasm.cpp#L139-L152
[ld-wasm-reg]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/bindings_wasm.cpp#L359
[ld-capi]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/src/bindings_capi.cpp#L167-L186
[ld-js]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/js/src/wasm.ts#L1-L3
[ld-sample]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/samples/typescript/DaveSessionManager.ts#L76-L82
[ld-test]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/test/external_sender.cpp#L52-L56
[ld-mlspp]: https://redirect.github.com/discord/libdave/blob/8de72b1f8a2ac3c5a5270755bb8091a62e3c6169/cpp/vcpkg-alts/openssl_3/overlay-ports/mlspp/vcpkg.json#L3
[ld-initial]: https://redirect.github.com/discord/libdave/blob/7b15f1fc16f159da0478aa6be909e38f1e957833/cpp/mls/session.cpp#L145-L153
[ld-110]: https://redirect.github.com/discord/libdave/blob/d6874165b9a7c8d2cc59712c7aceaa8dffb189b4/cpp/src/mls/session.cpp#L162-L170
[gl-op27]: https://redirect.github.com/disgoorg/godave/blob/c38276519ffa3d29911b38689c21b8bb7f7a2bfb/golibdave/golibdave.go#L160-L166

[m-ref]: https://redirect.github.com/cisco/mlspp/blob/1cc50a124a3bc4e143a787ec934280dc70c1034d/include/mls/crypto.h#L36-L41
[m-refhash]: https://redirect.github.com/cisco/mlspp/blob/1cc50a124a3bc4e143a787ec934280dc70c1034d/include/mls/crypto.h#L105-L116
[m-bytes]: https://redirect.github.com/cisco/mlspp/blob/1cc50a124a3bc4e143a787ec934280dc70c1034d/lib/bytes/include/bytes/bytes.h#L110
[m-vecw]: https://redirect.github.com/cisco/mlspp/blob/1cc50a124a3bc4e143a787ec934280dc70c1034d/lib/tls_syntax/include/tls/tls_syntax.h#L212-L228
[m-vecr]: https://redirect.github.com/cisco/mlspp/blob/1cc50a124a3bc4e143a787ec934280dc70c1034d/lib/tls_syntax/include/tls/tls_syntax.h#L283-L313
[m-struct]: https://redirect.github.com/cisco/mlspp/blob/1cc50a124a3bc4e143a787ec934280dc70c1034d/lib/tls_syntax/include/tls/tls_syntax.h#L527-L551
[m-varint]: https://redirect.github.com/cisco/mlspp/blob/1cc50a124a3bc4e143a787ec934280dc70c1034d/lib/tls_syntax/src/tls_syntax.cpp#L120-L175
[m-bool]: https://redirect.github.com/cisco/mlspp/blob/1cc50a124a3bc4e143a787ec934280dc70c1034d/lib/tls_syntax/src/tls_syntax.cpp#L73-L85

[p-lang]: https://redirect.github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L7
[p-handling]: https://redirect.github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L164-L180
[p-order]: https://redirect.github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L289-L293
[p-validity]: https://redirect.github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L295-L305
[p-recovery]: https://redirect.github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L319-L329
[p-op27]: https://redirect.github.com/discord/dave-protocol/blob/1b2b706ad4a562ede5c2ef901f9d77f6dbe79446/protocol.md?plain=1#L1020-L1048
[rfc-212]: https://www.rfc-editor.org/rfc/rfc9420.html#section-2.1.2
[rfc-52]: https://www.rfc-editor.org/rfc/rfc9420.html#section-5.2
[rfc-124]: https://www.rfc-editor.org/rfc/rfc9420.html#section-12.4

[dv-process]: https://redirect.github.com/Snazzah/davey/blob/73bcec27b7190ce47088c831e0aa72600cc657e7/davey/src/session.rs#L386-L400
[dv-revoke]: https://redirect.github.com/Snazzah/davey/blob/73bcec27b7190ce47088c831e0aa72600cc657e7/davey/src/session.rs#L471-L481
[dv-cargo]: https://redirect.github.com/Snazzah/davey/blob/73bcec27b7190ce47088c831e0aa72600cc657e7/davey/Cargo.toml#L13
[dv-added]: https://redirect.github.com/Snazzah/davey/commit/5dfe44a9677857834d62f765d777df087376ddd6
[om-ref]: https://redirect.github.com/openmls/openmls/blob/47dbedecad0c1fd8eb5368d582250ebfcc1e1ce6/openmls/src/ciphersuite/hash_ref.rs#L42-L66
[djs-pkg]: https://redirect.github.com/discordjs/discord.js/blob/5050a3b54c107c3229dd76da52e0cb77dba1b6be/packages/voice/package.json#L66
[djs-net]: https://redirect.github.com/discordjs/discord.js/blob/5050a3b54c107c3229dd76da52e0cb77dba1b6be/packages/voice/src/networking/Networking.ts#L589-L591
[djs-dave]: https://redirect.github.com/discordjs/discord.js/blob/5050a3b54c107c3229dd76da52e0cb77dba1b6be/packages/voice/src/networking/DAVESession.ts#L277-L288
