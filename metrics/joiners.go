package metrics

import (
	"slices"
	"time"
)

// Member is a current server member as the Joiners page counts them.
type Member struct {
	ID       string
	JoinedAt time.Time
	RoleIDs  []string
}

// JoinersInput is everything the Joiners page's figures are built from.
// Events and Days are read over ReadRanges(Now).
type JoinersInput struct {
	Now time.Time
	// Week is the Monday of the selected week.
	Week      time.Time
	State     State
	Events    []MemberEvent
	Days      []ActiveDay
	Members   []Member
	AORoles   []string
	RankRoles []string
}

// Joiners is the Joiners page's figures.
type Joiners struct {
	// AOs has one row per AO role, in the order given.
	AOs        []JoinerRow
	NoAO, Left JoinerRow
	Pipeline   Pipeline
}

// JoinerRow is one row of the AOs table. Average is the mean of the 4
// weeks before the selected week, and Trend ends at the selected week.
type JoinerRow struct {
	RoleID  string
	Count   Maybe[int]
	Average Maybe[float64]
	Trend   [TrendWeeks]Maybe[int]
	// Members joined in the selected week, latest first. Left has none.
	Members []Member
}

// Pipeline is the Cohort's joiners as a nested funnel, each step a subset
// of the one before. AOKnown covers HoldsAO and the steps after it, which
// mean nothing while no AO roles are set.
type Pipeline struct {
	Known, AOKnown                             bool
	Joined, Stayed, HoldsAO, Talked, HoldsRank int
}

// BuildJoiners makes the Joiners page's figures. Member counts come from
// the member list and are always known. Joined and left counts the users
// with a join in a week who are not members now, and is known for weeks
// inside both collection and the retention of raw rows.
func BuildJoiners(in JoinersInput) Joiners {
	b := newBuilder(Input{Now: in.Now, State: in.State, Members: in.Events, Days: in.Days})
	byWeek := map[int64][]Member{}
	present := map[string]Member{}
	for _, m := range in.Members {
		k := WeekStart(m.JoinedAt).Unix()
		byWeek[k] = append(byWeek[k], m)
		present[m.ID] = m
	}
	listed := func(hold func(Member) bool) func(time.Time) []Member {
		return func(wk time.Time) []Member {
			var out []Member
			for _, m := range byWeek[wk.Unix()] {
				if hold(m) {
					out = append(out, m)
				}
			}
			return out
		}
	}
	j := Joiners{Pipeline: b.pipeline(present, in.AORoles, in.RankRoles)}
	for _, r := range in.AORoles {
		row := joinerRow(in.Week, listed(func(m Member) bool { return slices.Contains(m.RoleIDs, r) }))
		row.RoleID = r
		j.AOs = append(j.AOs, row)
	}
	j.NoAO = joinerRow(in.Week, listed(func(m Member) bool { return !holdsAny(m, in.AORoles) }))
	cover := later(in.State.StartedAt, in.Now.Add(-RawRetention))
	j.Left = trendRow(in.Week, func(wk time.Time) Maybe[int] {
		n := 0
		for _, jn := range b.joinedIn(wk, wk.Add(week)) {
			if _, ok := present[jn.user]; !ok {
				n++
			}
		}
		return maybe(n, b.known(wk, cover))
	})
	return j
}

func joinerRow(selected time.Time, members func(time.Time) []Member) JoinerRow {
	r := trendRow(selected, func(wk time.Time) Maybe[int] { return maybe(len(members(wk)), true) })
	r.Members = members(selected)
	slices.SortFunc(r.Members, func(a, b Member) int { return b.JoinedAt.Compare(a.JoinedAt) })
	return r
}

func trendRow(selected time.Time, count func(time.Time) Maybe[int]) JoinerRow {
	at := func(k int) time.Time { return selected.Add(time.Duration(k) * week) }
	var r JoinerRow
	for i := range r.Trend {
		r.Trend[i] = count(at(i - (TrendWeeks - 1)))
	}
	r.Count = r.Trend[TrendWeeks-1]
	sum, ok := 0, true
	for _, c := range r.Trend[TrendWeeks-5 : TrendWeeks-1] {
		sum, ok = sum+c.V, ok && c.Known
	}
	r.Average = maybe(float64(sum)/4, ok)
	return r
}

func (b *builder) pipeline(present map[string]Member, ao, rank []string) Pipeline {
	js, ok := b.cohortJoiners()
	if !ok {
		return Pipeline{}
	}
	p := Pipeline{Known: true, AOKnown: len(ao) > 0, Joined: len(js)}
	for _, j := range js {
		if b.left(j) {
			continue
		}
		p.Stayed++
		m, here := present[j.user]
		if !here || !holdsAny(m, ao) {
			continue
		}
		p.HoldsAO++
		if !b.talked(j) {
			continue
		}
		p.Talked++
		if holdsAny(m, rank) {
			p.HoldsRank++
		}
	}
	return p
}

func holdsAny(m Member, roles []string) bool {
	return slices.ContainsFunc(m.RoleIDs, func(r string) bool { return slices.Contains(roles, r) })
}

// JoinerWeeks is the Mondays of the first and last weeks the Joiners page
// offers at now: the week holding now-RawRetention and the current week.
func JoinerWeeks(now time.Time) (first, last time.Time) {
	return WeekStart(now.Add(-RawRetention)), WeekStart(now)
}

// JoinerWeek is the Monday of the week holding the YYYY-MM-DD date s,
// clamped to JoinerWeeks(now), or the current week when s doesn't parse.
func JoinerWeek(s string, now time.Time) time.Time {
	first, last := JoinerWeeks(now)
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return last
	}
	wk := WeekStart(t)
	switch {
	case wk.Before(first):
		return first
	case wk.After(last):
		return last
	}
	return wk
}
