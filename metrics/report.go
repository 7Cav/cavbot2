package metrics

import (
	"cmp"
	"math"
	"slices"
	"time"
)

const (
	day  = 24 * time.Hour
	week = 7 * day
	// TrendWeeks is how many weeks a trend line shows, the report week last.
	TrendWeeks = 12
	// moveShare and moveFloor are when a section's measure makes a brief
	// line: a change of 30% or more, with this week or the average at 10 or more.
	moveShare = 0.30
	moveFloor = 10
	// quietSpan is how long a member who returned had no activity.
	quietSpan = 60 * day
	eps       = 1e-9
)

// Maybe is a figure the report shows. It is unknown when its window starts
// before the data it counts, and then V is zero.
type Maybe[T any] struct {
	V     T
	Known bool
}

func maybe[T any](v T, ok bool) Maybe[T] {
	if !ok {
		return Maybe[T]{}
	}
	return Maybe[T]{V: v, Known: true}
}

// Input is everything one report is built from, read over ReadRanges(Now).
type Input struct {
	Now      time.Time
	State    State
	Sections []Section
	Weeks    []WeekTotal
	Days     []ActiveDay
	Members  []MemberEvent
	Spans    []Span
	Replies  []FirstReply
}

// Ranges are the windows Build needs read for a report at a given time.
type Ranges struct {
	Weeks       Span // metric weeks and voice spans
	DaysFrom    time.Time
	MembersFrom time.Time
	Joins       Span // first replies, by join time
}

// ReadRanges is what to read for a report at now.
func ReadRanges(now time.Time) Ranges {
	w0 := WeekStart(now).Add(-week)
	members := w0.Add(-8 * week)
	if m := monthStart(now).AddDate(0, -len(Report{}.Months), 0).Add(-quietSpan); m.Before(members) {
		members = m
	}
	return Ranges{
		Weeks:       Span{Start: w0.Add(-(TrendWeeks - 1) * week), End: w0.Add(week)},
		DaysFrom:    now.Add(-RawRetention),
		MembersFrom: members,
		Joins:       Span{Start: w0.Add(-7 * week), End: w0},
	}
}

// Report is the Metrics page's figures.
type Report struct {
	// Week is the Monday the report week starts: the latest complete week.
	Week     time.Time
	Summary  Summary
	Brief    Brief
	Sections []SectionRow
	// Heatmap is average people in voice over the trend's weeks.
	Heatmap Heatmap
	Cohort  Cohort
	// Trend is active people over the 28 days to the end of each week.
	Trend  [TrendWeeks]TrendPoint
	Months [3]Month
}

type Summary struct {
	Active28, Active7 Maybe[int]
	// Active28Change is the percent change against the 28 days before.
	Active28Change Maybe[int]
	VoiceHours     Figure
	// Joined counts first joins over the last 4 weeks, and LeftShare the
	// share of them who left within 24 hours.
	Joined    Maybe[int]
	LeftShare Maybe[float64]
	// TalkedShare is the share of the 4 weeks before's joiners who stayed
	// past 24 hours and talked within 7 days.
	TalkedShare Maybe[float64]
}

// Figure is a measure in the report week, its mean over the 4 weeks before,
// and the change between them in whole percent.
type Figure struct {
	Now, Average Maybe[float64]
	Change       Maybe[int]
}

type SectionRow struct {
	Section
	Messages, VoiceHours, Active, VoicePerPerson Figure
	// Trend is active people per week, the report week last.
	Trend [TrendWeeks]Maybe[int]
}

type Measure int

const (
	MeasureMessages Measure = iota
	MeasureVoiceHours
	MeasureActive
)

type Brief struct {
	// Moves are the sections that moved, largest change first.
	Moves      []SectionMoves
	NewMembers NewMembers
	FirstReply FirstReplies
	VoicePeak  Maybe[Peak]
}

type SectionMoves struct {
	Name  string
	Moves []Move
}

type Move struct {
	Measure Measure
	Figure
}

// NewMembers is the report week's joins and how many left within 24 hours,
// and the share of the week before's joiners who talked within 7 days
// against the 4 weeks before that.
type NewMembers struct {
	Joined, Left         Maybe[int]
	Talked, TalkedBefore Maybe[float64]
}

// FirstReplies is the median first reply of the week before's joiners
// against the 4 weeks before that.
type FirstReplies struct {
	Median, Before Maybe[time.Duration]
}

type Peak struct {
	Day     time.Weekday
	Hour    int
	Average float64
}

// Heatmap is average people in voice by weekday, Monday first, and hour in UTC.
type Heatmap [7][24]Maybe[float64]

// Cohort is the members who joined 5 to 8 weeks ago, as a nested funnel.
type Cohort struct {
	Known                            bool
	Joined, Stayed, Talked, Active30 int
	// Messaged sent a message after joining, and Replied of them got a reply.
	Messaged, Replied int
	MedianReply       Maybe[time.Duration]
}

type TrendPoint struct {
	Week   time.Time
	Active Maybe[int]
}

type Month struct {
	Start                time.Time
	New, Returned, Quiet Maybe[int]
}

// Build makes the report. W0 below is the report week, and W-n the week n
// weeks before it. A figure is known only when its whole window starts at
// or after the data it counts: messages from the backfill's reach, voice,
// joins and leaves from the start of collection, and active from both.
func Build(in Input) Report {
	b := newBuilder(in)
	rows := b.sections()
	r := Report{
		Week:     b.w0,
		Summary:  b.summary(),
		Brief:    Brief{Moves: moves(rows), NewMembers: b.newMembers(), FirstReply: b.firstReplies(), VoicePeak: b.voicePeak()},
		Sections: rows,
		Heatmap:  b.heat(b.at(-(TrendWeeks - 1))),
		Cohort:   b.cohort(),
	}
	for i := range r.Trend {
		wk := b.at(i - (TrendWeeks - 1))
		end := wk.Add(week)
		r.Trend[i] = TrendPoint{Week: wk, Active: maybe(b.activeCount(end.Add(-28*day), end), b.known(end.Add(-28*day), b.live))}
	}
	this := monthStart(in.Now)
	for i := range r.Months {
		r.Months[i] = b.month(this.AddDate(0, i-len(r.Months), 0))
	}
	return r
}

type weekKey struct {
	week     int64
	category string
}

type joinKey struct {
	user   string
	joined int64
}

type joiner struct {
	user string
	at   time.Time
}

type builder struct {
	in        Input
	w0        time.Time
	msg, live time.Time
	weeks     map[weekKey]WeekTotal
	// days and joins hold each user's active days and join times, sorted.
	days, joins map[string][]time.Time
	leaves      map[string][]time.Time
	replies     map[joinKey]FirstReply
}

func newBuilder(in Input) *builder {
	b := &builder{in: in, w0: WeekStart(in.Now).Add(-week), msg: in.State.StartedAt, live: in.State.StartedAt,
		weeks: map[weekKey]WeekTotal{}, days: map[string][]time.Time{}, joins: map[string][]time.Time{},
		leaves: map[string][]time.Time{}, replies: map[joinKey]FirstReply{}}
	if !in.State.BackfilledAt.IsZero() {
		b.msg = b.msg.Add(-BackfillSpan)
	}
	for _, w := range in.Weeks {
		b.weeks[weekKey{w.Week.Unix(), w.CategoryID}] = w
	}
	for _, d := range in.Days {
		b.days[d.UserID] = append(b.days[d.UserID], d.Day)
	}
	for _, e := range in.Members {
		switch e.Kind {
		case MemberJoined:
			b.joins[e.UserID] = append(b.joins[e.UserID], e.At)
		case MemberLeft:
			b.leaves[e.UserID] = append(b.leaves[e.UserID], e.At)
		}
	}
	for _, m := range []map[string][]time.Time{b.days, b.joins} {
		for _, ts := range m {
			slices.SortFunc(ts, time.Time.Compare)
		}
	}
	for _, r := range in.Replies {
		b.replies[joinKey{r.UserID, r.JoinedAt.UnixMicro()}] = r
	}
	return b
}

// at is the Monday of week W+k.
func (b *builder) at(k int) time.Time { return b.w0.Add(time.Duration(k) * week) }

// known reports whether a window starting at start is inside the data
// counted from cover. Before collection starts nothing is.
func (b *builder) known(start, cover time.Time) bool {
	return !b.in.State.StartedAt.IsZero() && !start.Before(cover)
}

func (b *builder) total(wk time.Time, category string) WeekTotal {
	return b.weeks[weekKey{wk.Unix(), category}]
}

func (b *builder) sections() []SectionRow {
	count := func(get func(WeekTotal) int) func(WeekTotal) float64 {
		return func(w WeekTotal) float64 { return float64(get(w)) }
	}
	one := func(WeekTotal) float64 { return 1 }
	hours := func(w WeekTotal) float64 { return float64(w.VoiceSeconds) / 3600 }
	rows := make([]SectionRow, 0, len(b.in.Sections))
	for _, s := range b.in.Sections {
		row := SectionRow{
			Section:        s,
			Messages:       b.figure(s.CategoryID, b.msg, count(func(w WeekTotal) int { return w.Messages }), one),
			VoiceHours:     b.figure(s.CategoryID, b.live, hours, one),
			Active:         b.figure(s.CategoryID, b.live, count(func(w WeekTotal) int { return w.Active }), one),
			VoicePerPerson: b.figure(s.CategoryID, b.live, hours, count(func(w WeekTotal) int { return w.VoicePeople })),
		}
		for i := range row.Trend {
			wk := b.at(i - (TrendWeeks - 1))
			row.Trend[i] = maybe(b.total(wk, s.CategoryID).Active, b.known(wk, b.live))
		}
		rows = append(rows, row)
	}
	return rows
}

// figure is num/den in the report week against the sums of num and den
// over the 4 weeks before. A plain count has den 1, so its average is the
// mean.
func (b *builder) figure(category string, cover time.Time, num, den func(WeekTotal) float64) Figure {
	var sumNum, sumDen float64
	for k := -4; k <= -1; k++ {
		t := b.total(b.at(k), category)
		sumNum, sumDen = sumNum+num(t), sumDen+den(t)
	}
	t := b.total(b.w0, category)
	return change(ratio(num(t), den(t), b.known(b.w0, cover)), ratio(sumNum, sumDen, b.known(b.at(-4), cover)))
}

func ratio(num, den float64, ok bool) Maybe[float64] { return maybe(num/den, ok && den > 0) }

func change(now, avg Maybe[float64]) Figure {
	f := Figure{Now: now, Average: avg}
	if now.Known && avg.Known && avg.V > 0 {
		f.Change = maybe(int(math.Round((now.V/avg.V-1)*100)), true)
	}
	return f
}

func moved(f Figure) bool {
	if !f.Change.Known {
		return false
	}
	v, avg := f.Now.V, f.Average.V
	return math.Abs(v-avg) >= moveShare*avg-eps && max(v, avg) >= moveFloor
}

func moves(rows []SectionRow) []SectionMoves {
	var out []SectionMoves
	for _, r := range rows {
		sm := SectionMoves{Name: r.Name}
		for _, m := range []Move{{MeasureMessages, r.Messages}, {MeasureVoiceHours, r.VoiceHours}, {MeasureActive, r.Active}} {
			if moved(m.Figure) {
				sm.Moves = append(sm.Moves, m)
			}
		}
		if len(sm.Moves) > 0 {
			out = append(out, sm)
		}
	}
	largest := func(sm SectionMoves) int {
		n := 0
		for _, m := range sm.Moves {
			n = max(n, abs(m.Change.V))
		}
		return n
	}
	slices.SortStableFunc(out, func(a, b SectionMoves) int { return cmp.Compare(largest(b), largest(a)) })
	return out
}

func (b *builder) summary() Summary {
	today := dayOf(b.in.Now)
	end, from28, from7 := today.Add(day), today.Add(-27*day), today.Add(-6*day)
	before := from28.Add(-28 * day)
	s := Summary{
		Active28:   maybe(b.activeCount(from28, end), b.known(from28, b.live)),
		Active7:    maybe(b.activeCount(from7, end), b.known(from7, b.live)),
		VoiceHours: b.figure("", b.live, func(w WeekTotal) float64 { return float64(w.VoiceSeconds) / 3600 }, func(WeekTotal) float64 { return 1 }),
	}
	s.Active28Change = change(maybe(float64(s.Active28.V), s.Active28.Known),
		maybe(float64(b.activeCount(before, from28)), b.known(before, b.live))).Change
	joined := b.joiners(b.at(-3), b.at(1))
	s.Joined = maybe(len(joined), b.known(b.at(-3), b.live))
	s.LeftShare = share(countOf(joined, b.left), len(joined), s.Joined.Known)
	var stayed []joiner
	for _, j := range b.joiners(b.at(-4), b.w0) {
		if !b.left(j) {
			stayed = append(stayed, j)
		}
	}
	s.TalkedShare = share(countOf(stayed, b.talked), len(stayed), b.known(b.at(-4), b.live))
	return s
}

func (b *builder) newMembers() NewMembers {
	joined, last, before := b.joinedIn(b.w0, b.at(1)), b.joiners(b.at(-1), b.w0), b.joiners(b.at(-5), b.at(-1))
	ok := b.known(b.w0, b.live)
	return NewMembers{
		Joined:       maybe(len(joined), ok),
		Left:         maybe(countOf(joined, b.left), ok),
		Talked:       share(countOf(last, b.talked), len(last), b.known(b.at(-1), b.live)),
		TalkedBefore: share(countOf(before, b.talked), len(before), b.known(b.at(-5), b.live)),
	}
}

func (b *builder) firstReplies() FirstReplies {
	median := func(from, to time.Time) Maybe[time.Duration] {
		_, waits := b.replyWaits(b.joiners(from, to))
		return medianOf(waits, b.known(from, b.live))
	}
	return FirstReplies{Median: median(b.at(-1), b.w0), Before: median(b.at(-5), b.at(-1))}
}

func (b *builder) voicePeak() Maybe[Peak] {
	if !b.known(b.w0, b.live) {
		return Maybe[Peak]{}
	}
	var p Peak
	for d, hours := range b.heat(b.w0) {
		for h, v := range hours {
			if v.V > p.Average {
				p = Peak{Day: time.Weekday((d + 1) % 7), Hour: h, Average: v.V}
			}
		}
	}
	return maybe(p, p.Average > 0)
}

// heat is average people in voice per weekday and hour, from from (or the
// start of collection, whichever is later, rounded up to the hour) to the
// end of the report week.
func (b *builder) heat(from time.Time) Heatmap {
	var grid Heatmap
	if b.in.State.StartedAt.IsZero() {
		return grid
	}
	end := b.at(1)
	start := later(from, b.live)
	if t := start.Truncate(time.Hour); t.Before(start) {
		start = t.Add(time.Hour)
	}
	var secs, hours [7][24]float64
	for t := start; t.Before(end); t = t.Add(time.Hour) {
		d, h := slot(t)
		hours[d][h]++
	}
	for _, s := range b.in.Spans {
		stop := s.End
		if end.Before(stop) {
			stop = end
		}
		for t := later(s.Start, start); t.Before(stop); {
			next := t.Truncate(time.Hour).Add(time.Hour)
			if stop.Before(next) {
				next = stop
			}
			d, h := slot(t)
			secs[d][h] += next.Sub(t).Seconds()
			t = next
		}
	}
	for d := range grid {
		for h := range grid[d] {
			grid[d][h] = ratio(secs[d][h], 3600*hours[d][h], true)
		}
	}
	return grid
}

func (b *builder) cohort() Cohort {
	from := b.at(-7)
	if !b.known(from, b.live) {
		return Cohort{}
	}
	js := b.joiners(from, b.at(-3))
	c := Cohort{Known: true, Joined: len(js)}
	for _, j := range js {
		if b.left(j) {
			continue
		}
		c.Stayed++
		if !b.talked(j) {
			continue
		}
		c.Talked++
		if b.activeOn(j.user, dayOf(j.at).Add(23*day), dayOf(j.at).Add(31*day)) {
			c.Active30++
		}
	}
	var waits []time.Duration
	c.Messaged, waits = b.replyWaits(js)
	c.Replied, c.MedianReply = len(waits), medianOf(waits, true)
	return c
}

func (b *builder) month(start time.Time) Month {
	end, prev := start.AddDate(0, 1, 0), start.AddDate(0, -1, 0)
	var quiet, returned int
	for u := range b.days {
		if b.activeOn(u, prev, start) && !b.activeOn(u, start, end) {
			quiet++
		}
		first, ok := firstIn(b.days[u], start, end)
		if !ok || b.activeOn(u, first.Add(-quietSpan), first) {
			continue
		}
		if _, joined := firstIn(b.joins[u], first.Add(-quietSpan), first.Add(day)); !joined {
			returned++
		}
	}
	return Month{
		Start:    start,
		New:      maybe(len(b.joinedIn(start, end)), b.known(start, b.live)),
		Returned: maybe(returned, b.known(start.Add(-quietSpan), b.live)),
		Quiet:    maybe(quiet, b.known(prev, b.live)),
	}
}

// joiners are the users whose first join falls in [from, to).
func (b *builder) joiners(from, to time.Time) []joiner {
	var out []joiner
	for u, js := range b.joins {
		if !js[0].Before(from) && js[0].Before(to) {
			out = append(out, joiner{u, js[0]})
		}
	}
	return out
}

// joinedIn are the users with a join in [from, to), each at their first one there.
func (b *builder) joinedIn(from, to time.Time) []joiner {
	var out []joiner
	for u, js := range b.joins {
		if at, ok := firstIn(js, from, to); ok {
			out = append(out, joiner{u, at})
		}
	}
	return out
}

// left reports a leave within 24 hours of the join, exactly 24 included.
func (b *builder) left(j joiner) bool {
	for _, at := range b.leaves[j.user] {
		if at.After(j.at) && !at.After(j.at.Add(day)) {
			return true
		}
	}
	return false
}

// talked reports activity on the join day or the 7 days after it.
func (b *builder) talked(j joiner) bool {
	return b.activeOn(j.user, dayOf(j.at), dayOf(j.at).Add(8*day))
}

// replyWaits is how many joiners sent a message after joining, and the
// wait for a reply of each who got one.
func (b *builder) replyWaits(js []joiner) (messaged int, waits []time.Duration) {
	for _, j := range js {
		r, ok := b.replies[joinKey{j.user, j.at.UnixMicro()}]
		if !ok || r.FirstMessageAt.IsZero() {
			continue
		}
		messaged++
		if !r.RepliedAt.IsZero() {
			waits = append(waits, r.RepliedAt.Sub(r.FirstMessageAt))
		}
	}
	return messaged, waits
}

// firstIn is the first of the sorted times ts in [from, to).
func firstIn(ts []time.Time, from, to time.Time) (time.Time, bool) {
	i, _ := slices.BinarySearchFunc(ts, from, time.Time.Compare)
	if i < len(ts) && ts[i].Before(to) {
		return ts[i], true
	}
	return time.Time{}, false
}

func (b *builder) activeOn(user string, from, to time.Time) bool {
	_, ok := firstIn(b.days[user], from, to)
	return ok
}

func (b *builder) activeCount(from, to time.Time) int {
	n := 0
	for u := range b.days {
		if b.activeOn(u, from, to) {
			n++
		}
	}
	return n
}

func countOf(js []joiner, pred func(joiner) bool) int {
	n := 0
	for _, j := range js {
		if pred(j) {
			n++
		}
	}
	return n
}

func share(n, of int, ok bool) Maybe[float64] { return ratio(float64(n), float64(of), ok) }

func medianOf(ds []time.Duration, ok bool) Maybe[time.Duration] {
	if len(ds) == 0 {
		return Maybe[time.Duration]{}
	}
	slices.Sort(ds)
	n := len(ds)
	if n%2 == 1 {
		return maybe(ds[n/2], ok)
	}
	return maybe((ds[n/2-1]+ds[n/2])/2, ok)
}

func slot(t time.Time) (int, int) {
	u := t.UTC()
	return (int(u.Weekday()) + 6) % 7, u.Hour()
}

func dayOf(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
