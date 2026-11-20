package metrics

import (
	"reflect"
	"testing"
	"time"
)

func memberIDs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// weekInput is the Joiners input for week W0 with the given members and AO roles.
func weekInput(members []Member, ao []string) JoinersInput {
	return JoinersInput{Now: now, Week: w0, State: collecting, Members: members, AORoles: ao}
}

func TestJoinerTableCountsMembersByJoinWeek(t *testing.T) {
	members := []Member{
		{ID: "edge", JoinedAt: wk(0), RoleIDs: []string{"a"}},
		{ID: "both", JoinedAt: wk(1, -time.Second), RoleIDs: []string{"a", "b"}},
		{ID: "next", JoinedAt: wk(1), RoleIDs: []string{"a"}},
		{ID: "none", JoinedAt: wk(0, time.Hour), RoleIDs: []string{"rank"}},
		{ID: "p1", JoinedAt: wk(-1), RoleIDs: []string{"a"}},
		{ID: "p3", JoinedAt: wk(-3), RoleIDs: []string{"a"}},
		{ID: "p3b", JoinedAt: wk(-3, time.Hour), RoleIDs: []string{"a"}},
		{ID: "p5", JoinedAt: wk(-5), RoleIDs: []string{"a"}},
	}
	j := BuildJoiners(weekInput(members, []string{"b", "a"}))

	if len(j.AOs) != 2 || j.AOs[0].RoleID != "b" || j.AOs[1].RoleID != "a" {
		t.Fatalf("AO rows = %+v, want b then a", j.AOs)
	}
	a, b := j.AOs[1], j.AOs[0]
	if got := memberIDs(a.Members); !reflect.DeepEqual(got, []string{"both", "edge"}) {
		t.Errorf("a's members = %v, want both then edge: Monday 00:00 is in the week, the next Monday isn't", got)
	}
	if a.Count != known(2) || b.Count != known(1) {
		t.Errorf("a = %+v, b = %+v; want 2 and 1, a member with two AO roles counting in each", a.Count, b.Count)
	}
	if a.Average != known(0.75) {
		t.Errorf("a's average = %+v, want 3 joins over the 4 weeks before", a.Average)
	}
	if a.Trend[TrendWeeks-1] != known(2) || a.Trend[TrendWeeks-2] != known(1) {
		t.Errorf("a's trend ends %+v, %+v; want 1 then the selected week's 2", a.Trend[TrendWeeks-2], a.Trend[TrendWeeks-1])
	}
	if got := memberIDs(j.NoAO.Members); !reflect.DeepEqual(got, []string{"none"}) {
		t.Errorf("no AO role = %v, want none", got)
	}
	if got := BuildJoiners(weekInput(members, nil)).NoAO.Count; got != known(3) {
		t.Errorf("no AO role with none set = %+v, want every joiner of the week", got)
	}
}

func TestJoinedAndLeft(t *testing.T) {
	join := func(u string, at time.Time) MemberEvent { return MemberEvent{UserID: u, Kind: MemberJoined, At: at} }
	leave := func(u string, at time.Time) MemberEvent { return MemberEvent{UserID: u, Kind: MemberLeft, At: at} }
	events := []MemberEvent{
		join("gone", wk(0, time.Hour)), leave("gone", wk(0, 2*time.Hour)),
		join("here", wk(0, time.Hour)),
		join("again", wk(-2)), leave("again", wk(-2, time.Hour)), join("again", wk(0, 3*time.Hour)), leave("again", wk(0, 4*time.Hour)),
	}
	members := []Member{{ID: "here", JoinedAt: wk(0, time.Hour)}}
	tests := []struct {
		name    string
		week    time.Time
		state   State
		count   Maybe[int]
		average Maybe[float64]
		trend2  Maybe[int] // W-2
	}{
		{"members still here are left out", w0, collecting, known(2), known(0.25), known(1)},
		{"unknown before collection started", w0, State{StartedAt: wk(-2, time.Hour)}, known(2), Maybe[float64]{}, Maybe[int]{}},
		{"unknown before collection", w0, State{}, Maybe[int]{}, Maybe[float64]{}, Maybe[int]{}},
		{"unknown in the week raw rows are pruned inside", time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC), collecting, Maybe[int]{}, Maybe[float64]{}, Maybe[int]{}},
		{"known from the week after", time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC), collecting, known(0), Maybe[float64]{}, Maybe[int]{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := BuildJoiners(JoinersInput{Now: now, Week: tc.week, State: tc.state, Events: events, Members: members}).Left
			if l.Count != tc.count || l.Average != tc.average || l.Trend[TrendWeeks-3] != tc.trend2 {
				t.Errorf("joined and left = %+v, average %+v, W-2 %+v; want %+v, %+v, %+v", l.Count, l.Average, l.Trend[TrendWeeks-3], tc.count, tc.average, tc.trend2)
			}
		})
	}
}

func TestPipelineNests(t *testing.T) {
	joined := wk(-6, 10*time.Hour)
	var events []MemberEvent
	for _, u := range []string{"left", "noAO", "gone", "silent", "talker", "ranked"} {
		events = append(events, MemberEvent{UserID: u, Kind: MemberJoined, At: joined})
	}
	events = append(events, MemberEvent{UserID: "left", Kind: MemberLeft, At: joined.Add(time.Hour)},
		MemberEvent{UserID: "recent", Kind: MemberJoined, At: wk(-3)})
	var days []ActiveDay
	for _, u := range []string{"left", "noAO", "gone", "talker", "ranked", "recent"} {
		days = append(days, ActiveDay{UserID: u, Day: wk(-6)})
	}
	members := []Member{
		{ID: "left", RoleIDs: []string{"ao", "rank"}},
		{ID: "noAO", RoleIDs: []string{"rank"}},
		{ID: "silent", RoleIDs: []string{"ao", "rank"}},
		{ID: "talker", RoleIDs: []string{"ao"}},
		{ID: "ranked", RoleIDs: []string{"other", "ao", "rank"}},
		{ID: "recent", RoleIDs: []string{"ao", "rank"}},
	}
	in := JoinersInput{Now: now, Week: w0, State: collecting, Events: events, Days: days, Members: members, AORoles: []string{"ao"}, RankRoles: []string{"rank"}}

	want := Pipeline{Known: true, Joined: 6, Stayed: 5, HoldsAO: 3, Talked: 2, HoldsRank: 1}
	if p := BuildJoiners(in).Pipeline; p != want {
		t.Errorf("pipeline = %+v, want %+v", p, want)
	}
	in.State = State{StartedAt: wk(-7, time.Hour)}
	if p := BuildJoiners(in).Pipeline; p.Known {
		t.Errorf("pipeline with joins counted from inside W-7 = %+v, want unknown", p)
	}
}

func TestJoinerWeekClamps(t *testing.T) {
	first := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	current := wk(1)
	tests := []struct {
		in   string
		want time.Time
	}{
		{"", current},
		{"11/04/2026", current},
		{"2026-11-02", w0},
		{"2026-11-08", w0},
		{"2027-01-04", current},
		{"2026-05-13", first},
		{"2026-05-10", first},
		{"2025-01-01", first},
	}
	for _, tc := range tests {
		if got := JoinerWeek(tc.in, now); !got.Equal(tc.want) {
			t.Errorf("JoinerWeek(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
