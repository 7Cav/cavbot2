package panel

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// foxholerGone is an approved Foxholer who left the server: the member
// list doesn't hold them, and the store holds the names the panel last
// saw.
var foxholerGone = store.MemberNames{MemberID: "100000000000000009", DisplayName: "LT Wren.A", Username: "awren"}

// namesOf is a member's names as the member list shows them.
func namesOf(m commands.ListedMember) store.MemberNames {
	return store.MemberNames{MemberID: m.ID, DisplayName: m.DisplayName(), Username: m.Username}
}

// seedApproved gives the members given an External approval in the store,
// as an Approve saved earlier would have, whatever roles they hold now.
func seedApproved(t *testing.T, w *testWorld, members ...store.MemberNames) {
	t.Helper()
	approvals := make([]store.MemberApprovals, 0, len(members))
	for _, m := range members {
		approvals = append(approvals, store.MemberApprovals{MemberNames: m, Approved: store.FoxholeApprovals{External: true}})
	}
	seedApprovals(t, w, approvals...)
}

// seedApprovals gives each member given the approvals given with them in
// the store, as an Approve saved earlier would have, whatever roles they
// hold now.
func seedApprovals(t *testing.T, w *testWorld, approvals ...store.MemberApprovals) {
	t.Helper()
	entry := store.ChangeLogEntry{ForumUserID: managerUserID, ForumUsername: managerUsername, Action: store.ChangeApprove, Diff: json.RawMessage(`{}`)}
	if err := w.st.ApproveFoxholeMembers(context.Background(), testGuildID, approvals, entry); err != nil {
		t.Fatalf("seed approvals: %v", err)
	}
}

// reAddBlock returns the After a war block's re-add, and fails the test
// when the page has none.
func reAddBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	block := findElement(doc, "", "data-field", "after-a-war")
	if block == nil {
		t.Fatal("the page has no After a war block")
	}
	reAdd := findElement(block, "", "data-field", "re-add")
	if reAdd == nil {
		t.Fatal("the After a war block has no re-add")
	}
	return reAdd
}

// reAddCountLine returns the re-add's count line for the role given, and
// fails the test when it has none.
func reAddCountLine(t *testing.T, reAdd *html.Node, role string) *html.Node {
	t.Helper()
	line := findElement(reAdd, "", "data-readd-role", role)
	if line == nil {
		t.Fatalf("the re-add has no %s line", role)
	}
	return line
}

// The After a war block counts each role's approvals on a line of its own,
// by what a re-add would do with each: the approved Foxholers who hold the
// role already, those in the server who don't, and those who left the
// server. A member approved for both roles counts on both lines, as Marsh,
// Vance and the member who left do. A manager knows what Re-add will do
// before pressing it.
func TestAfterAWarBlockCountsEachRolesApprovalsByWhatReAddWouldDo(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApprovals(t, w,
		store.MemberApprovals{MemberNames: namesOf(memberDoe), Approved: internalApproval},
		store.MemberApprovals{MemberNames: namesOf(memberAsh), Approved: externalApproval},
		store.MemberApprovals{MemberNames: namesOf(memberKestrel), Approved: externalApproval},
		store.MemberApprovals{MemberNames: namesOf(memberMarsh), Approved: bothApprovals},
		store.MemberApprovals{MemberNames: namesOf(memberVance), Approved: bothApprovals},
		store.MemberApprovals{MemberNames: foxholerGone, Approved: bothApprovals})

	block := reAddBlock(t, parseHTML(t, w.b.get(foxholePath)))

	fields := []string{"approved", "holding", "not-holding", "not-in-server"}
	want := map[string]map[string]string{
		"internal": {"approved": "4", "holding": "2", "not-holding": "1", "not-in-server": "1"},
		"external": {"approved": "5", "holding": "2", "not-holding": "2", "not-in-server": "1"},
	}
	for role, counts := range want {
		line := reAddCountLine(t, block, role)
		got := map[string]string{}
		for _, field := range fields {
			got[field] = fieldText(t, line, field)
		}
		if !maps.Equal(got, counts) {
			t.Errorf("the re-add's %s line counts %v, want %v", role, got, counts)
		}
	}
}

// pressReAdd presses the After a war block's Re-add button on a page it
// loads, the way a browser posts its form, and follows the answer wherever
// it lands.
func pressReAdd(t *testing.T, b *browser) *http.Response {
	t.Helper()
	return follow(t, b, postReAdd(t, b, parseHTML(t, b.get(foxholePath))))
}

// reportEntries returns each member a report list names as "<id> <role>",
// with " <reason>" after it when the list gives one, in the order the list
// shows them.
func reportEntries(list *html.Node) []string {
	var out []string
	eachElement(list, func(n *html.Node) {
		if id, ok := attrValue(n, "data-member"); ok {
			entry := id + " " + attrOf(n, "data-role")
			if reason, ok := attrValue(n, "data-reason"); ok {
				entry += " " + reason
			}
			out = append(out, entry)
		}
	})
	return out
}

// Approvals for one role or both, as a seed names them.
var (
	internalApproval = store.FoxholeApprovals{Internal: true}
	externalApproval = store.FoxholeApprovals{External: true}
	bothApprovals    = store.FoxholeApprovals{Internal: true, External: true}
)

// After a purge of both roles, one press of Re-add, with no preview, gives
// each approved Foxholer in the server each role they're approved for, and
// nobody else anything: Doe, who held Internal unapproved, doesn't get it
// back. Ash gets Internal with no rank role, as every Internal grant does.
// The report lists one change per member and role, Internal first, then
// External, each in display name order, and the member who left the server
// once for each role they're approved for. The re-add's change log entry is
// the report.
func TestReAddAfterAPurgeGivesEachApprovedFoxholerEachApprovedRole(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberAsh.ID, memberKestrel.ID, memberMarsh.ID)
	seedApprovals(t, w, store.MemberApprovals{MemberNames: foxholerGone, Approved: bothApprovals})
	startPurge(t, w, "both")
	w.awaitActionEnd(t)

	pressReAdd(t, w.b)
	w.awaitActionEnd(t)

	holders := map[string][]string{"Internal": w.discord.holdersOf(roleInternal), "External": w.discord.holdersOf(roleExternal)}
	want := map[string][]string{"Internal": sorted([]string{memberAsh.ID, memberMarsh.ID}), "External": sorted([]string{memberKestrel.ID, memberMarsh.ID})}
	if !maps.EqualFunc(holders, want, slices.Equal) {
		t.Errorf("after the re-add the holders are %v, want %v", holders, want)
	}
	page := parseHTML(t, w.b.get(foxholePath))
	report := reportBlock(t, page)
	changed := []string{memberMarsh.ID + " internal", memberAsh.ID + " internal", memberMarsh.ID + " external", memberKestrel.ID + " external"}
	if got := reportEntries(reportList(t, report, "changed")); !slices.Equal(got, changed) {
		t.Errorf("the report changed %v, want %v in that order", got, changed)
	}
	skipped := []string{foxholerGone.MemberID + " internal left", foxholerGone.MemberID + " external left"}
	if got := reportEntries(reportList(t, report, "skipped")); !slices.Equal(got, skipped) {
		t.Errorf("the report skipped %v, want %v", got, skipped)
	}
	entries := changeEntries(t, page, "re_add")
	if len(entries) != 1 {
		t.Fatalf("the change log holds %d re-add entries, want 1", len(entries))
	}
	if got := reportEntries(reportList(t, entries[0], "changed")); !slices.Equal(got, changed) {
		t.Errorf("the re-add's change log entry changed %v, want %v", got, changed)
	}
}

// After a purge of Internal alone, Re-add gives Internal back to each
// member approved for it, and skips each External approval, whose member
// still holds External.
func TestReAddAfterAnInternalPurgeGivesInternalBackAndSkipsExternalHolders(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberAsh.ID, memberKestrel.ID, memberMarsh.ID)
	startPurge(t, w, "internal")
	w.awaitActionEnd(t)

	pressReAdd(t, w.b)
	w.awaitActionEnd(t)

	if got, want := w.discord.holdersOf(roleInternal), sorted([]string{memberAsh.ID, memberMarsh.ID}); !slices.Equal(got, want) {
		t.Errorf("after the re-add %v hold Internal, want %v", got, want)
	}
	report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
	if got, want := pairs(reportList(t, report, "changed")), sorted([]string{memberAsh.ID + " internal", memberMarsh.ID + " internal"}); !slices.Equal(got, want) {
		t.Errorf("the report changed %v, want %v", got, want)
	}
	skipped := sorted([]string{memberKestrel.ID + " external holding", memberMarsh.ID + " external holding"})
	if got := sorted(reportEntries(reportList(t, report, "skipped"))); !slices.Equal(got, skipped) {
		t.Errorf("the report skipped %v, want %v", got, skipped)
	}
}

// A Retry of a re-add gives back each role the re-add missed, Internal and
// External alike: Ash's Internal and Kestrel's External both failed, and
// the Retry gives each back.
func TestReAddRetryGivesBackEachRoleTheReAddMissed(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApprovals(t, w, store.MemberApprovals{MemberNames: namesOf(memberAsh), Approved: internalApproval},
		store.MemberApprovals{MemberNames: namesOf(memberKestrel), Approved: externalApproval})
	ash, kestrel := memberAsh, memberKestrel
	ash.RoleIDs, kestrel.RoleIDs = nil, nil
	setMembers(w, memberDoe, ash, kestrel, memberMarsh, memberVance)
	for _, id := range []string{memberAsh.ID, memberKestrel.ID} {
		w.discord.failRoleWrites(id, missingPermissions())
	}
	pressReAdd(t, w.b)
	w.awaitActionEnd(t)
	for _, id := range []string{memberAsh.ID, memberKestrel.ID} {
		w.discord.failRoleWrites(id, nil)
	}

	assertRedirect(t, w.b.postForm(foxholeRetryPath, formPosts(t, openRetry(t, w.b), foxholeRetryPath)), foxholePath)
	w.awaitActionEnd(t)

	holders := map[string][]string{"Internal": w.discord.holdersOf(roleInternal), "External": w.discord.holdersOf(roleExternal)}
	if !slices.Contains(holders["Internal"], memberAsh.ID) || !slices.Contains(holders["External"], memberKestrel.ID) {
		t.Errorf("after the Retry the holders are %v, want %s holding Internal and %s External", holders, memberAsh.ID, memberKestrel.ID)
	}
}

// A re-add checks each approved Foxholer against the member list when it
// reaches them. One who left the server, and one who holds External
// already, are each skipped with that reason, so the report names whom a
// manager has to chase. A re-add with someone to give External to runs
// whoever else it skips, and its report lists both.
func TestReAddReportSkipsApprovedFoxholersNotInTheServerOrHoldingTheRole(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberDoe), namesOf(memberKestrel), foxholerGone)

	pressReAdd(t, w.b)
	w.awaitActionEnd(t)

	report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
	if got, want := pairs(reportList(t, report, "changed")), []string{memberDoe.ID + " external"}; !slices.Equal(got, want) {
		t.Errorf("the report changed %v, want %v", got, want)
	}
	skipped := listedReasons(reportList(t, report, "skipped"))
	want := map[string]string{foxholerGone.MemberID: string(commands.SkipLeft), memberKestrel.ID: string(commands.SkipHolding)}
	if !maps.Equal(skipped, want) {
		t.Errorf("the report skipped %v, want %v", skipped, want)
	}
}

// Every role change a re-add makes carries the audit log reason the spec
// fixes, naming the re-add and the forum user who started it, so a Discord
// moderator knows who did it without opening the panel.
func TestReAddRoleChangesNameTheReAddAndTheForumUserWhoStartedIt(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberDoe), namesOf(memberVance))

	pressReAdd(t, w.b)
	w.awaitActionEnd(t)

	writes := w.discord.roleChanges()
	if len(writes) == 0 {
		t.Fatal("the re-add changed no role")
	}
	want := commands.ReAddReason + managerUsername + " (forum user " + strconv.Itoa(managerUserID) + ")"
	for _, write := range writes {
		if write.Reason != want {
			t.Errorf("the change to %s carries the reason %q, want %q", write.MemberID, write.Reason, want)
		}
	}
}

// postReAdd posts the re-add form of a page already loaded, as a browser
// does from a tab left open or with no script, and returns the answer.
func postReAdd(t *testing.T, b *browser, doc *html.Node) *http.Response {
	t.Helper()
	action := attrOf(reAddBlock(t, doc), "action")
	if action == "" {
		t.Fatal("the re-add posts nowhere")
	}
	return b.postForm(action, formPosts(t, doc, action))
}

// A re-add starts only with a complete member list, though it plans from
// the approvals the store holds. Posted while the list is partial, it is
// refused: the page says why, and no re-add report starts.
func TestReAddPostedWhileTheMemberListIsPartialIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberDoe))
	page := parseHTML(t, w.b.get(foxholePath))
	w.discord.setMemberList(arrivingList)

	refused := parseHTML(t, postReAdd(t, w.b, page))

	if findLive(refused, "", "data-error", "member-list") == nil {
		t.Error("the page doesn't say the re-add was refused for the member list")
	}
	if entries := changeEntries(t, refused, "re_add"); len(entries) != 0 {
		t.Errorf("the change log holds %d re-add entries, want none", len(entries))
	}
}

// One Foxhole action runs at a time. While a purge runs, the page disables
// Re-add, and the server refuses a re-add posted anyway, by a browser with
// no script: the page says so, and no re-add starts. Doe, approved without
// External, is someone to re-add throughout, so the running purge is what
// disables the button.
func TestReAddWhileAPurgeRunsIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberKestrel), namesOf(memberDoe))
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "external")
	hold.next(t)

	page := parseHTML(t, w.b.get(foxholePath))

	if button := findElement(reAddBlock(t, page), "button", "", ""); button == nil || !disabled(button) {
		t.Error("the Re-add button is enabled while a purge runs")
	}
	refused := parseHTML(t, postReAdd(t, w.b, page))
	if findLive(refused, "", "data-error", "action-running") == nil {
		t.Error("the page doesn't say the re-add was refused for the purge running")
	}
	hold.open()
	w.awaitActionEnd(t)
	if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "re_add"); len(entries) != 0 {
		t.Errorf("the change log holds %d re-add entries, want none", len(entries))
	}
}

// Re-add is enabled while either role's line has someone in the server
// without the role, and disabled while neither has, when a press would only
// put an empty report in place of the last one, which after a war names who
// lost each role. Disabled, it gives the nobody-to-re-add hint.
func TestReAddIsEnabledWhileEitherRoleHasSomeoneToReAdd(t *testing.T) {
	cases := []struct {
		name      string
		approvals []store.MemberApprovals
		disabled  bool
	}{
		// Vance, approved for Internal, doesn't hold it. Kestrel holds External.
		{"only Internal has someone to re-add", []store.MemberApprovals{
			{MemberNames: namesOf(memberVance), Approved: internalApproval},
			{MemberNames: namesOf(memberKestrel), Approved: externalApproval}}, false},
		// A regression pin. Doe, approved for External, doesn't hold it. Ash
		// holds Internal.
		{"only External has someone to re-add", []store.MemberApprovals{
			{MemberNames: namesOf(memberDoe), Approved: externalApproval},
			{MemberNames: namesOf(memberAsh), Approved: internalApproval}}, false},
		// A regression pin. Marsh holds both roles, and the member approved
		// for Internal has left the server.
		{"neither has someone to re-add", []store.MemberApprovals{
			{MemberNames: namesOf(memberMarsh), Approved: bothApprovals},
			{MemberNames: foxholerGone, Approved: internalApproval}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			seedApprovals(t, w, tc.approvals...)

			block := reAddBlock(t, parseHTML(t, w.b.get(foxholePath)))

			button := findElement(block, "button", "", "")
			if button == nil {
				t.Fatal("the re-add has no button")
			}
			if got := disabled(button); got != tc.disabled {
				t.Errorf("the Re-add button is disabled %v, want %v", got, tc.disabled)
			}
			if hinted := findElement(block, "", "data-field", "nobody-to-re-add") != nil; hinted != tc.disabled {
				t.Errorf("the nobody-to-re-add hint shows: %v, want %v", hinted, tc.disabled)
			}
		})
	}
}
