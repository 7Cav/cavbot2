package panel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/metrics"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// fakeMetrics answers the Metrics page's reads with the rows in data, read
// ranges aside, and fails its weeks read once a test sets failing.
type fakeMetrics struct {
	mu      sync.Mutex
	data    metrics.Input
	failing bool
}

func (f *fakeMetrics) fail() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = true
}

func (f *fakeMetrics) set(data metrics.Input) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = data
}

func (f *fakeMetrics) rows() metrics.Input {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func (f *fakeMetrics) MetricsState(context.Context) (metrics.State, error) {
	return f.rows().State, nil
}

func (f *fakeMetrics) MetricWeeks(context.Context, time.Time, time.Time) ([]metrics.WeekTotal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return nil, fmt.Errorf("metric weeks: %w", errStoreDown)
	}
	return f.data.Weeks, nil
}

func (f *fakeMetrics) ActiveDays(context.Context, time.Time, time.Time) ([]metrics.ActiveDay, error) {
	return f.rows().Days, nil
}

func (f *fakeMetrics) MemberEvents(context.Context, time.Time) ([]metrics.MemberEvent, error) {
	return f.rows().Members, nil
}

func (f *fakeMetrics) VoiceSpans(context.Context, time.Time, time.Time) ([]metrics.Span, error) {
	return f.rows().Spans, nil
}

func (f *fakeMetrics) FirstReplies(context.Context, time.Time, time.Time) ([]metrics.FirstReply, error) {
	return f.rows().Replies, nil
}

// busyServer is 12 weeks of activity in the category given, collected for
// 200 days, with the report week busier than the weeks before, a join each
// week, and voice in the report week.
func busyServer(now time.Time, category string) metrics.Input {
	w0 := metrics.WeekStart(now).AddDate(0, 0, -7)
	today := now.UTC().Truncate(24 * time.Hour)
	in := metrics.Input{State: metrics.State{StartedAt: now.AddDate(0, 0, -200), BackfilledAt: now.AddDate(0, 0, -199)}}
	for k := -11; k <= 0; k++ {
		n := 10
		if k == 0 {
			n = 30
		}
		for _, c := range []string{"", category} {
			in.Weeks = append(in.Weeks, metrics.WeekTotal{Week: w0.AddDate(0, 0, 7*k), CategoryID: c,
				Messages: 100 * n, VoiceSeconds: int64(n) * 3600, VoicePeople: 3, Active: n})
		}
		joined := w0.AddDate(0, 0, 7*k).Add(time.Hour)
		user := fmt.Sprintf("j%d", -k)
		in.Members = append(in.Members, metrics.MemberEvent{UserID: user, Kind: metrics.MemberJoined, At: joined})
		in.Days = append(in.Days, metrics.ActiveDay{UserID: user, Day: joined.Truncate(24 * time.Hour)},
			metrics.ActiveDay{UserID: user, Day: joined.Truncate(24*time.Hour).AddDate(0, 0, 25)})
		in.Replies = append(in.Replies, metrics.FirstReply{UserID: user, JoinedAt: joined, FirstMessageAt: joined.Add(time.Hour),
			RepliedAt: joined.Add(time.Duration(2-k%2) * time.Hour)})
	}
	in.Members = append(in.Members, metrics.MemberEvent{UserID: "j1", Kind: metrics.MemberLeft, At: w0.Add(-6 * 24 * time.Hour)})
	for d := range 60 {
		in.Days = append(in.Days, metrics.ActiveDay{UserID: fmt.Sprintf("u%d", d%7), Day: today.AddDate(0, 0, -d)})
	}
	for range 2 {
		in.Spans = append(in.Spans, metrics.Span{Start: w0.Add(45 * time.Hour), End: w0.Add(47*time.Hour + 30*time.Minute)})
	}
	return in
}

// A panel admin sees one row per category that holds a channel, in
// Discord's category order.
func TestMetricsPageListsSectionsInCategoryOrder(t *testing.T) {
	w := newTestWorld(t, testHub())
	w.metrics.set(busyServer(w.clock.Now(), "cat-2"))
	w.discord.mu.Lock()
	w.discord.channels[0].Position = 2
	w.discord.channels = append(w.discord.channels,
		&discordgo.Channel{ID: "cat-2", Name: "▬ A/1-7 ▬", Type: discordgo.ChannelTypeGuildCategory, Position: 1},
		&discordgo.Channel{ID: "text-2", Name: "chat", Type: discordgo.ChannelTypeGuildText, ParentID: "cat-2"},
		&discordgo.Channel{ID: "cat-empty", Name: "Empty", Type: discordgo.ChannelTypeGuildCategory})
	w.discord.mu.Unlock()
	signIn(t, w.forum, w.b)

	res := w.b.get(metricsPath)

	doc := parseHTML(t, res)
	if got := pageOf(t, doc); res.StatusCode != http.StatusOK || got != pageMetrics {
		t.Fatalf("GET %s answers page %q with status %d, want metrics with 200", metricsPath, got, res.StatusCode)
	}
	if got, want := valuesOf(doc, "data-category"), []string{"cat-2", "cat-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("section rows = %v, want %v", got, want)
	}
}

// A read that fails gets the could-not-load page, reported once, and the
// page carries none of the error's text.
func TestMetricsPageReadFailureIsReportedAndSaysSo(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	reported := recordSentry(t)
	w.metrics.fail()

	res := w.b.get(metricsPath)

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	if got := failureOf(t, doc); got != string(failureReadFailed) {
		t.Errorf("failure page = %q, want %s", got, failureReadFailed)
	}
	if strings.Contains(string(body), errStoreDown.Error()) {
		t.Error("the page carries the error's text")
	}
	if n := len(reported.recorded()); n != 1 {
		t.Errorf("sent %d Sentry events, want 1", n)
	}
}
