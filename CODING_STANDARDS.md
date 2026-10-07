# Coding standards

Judgement rules for the Standards review. Each heading names the code its
rules govern. You are done with this file when you have checked every hunk
against the rules under each heading it touches. Look up a term in
`GLOSSARY.md` when its meaning decides a finding.

## Previews, Confirms and saves

**A no-op says so and stops.** A preview that would change nobody says why
where its Confirm would be. A save that would change nothing ends with the
store and the change log as they were. Reference: `nobody-to-remove`,
`nobody-to-add` and `nobody-to-re-add` in `panel/templates/foxhole.html`.
The purge confirmation still offers Purge at zero holders (#474). Flag any
hunk that touches it.

**A Confirm acts on what its preview showed.** The gap between a preview
and its Confirm is a TOCTOU window. The member list can move in it, as when
a username changes hands. A Confirm acts only on members its preview
listed, or refuses and shows the preview again. Reference: `startRemoval`
acts on the IDs its preview posted, and `startAdd` matches its lines again
and refuses with `errAddChanged` when they name other members. A purge
confirmation lists counts rather than members, and the purge acts on
whoever holds the role when it starts.

## The page a save lands on

**The GET re-derives the save's result.** A save ends in a
Post/Redirect/Get. The redirect carries IDs, and the GET shows the result
as it holds at that load, so a reload drops whatever no longer holds.
Reference: `Cleared` and `Skipped` in `panel/foxhole.go`.

## ADRs

**The ADR wins.** Each file name in `docs/adr/` states its decision. Open
the ADRs whose decision a hunk touches. A conflict's finding asks for the
change to fit the ADR. Report an edit to a file in `docs/adr/` as needing
the maintainer's sign-off.

## SQL queries

**Every value is a parameter.** gosec catches concatenated SQL and a
formatted query held in a variable. Review checks the shape it misses:
`fmt.Sprintf` written inside the `Query` or `Exec` call.
