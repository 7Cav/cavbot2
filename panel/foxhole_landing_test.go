package panel

import (
	"net/http"
	"slices"
	"testing"

	"golang.org/x/net/html"
)

// listBlock returns the Foxhole page's block holding the list whose
// data-field is field, holders or no-role, and fails the test when the page
// has none.
func listBlock(t *testing.T, doc *html.Node, field string) *html.Node {
	t.Helper()
	block := findElement(doc, "section", "data-field", field)
	if block == nil {
		t.Fatalf("the page has no %s block", field)
	}
	return block
}

// namedOnlyIn checks the page's result lines name the member by attr, or
// nobody when member is empty, and that every line naming them lies within
// the block.
func namedOnlyIn(t *testing.T, doc, block *html.Node, attr, member string) {
	t.Helper()
	var want []string
	if member != "" {
		want = []string{member}
	}
	if got := valuesOf(doc, attr); !slices.Equal(got, want) {
		t.Errorf("the page's result lines name %v by %s, want %v", got, attr, want)
		return
	}
	if got := valuesOf(block, attr); !slices.Equal(got, want) {
		t.Errorf("the list's block names %v by %s, want %v: a result line sits outside it", got, attr, want)
	}
}

// A Foxhole page save that sends the manager back to a list lands on that
// list's block, which sits below the action blocks wherever they stack.
// A result line the save leaves, naming a member an Approve skipped or one
// a note clear took off the page, shows in that block and nowhere else on
// the page.
func TestAFoxholeSaveLandsOnTheListItWasMadeFrom(t *testing.T) {
	cases := []struct {
		name  string
		world func(*testing.T) *testWorld
		// save makes the save as a browser with no script does, and returns
		// the address of the form it posted and the answer.
		save func(*testing.T, *testWorld) (string, *http.Response)
		// block is the data-field of the list block the save lands on.
		block string
		// skipped and cleared are the member the result line names as
		// skipped or as cleared off the page, empty for no such line.
		skipped, cleared string
	}{
		{"an Approve that skips a member", newFoxholeWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			return foxholeApprovalsPath, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opApprove, memberKestrel.ID, memberDoe.ID)
		}, "holders", memberDoe.ID, ""},
		{"a Clear approval", newFoxholeWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			approveThroughThePage(t, w, memberKestrel.ID)
			return foxholeApprovalsPath, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opClear, memberKestrel.ID)
		}, "holders", "", ""},
		{"a note saved on a holder", newFoxholeWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			return foxholeNotesPath, submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "allied group lead")
		}, "holders", "", ""},
		{"a holder's note cleared", newNoRoleWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			return foxholeNotesPath, submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "")
		}, "holders", "", ""},
		{"a note saved in the no-role view", newNoRoleWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			return foxholeNotesPath, submitNote(t, w.b, openNoRoleNote(t, w.b, memberDoe.ID), "discharged 12 Sep")
		}, "no-role", "", ""},
		{"a note clear that takes a member off the no-role view", newNoRoleWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			return foxholeNotesPath, submitNote(t, w.b, openNoRoleNote(t, w.b, memberDoe.ID), "")
		}, "no-role", "", memberDoe.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world(t)
			control, res := tc.save(t, w)

			doc, fragment := landing(t, w.b, control, res)

			block := listBlock(t, doc, tc.block)
			if target := landedOn(doc, fragment); target == nil || !within(target, block) {
				t.Fatalf("the save lands at fragment %q, want on the %s block", fragment, tc.block)
			}
			namedOnlyIn(t, doc, block, "data-skipped", tc.skipped)
			namedOnlyIn(t, doc, block, "data-cleared", tc.cleared)
		})
	}
}

// The no-script note form's Cancel goes back where its Save does: to the
// view the form was opened from, at that view's list.
func TestNoteFormCancelLandsOnTheListItWasOpenedFrom(t *testing.T) {
	w := newNoRoleWorld(t)
	form := findLive(openNoRoleNote(t, w.b, memberDoe.ID), "form", "data-field", "note-form")
	if form == nil {
		t.Fatal("the page has no note form")
	}
	cancel := findElement(form, "a", "data-field", "cancel_note")
	if cancel == nil {
		t.Fatal("the note form has no Cancel")
	}
	href, _ := attrValue(cancel, "href")

	doc, fragment := landing(t, w.b, href, w.b.get(href))

	if target := landedOn(doc, fragment); target == nil || !within(target, listBlock(t, doc, "no-role")) {
		t.Errorf("Cancel lands at fragment %q, want on the no-role block", fragment)
	}
}

// resultRegionsWithin checks the page carries the live region the page
// script puts a note save's result in, and that every one it carries lies
// within the block.
func resultRegionsWithin(t *testing.T, doc, block *html.Node, page string) {
	t.Helper()
	var regions []*html.Node
	eachLiveElement(doc, func(n *html.Node) {
		if _, ok := attrValue(n, "data-note-result"); ok {
			regions = append(regions, n)
		}
	})
	if len(regions) == 0 {
		t.Errorf("%s has no result region", page)
	}
	for _, region := range regions {
		if !within(region, block) {
			t.Errorf("%s has a result region outside the list's block", page)
		}
	}
}

// With script, a note save posts in the background and copies the result
// region of the page it answers with into this page's. In the no-role view,
// where a clear takes the member off, that region sits in the view's block
// on a fresh load and on the page a clear answers with, which carries the
// cleared line in it.
func TestNoRoleViewKeepsTheResultRegionInItsBlock(t *testing.T) {
	w := newNoRoleWorld(t)
	fresh := openNoRoleView(t, w.b)
	resultRegionsWithin(t, fresh, listBlock(t, fresh, "no-role"), "a fresh load of the no-role view")

	doc, _ := landing(t, w.b, foxholeNotesPath, submitNote(t, w.b, openNoRoleNote(t, w.b, memberDoe.ID), ""))

	resultRegionsWithin(t, doc, listBlock(t, doc, "no-role"), "the page the clear answers with")
	line := findLive(doc, "", "data-cleared", memberDoe.ID)
	if line == nil {
		t.Fatalf("the page the clear answers with says nothing of %s's note being cleared", memberDoe.ID)
	}
	for n := line; ; n = n.Parent {
		if n == nil {
			t.Fatal("the cleared line sits outside the result region, so the page script never shows it")
		}
		if _, ok := attrValue(n, "data-note-result"); ok {
			break
		}
	}
}
