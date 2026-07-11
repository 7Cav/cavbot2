# ADR 0010: LOA Subject attribution — parse the last Username label

## Status

Accepted. Fixes #221 (`/loa` showed the person who filed an LOA instead of the
trooper on leave).

## Decision

`parseLOAPost` attributes an LOA to its **Subject** — the last `Username` label
in the post body — not the first.

An LOA is filed via a **PAF** (Personnel Action Form). When one trooper files on
behalf of another, the PAF body carries **two** `Username` labels: the
**Submitter** first (auto-filled from the filing account), then the **Subject**
inside the Rank/Username/Primary Billet block. The LOA belongs to the Subject.
The parser reads all `Username` matches and keeps the **last** one. A
self-request has a single label (or a duplicate naming the same person), so the
rule leaves those unchanged. `Start Date` / `End Date` are never duplicated in
practice and keep their first-match extraction.

## Why

The old parser took the first `Username` match, so every on-behalf LOA was filed
under the submitter — the real trooper never appeared in `/loa`, and `/awol`
credited the wrong person with LOA coverage. Against a full mirror of the LOA
forum nodes, of the posts where the two labels differ the **last** label matches
the machine-generated thread title (`[LOA Request] - <Rank>.<Name> | …`) 95% of
the time and the first only 2%; across all posts the last label agrees with the
title 98.9% vs 94.2%. So last-match is strictly better in every bucket, and no
post carries three or more labels.

We took the body's last label rather than the title because `/loa` keys the
cache on the `Username` **field** and looks it up against each roster member's
forum username — the body value is that field, whereas the title is a rendered
display string with a rank prefix that would need stripping and risks a
display-vs-username mismatch. Last-match also needs no special handling for the
posts that lack a standard title.

We fixed the parser rather than the forum form. The form's field labels are the
bot's parsing contract, so any relabel is a coupled, coordinated change — and,
crucially, a form change cannot reach the PAFs already filed. The parser fix
does: the LOA cache is in-process with no persistence, so every deploy
cold-starts and re-scans the last year of threads, re-attributing the historical
on-behalf PAFs on the next release with no migration.

## Consequences

- Fixes heal on the **next release deploy** (cold-start re-scan). There is no
  hot path; prod stays wrong until then.
- `/awol` is corrected for free — same cache, now keyed on the Subject.
- If the form is ever relabelled so the Submitter line is no longer a `Username`
  label, the Subject becomes the lone label and the last-match rule still holds.
- The rule assumes no field *after* the Subject carries a label containing the
  substring `Username` (a hypothetical `Submitter Username` or `Forum Username`
  would be grabbed instead). This holds across every current post — the max is
  two labels, the second being the Subject. Anchoring the label to line-start
  would harden against that drift, but was **rejected**: it drops two real LOAs
  whose `Rank`/`Username`/`Billet` fields are collapsed onto one line
  (`Rank: PVT Username: Name …`). Form drift is instead guarded by the
  mirror-seeded regression cases, per the LOA parsing note in CLAUDE.md.
