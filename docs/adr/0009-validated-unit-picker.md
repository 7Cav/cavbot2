# ADR 0009: Warden internal bulk-add takes a validated unit picker, not free text

## Status

Accepted. Implemented in #208 (issue #206).

## Decision

`/warden-bulkadd-internal` chooses its unit from a `Choices`-backed dropdown,
never from a typed string. Behind the dropdown is a small registry in
`commands/warden_bulkadd_internal.go`:

```go
type wardenInternalUnit struct{ value, label, query string }
```

The operator picks a `value`; the command resolves it to a registry row and
sends that row's author-controlled `query` to the milpac roster lookup. A value
that isn't in the registry is rejected before any roster fetch or role grant.
Adding a unit later is one new row: the dropdown choices and the lookup both
derive from the same slice, so there is no other code to touch.

## Why

The command is a bulk role-grant with no cheap undo. `/warden remove` only takes
the role off one member per call, so there is no cheap *bulk* undo: short of
removing each member by hand, the only reset is `/warden purge`, which wipes
everyone in `Verified Warden Internal` and forces a full rebuild. So the cost of
granting the wrong set of people is high and the recovery is disruptive.

The roster lookup is a fuzzy position-group substring search. With a free-text
field, a careless short query turns a safe lookup into a dangerous one: for
example `7` instead of `D/ACD` substring-matches most of the regiment, and the
command would happily add all of them. The blast radius of a typo is the whole
server.

A picker removes that input entirely. The operator can only emit a `value` the
registry already holds, and each registry `query` is verified by the author to
isolate exactly one unit before it ships. The registry is therefore both the
extension seam and the safety boundary: breadth is fixed at authoring time, not
at the keyboard.

This is also why there is no count cap on the command. A cap exists to bound
operator-controlled breadth; with the picker there is no operator-controlled
breadth left to bound, so a cap could only ever falsely reject a legitimately
large unit. The safety lives in the registry, not in a backstop.

## How to apply

- Adding a unit: append one `wardenInternalUnit` row whose `query` you have
  checked isolates only that unit's roster. Change nothing else.
- Never replace the picker with a free-text option, and never feed a roster
  lookup a position string an operator typed for a destructive or bulk grant.
- For the empty-result handling that pairs with this fixed input, see ADR 0002.
