package utils

import (
	"testing"
	"time"
)

// d builds a UTC midnight date for accountable-day table tests.
func d(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// dt builds an arbitrary-time UTC instant — used to prove the calc truncates to
// the calendar date regardless of the time-of-day component.
func dt(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.UTC)
}

func loa(start, end time.Time) LOAEntry {
	return LOAEntry{StartDate: start, EndDate: end}
}

func TestAccountableDaysAWOL(t *testing.T) {
	tests := []struct {
		name     string
		lastPost time.Time
		now      time.Time
		loas     []LOAEntry
		want     int
	}{
		{
			// (Jan 1, Jan 6] = Jan 2..6 = 5 accountable; 5-7 clamped to 0.
			name:     "no LOA, under threshold",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 6),
			want:     0,
		},
		{
			// (Jan 1, Jan 8] = Jan 2..8 = 7 accountable; exactly 7 is NOT over.
			name:     "no LOA, exactly 7 accountable (boundary, not flagged)",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 8),
			want:     0,
		},
		{
			// (Jan 1, Jan 9] = Jan 2..9 = 8 accountable; 8-7 = 1 day AWOL.
			name:     "no LOA, 8 accountable (boundary, 1 day AWOL)",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 9),
			want:     1,
		},
		{
			// 30 accountable dates, no LOA → 30-7 = 23.
			name:     "no LOA long gap, raw equals accountable",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 31),
			want:     23,
		},
		{
			// Candidate (Jan 1, Jan 31] = Jan 2..31 (30 dates). LOA Jan 3..14
			// inclusive covers 12 dates. 30-12 = 18 accountable; 18-7 = 11.
			name:     "expired LOA subtracts covered dates",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 31),
			loas:     []LOAEntry{loa(d(2026, time.January, 3), d(2026, time.January, 14))},
			want:     11,
		},
		{
			// PRD example: last post Jan 1, LOA Jan 3..14, today Jan 15.
			// Candidate Jan 2..15 (14). Covered Jan 3..14 (12). 14-12 = 2
			// accountable. 2-7 clamps to 0 → not flagged.
			name:     "PRD example: only Jan 2 + Jan 15 accountable",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 15),
			loas:     []LOAEntry{loa(d(2026, time.January, 3), d(2026, time.January, 14))},
			want:     0,
		},
		{
			// Active LOA covering through today: last post Jan 1, LOA Jan 2..today(Jan 20).
			// Candidate Jan 2..20 (19) all covered → 0 accountable → 0.
			name:     "active LOA covers whole gap",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 20),
			loas:     []LOAEntry{loa(d(2026, time.January, 2), d(2026, time.January, 20))},
			want:     0,
		},
		{
			// On LOA, still AWOL: 10 unexcused dates before the LOA started.
			// Last post Jan 1. Candidate Jan 2..Jan 31 (30). LOA Jan 12..31 (20)
			// covers the tail. Accountable = Jan 2..11 = 10. 10-7 = 3.
			name:     "on LOA still AWOL (pre-LOA gap counts)",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 31),
			loas:     []LOAEntry{loa(d(2026, time.January, 12), d(2026, time.January, 31))},
			want:     3,
		},
		{
			// Post AFTER an LOA ended: LOA entirely before lastPost → no candidate
			// date is covered. Candidate (Feb 1, Feb 20] = 19. 19-7 = 12.
			name:     "post after LOA, LOA before window contributes nothing",
			lastPost: d(2026, time.February, 1),
			now:      d(2026, time.February, 20),
			loas:     []LOAEntry{loa(d(2026, time.January, 1), d(2026, time.January, 20))},
			want:     12,
		},
		{
			// Partial overlap: LOA starts before lastPost, ends mid-window.
			// Candidate Jan 11..31 (21). LOA Jan 1..20 → covers Jan 11..20 (10).
			// Accountable = Jan 21..31 = 11. 11-7 = 4.
			name:     "partial overlap clamps to candidate window",
			lastPost: d(2026, time.January, 10),
			now:      d(2026, time.January, 31),
			loas:     []LOAEntry{loa(d(2026, time.January, 1), d(2026, time.January, 20))},
			want:     4,
		},
		{
			// Multiple disjoint LOAs both subtracted.
			// Candidate Jan 2..31 (30). LOA1 Jan 3..7 (5), LOA2 Jan 20..25 (6).
			// Covered 11. Accountable 19. 19-7 = 12.
			name:     "multiple disjoint LOAs",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 31),
			loas: []LOAEntry{
				loa(d(2026, time.January, 3), d(2026, time.January, 7)),
				loa(d(2026, time.January, 20), d(2026, time.January, 25)),
			},
			want: 12,
		},
		{
			// Overlapping LOAs merge — no double count.
			// Candidate Jan 2..31 (30). LOA1 Jan 3..10, LOA2 Jan 8..15 → union
			// Jan 3..15 (13). Accountable 17. 17-7 = 10.
			name:     "overlapping LOAs merged (no double-count)",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 31),
			loas: []LOAEntry{
				loa(d(2026, time.January, 3), d(2026, time.January, 10)),
				loa(d(2026, time.January, 8), d(2026, time.January, 15)),
			},
			want: 10,
		},
		{
			// Adjacent windows treated as continuous (no phantom day between).
			// Candidate Jan 2..31 (30). LOA1 Jan 3..10, LOA2 Jan 11..20 → Jan 3..20
			// (18). Accountable 12. 12-7 = 5.
			name:     "adjacent LOAs continuous",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 31),
			loas: []LOAEntry{
				loa(d(2026, time.January, 3), d(2026, time.January, 10)),
				loa(d(2026, time.January, 11), d(2026, time.January, 20)),
			},
			want: 5,
		},
		{
			// Future LOA does not reduce the current count (no candidate date in it).
			// Candidate Jan 2..15 (14). LOA Feb 1..10 is entirely future. 14-7 = 7.
			name:     "future LOA does not reduce count",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 15),
			loas:     []LOAEntry{loa(d(2026, time.February, 1), d(2026, time.February, 10))},
			want:     7,
		},
		{
			// Backwards range (end before start) covers nothing.
			// Candidate Jan 2..20 (19). Bad LOA ignored. 19-7 = 12.
			name:     "backwards range ignored",
			lastPost: d(2026, time.January, 1),
			now:      d(2026, time.January, 20),
			loas:     []LOAEntry{loa(d(2026, time.January, 15), d(2026, time.January, 5))},
			want:     12,
		},
		{
			// Time-of-day components must not matter: truncates to UTC date.
			// lastPost late Jan 1, now early Jan 9 → still (Jan 1, Jan 9] = 8 → 1.
			name:     "truncates time-of-day to UTC date",
			lastPost: dt(2026, time.January, 1, 23, 59),
			now:      dt(2026, time.January, 9, 0, 1),
			want:     1,
		},
		{
			// lastPost == now (posted today): zero candidate dates → 0.
			name:     "posted today, zero candidates",
			lastPost: d(2026, time.January, 9),
			now:      d(2026, time.January, 9),
			want:     0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AccountableDaysAWOL(tc.lastPost, tc.now, tc.loas)
			if got != tc.want {
				t.Fatalf("AccountableDaysAWOL = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestValidLOAWindows_DropsZeroValueEntry pins item 9: a zero-value LOAEntry
// (both dates zero) is malformed and must be filtered, never treated as covering
// the epoch-zero UTC date. Unreachable from production (parseLOAPost only emits
// when both dates parse) but cheap to harden.
func TestValidLOAWindows_DropsZeroValueEntry(t *testing.T) {
	got := validLOAWindows([]LOAEntry{{}})
	if len(got) != 0 {
		t.Fatalf("validLOAWindows should drop a zero-value entry; got %d kept", len(got))
	}
}

// TestValidLOAWindows_MixedSliceKeepsValid pins that a mixed slice (one valid
// window + one end-before-start window for the same user) keeps the valid one so
// it still subtracts.
func TestValidLOAWindows_MixedSliceKeepsValid(t *testing.T) {
	valid := loa(d(2026, time.January, 3), d(2026, time.January, 10))
	backwards := loa(d(2026, time.January, 20), d(2026, time.January, 5))
	got := validLOAWindows([]LOAEntry{valid, backwards})
	if len(got) != 1 || !got[0].StartDate.Equal(valid.StartDate) {
		t.Fatalf("validLOAWindows should keep only the well-formed window; got %+v", got)
	}
}

// TestAccountableDaysAWOL_MixedSliceValidStillSubtracts pins the same via the
// public calc: one valid + one backwards window for the user → the valid one
// still subtracts its covered dates.
func TestAccountableDaysAWOL_MixedSliceValidStillSubtracts(t *testing.T) {
	// Candidate (Jan 1, Jan 20] = Jan 2..20 (19). Valid LOA Jan 3..10 covers 8.
	// Backwards LOA ignored. Accountable 11 → 11-7 = 4.
	got := AccountableDaysAWOL(
		d(2026, time.January, 1),
		d(2026, time.January, 20),
		[]LOAEntry{
			loa(d(2026, time.January, 3), d(2026, time.January, 10)),
			loa(d(2026, time.January, 20), d(2026, time.January, 5)),
		},
	)
	if got != 4 {
		t.Fatalf("AccountableDaysAWOL = %d, want 4", got)
	}
}

// TestActiveWindow_UsesInjectedNow pins the PR #161 clock-skew fix: window
// selection is decided against the caller's `now`, not an internal clock, and at
// an exact boundary (now == EndDate is still active; now one tick past is not).
func TestActiveWindow_UsesInjectedNow(t *testing.T) {
	w := loa(d(2026, time.January, 1), d(2026, time.January, 10))
	w.ThreadID = 7
	windows := []LOAEntry{w}

	// On the StartDate (inclusive lower bound) → active.
	if got, ok := ActiveWindow(windows, d(2026, time.January, 1)); !ok || got.ThreadID != 7 {
		t.Fatalf("expected active window on StartDate (inclusive); got ok=%v id=%d", ok, got.ThreadID)
	}
	// On the EndDate (inclusive) → active.
	if got, ok := ActiveWindow(windows, d(2026, time.January, 10)); !ok || got.ThreadID != 7 {
		t.Fatalf("expected active window on EndDate; got ok=%v id=%d", ok, got.ThreadID)
	}
	// One day past EndDate → not active.
	if _, ok := ActiveWindow(windows, d(2026, time.January, 11)); ok {
		t.Fatalf("expected no active window past EndDate")
	}
	// Before StartDate → not active.
	if _, ok := ActiveWindow(windows, d(2025, time.December, 31)); ok {
		t.Fatalf("expected no active window before StartDate")
	}
}

// TestActiveWindow_NoActiveReturnsFalse pins the empty/no-active return: an empty
// history and a history with no window covering `now` both yield (_, false).
func TestActiveWindow_NoActiveReturnsFalse(t *testing.T) {
	if _, ok := ActiveWindow(nil, d(2026, time.January, 5)); ok {
		t.Fatalf("empty history should yield no active window")
	}
	windows := []LOAEntry{loa(d(2026, time.January, 1), d(2026, time.January, 10))}
	if _, ok := ActiveWindow(windows, d(2026, time.February, 1)); ok {
		t.Fatalf("no window covers now → expected (_, false)")
	}
}

func TestActiveWindow_LatestEndingWinsAmongOverlapping(t *testing.T) {
	a := loa(d(2026, time.January, 1), d(2026, time.January, 10))
	a.ThreadID = 1
	b := loa(d(2026, time.January, 5), d(2026, time.January, 20))
	b.ThreadID = 2
	got, ok := ActiveWindow([]LOAEntry{a, b}, d(2026, time.January, 8))
	if !ok || got.ThreadID != 2 {
		t.Fatalf("expected latest-ending active window (id 2); got ok=%v id=%d", ok, got.ThreadID)
	}
}

// TestDaysSinceLastPost_NoClampContract pins that DaysSinceLastPost is the raw
// whole-date span with NO 7-day clamp, unlike RawDaysAWOL which subtracts the
// requirement. 8 candidate dates → DaysSinceLastPost 8, RawDaysAWOL 1; 7 dates →
// DaysSinceLastPost 7, RawDaysAWOL 0.
func TestDaysSinceLastPost_NoClampContract(t *testing.T) {
	// (Jan 1, Jan 8] = 7 candidate dates.
	if got := DaysSinceLastPost(d(2026, time.January, 1), d(2026, time.January, 8)); got != 7 {
		t.Fatalf("DaysSinceLastPost(Jan1,Jan8) = %d, want 7 (no clamp)", got)
	}
	if got := RawDaysAWOL(d(2026, time.January, 1), d(2026, time.January, 8)); got != 0 {
		t.Fatalf("RawDaysAWOL(Jan1,Jan8) = %d, want 0 (clamped at requirement)", got)
	}
}

// TestDaysSinceLastPost_EarlyReturnGuard exercises the rawAccountableDates
// now<=lastPost guard via the raw path: posted today and a future-clock skew both
// yield zero candidate dates.
func TestDaysSinceLastPost_EarlyReturnGuard(t *testing.T) {
	if got := DaysSinceLastPost(d(2026, time.January, 9), d(2026, time.January, 9)); got != 0 {
		t.Fatalf("DaysSinceLastPost posted-today = %d, want 0", got)
	}
	// now before lastPost (clock skew) → still 0, not negative.
	if got := DaysSinceLastPost(d(2026, time.January, 9), d(2026, time.January, 1)); got != 0 {
		t.Fatalf("DaysSinceLastPost now<lastPost = %d, want 0", got)
	}
}

func TestRawDaysAWOL_NoSubtractionBoundary(t *testing.T) {
	// 7 whole dates → not flagged; 8 → 1. LOA windows are irrelevant here (raw).
	if got := RawDaysAWOL(d(2026, time.January, 1), d(2026, time.January, 8)); got != 0 {
		t.Fatalf("RawDaysAWOL 7 dates = %d, want 0", got)
	}
	if got := RawDaysAWOL(d(2026, time.January, 1), d(2026, time.January, 9)); got != 1 {
		t.Fatalf("RawDaysAWOL 8 dates = %d, want 1", got)
	}
}

// TestAccountableDaysAWOL_ZeroRangeCovered pins that a single-date LOA
// (StartDate == EndDate) covers exactly that one inclusive date — it is valid,
// not a malformed zero range. Candidate Jan 2..10 (9). LOA Jan 5..5 covers 1.
// Accountable 8. 8-7 = 1.
func TestAccountableDaysAWOL_SingleDateLOACoversOneDay(t *testing.T) {
	got := AccountableDaysAWOL(
		d(2026, time.January, 1),
		d(2026, time.January, 10),
		[]LOAEntry{loa(d(2026, time.January, 5), d(2026, time.January, 5))},
	)
	if got != 1 {
		t.Fatalf("AccountableDaysAWOL = %d, want 1", got)
	}
}
