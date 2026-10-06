package panel

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/7cav/cavbot2/commands"
)

// foxholeView is what the Foxhole page renders from: the holder list, read
// from the gateway state at this load, narrowed to the search and the
// filter.
type foxholeView struct {
	// ListReady is the member list complete at this load. Until it is, a
	// notice takes the holder list's place, so the page never shows a
	// partial list.
	ListReady bool
	// Query is the search as typed, rendered back into the search box.
	Query string
	// Filter names the filter the list shows: filterAll when the address
	// names none, or one the page doesn't know.
	Filter string
	// Filters are the filter links, each with how many holders matching the
	// search it lists.
	Filters []filterLink
	Holders []holderRow
	// NoHolders is nobody holding a Foxhole role at this load, whatever the
	// search. The empty list then says so, instead of saying no holder
	// matches.
	NoHolders bool
}

// filterLink is one filter link above the holder list. Each keeps the
// search.
type filterLink struct {
	Name  string
	Label string
	Count int
	Href  string
	On    bool
}

// holderFilter is one of the holder list's filters: which holders it
// lists.
type holderFilter struct {
	name  string
	label string
	keeps func(holderRow) bool
}

// filterAll is the filter that lists every holder, the list's default.
const filterAll = "all"

// holderFilters are the holder list's filters, in the order their links
// show.
var holderFilters = []holderFilter{
	{filterAll, "All", func(holderRow) bool { return true }},
	{"internal", "Internal", func(h holderRow) bool { return h.Internal }},
	{"external", "External", func(h holderRow) bool { return h.External }},
	{"flagged", "Flagged", holderRow.flagged},
}

// holderRow is one Foxhole role holder as the holder list shows them.
type holderRow struct {
	ID string
	// DisplayName is the name the member shows in the server: their server
	// nickname, else their global name, else their username.
	DisplayName string
	Username    string
	Internal    bool
	External    bool
	// RankRole is the name of the member's rank role, the most senior on
	// the rank ladder they hold. Empty when they hold none.
	RankRole string
	// NoRankRole is the "no rank role" flag: an Internal holder with no
	// rank role. Display only; the bot acts on no flag.
	NoRankRole bool
}

// foxholePath is the Foxhole page's address.
const foxholePath = "/foxhole"

// The Foxhole page's query parameters: the search, from the search form,
// and the filter, from the filter links.
const (
	paramQuery  = "q"
	paramFilter = "filter"
)

// foxholeService reads the Foxhole page from the gateway state, through the
// manager seam the hub page reads the guild through.
type foxholeService struct {
	manager commands.TempVCManager
	guildID string
}

// foxholePage is GET /foxhole, the Foxhole page. It reads the guild's roles
// and the member list from the gateway state and makes no Discord call. The
// search is a plain GET form, so it works without script.
func (p *Panel) foxholePage(w http.ResponseWriter, r *http.Request, sess session) {
	data := sess.page("Foxhole")
	data.Page = pageFoxhole
	q := r.URL.Query()
	data.Foxhole = p.foxhole.view(q.Get(paramQuery), q.Get(paramFilter))
	p.render(w, http.StatusOK, "foxhole", data)
}

// flagged reports whether the holder carries a flag.
func (h holderRow) flagged() bool { return h.NoRankRole }

// matches reports whether the holder's display name, username or Discord
// ID holds the search, ignoring case. An empty search matches everyone.
func (h holderRow) matches(search string) bool {
	return strings.Contains(strings.ToLower(h.DisplayName), search) ||
		strings.Contains(strings.ToLower(h.Username), search) ||
		strings.Contains(h.ID, search)
}

// view reads the Foxhole page from one snapshot of the guild's roles and
// one of its member list, through the manager seam, never Discord's API.
// The Foxhole roles are found by exact name, as the commands find them. The
// holder list keeps the holders who match query and the filter named. A
// member list that isn't complete holds no members, and the page has no
// holder list.
func (s foxholeService) view(query, filter string) foxholeView {
	list := s.manager.MemberList(s.guildID)
	if list.Status != commands.MemberListComplete {
		return foxholeView{}
	}
	search := strings.ToLower(strings.TrimSpace(query))
	guild := s.manager.GuildData(s.guildID)
	internalName, externalName := commands.FoxholeRoleNames()
	var internalID, externalID string
	roleNames := make(map[string]string, len(guild.Roles))
	for _, r := range guild.Roles {
		roleNames[r.ID] = r.Name
		switch {
		case r.Name == internalName && internalID == "":
			internalID = r.ID
		case r.Name == externalName && externalID == "":
			externalID = r.ID
		}
	}
	var matched []holderRow
	holders := 0
	for _, mem := range list.Members {
		row := holderRow{
			ID:          mem.ID,
			DisplayName: cmp.Or(mem.Nick, mem.GlobalName, mem.Username),
			Username:    mem.Username,
			Internal:    internalID != "" && slices.Contains(mem.RoleIDs, internalID),
			External:    externalID != "" && slices.Contains(mem.RoleIDs, externalID),
		}
		if !row.Internal && !row.External {
			continue
		}
		holders++
		if rank, ok := commands.SeniorRankRole(mem.RoleIDs); ok {
			row.RankRole = cmp.Or(roleNames[rank], rank)
		}
		row.NoRankRole = row.Internal && row.RankRole == ""
		if row.matches(search) {
			matched = append(matched, row)
		}
	}
	slices.SortFunc(matched, func(a, b holderRow) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)), cmp.Compare(a.ID, b.ID))
	})
	view := listView(matched, query, filter)
	view.NoHolders = holders == 0
	return view
}

// listView builds the page from the holders matching the search: a link for
// each filter with its count, and the holders the named filter keeps.
func listView(matched []holderRow, query, filter string) foxholeView {
	page := foxholeView{ListReady: true, Query: query, Filter: filterAll}
	for _, f := range holderFilters {
		if f.name == filter {
			page.Filter = f.name
		}
	}
	for _, f := range holderFilters {
		link := filterLink{Name: f.name, Label: f.label, Href: foxholeAddress(query, f.name), On: f.name == page.Filter}
		for _, h := range matched {
			if f.keeps(h) {
				link.Count++
				if link.On {
					page.Holders = append(page.Holders, h)
				}
			}
		}
		page.Filters = append(page.Filters, link)
	}
	return page
}

// foxholeAddress is the Foxhole page's address with the search and the
// filter, leaving out each that is the default.
func foxholeAddress(query, filter string) string {
	v := url.Values{}
	if filter != filterAll {
		v.Set(paramFilter, filter)
	}
	if query != "" {
		v.Set(paramQuery, query)
	}
	if len(v) == 0 {
		return foxholePath
	}
	return foxholePath + "?" + v.Encode()
}
