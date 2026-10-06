package panel

import (
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// submitSelection ticks the holder list's checkbox of each member named and
// presses the selection bar's button whose value is op, the way a browser
// submits the selection: the fields the selection form owns, the ticked
// boxes among them, and the name and value of the button pressed, sent by
// the method and to the address the button names, else the form's.
func submitSelection(t *testing.T, b *browser, doc *html.Node, op string, memberIDs ...string) *http.Response {
	t.Helper()
	rows := holderRows(t, doc)
	for _, id := range memberIDs {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("the holder list has no row for %s", id)
		}
		box := findElement(row, "input", "type", "checkbox")
		if box == nil {
			t.Fatalf("%s's row has no checkbox", id)
		}
		box.Attr = append(box.Attr, html.Attribute{Key: "checked"})
	}
	form := findElement(doc, "form", "action", foxholeApprovalsPath)
	if form == nil {
		t.Fatal("the page has no selection form")
	}
	var button *html.Node
	eachLiveElement(doc, func(n *html.Node) {
		if v, _ := attrValue(n, "value"); n.Data == "button" && v == op && ownedBy(n, form) {
			button = n
		}
	})
	if button == nil {
		t.Fatalf("the selection form has no %s button", op)
	}
	fields := formPosts(t, doc, foxholeApprovalsPath)
	name, _ := attrValue(button, "name")
	fields.Set(name, op)
	action, method := foxholeApprovalsPath, http.MethodPost
	if v, ok := attrValue(button, "formaction"); ok {
		action = v
	}
	if v, ok := attrValue(button, "formmethod"); ok {
		method = strings.ToUpper(v)
	}
	if method == http.MethodGet {
		return b.get(action + "?" + fields.Encode())
	}
	return b.postForm(action, fields)
}

// approvedRows returns the holder list's rows that carry the approved mark,
// by member ID, sorted.
func approvedRows(t *testing.T, doc *html.Node) []string {
	t.Helper()
	var out []string
	for id, row := range holderRows(t, doc) {
		if len(valuesOf(row, "data-approved")) > 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// newestChange returns the action of the newest entry in the Foxhole page's
// change log and the members it names as touched, sorted.
func newestChange(t *testing.T, doc *html.Node) (string, []string) {
	t.Helper()
	section := findElement(doc, "", "data-field", "changes")
	if section == nil {
		t.Fatal("the page has no change log")
	}
	entries := entryElements(section)
	if len(entries) == 0 {
		t.Fatal("the change log has no entries")
	}
	action, _ := attrValue(entries[0], "data-action")
	touched := valuesOf(entries[0], "data-touched")
	slices.Sort(touched)
	return action, touched
}

// Approve on a selection marks each selected External holder approved and
// skips the selected member who doesn't hold External. The page it lands on
// says it skipped them, and the change log's one new entry names the
// members the save approved.
func TestApproveMarksTheExternalHoldersAndSkipsOneWithoutExternal(t *testing.T) {
	w := newFoxholeWorld(t)

	res := submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opApprove, memberKestrel.ID, memberMarsh.ID, memberDoe.ID)

	assertRedirect(t, res, foxholePath)
	page := parseHTML(t, follow(t, w.b, res))
	approved := []string{memberKestrel.ID, memberMarsh.ID}
	if got := approvedRows(t, page); !slices.Equal(got, approved) {
		t.Errorf("the rows marked approved are %v, want %v", got, approved)
	}
	if got, want := valuesOf(page, "data-skipped"), []string{memberDoe.ID}; !slices.Equal(got, want) {
		t.Errorf("the page says it skipped %v, want %v", got, want)
	}
	if action, touched := newestChange(t, page); action != "approve" || !slices.Equal(touched, approved) {
		t.Errorf("the newest change log entry is %s touching %v, want approve touching %v", action, touched, approved)
	}
	if n := len(entryElements(findElement(page, "", "data-field", "changes"))); n != 1 {
		t.Errorf("the change log holds %d entries, want 1", n)
	}
}

// The line naming a member Approve skipped shows only while it is still
// true. Once the skipped member holds External, the address the save
// redirected to, reloaded or reached by Back, no longer names them.
func TestApproveSkipLineGoesOnceTheMemberHoldsExternal(t *testing.T) {
	w := newFoxholeWorld(t)
	res := submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opApprove, memberKestrel.ID, memberDoe.ID)
	landing := location(t, res).RequestURI()
	doe := memberDoe
	doe.RoleIDs = append(slices.Clone(doe.RoleIDs), roleExternal)
	setMembers(w, doe, memberAsh, memberKestrel, memberMarsh, memberVance)

	page := parseHTML(t, w.b.get(landing))

	if got := valuesOf(page, "data-skipped"); len(got) != 0 {
		t.Errorf("reloading %s says it skipped %v, want none", landing, got)
	}
}

// approveThroughThePage approves the members named through the selection
// bar, as a manager does, and fails the test unless the save lands.
func approveThroughThePage(t *testing.T, w *testWorld, memberIDs ...string) {
	t.Helper()
	res := submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opApprove, memberIDs...)
	assertRedirect(t, res, foxholePath)
}

// Clear approval on a selection clears each selected member's approval, and
// the change log's one new entry names every member it cleared.
func TestClearApprovalClearsEachSelectedApproval(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberKestrel.ID, memberMarsh.ID)

	res := submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opClear, memberKestrel.ID, memberMarsh.ID)

	assertRedirect(t, res, foxholePath)
	page := parseHTML(t, follow(t, w.b, res))
	if got := approvedRows(t, page); len(got) != 0 {
		t.Errorf("the rows marked approved are %v, want none", got)
	}
	want := []string{memberKestrel.ID, memberMarsh.ID}
	if action, touched := newestChange(t, page); action != "clear_approval" || !slices.Equal(touched, want) {
		t.Errorf("the newest change log entry is %s touching %v, want clear_approval touching %v", action, touched, want)
	}
}

// Approve and Clear approval make no call to Discord's API, read or write:
// Approve reads the member list from the gateway state, Clear approval
// reads the store alone, and neither changes a role. The Approve selection
// holds Doe, who doesn't hold External, the member a save that granted
// External would change. Each count runs across the save alone, not the
// page it redirects to.
func TestApprovalsSavesMakeNoDiscordCall(t *testing.T) {
	w := newFoxholeWorld(t)
	saves := []struct {
		press   string
		members []string
	}{
		{opApprove, []string{memberKestrel.ID, memberDoe.ID}},
		{opClear, []string{memberKestrel.ID}},
	}
	for _, save := range saves {
		page := parseHTML(t, w.b.get(foxholePath))
		before := w.discord.apiCallCount()

		res := submitSelection(t, w.b, page, save.press, save.members...)

		assertRedirect(t, res, foxholePath)
		if n := w.discord.apiCallCount() - before; n != 0 {
			t.Errorf("the %s save made %d calls to Discord's API, want 0", save.press, n)
		}
	}
}

// While the member list is partial, Approve can't see who holds External:
// the page the manager selected on was loaded with the list whole, the list
// goes partial before they press Approve, and the save is refused with the
// member-list reason, which says nothing changed. Clear approval reads
// nothing from the list, so it still saves.
func TestWhileTheMemberListIsPartialApproveIsRefusedAndClearApprovalSaves(t *testing.T) {
	t.Run("approve", func(t *testing.T) {
		w := newFoxholeWorld(t)
		page := parseHTML(t, w.b.get(foxholePath))
		w.discord.setMemberList(arrivingList)

		res := submitSelection(t, w.b, page, opApprove, memberKestrel.ID)

		if findLive(parseHTML(t, res), "", "data-error", "member-list") == nil {
			t.Errorf("the answer (status %d) names no member-list refusal", res.StatusCode)
		}
	})
	t.Run("clear approval", func(t *testing.T) {
		w := newFoxholeWorld(t)
		approveThroughThePage(t, w, memberKestrel.ID)
		page := parseHTML(t, w.b.get(foxholePath))
		w.discord.setMemberList(arrivingList)

		res := submitSelection(t, w.b, page, opClear, memberKestrel.ID)

		assertRedirect(t, res, foxholePath)
		for _, rec := range readNoteState(t, w.st).Records {
			if rec.MemberID == memberKestrel.ID && rec.Approved {
				t.Errorf("%s is still approved after the clear", memberKestrel.ID)
			}
		}
	})
}

// filterAnchor returns the page's filter link named filter, and fails the
// test when the page has none.
func filterAnchor(t *testing.T, doc *html.Node, filter string) *html.Node {
	t.Helper()
	link := findElement(doc, "a", "data-filter", filter)
	if link == nil {
		t.Fatalf("the page has no %s filter link", filter)
	}
	return link
}

// The holder list keeps listing approved collaborators who no longer hold
// External. One who left the server shows under the names the panel last
// saw, flagged "not in the server"; one in the server without External is
// flagged "approved, doesn't hold External". Both carry the approved mark.
// Flagged counts and lists them with the Internal holder who has no rank
// role, and Approved counts and lists the approved collaborators.
func TestHolderListShowsApprovedCollaboratorsWithTheirFlags(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberKestrel.ID, memberMarsh.ID)
	marsh := memberMarsh
	marsh.RoleIDs = []string{roleInternal, roleCPT}
	setMembers(w, memberDoe, memberAsh, marsh, memberVance)

	page := parseHTML(t, w.b.get(foxholePath))

	rows := holderRows(t, page)
	kestrel, ok := rows[memberKestrel.ID]
	if !ok {
		t.Fatalf("the holder list has no row for %s, who left the server", memberKestrel.ID)
	}
	if got := valuesOf(kestrel, "data-flag"); !slices.Equal(got, []string{"not-in-server"}) {
		t.Errorf("%s's flags = %v, want [not-in-server]", memberKestrel.ID, got)
	}
	if name, user := fieldText(t, kestrel, "display_name"), fieldText(t, kestrel, "username"); name != "kestrel_tlr" || user != "kestrel_tlr" {
		t.Errorf("%s shows as %q @%s, want the last names seen, kestrel_tlr @kestrel_tlr", memberKestrel.ID, name, user)
	}
	if got := valuesOf(rows[memberMarsh.ID], "data-flag"); !slices.Equal(got, []string{"approved-not-external"}) {
		t.Errorf("%s's flags = %v, want [approved-not-external]", memberMarsh.ID, got)
	}
	approved := []string{memberKestrel.ID, memberMarsh.ID}
	if got := approvedRows(t, page); !slices.Equal(got, approved) {
		t.Errorf("the rows marked approved are %v, want %v", got, approved)
	}
	wants := map[string][]string{
		"flagged":  {memberAsh.ID, memberKestrel.ID, memberMarsh.ID},
		"approved": approved,
	}
	for filter, want := range wants {
		link := filterAnchor(t, page, filter)
		if got := fieldText(t, link, "count"); got != strconv.Itoa(len(want)) {
			t.Errorf("the %s link's count = %q, want %d", filter, got, len(want))
		}
		href, _ := attrValue(link, "href")
		if got := keys(holderRows(t, parseHTML(t, w.b.get(href)))); !slices.Equal(got, want) {
			t.Errorf("the %s link lists %v, want %v", filter, got, want)
		}
	}
}

// A note starts on an approved collaborator as on a Foxhole role holder,
// even once they hold no Foxhole role: the row's Edit link opens the note
// form, and the note saved shows on the row.
func TestNoteStartsOnAnApprovedCollaboratorWithNoFoxholeRole(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberKestrel.ID)
	kestrel := memberKestrel
	kestrel.RoleIDs = nil
	setMembers(w, memberDoe, memberAsh, kestrel, memberMarsh, memberVance)

	res := submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "allied group lead")

	assertRedirect(t, res, foxholePath)
	if got := noteOf(t, parseHTML(t, follow(t, w.b, res)), memberKestrel.ID); got != "allied group lead" {
		t.Errorf("%s's row shows the note %q, want the one saved", memberKestrel.ID, got)
	}
}

// An approvals save sits behind the Foxhole page's gate: a signed-in forum
// user who opens no page posts the selection bar's Approve and gets the
// no-access page, and the store holds what it held.
func TestApproveByAUserInNeitherGroupIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	fields := formPosts(t, parseHTML(t, w.b.get(foxholePath)), foxholeApprovalsPath)
	fields.Set(fieldMember, memberKestrel.ID)
	fields.Set(fieldOp, opApprove)
	outsider := newBrowser(t, w.p)
	signInAs(t, w.forum, outsider, addUserOutsideAdminGroups(w.forum))
	before := readNoteState(t, w.st)

	res := outsider.postForm(foxholeApprovalsPath, fields)

	assertNoAccessPage(t, parseHTML(t, follow(t, outsider, res)))
	if after := readNoteState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
	}
}
