package utils

import (
	"testing"
	"time"
)

func TestDaysInMonth(t *testing.T) {
	tests := []struct {
		name  string
		month int
		year  int
		want  int
	}{
		{"january 31", 1, 2023, 31},
		{"february common year", 2, 2023, 28},
		{"february leap year div by 4", 2, 2024, 29},
		{"february century non-leap", 2, 1900, 28},
		{"february 400-year leap", 2, 2000, 29},
		{"april 30", 4, 2023, 30},
		{"june 30", 6, 2023, 30},
		{"september 30", 9, 2023, 30},
		{"november 30", 11, 2023, 30},
		{"december 31", 12, 2023, 31},
		{"july 31", 7, 2023, 31},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := daysInMonth(tc.month, tc.year); got != tc.want {
				t.Fatalf("daysInMonth(%d, %d) = %d, want %d", tc.month, tc.year, got, tc.want)
			}
		})
	}
}

func TestFormatTimeSince(t *testing.T) {
	// Fixed dates keep boundary cases deterministic regardless of the run date.
	// Anchoring to time.Now() (the old test) was flaky: on month-boundary days
	// AddDate overflows, e.g. 31MAY - 1 month -> 01MAY, yielding "30 days".
	date := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}

	tests := []struct {
		name  string
		start time.Time
		end   time.Time
		want  string
	}{
		{"zero duration", date(2026, time.May, 15), date(2026, time.May, 15), "0 days"},
		{"one day", date(2026, time.May, 14), date(2026, time.May, 15), "1 day"},
		{"several days", date(2026, time.May, 10), date(2026, time.May, 15), "5 days"},
		{"one month", date(2026, time.April, 15), date(2026, time.May, 15), "1 month"},
		{"two months no days", date(2026, time.March, 15), date(2026, time.May, 15), "2 months"},
		{"one year", date(2025, time.May, 15), date(2026, time.May, 15), "1 year"},
		{"two years", date(2024, time.May, 15), date(2026, time.May, 15), "2 years"},
		{"year plus month plus day", date(2024, time.April, 14), date(2025, time.May, 15), "1 year, 1 month, 1 day"},
		{"large duration", date(2013, time.February, 13), date(2023, time.May, 15), "10 years, 3 months, 2 days"},
		{"year plus days", date(2023, time.May, 11), date(2026, time.May, 15), "3 years, 4 days"},
		// Boundary: 01MAY -> 31MAY is 30 days, NOT "1 month" — the exact case
		// that flaked the old now.AddDate(0,-1,0) test on the 31st.
		{"thirty days not one month", date(2026, time.May, 1), date(2026, time.May, 31), "30 days"},
		// day borrow across a 30-day month (April)
		{"day borrow across short month", date(2026, time.April, 20), date(2026, time.May, 10), "20 days"},
		// Cross-year month borrow: Dec 15 -> Feb 10. months = Feb-Dec = -10, days
		// = 10-15 = -5. Day borrow: months-- (-11), days += daysInMonth(Jan) (31)
		// -> 26. Month borrow: months < 0 so years-- (0), months += 12 -> 1.
		// Exercises the previously-dead `months < 0` branch.
		{"cross-year month borrow", date(2025, time.December, 15), date(2026, time.February, 10), "1 month, 26 days"},
		// Cross-year borrow with no day borrow, exercising months<0 in isolation:
		// Nov 15 2025 -> Feb 15 2026. months = Feb-Nov = -9 -> +12 = 3, years 1->0.
		{"cross-year month borrow no day borrow", date(2025, time.November, 15), date(2026, time.February, 15), "3 months"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTimeSince(tc.start, tc.end); got != tc.want {
				t.Fatalf("formatTimeSince(%v, %v) = %q, want %q", tc.start, tc.end, got, tc.want)
			}
		})
	}
}

// TestFormatTimeSinceDuration covers the public time.Now()-anchored wrapper.
// Only the zero-duration case is stable against a live clock.
func TestFormatTimeSinceDuration(t *testing.T) {
	if got := FormatTimeSinceDuration(time.Now()); got != "0 days" {
		t.Fatalf("FormatTimeSinceDuration(now) = %q, want %q", got, "0 days")
	}
}
