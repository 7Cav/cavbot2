# ADR 0008: AWOL accountable-day model + LOA history store

## Status

Accepted. Implemented across #158 (LOA history store) and #159 (`/awol`
accountable-day count). Supersedes the original `/awol` behavior where the
**On LOA** indicator was purely informative and never affected the flag.

## Decision

`/awol` flags on **accountable days**, not raw time since last forum post.

- **AWOL** = no qualifying forum post in the last 7 days. Bot-derived; unrelated
  to the milpac record. (CONTEXT.md corrected accordingly.)
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

The **LOA cache becomes an LOA history store**:

- Multiple windows per username, deduped by **`ThreadID`** (one thread is one
  LOA; a second LOA is always a new thread).
- **Retention** replaces prune-on-expiry: a window is kept until its `EndDate`
  is older than the cold-start backfill horizon, sharing one constant with that
  backfill so warm and cold-started caches hold the identical window.
- New `GetEntries(username)`; `GetEntry`/`IsOnLOA`/`IsHealthy` unchanged so the
  store's two consumers (`/awol`, `/loa`) keep working.

**Degraded mode:** when the cache is unhealthy, `/awol` computes raw inactivity
with no LOA subtraction, still flags `> 7`, and reuses the #96 unknown/warning
treatment — worded so staff know accountable-day adjustment was *skipped*. It
never silently treats everyone as non-LOA.

## Why

The raw last-post date produces false-positive AWOLs: a trooper back from a long
approved LOA reads as "45 days AWOL" the instant the leave expires, and staff
can action someone who was excused per SOP. Subtracting LOA-covered dates fixes
this at the source.

The history store is forced by the math: the previous cache kept **one** window
per user and pruned it the day it ended, so it could neither represent multiple
LOAs nor recall a recently-ended one — both of which the accountable count
needs. Modeling coverage as a **set of dates** is what makes overlap/adjacency/
future-LOA handling fall out for free instead of as bespoke merge logic.

Showing the **overage** (days AWOL) rather than the total unexcused count keeps
the label honest: "16 accountable days" invited the same "but it says 16!"
confusion the feature set out to kill, whereas "9d AWOL" is unambiguous. Layout
and severity glyphs (🔴 `>14` · 🟠 `>7` · 🟡 `>0` · ⚪ active LOA) were validated
against live Discord rendering before being locked.

Degrading loudly rather than suppressing the report trades a rare, *visible*
false positive for never blinding staff during a forum-DB blip — the right call
for an accountability tool where humans are the safety net.

## How to apply

- An LOA excuses **only its own dates** — it never retroactively covers a gap
  that preceded it. A trooper currently on LOA who accrued >7 unexcused dates
  before it is still flagged ("on LOA, still AWOL", ⚪ glyph). This is
  intentionally stricter than "currently on LOA → never AWOL"; it is rare in
  practice because filing an LOA is itself a forum post that resets the clock.
- When touching the LOA store, preserve the `GetEntry`/`IsOnLOA`/`IsHealthy`
  contracts — `/loa` depends on them too, not just `/awol`'s LOA tag.
- Keep the accountable-day math a pure function (dates + windows → days AWOL)
  so every edge case is table-testable without Discord, the DB, or the clock.
- A malformed LOA range (end before start) must cover no dates and be
  DEBUG-logged — never crash or skew the count.
