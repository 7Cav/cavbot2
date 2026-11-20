package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/metrics"
	"github.com/7cav/cavbot2/utils"
)

// The Joiners page (#231, GLOSSARY.md, ADR 0022) lists a week's new server
// members by AO role, with the whole server's pipeline in group totals.
// metrics.BuildJoiners makes the figures; this file reads them and lays
// them out.

const joinersPath = "/joiners"

// The Joiners page's query parameters: the Monday of the week shown, and
// the row whose list shows, an AO role's ID or aoNone.
const (
	paramWeek = "week"
	paramAO   = "ao"
)

// Row keys that are not role IDs.
const (
	aoNone = "none"
	aoLeft = "left"
)

const noAORoleName = "No AO role"

type joinersView struct {
	// ListNotice stands in for every block while the member list isn't
	// complete, since each of them reads it.
	ListNotice *listNotice
	// Disconnected is the bot's gateway connection down with the list
	// complete: the page shows in full, and says the figures may be out of
	// date.
	Disconnected bool
	Week         string
	// Prev and Next are the addresses of the weeks either side, empty past
	// the weeks the page offers.
	Prev, Next string
	NoAORoles  bool
	// SettingsLink is a link to the AO roles on the Settings page, for a
	// panel admin.
	SettingsLink bool
	Rows         []joinerRowView
	List         joinerListView
	Pipeline     []funnelStep
}

// joinerRowView is one row of the AOs table. Href is empty for the row
// that has no list.
type joinerRowView struct {
	Key, Name, Href string
	Selected, Quiet bool
	Count, Average  string
	Spark           sparkline
}

type joinerListView struct {
	Name, Count string
	Members     []joinerMember
}

type joinerMember struct {
	DisplayName, Username string
	JoinedAt              time.Time
}

// joinersPage is GET /joiners, read under the hub page's time budget and
// with its failures answered the way the Metrics page answers its own.
func (p *Panel) joinersPage(w http.ResponseWriter, r *http.Request, sess session) {
	start := panelClock.Now()
	ctx, cancel := panelClock.WithTimeout(r.Context(), hubPageBudget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	reads := newPageReads(start, deadline)
	q := r.URL.Query()
	week, ao := metrics.JoinerWeek(q.Get(paramWeek), start), q.Get(paramAO)
	view, err := p.joiners(ctx, reads, start, week, ao)
	switch {
	case errors.Is(err, context.Canceled):
		utils.Info("Panel page abandoned", "step", "joiners page", "username", sess.username, "forum_user_id", sess.userID)
		return
	case errors.Is(err, errNoGuildData):
		p.noGuildData(w, r, sess, "joiners page", joinersAddress(week, ao), err)
		return
	case err != nil:
		var budget *budgetReport
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			budget = reads.report(hubPageBudget)
		}
		p.pageFailed(w, r, sess, "joiners page", joinersAddress(week, ao), err, budget)
		return
	}
	view.SettingsLink = sess.access.panelAdmin
	data := sess.page("Joiners")
	data.Page, data.Joiners = pageJoiners, view
	p.render(w, r, http.StatusOK, "joiners", data)
}

// joiners reads the guild's roles from the gateway state, the AO roles and
// the metrics rows from the store, then the member list, awaited as the
// Foxhole page awaits it. The AO roles are the stored ones the guild still
// has, highest position first, as the Settings picker shows them.
func (p *Panel) joiners(ctx context.Context, reads *pageReads, now, week time.Time, ao string) (joinersView, error) {
	s := p.hubs.forPage(reads)
	guild, err := s.readGuild(ctx)
	if err != nil {
		return joinersView{}, err
	}
	stored, err := s.deps.Store.GetAORoles(ctx, s.deps.GuildID)
	if err != nil {
		return joinersView{}, fmt.Errorf("%s: %w", aoRolesRead, err)
	}
	in := metrics.JoinersInput{Now: now, Week: week}
	for _, r := range guild.info.eligible {
		if slices.Contains(stored.RoleIDs, r.ID) {
			in.AORoles = append(in.AORoles, r.ID)
		}
	}
	for _, rank := range s.deps.Runtime.RankLadder().Ranks() {
		if rank.RoleID != "" {
			in.RankRoles = append(in.RankRoles, rank.RoleID)
		}
	}
	m, rg := s.deps.Metrics, metrics.ReadRanges(now)
	rd := storeReads{reads: reads}
	rd.read(metricsStateRead, func() (e error) { in.State, e = m.MetricsState(ctx); return e })
	rd.read(activeDaysRead, func() (e error) { in.Days, e = m.ActiveDays(ctx, rg.DaysFrom, now); return e })
	rd.read(memberEventsRead, func() (e error) { in.Events, e = m.MemberEvents(ctx, rg.MembersFrom); return e })
	if rd.err != nil {
		return joinersView{}, rd.err
	}
	list, err := p.foxhole.memberList(ctx, true)
	if err != nil {
		return joinersView{}, err
	}
	if list.Status != commands.MemberListComplete {
		notice := noticeFor(list)
		notice.Reload = joinersAddress(week, ao)
		return joinersView{ListNotice: notice}, nil
	}
	listed := make(map[string]commands.ListedMember, len(list.Members))
	for _, lm := range list.Members {
		if lm.Bot {
			continue
		}
		in.Members = append(in.Members, metrics.Member{ID: lm.ID, JoinedAt: lm.JoinedAt, RoleIDs: lm.RoleIDs})
		listed[lm.ID] = lm
	}
	v := joinersViewOf(metrics.BuildJoiners(in), guild.info.roleNames, listed, now, week, ao)
	v.Disconnected = !list.Connected
	return v, nil
}

// joinersViewOf lays out the figures. The list shows the AO ao names, or
// the members with no AO role for aoNone, and the first AO for anything
// else.
func joinersViewOf(j metrics.Joiners, roleNames map[string]string, listed map[string]commands.ListedMember, now, week time.Time, ao string) joinersView {
	selected, list, rowOf := aoNone, joinerListView{Name: noAORoleName}, j.NoAO
	if ao != aoNone && len(j.AOs) > 0 {
		r := j.AOs[max(0, slices.IndexFunc(j.AOs, func(r metrics.JoinerRow) bool { return r.RoleID == ao }))]
		selected, list.Name, rowOf = r.RoleID, roleNames[r.RoleID], r
	}
	row := func(key, name string, r metrics.JoinerRow) joinerRowView {
		return joinerRowView{Key: key, Name: name, Href: joinersAddress(week, key), Selected: key == selected,
			Count: shown(r.Count, fmtCount), Average: shown(r.Average, fmtAverage), Spark: sparkOf(r.Trend, "Joiners per week, last 12 weeks")}
	}
	v := joinersView{Week: weekSpan(week), NoAORoles: len(j.AOs) == 0}
	for _, r := range j.AOs {
		v.Rows = append(v.Rows, row(r.RoleID, roleNames[r.RoleID], r))
	}
	none, left := row(aoNone, noAORoleName, j.NoAO), row(aoLeft, "Joined and left", j.Left)
	none.Quiet, left.Quiet, left.Href = true, true, ""
	v.Rows = append(v.Rows, none, left)
	first, last := metrics.JoinerWeeks(now)
	if week.After(first) {
		v.Prev = joinersAddress(week.AddDate(0, 0, -7), selected)
	}
	if week.Before(last) {
		v.Next = joinersAddress(week.AddDate(0, 0, 7), selected)
	}
	list.Count = shown(rowOf.Count, fmtCount)
	for _, m := range rowOf.Members {
		lm := listed[m.ID]
		list.Members = append(list.Members, joinerMember{DisplayName: lm.DisplayName(), Username: lm.Username, JoinedAt: m.JoinedAt})
	}
	v.List = list
	pl := j.Pipeline
	v.Pipeline = funnelOf([]funnelCount{{"Joined", pl.Joined, pl.Known}, {"Stayed past 24 h", pl.Stayed, pl.Known},
		{"Holds an AO role", pl.HoldsAO, pl.AOKnown}, {"Talked", pl.Talked, pl.AOKnown}, {"Holds a rank role", pl.HoldsRank, pl.AOKnown}})
	return v
}

// joinersAddress is the Joiners page showing the week and the row given.
func joinersAddress(week time.Time, ao string) string {
	q := url.Values{paramWeek: {week.Format(time.DateOnly)}}
	if ao != "" {
		q.Set(paramAO, ao)
	}
	return joinersPath + "?" + q.Encode()
}

func fmtAverage(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
