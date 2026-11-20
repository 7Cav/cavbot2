package panel

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// joinerNames returns the display names the selected row's list shows,
// sorted.
func joinerNames(t *testing.T, doc *html.Node) []string {
	t.Helper()
	list := findElement(doc, "", "data-field", "joiners")
	if list == nil {
		t.Fatal("the page has no joiners list")
	}
	var names []string
	eachElement(list, func(n *html.Node) {
		if v, _ := attrValue(n, "data-field"); v == "display_name" {
			names = append(names, textOf(n))
		}
	})
	slices.Sort(names)
	return names
}

// The AOs table has a row per AO role, highest role position first, then No
// AO role and Joined and left, each counting the week's joiners. The list
// shows the first AO's joiners by default, and the row ao names otherwise.
func TestJoinersPageListsTheWeeksJoinersByAORole(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.addRoles(&discordgo.Role{ID: "role-arma", Name: "Arma 3", Position: 5}, &discordgo.Role{ID: "role-sc", Name: "Star Citizen", Position: 9})
	w.b.postForm("/ao-roles", aoRolesForm(t, w.st, "role-arma", "role-sc"))
	now := w.clock.Now()
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true, Members: []commands.ListedMember{
		testBotMember,
		{ID: "300000000000000001", Username: "halvo_k", Nick: "Halvorsen.K", JoinedAt: now.Add(-2 * time.Hour), RoleIDs: []string{"role-sc"}},
		{ID: "300000000000000002", Username: "dreyes", GlobalName: "Reyes.D", JoinedAt: now.Add(-26 * time.Hour), RoleIDs: []string{"role-sc", "role-arma"}},
		{ID: "300000000000000003", Username: "quietmoth", JoinedAt: now.Add(-50 * time.Hour)},
		{ID: "300000000000000004", Username: "tng", Nick: "Ng.T", JoinedAt: now.AddDate(0, 0, -14), RoleIDs: []string{"role-sc"}},
	}})

	doc := parseHTML(t, w.b.get(joinersPath))

	if got, want := valuesOf(doc, "data-ao"), []string{"role-sc", "role-arma", aoNone, aoLeft}; !slices.Equal(got, want) {
		t.Errorf("AO rows = %v, want %v", got, want)
	}
	for key, want := range map[string]string{"role-sc": "2", "role-arma": "1", aoNone: "1"} {
		if row := findElement(doc, "tr", "data-ao", key); row == nil {
			t.Errorf("no row for %s", key)
		} else if got := fieldText(t, row, "count"); got != want {
			t.Errorf("the %s row counts %s joiners, want %s", key, got, want)
		}
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{"Halvorsen.K", "Reyes.D"}},
		{"?ao=role-arma", []string{"Reyes.D"}},
		{"?ao=" + aoNone, []string{"quietmoth"}},
	} {
		if got := joinerNames(t, parseHTML(t, w.b.get(joinersPath+tc.query))); !slices.Equal(got, tc.want) {
			t.Errorf("GET %s%s lists %v, want %v", joinersPath, tc.query, got, tc.want)
		}
	}
}

// Every block reads the member list, so while the list can't complete the
// list notice stands in for all of them.
func TestJoinersPageShowsTheMemberListNoticeInPlaceOfItsBlocks(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListRefused, Connected: true, RetryAt: w.clock.Now().Add(2 * hubPageBudget)})

	res := w.b.get(joinersPath)

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	doc := parseHTML(t, res)
	memberListNotice(t, doc)
	for _, block := range []string{"aos", "joiners", "pipeline"} {
		if hasField(doc, block) {
			t.Errorf("the page shows the %s block", block)
		}
	}
}
