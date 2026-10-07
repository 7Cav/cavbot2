package panel

import (
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// addUserOutsideAdminGroups makes the forum know a user in none of the
// panel's admin groups.
func addUserOutsideAdminGroups(f *fakeForum) *forumAccount {
	return f.addUser(5678, "Roe.R", 2, []int{35, 72})
}

// assertNoAccessPage checks a page is the no-access page and carries none of
// the hub page's settings.
func assertNoAccessPage(t *testing.T, doc *html.Node) {
	t.Helper()
	if findElement(doc, "", "data-field", "no-access") == nil {
		t.Error("the page has no data-field=no-access")
	}
	if findElement(doc, "", "data-section", "moderators") != nil {
		t.Error("the page shows the guild-wide moderator section")
	}
}

// A forum user in none of the panel's admin groups signs in and lands on
// the no-access page, with no settings on it.
func TestUserOutsideTheAdminGroupsSignsInToTheNoAccessPage(t *testing.T) {
	w := newTestWorld(t, testHub())
	signInAs(t, w.forum, w.b, addUserOutsideAdminGroups(w.forum))

	res := w.b.get("/")

	assertNoAccessPage(t, parseHTML(t, res))
}

// A forum user in none of the panel's admin groups saves nothing: each
// settings save they post answers with the no-access page, which says
// nothing changed, and leaves the store as it was. A panel admin then
// posts the same form and it saves, so no row passes on a form the save
// would refuse anyway. The Foxhole manager's rows are regression pins: they
// passed before the Foxhole page existed, when the group check read a
// Foxhole manager as a user in no group. They pin that no hub route moved
// under the Foxhole page's gate.
func TestUserOutsideTheAdminGroupsSavesNothing(t *testing.T) {
	users := []struct {
		name string
		add  func(*fakeForum) *forumAccount
	}{
		{"in no group", addUserOutsideAdminGroups},
		{"Foxhole manager", addFoxholeManager},
	}
	cases := []struct {
		name string
		path func(t *testing.T, st store.Store) string
		form func(t *testing.T, st store.Store) url.Values
	}{
		{
			name: "create",
			path: func(*testing.T, store.Store) string { return "/hubs" },
			form: fixedForm(createForm("cat-1", "Squad Join", "Squad Voice")),
		},
		{
			name: "register",
			path: func(*testing.T, store.Store) string { return "/hubs" },
			form: fixedForm(registerForm("vc-2", "Squad Voice")),
		},
		{
			name: "update",
			path: func(t *testing.T, st store.Store) string { return hubPath(t, st, "hub-1") },
			form: func(t *testing.T, st store.Store) url.Values {
				form := updateForm(t, st)
				form.Set("base_string", "Bravo Voice")
				return form
			},
		},
		{
			name: "remove",
			path: func(t *testing.T, st store.Store) string { return hubPath(t, st, "hub-1") + "/remove" },
			form: fixedForm(nil),
		},
		{
			name: "moderators",
			path: func(*testing.T, store.Store) string { return "/moderators" },
			form: func(t *testing.T, st store.Store) url.Values { return moderatorsForm(t, st, "role-hq") },
		},
	}
	for _, u := range users {
		for _, tc := range cases {
			t.Run(u.name+"/"+tc.name, func(t *testing.T) {
				w := newTestWorld(t, testHub())
				signInAs(t, w.forum, w.b, u.add(w.forum))
				path, form := tc.path(t, w.st), tc.form(t, w.st)
				before := readSavedState(t, w.st)

				res := w.b.postForm(path, form)

				assertNoAccessPage(t, parseHTML(t, follow(t, w.b, res)))
				if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
					t.Errorf("the store after the save by a user outside the admin groups = %+v, want it as before, %+v", after, before)
				}
				panelAdmin := newBrowser(t, w.p)
				signIn(t, w.forum, panelAdmin)
				panelAdmin.postForm(path, form)
				if after := readSavedState(t, w.st); reflect.DeepEqual(after, before) {
					t.Error("the same form posted by a panel admin saved nothing: the row's form is one a save refuses")
				}
			})
		}
	}
}

// A panel admin whose forum account leaves every panel admin group keeps
// their panel session, and their next page is the no-access page.
func TestPanelAdminWhoLeavesTheAdminGroupsDropsToTheNoAccessPage(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.forum.setUserinfo(http.StatusOK, userinfoJSON(2, []int{35, 72}))

	res := w.b.get("/")

	assertNoAccessPage(t, parseHTML(t, res))
}

// A form on another site can post to the panel from a signed-in manager's
// browser, which sends the manager's cookie with it. A remove preview's
// Confirm posted that way is refused and starts no removal. The same
// Confirm posted from the panel's own page then starts it.
func TestConfirmPostedFromAnotherSiteIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))
	form := findElement(preview, "form", "", "")
	if form == nil {
		t.Fatal("the remove preview has no form")
	}
	action, _ := attrValue(form, "action")

	res := w.b.do(http.MethodPost, action, http.Header{
		"Content-Type":   {"application/x-www-form-urlencoded"},
		"Origin":         {"https://elsewhere.example"},
		"Sec-Fetch-Site": {"cross-site"},
	}, strings.NewReader(formValues(form).Encode()))

	if res.StatusCode < 400 || res.StatusCode > 499 {
		t.Errorf("the Confirm posted from another site answered %d, want a refusal", res.StatusCode)
	}
	if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "removal"); len(entries) != 0 {
		t.Errorf("the Confirm posted from another site started %d removals, want none", len(entries))
	}
	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)
	if slices.Contains(w.discord.holdersOf(roleExternal), memberKestrel.ID) {
		t.Errorf("the same Confirm posted from the panel's page left %s holding External", memberKestrel.ID)
	}
}
