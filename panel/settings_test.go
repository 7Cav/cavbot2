package panel

import (
	"net/http"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// The Settings page (#680, GLOSSARY.md): where a panel admin sets the
// guild-wide settings that belong to no hub, the rank roles and the
// recording roles.

// settingsLink returns the rail's link to the Settings page, or nil.
func settingsLink(doc *html.Node) *html.Node {
	return findElement(doc, "a", "data-rail", "settings")
}

// A panel admin finds the Settings page in the rail and opens it.
func TestPanelAdminOpensTheSettingsPageFromTheRail(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	link := settingsLink(parseHTML(t, follow(t, w.b, w.b.get("/"))))
	if link == nil {
		t.Fatal("the panel admin's rail has no data-rail=settings link")
	}
	href, _ := attrValue(link, "href")

	res := w.b.get(href)

	if got := pageOf(t, parseHTML(t, res)); res.StatusCode != http.StatusOK || got != "settings" {
		t.Errorf("the rail's link %s answers page %q with status %d, want settings with 200", href, got, res.StatusCode)
	}
}

// noAccessPageLoads are the pages whose address answers anyone but a panel
// admin with the no-access page, where other settings pages send them on
// to the page they land on.
var noAccessPageLoads = []string{settingsPath}

// A Foxhole manager and an outsider find no Settings link in the rail of
// the page they land on, and the page's address answers them with the
// no-access page.
func TestSettingsPageIsForPanelAdminsAlone(t *testing.T) {
	visitors := []struct {
		name string
		add  func(f *fakeForum) *forumAccount
	}{
		{"Foxhole manager", addFoxholeManager},
		{"outsider", addOutsider},
	}
	for _, v := range visitors {
		t.Run(v.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			landed := follow(t, w.b, signInAs(t, w.forum, w.b, v.add(w.forum)))
			if settingsLink(parseHTML(t, landed)) != nil {
				t.Error("the rail links to the Settings page")
			}

			for _, path := range noAccessPageLoads {
				res := w.b.get(path)

				if res.StatusCode != http.StatusForbidden {
					t.Errorf("GET %s status = %d, want 403", path, res.StatusCode)
				}
				assertNoAccessSays(t, parseHTML(t, res), noAccessRefused)
			}
		})
	}
}

// rankRows returns the Rank roles section's rows, in the page's order.
func rankRows(t *testing.T, doc *html.Node) []*html.Node {
	t.Helper()
	sec := findElement(doc, "section", "data-section", "rank-roles")
	if sec == nil {
		t.Fatal("page has no section under data-section=rank-roles")
	}
	var rows []*html.Node
	eachElement(sec, func(n *html.Node) {
		if _, ok := attrValue(n, "data-rank"); ok {
			rows = append(rows, n)
		}
	})
	return rows
}

// With nothing saved, the Rank roles section lists the ladder's ranks, most
// senior first, each with its role ID in code. A role the guild lacks is
// marked deleted, and one it has is not.
func TestRankRolesSectionShowsTheLadderInCodeWithNothingSaved(t *testing.T) {
	w := newTestWorld(t, testHub())
	w.discord.addRoles(&discordgo.Role{ID: testRankSGT, Name: "Sergeant"})
	signIn(t, w.forum, w.b)

	rows := rankRows(t, parseHTML(t, w.b.get(settingsPath)))

	ladder := commands.DefaultRankLadder().Ranks()
	if len(rows) != len(ladder) {
		t.Fatalf("section lists %d ranks, want the ladder's %d", len(rows), len(ladder))
	}
	for i, rank := range ladder {
		if got, _ := attrValue(rows[i], "data-rank"); got != rank.Abbrev {
			t.Errorf("row %d is rank %q, want %q", i, got, rank.Abbrev)
			continue
		}
		inputs := postedInputs(rows[i], rankRoleField(rank.Abbrev))
		if len(inputs) != 1 {
			t.Errorf("rank %s posts %d roles, want its one role in code", rank.Abbrev, len(inputs))
			continue
		}
		if got, _ := attrValue(inputs[0], "value"); got != rank.RoleID {
			t.Errorf("rank %s posts role %q, want %q", rank.Abbrev, got, rank.RoleID)
		}
		want := string(unavailableDeleted)
		if rank.RoleID == testRankSGT {
			want = ""
		}
		if got, _ := attrValue(inputs[0], "data-unavailable"); got != want {
			t.Errorf("rank %s role is marked data-unavailable=%q, want %q", rank.Abbrev, got, want)
		}
	}
}
