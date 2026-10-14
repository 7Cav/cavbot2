package panel

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// removeButton returns the selection bar's Remove button for the role, and
// fails the test when the bar has none.
func removeButton(t *testing.T, doc *html.Node, role string) *html.Node {
	t.Helper()
	form := findElement(doc, "form", "data-field", "selection")
	if form == nil {
		t.Fatal("the page has no selection bar")
	}
	var button *html.Node
	eachLiveElement(doc, func(n *html.Node) {
		if n.Data == "button" && attrOf(n, "data-remove") == role && ownedBy(n, form) {
			button = n
		}
	})
	if button == nil {
		t.Fatalf("the selection bar has no Remove %s button", role)
	}
	return button
}

// removePreviewBlock returns the page's remove preview, and fails the test when
// the page has none.
func removePreviewBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	preview := findElement(doc, "", "data-field", "remove-preview")
	if preview == nil {
		t.Fatal("the page has no remove preview")
	}
	return preview
}

// previewList returns the members one of the remove preview's lists names,
// each with the reason code it gives, empty for none.
func previewList(preview *html.Node, name string) map[string]string {
	list := findElement(preview, "", "data-list", name)
	if list == nil {
		return map[string]string{}
	}
	return listedReasons(list)
}

// seedNote saves a note on a member in the store, as a note save made
// earlier would have, under the names the member list shows.
func seedNote(t *testing.T, w *testWorld, names store.MemberNames, note string) {
	t.Helper()
	entry := store.ChangeLogEntry{ForumUserID: managerUserID, ForumUsername: managerUsername, Action: store.ChangeNote, Diff: []byte(`{}`)}
	save := store.NoteSave{MemberID: names.MemberID, Note: note, DisplayName: names.DisplayName, Username: names.Username}
	if err := w.st.SaveFoxholeNote(context.Background(), testGuildID, save, entry); err != nil {
		t.Fatalf("seed note: %v", err)
	}
}

// The remove preview of a selection lists who loses the role, who is
// skipped for not holding it, a member who left the server among them, and
// the losers approved for the role, whose approval for it the removal
// clears. It gives the rough time at one change a second, and
// each member's note with a count of them. Opening it changes nothing: no
// Discord call, and no report.
func TestRemovePreviewListsWhoLosesTheRoleWhoIsSkippedAndWhoseApprovalItClears(t *testing.T) {
	cases := []struct {
		role     string
		selected []string
		loses    []string
		skipped  map[string]string
		cleared  []string
	}{
		{"external", []string{memberKestrel.ID, memberMarsh.ID, memberDoe.ID, foxholerGone.MemberID},
			[]string{memberKestrel.ID, memberMarsh.ID},
			map[string]string{memberDoe.ID: "not-holding", foxholerGone.MemberID: "left"},
			[]string{memberMarsh.ID}},
		{"internal", []string{memberDoe.ID, memberMarsh.ID},
			[]string{memberDoe.ID, memberMarsh.ID}, map[string]string{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			w := newFoxholeWorld(t)
			seedApproved(t, w, namesOf(memberMarsh), foxholerGone)
			seedNote(t, w, namesOf(memberMarsh), "keeps External through the next war")
			seedNote(t, w, namesOf(memberDoe), "discharged 12 Sep")
			page := parseHTML(t, w.b.get(foxholePath))
			calls := w.discord.apiCallCount()

			preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, page, tc.role, tc.selected...)))

			if got := attrOf(preview, "data-role"); got != tc.role {
				t.Errorf("the preview removes %q, want %q", got, tc.role)
			}
			if got := slices.Sorted(maps.Keys(previewList(preview, "loses"))); !slices.Equal(got, sorted(tc.loses)) {
				t.Errorf("the preview lists %v as losing %s, want %v", got, tc.role, sorted(tc.loses))
			}
			if got := previewList(preview, "skipped"); !maps.Equal(got, tc.skipped) {
				t.Errorf("the preview skips %v, want %v", got, tc.skipped)
			}
			if got := slices.Sorted(maps.Keys(previewList(preview, "approval-cleared"))); !slices.Equal(got, sorted(tc.cleared)) {
				t.Errorf("the preview clears the approval of %v, want %v", got, sorted(tc.cleared))
			}
			estimate := findElement(preview, "", "data-field", "estimate")
			if estimate == nil {
				t.Fatal("the preview gives no estimate")
			}
			want := time.Duration(len(tc.loses)) * changeEstimate
			if got := attrOf(estimate, "data-seconds"); got != strconv.Itoa(int(want/time.Second)) {
				t.Errorf("the estimate is %s s, want %d s", got, int(want/time.Second))
			}
			notes := map[string]string{}
			eachElement(preview, func(n *html.Node) {
				if id, ok := attrValue(n, "data-member"); ok {
					if note := findElement(n, "", "data-field", "note"); note != nil && textOf(note) != "" {
						notes[id] = textOf(note)
					}
				}
			})
			wantNotes := map[string]string{memberMarsh.ID: "keeps External through the next war", memberDoe.ID: "discharged 12 Sep"}
			if !maps.Equal(notes, wantNotes) {
				t.Errorf("the preview shows the notes %v, want %v", notes, wantNotes)
			}
			if got := fieldText(t, preview, "note-count"); got != strconv.Itoa(len(wantNotes)) {
				t.Errorf("the preview counts %s members with a note, want %d", got, len(wantNotes))
			}
			if n := w.discord.apiCallCount() - calls; n != 0 {
				t.Errorf("opening the preview made %d calls to Discord's API, want 0", n)
			}
			if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "removal"); len(entries) != 0 {
				t.Errorf("opening the preview left %d removal entries in the change log, want none", len(entries))
			}
		})
	}
}

// sorted is a sorted copy of the IDs.
func sorted(ids []string) []string {
	return slices.Sorted(slices.Values(ids))
}

// confirmRemoval posts the remove preview's form, the way a browser does,
// and fails the test when the preview has none.
func confirmRemoval(t *testing.T, b *browser, preview *html.Node) *http.Response {
	t.Helper()
	form := findElement(preview, "form", "", "")
	if form == nil {
		t.Fatal("the remove preview has no form")
	}
	return submit(t, b, form)
}

// Confirming a remove preview takes the role off each member it lists as
// losing it, as a Foxhole action. An approved Foxholer the removal takes
// External from loses the External approval, and the report marks them;
// one who isn't approved has nothing to clear. The report is the removal's one
// entry in the change log.
func TestConfirmedRemovalTakesExternalAndClearsTheExternalApproval(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberMarsh))
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID, memberMarsh.ID)))

	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)

	if holders := w.discord.holdersOf(roleExternal); len(holders) != 0 {
		t.Errorf("after the removal %v hold External, want nobody", holders)
	}
	page := parseHTML(t, w.b.get(foxholePath))
	if got := approvedRows(t, page); len(got) != 0 {
		t.Errorf("after the removal the rows %v carry the approved mark, want none", got)
	}
	report := reportBlock(t, page)
	changed := reportList(t, report, "changed")
	if got, want := pairs(changed), sorted([]string{memberKestrel.ID + " external", memberMarsh.ID + " external"}); !slices.Equal(got, want) {
		t.Errorf("the report changed %v, want %v", got, want)
	}
	if got, want := approvalClearedIn(changed), []string{memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the report marks %v as losing their approval, want %v", got, want)
	}
	entries := changeEntries(t, page, "removal")
	if len(entries) != 1 {
		t.Fatalf("the change log holds %d removal entries, want 1", len(entries))
	}
	if got, want := attrOf(entries[0], "data-entry"), attrOf(report, "data-report"); got != want {
		t.Errorf("the removal's change log entry is %s, want the report, %s", got, want)
	}
}

// approvalClearedIn returns the members a report list marks as losing
// their approval, in the order it lists them.
func approvalClearedIn(list *html.Node) []string {
	var out []string
	eachElement(list, func(n *html.Node) {
		if _, ok := attrValue(n, "data-approval-cleared"); ok {
			out = append(out, attrOf(n, "data-member"))
		}
	})
	return out
}

// assertLostExternalKeptApproval fails the test unless the member no longer
// holds External yet still carries the approved mark on their row, and the
// report lists them as changed without marking them as losing their
// approval.
func assertLostExternalKeptApproval(t *testing.T, w *testWorld, memberID string) {
	t.Helper()
	if holders := w.discord.holdersOf(roleExternal); slices.Contains(holders, memberID) {
		t.Errorf("after the removal %v hold External, want %s among them no more", holders, memberID)
	}
	page := parseHTML(t, w.b.get(foxholePath))
	if got := approvedRows(t, page); !slices.Contains(got, memberID) {
		t.Errorf("after the removal the rows %v carry the approved mark, want %s among them", got, memberID)
	}
	item := findElement(reportList(t, reportBlock(t, page), "changed"), "", "data-member", memberID)
	if item == nil {
		t.Fatalf("the report doesn't list %s as changed", memberID)
	}
	if _, ok := attrValue(item, "data-approval-cleared"); ok {
		t.Errorf("the report marks %s as losing their approval, which the preview didn't name", memberID)
	}
}

// A removal clears only the approvals its preview named. A manager approves
// a selected member while the removal runs, before it reaches them: the
// removal takes their External and leaves the approval, so the next re-add
// gives External back.
func TestRemovalKeepsAnApprovalGivenWhileItRuns(t *testing.T) {
	w := newFoxholeWorld(t)
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID, memberMarsh.ID)))
	hold := holdRoleWrites(t, w)
	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	unreached := memberKestrel.ID
	if hold.next(t).MemberID == memberKestrel.ID {
		unreached = memberMarsh.ID
	}

	approveThroughThePage(t, w, unreached)
	hold.open()
	w.awaitActionEnd(t)

	assertLostExternalKeptApproval(t, w, unreached)
}

// A removal clears only the approvals its preview named. A manager approves
// Kestrel after the remove preview opened, showing no approval to clear, and
// before its Confirm: the removal takes Kestrel's External and leaves the
// approval.
func TestRemovalKeepsAnApprovalGivenAfterItsPreviewOpened(t *testing.T) {
	w := newFoxholeWorld(t)
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))
	if findElement(preview, "", "data-list", "approval-cleared") != nil {
		t.Fatal("the preview names an approval to clear before anyone approved Kestrel")
	}
	approveThroughThePage(t, w, memberKestrel.ID)

	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)

	assertLostExternalKeptApproval(t, w, memberKestrel.ID)
}

// Every role change a removal makes carries the audit log reason the spec
// fixes, naming the removal and the forum user who started it, so a Discord
// moderator knows who did it without opening the panel.
func TestRemovalRoleChangesNameTheRemovalAndTheForumUserWhoStartedIt(t *testing.T) {
	w := newFoxholeWorld(t)
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "internal", memberDoe.ID, memberAsh.ID)))

	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)

	writes := w.discord.roleChanges()
	if len(writes) == 0 {
		t.Fatal("the removal changed no role")
	}
	want := "Panel: Foxhole removal by " + managerUsername + " (forum user " + strconv.Itoa(managerUserID) + ")"
	for _, write := range writes {
		if write.Reason != want {
			t.Errorf("the change to %s carries the reason %q, want %q", write.MemberID, write.Reason, want)
		}
	}
}

// While the member list is partial the page can't tell who holds a role,
// so a Remove pressed then is refused: the page says so, which says nothing
// changed, opens no preview and makes no Discord call. The selection isn't
// kept, and the manager selects again once the rows are back.
func TestRemovePressedWhileTheMemberListIsPartialIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	page := parseHTML(t, w.b.get(foxholePath))
	w.discord.setMemberList(arrivingList)
	calls := w.discord.apiCallCount()

	doc := parseHTML(t, submitSelection(t, w.b, page, "external", memberKestrel.ID))

	if findLive(doc, "", "data-error", "member-list") == nil {
		t.Error("the page doesn't say the removal was refused for the member list")
	}
	if findElement(doc, "", "data-field", "remove-preview") != nil {
		t.Error("the page opens a remove preview from a partial member list")
	}
	if n := w.discord.apiCallCount() - calls; n != 0 {
		t.Errorf("the refused removal made %d calls to Discord's API, want 0", n)
	}
}

// One Foxhole action runs at a time. While a purge runs, the selection
// bar says removing is off in place of its Remove buttons, and the Confirm
// of a remove preview opened anyway, from a page loaded before the purge
// started, is off.
func TestRemoveControlsAreOffWhileAnActionRuns(t *testing.T) {
	w := newFoxholeWorld(t)
	before := parseHTML(t, w.b.get(foxholePath))
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	hold.next(t)

	page := parseHTML(t, w.b.get(foxholePath))

	bar := findElement(page, "form", "data-field", "selection")
	if bar == nil {
		t.Fatal("the page has no selection bar")
	}
	eachLiveElement(page, func(n *html.Node) {
		// The way submitSelection presses a Remove button: by the role its
		// value names.
		if value := attrOf(n, "value"); n.Data == "button" && ownedBy(n, bar) && (value == "internal" || value == "external") {
			t.Errorf("the selection bar offers Remove %s while a purge runs, want the reason in its place", value)
		}
	})
	if findElement(bar, "", "data-field", "busy") == nil {
		t.Error("the selection bar doesn't say why it offers no Remove while a purge runs")
	}
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, before, "external", memberKestrel.ID)))
	if button := findElement(preview, "", "data-field", "confirm"); button == nil || !disabled(button) {
		t.Error("the remove preview's Confirm is enabled while a purge runs")
	}
}

// enabledConfirm reports whether a remove preview offers a Confirm a
// manager can press.
func enabledConfirm(preview *html.Node) bool {
	button := findElement(preview, "", "data-field", "confirm")
	return button != nil && !disabled(button)
}

// A removal that would take the role from nobody can't be confirmed: a
// preview in which no member selected holds the role, as when Doe alone is
// selected for External, offers no Confirm, where one with a member losing
// the role does.
func TestRemovePreviewWithNobodyLosingTheRoleCantBeConfirmed(t *testing.T) {
	w := newFoxholeWorld(t)
	page := parseHTML(t, w.b.get(foxholePath))

	nobody := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, page, "external", memberDoe.ID)))
	somebody := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))

	if enabledConfirm(nobody) {
		t.Error("the preview of removing External from Doe alone, who doesn't hold it, offers a Confirm")
	}
	if !enabledConfirm(somebody) {
		t.Error("the preview of removing External from Kestrel, who holds it, offers no Confirm")
	}
}

// A removal of Internal clears the Internal approval of each member its
// preview named, as a removal of External clears the External one, and
// leaves the member's other approval. Marsh, approved for both roles, loses
// Internal: the preview names his approval as one it clears, his row keeps
// the External approval alone, and the report marks him as losing the
// Internal one.
func TestRemovalOfInternalClearsTheInternalApprovalAndKeepsTheExternalOne(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApprovals(t, w, store.MemberApprovals{MemberNames: namesOf(memberMarsh), Approved: bothApprovals})
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "internal", memberMarsh.ID)))
	if got, want := slices.Sorted(maps.Keys(previewList(preview, "approval-cleared"))), []string{memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the preview clears the approval of %v, want %v", got, want)
	}

	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)

	if holders := w.discord.holdersOf(roleInternal); slices.Contains(holders, memberMarsh.ID) {
		t.Errorf("after the removal %v hold Internal, want %s among them no more", holders, memberMarsh.ID)
	}
	page := parseHTML(t, w.b.get(foxholePath))
	if got, want := approvedRoles(t, page)[memberMarsh.ID], []string{"external"}; !slices.Equal(got, want) {
		t.Errorf("after the removal %s's approvals are %v, want %v", memberMarsh.ID, got, want)
	}
	if got, want := approvalClearedIn(reportList(t, reportBlock(t, page), "changed")), []string{memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the report marks %v as losing their approval, want %v", got, want)
	}
}

// vanceWithInternal is Vance once he holds Internal.
func vanceWithInternal() commands.ListedMember {
	vance := memberVance
	vance.RoleIDs = []string{roleSGT, roleInternal}
	return vance
}

// removeUnderASearchAndAFilter opens the holder list under the search "r"
// and the Internal filter, which together list Ash, Marsh and Vance alone,
// ticks the members given, and presses the selection bar's Remove for the
// role, the way a browser with no script does. It returns the page Remove
// opens.
func removeUnderASearchAndAFilter(role string, memberIDs ...string) func(*testing.T, *testWorld) *html.Node {
	return func(t *testing.T, w *testWorld) *html.Node {
		t.Helper()
		internal := attrOf(filterAnchor(t, parseHTML(t, w.b.get(foxholePath)), "internal"), "href")
		listed := parseHTML(t, submitSearch(t, w.b, parseHTML(t, w.b.get(internal)), "r"))
		return parseHTML(t, submitSelection(t, w.b, listed, role, memberIDs...))
	}
}

// landsOn checks a page opened at fragment lands on the heading of the
// block whose data-field is field, or at the page's top when field is
// empty.
func landsOn(t *testing.T, doc *html.Node, fragment, field string) {
	t.Helper()
	if field == "" {
		if fragment != "" {
			t.Errorf("the page opens at fragment %q, want its top", fragment)
		}
		return
	}
	heading := landedOn(doc, fragment)
	if heading == nil || !isHeading(heading) || !within(heading, foxholeBlock(t, doc, field)) {
		t.Errorf("the page opens at fragment %q, want the heading of the %s block", fragment, field)
	}
}

// tickedRows returns the members whose holder list row is ticked, sorted.
func tickedRows(t *testing.T, doc *html.Node) []string {
	t.Helper()
	var out []string
	for id, row := range holderRows(t, doc) {
		if _, ticked := attrValue(findElement(row, "input", "type", "checkbox"), "checked"); ticked {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Cancel on a remove preview returns to where Remove was pressed. From the
// selection bar it lands on the holder list's heading, under the search and
// the filter the selection was made under, whether or not anyone selected
// holds the role. A remove address with the no-role filter lands on the
// no-role view's heading. A Retry of a removal has no selection to return
// to, and its Cancel opens the page at its top with no box ticked. The
// preview page's own holder list shows the selection ticked, and so does
// the page Cancel opens, a member the preview skips included.
func TestRemovePreviewCancelReturnsToWhereRemoveWasPressed(t *testing.T) {
	ashAndMarsh := sorted([]string{memberAsh.ID, memberMarsh.ID})
	cases := []struct {
		name string
		// open opens a remove preview as a browser with no script does, and
		// returns the page it opens.
		open func(*testing.T, *testWorld) *html.Node
		// block is the data-field of the list block Cancel lands on, empty
		// for the page's top.
		block string
		// selection is the members ticked when Remove was pressed, nil for
		// a preview the selection bar didn't open.
		selection []string
	}{
		{"Remove External, which skips a member", removeUnderASearchAndAFilter("external", ashAndMarsh...), "holders", ashAndMarsh},
		{"Remove Internal", removeUnderASearchAndAFilter("internal", ashAndMarsh...), "holders", ashAndMarsh},
		{"a preview where nobody selected holds the role", removeUnderASearchAndAFilter("external", memberAsh.ID),
			"holders", []string{memberAsh.ID}},
		{"a remove address typed with the no-role filter", func(t *testing.T, w *testWorld) *html.Node {
			typed := url.Values{paramFilter: {filterNoRole}, paramRemove: {"external"}, fieldMember: {memberKestrel.ID}}
			return parseHTML(t, w.b.get(foxholePath+"?"+typed.Encode()+"#"+removePreviewAnchor))
		}, "no-role", nil},
		{"the Retry of a removal", func(t *testing.T, w *testWorld) *html.Node {
			w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
			preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))
			assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
			w.awaitActionEnd(t)
			return openRetry(t, w.b)
		}, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			setMembers(w, memberDoe, memberAsh, memberKestrel, memberMarsh, vanceWithInternal())
			page := tc.open(t, w)
			if tc.selection != nil {
				if got := tickedRows(t, page); !slices.Equal(got, tc.selection) {
					t.Errorf("the preview page's holder list ticks %v, want the selection %v", got, tc.selection)
				}
			}
			cancel := cancelAddress(t, removePreviewBlock(t, page))

			doc, fragment := landing(t, w.b, cancel, w.b.get(cancel))

			landsOn(t, doc, fragment, tc.block)
			// The no-role view has no boxes to tick.
			if tc.block != "no-role" {
				if got := tickedRows(t, doc); !slices.Equal(got, tc.selection) {
					t.Errorf("the page Cancel opens ticks %v, want %v", got, tc.selection)
				}
			}
			if tc.selection != nil {
				want := sorted([]string{memberAsh.ID, memberMarsh.ID, memberVance.ID})
				if got := keys(holderRows(t, doc)); !slices.Equal(got, want) {
					t.Errorf("the page Cancel opens lists %v, want %v under the search and the filter", got, want)
				}
			}
		})
	}
}

// A member the selection names who has no holder list row by the time
// Cancel is followed, such as one who lost their Foxhole roles meanwhile,
// is left out: the page still opens, with the rest of the selection ticked.
func TestRemovePreviewCancelTicksTheSelectionStillListed(t *testing.T) {
	w := newFoxholeWorld(t)
	setMembers(w, memberDoe, memberAsh, memberKestrel, memberMarsh, vanceWithInternal())
	page := removeUnderASearchAndAFilter("internal", memberAsh.ID, memberMarsh.ID)(t, w)
	cancel := cancelAddress(t, removePreviewBlock(t, page))
	marsh := memberMarsh
	marsh.RoleIDs = []string{roleCPT}
	setMembers(w, memberDoe, memberAsh, memberKestrel, marsh, vanceWithInternal())

	doc, _ := landing(t, w.b, cancel, w.b.get(cancel))

	if got, want := tickedRows(t, doc), []string{memberAsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the page Cancel opens ticks %v, want %v", got, want)
	}
}
