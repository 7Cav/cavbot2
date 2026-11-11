package metrics

import (
	"reflect"
	"testing"
	"time"
)

var (
	// now is a Tuesday, so the report week W0 runs Monday 2 Nov to Sunday 8 Nov.
	now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	w0  = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	// collecting started long ago and the backfill finished, so every window is known.
	collecting = State{StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), BackfilledAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}
)

// wk is the Monday of week W+k, plus any offset.
func wk(k int, offset ...time.Duration) time.Time {
	t := w0.AddDate(0, 0, 7*k)
	for _, d := range offset {
		t = t.Add(d)
	}
	return t
}

// weeksOf gives category c the totals in W0 and the same totals in each of the 4 weeks before.
func weeksOf(c string, now, before WeekTotal) []WeekTotal {
	out := []WeekTotal{now}
	out[0].Week, out[0].CategoryID = w0, c
	for k := -4; k <= -1; k++ {
		before.Week, before.CategoryID = wk(k), c
		out = append(out, before)
	}
	return out
}

func known[T any](v T) Maybe[T] { return Maybe[T]{V: v, Known: true} }

func TestReportWeekIsTheLatestCompleteWeek(t *testing.T) {
	weeks := []WeekTotal{{Week: wk(0), CategoryID: "c", Messages: 5}, {Week: wk(-1), CategoryID: "c", Messages: 9}}
	tests := []struct {
		name         string
		now          time.Time
		week         time.Time
		weekMessages float64
	}{
		{"Monday 00:00 ends the week before", wk(1), wk(0), 5},
		{"Sunday 23:59:59 is still inside the current week", wk(1, -time.Second), wk(-1), 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Build(Input{Now: tc.now, State: collecting, Sections: []Section{{CategoryID: "c"}}, Weeks: weeks})
			if !r.Week.Equal(tc.week) {
				t.Errorf("report week = %v, want %v", r.Week, tc.week)
			}
			if got := r.Sections[0].Messages.Now; got != known(tc.weekMessages) {
				t.Errorf("the row's messages = %+v, want %v", got, tc.weekMessages)
			}
		})
	}
}

func TestSectionFigures(t *testing.T) {
	messages := func(r SectionRow) Figure { return r.Messages }
	voice := func(r SectionRow) Figure { return r.VoiceHours }
	perPerson := func(r SectionRow) Figure { return r.VoicePerPerson }
	tests := []struct {
		name  string
		state State
		now   WeekTotal
		prior WeekTotal
		pick  func(SectionRow) Figure
		want  Figure
	}{
		{
			name: "change against the 4-week mean, rounded", state: collecting, pick: messages,
			now: WeekTotal{Messages: 13}, prior: WeekTotal{Messages: 8},
			want: Figure{Now: known(13.0), Average: known(8.0), Change: known(63)},
		},
		{
			name: "a zero average has no change", state: collecting, pick: messages,
			now: WeekTotal{Messages: 5}, prior: WeekTotal{},
			want: Figure{Now: known(5.0), Average: known(0.0)},
		},
		{
			name: "voice is unknown in a week collection started inside", pick: voice,
			state: State{StartedAt: wk(0, time.Hour)},
			now:   WeekTotal{VoiceSeconds: 3600}, prior: WeekTotal{VoiceSeconds: 3600},
			want: Figure{},
		},
		{
			name: "voice is known for whole weeks after collection started, its average not yet", pick: voice,
			state: State{StartedAt: wk(-3)},
			now:   WeekTotal{VoiceSeconds: 7200}, prior: WeekTotal{VoiceSeconds: 3600},
			want: Figure{Now: known(2.0)},
		},
		{
			name: "messages before collection are unknown until the backfill finishes", pick: messages,
			state: State{StartedAt: now},
			now:   WeekTotal{Messages: 20}, prior: WeekTotal{Messages: 10},
			want: Figure{},
		},
		{
			name: "after the backfill, messages are known 12 weeks before collection started", pick: messages,
			state: State{StartedAt: now, BackfilledAt: now.Add(time.Hour)},
			now:   WeekTotal{Messages: 20}, prior: WeekTotal{Messages: 10},
			want: Figure{Now: known(20.0), Average: known(10.0), Change: known(100)},
		},
		{
			name: "voice per person divides hours by people in voice", state: collecting, pick: perPerson,
			now:   WeekTotal{VoiceSeconds: 10 * 3600, VoicePeople: 4},
			prior: WeekTotal{VoiceSeconds: 2 * 3600, VoicePeople: 1},
			want:  Figure{Now: known(2.5), Average: known(2.0), Change: known(25)},
		},
		{
			name: "voice per person is unknown with nobody in voice", state: collecting, pick: perPerson,
			now: WeekTotal{}, prior: WeekTotal{VoiceSeconds: 3600, VoicePeople: 1},
			want: Figure{Average: known(1.0)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Build(Input{Now: now, State: tc.state, Sections: []Section{{CategoryID: "c"}}, Weeks: weeksOf("c", tc.now, tc.prior)})
			if got := tc.pick(r.Sections[0]); got != tc.want {
				t.Errorf("figure = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSectionsFollowTheirOrderWithZeroRows(t *testing.T) {
	sections := []Section{{CategoryID: "b", Name: "B"}, {CategoryID: "a", Name: "A"}}
	r := Build(Input{Now: now, State: collecting, Sections: sections, Weeks: weeksOf("a", WeekTotal{Messages: 4, Active: 3}, WeekTotal{Messages: 4})})

	if len(r.Sections) != 2 || r.Sections[0].Name != "B" || r.Sections[1].Name != "A" {
		t.Fatalf("rows = %+v, want B then A", r.Sections)
	}
	if got := r.Sections[0].Messages.Now; got != known(0.0) {
		t.Errorf("a section with no activity reads %+v, want a known 0", got)
	}
	if got := r.Sections[1].Trend[TrendWeeks-1]; got != known(3) {
		t.Errorf("the trend's report week = %+v, want the week's 3 active", got)
	}
}

func TestBriefNamesTheSectionsThatMoved(t *testing.T) {
	msgs := func(c string, now, before int) []WeekTotal {
		return weeksOf(c, WeekTotal{Messages: now}, WeekTotal{Messages: before})
	}
	tests := []struct {
		name  string
		state State
		weeks []WeekTotal
		want  []string
	}{
		{"30% up moves", collecting, msgs("a", 13, 10), []string{"A"}},
		{"30% down moves", collecting, msgs("a", 7, 10), []string{"A"}},
		{"29.9% stays", collecting, weeksOf("a", WeekTotal{VoiceSeconds: 46764}, WeekTotal{VoiceSeconds: 36000}), nil},
		{"30.0% of voice hours moves", collecting, weeksOf("a", WeekTotal{VoiceSeconds: 46800}, WeekTotal{VoiceSeconds: 36000}), []string{"A"}},
		{"under 10 both ways stays", collecting, msgs("a", 9, 6), nil},
		{"a fall from 10 moves", collecting, msgs("a", 2, 10), []string{"A"}},
		{"a zero average stays", collecting, msgs("a", 50, 0), nil},
		{"an unknown average stays", State{StartedAt: now}, msgs("a", 50, 10), nil},
		{"largest change first", collecting, append(msgs("a", 14, 10), msgs("b", 4, 10)...), []string{"B", "A"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Build(Input{Now: now, State: tc.state, Sections: []Section{{CategoryID: "a", Name: "A"}, {CategoryID: "b", Name: "B"}}, Weeks: tc.weeks})
			var got []string
			for _, m := range r.Brief.Moves {
				got = append(got, m.Name)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sections in the brief = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSummaryActive(t *testing.T) {
	today := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	days := []ActiveDay{
		{UserID: "u1", Day: today}, {UserID: "u1", Day: today.AddDate(0, 0, -1)},
		{UserID: "u2", Day: today.AddDate(0, 0, -27)},
		{UserID: "u3", Day: today.AddDate(0, 0, -28)},
	}
	tests := []struct {
		name          string
		state         State
		a28, a7, chng Maybe[int]
		// trend is the 28 days to the end of the report week, Sunday 8 Nov.
		trend Maybe[int]
	}{
		{"distinct users over 28 and 7 days, today included", collecting, known(2), known(1), known(100), known(2)},
		{"the change is unknown while the 28 days before are", State{StartedAt: today.AddDate(0, 0, -27)}, known(2), known(1), Maybe[int]{}, Maybe[int]{}},
		{"unknown while collection is younger than the window", State{StartedAt: today.AddDate(0, 0, -10)}, Maybe[int]{}, known(1), Maybe[int]{}, Maybe[int]{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Build(Input{Now: now, State: tc.state, Days: days})
			s := r.Summary
			if s.Active28 != tc.a28 || s.Active7 != tc.a7 || s.Active28Change != tc.chng {
				t.Errorf("active 28 = %+v, 7 = %+v, change = %+v; want %+v, %+v, %+v", s.Active28, s.Active7, s.Active28Change, tc.a28, tc.a7, tc.chng)
			}
			if got := r.Trend[TrendWeeks-1].Active; got != tc.trend {
				t.Errorf("the trend's report week = %+v, want %+v", got, tc.trend)
			}
		})
	}
}

func TestSummaryJoins(t *testing.T) {
	join := func(u string, at time.Time) MemberEvent { return MemberEvent{UserID: u, Kind: MemberJoined, At: at} }
	leave := func(u string, at time.Time) MemberEvent { return MemberEvent{UserID: u, Kind: MemberLeft, At: at} }
	members := []MemberEvent{
		join("j1", wk(-1, time.Hour)), leave("j1", wk(-1, 25*time.Hour)), join("j1", wk(0)),
		join("j2", wk(0, 2*time.Hour)), leave("j2", wk(0, 26*time.Hour+time.Second)),
		join("j3", wk(0, 3*time.Hour)), leave("j3", wk(0, 4*time.Hour)),
		join("old", wk(-4)),
		join("t1", wk(-2)), join("t2", wk(-3)),
	}
	days := []ActiveDay{{UserID: "t1", Day: wk(-2)}}

	r := Build(Input{Now: now, State: collecting, Members: members, Days: days})
	s, nm := r.Summary, r.Brief.NewMembers

	if s.Joined != known(5) {
		t.Errorf("joined over 4 weeks = %+v, want 5: a rejoin counts once, and W-4 is outside", s.Joined)
	}
	if s.LeftShare != known(0.4) {
		t.Errorf("left within 24 h = %+v, want 0.4: exactly 24 h counts, a second more doesn't", s.LeftShare)
	}
	if nm.Joined != known(3) || nm.Left != known(1) {
		t.Errorf("the brief's joins this week = %+v, left %+v; want 3, a rejoin included, and 1", nm.Joined, nm.Left)
	}
	if s.TalkedShare != known(1.0/3) {
		t.Errorf("talked within 7 days = %+v, want 1 of old, t1 and t2, who stayed", s.TalkedShare)
	}
}

func TestCohortFunnel(t *testing.T) {
	joined := wk(-6, 10*time.Hour)
	joinDay := wk(-6)
	on := func(u string, days ...int) []ActiveDay {
		var out []ActiveDay
		for _, d := range days {
			out = append(out, ActiveDay{UserID: u, Day: joinDay.AddDate(0, 0, d)})
		}
		return out
	}
	var members []MemberEvent
	for _, u := range []string{"left", "a30", "late", "edge30", "outside30", "skipped"} {
		members = append(members, MemberEvent{UserID: u, Kind: MemberJoined, At: joined})
	}
	members = append(members, MemberEvent{UserID: "left", Kind: MemberLeft, At: joined.Add(time.Hour)},
		MemberEvent{UserID: "recent", Kind: MemberJoined, At: wk(-3)})
	var days []ActiveDay
	days = append(days, on("left", 0, 25)...)
	days = append(days, on("a30", 7, 23)...)
	days = append(days, on("late", 8)...)
	days = append(days, on("edge30", 0, 30)...)
	days = append(days, on("outside30", 0, 22, 31)...)
	days = append(days, on("skipped", 25)...)

	c := Build(Input{Now: now, State: collecting, Members: members, Days: days}).Cohort

	want := Cohort{Known: true, Joined: 6, Stayed: 5, Talked: 3, Active30: 2}
	if c != want {
		t.Errorf("cohort = %+v, want %+v", c, want)
	}
	if c := Build(Input{Now: now, State: State{StartedAt: wk(-7, time.Hour)}, Members: members, Days: days}).Cohort; c.Known {
		t.Errorf("cohort with joins counted from inside W-7 = %+v, want unknown", c)
	}
}

// Each wait is also a W-1 joiner's, doubled, for the brief's first reply.
func TestCohortMedianReply(t *testing.T) {
	joined, lastWeek := wk(-5), wk(-1)
	tests := []struct {
		name              string
		waits             []time.Duration // a negative wait is a message with no reply
		messaged, replied int
		median            Maybe[time.Duration]
	}{
		{"odd count", []time.Duration{30 * time.Minute, 10 * time.Minute, 20 * time.Minute}, 3, 3, known(20 * time.Minute)},
		{"even count", []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 50 * time.Minute}, 4, 4, known(25 * time.Minute)},
		{"no reply is left out", []time.Duration{10 * time.Minute, -1}, 2, 1, known(10 * time.Minute)},
		{"nobody replied", []time.Duration{-1}, 1, 0, Maybe[time.Duration]{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var members []MemberEvent
			var replies []FirstReply
			for i, w := range tc.waits {
				for _, j := range []struct {
					u    string
					at   time.Time
					wait time.Duration
				}{{string(rune('a' + i)), joined, w}, {string(rune('A' + i)), lastWeek, 2 * w}} {
					members = append(members, MemberEvent{UserID: j.u, Kind: MemberJoined, At: j.at})
					r := FirstReply{UserID: j.u, JoinedAt: j.at, FirstMessageAt: j.at.Add(time.Hour)}
					if w >= 0 {
						r.RepliedAt = r.FirstMessageAt.Add(j.wait)
					}
					replies = append(replies, r)
				}
			}
			r := Build(Input{Now: now, State: collecting, Members: members, Replies: replies})
			c, fr := r.Cohort, r.Brief.FirstReply
			if c.Messaged != tc.messaged || c.Replied != tc.replied || c.MedianReply != tc.median {
				t.Errorf("messaged %d, replied %d, median %+v; want %d, %d, %+v", c.Messaged, c.Replied, c.MedianReply, tc.messaged, tc.replied, tc.median)
			}
			lastMedian := Maybe[time.Duration]{V: 2 * tc.median.V, Known: tc.median.Known}
			if fr.Median != lastMedian || fr.Before != tc.median {
				t.Errorf("the brief's first reply = %+v against %+v; want %+v against %+v", fr.Median, fr.Before, lastMedian, tc.median)
			}
		})
	}
}

func TestMonths(t *testing.T) {
	sep := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	days := []ActiveDay{
		{UserID: "quiet", Day: sep},
		{UserID: "back", Day: oct}, {UserID: "back", Day: oct.AddDate(0, 0, -61)},
		{UserID: "stayed", Day: oct}, {UserID: "stayed", Day: oct.AddDate(0, 0, -60)},
		{UserID: "n1", Day: oct}, {UserID: "n2", Day: oct},
	}
	members := []MemberEvent{
		{UserID: "n1", Kind: MemberJoined, At: oct}, {UserID: "n1", Kind: MemberJoined, At: oct.Add(time.Hour)},
		{UserID: "n2", Kind: MemberJoined, At: sep},
	}
	tests := []struct {
		name  string
		state State
		want  Month
	}{
		{"new, returned after 60 quiet days and not new, went quiet", collecting,
			Month{Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), New: known(1), Returned: known(1), Quiet: known(1)}},
		{"unknown where the month or its lookback starts before collection", State{StartedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)},
			Month{Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), New: known(1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Build(Input{Now: now, State: tc.state, Days: days, Members: members})
			if got := r.Months[2]; got != tc.want {
				t.Errorf("October = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHeatmapAveragesCoveredHours(t *testing.T) {
	// Half an hour either side of Monday 2100z, in W0.
	spans := []Span{{Start: wk(0, 20*time.Hour+30*time.Minute), End: wk(0, 21*time.Hour+30*time.Minute)}}
	tests := []struct {
		name     string
		started  time.Time
		mon20    Maybe[float64]
		mon21    Maybe[float64]
		sunday23 bool
	}{
		{"12 covered weeks", collecting.StartedAt, known(0.5 / 12), known(0.5 / 12), true},
		{"collection started at W-2 Monday 2015z", wk(-2, 20*time.Hour+15*time.Minute), known(0.5 / 2), known(0.5 / 3), true},
		{"collection started after the report week", now, Maybe[float64]{}, Maybe[float64]{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Build(Input{Now: now, State: State{StartedAt: tc.started}, Spans: spans}).Heatmap
			if h[0][20] != tc.mon20 || h[0][21] != tc.mon21 {
				t.Errorf("Monday 2000z = %+v, 2100z = %+v; want %+v, %+v", h[0][20], h[0][21], tc.mon20, tc.mon21)
			}
			if h[6][23].Known != tc.sunday23 {
				t.Errorf("Sunday 2300z known = %v, want %v", h[6][23].Known, tc.sunday23)
			}
		})
	}
}

func TestVoicePeakIsFromTheReportWeekAlone(t *testing.T) {
	var spans []Span
	for range 3 {
		spans = append(spans, Span{Start: wk(-1, 5*24*time.Hour+21*time.Hour), End: wk(-1, 5*24*time.Hour+22*time.Hour)})
	}
	spans = append(spans, Span{Start: wk(0, 24*time.Hour+10*time.Hour), End: wk(0, 24*time.Hour+11*time.Hour)})

	peak := Build(Input{Now: now, State: collecting, Spans: spans}).Brief.VoicePeak

	if want := known(Peak{Day: time.Tuesday, Hour: 10, Average: 1}); peak != want {
		t.Errorf("voice peak = %+v, want %+v", peak, want)
	}
}

// readOver is in read as the store reads it over rg.
func readOver(in Input, rg Ranges) Input {
	inWeeks := func(t time.Time) bool { return !t.Before(rg.Weeks.Start) && t.Before(rg.Weeks.End) }
	out := Input{Now: in.Now, State: in.State, Sections: in.Sections}
	for _, w := range in.Weeks {
		if inWeeks(w.Week) {
			out.Weeks = append(out.Weeks, w)
		}
	}
	for _, d := range in.Days {
		if !d.Day.Before(dayOf(rg.DaysFrom)) && !d.Day.After(in.Now) {
			out.Days = append(out.Days, d)
		}
	}
	for _, e := range in.Members {
		if !e.At.Before(rg.MembersFrom) {
			out.Members = append(out.Members, e)
		}
	}
	for _, s := range in.Spans {
		if s.Start.Before(rg.Weeks.End) && s.End.After(rg.Weeks.Start) {
			if rg.Weeks.End.Before(s.End) {
				s.End = rg.Weeks.End
			}
			out.Spans = append(out.Spans, Span{Start: later(s.Start, rg.Weeks.Start), End: s.End})
		}
	}
	for _, r := range in.Replies {
		if !r.JoinedAt.Before(rg.Joins.Start) && r.JoinedAt.Before(rg.Joins.End) {
			out.Replies = append(out.Replies, r)
		}
	}
	return out
}

// A report over the rows ReadRanges names is the report over every row.
func TestReadRangesHoldEveryRowTheReportUses(t *testing.T) {
	in := Input{Now: now, State: collecting, Sections: []Section{{CategoryID: "c"}}}
	for k := -20; k <= 0; k++ {
		for _, c := range []string{"", "c"} {
			in.Weeks = append(in.Weeks, WeekTotal{Week: wk(k), CategoryID: c, Messages: 50 - k, VoiceSeconds: int64(3600 * (12 - k)), VoicePeople: 3, Active: 20 - k})
		}
	}
	// A member joins every day over 250 days, some leave within the hour,
	// and each first talks 0 to 70 days after joining.
	for i := range 250 {
		u, joined := string(rune(0x100+i)), now.Add(-time.Duration(i)*day-time.Duration(i%24)*time.Hour)
		in.Members = append(in.Members, MemberEvent{UserID: u, Kind: MemberJoined, At: joined})
		if i%5 == 0 {
			in.Members = append(in.Members, MemberEvent{UserID: u, Kind: MemberLeft, At: joined.Add(time.Hour)})
		}
		for _, d := range []int{i % 71, i%71 + 20 + i%13} {
			if at := dayOf(joined).AddDate(0, 0, d); !at.After(now) {
				in.Days = append(in.Days, ActiveDay{UserID: u, Day: at})
			}
		}
		in.Replies = append(in.Replies, FirstReply{UserID: u, JoinedAt: joined, FirstMessageAt: joined.Add(time.Hour), RepliedAt: joined.Add(time.Duration(60+i) * time.Minute)})
		in.Spans = append(in.Spans, Span{Start: joined, End: joined.Add(time.Duration(30+i%90) * time.Minute)})
	}

	if got, want := Build(readOver(in, ReadRanges(now))), Build(in); !reflect.DeepEqual(got, want) {
		t.Errorf("report over the read ranges = %+v\nwant %+v", got, want)
	}
}
