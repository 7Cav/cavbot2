package panel

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
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
		return b.getForm(action, fields)
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

// approvedRoles returns the roles each holder list row's Approved tags name,
// by member ID, each sorted. A row with no approval is left out.
func approvedRoles(t *testing.T, doc *html.Node) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for id, row := range holderRows(t, doc) {
		if roles := valuesOf(row, "data-approved"); len(roles) > 0 {
			slices.Sort(roles)
			out[id] = roles
		}
	}
	return out
}

// Approve on a selection gives each selected member an approval for each
// Foxhole role they hold: Doe holds Internal, Kestrel External and Marsh
// both. Nobody is skipped, so the page it lands on names nobody as
// skipped, and the change log's one new entry names the members the save
// approved.
func TestApproveGivesEachMemberAnApprovalForEachFoxholeRoleTheyHold(t *testing.T) {
	w := newFoxholeWorld(t)

	res := submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opApprove, memberDoe.ID, memberKestrel.ID, memberMarsh.ID)

	assertRedirect(t, res, foxholePath)
	page := parseHTML(t, follow(t, w.b, res))
	want := map[string][]string{
		memberDoe.ID:     {"internal"},
		memberKestrel.ID: {"external"},
		memberMarsh.ID:   {"external", "internal"},
	}
	if got := approvedRoles(t, page); !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("the rows' approvals are %v, want %v", got, want)
	}
	if got := valuesOf(page, "data-skipped"); len(got) != 0 {
		t.Errorf("the page says it skipped %v, want nobody", got)
	}
	approved := sorted([]string{memberDoe.ID, memberKestrel.ID, memberMarsh.ID})
	if action, touched := newestChange(t, page); action != "approve" || !slices.Equal(touched, approved) {
		t.Errorf("the newest change log entry is %s touching %v, want approve touching %v", action, touched, approved)
	}
	if n := len(entryElements(findElement(page, "", "data-field", "changes"))); n != 1 {
		t.Errorf("the change log holds %d entries, want 1", n)
	}
}

// withoutFoxholeRoles is the fixture's member list with Kestrel holding no
// Foxhole role, as after a purge: an approved Foxholer is the one member
// the holder list shows with none.
func withoutFoxholeRoles(w *testWorld) {
	kestrel := memberKestrel
	kestrel.RoleIDs = nil
	setMembers(w, memberDoe, memberAsh, kestrel, memberMarsh, memberVance)
}

// Approve skips a selected member who holds no Foxhole role, so has no role
// to be approved for, and approves the rest of the selection. The page it
// lands on names the member it skipped.
func TestApproveSkipsAMemberHoldingNoFoxholeRoleAndApprovesTheRest(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberKestrel.ID)
	withoutFoxholeRoles(w)

	res := submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opApprove, memberKestrel.ID, memberDoe.ID)

	assertRedirect(t, res, foxholePath)
	page := parseHTML(t, follow(t, w.b, res))
	if got, want := approvedRoles(t, page)[memberDoe.ID], []string{"internal"}; !slices.Equal(got, want) {
		t.Errorf("%s's approvals are %v, want %v", memberDoe.ID, got, want)
	}
	if got, want := valuesOf(page, "data-skipped"), []string{memberKestrel.ID}; !slices.Equal(got, want) {
		t.Errorf("the page says it skipped %v, want %v", got, want)
	}
}

// The line naming a member Approve skipped shows only while it is still
// true. Once the skipped member holds a Foxhole role again, Internal here,
// the address the save redirected to, reloaded or reached by Back, no
// longer names them.
func TestApproveSkipLineGoesOnceTheMemberHoldsAFoxholeRole(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberKestrel.ID)
	withoutFoxholeRoles(w)
	res := submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), opApprove, memberKestrel.ID, memberDoe.ID)
	landing := location(t, res).RequestURI()
	kestrel := memberKestrel
	kestrel.RoleIDs = []string{roleInternal}
	setMembers(w, memberDoe, memberAsh, kestrel, memberMarsh, memberVance)

	page := parseHTML(t, w.b.get(landing))

	if got := valuesOf(page, "data-skipped"); len(got) != 0 {
		t.Errorf("reloading %s says it skipped %v, want none", landing, got)
	}
}

// The Approved filter counts and lists every member with an approval,
// whichever role it is for: Ash approved for Internal, Kestrel for
// External and Marsh for both.
func TestApprovedFilterListsEveryoneWithAnApproval(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApprovals(t, w,
		store.MemberApprovals{MemberNames: namesOf(memberAsh), Approved: store.FoxholeApprovals{Internal: true}},
		store.MemberApprovals{MemberNames: namesOf(memberKestrel), Approved: store.FoxholeApprovals{External: true}},
		store.MemberApprovals{MemberNames: namesOf(memberMarsh), Approved: store.FoxholeApprovals{Internal: true, External: true}})

	link := filterAnchor(t, parseHTML(t, w.b.get(foxholePath)), "approved")

	want := sorted([]string{memberAsh.ID, memberKestrel.ID, memberMarsh.ID})
	if got := fieldText(t, link, "count"); got != strconv.Itoa(len(want)) {
		t.Errorf("the approved link's count = %q, want %d", got, len(want))
	}
	href, _ := attrValue(link, "href")
	if got := keys(holderRows(t, parseHTML(t, w.b.get(href)))); !slices.Equal(got, want) {
		t.Errorf("the approved link lists %v, want %v", got, want)
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
			if rec.MemberID == memberKestrel.ID && rec.Approved.Any() {
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

// Approve on a selection that would approve nobody and skip nobody changes
// nothing: every selected member is an approved collaborator already, or
// nothing was ticked, which only a browser without the page script sends.
// The save is refused with the nobody-to-change reason, and the store and
// the change log are as before.
func TestApproveThatWouldChangeNobodyIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		members []string
	}{
		{"everyone selected is approved", []string{memberKestrel.ID, memberMarsh.ID}},
		{"nothing ticked", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			approveThroughThePage(t, w, memberKestrel.ID, memberMarsh.ID)
			page := parseHTML(t, w.b.get(foxholePath))
			before := readNoteState(t, w.st)

			res := submitSelection(t, w.b, page, opApprove, tc.members...)

			assertRefused(t, res, refusalNobodyToChange, w.st, before)
		})
	}
}

// An Approve that approves nobody but skips someone isn't a no-op the page
// has nothing to say about: the save lands, and the page it lands on names
// the member skipped for holding no Foxhole role.
func TestApproveThatOnlySkipsSomeoneStillLandsWithTheSkipLine(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberKestrel.ID)
	withoutFoxholeRoles(w)
	page := parseHTML(t, w.b.get(foxholePath))

	res := submitSelection(t, w.b, page, opApprove, memberKestrel.ID)

	assertRedirect(t, res, foxholePath)
	if got, want := valuesOf(parseHTML(t, follow(t, w.b, res)), "data-skipped"), []string{memberKestrel.ID}; !slices.Equal(got, want) {
		t.Errorf("the page says it skipped %v, want %v", got, want)
	}
}

// Clear approval on a selection with no approved collaborator in it, or
// with nothing ticked, changes nothing. The save is refused with the
// nobody-to-change reason, and the store and the change log are as before.
func TestClearApprovalThatWouldChangeNobodyIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		members []string
	}{
		{"nobody selected is approved", []string{memberMarsh.ID, memberDoe.ID}},
		{"nothing ticked", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			approveThroughThePage(t, w, memberKestrel.ID)
			page := parseHTML(t, w.b.get(foxholePath))
			before := readNoteState(t, w.st)

			res := submitSelection(t, w.b, page, opClear, tc.members...)

			assertRefused(t, res, refusalNobodyToChange, w.st, before)
		})
	}
}
