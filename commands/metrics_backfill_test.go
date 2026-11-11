package commands

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// fakeHistory serves message history the way Discord pages it: messages
// newest first per channel, and archived threads newest archive time
// first per channel.
type fakeHistory struct {
	messages map[string][]*discordgo.Message
	active   []*discordgo.Channel
	public   map[string][]*discordgo.Channel
	private  map[string][]*discordgo.Channel
	fail     map[string]bool
	// read lists every channel whose messages were read, once per page.
	read []string
}

func (h *fakeHistory) ChannelMessages(channelID string, limit int, beforeID, _, _ string, _ ...discordgo.RequestOption) ([]*discordgo.Message, error) {
	h.read = append(h.read, channelID)
	if h.fail[channelID] {
		return nil, errors.New("missing access")
	}
	msgs := h.messages[channelID]
	if beforeID != "" {
		i := slices.IndexFunc(msgs, func(m *discordgo.Message) bool { return m.ID == beforeID })
		msgs = msgs[i+1:]
	}
	return msgs[:min(limit, len(msgs))], nil
}

func (h *fakeHistory) GuildThreadsActive(string, ...discordgo.RequestOption) (*discordgo.ThreadsList, error) {
	return &discordgo.ThreadsList{Threads: h.active}, nil
}

func (h *fakeHistory) ThreadsArchived(channelID string, before *time.Time, limit int, _ ...discordgo.RequestOption) (*discordgo.ThreadsList, error) {
	return archivedPage(h.public[channelID], before, limit), nil
}

func (h *fakeHistory) ThreadsPrivateArchived(channelID string, before *time.Time, limit int, _ ...discordgo.RequestOption) (*discordgo.ThreadsList, error) {
	return archivedPage(h.private[channelID], before, limit), nil
}

func archivedPage(threads []*discordgo.Channel, before *time.Time, limit int) *discordgo.ThreadsList {
	if before != nil {
		i := slices.IndexFunc(threads, func(t *discordgo.Channel) bool { return t.ThreadMetadata.ArchiveTimestamp.Before(*before) })
		if i < 0 {
			i = len(threads)
		}
		threads = threads[i:]
	}
	n := min(limit, len(threads))
	return &discordgo.ThreadsList{Threads: threads[:n], HasMore: n < len(threads)}
}

func historyMessage(id string, at time.Time) *discordgo.Message {
	return &discordgo.Message{ID: id, Author: &discordgo.User{ID: "human"}, Timestamp: at}
}

func archivedThread(id, parentID string, archivedAt time.Time) *discordgo.Channel {
	return &discordgo.Channel{ID: id, ParentID: parentID, Type: discordgo.ChannelTypeGuildPublicThread,
		ThreadMetadata: &discordgo.ThreadMetadata{Archived: true, ArchiveTimestamp: archivedAt}}
}

func TestMetricsBackfill(t *testing.T) {
	cutoff := metricsTestNow.Add(-12 * 7 * 24 * time.Hour)
	after := cutoff.Add(time.Hour)

	// general holds 150 messages since the cutoff, a bot's among them, then
	// 100 older: the second page reaches the cutoff and no third is read.
	var general []*discordgo.Message
	for i := range 250 {
		at := after.Add(time.Duration(150-i) * time.Minute)
		if i >= 150 {
			at = cutoff.Add(-time.Duration(i) * time.Minute)
		}
		general = append(general, historyMessage(fmt.Sprintf("general-%d", i), at))
	}
	general[3].Author = &discordgo.User{ID: "bot", Bot: true}

	// news has 101 archived threads since the cutoff, two pages of them,
	// then one archived before it.
	var newsArchive []*discordgo.Channel
	for i := range 101 {
		newsArchive = append(newsArchive, archivedThread(fmt.Sprintf("news-thread-%d", i), "news", after.Add(-time.Duration(i)*time.Second)))
	}
	newsArchive = append(newsArchive, archivedThread("news-old", "news", cutoff.Add(-time.Hour)))

	guild := metricsTestGuild()
	guild.Channels = append(guild.Channels,
		&discordgo.Channel{ID: "news", ParentID: "cat-ops", Type: discordgo.ChannelTypeGuildNews},
		&discordgo.Channel{ID: "forum", ParentID: "cat-ops", Type: discordgo.ChannelTypeGuildForum},
	)
	history := &fakeHistory{
		messages: map[string][]*discordgo.Message{
			"general":         general,
			"vc-a":            {historyMessage("voice-chat", after)},
			"thread":          {historyMessage("active-thread", after)},
			"archived":        {historyMessage("archived-thread", after)},
			"private":         {historyMessage("private-thread", after)},
			"forum-post":      {historyMessage("forum-post", after)},
			"news-thread-100": {historyMessage("second-page-thread", after)},
			"vc-ops":          {historyMessage("after-failure", after)},
		},
		active:  []*discordgo.Channel{{ID: "thread", ParentID: "general", Type: discordgo.ChannelTypeGuildPublicThread}},
		public:  map[string][]*discordgo.Channel{"general": {archivedThread("archived", "general", after)}, "news": newsArchive, "forum": {archivedThread("forum-post", "forum", after)}},
		private: map[string][]*discordgo.Channel{"general": {archivedThread("private", "general", after)}},
		fail:    map[string]bool{"loose": true},
	}
	st := newFakeMetricsStore()
	c, _, _ := newTestMetricsCollector(t, guild, st, history)

	c.collectHistory()

	for i := range 250 {
		id := fmt.Sprintf("general-%d", i)
		if _, stored := st.messages[id]; stored != (i < 150 && i != 3) {
			t.Errorf("%s stored = %v", id, stored)
		}
	}
	for id, category := range map[string]string{
		"voice-chat":         "cat-a",
		"active-thread":      "cat-a",
		"archived-thread":    "cat-a",
		"private-thread":     "cat-a",
		"forum-post":         "cat-ops",
		"second-page-thread": "cat-ops",
		"after-failure":      "cat-ops",
	} {
		if got, ok := st.messages[id]; !ok || got.CategoryID != category {
			t.Errorf("%s = %+v (stored %v), want category %s", id, got, ok, category)
		}
	}
	for _, unread := range []string{"news-old", "forum"} {
		if slices.Contains(history.read, unread) {
			t.Errorf("read messages of %s", unread)
		}
	}
	if n := countOf(history.read, "general"); n != 2 {
		t.Errorf("read %d pages of general, want 2", n)
	}
	if !st.backfilledAt.Equal(metricsTestNow) {
		t.Errorf("backfilled at %v, want %v", st.backfilledAt, metricsTestNow)
	}
	if st.prunedBefore.IsZero() {
		t.Error("hourly job did not start after the backfill")
	}
}

func TestMetricsBackfillRunsOnce(t *testing.T) {
	st := newFakeMetricsStore()
	st.state.StartedAt = metricsTestNow.Add(-30 * 24 * time.Hour)
	st.state.BackfilledAt = st.state.StartedAt
	history := &fakeHistory{messages: map[string][]*discordgo.Message{"general": {historyMessage("m1", metricsTestNow)}}}
	c, _, _ := newTestMetricsCollector(t, metricsTestGuild(), st, history)

	c.collectHistory()

	if len(history.read) != 0 || len(st.messages) != 0 {
		t.Errorf("backfilled guild read %v and stored %v, want nothing", history.read, st.messages)
	}
	if st.prunedBefore.IsZero() {
		t.Error("hourly job did not start")
	}
}

func countOf(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}
