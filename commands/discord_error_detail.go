package commands

import (
	"fmt"
	"time"
)

// DiscordErrorDetail is the body-free phrase a Discord error gets, for a
// caller outside this package that shows a user why Discord refused a call.
// The panel's hub page uses it. The raw response body never reaches the
// phrase.
//
// A 429 is read first, through the spawned channel classifier. The panel's
// calls never let discordgo retry, so a 429 arrives as a
// *discordgo.RateLimitError, which the warden classifier cannot see and would
// call a lost connection (#340). The phrase names the limit and carries the
// wait as plain text, because Discord's <t:...> markup does not render in a
// web page. Everything else is the warden classifier's phrase, as before.
func DiscordErrorDetail(err error) string {
	if fault := classifySpawnedChannelError(err); fault.rateLimited {
		return "rate limit reached. " + rateLimitWait(fault.retryAfter)
	}
	return classifyDiscordError(err).UserDetail
}

// rateLimitWait is the plain-text wait sentence for a 429, from the
// retry_after Discord sent, rounded up to whole minutes. A wait under a
// minute is not shown as a count. A zero wait is a 429 that carried none, so
// the sentence gives no number. It ends without a full stop because the
// caller adds one.
func rateLimitWait(retryAfter time.Duration) string {
	if retryAfter <= 0 {
		return "Try again in a few minutes"
	}
	if retryAfter < time.Minute {
		return "Try again in under a minute"
	}
	minutes := int((retryAfter + time.Minute - 1) / time.Minute)
	if minutes == 1 {
		return "Try again in 1 minute"
	}
	return fmt.Sprintf("Try again in %d minutes", minutes)
}
