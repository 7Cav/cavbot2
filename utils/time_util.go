package utils

import (
	"fmt"
	"strings"
	"time"
)

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
	endDate := time.Now()

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
