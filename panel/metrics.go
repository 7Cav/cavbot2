package panel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/7cav/cavbot2/metrics"
	"github.com/7cav/cavbot2/utils"
)

// The Metrics page (#949, GLOSSARY.md) shows panel admins how the Discord
// server is used, in group totals. metrics.Build makes the figures; this
// file reads them and lays them out, the charts as SVG geometry the
// template draws.

const metricsPath = "/metrics"

// MetricsReader is what the Metrics page reads (*store.Postgres).
type MetricsReader interface {
	MetricsState(ctx context.Context) (metrics.State, error)
	MetricWeeks(ctx context.Context, from, to time.Time) ([]metrics.WeekTotal, error)
	ActiveDays(ctx context.Context, from, now time.Time) ([]metrics.ActiveDay, error)
	MemberEvents(ctx context.Context, from time.Time) ([]metrics.MemberEvent, error)
	VoiceSpans(ctx context.Context, from, to time.Time) ([]metrics.Span, error)
	FirstReplies(ctx context.Context, joinedFrom, joinedTo time.Time) ([]metrics.FirstReply, error)
}

// metricsPage is GET /metrics, read under the hub page's time budget and
// with its failures answered the way the Settings page answers its own.
func (p *Panel) metricsPage(w http.ResponseWriter, r *http.Request, sess session) {
	start := panelClock.Now()
	ctx, cancel := panelClock.WithTimeout(r.Context(), hubPageBudget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	reads := newPageReads(start, deadline)
	report, since, err := p.hubs.metricsReport(ctx, reads, start)
	switch {
	case errors.Is(err, context.Canceled):
		utils.Info("Panel page abandoned", "step", "metrics page", "username", sess.username, "forum_user_id", sess.userID)
		return
	case errors.Is(err, errNoGuildData):
		p.noGuildData(w, r, sess, "metrics page", metricsPath, err)
		return
	case err != nil:
		var budget *budgetReport
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			budget = reads.report(hubPageBudget)
		}
		p.pageFailed(w, r, sess, "metrics page", metricsPath, err, budget)
		return
	}
	data := sess.page("Metrics")
	data.Page, data.Metrics = pageMetrics, metricsViewOf(report, since)
	p.render(w, r, http.StatusOK, "metrics", data)
}

// metricsReport reads the sections from the gateway state and the rows
// from the bot store, and builds the report, with when collection started.
func (s *hubService) metricsReport(ctx context.Context, reads *pageReads, now time.Time) (metrics.Report, time.Time, error) {
	guild, err := s.readGuild(ctx)
	if err != nil {
		return metrics.Report{}, time.Time{}, err
	}
	m, rg := s.deps.Metrics, metrics.ReadRanges(now)
	in := metrics.Input{Now: now, Sections: metrics.Sections(guild.channels.all, slices.Collect(maps.Values(guild.info.live)))}
	read := func(name string, f func() error) {
		if err != nil {
			return
		}
		_, err = timeStoreRead(reads, name, func() (struct{}, error) { return struct{}{}, f() })
		if err != nil {
			err = fmt.Errorf("%s: %w", name, err)
		}
	}
	read("read metrics state", func() (e error) { in.State, e = m.MetricsState(ctx); return e })
	read("read metric weeks", func() (e error) { in.Weeks, e = m.MetricWeeks(ctx, rg.Weeks.Start, rg.Weeks.End); return e })
	read("read active days", func() (e error) { in.Days, e = m.ActiveDays(ctx, rg.DaysFrom, now); return e })
	read("read member events", func() (e error) { in.Members, e = m.MemberEvents(ctx, rg.MembersFrom); return e })
	read("read voice spans", func() (e error) { in.Spans, e = m.VoiceSpans(ctx, rg.Weeks.Start, rg.Weeks.End); return e })
	read("read first replies", func() (e error) { in.Replies, e = m.FirstReplies(ctx, rg.Joins.Start, rg.Joins.End); return e })
	if err != nil {
		return metrics.Report{}, time.Time{}, err
	}
	return metrics.Build(in), in.State.StartedAt, nil
}

// metricsView is what the Metrics page renders from.
type metricsView struct {
	Week string
	// Since is when collection started, zero before it has.
	Since    time.Time
	Stats    []metricStat
	Brief    []briefLine
	Sections []sectionRowView
	Heatmap  heatmapView
	Funnel   []funnelStep
	Reply    string
	Trend    lineChart
	Months   []monthRow
}

type metricStat struct{ Label, Value, Detail string }

// briefLine is one line of This week: Lead in bright text, then Text. A
// line with no Lead is a quiet note.
type briefLine struct{ Lead, Text string }

type sectionRowView struct {
	CategoryID, Name string
	Cells            []figureCell
	Spark            sparkline
}

type figureCell struct{ Value, Change string }

type sparkline struct {
	Points       string
	LastX, LastY float64
	HasLast      bool
	Label        string
}

type heatmapView struct {
	CellW  float64
	Cells  []heatCell
	Days   []axisLabel
	Hours  []axisLabel
	Height float64
}

type heatCell struct {
	X, Y    float64
	Opacity string
	Empty   bool
	Title   string
}

type axisLabel struct {
	X, Y float64
	Text string
}

type funnelStep struct{ Label, Count, Share, Width string }

type lineChart struct {
	Points string
	Last   *axisLabel
	Grid   []axisLabel
	Hovers []hoverBand
	Labels []axisLabel
	Label  string
}

type hoverBand struct {
	X, W  float64
	Title string
}

type monthRow struct{ Name, New, Returned, Quiet string }

// The charts' geometry, in viewBox units. Each scales to its block's width.
const (
	sparkW, sparkH       = 96.0, 22.0
	heatW, heatLeft      = 400.0, 34.0
	heatPitch, heatCellH = 15.0, 13.0
	lineW, lineH         = 400.0, 140.0
	lineLeft             = 40.0
)

const unknownFigure = "-"

func metricsViewOf(r metrics.Report, since time.Time) metricsView {
	weekEnd := r.Week.AddDate(0, 0, 6)
	v := metricsView{
		Week:    r.Week.Format("2 Jan") + " to " + weekEnd.Format("2 Jan 2006"),
		Since:   since,
		Stats:   statsOf(r.Summary),
		Brief:   briefOf(r.Brief),
		Heatmap: heatmapOf(r.Heatmap),
		Trend:   trendOf(r.Trend),
	}
	for _, row := range r.Sections {
		v.Sections = append(v.Sections, sectionRowView{CategoryID: row.CategoryID, Name: row.Name, Spark: sparkOf(row.Trend), Cells: []figureCell{
			cellOf(row.Messages), cellOf(row.VoiceHours), cellOf(row.Active), cellOf(row.VoicePerPerson),
		}})
	}
	c := r.Cohort
	for _, s := range []struct {
		label string
		n     int
	}{{"Joined", c.Joined}, {"Stayed past 24 h", c.Stayed}, {"Talked within 7 days", c.Talked}, {"Active at 30 days", c.Active30}} {
		step := funnelStep{Label: s.label, Count: unknownFigure, Share: unknownFigure, Width: "0%"}
		if c.Known {
			step.Count = fmtCount(s.n)
			if c.Joined > 0 {
				share := float64(s.n) / float64(c.Joined)
				step.Share, step.Width = fmtShare(share), strconv.FormatFloat(round1(share*100), 'f', -1, 64)+"%"
			}
		}
		v.Funnel = append(v.Funnel, step)
	}
	v.Reply = "Median time to first reply: " + shown(c.MedianReply, fmtWait) + "."
	if c.Known && c.Messaged > 0 {
		v.Reply += fmt.Sprintf(" %s of the %s who sent a message got a reply.", fmtCount(c.Replied), fmtCount(c.Messaged))
	}
	for _, m := range r.Months {
		v.Months = append(v.Months, monthRow{Name: m.Start.Format("Jan 2006"), New: shown(m.New, fmtCount), Returned: shown(m.Returned, fmtCount), Quiet: shown(m.Quiet, fmtCount)})
	}
	return v
}

func statsOf(s metrics.Summary) []metricStat {
	stats := []metricStat{
		{Label: "Active, 28 days", Value: shown(s.Active28, fmtCount)},
		{Label: "Active, 7 days", Value: shown(s.Active7, fmtCount)},
		{Label: "Voice hours, week", Value: shown(s.VoiceHours.Now, fmtNum)},
		{Label: "Joined, 4 weeks", Value: shown(s.Joined, fmtCount)},
		{Label: "Talked within 7 days", Value: shown(s.TalkedShare, fmtShare), Detail: "of new members who stayed"},
	}
	if s.Active28Change.Known {
		stats[0].Detail = fmtChange(s.Active28Change.V) + " against the 28 days before"
	}
	if s.Active7.Known && s.Active28.Known && s.Active28.V > 0 {
		stats[1].Detail = fmtShare(float64(s.Active7.V)/float64(s.Active28.V)) + " of 28-day active"
	}
	if s.VoiceHours.Change.Known {
		stats[2].Detail = fmtChange(s.VoiceHours.Change.V) + " against the 4 weeks before"
	}
	if s.LeftShare.Known {
		stats[3].Detail = fmtShare(s.LeftShare.V) + " left within 24 h"
	}
	return stats
}

var measureWords = map[metrics.Measure]string{
	metrics.MeasureMessages:   "messages",
	metrics.MeasureVoiceHours: "voice hours",
	metrics.MeasureActive:     "active people",
}

func briefOf(b metrics.Brief) []briefLine {
	var lines []briefLine
	for _, sm := range b.Moves {
		parts := make([]string, 0, len(sm.Moves))
		for _, m := range sm.Moves {
			dir := "up"
			if m.Change.V < 0 {
				dir = "down"
			}
			parts = append(parts, fmt.Sprintf("%s %s %d%% (%s, 4-week average %s)", measureWords[m.Measure], dir, abs(m.Change.V), fmtNum(m.Now.V), fmtNum(m.Average.V)))
		}
		lines = append(lines, briefLine{Lead: sm.Name, Text: strings.Join(parts, ", ") + "."})
	}
	if len(lines) == 0 {
		lines = append(lines, briefLine{Text: "No section moved 30% or more."})
	}
	var joins []string
	if n := b.NewMembers; n.Joined.Known {
		joins = append(joins, fmt.Sprintf("%s joined, %s left within 24 hours.", fmtCount(n.Joined.V), fmtCount(n.Left.V)))
	}
	if n := b.NewMembers; n.Talked.Known {
		s := fmtShare(n.Talked.V) + " of the week before's joiners talked within 7 days"
		if n.TalkedBefore.Known {
			s += ", against " + fmtShare(n.TalkedBefore.V) + " for the 4 weeks before"
		}
		joins = append(joins, s+".")
	}
	if len(joins) > 0 {
		lines = append(lines, briefLine{Lead: "New members", Text: strings.Join(joins, " ")})
	}
	if f := b.FirstReply; f.Median.Known {
		s := "the week before's joiners waited a median of " + fmtWait(f.Median.V) + " for a reply"
		if f.Before.Known {
			s += ", against " + fmtWait(f.Before.V) + " for the 4 weeks before"
		}
		lines = append(lines, briefLine{Lead: "First reply", Text: s + "."})
	}
	if p := b.VoicePeak; p.Known {
		lines = append(lines, briefLine{Lead: "Voice peak", Text: fmt.Sprintf("%s in voice on average at %s %02d00z.", fmtNum(p.V.Average), p.V.Day.String()[:3], p.V.Hour)})
	}
	return lines
}

func cellOf(f metrics.Figure) figureCell {
	return figureCell{Value: shown(f.Now, fmtNum), Change: shown(f.Change, fmtChange)}
}

// sparkOf draws a section's active people per week, its known weeks alone.
func sparkOf(trend [metrics.TrendWeeks]metrics.Maybe[int]) sparkline {
	lo, hi := math.Inf(1), math.Inf(-1)
	words := make([]string, len(trend))
	for i, t := range trend {
		words[i] = shown(t, fmtCount)
		if t.Known {
			lo, hi = math.Min(lo, float64(t.V)), math.Max(hi, float64(t.V))
		}
	}
	s := sparkline{Label: "Active people per week, last 12 weeks: " + strings.Join(words, ", ")}
	var pts []string
	for i, t := range trend {
		if !t.Known {
			continue
		}
		x, y := round1(float64(i)*sparkW/float64(len(trend)-1)), sparkH/2
		if hi > lo {
			y = round1(sparkH - 2 - (float64(t.V)-lo)/(hi-lo)*(sparkH-4))
		}
		pts = append(pts, fmt.Sprintf("%g,%g", x, y))
		s.LastX, s.LastY, s.HasLast = x, y, true
	}
	s.Points = strings.Join(pts, " ")
	return s
}

var weekdays = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func heatmapOf(h metrics.Heatmap) heatmapView {
	peak := 0.0
	for _, hours := range h {
		for _, c := range hours {
			peak = math.Max(peak, c.V)
		}
	}
	cw := (heatW - heatLeft) / 24
	v := heatmapView{CellW: round1(cw - 2), Height: 7*heatPitch + 14}
	for d, hours := range h {
		y := float64(d) * heatPitch
		v.Days = append(v.Days, axisLabel{X: heatLeft - 5, Y: y + heatCellH - 3, Text: weekdays[d]})
		for hr, c := range hours {
			cell := heatCell{X: round1(heatLeft + float64(hr)*cw), Y: y, Empty: !c.Known,
				Title: fmt.Sprintf("%s %02d00z: no data yet", weekdays[d], hr)}
			if c.Known {
				share := 0.0
				if peak > 0 {
					share = c.V / peak
				}
				cell.Opacity = strconv.FormatFloat(0.08+0.92*math.Pow(share, 0.9), 'f', 2, 64)
				cell.Title = fmt.Sprintf("%s %02d00z: %s in voice on average", weekdays[d], hr, fmtNum(c.V))
			}
			v.Cells = append(v.Cells, cell)
		}
	}
	for _, hr := range []int{0, 6, 12, 18, 23} {
		v.Hours = append(v.Hours, axisLabel{X: round1(heatLeft + (float64(hr)+0.5)*cw), Y: 7*heatPitch + 11, Text: fmt.Sprintf("%02dz", hr)})
	}
	return v
}

// trendOf draws active people over 28 days at each week's end, its known
// weeks alone, between gridlines a quarter and three quarters up.
func trendOf(points [metrics.TrendWeeks]metrics.TrendPoint) lineChart {
	lo, hi := math.Inf(1), math.Inf(-1)
	words := make([]string, len(points))
	for i, p := range points {
		words[i] = shown(p.Active, fmtCount)
		if p.Active.Known {
			lo, hi = math.Min(lo, float64(p.Active.V)), math.Max(hi, float64(p.Active.V))
		}
	}
	c := lineChart{Label: "Active people over 28 days, at the end of each of the last 12 weeks: " + strings.Join(words, ", ")}
	step := (lineW - lineLeft) / float64(len(points)-1)
	x := func(i int) float64 { return round1(lineLeft + float64(i)*step) }
	ending := func(p metrics.TrendPoint) string { return p.Week.AddDate(0, 0, 6).Format("2 Jan") }
	for i, p := range points {
		c.Hovers = append(c.Hovers, hoverBand{X: round1(x(i) - step/2), W: round1(step),
			Title: fmt.Sprintf("28 days to %s: %s active", ending(p), shown(p.Active, fmtCount))})
	}
	c.Labels = []axisLabel{{X: lineLeft, Y: lineH + 16, Text: ending(points[0])}, {X: lineW, Y: lineH + 16, Text: ending(points[len(points)-1])}}
	if math.IsInf(lo, 1) {
		return c
	}
	lo, hi = lo*0.9, hi*1.08
	if hi <= lo {
		hi = lo + 1
	}
	y := func(v float64) float64 { return round1(lineH - (v-lo)/(hi-lo)*lineH) }
	for _, f := range []float64{0.25, 0.75} {
		t := lo + (hi-lo)*f
		c.Grid = append(c.Grid, axisLabel{X: lineLeft - 6, Y: y(t), Text: fmtCount(int(math.Round(t)))})
	}
	var pts []string
	for i, p := range points {
		if p.Active.Known {
			pts = append(pts, fmt.Sprintf("%g,%g", x(i), y(float64(p.Active.V))))
			c.Last = &axisLabel{X: x(i), Y: y(float64(p.Active.V))}
		}
	}
	c.Points = strings.Join(pts, " ")
	return c
}

func shown[T any](m metrics.Maybe[T], format func(T) string) string {
	if !m.Known {
		return unknownFigure
	}
	return format(m.V)
}

// fmtCount writes a count with thousands separators, as in 1,234.
func fmtCount(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// fmtNum writes a figure to one decimal under 10 and whole above it.
func fmtNum(v float64) string {
	if v < 10 {
		return strconv.FormatFloat(round1(v), 'f', -1, 64)
	}
	return fmtCount(int(math.Round(v)))
}

func fmtShare(v float64) string { return strconv.Itoa(int(math.Round(v*100))) + "%" }

func fmtChange(pct int) string { return fmt.Sprintf("%+d%%", pct) }

// fmtWait writes a wait in minutes under an hour, and in hours above it.
func fmtWait(d time.Duration) string {
	if d < time.Hour {
		return strconv.Itoa(int(d.Round(time.Minute)/time.Minute)) + " min"
	}
	return fmtNum(d.Hours()) + " h"
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
