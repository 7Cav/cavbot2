package panel

import (
	"net/http"
	"strconv"
	"testing"

	"golang.org/x/net/html"
)

// within reports whether n is el or sits inside it.
func within(n, el *html.Node) bool {
	for ; n != nil; n = n.Parent {
		if n == el {
			return true
		}
	}
	return false
}

// Every way the panel sends a panel admin to a hub's edit form lands on the
// form, not on the top of the Hubs page, three blocks above it: the hub
// list's Edit, the form's own Save, and Cancel on a remove preview opened
// from the form.
func TestEditFormOpensAtTheForm(t *testing.T) {
	cases := []struct {
		name string
		// open goes to the hub's edit form one way, and returns the address
		// the control it used names and the answer.
		open func(t *testing.T, w *testWorld, id int64) (control string, res *http.Response)
	}{
		{"Edit on the hub list", func(t *testing.T, w *testWorld, id int64) (string, *http.Response) {
			edit := findElement(listRow(t, parseHTML(t, w.b.get("/")), id), "a", "data-field", "edit")
			if edit == nil {
				t.Fatal("the hub's list row offers no Edit")
			}
			href := attrOf(edit, "href")
			return href, w.b.get(href)
		}},
		{"the edit form's Save", func(t *testing.T, w *testWorld, id int64) (string, *http.Response) {
			doc := parseHTML(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)))
			save := findElement(hubSection(t, doc, id), "", "data-field", "save")
			if save == nil {
				t.Fatal("the edit form has no Save")
			}
			form := save.Parent
			for form != nil && form.Data != "form" {
				form = form.Parent
			}
			if form == nil {
				t.Fatal("the edit form's Save sits in no form")
			}
			fields := formFields(doc, form)
			fields.Set("base_string", "Bravo Voice")
			action := attrOf(form, "action")
			return action, w.b.postForm(action, fields)
		}},
		{"Cancel on a remove preview opened from the form", func(t *testing.T, w *testWorld, id int64) (string, *http.Response) {
			cancel := findElement(removePreviewBlock(t, openHubRemovePreview(t, w.b, id)), "a", "data-field", "cancel")
			if cancel == nil {
				t.Fatal("the remove preview has no Cancel")
			}
			href := attrOf(cancel, "href")
			return href, w.b.get(href)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			id := storedHubID(t, w.st, "hub-1")
			control, res := tc.open(t, w, id)

			doc, fragment := landing(t, w.b, control, res)

			target := landedOn(doc, fragment)
			if target == nil {
				t.Fatalf("the page opens at fragment %q, which names nothing, want the hub's edit form", fragment)
			}
			section := hubSection(t, doc, id)
			if !within(target, section) {
				t.Error("the page lands outside the hub's edit section, want on it")
			}
			if findElement(section, "", "data-field", "save") == nil {
				t.Error("the page lands on the hub's section, which shows no edit form")
			}
		})
	}
}
