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

// landedOnList returns the heading a page opened at fragment lands on and
// the list block holding it, the one whose data-field is field. It fails the
// test unless the heading is that list's, and unless every aria-describedby
// on the page names an element the page carries.
func landedOnList(t *testing.T, doc *html.Node, fragment, field string) (heading, block *html.Node) {
	t.Helper()
	block = listBlock(t, doc, field)
	heading = landedHeading(t, doc, fragment, block, "the "+field+" list's heading")
	describedByResolves(t, doc)
	return heading, block
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
	page := parseHTML(t, w.b.get(foxholePath))
	return attrOf(selectionForm(t, page), "action"), submitSelection(t, w.b, page, opApprove, memberKestrel.ID, memberDoe.ID)
}

// clearKestrelsApproval clears the approval Kestrel was given.
func clearKestrelsApproval(t *testing.T, w *testWorld) (string, *http.Response) {
	t.Helper()
	approveThroughThePage(t, w, memberKestrel.ID)
	page := parseHTML(t, w.b.get(foxholePath))
	return attrOf(selectionForm(t, page), "action"), submitSelection(t, w.b, page, opClear, memberKestrel.ID)
}

// clearNoRoleNoteOnDoe clears the note on Doe's no-role row, which takes
// Doe off the page.
func clearNoRoleNoteOnDoe(t *testing.T, w *testWorld) (string, *http.Response) {
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
		{"a note clear that takes a member off the no-role view", newNoRoleWorld, clearNoRoleNoteOnDoe, "no-role", "", memberDoe.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world(t)
			control, res := tc.save(t, w)

			doc, fragment := landing(t, w.b, control, res)

			_, block := landedOnList(t, doc, fragment, tc.block)
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
		{"a note clear that takes a member off the no-role view", newNoRoleWorld, clearNoRoleNoteOnDoe, "no-role", func(doc *html.Node) *html.Node {
			return findLive(doc, "", "data-cleared", memberDoe.ID)
		}, ""},
		{"a Clear approval", newFoxholeWorld, clearKestrelsApproval, "holders", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world(t)
			control, res := tc.save(t, w)

			doc, fragment := landing(t, w.b, control, res)

			heading, _ := landedOnList(t, doc, fragment, tc.block)
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
			if tc.names != "" && !strings.Contains(got, tc.names) {
				t.Errorf("the landed heading's description is %q, want it to name %s", got, tc.names)
			}
		})
	}
}

// refusalKinds returns the data-error kind of each refusal note on the page.
func refusalKinds(doc *html.Node) []string {
	var kinds []string
	eachLiveElement(doc, func(n *html.Node) {
		if kind, ok := attrValue(n, "data-error"); ok {
			kinds = append(kinds, kind)
		}
	})
	return kinds
}

// A refused Approve or Clear approval answers with the page, not a
// redirect. The page opens at the holder list's heading, where the manager
// pressed the button, not at the top far above the list. The refusal note
// shows once, in the list's block under the heading, and is the heading's
// description, so a screen reader says why nothing changed as focus lands.
// PRODUCT.md's Accessibility & Inclusion asks this of every refused save.
// Only a browser without the page script sends a press with nothing ticked.
func TestARefusedApprovalsSaveLandsOnTheHolderList(t *testing.T) {
	cases := []struct {
		name    string
		op      string
		members []string
	}{
		{"an Approve on a member approved for each Foxhole role they hold", opApprove, []string{memberKestrel.ID}},
		{"an Approve with nothing ticked", opApprove, nil},
		{"a Clear approval on a member with no approval", opClear, []string{memberMarsh.ID}},
		{"a Clear approval with nothing ticked", opClear, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			approveThroughThePage(t, w, memberKestrel.ID)
			page := parseHTML(t, w.b.get(foxholePath))

			res, fragment := arrive(t, w.b, attrOf(selectionForm(t, page), "action"), submitSelection(t, w.b, page, tc.op, tc.members...))

			if res.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusConflict)
			}
			doc := parseHTML(t, res)
			heading, block := landedOnList(t, doc, fragment, "holders")
			refusalDescribing(t, doc, heading, block)
			if got, want := refusalKinds(doc), []string{refusalNobodyToChange}; !slices.Equal(got, want) {
				t.Errorf("the page shows refusal notes %v, want the one nobody-to-change note", got)
			}
		})
	}
}

// While the member list is partial, the member list notice stands in the
// holder list's place, so a refused Approve or Clear approval has no list
// to open at. The panel refuses Approve for the partial list. Clear approval
// reads no list, so the panel refuses a Clear approval on a member with no
// approval as changing nobody. Either answer shows its note once, with no
// holder list.
func TestARefusedApprovalsSaveWhileTheMemberListIsPartialShowsItsNoteOnce(t *testing.T) {
	cases := []struct {
		name   string
		op     string
		reason string
	}{
		{"an Approve", opApprove, "member-list"},
		{"a Clear approval on a member with no approval", opClear, refusalNobodyToChange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			page := parseHTML(t, w.b.get(foxholePath))
			w.discord.setMemberList(arrivingList)

			doc := parseHTML(t, submitSelection(t, w.b, page, tc.op, memberMarsh.ID))

			if got, want := refusalKinds(doc), []string{tc.reason}; !slices.Equal(got, want) {
				t.Errorf("the page shows refusal notes %v, want the one %s note", got, tc.reason)
			}
			if findLive(doc, "section", "data-field", "holders") != nil {
				t.Error("the page shows a holder list block, want the member list notice in its place")
			}
			describedByResolves(t, doc)
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

	landedOnList(t, doc, fragment, "no-role")
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

			landedOnList(t, doc, fragment, tc.block)
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

// noteBlock returns the Foxhole page's note block, the one holding the
// note form it opens, and fails the test when the page has none.
func noteBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	block := findElement(doc, "section", "data-field", "note-block")
	if block == nil {
		t.Fatal("the page has no note block")
	}
	return block
}

// landedOnNote returns the heading a page opened at fragment lands on and
// the note block holding it. It fails the test unless the heading is the
// note block's, and unless every aria-describedby on the page names an
// element the page carries.
func landedOnNote(t *testing.T, doc *html.Node, fragment string) (heading, block *html.Node) {
	t.Helper()
	block = noteBlock(t, doc)
	heading = landedHeading(t, doc, fragment, block, "the note block's heading")
	describedByResolves(t, doc)
	return heading, block
}

// Without script, a row's note link opens the page at the note block, with
// its heading focused, not at the top far above the form it opens. The
// form shows no refusal, so the heading reads no description.
func TestANoteLinkLandsOnTheNoteBlock(t *testing.T) {
	cases := []struct {
		name  string
		world func(*testing.T) *testWorld
		// link is the address of the row's note link on a fresh load of its
		// view.
		link func(*testing.T, *browser) string
	}{
		{"a holder row's Add a note", newFoxholeWorld, func(t *testing.T, b *browser) string {
			return editNoteLink(t, parseHTML(t, b.get(foxholePath)), memberDoe.ID)
		}},
		{"a no-role row's Edit", newNoRoleWorld, func(t *testing.T, b *browser) string {
			row, ok := noRoleRows(t, openNoRoleView(t, b))[memberDoe.ID]
			if !ok {
				t.Fatalf("the no-role view has no row for %s", memberDoe.ID)
			}
			return attrOf(noteLink(t, row), "href")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world(t)
			href := tc.link(t, w.b)

			doc, fragment := landing(t, w.b, href, w.b.get(href))

			heading, _ := landedOnNote(t, doc, fragment)
			if got := accessibleDescription(doc, heading); got != "" {
				t.Errorf("the note block's heading is described as %q, want no description", got)
			}
		})
	}
}

// A Reload link opens the page at its top even with a note form open. The
// blocks that carry one, such as the member list notice, sit above the note
// block, so landing there would scroll past the block the manager reloaded
// to check.
func TestReloadWithANoteFormOpenLandsAtTheTop(t *testing.T) {
	w := newFoxholeWorld(t)
	href := editNoteLink(t, parseHTML(t, w.b.get(foxholePath)), memberDoe.ID)
	w.discord.setMemberList(arrivingList)
	doc, _ := landing(t, w.b, href, w.b.get(href))
	noteBlock(t, doc)
	reload := findElement(memberListNotice(t, doc), "a", "data-field", "reload")
	if reload == nil {
		t.Fatal("the notice has no Reload link")
	}
	to := attrOf(reload, "href")

	_, fragment := landing(t, w.b, to, w.b.get(to))

	if fragment != "" {
		t.Errorf("Reload opens the page at fragment %q, want its top", fragment)
	}
}

// noteFormAction is the address the page's note form posts to, fragment
// and all, and fails the test when the page has no note form.
func noteFormAction(t *testing.T, doc *html.Node) string {
	t.Helper()
	form := findLive(doc, "form", "data-field", "note-form")
	if form == nil {
		t.Fatal("the page has no note form")
	}
	return attrOf(form, "action")
}

// A refused note save answers with the page, not a redirect, at the address
// the note form posts to. Without script the page opens at the note block's
// heading, not at the top far above the form. The refusal note shows under
// the heading and is its description, so a screen reader says why nothing
// saved as focus lands. PRODUCT.md's Accessibility & Inclusion asks this of
// every refused save, whatever refused it.
func TestARefusedNoteSaveLandsOnTheNoteBlock(t *testing.T) {
	cases := []struct {
		name string
		// refuse opens a note form through the page and saves it as a
		// browser with no script does, so the save is refused, and returns
		// the page it saved from and the answer.
		refuse func(*testing.T, *testWorld) (*html.Node, *http.Response)
		// kind is the refusal's data-error kind.
		kind string
	}{
		{"a save that changes nothing", func(t *testing.T, w *testWorld) (*html.Node, *http.Response) {
			assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
			page := openNote(t, w.b, memberDoe.ID)
			return page, submitNote(t, w.b, page, "discharged 12 Sep")
		}, refusalUnchanged},
		{"a form another save got to first", func(t *testing.T, w *testWorld) (*html.Node, *http.Response) {
			kept := openNote(t, w.b, memberDoe.ID)
			assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
			return kept, submitNote(t, w.b, kept, "rejoined, fine to re-add")
		}, refusalStale},
		{"a note started on a member who has since lost their Foxhole role", func(t *testing.T, w *testWorld) (*html.Node, *http.Response) {
			page := openNote(t, w.b, memberKestrel.ID)
			withoutFoxholeRoles(w)
			return page, submitNote(t, w.b, page, "allied group lead")
		}, "not-holder"},
		{"a note started while the member list is partial", func(t *testing.T, w *testWorld) (*html.Node, *http.Response) {
			page := openNote(t, w.b, memberKestrel.ID)
			w.discord.setMemberList(arrivingList)
			return page, submitNote(t, w.b, page, "allied group lead")
		}, "member-list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			page, res := tc.refuse(t, w)

			res, fragment := arrive(t, w.b, noteFormAction(t, page), res)

			doc := parseHTML(t, res)
			heading, block := landedOnNote(t, doc, fragment)
			note := refusalDescribing(t, doc, heading, block)
			if got := attrOf(note, "data-error"); got != tc.kind {
				t.Errorf("the note block shows a %q refusal, want %q", got, tc.kind)
			}
		})
	}
}
