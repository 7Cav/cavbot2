package panel

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// testFoxholeGroupID is the Foxhole group the test panel is configured
// with, the production default.
const testFoxholeGroupID = 323

// The Foxhole manager addFoxholeManager adds: their forum user ID and
// username.
const (
	managerUserID   = 2468
	managerUsername = "Smith.F"
)

// addFoxholeManager makes the forum know a user in the Foxhole group by a
// secondary group, and in none of the panel's admin groups.
func addFoxholeManager(f *fakeForum) *forumAccount {
	return f.addUser(managerUserID, managerUsername, 2, []int{35, testFoxholeGroupID})
}

// follow follows the panel's redirects from res, the way a browser does,
// and returns the response it lands on.
func follow(t *testing.T, b *browser, res *http.Response) *http.Response {
	t.Helper()
	for hops := 0; res.StatusCode >= 300 && res.StatusCode <= 399; hops++ {
		if hops == 10 {
			t.Fatalf("more than 10 redirects, the last to %s", res.Header.Get("Location"))
		}
		res = b.get(location(t, res).RequestURI())
	}
	return res
}

// pageOf is the page a document is, the data-page on its <main>, or empty
// for a page that carries none, such as the no-access page.
func pageOf(t *testing.T, doc *html.Node) string {
	t.Helper()
	m := findElement(doc, "main", "", "")
	if m == nil {
		t.Fatal("page has no <main>")
	}
	page, _ := attrValue(m, "data-page")
	return page
}

// A forum user in the Foxhole group, by primary or by secondary group, and
// in none of the panel's admin groups, signs in and lands on the Foxhole
// page.
func TestFoxholeManagerSignsInToTheFoxholePage(t *testing.T) {
	cases := []struct {
		name      string
		primary   int
		secondary []int
	}{
		{"primary group", testFoxholeGroupID, []int{35}},
		{"secondary group", 2, []int{35, testFoxholeGroupID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			manager := w.forum.addUser(2468, "Smith.F", tc.primary, tc.secondary)

			res := follow(t, w.b, signInAs(t, w.forum, w.b, manager))

			if res.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", res.StatusCode)
			}
			if got := pageOf(t, parseHTML(t, res)); got != "foxhole" {
				t.Errorf("landed on page %q, want foxhole", got)
			}
		})
	}
}

// A Foxhole manager who isn't a panel admin and asks for a hub's edit form
// lands on the Foxhole page: no hub page shows them a hub, its settings or
// its change log.
func TestFoxholeManagerAskingForAHubLandsOnTheFoxholePage(t *testing.T) {
	w := newTestWorld(t, testHub())
	signInAs(t, w.forum, w.b, addFoxholeManager(w.forum))

	res := follow(t, w.b, w.b.get("/?hub="+strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)))

	doc := parseHTML(t, res)
	if got := pageOf(t, doc); got != "foxhole" {
		t.Errorf("landed on page %q, want foxhole", got)
	}
	for _, attr := range []string{"data-hub", "data-entry", "data-section"} {
		if found := valuesOf(doc, attr); len(found) > 0 {
			t.Errorf("the page carries %s elements %v, want none of the hub page's", attr, found)
		}
	}
}

// railLinks returns each link in the rail that carries data-nav, keyed by
// it: a nav link names the page it opens, and the mark names "landing",
// the page the user lands on.
func railLinks(t *testing.T, doc *html.Node) map[string]string {
	t.Helper()
	rail := findElement(doc, "aside", "", "")
	if rail == nil {
		t.Fatal("page has no rail")
	}
	links := map[string]string{}
	eachElement(rail, func(n *html.Node) {
		if n.Data != "a" {
			return
		}
		if nav, ok := attrValue(n, "data-nav"); ok {
			href, _ := attrValue(n, "href")
			links[nav] = href
		}
	})
	return links
}

// The rail shows the Hubs link to panel admins alone and the Foxhole link to
// panel admins and Foxhole managers, and every link it shows, the mark
// included, lands on the page it names. The panel admin row's Hubs link is a
// regression pin: panel admins keep the hub page as it was. The row in both
// groups catches a root that sends every Foxhole manager to the Foxhole
// page, panel admins among them.
func TestRailShowsOnlyThePagesTheUserOpensAndEachLinkOpensIt(t *testing.T) {
	cases := []struct {
		name      string
		primary   int
		secondary []int
		hubs      bool
		foxhole   bool
		landing   string
	}{
		{"panel admin", 2, []int{47}, true, true, "hubs"},
		{"Foxhole manager", 2, []int{testFoxholeGroupID}, false, true, "foxhole"},
		{"panel admin and Foxhole manager", 2, []int{47, testFoxholeGroupID}, true, true, "hubs"},
		{"neither", 2, []int{35}, false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			user := w.forum.addUser(2468, "Smith.F", tc.primary, tc.secondary)

			links := railLinks(t, parseHTML(t, follow(t, w.b, signInAs(t, w.forum, w.b, user))))

			if _, ok := links["hubs"]; ok != tc.hubs {
				t.Errorf("the rail shows the Hubs link: %v, want %v", ok, tc.hubs)
			}
			if _, ok := links["foxhole"]; ok != tc.foxhole {
				t.Errorf("the rail shows the Foxhole link: %v, want %v", ok, tc.foxhole)
			}
			if tc.landing == "" {
				return
			}
			for nav, href := range links {
				want := nav
				if nav == "landing" {
					want = tc.landing
				}
				res := follow(t, w.b, w.b.get(href))
				if got := pageOf(t, parseHTML(t, res)); res.StatusCode != http.StatusOK || got != want {
					t.Errorf("the %s link to %s lands on page %q with status %d, want page %q with 200", nav, href, got, res.StatusCode, want)
				}
			}
		})
	}
}

// A Foxhole manager whose forum account leaves the Foxhole group keeps their
// panel session: their next load of the Foxhole page is the no-access page,
// and once the forum puts them back in the group the page opens with no new
// sign-in.
func TestFoxholeManagerWhoLeavesTheGroupKeepsTheSession(t *testing.T) {
	w := newTestWorld(t)
	manager := addFoxholeManager(w.forum)
	signInAs(t, w.forum, w.b, manager)
	w.forum.setGroups(manager, 2, []int{35})

	res := follow(t, w.b, w.b.get("/foxhole"))

	assertNoAccessPage(t, parseHTML(t, res))
	w.forum.setGroups(manager, 2, []int{35, testFoxholeGroupID})
	if got := pageOf(t, parseHTML(t, follow(t, w.b, w.b.get("/foxhole")))); got != "foxhole" {
		t.Errorf("back in the group, the Foxhole page address lands on page %q, want foxhole", got)
	}
}

// A user in both groups whose forum account leaves the admin groups keeps
// their panel session, and the panel's root now takes them to the Foxhole
// page, the one page they still open.
func TestPanelAdminAndFoxholeManagerWhoLeavesTheAdminGroupsDropsToTheFoxholePage(t *testing.T) {
	w := newTestWorld(t, testHub())
	user := w.forum.addUser(2468, "Smith.F", 2, []int{47, testFoxholeGroupID})
	signInAs(t, w.forum, w.b, user)
	w.forum.setGroups(user, 2, []int{testFoxholeGroupID})

	res := follow(t, w.b, w.b.get("/"))

	if got := pageOf(t, parseHTML(t, res)); res.StatusCode != http.StatusOK || got != "foxhole" {
		t.Errorf("the panel's root lands on page %q with status %d, want foxhole with 200", got, res.StatusCode)
	}
}

// The guild's Foxhole roles, named from the default base name, and two rank
// roles. A rank role is one on the rank ladder in code, so roleSGT and
// roleCPT are the live guild's SGT and CPT role IDs as the ladder holds
// them.
const (
	roleInternal = "role-fx-internal"
	roleExternal = "role-fx-external"
	roleSGT      = "899328273752928318"
	roleCPT      = "899326238685024267"
)

var foxholeGuildRoles = []*discordgo.Role{
	{ID: roleInternal, Name: "Verified Foxhole Internal"},
	{ID: roleExternal, Name: "Verified Foxhole External"},
	{ID: roleSGT, Name: "Sergeant"},
	{ID: roleCPT, Name: "Captain"},
}

// The fixture's members. Each one's names differ from the others', so a
// row shows whose they are. Doe holds Internal and a rank role and has a
// nickname, a global name and a username; Ash holds Internal and no rank
// role and has no nickname; Kestrel holds External alone and has only a
// username; Marsh holds both roles and a rank role; Vance holds a rank role
// and no Foxhole role.
var (
	memberDoe = commands.ListedMember{ID: "100000000000000001", Username: "jdoe", GlobalName: "John Doe",
		Nick: "SGT Doe.J", RoleIDs: []string{roleSGT, roleInternal}}
	memberAsh = commands.ListedMember{ID: "100000000000000002", Username: "rowan_ash", GlobalName: "Rowan Ash",
		RoleIDs: []string{roleInternal}}
	memberKestrel = commands.ListedMember{ID: "100000000000000003", Username: "kestrel_tlr",
		RoleIDs: []string{roleExternal}}
	memberMarsh = commands.ListedMember{ID: "100000000000000004", Username: "emarsh", GlobalName: "Ellis Marsh",
		Nick: "CPT Marsh.E", RoleIDs: []string{roleInternal, roleCPT, roleExternal}}
	memberVance = commands.ListedMember{ID: "100000000000000005", Username: "rvance", Nick: "SGT Vance.R",
		RoleIDs: []string{roleSGT}}
)

// newFoxholeWorld is the test world with the fixture's roles in the guild
// and a complete member list of the fixture's members, and the Foxhole role
// base name left at its default. Its Foxhole actions pause on w.pause. The
// browser is signed in as a Foxhole manager.
func newFoxholeWorld(t *testing.T) *testWorld {
	t.Helper()
	return newFoxholeWorldWith(t, func(st store.Store) store.Store { return st })
}

// newFoxholeWorldWith is newFoxholeWorld over the store fake as wrap
// wraps it. w.st is the fake itself.
func newFoxholeWorldWith(t *testing.T, wrap func(store.Store) store.Store) *testWorld {
	t.Helper()
	t.Setenv("FOXHOLE_ROLE_BASE_NAME", "")
	t.Setenv("WARDEN_ROLE_BASE_NAME", "")
	fake := store.NewFake()
	watch := endWatch{Store: wrap(fake), ended: make(chan struct{}, 16)}
	pause := installPauseClock(t)
	w := newTestWorldOver(t, watch, newFakeForum(t))
	w.st, w.actionEnded, w.pause = fake, watch.ended, pause
	w.discord.addRoles(foxholeGuildRoles...)
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{memberDoe, memberAsh, memberKestrel, memberMarsh, memberVance}})
	signInAs(t, w.forum, w.b, addFoxholeManager(w.forum))
	return w
}

// holderRows returns the holder list's rows keyed by member ID, and fails
// the test when the page has no holder list.
func holderRows(t *testing.T, doc *html.Node) map[string]*html.Node {
	t.Helper()
	list := findElement(doc, "", "data-field", "holders")
	if list == nil {
		t.Fatal("the page has no holder list")
	}
	rows := map[string]*html.Node{}
	eachElement(list, func(n *html.Node) {
		if id, ok := attrValue(n, "data-member"); ok {
			rows[id] = n
		}
	})
	return rows
}

// The holder list has a row for each member holding Internal or External,
// and for nobody else, each with the member's display name, username,
// Foxhole roles and rank role. The display name is the server nickname,
// else the global name, else the username. An Internal holder with no rank
// role is flagged, and nobody else is.
func TestHolderListShowsEachFoxholeRoleHolder(t *testing.T) {
	w := newFoxholeWorld(t)

	rows := holderRows(t, parseHTML(t, w.b.get("/foxhole")))

	type want struct {
		display, username, rank string
		roles, flags            []string
	}
	wants := map[string]want{
		memberDoe.ID:     {"SGT Doe.J", "jdoe", "Sergeant", []string{"internal"}, nil},
		memberAsh.ID:     {"Rowan Ash", "rowan_ash", "", []string{"internal"}, []string{"no-rank-role"}},
		memberKestrel.ID: {"kestrel_tlr", "kestrel_tlr", "", []string{"external"}, nil},
		memberMarsh.ID:   {"CPT Marsh.E", "emarsh", "Captain", []string{"internal", "external"}, nil},
	}
	if len(rows) != len(wants) {
		t.Errorf("the holder list has rows for %v, want one for each of the %d holders", keys(rows), len(wants))
	}
	for id, want := range wants {
		row, ok := rows[id]
		if !ok {
			t.Errorf("no row for holder %s", id)
			continue
		}
		if got := fieldText(t, row, "display_name"); got != want.display {
			t.Errorf("%s: display name %q, want %q", id, got, want.display)
		}
		if got := fieldText(t, row, "username"); got != want.username {
			t.Errorf("%s: username %q, want %q", id, got, want.username)
		}
		rank := ""
		if el := findElement(row, "", "data-field", "rank_role"); el != nil {
			rank = textOf(el)
		}
		if rank != want.rank {
			t.Errorf("%s: rank role %q, want %q", id, rank, want.rank)
		}
		if got := dataRoles(row); !slices.Equal(got, want.roles) {
			t.Errorf("%s: roles %v, want %v", id, got, want.roles)
		}
		if got := valuesOf(row, "data-flag"); !slices.Equal(got, want.flags) {
			t.Errorf("%s: flags %v, want %v", id, got, want.flags)
		}
	}
}

// keys returns a map's keys, sorted.
func keys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// submitSearch submits the page's search form with the query typed in its
// search box, the way a browser submits a GET form: every field it carries
// goes in the address's query.
func submitSearch(t *testing.T, b *browser, doc *html.Node, query string) *http.Response {
	t.Helper()
	form := findElement(doc, "form", "data-field", "search")
	if form == nil {
		t.Fatal("the page has no search form")
	}
	action, _ := attrValue(form, "action")
	fields := url.Values{}
	eachElement(form, func(n *html.Node) {
		name, ok := attrValue(n, "name")
		if n.Data != "input" || !ok {
			return
		}
		value, _ := attrValue(n, "value")
		if kind, _ := attrValue(n, "type"); kind == "search" {
			value = query
		}
		fields.Add(name, value)
	})
	return b.get(action + "?" + fields.Encode())
}

// Search lists the holders whose display name, username or Discord ID
// holds the query, ignoring case. Each query below matches its member
// through the one field it names.
func TestHolderSearchMatchesDisplayNameUsernameOrID(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"part of a display name, in another case", "doe.j", []string{memberDoe.ID}},
		{"a username, in another case", "ROWAN_ASH", []string{memberAsh.ID}},
		{"a Discord ID", memberKestrel.ID, []string{memberKestrel.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			page := parseHTML(t, w.b.get("/foxhole"))

			rows := holderRows(t, parseHTML(t, submitSearch(t, w.b, page, tc.query)))

			if got := keys(rows); !slices.Equal(got, tc.want) {
				t.Errorf("search %q lists %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// Each filter link carries how many holders it lists, and following it lists
// exactly them: All, Internal, External, and Flagged, the holders with a
// flag.
func TestHolderFiltersCountAndListTheirHolders(t *testing.T) {
	w := newFoxholeWorld(t)
	doc := parseHTML(t, w.b.get("/foxhole"))
	links := map[string]*html.Node{}
	eachElement(doc, func(n *html.Node) {
		if filter, ok := attrValue(n, "data-filter"); ok && n.Data == "a" {
			links[filter] = n
		}
	})
	wants := map[string][]string{
		"all":      {memberDoe.ID, memberAsh.ID, memberKestrel.ID, memberMarsh.ID},
		"internal": {memberDoe.ID, memberAsh.ID, memberMarsh.ID},
		"external": {memberKestrel.ID, memberMarsh.ID},
		"flagged":  {memberAsh.ID},
	}
	for filter, want := range wants {
		t.Run(filter, func(t *testing.T) {
			link, ok := links[filter]
			if !ok {
				t.Fatalf("no filter link %q among %v", filter, keys(links))
			}
			if got, err := strconv.Atoi(fieldText(t, link, "count")); err != nil || got != len(want) {
				t.Errorf("the link's count = %q, want %d", fieldText(t, link, "count"), len(want))
			}
			href, _ := attrValue(link, "href")

			rows := holderRows(t, parseHTML(t, follow(t, w.b, w.b.get(href))))

			if got := keys(rows); !slices.Equal(got, want) {
				t.Errorf("the link to %s lists %v, want %v", href, got, want)
			}
		})
	}
}

// The Foxhole page reads the guild's roles and the member list from the
// gateway state: rendering the holder list makes no read of Discord's API.
func TestFoxholePageMakesNoDiscordCall(t *testing.T) {
	w := newFoxholeWorld(t)
	before := w.discord.apiReadCount()

	rows := holderRows(t, parseHTML(t, w.b.get("/foxhole")))

	if len(rows) == 0 {
		t.Fatal("the holder list is empty, want the fixture's holders")
	}
	if n := w.discord.apiReadCount() - before; n != 0 {
		t.Errorf("the page load made %d reads of Discord's API, want 0", n)
	}
}

// The holder list reads the Foxhole roles by the names the commands use,
// composed from FOXHOLE_ROLE_BASE_NAME: with another base name set, it lists
// the holders of the roles carrying that name, and not of the roles named
// from the default.
func TestHolderListFollowsTheFoxholeRoleBaseName(t *testing.T) {
	w := newFoxholeWorld(t)
	t.Setenv("FOXHOLE_ROLE_BASE_NAME", "Regiment Foxhole")
	w.discord.addRoles(&discordgo.Role{ID: "role-rf-internal", Name: "Regiment Foxhole Internal"})
	hollis := commands.ListedMember{ID: "100000000000000006", Username: "hollis", RoleIDs: []string{"role-rf-internal", roleSGT}}
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{memberDoe, hollis}})

	rows := holderRows(t, parseHTML(t, w.b.get("/foxhole")))

	if got, want := keys(rows), []string{hollis.ID}; !slices.Equal(got, want) {
		t.Errorf("the holder list lists %v, want %v", got, want)
	}
}
