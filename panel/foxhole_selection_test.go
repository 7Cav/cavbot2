package panel

import (
	"maps"
	"net/url"
	"slices"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// addressSelecting is the Foxhole page's address with the view and the
// selection given, the way the page script carries a selection through a
// search, a filter link or a visit to the no-role view.
func addressSelecting(view url.Values, memberIDs ...string) string {
	v := maps.Clone(view)
	if v == nil {
		v = url.Values{}
	}
	v[fieldMember] = memberIDs
	return foxholePath + "?" + v.Encode()
}

// hiddenGroup returns the holder list's group of the selected members
// the search or the filter hides, nil when the page shows none.
func hiddenGroup(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	list := findElement(doc, "", "data-field", "holders")
	if list == nil {
		t.Fatal("the page has no holder list")
	}
	return findElement(list, "", "data-field", "hidden-selection")
}

// groupRows returns the rows of the holder list's group of the selection
// the search or the filter hides, in the order the page shows them, and
// fails the test when the page shows no such group.
func groupRows(t *testing.T, doc *html.Node) []*html.Node {
	t.Helper()
	group := hiddenGroup(t, doc)
	if group == nil {
		t.Fatal("the holder list shows no group of the selection the search or the filter hides")
	}
	var rows []*html.Node
	eachElement(group, func(n *html.Node) {
		if n.Data == "tr" {
			rows = append(rows, n)
		}
	})
	return rows
}

// groupMembers returns the members the rows name, in their order, each
// with whether its box is ticked. A row naming no member is left out.
func groupMembers(rows []*html.Node) ([]string, map[string]bool) {
	var ids []string
	ticked := map[string]bool{}
	for _, row := range rows {
		id, ok := attrValue(row, "data-member")
		if !ok {
			continue
		}
		ids = append(ids, id)
		ticked[id] = boxTicked(row)
	}
	return ids, ticked
}

// matchingRows returns whether each holder list row outside the group of
// the hidden selection is ticked, by member ID: the rows the search and
// the filter match.
func matchingRows(t *testing.T, doc *html.Node) map[string]bool {
	t.Helper()
	inGroup := map[string]bool{}
	if group := hiddenGroup(t, doc); group != nil {
		for _, id := range valuesOf(group, "data-member") {
			inGroup[id] = true
		}
	}
	out := map[string]bool{}
	for id, row := range holderRows(t, doc) {
		if !inGroup[id] {
			out[id] = boxTicked(row)
		}
	}
	return out
}

// A selection carried through a search shows the selected members the
// search hides in a group at the top of the holder list, ticked, under a
// label row and in the list's usual order: by display name ignoring case,
// so Kestrel, whose display name is his username kestrel_tlr, comes before
// SGT Doe.J. Ash, whom the search matches, stays ticked in place among the
// matching rows, and Marsh, matched and not selected, stays unticked.
func TestHolderListShowsTheSelectionASearchHidesAtItsTop(t *testing.T) {
	w := newFoxholeWorld(t)
	address := addressSelecting(url.Values{paramQuery: {"a"}}, memberDoe.ID, memberKestrel.ID, memberAsh.ID)

	doc := parseHTML(t, w.b.get(address))

	rows := groupRows(t, doc)
	if len(rows) == 0 {
		t.Fatal("the group of the hidden selection has no rows")
	}
	if label := findElement(rows[0], "", "data-field", "group-label"); label == nil || textOf(label) == "" {
		t.Error("the group's first row is no label row, want the label heading the group")
	}
	ids, ticked := groupMembers(rows)
	if want := []string{memberKestrel.ID, memberDoe.ID}; !slices.Equal(ids, want) {
		t.Errorf("the group lists %v, want %v in display name order", ids, want)
	}
	for _, id := range ids {
		if !ticked[id] {
			t.Errorf("%s's row in the group is unticked, want it ticked", id)
		}
	}
	if got, want := matchingRows(t, doc), map[string]bool{memberAsh.ID: true, memberMarsh.ID: false}; !maps.Equal(got, want) {
		t.Errorf("the matching rows, each with whether it is ticked, are %v, want %v", got, want)
	}
}

// A selection carried through a filter link shows the selected members the
// filter hides in the group at the top of the holder list: under the
// External filter, Ash, who holds Internal alone, shows there ticked, with
// the Internal approval he has, like any row of the list.
func TestHolderListShowsTheSelectionAFilterHidesAtItsTop(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberAsh.ID)

	doc := parseHTML(t, w.b.get(addressSelecting(url.Values{paramFilter: {"external"}}, memberAsh.ID)))

	rows := groupRows(t, doc)
	ids, ticked := groupMembers(rows)
	if want := []string{memberAsh.ID}; !slices.Equal(ids, want) {
		t.Fatalf("the group lists %v, want %v", ids, want)
	}
	if !ticked[memberAsh.ID] {
		t.Error("Ash's row in the group is unticked, want it ticked")
	}
	if got, want := approvedRoles(t, doc)[memberAsh.ID], []string{"internal"}; !slices.Equal(got, want) {
		t.Errorf("Ash's row in the group shows the approvals %v, want %v", got, want)
	}
}

// A search nothing matches still shows the selection it hides, in the group
// at the top of the holder list, with the empty list's line below it.
func TestHolderListShowsTheHiddenSelectionAboveAnEmptySearch(t *testing.T) {
	w := newFoxholeWorld(t)

	doc := parseHTML(t, w.b.get(addressSelecting(url.Values{paramQuery: {"zzz"}}, memberDoe.ID)))

	ids, ticked := groupMembers(groupRows(t, doc))
	if want := []string{memberDoe.ID}; !slices.Equal(ids, want) || !ticked[memberDoe.ID] {
		t.Errorf("the group lists %v, ticked %v, want %v ticked", ids, ticked, want)
	}
	if got := matchingRows(t, doc); len(got) != 0 {
		t.Errorf("the list shows the matching rows %v, want none", keys(got))
	}
	var order []string
	eachElement(doc, func(n *html.Node) {
		if field, _ := attrValue(n, "data-field"); field == "hidden-selection" || field == "list-empty" {
			order = append(order, field)
		}
	})
	if want := []string{"hidden-selection", "list-empty"}; !slices.Equal(order, want) {
		t.Errorf("the holder list shows %v in that order, want the group above the empty list's line, %v", order, want)
	}
}

// The selection bar acts on the whole selection, the group of the members
// the search hides included: Clear approval pressed on the boxes as the page
// ticks them, the way a browser without script posts them, clears Doe's and
// Kestrel's approvals from the group and Ash's from the matching rows.
func TestClearApprovalActsOnTheSelectionTheSearchHides(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberDoe.ID, memberKestrel.ID, memberAsh.ID)
	doc := parseHTML(t, w.b.get(addressSelecting(url.Values{paramQuery: {"a"}}, memberDoe.ID, memberKestrel.ID, memberAsh.ID)))

	res := submitSelection(t, w.b, doc, opClear)

	assertRedirect(t, res, foxholePath)
	if got := approvedRows(t, parseHTML(t, w.b.get(foxholePath))); len(got) != 0 {
		t.Errorf("after Clear approval the holder list marks %v approved, want nobody", got)
	}
}

// Remove External pressed on the boxes as the page ticks them opens a
// preview of the whole selection: Kestrel, whom the Internal filter hides,
// from the group, and Marsh from the matching rows.
func TestRemovePreviewTakesTheSelectionTheFilterHides(t *testing.T) {
	w := newFoxholeWorld(t)
	doc := parseHTML(t, w.b.get(addressSelecting(url.Values{paramFilter: {"internal"}}, memberKestrel.ID, memberMarsh.ID)))

	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, doc, "external")))

	if got, want := keys(previewList(preview, "loses")), sorted([]string{memberKestrel.ID, memberMarsh.ID}); !slices.Equal(got, want) {
		t.Errorf("the preview lists %v as losing External, want %v", got, want)
	}
}

// filterCounts returns the count each of the page's filter links carries,
// the link to the no-role view's included, by the filter it names.
func filterCounts(t *testing.T, doc *html.Node) map[string]string {
	t.Helper()
	out := map[string]string{}
	eachElement(doc, func(n *html.Node) {
		if filter, ok := attrValue(n, "data-filter"); ok && n.Data == "a" {
			out[filter] = fieldText(t, n, "count")
		}
	})
	return out
}

// Each filter link counts what it would list under the search, whatever the
// selection: the group of the members the search hides adds to no count.
func TestFilterCountsLeaveOutTheHiddenSelection(t *testing.T) {
	w := newFoxholeWorld(t)
	search := url.Values{paramQuery: {"a"}}
	plain := filterCounts(t, parseHTML(t, w.b.get(foxholePath+"?"+search.Encode())))

	selected := filterCounts(t, parseHTML(t, w.b.get(addressSelecting(search, memberDoe.ID, memberKestrel.ID))))

	if !maps.Equal(selected, plain) {
		t.Errorf("with Doe and Kestrel selected the filter links count %v, want %v as with nobody selected", selected, plain)
	}
}

// viewAddresses returns the address of each control on the page that opens
// the page again in a view: the search form's fields, as the address it
// sends, each filter link, the link to the no-role view, the line counting
// the other view's matches and the Reload in the block named busy, the
// progress block or the block naming the commands running. It fails the
// test when the page lacks the search form, the line or the Reload, so a
// page that shows none can't pass for one whose addresses name no members.
func viewAddresses(t *testing.T, doc *html.Node, busy string) map[string]string {
	t.Helper()
	out := map[string]string{}
	form := findElement(doc, "form", "data-field", "search")
	if form == nil {
		t.Fatal("the page has no search form")
	}
	out["search"] = "?" + formFields(doc, form).Encode()
	eachElement(doc, func(n *html.Node) {
		if filter, ok := attrValue(n, "data-filter"); ok && n.Data == "a" {
			out["filter "+filter] = attrOf(n, "href")
		}
	})
	also := findElement(doc, "", "data-field", "also-matches")
	if also == nil {
		t.Fatal("the page has no line counting the other view's matches")
	}
	out["also matches"] = attrOf(findElement(also, "a", "", ""), "href")
	block := findElement(doc, "", "data-field", busy)
	if block == nil {
		t.Fatalf("the page has no %s block", busy)
	}
	reload := findElement(block, "a", "data-keeps-selection", "")
	if reload == nil {
		t.Fatalf("the %s block has no Reload link", busy)
	}
	out["reload"] = attrOf(reload, "href")
	return out
}

// holdAction starts a purge of Internal held at its first role change, so
// the page shows the progress block.
func holdAction(t *testing.T, w *testWorld) {
	t.Helper()
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	hold.next(t)
}

// Without script the selection doesn't travel, so a box a manager unticked
// never comes back ticked. A page whose address names members renders every
// control that opens the page again in a view with an address that names
// none: the search, the filter links, the link to the no-role view and back,
// the line counting the other view's matches, and the Reload while an
// action or a /foxhole command runs. The search "a" matches holders and
// Vance, whose note puts him in the no-role view.
func TestViewAddressesNameNoSelection(t *testing.T) {
	cases := []struct {
		name string
		view url.Values
		// busy starts what the page shows a Reload for, and names the block
		// that shows it.
		busy  func(*testing.T, *testWorld)
		block string
	}{
		{"the holder list while an action runs", url.Values{paramQuery: {"a"}}, holdAction, "progress"},
		{"the holder list while a /foxhole command runs", url.Values{paramQuery: {"a"}},
			func(t *testing.T, w *testWorld) { holdFoxholeAdd(t, w) }, "command-running"},
		{"the no-role view while an action runs", url.Values{paramQuery: {"a"}, paramFilter: {filterNoRole}}, holdAction, "progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			seedNote(t, w, store.MemberNames{MemberID: memberVance.ID, DisplayName: "SGT Vance.R", Username: "rvance"}, "left the regiment")
			tc.busy(t, w)

			doc := parseHTML(t, w.b.get(addressSelecting(tc.view, memberDoe.ID, memberAsh.ID)))

			for control, address := range viewAddresses(t, doc, tc.block) {
				u, err := url.Parse(address)
				if err != nil {
					t.Fatalf("%s: address %q: %v", control, address, err)
				}
				if named := u.Query()[fieldMember]; len(named) > 0 {
					t.Errorf("%s opens %s, which names the members %v, want none", control, address, named)
				}
			}
		})
	}
}

// A successful Approve or Clear approval ends the selection: each lands on
// the holder list with no box ticked, though it acted on members the search
// hid.
func TestApprovalsSaveEndsTheSelectionTheSearchHid(t *testing.T) {
	w := newFoxholeWorld(t)
	address := addressSelecting(url.Values{paramQuery: {"a"}}, memberDoe.ID, memberKestrel.ID, memberAsh.ID)
	for _, op := range []string{opApprove, opClear} {
		res := submitSelection(t, w.b, parseHTML(t, w.b.get(address)), op)

		assertRedirect(t, res, foxholePath)
		doc := parseHTML(t, follow(t, w.b, res))

		if got := tickedRows(t, doc); len(got) != 0 {
			t.Errorf("the %s lands on a page that ticks %v, want no box ticked", op, got)
		}
	}
}

// The no-role view shows nothing of the selection its address carries: no
// box to tick on Vance's row, which his note puts there, and no row or
// field naming a selected member.
func TestNoRoleViewShowsNothingOfTheSelection(t *testing.T) {
	w := newFoxholeWorld(t)
	seedNote(t, w, store.MemberNames{MemberID: memberVance.ID, DisplayName: "SGT Vance.R", Username: "rvance"}, "left the regiment")
	selected := []string{memberDoe.ID, memberKestrel.ID}

	doc := parseHTML(t, w.b.get(addressSelecting(url.Values{paramFilter: {filterNoRole}}, selected...)))

	if findElement(doc, "tr", "data-member", memberVance.ID) == nil {
		t.Fatal("the no-role view has no row for Vance")
	}
	eachLiveElement(doc, func(n *html.Node) {
		if kind, _ := attrValue(n, "type"); n.Data == "input" && kind == "checkbox" {
			t.Errorf("the no-role view shows a checkbox, %s, want none", attrOf(n, "name"))
		}
		for _, id := range selected {
			if member, _ := attrValue(n, "data-member"); member == id {
				t.Errorf("the no-role view shows a row of the selected member %s", id)
			}
			if value, _ := attrValue(n, "value"); n.Data == "input" && value == id {
				t.Errorf("the no-role view has the field %s naming the selected member %s", attrOf(n, "name"), id)
			}
		}
	})
}
