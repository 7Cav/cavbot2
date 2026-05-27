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
	sparrowDiscordID   = "154035997187899392"
	starCitizenRoleID  = "1385946179174531223"
	joinerLookbackDays = 7

	joinerFireWeekday = time.Sunday
	joinerFireHourUTC = 4
	joinerFireMinUTC  = 20

	// guildMembersPageLimit is the per-page max accepted by Discord's
	// GET /guilds/{id}/members endpoint.
	guildMembersPageLimit = 1000
)

// joinerReportSession is the Discord REST surface the report uses. Narrow
// interface so the walker is testable without a live gateway; *discordgo.Session
// satisfies it.
type joinerReportSession interface {
	GuildMembers(guildID string, after string, limit int, options ...discordgo.RequestOption) ([]*discordgo.Member, error)
	UserChannelCreate(recipientID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	ChannelMessageSend(channelID string, content string, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

// nextJoinerReportFire returns the next Sunday 04:20 UTC strictly after now.
// Strict-after avoids a double-fire if the scheduler starts at the exact
// moment of a scheduled fire (or recomputes immediately after one).
func nextJoinerReportFire(now time.Time) time.Time {
	n := now.UTC()
	candidate := time.Date(n.Year(), n.Month(), n.Day(), joinerFireHourUTC, joinerFireMinUTC, 0, 0, time.UTC)
	daysUntilWeekday := (int(joinerFireWeekday) - int(candidate.Weekday()) + 7) % 7
	candidate = candidate.AddDate(0, 0, daysUntilWeekday)
	if !candidate.After(n) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate
}

// walkRecentJoinersWithRole paginates the full guild member list and returns
// the subset that joined within `lookback` of `now` and currently holds
// `roleID`. Results are sorted by JoinedAt ascending so the DM reads
// chronologically.
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
			// Page contained no valid members — can't advance the cursor
			// without risking an infinite loop, so stop here.
			break
		}
		after = lastSeenID
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
	lookback := time.Duration(joinerLookbackDays) * 24 * time.Hour
	matches, err := walkRecentJoinersWithRole(s, guildID, now, lookback, starCitizenRoleID)
	if err != nil {
		return fmt.Errorf("walk members: %w", err)
	}
	body := formatJoinerReport(matches, now.Add(-lookback))
	ch, err := s.UserChannelCreate(sparrowDiscordID)
	if err != nil {
		return fmt.Errorf("open DM: %w", err)
	}
	if _, err := s.ChannelMessageSend(ch.ID, body); err != nil {
		return fmt.Errorf("send DM: %w", err)
	}
	utils.Info("Star Citizen joiner report sent",
		"matches", len(matches),
		"week_of", now.Add(-lookback).UTC().Format("2006-01-02"),
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
func runJoinerReportSchedulerLoop(s joinerReportSession, guildID string, now func() time.Time) {
	defer utils.RecoverPanic("star-citizen-joiner-report")
	for {
		fire := nextJoinerReportFire(now())
		utils.Info("Star Citizen joiner report scheduled",
			"next_fire_utc", fire.Format(time.RFC3339))
		time.Sleep(time.Until(fire))
		if err := runJoinerReport(s, guildID, now()); err != nil {
			utils.CaptureError("Star Citizen joiner report failed", err)
		}
	}
}
