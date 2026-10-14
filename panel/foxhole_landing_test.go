package panel

import (
	"net/http"
	"slices"
	"strings"
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

// The Foxhole page saves the landing tests make as a browser with no script
// does. Each returns the address of the form it posted and the answer.

// approveThatSkipsKestrel approves Kestrel, takes away Kestrel's Foxhole
// role, then approves Kestrel again with Doe, so the Approve skips Kestrel.
func approveThatSkipsKestrel(t *testing.T, w *testWorld) (string, *http.Response) {
	t.Helper()
	approveThroughThePage(t, w, memberKestrel.ID)
	withoutFoxholeRoles(w)
	return foxholeApprovalsPath, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opApprove, memberKestrel.ID, memberDoe.ID)
}

// clearKestrelsApproval clears the approval Kestrel was given.
func clearKestrelsApproval(t *testing.T, w *testWorld) (string, *http.Response) {
	t.Helper()
	approveThroughThePage(t, w, memberKestrel.ID)
	return foxholeApprovalsPath, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opClear, memberKestrel.ID)
}

// clearDoesNoRoleNote clears the note on Doe's no-role row, which takes
// Doe off the page.
func clearDoesNoRoleNote(t *testing.T, w *testWorld) (string, *http.Response) {
	t.Helper()
	return foxholeNotesPath, submitNote(t, w.b, openNoRoleNote(t, w.b, memberDoe.ID), "")
}

// A Foxhole page save that sends the manager back to a list lands on that
// list's heading, in the list's block below the action blocks wherever they
// stack, so a keyboard or screen reader user starts at the list. A result
// line the save leaves, naming a member an Approve skipped or one a note
// clear took off the page, shows in that block and nowhere else on the page.
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
		{"an Approve that skips a member", newFoxholeWorld, approveThatSkipsKestrel, "holders", memberKestrel.ID, ""},
		{"a Clear approval", newFoxholeWorld, clearKestrelsApproval, "holders", "", ""},
		{"a note saved on a holder", newFoxholeWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			return foxholeNotesPath, submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "allied group lead")
		}, "holders", "", ""},
		{"a holder's note cleared", newNoRoleWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			return foxholeNotesPath, submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "")
		}, "holders", "", ""},
		{"a note saved in the no-role view", newNoRoleWorld, func(t *testing.T, w *testWorld) (string, *http.Response) {
			return foxholeNotesPath, submitNote(t, w.b, openNoRoleNote(t, w.b, memberDoe.ID), "discharged 12 Sep")
		}, "no-role", "", ""},
		{"a note clear that takes a member off the no-role view", newNoRoleWorld, clearDoesNoRoleNote, "no-role", "", memberDoe.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world(t)
			control, res := tc.save(t, w)

			doc, fragment := landing(t, w.b, control, res)

			block := listBlock(t, doc, tc.block)
			landedHeading(t, doc, fragment, block, "the "+tc.block+" list's heading")
			describedByResolves(t, doc)
			namedOnlyIn(t, doc, block, "data-skipped", tc.skipped)
			namedOnlyIn(t, doc, block, "data-cleared", tc.cleared)
		})
	}
}

// A save that lands on a list with a result line makes the line the list
// heading's description, so a screen reader says who the save skipped, or
// whose note it cleared, as focus lands, though the line was on the page
// before any live region could announce it. A save that leaves no result
// line leaves the heading undescribed.
func TestAFoxholeSaveResultLineDescribesTheListHeading(t *testing.T) {
	cases := []struct {
		name  string
		world func(*testing.T) *testWorld
		save  func(*testing.T, *testWorld) (string, *http.Response)
		// block is the data-field of the list block the save lands on.
		block string
		// line finds the result line the heading's description reads, nil
		// for a save that leaves none.
		line func(*html.Node) *html.Node
		// names is text the description carries, such as a member the line
		// names, empty for none to check.
		names string
	}{
		{"an Approve that skips a member", newFoxholeWorld, approveThatSkipsKestrel, "holders", func(doc *html.Node) *html.Node {
			return findLive(doc, "", "data-field", "approve-skipped")
		}, memberKestrel.Username},
		{"a note clear that takes a member off the no-role view", newNoRoleWorld, clearDoesNoRoleNote, "no-role", func(doc *html.Node) *html.Node {
			return findLive(doc, "", "data-cleared", memberDoe.ID)
		}, ""},
		{"a Clear approval", newFoxholeWorld, clearKestrelsApproval, "holders", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world(t)
			control, res := tc.save(t, w)

			doc, fragment := landing(t, w.b, control, res)

			heading := landedHeading(t, doc, fragment, listBlock(t, doc, tc.block), "the "+tc.block+" list's heading")
			got := accessibleDescription(doc, heading)
			if tc.line == nil {
				if got != "" {
					t.Errorf("the landed heading's description is %q, want none", got)
				}
				return
			}
			line := tc.line(doc)
			if line == nil {
				t.Fatal("the page shows no result line")
			}
			if want := announcedText(line); got != want {
				t.Errorf("the landed heading's description is %q, want the result line, %q", got, want)
			}
			if !strings.Contains(got, tc.names) {
				t.Errorf("the landed heading's description is %q, want it to name %s", got, tc.names)
			}
		})
	}
}

// The no-script note form's Cancel goes back where its Save does: to the
// view the form was opened from, at that view's list's heading.
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

	landedHeading(t, doc, fragment, listBlock(t, doc, "no-role"), "the no-role list's heading")
	describedByResolves(t, doc)
}

// Each link and form that opens a list from the Foxhole page lands on that
// list's heading: a filter link, the search, the link to the no-role view,
// and the link at the top of the page that skips past the action blocks.
func TestFoxholeLinksToAListLandOnItsHeading(t *testing.T) {
	cases := []struct {
		name string
		// open follows the control one way from a fresh load of the page,
		// and returns the address the control names and the answer.
		open func(*testing.T, *browser, *html.Node) (string, *http.Response)
		// block is the data-field of the list block the control opens.
		block string
	}{
		{"a filter link", func(t *testing.T, b *browser, doc *html.Node) (string, *http.Response) {
			href := attrOf(filterAnchor(t, doc, "internal"), "href")
			return href, b.get(href)
		}, "holders"},
		{"a search", func(t *testing.T, b *browser, doc *html.Node) (string, *http.Response) {
			return attrOf(findElement(doc, "form", "data-field", "search"), "action"), submitSearch(t, b, doc, "kestrel")
		}, "holders"},
		{"the link to the no-role view", func(t *testing.T, b *browser, doc *html.Node) (string, *http.Response) {
			href := attrOf(noRoleLink(t, doc), "href")
			return href, b.get(href)
		}, "no-role"},
		{"the link past the action blocks", func(t *testing.T, b *browser, doc *html.Node) (string, *http.Response) {
			jump := findElement(doc, "a", "data-field", "list-jump")
			if jump == nil {
				t.Fatal("the page has no link past the action blocks")
			}
			return attrOf(jump, "href"), b.get(foxholePath)
		}, "holders"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newNoRoleWorld(t)
			control, res := tc.open(t, w.b, parseHTML(t, w.b.get(foxholePath)))

			doc, fragment := landing(t, w.b, control, res)

			landedHeading(t, doc, fragment, listBlock(t, doc, tc.block), "the "+tc.block+" list's heading")
			describedByResolves(t, doc)
		})
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
