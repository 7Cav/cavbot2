# Coding standards

The Standards review applies these rules to a diff. Each is a judgement call
no linter makes. golangci-lint (`.golangci.yml`) and the test suite enforce
the mechanical rules, so review skips those.

`GLOSSARY.md` defines the terms used here. Each file name in `docs/adr/` says
what that ADR decides. Open an ADR when a hunk touches its decision.

## Foxhole actions and saves

**Nothing to change, nothing to confirm.** A preview or confirmation that
would change nobody offers no Confirm, and says why. A save that changes
nothing writes no change log entry. The remove, add and re-add previews
follow this (#449, #450, #451), and so do note and approval saves (#442,
#444). The purge confirmation is the known exception, tracked in #474.

**A Confirm changes only the members its preview listed.** The member list
can move between a preview and its Confirm, as when a username changes
hands. No action then reaches a member the manager never saw. The removal
acts on the IDs its preview posted. The add matches its lines again and
refuses when they name other members (`errAddChanged`, #451). A purge
confirmation lists counts rather than members, and the purge takes the role
off whoever holds it when it starts. That is by design (#434).

**A save's result holds at every load.** The page a save lands on shows the
save's result, such as a cleared note or the members an Approve skipped. The
page checks that result against the state at its own load, so a reload
drops what no longer holds instead of repeating it (`Cleared` and `Skipped`
in `panel/foxhole.go`, #443, #444).

## Decisions

**An ADR conflict changes the diff, not the ADR.** Where a hunk conflicts
with an ADR, the finding asks for the change to fit the ADR. Report any edit
to a file in `docs/adr/` as needing the maintainer's sign-off.

## SQL

**Queries take every value as a parameter.** gosec catches SQL built by
concatenation, and a formatted query held in a variable. It misses
`fmt.Sprintf` written inside the `Query` or `Exec` call, so review checks
that shape.
