package panel

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// namedAddress is the address a page that answers a form names as the one
// that shows it, which the page script puts in the tab (ADR 0019). It fails
// the test when the page names none.
func namedAddress(t *testing.T, doc *html.Node) string {
	t.Helper()
	main := findElement(doc, "main", "", "")
	if main == nil {
		t.Fatal("the page has no <main>")
	}
	address, ok := attrValue(main, "data-address")
	if !ok {
		t.Fatal("the page names no address that shows it")
	}
	return address
}

// proxyRequestLine is the longest request line nginx in front of the panel
// takes by default. An address at least this long gets a 414.
const proxyRequestLine = 8 * 1024

// longPaste is a paste box's text longer than proxyRequestLine once
// URL-encoded, one made-up member a line.
func longPaste(t *testing.T) string {
	t.Helper()
	var lines []string
	for i := range 500 {
		lines = append(lines, fmt.Sprintf("allied_member_%03d", i))
	}
	paste := strings.Join(lines, "\n")
	if n := len(url.QueryEscape(paste)); n <= proxyRequestLine {
		t.Fatalf("the paste is %d bytes URL-encoded, want more than %d", n, proxyRequestLine)
	}
	return paste
}

// A Foxhole form that saves or starts something, and a preview the panel
// answers by POST, answers with the Foxhole page itself at the form's
// address (ADR 0019). Each answer keeps its status and names an address
// that shows that page by GET, as it stands then, which the page script
// puts in the tab, so a reload never sends the form again. The family rows
// load that address the way a browser does and check what it shows. The
// others share their address with a family and check only their status.
func TestFoxholeFormAnswersNameAnAddressThatShowsTheirPage(t *testing.T) {
	cases := []struct {
		name string
		// answer readies the world and returns the answer to the form, posted
		// the way a browser with no script posts it, and what happens after
		// it and before the reload, nil for nothing.
		answer func(t *testing.T, w *testWorld) (*http.Response, func())
		status int
		// shows checks the page the named address loads, opened at fragment.
		// Nil for a row that checks only its status.
		shows func(t *testing.T, doc *html.Node, fragment string)
	}{
		{name: "a refused Approve under a search and a filter", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				setMembersWithVanceOnInternal(w)
				// Marsh is approved already for both roles he holds.
				approveThroughThePage(t, w, memberMarsh.ID)
				return submitSelection(t, w.b, listUnderASearchAndAFilter(t, w), opApprove, memberMarsh.ID), nil
			},
			shows: func(t *testing.T, doc *html.Node, fragment string) {
				landedOnBlock(t, doc, fragment, "holders")
				if got, want := tickedRows(t, doc), []string{memberMarsh.ID}; !slices.Equal(got, want) {
					t.Errorf("the page ticks %v, want the selection %v", got, want)
				}
				if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
					t.Errorf("the page lists %v, want %v under the search and the filter", got, listedUnderSearchAndFilter)
				}
			}},
		{name: "a refused note save", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				seedNote(t, w, namesOf(memberAsh), "allied liaison")
				kept := openNote(t, w.b, memberAsh.ID)
				// Another save gets to Ash's note first, so the kept form is stale.
				assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberAsh.ID), "liaison to the 101st"), foxholePath)
				return submitNote(t, w.b, kept, "liaison, ask S1"), nil
			},
			shows: func(t *testing.T, doc *html.Node, fragment string) {
				heading, _ := landedOnBlock(t, doc, fragment, "note-block")
				if got := textOf(heading); !strings.Contains(got, memberAsh.Username) {
					t.Errorf("the note block's heading reads %q, want it to name %s", got, memberAsh.Username)
				}
				if got, want := typedNote(t, doc), "liaison to the 101st"; got != want {
					t.Errorf("the note box holds %q, want the note as stored, %q", got, want)
				}
			}},
		{name: "a remove Confirm refused while another action runs, from a preview the selection bar opened", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "internal", memberAsh.ID, memberDoe.ID)))
				hold := holdRoleWrites(t, w)
				startPurge(t, w, "external")
				hold.next(t)
				return confirmRemoval(t, w.b, preview), func() {
					hold.open()
					w.awaitActionEnd(t)
				}
			},
			shows: func(t *testing.T, doc *html.Node, fragment string) {
				_, preview := landedOnBlock(t, doc, fragment, "remove-preview")
				if got, want := previewMembers(preview), sorted([]string{memberAsh.ID, memberDoe.ID}); !slices.Equal(got, want) {
					t.Errorf("the remove preview names %v, want the selection %v", got, want)
				}
				if !enabledConfirm(preview) {
					t.Error("the remove preview offers no Confirm once the action that blocked it has ended")
				}
			}},
		{name: "a refused purge", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) { return stalePurge("external")(t, w)(), nil },
			shows: func(t *testing.T, _ *html.Node, fragment string) {
				if fragment != "" {
					t.Errorf("the address opens the page at %q, want its top", fragment)
				}
			}},
		{name: "the add preview of a paste longer than the proxy's request line", status: http.StatusOK,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				page := parseHTML(t, w.b.get(foxholePath))
				return previewPaste(t, w.b, page, addForm(t, page), "external", longPaste(t)), nil
			},
			shows: func(t *testing.T, doc *html.Node, _ string) {
				fields := formFields(doc, addForm(t, doc))
				if got := fields.Get(fieldRole); got != "external" {
					t.Errorf("the paste box picks the role %q, want external", got)
				}
				if got := fields.Get(fieldLines); got != "" {
					t.Errorf("the paste box holds %q, want it empty", got)
				}
			}},
		{name: "a refused Clear approval", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				// Ash has no approval to clear.
				return submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opClear, memberAsh.ID), nil
			}},
		{name: "a refused re-add", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) { return staleReAdd(t, w)(), nil }},
		{name: "a refused Retry", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) { return stalePurgeRetry(t, w)(), nil }},
		{name: "a roster add refused for nobody to change", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) { return staleRosterAdd(t, w)(), nil }},
		{name: "a refused add Confirm", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) { return staleAdd("rvance")(t, w)(), nil }},
		{name: "a remove Confirm refused while another action runs, from a Retry's preview", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				failedRemoval(t, w)
				preview := removePreviewBlock(t, openRetry(t, w.b))
				hold := holdRoleWrites(t, w)
				startPurge(t, w, "external")
				hold.next(t)
				return confirmRemoval(t, w.b, preview), nil
			}},
		{name: "an Approve refused while the member list is partial", status: http.StatusServiceUnavailable,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				page := parseHTML(t, w.b.get(foxholePath))
				w.discord.setMemberList(arrivingList)
				return submitSelection(t, w.b, page, opApprove, memberAsh.ID), nil
			}},
		{name: "a remove Confirm refused while the member list is partial", status: http.StatusServiceUnavailable,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))
				w.discord.setMemberList(arrivingList)
				return confirmRemoval(t, w.b, preview), nil
			}},
		{name: "a roster add refused because the roster changed", status: http.StatusConflict,
			answer: func(t *testing.T, w *testWorld) (*http.Response, func()) {
				api := serveRoster(t, trooperVance)
				preview := parseHTML(t, previewRoster(t, w, "D/ACD"))
				api.setTroopers(trooperVance, trooperDoe)
				return confirmRoster(t, w.b, preview), nil
			},
			shows: func(t *testing.T, doc *html.Node, fragment string) {
				_, preview := landedOnBlock(t, doc, fragment, "roster-preview")
				if got, want := slices.Sorted(maps.Keys(rosterRows(preview))), []string{"Doe.J", "Vance.R"}; !slices.Equal(got, want) {
					t.Errorf("the roster preview lists %v, want the roster as it stands, %v", got, want)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)

			res, then := tc.answer(t, w)

			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
			address := namedAddress(t, parseHTML(t, res))
			if len(address) >= proxyRequestLine {
				t.Errorf("the address is %d bytes long, want it under the proxy's %d-byte request line", len(address), proxyRequestLine)
			}
			if tc.shows == nil {
				return
			}
			if then != nil {
				then()
			}
			doc, fragment := landing(t, w.b, address, w.b.get(address))
			tc.shows(t, doc, fragment)
		})
	}
}

// Preview roster loads the Foxhole page by GET, with the unit in its
// address, so a reload of the roster preview reads the roster again and
// never sends a form (ADR 0019). Doe joins the roster between the two
// loads, and the reload lists them.
func TestRosterPreviewReloadReadsTheRosterAgain(t *testing.T) {
	w := newFoxholeWorld(t)
	api := serveRoster(t, trooperVance)
	address, res := pressPreviewRoster(t, w, "D/ACD")
	doc, fragment := landing(t, w.b, address, res)
	landedOnBlock(t, doc, fragment, "roster-preview")
	api.setTroopers(trooperVance, trooperDoe)

	res = w.b.reload()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("the reload answered %d, want 200", res.StatusCode)
	}
	rows := rosterRows(rosterPreviewBlock(t, parseHTML(t, res)))
	if got, want := slices.Sorted(maps.Keys(rows)), []string{"Doe.J", "Vance.R"}; !slices.Equal(got, want) {
		t.Errorf("the reloaded roster preview lists %v, want the roster as it stands, %v", got, want)
	}
}

// The panel no longer serves the address Preview roster posted to before it
// became a GET. A Foxhole manager's post there gets the answer of any
// address the panel doesn't serve.
func TestOldRosterPreviewAddressIsServedNoMore(t *testing.T) {
	w := newFoxholeWorld(t)
	serveRoster(t, trooperVance)
	form := url.Values{fieldUnit: {"D/ACD"}}

	got := w.b.postForm("/foxhole/roster/preview", form).StatusCode

	if want := w.b.postForm("/foxhole/nothing-here", form).StatusCode; got != want {
		t.Errorf("a post to the old address answers %d, want %d, the answer of an address the panel doesn't serve", got, want)
	}
}

// A reload of an add preview while the member list is partial still shows
// the paste box, with the role the address names picked, so the page script
// has a box to put back the paste the tab keeps. It says nothing was
// refused: nothing was sent. Once the list has arrived, the member list notice's
// Reload opens the add preview's address again.
func TestAddPreviewAddressWhileTheMemberListIsPartialShowsThePasteBox(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.setMemberList(arrivingList)
	address := foxholePath + "?" + url.Values{paramAdd: {"external"}}.Encode() + "#" + addPreviewAnchor

	doc := parseHTML(t, w.b.get(address))

	if got := formFields(doc, addForm(t, doc)).Get(fieldRole); got != "external" {
		t.Errorf("the paste box picks the role %q, want external", got)
	}
	if kinds := refusalKinds(doc); len(kinds) != 0 {
		t.Errorf("the page gives the refusals %v, want none", kinds)
	}
	reload := noticeReload(t, doc)
	setMembersWithVanceOnInternal(w)
	reloaded, _ := landing(t, w.b, reload, w.b.get(reload))
	if got := formFields(reloaded, addForm(t, reloaded)).Get(fieldRole); got != "external" {
		t.Errorf("the notice's Reload %s opens the paste box with the role %q picked, want external", reload, got)
	}
}

// An Approve refused while the member list is partial names the holder
// list with the selection ticked as its address, and the page script puts
// it in the tab. The member list notice's Reload then still loads that
// page once the list has arrived. A link to the very address the tab shows
// would only scroll.
func TestNoticeReloadOfARefusedApproveLoadsFromTheAddressThePageNames(t *testing.T) {
	w := newFoxholeWorld(t)
	setMembersWithVanceOnInternal(w)
	listed := listUnderASearchAndAFilter(t, w)
	w.discord.setMemberList(arrivingList)
	refused := parseHTML(t, submitSelection(t, w.b, listed, opApprove, memberAsh.ID, memberMarsh.ID))
	w.b.replaceState(namedAddress(t, refused))
	reload := noticeReload(t, refused)
	setMembersWithVanceOnInternal(w)

	doc, fragment := landing(t, w.b, reload, w.b.get(reload))

	if findElement(doc, "", "data-notice", "member-list") != nil {
		t.Fatalf("the notice's Reload %s left the tab on the member list notice", reload)
	}
	landedOnBlock(t, doc, fragment, "holders")
	if got, want := tickedRows(t, doc), sorted([]string{memberAsh.ID, memberMarsh.ID}); !slices.Equal(got, want) {
		t.Errorf("the notice's Reload %s ticks %v, want the selection %v", reload, got, want)
	}
}

// Pressing Preview roster again from the open roster preview reads the
// roster again, as the first press does. With one validated unit, the
// second press asks for the very address the tab shows, which a browser
// only scrolls to unless the press goes through a redirect.
func TestPreviewRosterPressedAgainReadsTheRosterAgain(t *testing.T) {
	w := newFoxholeWorld(t)
	api := serveRoster(t, trooperVance)
	address, res := pressPreviewRoster(t, w, "D/ACD")
	doc, _ := landing(t, w.b, address, res)
	api.setTroopers(trooperVance, trooperDoe)

	address, res = pressPreviewRosterOn(t, w.b, doc, "D/ACD")

	again, _ := landing(t, w.b, address, res)
	rows := rosterRows(rosterPreviewBlock(t, again))
	if got, want := slices.Sorted(maps.Keys(rows)), []string{"Doe.J", "Vance.R"}; !slices.Equal(got, want) {
		t.Errorf("the second Preview roster lists %v, want the roster as it stands, %v", got, want)
	}
}
