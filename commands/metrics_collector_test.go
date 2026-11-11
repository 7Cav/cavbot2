package commands

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/metrics"
	"github.com/bwmarrin/discordgo"
)

const testMetricsGuild = "metrics-guild"

// metricsTestNow is a Wednesday.
var metricsTestNow = time.Date(2026, 11, 11, 15, 30, 0, 0, time.UTC)

// fakeMetricsStore keeps what the collector's writes leave behind: each
// user's open voice presence, the stored messages and the member events.
type fakeMetricsStore struct {
	mu           sync.Mutex
	state        metrics.State
	open         map[string]metrics.VoicePresence
	messages     map[string]metrics.Message
	members      []metrics.MemberEvent
	backfilledAt time.Time
	prunedBefore time.Time
	rollUp       [2]time.Time
	rolledUp     []metrics.Section
	panicPrune   bool
}

func newFakeMetricsStore() *fakeMetricsStore {
	return &fakeMetricsStore{open: map[string]metrics.VoicePresence{}, messages: map[string]metrics.Message{}}
}

func (f *fakeMetricsStore) StartMetrics(_ context.Context, now time.Time) (metrics.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.StartedAt.IsZero() {
		f.state.StartedAt = now
	}
	return f.state, nil
}

func (f *fakeMetricsStore) MarkMetricsBackfilled(_ context.Context, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.backfilledAt = at
	return nil
}

func (f *fakeMetricsStore) RecordMessages(_ context.Context, msgs []metrics.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range msgs {
		f.messages[m.ID] = m
	}
	return nil
}

func (f *fakeMetricsStore) SetVoicePresence(_ context.Context, vp metrics.VoicePresence, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vp.ChannelID == "" {
		delete(f.open, vp.UserID)
	} else {
		f.open[vp.UserID] = vp
	}
	return nil
}

func (f *fakeMetricsStore) SyncVoicePresence(_ context.Context, present []metrics.VoicePresence, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open = map[string]metrics.VoicePresence{}
	for _, vp := range present {
		f.open[vp.UserID] = vp
	}
	return nil
}

func (f *fakeMetricsStore) RecordMemberEvent(_ context.Context, e metrics.MemberEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = append(f.members, e)
	return nil
}

func (f *fakeMetricsStore) RollUpMetricWeeks(_ context.Context, from, to time.Time, sections []metrics.Section) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollUp = [2]time.Time{from, to}
	f.rolledUp = sections
	return nil
}

func (f *fakeMetricsStore) PruneMetrics(_ context.Context, before time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicPrune {
		panic("prune")
	}
	f.prunedBefore = before
	return nil
}

// metricsTestGuild is a guild with a company category, whose voice counts
// by the A/1-7 role, an Operations category, an AFK channel, a thread and
// a human and a bot member.
func metricsTestGuild() *discordgo.Guild {
	return &discordgo.Guild{
		ID:           testMetricsGuild,
		AfkChannelID: "afk",
		Channels: []*discordgo.Channel{
			{ID: "cat-a", Name: "▬ A/1-7 ▬", Type: discordgo.ChannelTypeGuildCategory},
			{ID: "cat-ops", Name: "Operations", Type: discordgo.ChannelTypeGuildCategory, Position: 1},
			{ID: "general", ParentID: "cat-a", Type: discordgo.ChannelTypeGuildText},
			{ID: "vc-a", ParentID: "cat-a", Type: discordgo.ChannelTypeGuildVoice},
			{ID: "vc-ops", ParentID: "cat-ops", Type: discordgo.ChannelTypeGuildVoice},
			{ID: "afk", ParentID: "cat-ops", Type: discordgo.ChannelTypeGuildVoice},
			{ID: "loose", Type: discordgo.ChannelTypeGuildText},
		},
		Threads: []*discordgo.Channel{
			{ID: "thread", ParentID: "general", Type: discordgo.ChannelTypeGuildPublicThread},
		},
		Roles: []*discordgo.Role{
			{ID: "role-a", Name: "A/1-7"},
			{ID: "role-member", Name: "Member"},
		},
		Members: []*discordgo.Member{
			{GuildID: testMetricsGuild, User: &discordgo.User{ID: "human"}, Roles: []string{"role-member", "role-a"}},
			{GuildID: testMetricsGuild, User: &discordgo.User{ID: "bot", Bot: true}},
		},
	}
}

// newTestMetricsCollector builds a collector over a state holding guild,
// on the fake clock. Work it schedules lands in the returned slice.
func newTestMetricsCollector(t *testing.T, guild *discordgo.Guild, st MetricsStore, history messageHistory) (*MetricsCollector, *discordgo.State, *[]func()) {
	t.Helper()
	prevNow, prevAfter := metricsNow, metricsAfterFunc
	t.Cleanup(func() { metricsNow, metricsAfterFunc = prevNow, prevAfter })
	metricsNow = func() time.Time { return metricsTestNow }
	scheduled := &[]func(){}
	metricsAfterFunc = func(_ time.Duration, f func()) { *scheduled = append(*scheduled, f) }

	state := discordgo.NewState()
	if err := state.GuildAdd(guild); err != nil {
		t.Fatal(err)
	}
	c, err := newMetricsCollector(context.Background(), state, history, st, testMetricsGuild)
	if err != nil {
		t.Fatal(err)
	}
	return c, state, scheduled
}

// dispatchVoice applies a voice event to the state, as discordgo does
// before any handler runs, then hands it to the collector.
func dispatchVoice(t *testing.T, c *MetricsCollector, state *discordgo.State, vs *discordgo.VoiceState) {
	t.Helper()
	vs.GuildID = testMetricsGuild
	ev := &discordgo.VoiceStateUpdate{VoiceState: vs}
	if err := state.OnInterface(&discordgo.Session{StateEnabled: true}, ev); err != nil {
		t.Fatal(err)
	}
	c.handleVoiceStateUpdate(ev)
}

func TestMetricsMessageFilter(t *testing.T) {
	human := &discordgo.User{ID: "human"}
	cases := []struct {
		name      string
		msg       discordgo.Message
		kept      bool
		category  string
		addressed []string
	}{
		{name: "default message", msg: discordgo.Message{ChannelID: "general", Author: human}, kept: true, category: "cat-a"},
		{
			name: "reply addresses its target and mentions, author dropped",
			msg: discordgo.Message{
				ChannelID: "general", Author: human, Type: discordgo.MessageTypeReply,
				ReferencedMessage: &discordgo.Message{Author: &discordgo.User{ID: "target"}},
				Mentions:          []*discordgo.User{{ID: "target"}, human, {ID: "other"}},
			},
			kept: true, category: "cat-a", addressed: []string{"target", "other"},
		},
		{name: "thread counts for its parent's category", msg: discordgo.Message{ChannelID: "thread", Author: human}, kept: true, category: "cat-a"},
		{name: "channel without a category", msg: discordgo.Message{ChannelID: "loose", Author: human}, kept: true},
		{name: "channel missing from state", msg: discordgo.Message{ChannelID: "unknown", Author: human}, kept: true},
		{name: "bot", msg: discordgo.Message{ChannelID: "general", Author: &discordgo.User{ID: "bot", Bot: true}}},
		{name: "webhook", msg: discordgo.Message{ChannelID: "general", Author: human, WebhookID: "hook"}},
		{name: "system message", msg: discordgo.Message{ChannelID: "general", Author: human, Type: discordgo.MessageTypeGuildMemberJoin}},
		{name: "other guild", msg: discordgo.Message{ChannelID: "general", Author: human, GuildID: "elsewhere"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeMetricsStore()
			c, _, _ := newTestMetricsCollector(t, metricsTestGuild(), st, nil)
			msg := tc.msg
			msg.ID = "m1"
			msg.Timestamp = metricsTestNow
			if msg.GuildID == "" {
				msg.GuildID = testMetricsGuild
			}
			c.handleMessageCreate(&discordgo.MessageCreate{Message: &msg})

			got, kept := st.messages["m1"]
			if kept != tc.kept {
				t.Fatalf("kept = %v, want %v", kept, tc.kept)
			}
			if !kept {
				return
			}
			if got.UserID != "human" || got.CategoryID != tc.category || !got.SentAt.Equal(metricsTestNow) {
				t.Errorf("row = %+v, want user human, category %q, sent at %v", got, tc.category, metricsTestNow)
			}
			if !slices.Equal(got.Addressed, tc.addressed) {
				t.Errorf("addressed = %v, want %v", got.Addressed, tc.addressed)
			}
		})
	}
}

func TestMetricsVoicePresence(t *testing.T) {
	cases := []struct {
		name string
		vs   discordgo.VoiceState
		// open is the presence the store holds for the user after the
		// event, nil for none.
		open *metrics.VoicePresence
	}{
		{
			name: "company member snapshots only section roles",
			vs:   discordgo.VoiceState{UserID: "human", ChannelID: "vc-ops"},
			open: &metrics.VoicePresence{UserID: "human", ChannelID: "vc-ops", CategoryID: "cat-ops", RoleIDs: []string{"role-a"}},
		},
		{
			name: "mute keeps the user counted",
			vs:   discordgo.VoiceState{UserID: "human", ChannelID: "vc-a", SelfMute: true, Mute: true, Deaf: true},
			open: &metrics.VoicePresence{UserID: "human", ChannelID: "vc-a", CategoryID: "cat-a", RoleIDs: []string{"role-a"}},
		},
		{
			name: "member missing from the cache counts with the event's member",
			vs: discordgo.VoiceState{UserID: "newcomer", ChannelID: "vc-a", Member: &discordgo.Member{
				User: &discordgo.User{ID: "newcomer"}, Roles: []string{"role-a", "role-member"},
			}},
			open: &metrics.VoicePresence{UserID: "newcomer", ChannelID: "vc-a", CategoryID: "cat-a", RoleIDs: []string{"role-a"}},
		},
		{name: "self-deafened", vs: discordgo.VoiceState{UserID: "human", ChannelID: "vc-a", SelfDeaf: true}},
		{name: "AFK channel", vs: discordgo.VoiceState{UserID: "human", ChannelID: "afk"}},
		{name: "left", vs: discordgo.VoiceState{UserID: "human"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeMetricsStore()
			c, state, _ := newTestMetricsCollector(t, metricsTestGuild(), st, nil)
			// The user starts in voice, so each case shows what the event
			// does to a counted presence.
			dispatchVoice(t, c, state, &discordgo.VoiceState{UserID: tc.vs.UserID, ChannelID: "vc-a"})
			vs := tc.vs
			dispatchVoice(t, c, state, &vs)

			got, open := st.open[tc.vs.UserID]
			switch {
			case tc.open == nil && open:
				t.Errorf("open presence = %+v, want none", got)
			case tc.open != nil && !open:
				t.Errorf("no open presence, want %+v", *tc.open)
			case tc.open != nil && (got.ChannelID != tc.open.ChannelID || got.CategoryID != tc.open.CategoryID || !slices.Equal(got.RoleIDs, tc.open.RoleIDs)):
				t.Errorf("open presence = %+v, want %+v", got, *tc.open)
			}
		})
	}
}

func TestMetricsVoiceIgnoresBots(t *testing.T) {
	st := newFakeMetricsStore()
	c, state, _ := newTestMetricsCollector(t, metricsTestGuild(), st, nil)
	dispatchVoice(t, c, state, &discordgo.VoiceState{UserID: "bot", ChannelID: "vc-a"})
	dispatchVoice(t, c, state, &discordgo.VoiceState{UserID: "recorder", ChannelID: "vc-a", Member: &discordgo.Member{
		User: &discordgo.User{ID: "recorder", Bot: true},
	}})
	if len(st.open) != 0 {
		t.Errorf("open presences = %v, want none", st.open)
	}
}

func TestMetricsGuildCreateSyncsVoice(t *testing.T) {
	st := newFakeMetricsStore()
	// A session left open by the last gateway session, for a user no
	// longer in voice.
	st.open["gone"] = metrics.VoicePresence{UserID: "gone", ChannelID: "vc-a"}
	guild := metricsTestGuild()
	guild.VoiceStates = []*discordgo.VoiceState{
		{UserID: "human", ChannelID: "vc-a"},
		{UserID: "bot", ChannelID: "vc-a"},
		{UserID: "deaf", ChannelID: "vc-a", SelfDeaf: true},
		{UserID: "idle", ChannelID: "afk"},
	}
	c, _, scheduled := newTestMetricsCollector(t, guild, st, nil)

	c.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{ID: testMetricsGuild}})
	c.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{ID: testMetricsGuild}})

	if got := st.open; len(got) != 1 || got["human"].ChannelID != "vc-a" || !slices.Equal(got["human"].RoleIDs, []string{"role-a"}) {
		t.Errorf("open presences = %v, want only human in vc-a with role-a", got)
	}
	if len(*scheduled) != 1 {
		t.Errorf("history work scheduled %d times, want once", len(*scheduled))
	}
}

func TestMetricsShutdownEndsVoiceAndDropsLaterEvents(t *testing.T) {
	st := newFakeMetricsStore()
	history := &fakeHistory{messages: map[string][]*discordgo.Message{"general": {historyMessage("earlier", metricsTestNow.Add(-time.Hour))}}}
	c, state, scheduled := newTestMetricsCollector(t, metricsTestGuild(), st, history)
	dispatchVoice(t, c, state, &discordgo.VoiceState{UserID: "human", ChannelID: "vc-a"})

	c.Shutdown()
	if len(st.open) != 0 {
		t.Fatalf("open presences after shutdown = %v, want none", st.open)
	}

	dispatchVoice(t, c, state, &discordgo.VoiceState{UserID: "human", ChannelID: "vc-ops"})
	c.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{ID: testMetricsGuild}})
	c.handleMessageCreate(&discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "m1", GuildID: testMetricsGuild, ChannelID: "general", Author: &discordgo.User{ID: "human"},
	}})
	c.recordMember(&discordgo.Member{GuildID: testMetricsGuild, User: &discordgo.User{ID: "human"}}, metrics.MemberJoined)
	c.runJob()
	if len(st.open) != 0 || len(st.messages) != 0 || len(st.members) != 0 || !st.prunedBefore.IsZero() {
		t.Errorf("after shutdown: open %v, messages %v, members %v, pruned before %v; want nothing recorded",
			st.open, st.messages, st.members, st.prunedBefore)
	}
	// The GUILD_CREATE still schedules the history work, which a stop
	// keeps from running.
	for _, f := range *scheduled {
		f()
	}
	if len(st.messages) != 0 || !st.backfilledAt.IsZero() {
		t.Errorf("history work ran after shutdown")
	}
}

func TestMetricsMemberEventsSkipBots(t *testing.T) {
	st := newFakeMetricsStore()
	c, _, _ := newTestMetricsCollector(t, metricsTestGuild(), st, nil)
	human := &discordgo.User{ID: "human"}
	c.recordMember(&discordgo.Member{GuildID: testMetricsGuild, User: human}, metrics.MemberJoined)
	c.recordMember(&discordgo.Member{GuildID: testMetricsGuild, User: &discordgo.User{ID: "bot", Bot: true}}, metrics.MemberJoined)
	c.recordMember(&discordgo.Member{GuildID: "elsewhere", User: human}, metrics.MemberJoined)
	c.recordMember(&discordgo.Member{GuildID: testMetricsGuild, User: human}, metrics.MemberLeft)

	want := []metrics.MemberEvent{
		{UserID: "human", Kind: metrics.MemberJoined, At: metricsTestNow},
		{UserID: "human", Kind: metrics.MemberLeft, At: metricsTestNow},
	}
	if !slices.Equal(st.members, want) {
		t.Errorf("member events = %v, want %v", st.members, want)
	}
}

var errMetricsStoreDown = errors.New("store down")

// downMetricsStore fails every write, as in a database outage.
type downMetricsStore struct{ *fakeMetricsStore }

func (downMetricsStore) MarkMetricsBackfilled(context.Context, time.Time) error {
	return errMetricsStoreDown
}
func (downMetricsStore) RecordMessages(context.Context, []metrics.Message) error {
	return errMetricsStoreDown
}
func (downMetricsStore) SetVoicePresence(context.Context, metrics.VoicePresence, time.Time) error {
	return errMetricsStoreDown
}
func (downMetricsStore) SyncVoicePresence(context.Context, []metrics.VoicePresence, time.Time) error {
	return errMetricsStoreDown
}
func (downMetricsStore) RecordMemberEvent(context.Context, metrics.MemberEvent) error {
	return errMetricsStoreDown
}
func (downMetricsStore) RollUpMetricWeeks(context.Context, time.Time, time.Time, []metrics.Section) error {
	return errMetricsStoreDown
}
func (downMetricsStore) PruneMetrics(context.Context, time.Time) error { return errMetricsStoreDown }

func TestMetricsStoreOutageFilesOneIssuePerKind(t *testing.T) {
	events := recordSentry(t)
	history := &fakeHistory{messages: map[string][]*discordgo.Message{"general": {historyMessage("earlier", metricsTestNow.Add(-time.Hour))}}}
	c, state, _ := newTestMetricsCollector(t, metricsTestGuild(), downMetricsStore{newFakeMetricsStore()}, history)

	for range 2 {
		c.handleGuildCreate(&discordgo.GuildCreate{Guild: &discordgo.Guild{ID: testMetricsGuild}})
		dispatchVoice(t, c, state, &discordgo.VoiceState{UserID: "human", ChannelID: "vc-a"})
		c.handleMessageCreate(&discordgo.MessageCreate{Message: &discordgo.Message{
			ID: "m1", GuildID: testMetricsGuild, ChannelID: "general", Author: &discordgo.User{ID: "human"},
		}})
		c.recordMember(&discordgo.Member{GuildID: testMetricsGuild, User: &discordgo.User{ID: "human"}}, metrics.MemberJoined)
		c.collectHistory()
	}

	issues := map[string]int{}
	for _, e := range events.Events() {
		if len(e.Fingerprint) != 1 {
			t.Fatalf("event %q has fingerprint %v, want one kind", e.Message, e.Fingerprint)
		}
		issues[e.Fingerprint[0]]++
	}
	want := []string{"metrics-backfill", "metrics-job", "metrics-member", "metrics-message", "metrics-voice"}
	if got := slices.Sorted(maps.Keys(issues)); !slices.Equal(got, want) {
		t.Errorf("Sentry issues = %v, want %v", got, want)
	}
}
