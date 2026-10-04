package panel

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// addNonAdmin makes the forum know a user in no panel admin group.
func addNonAdmin(f *fakeForum) *forumAccount {
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

// A forum user in no panel admin group signs in and lands on the no-access
// page, with no settings on it.
func TestNonAdminSignsInToTheNoAccessPage(t *testing.T) {
	w := newTestWorld(t, testHub())
	signInAs(t, w.forum, w.b, addNonAdmin(w.forum))

	res := w.b.get("/")

	assertNoAccessPage(t, parseHTML(t, res))
}

// A forum user in no panel admin group saves nothing: each settings save
// they post leaves the store as it was. A panel admin then posts the same
// form and it saves, so no row passes on a form the save would refuse
// anyway.
func TestNonAdminSaveWritesNothing(t *testing.T) {
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
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signInAs(t, w.forum, w.b, addNonAdmin(w.forum))
			path, form := tc.path(t, w.st), tc.form(t, w.st)
			before := readSavedState(t, w.st)

			w.b.postForm(path, form)

			if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
				t.Errorf("the store after the non-admin's save = %+v, want it as before, %+v", after, before)
			}
			admin := newBrowser(t, w.p)
			signIn(t, w.forum, admin)
			admin.postForm(path, form)
			if after := readSavedState(t, w.st); reflect.DeepEqual(after, before) {
				t.Error("the same form posted by a panel admin saved nothing: the row's form is one a save refuses")
			}
		})
	}
}

// A panel admin whose forum account leaves every panel admin group keeps
// their panel session, and their next page is the no-access page.
func TestAdminWhoLosesTheGroupDropsToTheNoAccessPage(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.forum.setUserinfo(http.StatusOK, userinfoJSON(2, []int{35, 72}))

	res := w.b.get("/")

	assertNoAccessPage(t, parseHTML(t, res))
}
