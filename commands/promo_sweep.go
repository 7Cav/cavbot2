package commands

// Scheduled promotion sweep: the issue #4 "Notify" requirement. A weekly
// background pass posts the promotion-eligible candidate list for the
// configured position scopes to a configured channel, so S1 gets told
// rather than having to remember to ask.
//
// Pattern mirrors the Star Citizen joiner-report scheduler
// (star_citizen_joiners.go): sleep-until-fire loop, per-fire panic recovery
// (a panic in one sweep must not kill the loop; see issue #119 rationale
// there), a narrow session interface for testability, and a `now` seam.
//
// The target channel, swept scope, and the kill switch are all compile-time
// constants, not environment variables: tenant-specific 7Cav config the bot
// keeps in code, matching the joiner report (hardcoded Discord IDs) and
// /warden (role name). Environment variables here are reserved for secrets and
// deployment identity. Disabling the sweep or moving its channel is a code
// change and redeploy, the same as the joiner report.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// 7Cav-specific identifiers, hardcoded rather than env-configured because
// they are tenant-specific (matching the joiner report and /warden).
const (
	promoSweepChannelID = "1529633362275471531"

	// promoSweepScope is the scope swept each week. "activeduty" is the whole
	// ROSTER_TYPE_COMBAT roster, which is what S1 asked for; see the API load
	// note on promoSweepFilter.
	promoSweepScope = "activeduty"

	// promoSweepDisabled is the kill switch. Flip to true and redeploy to
	// silence the weekly sweep.
	promoSweepDisabled = false
)

// TODO(S1 follow-up): Monday 09:00 UTC is a placeholder cadence chosen by
// engineering, not by the consumer. Before this leaves the test guild, ask
// S1 when and how often they actually want the reminder (weekly vs
// fortnightly vs monthly, day, and hour) and update this constant.
var promoSweepFireSchedule = mustWeeklyFireTime(time.Monday, 9, 0)

// promoSweepSession is the Discord REST surface the sweep needs;
// *discordgo.Session satisfies it. The complex send rather than the plain one
// so a large sweep can carry the full report as an attachment.
type promoSweepSession interface {
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

type promoSweepConfig struct {
	ChannelID string
	Positions []string
}

// newPromoSweepConfig builds the sweep config from the compile-time constants.
func newPromoSweepConfig() promoSweepConfig {
	return promoSweepConfig{
		ChannelID: promoSweepChannelID,
		Positions: []string{promoSweepScope},
	}
}

// StartPromoSweepScheduler launches the weekly sweep goroutine. Called from
// main after the gateway opens, alongside StartJoinerReportScheduler.
func StartPromoSweepScheduler(s *discordgo.Session) {
	if promoSweepDisabled {
		utils.Info("Promotion sweep scheduler disabled (promoSweepDisabled)")
		return
	}
	cfg := newPromoSweepConfig()
	utils.Info("Starting promotion sweep scheduler",
		"cadence", "weekly Monday 09:00 UTC",
		"channel_id", cfg.ChannelID,
		"positions", strings.Join(cfg.Positions, ","))
	go runPromoSweepSchedulerLoop(s, cfg, time.Now)
}

// runPromoSweepSchedulerLoop is the goroutine body; split out for the `now`
// seam. Per-fire panic recovery keeps the loop alive across a bad sweep; no
// in-cycle retry; next Monday is the retry.
func runPromoSweepSchedulerLoop(s promoSweepSession, cfg promoSweepConfig, now func() time.Time) {
	for {
		fire := nextPromoSweepFire(now())
		utils.Info("Promotion sweep scheduled", "next_fire_utc", fire.Format(time.RFC3339))
		time.Sleep(time.Until(fire))
		func() {
			defer utils.RecoverPanic("promo-sweep")
			// Anchor eligibility to the scheduled fire time, not wall clock
			// at wakeup, for the same drift reason as the joiner report.
			if err := runPromoSweep(s, cfg, fire); err != nil {
				utils.CaptureError("Promotion sweep failed", err,
					"channel_id", cfg.ChannelID, "fire_utc", fire.Format(time.RFC3339))
			}
		}()
	}
}

// nextPromoSweepFire returns the next scheduled fire strictly after now.
// The schedule is UTC-anchored regardless of the host's timezone, matching
// the joiner report scheduler; only the caller's clock is local. Strict
// "after" avoids a double-fire at exact-boundary starts.
func nextPromoSweepFire(now time.Time) time.Time {
	n := now.UTC()
	candidate := time.Date(n.Year(), n.Month(), n.Day(),
		promoSweepFireSchedule.hour, promoSweepFireSchedule.minute, 0, 0, time.UTC)
	daysUntilWeekday := (int(promoSweepFireSchedule.weekday) - int(candidate.Weekday()) + 7) % 7
	candidate = candidate.AddDate(0, 0, daysUntilWeekday)
	if !candidate.After(n) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate
}

// runPromoSweep executes one sweep: an eligibility pass per configured
// position, one message per position. A failed position is reported and the
// sweep continues to the next; the first send failure aborts (if the channel
// is unreachable every subsequent send fails identically).
func runPromoSweep(s promoSweepSession, cfg promoSweepConfig, asOf time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, position := range cfg.Positions {
		scan, err := collectPromoCandidates(ctx, position, "", asOf, promoFilter{})
		if err != nil {
			utils.CaptureError("Promotion sweep position pass failed", err, "position", position)
			continue
		}
		scopeLabel := scopeDisplay(position)
		msg := &discordgo.MessageSend{}
		if scan.EmptyRoster {
			// A configured (fixed-input) position returning empty is
			// structurally a bug, not a user typo; Sentry per ADR 0002.
			utils.CaptureError(
				"Promotion sweep roster lookup returned zero members",
				fmt.Errorf("empty roster for configured position %q", position),
				"position", position,
			)
			msg.Content = fmt.Sprintf("⚠️ Promotion sweep: the %s roster came back empty. This shouldn't happen for a configured scope; the issue has been reported.", position)
		} else {
			// Same rule as the slash command: the post keeps the inline list
			// (and its "…and N more" notice), and anything longer rides the
			// full detail as attached CSV + HTML reports.
			body, omitted := formatPromoMessage(scopeLabel, promoFilter{}, asOf, scan.Candidates, scan.SkippedCount, scan.ViiActive)
			if omitted > 0 {
				msg.Files = []*discordgo.File{
					promoCSVFile(scopeLabel, promoFilter{}, asOf, scan),
					promoReportFile(scopeLabel, promoFilter{}, asOf, scan),
				}
			}
			msg.Content = "📋 **Weekly promotion sweep**\n" + body
		}
		if _, err := s.ChannelMessageSendComplex(cfg.ChannelID, msg); err != nil {
			return fmt.Errorf("send sweep message for %s: %w", position, err)
		}
		utils.Info("Promotion sweep posted", "position", position, "eligible", len(scan.Candidates))
	}
	return nil
}
