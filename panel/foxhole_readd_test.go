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

// collaboratorGone is an approved collaborator who left the server: the
// member list doesn't hold them, and the store holds the names the panel
// last saw.
var collaboratorGone = store.MemberNames{MemberID: "100000000000000009", DisplayName: "LT Wren.A", Username: "awren"}

// namesOf is a member's names as the member list shows them.
func namesOf(m commands.ListedMember) store.MemberNames {
	return store.MemberNames{MemberID: m.ID, DisplayName: m.DisplayName(), Username: m.Username}
}

// seedApproved marks the members given approved collaborators in the store,
// as an Approve saved earlier would have, whatever roles they hold now.
func seedApproved(t *testing.T, w *testWorld, members ...store.MemberNames) {
	t.Helper()
	entry := store.ChangeLogEntry{ForumUserID: managerUserID, ForumUsername: managerUsername, Action: store.ChangeApprove, Diff: json.RawMessage(`{}`)}
	if err := w.st.ApproveFoxholeMembers(context.Background(), testGuildID, members, entry); err != nil {
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

// The After a war block counts the approved collaborators by what a re-add
// would do with each: those who hold External already, those in the server
// who don't, and those who left the server. A manager knows what Re-add
// will do before pressing it.
func TestAfterAWarBlockCountsApprovedCollaboratorsByWhatReAddWouldDo(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberKestrel), namesOf(memberMarsh),
		namesOf(memberDoe), namesOf(memberAsh), namesOf(memberVance), collaboratorGone)

	block := reAddBlock(t, parseHTML(t, w.b.get(foxholePath)))

	got := map[string]string{}
	for _, field := range []string{"holding", "not-holding", "not-in-server"} {
		got[field] = fieldText(t, block, field)
	}
	if want := map[string]string{"holding": "2", "not-holding": "3", "not-in-server": "1"}; !maps.Equal(got, want) {
		t.Errorf("the re-add counts approved collaborators %v, want %v", got, want)
	}
}

// pressReAdd presses the After a war block's Re-add button, the way a
// browser posts its form, and follows the answer wherever it lands.
func pressReAdd(t *testing.T, b *browser) *http.Response {
	t.Helper()
	doc := parseHTML(t, b.get(foxholePath))
	action := attrOf(reAddBlock(t, doc), "action")
	if action == "" {
		t.Fatal("the re-add posts nowhere")
	}
	return follow(t, b, b.postForm(action, formPosts(t, doc, action)))
}

// changeEntries returns the change log's entries of the action given, by
// their data-action marker.
func changeEntries(t *testing.T, doc *html.Node, action string) []*html.Node {
	t.Helper()
	section := findElement(doc, "", "data-field", "changes")
	if section == nil {
		t.Fatal("the page has no change log")
	}
	var out []*html.Node
	for _, el := range entryElements(section) {
		if attrOf(el, "data-action") == action {
			out = append(out, el)
		}
	}
	return out
}

// After a purge of External, one press of Re-add, with no preview, gives
// External back to the approved collaborators: Kestrel, approved, holds it
// again, and Marsh, who held it unapproved, doesn't. The report names
// Kestrel as changed, and so does the re-add's change log entry, which is
// the report.
func TestReAddAfterAPurgeGivesExternalBackToApprovedCollaboratorsOnly(t *testing.T) {
	w := newFoxholeWorld(t)
	approveThroughThePage(t, w, memberKestrel.ID)
	startPurge(t, w, "external")
	w.awaitActionEnd(t)

	pressReAdd(t, w.b)
	w.awaitActionEnd(t)

	if got, want := w.discord.holdersOf(roleExternal), []string{memberKestrel.ID}; !slices.Equal(got, want) {
		t.Errorf("after the re-add %v hold External, want %v", got, want)
	}
	page := parseHTML(t, w.b.get(foxholePath))
	want := []string{memberKestrel.ID + " external"}
	if got := pairs(reportList(t, reportBlock(t, page), "changed")); !slices.Equal(got, want) {
		t.Errorf("the report changed %v, want %v", got, want)
	}
	entries := changeEntries(t, page, "re_add")
	if len(entries) != 1 {
		t.Fatalf("the change log holds %d re-add entries, want 1", len(entries))
	}
	if got := pairs(reportList(t, entries[0], "changed")); !slices.Equal(got, want) {
		t.Errorf("the re-add's change log entry changed %v, want %v", got, want)
	}
}

// A re-add checks each approved collaborator against the member list when
// it reaches them. One who left the server, and one who holds External
// already, are each skipped with that reason, so the report names whom a
// manager has to chase.
func TestReAddReportSkipsCollaboratorsNotInTheServerOrHoldingExternal(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberKestrel), collaboratorGone)

	pressReAdd(t, w.b)
	w.awaitActionEnd(t)

	skipped := listedReasons(reportList(t, reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "skipped"))
	if want := map[string]string{collaboratorGone.MemberID: "left", memberKestrel.ID: "holding"}; !maps.Equal(skipped, want) {
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
	want := "Panel: Foxhole re-add of approved collaborators by " + managerUsername + " (forum user " + strconv.Itoa(managerUserID) + ")"
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
// no script: the page says so, and no re-add starts.
func TestReAddWhileAPurgeRunsIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberKestrel))
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
