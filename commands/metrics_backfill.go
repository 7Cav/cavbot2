package commands

import (
	"context"
	"time"

	"github.com/7cav/cavbot2/metrics"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The message backfill (#949) stores metrics.BackfillSpan of history before
// StartedAt. One goroutine reads a page at a time, and discordgo waits out
// Discord's rate limits. The store skips a message it already has, so a
// backfill a stop cut short reruns from the start on the next one.

// messageHistory is the message history the backfill reads
// (*discordgo.Session).
type messageHistory interface {
	ChannelMessages(channelID string, limit int, beforeID, afterID, aroundID string, options ...discordgo.RequestOption) ([]*discordgo.Message, error)
	GuildThreadsActive(guildID string, options ...discordgo.RequestOption) (*discordgo.ThreadsList, error)
	ThreadsArchived(channelID string, before *time.Time, limit int, options ...discordgo.RequestOption) (*discordgo.ThreadsList, error)
	ThreadsPrivateArchived(channelID string, before *time.Time, limit int, options ...discordgo.RequestOption) (*discordgo.ThreadsList, error)
}

// historyPage is the most messages or threads Discord gives in one page.
const historyPage = 100

// historyChannel is a channel or thread to backfill and the category its
// messages count for.
type historyChannel struct{ id, categoryID string }

// collectHistory backfills message history unless a backfill has
// finished, then starts the hourly job. A stop starts no job.
func (c *MetricsCollector) collectHistory() {
	if c.started.BackfilledAt.IsZero() {
		c.backfill()
	}
	if c.ctx.Err() != nil {
		return
	}
	c.runJob()
}

// backfill stores the history of every channel and thread, and marks the
// backfill finished unless a stop cut it short. A channel that fails is
// captured and skipped.
func (c *MetricsCollector) backfill() {
	defer utils.RecoverPanic("metrics-backfill")
	cutoff := c.started.StartedAt.Add(-metrics.BackfillSpan)
	utils.Info("Metrics backfill starting", "since", cutoff)
	channels := c.historyChannels(cutoff)
	stored := 0
	for _, ch := range channels {
		if c.ctx.Err() != nil {
			return
		}
		n, err := c.backfillChannel(ch, cutoff)
		stored += n
		if err != nil && c.ctx.Err() == nil {
			utils.CaptureErrorIn(metricsBackfillIssue, "Metrics backfill skipped a channel", err, "channel_id", ch.id)
		}
	}
	if c.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, metricsStoreTimeout)
	defer cancel()
	if err := c.st.MarkMetricsBackfilled(ctx, metricsNow()); err != nil {
		utils.CaptureErrorIn(metricsBackfillIssue, "Metrics backfill not marked finished", err)
		return
	}
	utils.Info("Metrics backfill finished", "channels", len(channels), "messages", stored)
}

// historyChannels is every channel with message history: the text, news,
// voice and stage channels in the state, and the active threads and the
// threads archived since cutoff. Forum and media channels hold threads
// only. A thread counts for its parent's category.
func (c *MetricsCollector) historyChannels(cutoff time.Time) []historyChannel {
	categories := map[string]string{}
	var channels []historyChannel
	var public, private []string
	c.withGuild(func(g *discordgo.Guild) {
		for _, ch := range g.Channels {
			categories[ch.ID] = ch.ParentID
			switch ch.Type {
			case discordgo.ChannelTypeGuildText:
				channels = append(channels, historyChannel{ch.ID, ch.ParentID})
				public = append(public, ch.ID)
				private = append(private, ch.ID)
			case discordgo.ChannelTypeGuildNews:
				channels = append(channels, historyChannel{ch.ID, ch.ParentID})
				public = append(public, ch.ID)
			case discordgo.ChannelTypeGuildVoice, discordgo.ChannelTypeGuildStageVoice:
				channels = append(channels, historyChannel{ch.ID, ch.ParentID})
			case discordgo.ChannelTypeGuildForum, discordgo.ChannelTypeGuildMedia:
				public = append(public, ch.ID)
			}
		}
	})

	seen := map[string]bool{}
	addThreads := func(threads []*discordgo.Channel) {
		for _, t := range threads {
			if !seen[t.ID] {
				seen[t.ID] = true
				channels = append(channels, historyChannel{t.ID, categories[t.ParentID]})
			}
		}
	}
	active, err := c.history.GuildThreadsActive(c.guildID, discordgo.WithContext(c.ctx))
	if err != nil {
		c.captureListing("Metrics backfill could not list active threads", err, c.guildID)
	} else {
		addThreads(active.Threads)
	}
	for _, id := range public {
		addThreads(c.archivedThreads(c.history.ThreadsArchived, id, cutoff))
	}
	for _, id := range private {
		addThreads(c.archivedThreads(c.history.ThreadsPrivateArchived, id, cutoff))
	}
	return channels
}

// archivedThreads pages one archive listing, newest archive time first,
// up to the first thread archived before cutoff, which holds no later
// message.
func (c *MetricsCollector) archivedThreads(list func(string, *time.Time, int, ...discordgo.RequestOption) (*discordgo.ThreadsList, error), channelID string, cutoff time.Time) []*discordgo.Channel {
	var threads []*discordgo.Channel
	var before *time.Time
	for c.ctx.Err() == nil {
		page, err := list(channelID, before, historyPage, discordgo.WithContext(c.ctx))
		if err != nil {
			c.captureListing("Metrics backfill could not list archived threads", err, channelID)
			return threads
		}
		for _, t := range page.Threads {
			if t.ThreadMetadata == nil || t.ThreadMetadata.ArchiveTimestamp.Before(cutoff) {
				return threads
			}
			threads = append(threads, t)
		}
		if !page.HasMore || len(page.Threads) == 0 {
			return threads
		}
		last := page.Threads[len(page.Threads)-1].ThreadMetadata.ArchiveTimestamp
		before = &last
	}
	return threads
}

// captureListing captures a thread listing that failed, unless a stop
// cancelled it.
func (c *MetricsCollector) captureListing(msg string, err error, channelID string) {
	if c.ctx.Err() == nil {
		utils.CaptureErrorIn(metricsBackfillIssue, msg, err, "channel_id", channelID)
	}
}

// backfillChannel stores the channel's messages sent since cutoff, one
// page at a time from the newest, and returns how many it stored.
func (c *MetricsCollector) backfillChannel(ch historyChannel, cutoff time.Time) (int, error) {
	stored := 0
	before := ""
	for {
		page, err := c.history.ChannelMessages(ch.id, historyPage, before, "", "", discordgo.WithContext(c.ctx))
		if err != nil {
			return stored, err
		}
		done := len(page) < historyPage
		var rows []metrics.Message
		for _, m := range page {
			if m.Timestamp.Before(cutoff) {
				done = true
				break
			}
			if row, ok := metricsMessage(m); ok {
				row.CategoryID = ch.categoryID
				rows = append(rows, row)
			}
		}
		if len(rows) > 0 {
			ctx, cancel := context.WithTimeout(c.ctx, metricsStoreTimeout)
			err := c.st.RecordMessages(ctx, rows)
			cancel()
			if err != nil {
				return stored, err
			}
			stored += len(rows)
		}
		if done {
			return stored, nil
		}
		before = page[len(page)-1].ID
	}
}
