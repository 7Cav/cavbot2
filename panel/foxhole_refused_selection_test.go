package panel

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// The selection lasts until an Approve, a Clear approval or a removal made
// from it goes ahead. A refused Approve or Clear approval answers with the
// members the selection bar posted still ticked, under the search and the
// filter they were selected under: an Approve of Marsh, approved already
// for both roles he holds, and a Clear approval of Ash, who has no
// approval.
func TestARefusedApprovalsSaveKeepsTheSelection(t *testing.T) {
	cases := []struct {
		name   string
		op     string
		member string
	}{
		{"an Approve on a member approved for each Foxhole role they hold", opApprove, memberMarsh.ID},
		{"a Clear approval on a member with no approval", opClear, memberAsh.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			setMembersWithVanceOnInternal(w)
			approveThroughThePage(t, w, memberMarsh.ID)

			res := submitSelection(t, w.b, listUnderASearchAndAFilter(t, w), tc.op, tc.member)

			if res.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusConflict)
			}
			doc := parseHTML(t, res)
			if got, want := tickedRows(t, doc), []string{tc.member}; !slices.Equal(got, want) {
				t.Errorf("the refused page ticks %v, want the selection %v", got, want)
			}
			if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
				t.Errorf("the refused page lists %v, want %v under the search and the filter", got, listedUnderSearchAndFilter)
			}
		})
	}
}

// noticeReload returns the address of the member list notice's Reload link,
// and fails the test when the page has no notice or the notice no link.
func noticeReload(t *testing.T, doc *html.Node) string {
	t.Helper()
	reload := findElement(memberListNotice(t, doc), "a", "data-field", "reload")
	if reload == nil {
		t.Fatal("the member list notice has no Reload link")
	}
	return attrOf(reload, "href")
}

// An Approve pressed while the member list is partial is refused, and the
// member list notice stands in the holder list's place. Once the list is
// complete, the notice's Reload opens the holder list at its heading under
// the same search and filter, with the members the bar posted ticked.
func TestApproveRefusedWhileTheMemberListIsPartialReloadsToTheSelection(t *testing.T) {
	w := newFoxholeWorld(t)
	setMembersWithVanceOnInternal(w)
	listed := listUnderASearchAndAFilter(t, w)
	w.discord.setMemberList(arrivingList)
	refused := parseHTML(t, submitSelection(t, w.b, listed, opApprove, memberAsh.ID, memberMarsh.ID))
	reload := noticeReload(t, refused)
	setMembersWithVanceOnInternal(w)

	doc, fragment := landing(t, w.b, reload, w.b.get(reload))

	landedOnBlock(t, doc, fragment, "holders")
	if got, want := tickedRows(t, doc), sorted([]string{memberAsh.ID, memberMarsh.ID}); !slices.Equal(got, want) {
		t.Errorf("the notice's Reload %s ticks %v, want the selection %v", reload, got, want)
	}
	if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
		t.Errorf("the notice's Reload %s lists %v, want %v under the search and the filter", reload, got, listedUnderSearchAndFilter)
	}
}

// A page load that shows the member list notice answers no refusal, so its
// Reload opens the page in the view it shows, at its top with no box
// ticked, even when its address names a selection, as the address Cancel
// on a remove preview opens does.
func TestMemberListNoticeOfAPageLoadReloadsNoSelection(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.setMemberList(arrivingList)
	selection := url.Values{paramQuery: {"r"}, paramFilter: {"internal"}, fieldMember: {memberAsh.ID, memberMarsh.ID}}
	reload := noticeReload(t, parseHTML(t, w.b.get(foxholePath+"?"+selection.Encode()+"#"+holderListAnchor)))
	setMembersWithVanceOnInternal(w)

	doc, fragment := landing(t, w.b, reload, w.b.get(reload))

	if got := tickedRows(t, doc); len(got) != 0 {
		t.Errorf("the notice's Reload %s ticks %v, want no box ticked", reload, got)
	}
	if fragment != "" {
		t.Errorf("the notice's Reload %s opens at %q, want the top of the page", reload, fragment)
	}
}

// previewMembers returns every member the remove preview lists, under Loses
// or Skipped, sorted.
func previewMembers(preview *html.Node) []string {
	var out []string
	for _, list := range []string{"loses", "skipped"} {
		out = append(out, keys(previewList(preview, list))...)
	}
	return sorted(out)
}

// A Remove pressed while the member list is partial is refused, with the
// member list notice in the holder list's place. Once the list is complete,
// the notice's Reload opens that removal's preview: the same role and
// members, under the same search and filter, with the members ticked in its
// holder list.
func TestRemoveRefusedWhileTheMemberListIsPartialReloadsToItsPreview(t *testing.T) {
	w := newFoxholeWorld(t)
	setMembersWithVanceOnInternal(w)
	listed := listUnderASearchAndAFilter(t, w)
	w.discord.setMemberList(arrivingList)
	refused := parseHTML(t, submitSelection(t, w.b, listed, "external", memberAsh.ID, memberMarsh.ID))
	if findLive(refused, "", "data-error", "member-list") == nil {
		t.Fatal("the page doesn't say the removal was refused for the member list")
	}
	reload := noticeReload(t, refused)
	setMembersWithVanceOnInternal(w)

	doc, fragment := landing(t, w.b, reload, w.b.get(reload))

	_, preview := landedOnBlock(t, doc, fragment, "remove-preview")
	if got := attrOf(preview, "data-role"); got != "external" {
		t.Errorf("the notice's Reload %s opens the preview of removing %q, want external", reload, got)
	}
	selection := sorted([]string{memberAsh.ID, memberMarsh.ID})
	if got := previewMembers(preview); !slices.Equal(got, selection) {
		t.Errorf("the preview lists %v, want the selection %v", got, selection)
	}
	if got := tickedRows(t, doc); !slices.Equal(got, selection) {
		t.Errorf("the preview page ticks %v, want the selection %v", got, selection)
	}
	if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
		t.Errorf("the preview page lists %v, want %v under the search and the filter", got, listedUnderSearchAndFilter)
	}
}

// openRemovalOfAshAndMarsh opens the preview of removing External from Ash,
// who doesn't hold it, and Marsh, who does, from the holder list under the
// search and the filter of listUnderASearchAndAFilter, the way a browser
// with no script does. It returns the preview page and the preview.
func openRemovalOfAshAndMarsh(t *testing.T, w *testWorld) (*html.Node, *html.Node) {
	t.Helper()
	setMembersWithVanceOnInternal(w)
	page := removeUnderASearchAndAFilter("external", memberAsh.ID, memberMarsh.ID)(t, w)
	return page, removePreviewBlock(t, page)
}

// A Confirm pressed while the member list is partial is refused, with the
// refusal at the top of the page and the member list notice in the holder
// list's place. Once the list is complete, the notice's Reload opens the
// preview again, every member it was opened with ticked under the same
// search and filter, the member it skips included.
func TestConfirmRefusedWhileTheMemberListIsPartialReloadsToItsPreview(t *testing.T) {
	w := newFoxholeWorld(t)
	_, preview := openRemovalOfAshAndMarsh(t, w)
	w.discord.setMemberList(arrivingList)

	refused := parseHTML(t, confirmRemoval(t, w.b, preview))

	if got, want := refusalKinds(refused), []string{"member-list"}; !slices.Equal(got, want) {
		t.Errorf("the page shows refusal notes %v, want the one member-list note", got)
	}
	if findElement(refused, "", "data-field", "remove-preview") != nil {
		t.Error("the page opens a remove preview from a partial member list")
	}
	reload := noticeReload(t, refused)
	setMembersWithVanceOnInternal(w)
	doc, fragment := landing(t, w.b, reload, w.b.get(reload))
	landedOnBlock(t, doc, fragment, "remove-preview")
	selection := sorted([]string{memberAsh.ID, memberMarsh.ID})
	if got := tickedRows(t, doc); !slices.Equal(got, selection) {
		t.Errorf("the notice's Reload %s ticks %v, want the selection %v", reload, got, selection)
	}
	if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
		t.Errorf("the notice's Reload %s lists %v, want %v under the search and the filter", reload, got, listedUnderSearchAndFilter)
	}
}

// runAddOfVance starts an add of External to Vance through the page, and
// holds it inside its role change, so it runs until the test ends. Vance
// keeps Internal, so listUnderASearchAndAFilter lists the same members as
// before it.
func runAddOfVance(t *testing.T, w *testWorld) {
	t.Helper()
	hold := holdRoleWrites(t, w)
	page := parseHTML(t, w.b.get(foxholePath))
	preview := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", memberVance.Username))
	assertRedirect(t, confirmAdd(t, w.b, preview, nil), foxholePath)
	hold.next(t)
}

// confirmAction is the address the remove preview's Confirm posts to,
// fragment and all, and fails the test when the preview has no form.
func confirmAction(t *testing.T, preview *html.Node) string {
	t.Helper()
	form := findElement(preview, "form", "", "")
	if form == nil {
		t.Fatal("the remove preview has no form")
	}
	return attrOf(form, "action")
}

// A Confirm pressed while another Foxhole action runs is refused, and the
// answer opens the same preview again, read from Discord as it stands: its
// Confirm off, with the link that opens it again once the action ends. The
// page opens at the preview's heading, whose description is the refusal,
// which shows nowhere else. Every member the preview was opened with stays
// ticked under the same search and filter, the member it skips included.
func TestConfirmRefusedWhileAnotherActionRunsOpensThePreviewAgain(t *testing.T) {
	w := newFoxholeWorld(t)
	_, preview := openRemovalOfAshAndMarsh(t, w)
	confirm := confirmAction(t, preview)
	runAddOfVance(t, w)

	res, fragment := arrive(t, w.b, confirm, confirmRemoval(t, w.b, preview))

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusConflict)
	}
	doc := parseHTML(t, res)
	heading, block := landedOnBlock(t, doc, fragment, "remove-preview")
	if got := attrOf(refusalDescribing(t, doc, heading, block), "data-error"); got != "action-running" {
		t.Errorf("the preview shows a %q refusal, want action-running", got)
	}
	if got, want := refusalKinds(doc), []string{"action-running"}; !slices.Equal(got, want) {
		t.Errorf("the page shows refusal notes %v, want the one under the preview's heading", got)
	}
	if got, want := keys(previewList(block, "loses")), []string{memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the preview lists %v as losing External, want %v", got, want)
	}
	if got, want := keys(previewList(block, "skipped")), []string{memberAsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the preview skips %v, want %v", got, want)
	}
	if button := findElement(block, "", "data-field", "confirm"); button == nil || !disabled(button) {
		t.Error("the preview's Confirm is missing or on while another action runs, want it there and off")
	}
	if findElement(block, "", "data-field", "reopen") == nil {
		t.Error("the preview offers no link to open the removal again")
	}
	if got, want := tickedRows(t, doc), sorted([]string{memberAsh.ID, memberMarsh.ID}); !slices.Equal(got, want) {
		t.Errorf("the page ticks %v, want the selection %v", got, want)
	}
	if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
		t.Errorf("the page lists %v, want %v under the search and the filter", got, listedUnderSearchAndFilter)
	}
}

// A Confirm refused because nobody it lists would change, as when Marsh
// lost External between the preview and the Confirm, opens the preview
// again as it stands: the nobody hint where Confirm would be, no Confirm,
// the refusal under its heading, and Marsh still ticked.
func TestConfirmRefusedForNobodyToChangeOpensThePreviewAgain(t *testing.T) {
	w := newFoxholeWorld(t)
	page := parseHTML(t, w.b.get(foxholePath))
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, page, "external", memberMarsh.ID)))
	confirm := confirmAction(t, preview)
	w.discord.editMember(memberMarsh.ID, func(m *commands.ListedMember) {
		m.RoleIDs = slices.DeleteFunc(m.RoleIDs, func(id string) bool { return id == roleExternal })
	})

	res, fragment := arrive(t, w.b, confirm, confirmRemoval(t, w.b, preview))

	doc := parseHTML(t, res)
	heading, block := landedOnBlock(t, doc, fragment, "remove-preview")
	if got := attrOf(refusalDescribing(t, doc, heading, block), "data-error"); got != refusalNobodyToChange {
		t.Errorf("the preview shows a %q refusal, want %s", got, refusalNobodyToChange)
	}
	if findElement(block, "", "data-field", "nobody-to-remove") == nil {
		t.Error("the preview gives no hint that nobody selected holds External")
	}
	if findElement(block, "", "data-field", "confirm") != nil {
		t.Error("the preview offers a Confirm, want none with nobody to change")
	}
	if got, want := tickedRows(t, doc), []string{memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the page ticks %v, want the selection %v", got, want)
	}
}

// A Confirm removes the role only from the members its preview listed as
// losing it. Ash, whom the preview skipped for not holding External, goes
// back with the Confirm as part of the selection, and gets External before
// the Confirm: the removal takes it from Marsh alone, and Ash keeps it.
func TestConfirmRemovesTheRoleFromTheMembersItsPreviewListedAlone(t *testing.T) {
	w := newFoxholeWorld(t)
	_, preview := openRemovalOfAshAndMarsh(t, w)
	w.discord.editMember(memberAsh.ID, func(m *commands.ListedMember) {
		m.RoleIDs = append(m.RoleIDs, roleExternal)
	})

	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)

	if got, want := w.discord.holdersOf(roleExternal), []string{memberAsh.ID, memberKestrel.ID}; !slices.Equal(sorted(got), want) {
		t.Errorf("after the removal %v hold External, want %v", sorted(got), want)
	}
}

// The remove preview a Retry of a removal opens holds the members the
// removal missed, never a selection. A refused Confirm on it answers with
// the Foxhole page as it stands: nothing open, no box ticked, and the
// refusal at the top of the page, where the page opens.
func TestConfirmRefusedOnARetrysRemovePreviewOpensNothing(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))
	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)
	retry := removePreviewBlock(t, openRetry(t, w.b))
	confirm := confirmAction(t, retry)
	runAddOfVance(t, w)

	res, fragment := arrive(t, w.b, confirm, confirmRemoval(t, w.b, retry))

	doc := parseHTML(t, res)
	if findElement(doc, "", "data-field", "remove-preview") != nil {
		t.Error("the refusal opens a remove preview, want the Foxhole page with nothing open")
	}
	if got := tickedRows(t, doc); len(got) != 0 {
		t.Errorf("the page ticks %v, want no box ticked", got)
	}
	if got, want := refusalKinds(doc), []string{"action-running"}; !slices.Equal(got, want) {
		t.Errorf("the page shows refusal notes %v, want the one action-running note", got)
	}
	if note := firstWithAttr(foxholeBlock(t, doc, "holders"), "data-error"); note != nil {
		t.Error("the refusal shows in the holder list's block, want it at the top of the page")
	}
	if at := landedOn(doc, fragment); at != nil {
		t.Errorf("the page opens at %q, want its top", fragment)
	}
}

// A member selected who has no holder list row by the time the selection
// comes back, as Vance once he leaves the server, is left out: the refused
// page ticks the rest and says nothing about him.
func TestARefusedSaveLeavesOutASelectedMemberWithNoRow(t *testing.T) {
	w := newFoxholeWorld(t)
	setMembersWithVanceOnInternal(w)
	page := parseHTML(t, w.b.get(foxholePath))
	setMembers(w, memberDoe, memberAsh, memberKestrel, memberMarsh)

	res := submitSelection(t, w.b, page, opClear, memberKestrel.ID, memberVance.ID)

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusConflict)
	}
	doc := parseHTML(t, res)
	if got, want := tickedRows(t, doc), []string{memberKestrel.ID}; !slices.Equal(got, want) {
		t.Errorf("the page ticks %v, want %v", got, want)
	}
	for _, name := range []string{memberVance.ID, memberVance.Username} {
		if strings.Contains(textOf(doc), name) {
			t.Errorf("the page's text names %s, who left the server, want nothing said about him", name)
		}
	}
}
