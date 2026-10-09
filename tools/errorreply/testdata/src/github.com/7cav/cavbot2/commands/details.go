package commands

import (
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
)

// DiscordErrorDetail is the phrase a Discord error gets, chosen by
// classifying it, for a page that shows why Discord refused.
func DiscordErrorDetail(err error) string {
	return failureReason(err)
}

// RateLimitWait is the wait a 429 asks for, in whole minutes, read off
// Discord's retry_after.
func RateLimitWait(err error) string {
	var rateLimitErr *discordgo.RateLimitError
	if !errors.As(err, &rateLimitErr) || rateLimitErr.RateLimit == nil || rateLimitErr.TooManyRequests == nil {
		return "Try again in a few minutes"
	}
	minutes := int((rateLimitErr.RetryAfter + time.Minute - 1) / time.Minute)
	return fmt.Sprintf("Try again in %d minutes", minutes)
}

// lastErr is the last error the background sweep met.
var lastErr = errors.New("HTTP 403 Forbidden, {\"message\": \"Missing Access\", \"code\": 50001}")

// LastSweepFailure is the text of the last error the background sweep met.
func LastSweepFailure() string {
	return lastErr.Error()
}

// Describe is a failure sentence around the detail it's given.
func Describe(detail string) string {
	return "Failed: " + detail
}

// Classify is the phrase a Discord error gets, chosen by classifying it, and
// the error, for the caller to log.
func Classify(err error) (string, error) {
	return failureReason(err), err
}
