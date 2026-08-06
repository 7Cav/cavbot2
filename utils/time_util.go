package utils

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	// zuluDatePattern matches DDMMMYY with a one- or two-digit day.
	zuluDatePattern = regexp.MustCompile(`^(\d{1,2})([A-Za-z]{3})(\d{2})$`)
	// zuluTimePattern matches HHMM with an optional Zulu suffix. The suffix is
	// a no-op — the value is already Zulu — but members write it, so accepting
	// it costs nothing and rejecting it would fail the way people actually type.
	zuluTimePattern = regexp.MustCompile(`^(\d{2})(\d{2})[Zz]?$`)
)

var zuluMonths = map[string]string{
	"JAN": "01", "FEB": "02", "MAR": "03", "APR": "04",
	"MAY": "05", "JUN": "06", "JUL": "07", "AUG": "08",
	"SEP": "09", "OCT": "10", "NOV": "11", "DEC": "12",
}

// Callers need to know WHICH half was bad so they can tell the member which
// field to fix — naming the field they got right is worse than saying nothing.
var (
	ErrInvalidZuluDate = errors.New("invalid Zulu date")
	ErrInvalidZuluTime = errors.New("invalid Zulu time")
)

// ParseZuluDateTime parses a regiment-style date and time pair as Zulu (UTC),
// e.g. ("10NOV25", "1830"). The century is fixed at 20xx. Each half is validated
// on its own — rather than assembling both and parsing once — so that calendar
// range errors are attributed to the right field: 31FEB26 is a bad date, 2500
// is a bad time, and a single combined parse cannot tell the two apart.
// Errors wrap ErrInvalidZuluDate or ErrInvalidZuluTime; match with errors.Is.
func ParseZuluDateTime(dateStr, timeStr string) (time.Time, error) {
	dateParts := zuluDatePattern.FindStringSubmatch(dateStr)
	if dateParts == nil {
		return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidZuluDate, dateStr)
	}
	month, ok := zuluMonths[strings.ToUpper(dateParts[2])]
	if !ok {
		return time.Time{}, fmt.Errorf("%w: unknown month %s", ErrInvalidZuluDate, dateParts[2])
	}
	day := dateParts[1]
	if len(day) == 1 {
		day = "0" + day
	}

	timeParts := zuluTimePattern.FindStringSubmatch(timeStr)
	if timeParts == nil {
		return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidZuluTime, timeStr)
	}

	datePart := fmt.Sprintf("20%s-%s-%s", dateParts[3], month, day)
	if _, err := time.Parse("2006-01-02", datePart); err != nil {
		return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidZuluDate, dateStr)
	}
	timePart := fmt.Sprintf("%s:%s", timeParts[1], timeParts[2])
	if _, err := time.Parse("15:04", timePart); err != nil {
		return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidZuluTime, timeStr)
	}

	return time.Parse(time.RFC3339, datePart+"T"+timePart+":00Z")
}

func daysInMonth(month, year int) int {
	switch month {
	case 2: // Leap years need to die along with daylight savings time
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	default:
		return 31
	}
}

func FormatTimeSinceDuration(startDate time.Time) string {
	return formatTimeSince(startDate, time.Now())
}

// formatTimeSince is the clock-injected core of FormatTimeSinceDuration. Tests
// drive it with a fixed endDate so boundary cases stay deterministic — anchoring
// to time.Now() makes the month-length AddDate math flaky near month boundaries
// (e.g. on the 31st, "one month ago" overflows to the 1st of the current month).
func formatTimeSince(startDate, endDate time.Time) string {
	years := endDate.Year() - startDate.Year()
	months := int(endDate.Month() - startDate.Month())
	days := endDate.Day() - startDate.Day()

	if days < 0 {
		months--
		prevMonth := endDate.AddDate(0, -1, 0)
		days += daysInMonth(int(prevMonth.Month()), prevMonth.Year())
	}
	if months < 0 {
		years--
		months += 12
	}

	var parts []string
	if years > 0 {
		if years == 1 {
			parts = append(parts, "1 year")
		} else {
			parts = append(parts, fmt.Sprintf("%d years", years))
		}
	}
	if months > 0 {
		if months == 1 {
			parts = append(parts, "1 month")
		} else {
			parts = append(parts, fmt.Sprintf("%d months", months))
		}
	}
	if days > 0 {
		if days == 1 {
			parts = append(parts, "1 day")
		} else {
			parts = append(parts, fmt.Sprintf("%d days", days))
		}
	}

	if len(parts) == 0 {
		return "0 days"
	}
	return strings.Join(parts, ", ")
}
