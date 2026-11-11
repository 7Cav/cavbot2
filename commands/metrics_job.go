package commands

import (
	"context"
	"time"

	"github.com/7cav/cavbot2/metrics"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

const (
	metricsJobInterval = time.Hour
	// metricsJobTimeout bounds one pass, whose rollup takes about a second.
	metricsJobTimeout = time.Minute
)

// runJob prunes raw rows past metrics.RawRetention, then recomputes every
// complete week whose raw rows are all kept, which heals a late backfill, and
// leaves a pruned week's totals as they were. The next pass is scheduled
// even after a panic. A stopped collector runs none.
func (c *MetricsCollector) runJob() {
	if c.stopped.Load() {
		return
	}
	defer metricsAfterFunc(metricsJobInterval, c.runJob)
	defer utils.RecoverPanic("metrics-job")
	now := metricsNow()
	cutoff := now.Add(-metrics.RawRetention)
	ctx, cancel := context.WithTimeout(c.ctx, metricsJobTimeout)
	defer cancel()
	if err := c.st.PruneMetrics(ctx, cutoff); err != nil {
		utils.CaptureErrorIn(metricsJobIssue, "Metrics not pruned", err)
	}
	var sections []metrics.Section
	if !c.withGuild(func(g *discordgo.Guild) { sections = metrics.Sections(g.Channels, g.Roles) }) {
		return
	}
	if err := c.st.RollUpMetricWeeks(ctx, firstMondayOnOrAfter(cutoff), metrics.WeekStart(now), sections); err != nil {
		utils.CaptureErrorIn(metricsJobIssue, "Metric weeks not rolled up", err)
	}
}

func firstMondayOnOrAfter(t time.Time) time.Time {
	monday := metrics.WeekStart(t)
	if monday.Before(t) {
		return monday.AddDate(0, 0, 7)
	}
	return monday
}
