package panel

import (
	"context"
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

// storedRankRoles reads the rank roles back through the store, the read the
// runtime makes at startup.
func storedRankRoles(t *testing.T, st store.Store) store.RankRoles {
	t.Helper()
	roles, err := st.GetRankRoles(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("GetRankRoles: %v", err)
	}
	return roles
}

// rankRolesForm is the Rank roles section's form as a page loaded now posts
// it, with the changes given made: each rank's role as the stored ladder
// gives it, a change's role in its rank's place, and no role for a change
// to empty.
func rankRolesForm(t *testing.T, st store.Store, changes map[string]string) url.Values {
	t.Helper()
	stored := storedRankRoles(t, st)
	form := url.Values{"version": {strconv.FormatInt(stored.Version, 10)}}
	for _, rank := range commands.RankLadderOf(stored).Ranks() {
		id := rank.RoleID
		if changed, ok := changes[rank.Abbrev]; ok {
			id = changed
		}
		if id != "" {
			form.Set(rankRoleField(rank.Abbrev), id)
		}
	}
	return form
}

// A save that gives SGT a role makes its holders Cav members at once: one
// who joins a hub owns the channel it spawns.
func TestRankRolesSaveMakesARoleHolderOwnTheirSpawnedChannel(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq"}))

	assertRedirect(t, res, settingsPath)
	w.joinAs("user-hq", "hub-1", "role-hq")
	w.joinAs("user-hq", "spawn-1", "role-hq")
	if owner, _ := w.runtime.Owner("spawn-1"); owner != "user-hq" {
		t.Errorf("Owner(spawn-1) = %q, want user-hq, who holds the role saved for SGT", owner)
	}
}

// A save that gives RCT a role shows on the Foxhole page at once: an
// Internal holder of that role shows it as their rank role, unflagged.
func TestRankRolesSaveShowsOnTheFoxholePageAtOnce(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.addRoles(&discordgo.Role{ID: "role-recruit", Name: "Recruit"})
	w.discord.editMember(memberAsh.ID, func(m *commands.ListedMember) { m.RoleIDs = append(m.RoleIDs, "role-recruit") })
	admin := secondBrowser(t, w)

	assertRedirect(t, admin.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"RCT": "role-recruit"})), settingsPath)

	row := holderRows(t, parseHTML(t, w.b.get(foxholePath)))[memberAsh.ID]
	if row == nil {
		t.Fatalf("no holder row for %s", memberAsh.ID)
	}
	if got := fieldText(t, row, "rank_role"); got != "Recruit" {
		t.Errorf("Ash's rank role = %q, want Recruit, the role saved for RCT", got)
	}
	if flags := valuesOf(row, "data-flag"); slices.Contains(flags, "no-rank-role") {
		t.Errorf("Ash's row is flagged %v, want no no-rank-role flag", flags)
	}
}
