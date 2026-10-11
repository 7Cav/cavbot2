package panel

import (
	"net/http"
	"net/url"
	"testing"

	"golang.org/x/net/html"
)

// A hub page save ends on the page with a line in the saved form saying it
// saved, so the panel admin who pressed Save knows it took.

// savedForm is one hub page form a panel admin saves: what its Save posts,
// and where on the page it lands.
type savedForm struct {
	name string
	// post is the form's action and its fields, as a page loaded now posts
	// them with value changed in, so the save goes through.
	post func(t *testing.T, w *testWorld, value string) (string, url.Values)
	// values are two values the form saves, each a change from the other
	// and from what's stored.
	values [2]string
	// section is the form's part of the page.
	section func(t *testing.T, w *testWorld, doc *html.Node) *html.Node
}

var savedForms = []savedForm{
	{
		name: "the moderator roles for every hub",
		post: func(t *testing.T, w *testWorld, role string) (string, url.Values) {
			return "/moderators", moderatorsForm(t, w.st, role)
		},
		values:  [2]string{"role-mp", "role-hq"},
		section: func(t *testing.T, _ *testWorld, doc *html.Node) *html.Node { return moderatorsSection(t, doc) },
	},
	{
		name: "the recording roles",
		post: func(t *testing.T, w *testWorld, role string) (string, url.Values) {
			return "/recording-roles", recordingRolesForm(t, w.st, role)
		},
		values:  [2]string{"role-mp", "role-hq"},
		section: func(t *testing.T, _ *testWorld, doc *html.Node) *html.Node { return recordingRolesSection(t, doc) },
	},
	{
		name: "a hub's edit form",
		post: func(t *testing.T, w *testWorld, baseString string) (string, url.Values) {
			form := updateForm(t, w.st)
			form.Set("base_string", baseString)
			return hubPath(t, w.st, "hub-1"), form
		},
		values: [2]string{"Bravo Voice", "Charlie Voice"},
		section: func(t *testing.T, w *testWorld, doc *html.Node) *html.Node {
			return hubSection(t, doc, storedHubID(t, w.st, "hub-1"))
		},
	},
}

// savedLine is the line in a form's section saying the form saved, nil for
// none.
func savedLine(section *html.Node) *html.Node {
	return findElement(section, "", "data-field", "saved")
}

func TestASaveLandsOnALineSayingItSaved(t *testing.T) {
	for _, form := range savedForms {
		t.Run(form.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			action, fields := form.post(t, w, form.values[0])

			doc, _ := landing(t, w.b, action, w.b.postForm(action, fields))

			if savedLine(form.section(t, w, doc)) == nil {
				t.Errorf("saving %s lands on a page with no line saying it saved", form.name)
			}
		})
	}
}

// The line holds only while the save it names does: once another panel
// admin saves the same form, the address the first save landed on shows no
// line, since what that save stored is no longer what's stored.
func TestASavedLineGoesOnceSomeoneSavesTheFormAgain(t *testing.T) {
	for _, form := range savedForms {
		t.Run(form.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			action, fields := form.post(t, w, form.values[0])
			landed := location(t, w.b.postForm(action, fields))
			action, fields = form.post(t, w, form.values[1])
			if res := secondBrowser(t, w).postForm(action, fields); res.StatusCode < 300 || res.StatusCode > 399 {
				t.Fatalf("the other panel admin's save answered %d, want it to go through", res.StatusCode)
			}

			res := w.b.get(landed.RequestURI())

			if res.StatusCode != http.StatusOK {
				t.Fatalf("loading the first save's landing again answered %d, want 200", res.StatusCode)
			}
			if savedLine(form.section(t, w, parseHTML(t, res))) != nil {
				t.Errorf("after another save of %s, the first save's landing still says it saved", form.name)
			}
		})
	}
}

// Creating a hub, or registering one, lands on the hub list with a line
// naming the hub just added, since the list sits below the page's first
// sections, out of view where the page opens.
func TestAddingAHubLandsOnTheListNamingIt(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
	}{
		{"create", createForm("cat-1", "Squad Join", "Squad Voice")},
		{"register", registerForm("vc-2", "Squad Voice")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			signIn(t, w.forum, w.b)

			doc, _ := landing(t, w.b, "/hubs", w.b.postForm("/hubs", tc.form))

			line := findElement(doc, "", "data-field", "added")
			if line == nil {
				t.Fatalf("a %s lands on a page with no line naming the hub it added", tc.name)
			}
			if got := fieldText(t, line, "base_string"); got != "Squad Voice" {
				t.Errorf("a %s's line names hub %q, want Squad Voice", tc.name, got)
			}
		})
	}
}
