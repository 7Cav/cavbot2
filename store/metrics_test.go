package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/7cav/cavbot2/metrics"
)

// The Discord metrics tables (#949) live on *Postgres alone, so these cases
// skip without a test database.

// metricsBase is a Monday, the start of the week most cases use.
var metricsBase = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

func openMetricsStore(t *testing.T) *Postgres {
	t.Helper()
	return openTestPostgres(t).(*Postgres)
}

// texts runs a query that returns one text column.
func texts(t *testing.T, p *Postgres, query string, args ...any) []string {
	t.Helper()
	got, err := queryAll(context.Background(), p.db, func(row scanner) (string, error) {
		var s string
		err := row.Scan(&s)
		return s, err
	}, query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return got
}

// voiceSessions lists every session as "user channel start-end", with times
// as offsets from metricsBase and "open" for no end.
func voiceSessions(t *testing.T, p *Postgres) []string {
	t.Helper()
	return texts(t, p, `
		SELECT format('%s %s %s-%s', user_id, channel_id, started_at - $1, COALESCE((ended_at - $1)::text, 'open'))
		FROM metric_voice_sessions ORDER BY user_id, started_at`, metricsBase)
}

// openVoice sets a member's presence, and voiceSession records one closed
// session from start to end.
func openVoice(t *testing.T, p *Postgres, vp metrics.VoicePresence, at time.Time) {
	t.Helper()
	if err := p.SetVoicePresence(context.Background(), vp, at); err != nil {
		t.Fatalf("SetVoicePresence: %v", err)
	}
}

func voiceSession(t *testing.T, p *Postgres, vp metrics.VoicePresence, start, end time.Time) {
	t.Helper()
	openVoice(t, p, vp, start)
	openVoice(t, p, metrics.VoicePresence{UserID: vp.UserID}, end)
}

func recordMessages(t *testing.T, p *Postgres, msgs ...metrics.Message) {
	t.Helper()
	if err := p.RecordMessages(context.Background(), msgs); err != nil {
		t.Fatalf("RecordMessages: %v", err)
	}
}

func recordMemberEvent(t *testing.T, p *Postgres, e metrics.MemberEvent) {
	t.Helper()
	if err := p.RecordMemberEvent(context.Background(), e); err != nil {
		t.Fatalf("RecordMemberEvent: %v", err)
	}
}

// StartMetrics keeps the first start, and the backfill mark reads back.
func TestMetricsStateKeepsTheFirstStart(t *testing.T) {
	p := openMetricsStore(t)
	ctx := context.Background()
	if got, err := p.MetricsState(ctx); err != nil || got != (metrics.State{}) {
		t.Fatalf("MetricsState before a start = %+v, %v, want the zero state", got, err)
	}
	first := metricsBase
	if _, err := p.StartMetrics(ctx, first); err != nil {
		t.Fatalf("StartMetrics: %v", err)
	}
	got, err := p.StartMetrics(ctx, first.Add(time.Hour))
	if err != nil || !got.StartedAt.Equal(first) || !got.BackfilledAt.IsZero() {
		t.Fatalf("second StartMetrics = %+v, %v, want started at %v and not backfilled", got, err, first)
	}
	backfilled := first.Add(20 * time.Minute)
	if err := p.MarkMetricsBackfilled(ctx, backfilled); err != nil {
		t.Fatalf("MarkMetricsBackfilled: %v", err)
	}
	got, err = p.MetricsState(ctx)
	if err != nil || !got.StartedAt.Equal(first) || !got.BackfilledAt.Equal(backfilled) {
		t.Errorf("MetricsState = %+v, %v, want started at %v and backfilled at %v", got, err, first, backfilled)
	}
}

// A message recorded twice, as when the backfill meets live collection,
// is stored once, as first recorded.
func TestRecordMessagesSkipsAMessageAlreadyStored(t *testing.T) {
	p := openMetricsStore(t)
	recordMessages(t, p, metrics.Message{ID: "m1", UserID: "u1", SentAt: metricsBase})
	recordMessages(t, p,
		metrics.Message{ID: "m1", UserID: "u1", SentAt: metricsBase, Addressed: []string{"u2"}},
		metrics.Message{ID: "m2", UserID: "u2", SentAt: metricsBase})
	got := texts(t, p, `SELECT message_id || ' ' || cardinality(addressed_ids) FROM metric_messages ORDER BY 1`)
	if want := []string{"m1 0", "m2 0"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

func TestSetVoicePresence(t *testing.T) {
	ch1 := metrics.VoicePresence{UserID: "u", ChannelID: "ch1"}
	ch2 := metrics.VoicePresence{UserID: "u", ChannelID: "ch2"}
	gone := metrics.VoicePresence{UserID: "u"}
	type step struct {
		vp metrics.VoicePresence
		at time.Duration
	}
	tests := []struct {
		name  string
		steps []step
		want  []string
	}{
		{"a mute toggle in the same channel keeps the session",
			[]step{{ch1, 0}, {ch1, 10 * time.Minute}}, []string{"u ch1 00:00:00-open"}},
		{"a move ends one session and opens the next",
			[]step{{ch1, 0}, {ch2, 10 * time.Minute}}, []string{"u ch1 00:00:00-00:10:00", "u ch2 00:10:00-open"}},
		{"leaving ends the session",
			[]step{{ch1, 0}, {gone, 10 * time.Minute}}, []string{"u ch1 00:00:00-00:10:00"}},
		{"an end older than the session's start ends it at its start",
			[]step{{ch1, 10 * time.Minute}, {gone, 0}}, []string{"u ch1 00:10:00-00:10:00"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := openMetricsStore(t)
			for _, s := range tt.steps {
				openVoice(t, p, s.vp, metricsBase.Add(s.at))
			}
			if got := voiceSessions(t, p); !slices.Equal(got, tt.want) {
				t.Errorf("sessions = %v, want %v", got, tt.want)
			}
		})
	}
}

// A sync ends the sessions of absent and moved members, keeps the ones that
// match and opens the missing ones. A nil sync ends every session.
func TestSyncVoicePresence(t *testing.T) {
	p := openMetricsStore(t)
	ctx := context.Background()
	for _, vp := range []metrics.VoicePresence{
		{UserID: "a", ChannelID: "ch1"}, {UserID: "b", ChannelID: "ch1"}, {UserID: "c", ChannelID: "ch2"},
	} {
		openVoice(t, p, vp, metricsBase)
	}
	present := []metrics.VoicePresence{
		{UserID: "a", ChannelID: "ch1"}, {UserID: "b", ChannelID: "ch2"}, {UserID: "d", ChannelID: "ch3"},
	}
	if err := p.SyncVoicePresence(ctx, present, metricsBase.Add(time.Hour)); err != nil {
		t.Fatalf("SyncVoicePresence: %v", err)
	}
	want := []string{
		"a ch1 00:00:00-open",
		"b ch1 00:00:00-01:00:00",
		"b ch2 01:00:00-open",
		"c ch2 00:00:00-01:00:00",
		"d ch3 01:00:00-open",
	}
	if got := voiceSessions(t, p); !slices.Equal(got, want) {
		t.Errorf("sessions after a sync = %v, want %v", got, want)
	}
	if err := p.SyncVoicePresence(ctx, nil, metricsBase.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncVoicePresence(nil): %v", err)
	}
	want = []string{
		"a ch1 00:00:00-02:00:00",
		"b ch1 00:00:00-01:00:00",
		"b ch2 01:00:00-02:00:00",
		"c ch2 00:00:00-01:00:00",
		"d ch3 01:00:00-02:00:00",
	}
	if got := voiceSessions(t, p); !slices.Equal(got, want) {
		t.Errorf("sessions after a nil sync = %v, want %v", got, want)
	}
}

// One fixture covers the rollup's attribution rules: role sections count
// voice in any channel, a session counts on each role's row, a section
// without roles counts its own channels, a session across Sunday midnight
// splits by seconds, an open session counts to the week's end, active is by channel and distinct, the server row holds
// everything, and a recompute replaces only the weeks in its range.
func TestRollUpMetricWeeks(t *testing.T) {
	p := openMetricsStore(t)
	ctx := context.Background()
	w1, w2 := metricsBase, metricsBase.AddDate(0, 0, 7)
	day := func(d, hour, minute int) time.Time {
		return w1.Add(time.Duration(d*24+hour)*time.Hour + time.Duration(minute)*time.Minute)
	}
	sections := []metrics.Section{
		{CategoryID: "cat-a", RoleIDs: []string{"r-a"}},
		{CategoryID: "cat-b", RoleIDs: []string{"r-b"}},
		{CategoryID: "cat-ops"},
	}
	voiceSession(t, p, metrics.VoicePresence{UserID: "u1", ChannelID: "ops", CategoryID: "cat-ops", RoleIDs: []string{"r-a", "r-b"}}, day(1, 20, 0), day(1, 21, 0))
	voiceSession(t, p, metrics.VoicePresence{UserID: "u2", ChannelID: "a", CategoryID: "cat-a"}, day(2, 20, 0), day(2, 20, 30))
	voiceSession(t, p, metrics.VoicePresence{UserID: "u3", ChannelID: "ops", CategoryID: "cat-ops"}, day(6, 23, 0), day(7, 1, 0))
	openVoice(t, p, metrics.VoicePresence{UserID: "u5", ChannelID: "ops", CategoryID: "cat-ops"}, day(13, 23, 0))
	recordMessages(t, p,
		metrics.Message{ID: "m1", UserID: "u1", CategoryID: "cat-a", SentAt: day(0, 9, 0)},
		metrics.Message{ID: "m2", UserID: "u1", CategoryID: "cat-a", SentAt: day(3, 9, 0)},
		metrics.Message{ID: "m3", UserID: "u2", CategoryID: "cat-a", SentAt: day(6, 23, 59)},
		metrics.Message{ID: "m4", UserID: "u4", SentAt: day(4, 9, 0)},
		metrics.Message{ID: "m5", UserID: "u1", CategoryID: "cat-b", SentAt: day(7, 0, 0)},
	)
	if _, err := p.db.ExecContext(ctx, `INSERT INTO metric_weeks VALUES
		('2026-09-28', '', 1, 0, 0, 1), ('2026-10-05', 'cat-gone', 5, 5, 5, 5)`); err != nil {
		t.Fatalf("seed metric_weeks: %v", err)
	}
	if err := p.RollUpMetricWeeks(ctx, w1, w2.AddDate(0, 0, 7), sections); err != nil {
		t.Fatalf("RollUpMetricWeeks: %v", err)
	}
	weeks, err := p.MetricWeeks(ctx, w1.AddDate(0, 0, -7), w2.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("MetricWeeks: %v", err)
	}
	var got []string
	for _, w := range weeks {
		got = append(got, fmt.Sprintf("%s %q messages %d voice %ds people %d active %d",
			w.Week.Format(time.DateOnly), w.CategoryID, w.Messages, w.VoiceSeconds, w.VoicePeople, w.Active))
	}
	want := []string{
		`2026-09-28 "" messages 1 voice 0s people 0 active 1`,
		`2026-10-05 "" messages 4 voice 9000s people 3 active 4`,
		`2026-10-05 "cat-a" messages 3 voice 3600s people 1 active 2`,
		`2026-10-05 "cat-b" messages 0 voice 3600s people 1 active 0`,
		`2026-10-05 "cat-ops" messages 0 voice 7200s people 2 active 2`,
		`2026-10-12 "" messages 1 voice 7200s people 2 active 3`,
		`2026-10-12 "cat-b" messages 1 voice 0s people 0 active 1`,
		`2026-10-12 "cat-ops" messages 0 voice 7200s people 2 active 2`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("weeks =\n%v\nwant\n%v", got, want)
	}
}

// Active days come from messages and from every day a session overlaps,
// with an open session running to now and one ending at midnight adding
// no day after it, from from's day on.
func TestActiveDays(t *testing.T) {
	p := openMetricsStore(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	from := time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)
	voiceSession(t, p, metrics.VoicePresence{UserID: "u1", ChannelID: "ch"}, time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC), time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC))
	openVoice(t, p, metrics.VoicePresence{UserID: "u2", ChannelID: "ch"}, time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC))
	voiceSession(t, p, metrics.VoicePresence{UserID: "u5", ChannelID: "ch"}, time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC), time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))
	recordMessages(t, p,
		metrics.Message{ID: "m1", UserID: "u3", SentAt: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)},
		metrics.Message{ID: "m2", UserID: "u4", SentAt: time.Date(2026, 10, 6, 23, 0, 0, 0, time.UTC)},
		metrics.Message{ID: "m3", UserID: "u1", SentAt: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)},
	)
	days, err := p.ActiveDays(context.Background(), from, now)
	if err != nil {
		t.Fatalf("ActiveDays: %v", err)
	}
	var got []string
	for _, d := range days {
		got = append(got, d.Day.Format(time.DateOnly)+" "+d.UserID)
	}
	want := []string{"2026-10-07 u1", "2026-10-07 u3", "2026-10-08 u1", "2026-10-08 u2", "2026-10-08 u5", "2026-10-09 u2", "2026-10-10 u2"}
	if !slices.Equal(got, want) {
		t.Errorf("active days = %v, want %v", got, want)
	}
}

// Voice spans are clipped to the range, an open session ends at its end,
// and a session that only touches the range is left out.
func TestVoiceSpansClipToTheRange(t *testing.T) {
	p := openMetricsStore(t)
	from, to := metricsBase, metricsBase.AddDate(0, 0, 7)
	voiceSession(t, p, metrics.VoicePresence{UserID: "across-start", ChannelID: "ch"}, from.Add(-time.Hour), from.Add(time.Hour))
	voiceSession(t, p, metrics.VoicePresence{UserID: "inside", ChannelID: "ch"}, from.Add(30*time.Hour), from.Add(31*time.Hour))
	voiceSession(t, p, metrics.VoicePresence{UserID: "before", ChannelID: "ch"}, from.Add(-time.Hour), from)
	voiceSession(t, p, metrics.VoicePresence{UserID: "after", ChannelID: "ch"}, to, to.Add(time.Hour))
	openVoice(t, p, metrics.VoicePresence{UserID: "open", ChannelID: "ch"}, to.Add(-2*time.Hour))
	spans, err := p.VoiceSpans(context.Background(), from, to)
	if err != nil {
		t.Fatalf("VoiceSpans: %v", err)
	}
	var got []string
	for _, s := range spans {
		got = append(got, fmt.Sprintf("%v-%v", s.Start.Sub(from), s.End.Sub(from)))
	}
	want := []string{"0s-1h0m0s", "30h0m0s-31h0m0s", "166h0m0s-168h0m0s"}
	if !slices.Equal(got, want) {
		t.Errorf("spans = %v, want %v", got, want)
	}
}

// MemberEvents returns the joins and leaves from from on, oldest first.
func TestMemberEventsFromACutoffOldestFirst(t *testing.T) {
	p := openMetricsStore(t)
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "u", Kind: metrics.MemberLeft, At: metricsBase.Add(time.Hour)})
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "u", Kind: metrics.MemberJoined, At: metricsBase})
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "old", Kind: metrics.MemberJoined, At: metricsBase.Add(-time.Second)})
	events, err := p.MemberEvents(context.Background(), metricsBase)
	if err != nil {
		t.Fatalf("MemberEvents: %v", err)
	}
	var got []string
	for _, e := range events {
		got = append(got, fmt.Sprintf("%s %s %v", e.UserID, e.Kind, e.At.Sub(metricsBase)))
	}
	if want := []string{"u join 0s", "u leave 1h0m0s"}; !slices.Equal(got, want) {
		t.Errorf("member events = %v, want %v", got, want)
	}
}

// A join's first message is the member's first at or after the join, and
// its reply is the first later message by someone else that addresses them.
func TestFirstReplies(t *testing.T) {
	p := openMetricsStore(t)
	joined := metricsBase.Add(12 * time.Hour)
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "u", Kind: metrics.MemberJoined, At: joined})
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "w", Kind: metrics.MemberJoined, At: joined.Add(10 * time.Minute)})
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "early", Kind: metrics.MemberJoined, At: joined.Add(-48 * time.Hour)})
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "u", Kind: metrics.MemberLeft, At: joined.Add(time.Minute)})
	recordMessages(t, p,
		metrics.Message{ID: "before-join", UserID: "u", SentAt: joined.Add(-time.Hour)},
		metrics.Message{ID: "before-first", UserID: "v", SentAt: joined.Add(30 * time.Minute), Addressed: []string{"u"}},
		metrics.Message{ID: "first", UserID: "u", SentAt: joined.Add(time.Hour)},
		metrics.Message{ID: "self", UserID: "u", SentAt: joined.Add(2 * time.Hour), Addressed: []string{"u"}},
		metrics.Message{ID: "reply", UserID: "v", SentAt: joined.Add(3 * time.Hour), Addressed: []string{"x", "u"}},
		metrics.Message{ID: "later-reply", UserID: "v", SentAt: joined.Add(4 * time.Hour), Addressed: []string{"u"}},
	)
	replies, err := p.FirstReplies(context.Background(), metricsBase, metricsBase.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("FirstReplies: %v", err)
	}
	offset := func(at time.Time) string {
		if at.IsZero() {
			return "none"
		}
		return at.Sub(joined).String()
	}
	var got []string
	for _, r := range replies {
		got = append(got, fmt.Sprintf("%s joined %s first %s replied %s", r.UserID, offset(r.JoinedAt), offset(r.FirstMessageAt), offset(r.RepliedAt)))
	}
	want := []string{"u joined 0s first 1h0m0s replied 3h0m0s", "w joined 10m0s first none replied none"}
	if !slices.Equal(got, want) {
		t.Errorf("first replies = %v, want %v", got, want)
	}
}

// A prune deletes raw rows older than the cutoff, and keeps open sessions
// and the weekly totals.
func TestPruneMetricsKeepsOpenSessionsAndWeeks(t *testing.T) {
	p := openMetricsStore(t)
	ctx := context.Background()
	cutoff := metricsBase
	old, recent := cutoff.Add(-time.Hour), cutoff.Add(time.Hour)
	recordMessages(t, p,
		metrics.Message{ID: "old", UserID: "u", SentAt: old},
		metrics.Message{ID: "recent", UserID: "u", SentAt: recent})
	voiceSession(t, p, metrics.VoicePresence{UserID: "old", ChannelID: "ch"}, old.Add(-time.Hour), old)
	voiceSession(t, p, metrics.VoicePresence{UserID: "across", ChannelID: "ch"}, old, recent)
	openVoice(t, p, metrics.VoicePresence{UserID: "open", ChannelID: "ch"}, old.AddDate(0, 0, -200))
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "old", Kind: metrics.MemberJoined, At: old})
	recordMemberEvent(t, p, metrics.MemberEvent{UserID: "recent", Kind: metrics.MemberJoined, At: recent})
	if _, err := p.db.ExecContext(ctx, `INSERT INTO metric_weeks VALUES ('2026-01-05', '', 1, 0, 0, 1)`); err != nil {
		t.Fatalf("seed metric_weeks: %v", err)
	}
	if err := p.PruneMetrics(ctx, cutoff); err != nil {
		t.Fatalf("PruneMetrics: %v", err)
	}
	for _, c := range []struct {
		what, query string
		want        []string
	}{
		{"messages", `SELECT message_id FROM metric_messages ORDER BY 1`, []string{"recent"}},
		{"sessions", `SELECT user_id FROM metric_voice_sessions ORDER BY 1`, []string{"across", "open"}},
		{"member events", `SELECT user_id FROM metric_member_events ORDER BY 1`, []string{"recent"}},
		{"weeks", `SELECT week::text FROM metric_weeks`, []string{"2026-01-05"}},
	} {
		if got := texts(t, p, c.query); !slices.Equal(got, c.want) {
			t.Errorf("%s after a prune = %v, want %v", c.what, got, c.want)
		}
	}
}

// Every metrics read and write reports a database failure, so the collector
// and the page can report it instead of counting nothing.
func TestMetricsCallsReportADatabaseFailure(t *testing.T) {
	p := openMetricsStore(t)
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()
	present := []metrics.VoicePresence{{UserID: "u", ChannelID: "ch"}}
	calls := map[string]func() error{
		"StartMetrics":          func() error { _, err := p.StartMetrics(ctx, metricsBase); return err },
		"MetricsState":          func() error { _, err := p.MetricsState(ctx); return err },
		"MarkMetricsBackfilled": func() error { return p.MarkMetricsBackfilled(ctx, metricsBase) },
		"RecordMessages":        func() error { return p.RecordMessages(ctx, []metrics.Message{{ID: "m"}}) },
		"SetVoicePresence":      func() error { return p.SetVoicePresence(ctx, present[0], metricsBase) },
		"SyncVoicePresence":     func() error { return p.SyncVoicePresence(ctx, present, metricsBase) },
		"RecordMemberEvent": func() error {
			return p.RecordMemberEvent(ctx, metrics.MemberEvent{UserID: "u", Kind: metrics.MemberJoined, At: metricsBase})
		},
		"RollUpMetricWeeks": func() error { return p.RollUpMetricWeeks(ctx, metricsBase, metricsBase, nil) },
		"PruneMetrics":      func() error { return p.PruneMetrics(ctx, metricsBase) },
		"MetricWeeks":       func() error { _, err := p.MetricWeeks(ctx, metricsBase, metricsBase); return err },
		"ActiveDays":        func() error { _, err := p.ActiveDays(ctx, metricsBase, metricsBase); return err },
		"MemberEvents":      func() error { _, err := p.MemberEvents(ctx, metricsBase); return err },
		"VoiceSpans":        func() error { _, err := p.VoiceSpans(ctx, metricsBase, metricsBase); return err },
		"FirstReplies":      func() error { _, err := p.FirstReplies(ctx, metricsBase, metricsBase); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s on a closed database returned no error", name)
		}
	}
}
