# ADR 0008: AWOL accountable-day model

## Status

Accepted. Implemented across #158 (LOA history store) and #159 (`/awol`
accountable-day count). Supersedes the original `/awol` behavior where the
**On LOA** indicator was purely informative and never affected the flag.

**Amended 2026-10-18** (#787): LOAs come from the 7Cav API on every command in
place of the LOA history store, and a needs-review LOA covers none of its
dates.

## Decision

`/awol` flags on **accountable days**, not raw time since last forum post.

- **AWOL** = no qualifying forum post in the last 7 days. Bot-derived; unrelated
  to the milpac record. (GLOSSARY.md corrected accordingly.)
- A **day** is a **UTC calendar date** — UTC is 7Cav standard time.
- **Accountable day:** candidate dates are `(lastPostDate, today]` (post day
  excluded, today included). An **LOA** covers the inclusive range
  `[StartDate, EndDate]`. Coverage is a **set of dates** — overlapping,
  adjacent, multiple, and future LOAs need no special handling; the union of
  covered dates does the work. A date is accountable unless covered.
- Flag when accountable dates **> 7** (no grace buffer — whole-date counting
  already absorbs the intra-day slop the old magic `8` was compensating for).
- The displayed figure is **days AWOL = accountable − 7** (the overage past the
  requirement), so the number on screen means exactly what the word says.

**LOAs come from the 7Cav API on every command** (#787).

**Degraded mode:** when the LOA call fails, `/awol` computes raw inactivity
with no LOA subtraction, still flags `> 7`, and reuses the #96 unknown/warning
treatment — worded so staff know accountable-day adjustment was *skipped*. It
never silently treats everyone as non-LOA.

## Why

The raw last-post date produces false-positive AWOLs: a trooper back from a long
approved LOA reads as "45 days AWOL" the instant the leave expires, and staff
can action someone who was excused per SOP. Subtracting LOA-covered dates fixes
this at the source.

The count needs every LOA a trooper has, ended ones included. The original
cache kept **one** window per user and pruned it the day it ended, so it could
neither represent multiple LOAs nor recall a recently-ended one. Modeling
coverage as a **set of dates** is what makes overlap/adjacency/future-LOA
handling fall out for free instead of as bespoke merge logic.

Showing the **overage** (days AWOL) rather than the total unexcused count keeps
the label honest: "16 accountable days" invited the same "but it says 16!"
confusion the feature set out to kill, whereas "9d AWOL" is unambiguous. Layout
and severity glyphs (🔴 `>14` · 🟠 `>7` · 🟡 `>0` · ⚪ active LOA) were validated
against live Discord rendering before being locked.

Degrading loudly rather than suppressing the report trades a rare, *visible*
false positive for never blinding staff during an API outage. That is the right
call for an accountability tool where humans are the safety net.

## How to apply

- An LOA excuses **only its own dates** — it never retroactively covers a gap
  that preceded it. A trooper currently on LOA who accrued >7 unexcused dates
  before it is still flagged ("on LOA, still AWOL", ⚪ glyph). This is
  intentionally stricter than "currently on LOA → never AWOL"; it is rare in
  practice because a self-filed LOA is itself a forum post that resets the
  clock. An LOA filed for the Subject by someone else (ADR 0010) is the
  Submitter's post, so it leaves the Subject's clock running.
- Keep the accountable-day math a pure function (dates + windows → days AWOL)
  so every edge case is table-testable without Discord, the 7Cav API, or the
  clock.
- A needs-review LOA covers none of its dates. One LOA in the past year ran
  7,683 days, and counting its dates would have hidden that trooper from
  `/awol` for decades. If an LOA that ends before it starts reaches the count
  anyway, it covers nothing and the bot DEBUG-logs it. It never crashes or
  skews the count.
