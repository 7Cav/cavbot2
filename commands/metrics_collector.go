package commands

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/7cav/cavbot2/metrics"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The collector of the Metrics page's rows (#949). It turns gateway events
// and a one-time backfill of message history into IDs and times in the bot
// store, never message text. Bots, webhooks, system messages, the AFK
// channel and self-deafened time don't count.

// MetricsStore is what the collector writes through (*store.Postgres).
type MetricsStore interface {
	StartMetrics(ctx context.Context, now time.Time) (metrics.State, error)
	MarkMetricsBackfilled(ctx context.Context, at time.Time) error
	RecordMessages(ctx context.Context, msgs []metrics.Message) error
	SetVoicePresence(ctx context.Context, vp metrics.VoicePresence, at time.Time) error
	SyncVoicePresence(ctx context.Context, present []metrics.VoicePresence, at time.Time) error
	RecordMemberEvent(ctx context.Context, e metrics.MemberEvent) error
	RollUpMetricWeeks(ctx context.Context, from, to time.Time, sections []metrics.Section) error
	PruneMetrics(ctx context.Context, before time.Time) error
}

// metricsStoreTimeout bounds one store write.
const metricsStoreTimeout = 5 * time.Second

// metricsNow is the collector's clock, and metricsAfterFunc runs its
// background work, package vars so tests run both by hand.
var (
	metricsNow       = time.Now
	metricsAfterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
)

// One Sentry issue per kind of failure, so a database outage files five
// issues and not one per event.
var (
	metricsMessageIssue  = utils.SentryIssue{Fingerprint: []string{"metrics-message"}}
	metricsVoiceIssue    = utils.SentryIssue{Fingerprint: []string{"metrics-voice"}}
	metricsMemberIssue   = utils.SentryIssue{Fingerprint: []string{"metrics-member"}}
	metricsBackfillIssue = utils.SentryIssue{Fingerprint: []string{"metrics-backfill"}}
	metricsJobIssue      = utils.SentryIssue{Fingerprint: []string{"metrics-job"}}
)

// MetricsCollector records the guild's activity for the Metrics page.
type MetricsCollector struct {
	ctx     context.Context
	cancel  context.CancelFunc
	state   *discordgo.State
	history messageHistory
	st      MetricsStore
	guildID string
	started metrics.State
	once    sync.Once
	// mu holds each voice read of the state through its write, so the
	// store gets every user's presence in the order the state held it.
	mu      sync.Mutex
	stopped atomic.Bool
}

// StartMetrics calls StartMetrics on the store and registers the gateway handlers.
// The first GUILD_CREATE with the guild available starts the backfill (when not yet done) and then the hourly job.
func StartMetrics(ctx context.Context, dg *discordgo.Session, st MetricsStore, guildID string) (*MetricsCollector, error) {
	c, err := newMetricsCollector(ctx, dg.State, dg, st, guildID)
	if err != nil {
		return nil, err
	}
	dg.AddHandler(func(_ *discordgo.Session, g *discordgo.GuildCreate) {
		defer utils.RecoverPanic("metrics-guild-create")
		c.handleGuildCreate(g)
	})
	dg.AddHandler(func(_ *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
		defer utils.RecoverPanic("metrics-voice-state")
		c.handleVoiceStateUpdate(vs)
	})
	dg.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		defer utils.RecoverPanic("metrics-message")
		c.handleMessageCreate(m)
	})
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.GuildMemberAdd) {
		defer utils.RecoverPanic("metrics-member-add")
		c.recordMember(e.Member, metrics.MemberJoined)
	})
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.GuildMemberRemove) {
		defer utils.RecoverPanic("metrics-member-remove")
		c.recordMember(e.Member, metrics.MemberLeft)
	})
	return c, nil
}

func newMetricsCollector(ctx context.Context, state *discordgo.State, history messageHistory, st MetricsStore, guildID string) (*MetricsCollector, error) {
	startCtx, cancelStart := context.WithTimeout(ctx, metricsStoreTimeout)
	defer cancelStart()
	started, err := st.StartMetrics(startCtx, metricsNow())
	if err != nil {
		return nil, err
	}
	c := &MetricsCollector{state: state, history: history, st: st, guildID: guildID, started: started}
	c.ctx, c.cancel = context.WithCancel(ctx)
	utils.Info("Collecting Discord metrics", "started_at", started.StartedAt, "backfilled", !started.BackfilledAt.IsZero())
	return c, nil
}

// Shutdown ends every open voice session now and drops later events.
func (c *MetricsCollector) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped.Store(true)
	c.cancel()
	storeWrite(metricsVoiceIssue, "Voice sessions not ended at shutdown", func(ctx context.Context) error {
		return c.st.SyncVoicePresence(ctx, nil, metricsNow())
	})
}

// handleGuildCreate makes the open voice sessions match the guild as it is
// now, since events in a gap between gateway sessions are lost. The first
// one with the guild available starts the history work.
func (c *MetricsCollector) handleGuildCreate(g *discordgo.GuildCreate) {
	if g.ID != c.guildID {
		return
	}
	c.mu.Lock()
	var present []metrics.VoicePresence
	available := c.withGuild(func(g *discordgo.Guild) { present = presentInVoice(g) })
	if available && !c.stopped.Load() {
		storeWrite(metricsVoiceIssue, "Voice sessions not synced", func(ctx context.Context) error {
			return c.st.SyncVoicePresence(ctx, present, metricsNow())
		})
	}
	c.mu.Unlock()
	if available {
		c.once.Do(func() { metricsAfterFunc(0, c.collectHistory) })
	}
}

func (c *MetricsCollector) handleVoiceStateUpdate(vs *discordgo.VoiceStateUpdate) {
	if vs == nil || vs.VoiceState == nil || vs.GuildID != c.guildID {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped.Load() {
		return
	}
	var (
		vp    metrics.VoicePresence
		human bool
	)
	if !c.withGuild(func(g *discordgo.Guild) { vp, human = voicePresence(g, vs.UserID, sectionRoleIDs(g)) }) || !human {
		return
	}
	storeWrite(metricsVoiceIssue, "Voice presence not recorded", func(ctx context.Context) error {
		return c.st.SetVoicePresence(ctx, vp, metricsNow())
	})
}

func (c *MetricsCollector) handleMessageCreate(m *discordgo.MessageCreate) {
	if m == nil || m.Message == nil || m.GuildID != c.guildID || c.stopped.Load() {
		return
	}
	row, ok := metricsMessage(m.Message)
	if !ok {
		return
	}
	found := false
	c.withGuild(func(g *discordgo.Guild) { row.CategoryID, found = categoryOf(g, m.ChannelID) })
	if !found {
		utils.Debug("Metrics message in a channel missing from state", "channel_id", m.ChannelID)
	}
	storeWrite(metricsMessageIssue, "Message not recorded", func(ctx context.Context) error {
		return c.st.RecordMessages(ctx, []metrics.Message{row})
	})
}

func (c *MetricsCollector) recordMember(m *discordgo.Member, kind metrics.MemberEventKind) {
	if m == nil || m.User == nil || m.User.Bot || m.GuildID != c.guildID || c.stopped.Load() {
		return
	}
	e := metrics.MemberEvent{UserID: m.User.ID, Kind: kind, At: metricsNow()}
	storeWrite(metricsMemberIssue, "Member event not recorded", func(ctx context.Context) error {
		return c.st.RecordMemberEvent(ctx, e)
	}, "kind", kind)
}

// storeWrite runs one store write under metricsStoreTimeout and captures
// its failure in issue.
func storeWrite(issue utils.SentryIssue, msg string, write func(context.Context) error, kv ...any) {
	ctx, cancel := context.WithTimeout(context.Background(), metricsStoreTimeout)
	defer cancel()
	if err := write(ctx); err != nil {
		utils.CaptureErrorIn(issue, msg, err, kv...)
	}
}

// withGuild runs read on the cached guild under the state's read lock, and
// reports false when the guild isn't cached. read must call no State
// method, since each takes the same lock, which Go forbids recursively.
func (c *MetricsCollector) withGuild(read func(g *discordgo.Guild)) bool {
	c.state.RLock()
	defer c.state.RUnlock()
	for _, g := range c.state.Guilds {
		if g.ID == c.guildID && !g.Unavailable {
			read(g)
			return true
		}
	}
	return false
}

// metricsMessage is the row m counts as, with no category yet, and false
// for a bot's, a webhook's or a system message.
func metricsMessage(m *discordgo.Message) (metrics.Message, bool) {
	if m.Author == nil || m.Author.Bot || m.WebhookID != "" ||
		(m.Type != discordgo.MessageTypeDefault && m.Type != discordgo.MessageTypeReply) {
		return metrics.Message{}, false
	}
	var addressed []string
	add := func(u *discordgo.User) {
		if u != nil && u.ID != m.Author.ID && !slices.Contains(addressed, u.ID) {
			addressed = append(addressed, u.ID)
		}
	}
	if m.ReferencedMessage != nil {
		add(m.ReferencedMessage.Author)
	}
	for _, u := range m.Mentions {
		add(u)
	}
	return metrics.Message{ID: m.ID, UserID: m.Author.ID, SentAt: m.Timestamp, Addressed: addressed}, true
}

// categoryOf is the category a message in channelID counts for: the
// channel's, or its parent channel's for a thread. It is false when the
// state lacks the channel.
func categoryOf(g *discordgo.Guild, channelID string) (string, bool) {
	ch := cachedChannel(g, channelID)
	if ch != nil && ch.IsThread() {
		ch = cachedChannel(g, ch.ParentID)
	}
	if ch == nil {
		return "", false
	}
	return ch.ParentID, true
}

func cachedChannel(g *discordgo.Guild, id string) *discordgo.Channel {
	for _, list := range [][]*discordgo.Channel{g.Channels, g.Threads} {
		for _, ch := range list {
			if ch.ID == id {
				return ch
			}
		}
	}
	return nil
}

// sectionRoleIDs is every role some section counts voice by.
func sectionRoleIDs(g *discordgo.Guild) map[string]bool {
	ids := map[string]bool{}
	for _, s := range metrics.Sections(g.Channels, g.Roles) {
		for _, id := range s.RoleIDs {
			ids[id] = true
		}
	}
	return ids
}

// voicePresence is where userID counts in voice in g, with the member's
// section roles, and false for a bot, which never counts.
func voicePresence(g *discordgo.Guild, userID string, sectionRoles map[string]bool) (metrics.VoicePresence, bool) {
	vp := metrics.VoicePresence{UserID: userID}
	var vs *discordgo.VoiceState
	for _, v := range g.VoiceStates {
		if v.UserID == userID {
			vs = v
			break
		}
	}
	var member *discordgo.Member
	for _, m := range g.Members {
		if m.User != nil && m.User.ID == userID {
			member = m
			break
		}
	}
	// A GUILD_CREATE's voice states carry no member, and an event's do.
	if member == nil && vs != nil {
		member = vs.Member
	}
	if member != nil && member.User != nil && member.User.Bot {
		return vp, false
	}
	if vs == nil || vs.ChannelID == "" || vs.ChannelID == g.AfkChannelID || vs.SelfDeaf {
		return vp, true
	}
	vp.ChannelID = vs.ChannelID
	if ch := cachedChannel(g, vs.ChannelID); ch != nil {
		vp.CategoryID = ch.ParentID
	}
	if member != nil {
		for _, r := range member.Roles {
			if sectionRoles[r] {
				vp.RoleIDs = append(vp.RoleIDs, r)
			}
		}
	}
	return vp, true
}

// presentInVoice is every human counted in voice in g.
func presentInVoice(g *discordgo.Guild) []metrics.VoicePresence {
	roles := sectionRoleIDs(g)
	present := []metrics.VoicePresence{}
	for _, vs := range g.VoiceStates {
		if vp, human := voicePresence(g, vs.UserID, roles); human && vp.ChannelID != "" {
			present = append(present, vp)
		}
	}
	return present
}
