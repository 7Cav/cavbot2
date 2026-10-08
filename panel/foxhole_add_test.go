package panel

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// addForm returns the Add members block's paste form, and fails the test
// when the page has none.
func addForm(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	block := findElement(doc, "", "data-field", "add-members")
	if block == nil {
		t.Fatal("the page has no Add members block")
	}
	form := findElement(block, "form", "data-field", "add-form")
	if form == nil {
		t.Fatal("the Add members block has no paste form")
	}
	return form
}

// previewPaste picks the role in a paste form, fills its paste box with
// text and presses its Preview, the way a browser posts the form.
func previewPaste(t *testing.T, b *browser, doc, form *html.Node, role, text string) *http.Response {
	t.Helper()
	fields := formFields(doc, form)
	fields.Set(fieldRole, role)
	fields.Set(fieldLines, text)
	action, _ := attrValue(form, "action")
	return b.postForm(action, fields)
}

// pastePreview loads the Foxhole page, previews the paste of text for the
// role through its Add members block, and returns the add preview it
// opens, failing the test when it opens none.
func pastePreview(t *testing.T, w *testWorld, role, text string) *html.Node {
	t.Helper()
	page := parseHTML(t, w.b.get(foxholePath))
	return addPreviewBlock(t, parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), role, text)))
}

// addPreviewBlock returns the page's add preview, and fails the test when
// the page has none.
func addPreviewBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	preview := findElement(doc, "", "data-field", "add-preview")
	if preview == nil {
		t.Fatal("the page has no add preview")
	}
	return preview
}

// pastedRow returns the add preview's row for the pasted line numbered line,
// and fails the test when the preview has none.
func pastedRow(t *testing.T, preview *html.Node, line int) *html.Node {
	t.Helper()
	row := findElement(preview, "", "data-line", strconv.Itoa(line))
	if row == nil {
		t.Fatalf("the add preview has no row for line %d", line)
	}
	return row
}

// Each pasted line's preview row says what the add does with it and, when
// it matched one member, who and by what. A line matches a Discord ID, a
// mention, or a whole username, server nickname or global name, ignoring
// case, spaces around it and one leading @, and never part of a name.
// Matching reads the member list only, so the preview makes no call to
// Discord's API.
func TestAddPreviewRowGivesEachLinesResultAndWhatItMatched(t *testing.T) {
	cases := []struct {
		name    string
		paste   string
		line    int
		result  string
		member  string
		matched string
		sameAs  string
	}{
		{"Discord ID", memberDoe.ID, 1, "gets", memberDoe.ID, "id", ""},
		{"mention", "<@" + memberAsh.ID + ">", 1, "gets", memberAsh.ID, "id", ""},
		{"nickname mention", "<@!" + memberVance.ID + ">", 1, "gets", memberVance.ID, "id", ""},
		{"username", "rvance", 1, "gets", memberVance.ID, "username", ""},
		{"nickname with spaces, an @ and another case", "  @SGT doe.j ", 1, "gets", memberDoe.ID, "nickname", ""},
		{"global name", "rowan ash", 1, "gets", memberAsh.ID, "global-name", ""},
		{"a holder of the role", "kestrel_tlr", 1, "holding", memberKestrel.ID, "username", ""},
		{"part of a name", "Rowan", 1, "no-match", "", "", ""},
		{"an ID no member has", "123456789012345678", 1, "no-such-id", "", "", ""},
		{"line 1's member again", "jdoe\n" + memberDoe.ID, 2, "same-member", memberDoe.ID, "id", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			calls := w.discord.apiCallCount()

			row := pastedRow(t, pastePreview(t, w, "external", tc.paste), tc.line)

			if got := attrOf(row, "data-result"); got != tc.result {
				t.Errorf("line %d's result is %q, want %q", tc.line, got, tc.result)
			}
			if got := attrOf(row, "data-member"); got != tc.member {
				t.Errorf("line %d matched member %q, want %q", tc.line, got, tc.member)
			}
			if got := attrOf(row, "data-matched"); got != tc.matched {
				t.Errorf("line %d matched by %q, want %q", tc.line, got, tc.matched)
			}
			if got := attrOf(row, "data-same-as"); got != tc.sameAs {
				t.Errorf("line %d names the same member as line %q, want %q", tc.line, got, tc.sameAs)
			}
			if n := w.discord.apiCallCount() - calls; n != 0 {
				t.Errorf("the preview made %d calls to Discord's API, want 0", n)
			}
		})
	}
}

// addRows returns the add preview's rows, in the order the page lists them.
func addRows(preview *html.Node) []*html.Node {
	var rows []*html.Node
	eachElement(preview, func(n *html.Node) {
		if _, ok := attrValue(n, "data-line"); ok {
			rows = append(rows, n)
		}
	})
	return rows
}

// rolesShown returns the Foxhole roles an element marks as held, sorted.
func rolesShown(n *html.Node) []string {
	return sorted(valuesOf(n, "data-role"))
}

// A row that matched a member shows their display name, @username, rank
// role, the Foxhole roles they hold and their note in full, read-only. Rows
// keep the order the lines were pasted in, and a line above the table
// counts the members in the preview with a note, each once however many
// lines name them.
func TestAddPreviewRowsShowEachMemberInPasteOrderWithTheirNote(t *testing.T) {
	w := newFoxholeWorld(t)
	longNote := strings.Repeat("discharged 12 Sep, ask S1 before re-adding; ", 8)
	seedNote(t, w, namesOf(memberDoe), longNote)
	seedNote(t, w, namesOf(memberMarsh), "keeps External through the next war")
	paste := strings.Join([]string{"Ellis Marsh", "<@" + memberKestrel.ID + ">", "sgt doe.j", "rowan_ash", "jdoe"}, "\n")

	preview := pastePreview(t, w, "external", paste)

	var order []string
	for _, row := range addRows(preview) {
		order = append(order, attrOf(row, "data-line")+" "+attrOf(row, "data-member"))
	}
	want := []string{"1 " + memberMarsh.ID, "2 " + memberKestrel.ID, "3 " + memberDoe.ID, "4 " + memberAsh.ID, "5 " + memberDoe.ID}
	if !slices.Equal(order, want) {
		t.Errorf("the preview lists the lines and members %v, want %v", order, want)
	}
	marsh := pastedRow(t, preview, 1)
	for field, want := range map[string]string{"display_name": "CPT Marsh.E", "username": "emarsh", "rank_role": "Captain",
		"note": "keeps External through the next war"} {
		if got := fieldText(t, marsh, field); got != want {
			t.Errorf("Marsh's row shows %s %q, want %q", field, got, want)
		}
	}
	if got, want := rolesShown(marsh), []string{"external", "internal"}; !slices.Equal(got, want) {
		t.Errorf("Marsh's row shows the roles %v, want %v", got, want)
	}
	if got := fieldText(t, pastedRow(t, preview, 3), "note"); got != strings.TrimSpace(longNote) {
		t.Errorf("Doe's row shows the note %q, want it in full, %q", got, longNote)
	}
	if got := fieldText(t, preview, "note-count"); got != "2" {
		t.Errorf("the preview counts %s members with a note, want 2", got)
	}
}

// withMembers puts more members in the test world's member list, beside the
// fixture's.
func withMembers(t *testing.T, w *testWorld, extra ...commands.ListedMember) {
	t.Helper()
	members := append([]commands.ListedMember{memberDoe, memberAsh, memberKestrel, memberMarsh, memberVance}, extra...)
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true, Members: members})
}

// ghosts are n members, each with "ghost" for a name: the first as their
// username, holding External, the rest as their server nickname, in other
// cases.
func ghosts(n int) []commands.ListedMember {
	var members []commands.ListedMember
	for i := range n {
		m := commands.ListedMember{ID: fmt.Sprintf("2000000000000000%02d", i+1), Username: fmt.Sprintf("member_%d", i+1)}
		if i == 0 {
			m.Username, m.RoleIDs = "ghost", []string{roleExternal}
		} else {
			m.Nick = []string{"GHOST", "Ghost", "gHoSt"}[i%3]
		}
		members = append(members, m)
	}
	return members
}

// idsOf returns the members' IDs, in order.
func idsOf(members []commands.ListedMember) []string {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	return ids
}

// choicesOf returns a pick row's choices keyed by member ID.
func choicesOf(row *html.Node) map[string]*html.Node {
	choices := map[string]*html.Node{}
	eachElement(row, func(n *html.Node) {
		if id, ok := attrValue(n, "data-choice"); ok {
			choices[id] = n
		}
	})
	return choices
}

// A line matching two to five members is a pick row: a radio-button choice
// for each, none chosen, each saying what the line matched them by and
// what picking them does. A line matching more than five lists no choices
// and says how many it matched.
func TestAddPreviewLineMatchingSeveralMembersOffersThemUpToFive(t *testing.T) {
	cases := []struct {
		matches int
		result  string
		choices int
	}{
		{2, "pick", 2},
		{5, "pick", 5},
		{6, "too-many", 0},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.matches), func(t *testing.T) {
			w := newFoxholeWorld(t)
			named := ghosts(tc.matches)
			withMembers(t, w, named...)

			row := pastedRow(t, pastePreview(t, w, "external", "jdoe\n@Ghost"), 2)

			if got := attrOf(row, "data-result"); got != tc.result {
				t.Errorf("the line's result is %q, want %q", got, tc.result)
			}
			choices := choicesOf(row)
			if len(choices) != tc.choices {
				t.Fatalf("the row offers %d choices, want %d", len(choices), tc.choices)
			}
			if tc.choices == 0 {
				if got := attrOf(row, "data-matches"); got != strconv.Itoa(tc.matches) {
					t.Errorf("the row says the line matched %s members, want %d", got, tc.matches)
				}
				return
			}
			for i, id := range idsOf(named) {
				choice, ok := choices[id]
				if !ok {
					t.Errorf("the row offers no choice of %s", id)
					continue
				}
				radio := findElement(choice, "input", "type", "radio")
				if radio == nil || attrOf(radio, "value") != id {
					t.Errorf("%s's choice has no radio button for them", id)
				} else if _, checked := attrValue(radio, "checked"); checked {
					t.Errorf("%s's choice is chosen, want none chosen", id)
				}
				matched, result := "nickname", "gets"
				if i == 0 {
					matched, result = "username", "holding"
				}
				if got := attrOf(choice, "data-matched"); got != matched {
					t.Errorf("%s's choice matched by %q, want %q", id, got, matched)
				}
				if got := attrOf(choice, "data-result"); got != result {
					t.Errorf("%s's choice's result is %q, want %q", id, got, result)
				}
			}
		})
	}
}

// confirmAdd picks, in each pick row of the page's add preview, the choice
// of the member given for its line, and presses Confirm, the way a browser
// posts the add's form. It fails the test when the preview offers no
// Confirm to press.
func confirmAdd(t *testing.T, b *browser, doc *html.Node, picks map[int]string) *http.Response {
	t.Helper()
	preview := addPreviewBlock(t, doc)
	if button := findElement(preview, "button", "data-field", "confirm"); button == nil || disabled(button) {
		t.Fatal("the add preview offers no Confirm to press")
	}
	for line, id := range picks {
		choice := choicesOf(pastedRow(t, preview, line))[id]
		if choice == nil {
			t.Fatalf("line %d offers no choice of %s", line, id)
		}
		radio := findElement(choice, "input", "type", "radio")
		radio.Attr = append(radio.Attr, html.Attribute{Key: "checked"})
	}
	return b.postForm(foxholeAddPath, formPosts(t, doc, foxholeAddPath))
}

// Two twins for the add's pick rows, each "twin" by a different kind of
// name.
var (
	twinA = commands.ListedMember{ID: "300000000000000001", Username: "twin_a", Nick: "Twin"}
	twinB = commands.ListedMember{ID: "300000000000000002", Username: "twin_b", GlobalName: "twin"}
)

// crowd is six members nicknamed "crowd", one more than a pick row offers.
func crowd() []commands.ListedMember {
	var members []commands.ListedMember
	for i := range 6 {
		members = append(members, commands.ListedMember{ID: fmt.Sprintf("40000000000000000%d", i+1),
			Username: fmt.Sprintf("crowd_%d", i+1), Nick: "crowd"})
	}
	return members
}

// Confirming an add preview gives the role to each member a row says gets
// it, and to the choice picked in a pick row, each once, even when the pick
// lands on a member another line matched. A member who already holds it is
// skipped with that reason. A line matching more than five, and a pick row
// with nothing picked, add nobody, and nobody else's roles change.
func TestConfirmedAddGivesTheRoleToEachMatchedAndPickedMemberOnce(t *testing.T) {
	w := newFoxholeWorld(t)
	ghost := ghosts(2)
	withMembers(t, w, append(append(ghost, twinA, twinB), crowd()...)...)
	paste := strings.Join([]string{"rvance", "kestrel_tlr", "ghost", ghost[1].Username, "twin", "crowd", "Twin"}, "\n")
	page := parseHTML(t, w.b.get(foxholePath))
	doc := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", paste))

	assertRedirect(t, confirmAdd(t, w.b, doc, map[int]string{3: ghost[1].ID, 5: twinB.ID}), foxholePath)
	w.awaitActionEnd(t)

	want := sorted([]string{memberKestrel.ID, memberMarsh.ID, ghost[0].ID, memberVance.ID, ghost[1].ID, twinB.ID})
	if got := w.discord.holdersOf(roleExternal); !slices.Equal(got, want) {
		t.Errorf("after the add %v hold External, want %v", got, want)
	}
	changes := map[string]int{}
	for _, write := range w.discord.roleChanges() {
		changes[write.MemberID+" "+write.RoleID]++
	}
	wantChanges := map[string]int{memberVance.ID + " " + roleExternal: 1, ghost[1].ID + " " + roleExternal: 1, twinB.ID + " " + roleExternal: 1}
	if !maps.Equal(changes, wantChanges) {
		t.Errorf("the add made the role changes %v, want %v", changes, wantChanges)
	}
	skipped := listedReasons(reportList(t, reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "skipped"))
	if want := map[string]string{memberKestrel.ID: "holding"}; !maps.Equal(skipped, want) {
		t.Errorf("the report skipped %v, want %v", skipped, want)
	}
}

// addedNobodyLines returns the lines a report lists as having added nobody, in
// the order it lists them, each as its text, its reason code and the
// marker that reason carries: the line a same-member line repeats, or how
// many members a line matched.
func addedNobodyLines(report *html.Node) []string {
	var lines []string
	if list := findElement(report, "", "data-list", "added-nobody"); list != nil {
		eachElement(list, func(n *html.Node) {
			if reason, ok := attrValue(n, "data-reason"); ok {
				text := findElement(n, "", "data-field", "text")
				lines = append(lines, strings.Join([]string{textOf(text), reason, attrOf(n, "data-same-as") + attrOf(n, "data-matches")}, " "))
			}
		})
	}
	return lines
}

// An add's report lists, beside its four member lists, each pasted line
// that added nobody, in the order pasted, with its text and why: no member
// with the ID, the same member as an earlier line, matched several members
// and none picked, or no member matches. The report is the add's one entry
// in the change log.
func TestAddReportListsEachLineThatAddedNobodyWithItsReason(t *testing.T) {
	w := newFoxholeWorld(t)
	withMembers(t, w, ghosts(2)...)
	paste := strings.Join([]string{"SGT Doe.J", "999999999999999999", "jdoe", "ghost", "nobody_here"}, "\n")
	page := parseHTML(t, w.b.get(foxholePath))
	doc := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", paste))

	assertRedirect(t, confirmAdd(t, w.b, doc, nil), foxholePath)
	w.awaitActionEnd(t)

	page = parseHTML(t, w.b.get(foxholePath))
	want := []string{"999999999999999999 no-such-id ", "jdoe same-member 1", "ghost none-picked 2", "nobody_here no-match "}
	if got := addedNobodyLines(reportBlock(t, page)); !slices.Equal(got, want) {
		t.Errorf("the report lists the lines that added nobody as %q, want %q", got, want)
	}
	if entries := changeEntries(t, page, "add"); len(entries) != 1 {
		t.Errorf("the change log holds %d add entries, want 1", len(entries))
	}
}

// Every role change an add makes carries the audit log reason the spec
// fixes, naming the add and the forum user who started it, so a Discord
// moderator knows who did it without opening the panel.
func TestAddRoleChangesNameTheAddAndTheForumUserWhoStartedIt(t *testing.T) {
	w := newFoxholeWorld(t)
	page := parseHTML(t, w.b.get(foxholePath))
	doc := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "jdoe\nrowan_ash"))

	assertRedirect(t, confirmAdd(t, w.b, doc, nil), foxholePath)
	w.awaitActionEnd(t)

	writes := w.discord.roleChanges()
	if len(writes) == 0 {
		t.Fatal("the add changed no role")
	}
	want := "Panel: Foxhole add by " + managerUsername + " (forum user " + strconv.Itoa(managerUserID) + ")"
	for _, write := range writes {
		if write.Reason != want {
			t.Errorf("the change to %s carries the reason %q, want %q", write.MemberID, write.Reason, want)
		}
	}
}

// While the member list is partial the page can't match a line, so a
// Preview or a Confirm pressed then is refused: the page says nothing
// changed, opens no add preview, and shows the Add members block with the
// pasted text and its Preview, to send again once the list is back.
func TestAddPressedWhileTheMemberListIsPartialIsRefusedKeepingThePaste(t *testing.T) {
	const paste = "jdoe\nrowan_ash"
	cases := []struct {
		name  string
		press func(t *testing.T, w *testWorld) *http.Response
	}{
		{"preview", func(t *testing.T, w *testWorld) *http.Response {
			page := parseHTML(t, w.b.get(foxholePath))
			w.discord.setMemberList(arrivingList)
			return previewPaste(t, w.b, page, addForm(t, page), "external", paste)
		}},
		{"confirm", func(t *testing.T, w *testWorld) *http.Response {
			page := parseHTML(t, w.b.get(foxholePath))
			doc := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", paste))
			w.discord.setMemberList(arrivingList)
			return confirmAdd(t, w.b, doc, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			w.p.pageBudget = partialListBudget

			doc := parseHTML(t, tc.press(t, w))

			if findLive(doc, "", "data-error", "member-list") == nil {
				t.Error("the page doesn't say the add was refused for the member list")
			}
			if findElement(doc, "", "data-field", "add-preview") != nil {
				t.Error("the page opens an add preview from a partial member list")
			}
			form := addForm(t, doc)
			if got := formFields(doc, form).Get(fieldLines); got != paste {
				t.Errorf("the paste box holds %q, want the pasted text %q", got, paste)
			}
			if findElement(form, "button", "data-field", "preview") == nil {
				t.Error("the paste box has no Preview")
			}
		})
	}
}

// A regression pin, green from the paste box's first build: the paste box
// has no line cap, so a whole allied regiment goes in one action. A paste
// of more lines than /foxhole bulkadd's 50-entry cap previews a row for
// every line.
func TestAddPreviewHasNoLineCap(t *testing.T) {
	w := newFoxholeWorld(t)
	var lines []string
	for i := range 51 {
		lines = append(lines, fmt.Sprintf("ally_%d", i+1))
	}

	rows := addRows(pastePreview(t, w, "external", strings.Join(lines, "\n")))

	if len(rows) != len(lines) {
		t.Errorf("the preview has %d rows, want one for each of the %d lines", len(rows), len(lines))
	}
}

// One Foxhole action runs at a time. While a purge runs, an add preview's
// Confirm is disabled, while its Preview again stays open, so a manager can
// ready a paste during a long purge and confirm it once the purge ends.
func TestAddConfirmIsDisabledWhileAnActionRunsAndPreviewStaysOpen(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	hold.next(t)

	preview := pastePreview(t, w, "external", "jdoe")

	if button := findElement(preview, "button", "data-field", "confirm"); button == nil || !disabled(button) {
		t.Error("the add preview's Confirm is enabled while a purge runs")
	}
	if button := findElement(preview, "button", "data-field", "preview"); button == nil || disabled(button) {
		t.Error("the add preview's Preview again is disabled while a purge runs")
	}
}

// An add that would give the role to nobody can't be confirmed: a preview
// whose every line matches nobody, names a member who already holds the
// role, or matches more than five members offers no Confirm.
func TestAddPreviewWithNobodyToAddCantBeConfirmed(t *testing.T) {
	w := newFoxholeWorld(t)
	withMembers(t, w, crowd()...)

	preview := pastePreview(t, w, "external", "nobody_here\nkestrel_tlr\ncrowd")

	if button := findElement(preview, "button", "data-field", "confirm"); button != nil && !disabled(button) {
		t.Error("the preview of an add that gives the role to nobody offers a Confirm")
	}
}

// Confirm adds only the members the preview showed. When a line names
// someone else by the time Confirm is pressed, as when a username changes
// hands, nothing starts: the page says the members changed and shows the
// add preview as it stands now, to check and confirm again.
func TestAddConfirmedAfterALineNamesSomeoneElseStartsNothing(t *testing.T) {
	w := newFoxholeWorld(t)
	newcomer := commands.ListedMember{ID: "500000000000000001", Username: "rvance_alt"}
	withMembers(t, w, newcomer)
	page := parseHTML(t, w.b.get(foxholePath))
	doc := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "rvance"))
	w.discord.editMember(memberVance.ID, func(m *commands.ListedMember) { m.Username = "rvance_old" })
	w.discord.editMember(newcomer.ID, func(m *commands.ListedMember) { m.Username = "rvance" })

	after := parseHTML(t, confirmAdd(t, w.b, doc, nil))

	if findLive(after, "", "data-error", "changed") == nil {
		t.Error("the page doesn't say the members changed since the preview")
	}
	if got := attrOf(pastedRow(t, addPreviewBlock(t, after), 1), "data-member"); got != newcomer.ID {
		t.Errorf("the add preview's line 1 names %q, want the member it names now, %s", got, newcomer.ID)
	}
	if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "add"); len(entries) != 0 {
		t.Errorf("the change log holds %d add entries, want none", len(entries))
	}
}
