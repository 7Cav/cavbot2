package commands

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// DiscordErrorDetail is what the panel's hub page shows after "Discord did
// not rename the channel: ". A 429 reaches it as a *discordgo.RateLimitError,
// because the panel's manager never lets discordgo retry, and the page must
// name the rate limit and the wait, not a lost connection (#340).

func TestDiscordErrorDetailRateLimitNamesTheWaitInWholeMinutes(t *testing.T) {
	got := DiscordErrorDetail(rateLimitError(7*time.Minute + 42*time.Second))

	if !strings.Contains(got, "8 minutes") {
		t.Errorf("detail %q, want the wait rounded up to 8 minutes", got)
	}
	if strings.Contains(got, "could not reach Discord") {
		t.Errorf("detail %q reads as a lost connection; Discord answered", got)
	}
}

// A wait of exactly one minute is singular. Under a minute is not shown as a
// count, and a 429 with no wait still names the limit and gives no number.
func TestDiscordErrorDetailRateLimitWaitBoundaries(t *testing.T) {
	for _, tc := range []struct {
		label      string
		retryAfter time.Duration
		want       string
		unwanted   string
	}{
		{"exactly one minute", time.Minute, "1 minute", "minutes"},
		{"under a minute", 30 * time.Second, "under a minute", ""},
		{"no wait", 0, "a few minutes", ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := DiscordErrorDetail(rateLimitError(tc.retryAfter))

			if !strings.Contains(got, tc.want) {
				t.Errorf("detail %q, want it to carry %q", got, tc.want)
			}
			if tc.unwanted != "" && strings.Contains(got, tc.unwanted) {
				t.Errorf("detail %q carries %q, want it absent", got, tc.unwanted)
			}
		})
	}
}

// Only a 429 carries a wait. Every other refusal keeps the warden phrase.
func TestDiscordErrorDetailNon429CarriesNoWait(t *testing.T) {
	got := DiscordErrorDetail(restError(http.StatusForbidden, 0, rawBodyMarker))

	if got == "" || strings.Contains(got, "minute") {
		t.Errorf("detail %q on a 403, want the warden phrase with no wait", got)
	}
}
