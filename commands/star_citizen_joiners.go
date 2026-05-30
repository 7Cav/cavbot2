package commands

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// 7Cav-specific identifiers and cadence. Hardcoded rather than env-configured
// because they're tenant-specific (matching the /warden role-ID precedent).
const (
	sparrowDiscordID  = "154035997187899392"
	starCitizenRoleID = "1385946179174531223"

	// joinerLookback must stay >= the cadence interval below, or members
	// who joined between two fires would never be reported.
	joinerLookback = 7 * 24 * time.Hour

	// guildMembersPageLimit is the per-page max accepted by Discord's
	// GET /guilds/{id}/members endpoint.
	guildMembersPageLimit = 1000
)

// weeklyFireTime is a typed wrapper so invalid combinations (hour=25 etc.)
// can't be expressed silently. Fields are lowercase so callers route through
// mustWeeklyFireTime, which validates at package init.
type weeklyFireTime struct {
	weekday time.Weekday
	hour    int
	minute  int
}

func mustWeeklyFireTime(wd time.Weekday, hour, minute int) weeklyFireTime {
	if wd < time.Sunday || wd > time.Saturday {
		panic(fmt.Sprintf("weeklyFireTime: invalid weekday %d", wd))
	}
	if hour < 0 || hour > 23 {
		panic(fmt.Sprintf("weeklyFireTime: invalid hour %d", hour))
	}
	if minute < 0 || minute > 59 {
		panic(fmt.Sprintf("weeklyFireTime: invalid minute %d", minute))
	}
	return weeklyFireTime{weekday: wd, hour: hour, minute: minute}
}

var joinerFireSchedule = mustWeeklyFireTime(time.Sunday, 4, 20)

// joinerReportSession is the Discord REST surface the report uses. Narrow
// interface so the walker is testable without a live gateway; *discordgo.Session
// satisfies it.
type joinerReportSession interface {
	GuildMembers(guildID string, after string, limit int, options ...discordgo.RequestOption) ([]*discordgo.Member, error)
	UserChannelCreate(recipientID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	ChannelMessageSend(channelID string, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

// nextJoinerReportFire returns the next scheduled fire strictly after now.
// Strict-after avoids a double-fire if the scheduler starts at the exact
// moment of a scheduled fire (or recomputes immediately after one).
func nextJoinerReportFire(now time.Time) time.Time {
	n := now.UTC()
	candidate := time.Date(n.Year(), n.Month(), n.Day(),
		joinerFireSchedule.hour, joinerFireSchedule.minute, 0, 0, time.UTC)
	daysUntilWeekday := (int(joinerFireSchedule.weekday) - int(candidate.Weekday()) + 7) % 7
	candidate = candidate.AddDate(0, 0, daysUntilWeekday)
	if !candidate.After(n) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate
}

// walkRecentJoinersWithRole paginates the full guild member list and returns
// the subset that joined within `lookback` of `now` and currently holds
// `roleID`. Results are sorted by JoinedAt ascending so the DM reads
// chronologically. A `JoinedAt` exactly equal to the cutoff (now - lookback)
// is included.
func walkRecentJoinersWithRole(
	s joinerReportSession,
	guildID string,
	now time.Time,
	lookback time.Duration,
	roleID string,
) ([]*discordgo.Member, error) {
	cutoff := now.Add(-lookback)
	var matches []*discordgo.Member
	var after string
	for {
		page, err := s.GuildMembers(guildID, after, guildMembersPageLimit)
		if err != nil {
			return nil, fmt.Errorf("GuildMembers (after=%q): %w", after, err)
		}
		if len(page) == 0 {
			break
		}
		var lastSeenID string
		for _, m := range page {
			if m == nil || m.User == nil {
				// Nil-User entries should never appear from a healthy API
				// response; log at DEBUG so a LOG_LEVEL=DEBUG run can detect
				// upstream data-quality drift.
				utils.Debug("joiner walk: skipping member with nil User",
					"guild_id", guildID, "after", after)
				continue
			}
			lastSeenID = m.User.ID
			if m.JoinedAt.Before(cutoff) {
				continue
			}
			for _, r := range m.Roles {
				if r == roleID {
					matches = append(matches, m)
					break
				}
			}
		}
		if lastSeenID == "" {
			// Whole page had no usable User.ID — we can't advance the cursor
			// without risking re-requesting the same page indefinitely. Stop
			// and surface as an error so a clean-looking "0 joiners" report
			// doesn't mask an upstream Discord regression.
			err := fmt.Errorf("page of %d members had no usable User.ID; halting pagination at after=%q",
				len(page), after)
			return nil, err
		}
		after = lastSeenID
		// Optimization: a short page can't have a successor under current
		// pagination behavior, so skip the unnecessary follow-up request.
		// The empty-page check above is the actual walk-complete signal.
		if len(page) < guildMembersPageLimit {
			break
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].JoinedAt.Before(matches[j].JoinedAt)
	})
	return matches, nil
}

// formatJoinerReport renders the DM body. weekOf is the start of the rolling
// lookback window so Sparrow can confirm which window the report covers.
func formatJoinerReport(matches []*discordgo.Member, weekOf time.Time) string {
	noun := "joiners"
	if len(matches) == 1 {
		noun = "joiner"
	}
	header := fmt.Sprintf(
		"Star Citizen joiner report for week of %s: %d new %s.",
		weekOf.UTC().Format("2006-01-02"), len(matches), noun,
	)
	if len(matches) == 0 {
		return header
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	for _, m := range matches {
		name := m.User.Username
		if m.Nick != "" {
			name = m.Nick + " (" + name + ")"
		}
		fmt.Fprintf(&b, "- %s — `%s` — joined %s\n",
			name, m.User.ID, m.JoinedAt.UTC().Format(time.RFC3339))
	}
	return strings.TrimRight(b.String(), "\n")
}

// runJoinerReport performs one fire of the report: walk → format → DM. The
// empty-match case still DMs so Sparrow can confirm the job ran.
func runJoinerReport(s joinerReportSession, guildID string, now time.Time) error {
	matches, err := walkRecentJoinersWithRole(s, guildID, now, joinerLookback, starCitizenRoleID)
	if err != nil {
		return fmt.Errorf("walk members: %w", err)
	}
	body := formatJoinerReport(matches, now.Add(-joinerLookback))
	ch, err := s.UserChannelCreate(sparrowDiscordID)
	if err != nil {
		return fmt.Errorf("open DM: %w", err)
	}
	if _, err := s.ChannelMessageSend(ch.ID, body); err != nil {
		return fmt.Errorf("send DM: %w", err)
	}
	utils.Info("Star Citizen joiner report sent",
		"matches", len(matches),
		"week_of", now.Add(-joinerLookback).UTC().Format("2006-01-02"),
	)
	return nil
}

// StartJoinerReportScheduler launches the weekly background goroutine. Each
// iteration sleeps until next Sunday 04:20 UTC, fires the report, then loops.
// Per-fire failures are logged + captured to Sentry; no in-cycle retry —
// next Sunday is the retry.
func StartJoinerReportScheduler(s *discordgo.Session, guildID string) {
	utils.Info("Starting Star Citizen joiner report scheduler",
		"cadence", "weekly Sunday 04:20 UTC")
	go runJoinerReportSchedulerLoop(s, guildID, time.Now)
}

// runJoinerReportSchedulerLoop is the body of the scheduler goroutine. Split
// out for the testable seam on `now`; the loop itself never returns under
// normal operation, so it's exercised end-to-end rather than unit-tested.
//
// Panic recovery is scoped to each per-fire execution (the inner func below),
// NOT the outer loop. A panic in one report fire is captured to Sentry and the
// loop survives to compute the next weekly fire — matching the ordinary-error
// continuity policy (no in-cycle retry; next Sunday is the retry). If recover
// were at the loop scope instead, a single panic would unwind the for and
// permanently disable the weekly report until process restart (issue #119).
func runJoinerReportSchedulerLoop(s joinerReportSession, guildID string, now func() time.Time) {
	for {
		fire := nextJoinerReportFire(now())
		utils.Info("Star Citizen joiner report scheduled",
			"next_fire_utc", fire.Format(time.RFC3339))
		time.Sleep(time.Until(fire))
		func() {
			defer utils.RecoverPanic("star-citizen-joiner-report")
			// Anchor the rolling window to the scheduled fire time, not the
			// wall clock at wakeup. Host suspend / GC delays would otherwise
			// drift the cutoff forward across cycles and silently drop
			// joiners who landed in the gap.
			if err := runJoinerReport(s, guildID, fire); err != nil {
				utils.CaptureError("Star Citizen joiner report failed", err,
					"guild_id", guildID, "fire_utc", fire.Format(time.RFC3339))
			}
		}()
	}
}
