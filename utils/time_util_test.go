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

func TestFormatTimeSinceDuration(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name  string
		start time.Time
		want  string
	}{
		{"zero duration", now, "0 days"},
		{"one day", now.AddDate(0, 0, -1), "1 day"},
		{"several days", now.AddDate(0, 0, -5), "5 days"},
		{"one month", now.AddDate(0, -1, 0), "1 month"},
		{"one year", now.AddDate(-1, 0, 0), "1 year"},
		{"two years", now.AddDate(-2, 0, 0), "2 years"},
		{"year plus month plus day", now.AddDate(-1, -1, -1), "1 year, 1 month, 1 day"},
		{"large duration", now.AddDate(-10, -3, -2), "10 years, 3 months, 2 days"},
		{"two months no days", now.AddDate(0, -2, 0), "2 months"},
		{"year plus days", now.AddDate(-3, 0, -4), "3 years, 4 days"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatTimeSinceDuration(tc.start); got != tc.want {
				t.Fatalf("FormatTimeSinceDuration(%v) = %q, want %q", tc.start, got, tc.want)
			}
		})
	}
}
