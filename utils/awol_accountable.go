package utils

import "time"

// awolRequirementDays is the SOP forum-activity requirement: a trooper must post
// at least once every 7 days. Accountable dates beyond this are AWOL overage.
const awolRequirementDays = 7

// utcDate truncates an instant to its UTC calendar date (midnight UTC). UTC is
// 7Cav standard time, so "a day" is a UTC calendar date throughout the AWOL calc.
func utcDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// rawAccountableDates is the count of candidate UTC dates in (lastPost, now] with
// no LOA subtraction: the whole-date inactivity span. This is the figure the
// degraded path flags on when the LOA store can't be trusted (ADR 0008).
func rawAccountableDates(lastPost, now time.Time) int {
	start := utcDate(lastPost)
	today := utcDate(now)
	if !today.After(start) {
		return 0
	}
	// Days strictly after the post date through today inclusive: (today - start).
	return int(today.Sub(start).Hours() / 24)
}

// coveredByLOA reports whether a UTC date is covered by any LOA window. An LOA
// covers the inclusive range [StartDate, EndDate]. A backwards/zero-after-trunc
// range (end before start) covers nothing — those are filtered upstream by
// validLOAWindows so they're also DEBUG-logged once, not silently per-date.
func coveredByLOA(date time.Time, windows []LOAEntry) bool {
	for _, w := range windows {
		s := utcDate(w.StartDate)
		e := utcDate(w.EndDate)
		if !date.Before(s) && !date.After(e) {
			return true
		}
	}
	return false
}

// validLOAWindows returns the windows whose UTC date range is well-formed
// (EndDate not before StartDate). Backwards ranges are dropped and DEBUG-logged
// so one malformed forum post can't crash or skew the count (ADR 0008).
func validLOAWindows(windows []LOAEntry) []LOAEntry {
	out := make([]LOAEntry, 0, len(windows))
	for _, w := range windows {
		if utcDate(w.EndDate).Before(utcDate(w.StartDate)) {
			Debug("LOA window ignored (end before start)",
				"username", w.Username,
				"thread_id", w.ThreadID,
				"start", w.StartDate,
				"end", w.EndDate,
			)
			continue
		}
		out = append(out, w)
	}
	return out
}

// accountableDates counts candidate UTC dates in (lastPost, now] that are NOT
// covered by any (valid) LOA window. The candidate set excludes the post day and
// includes today; coverage is the union of inclusive LOA date ranges, so
// overlapping / adjacent / multiple / future LOAs need no special handling.
func accountableDates(lastPost, now time.Time, windows []LOAEntry) int {
	start := utcDate(lastPost)
	today := utcDate(now)
	valid := validLOAWindows(windows)
	count := 0
	for date := start.AddDate(0, 0, 1); !date.After(today); date = date.AddDate(0, 0, 1) {
		if !coveredByLOA(date, valid) {
			count++
		}
	}
	return count
}

// AccountableDaysAWOL is the pure accountable-day calc (ADR 0008): given a
// trooper's last forum post, the current instant, and their LOA windows, it
// returns days AWOL = accountable dates − 7 (the overage past the 7-day forum
// requirement), clamped at zero. Clock-injected and DB-free so every edge case is
// table-testable. Backwards LOA ranges are ignored and DEBUG-logged.
func AccountableDaysAWOL(lastPost, now time.Time, windows []LOAEntry) int {
	return overage(accountableDates(lastPost, now, windows))
}

// RawDaysAWOL is the degraded-mode figure: whole-date inactivity with NO LOA
// subtraction, overage past the 7-day requirement, clamped at zero. Used when the
// LOA store is unhealthy so the report degrades loudly rather than silently
// treating everyone as non-LOA (ADR 0008).
func RawDaysAWOL(lastPost, now time.Time) int {
	return overage(rawAccountableDates(lastPost, now))
}

// DaysSinceLastPost is the raw whole-date inactivity span (candidate UTC dates in
// (lastPost, now], no LOA subtraction, no 7-day clamp). It is the "last post Nd"
// secondary context shown on every row — the unadjusted figure, so staff still
// know when the trooper actually last posted regardless of LOA coverage.
func DaysSinceLastPost(lastPost, now time.Time) int {
	return rawAccountableDates(lastPost, now)
}

func overage(accountable int) int {
	if accountable <= awolRequirementDays {
		return 0
	}
	return accountable - awolRequirementDays
}

// ActiveWindow returns the LOA window active at `now` (clock-injected by the
// caller, NOT an internal time.Now()), preferring the latest-ending if several
// overlap. The bool is false when no window is active. /awol uses this so the
// On-LOA verdict, the days-AWOL calc, and the [[LOA]] thread link are all decided
// against the SAME `now` — no boundary disagreement between selection and verdict
// (PR #161 clock-skew item). Clock-free GetEntries supplies the windows.
func ActiveWindow(windows []LOAEntry, now time.Time) (LOAEntry, bool) {
	var (
		best  LOAEntry
		found bool
	)
	for _, w := range windows {
		if !w.isActiveAt(now) {
			continue
		}
		if !found || w.EndDate.After(best.EndDate) {
			best, found = w, true
		}
	}
	return best, found
}
