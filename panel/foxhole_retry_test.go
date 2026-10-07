package panel

import (
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// stopHeld presses Stop on the running action's progress block while its
// role change is held, lets the change answer, and waits for the action to
// end: the change in flight finishes, and the rest is not attempted.
func stopHeld(t *testing.T, w *testWorld, hold *roleHold) {
	t.Helper()
	assertRedirect(t, submit(t, w.b, stopForm(t, progressBlock(t, parseHTML(t, w.b.get(foxholePath))))), foxholePath)
	hold.pass(t)
	w.awaitActionEnd(t)
}

// retryLink returns a report's Retry, nil when it offers none.
func retryLink(report *html.Node) *html.Node {
	return findElement(report, "a", "data-field", "retry")
}

// A report offers Retry when the action failed on a member or never
// attempted one, and only then: a report with nothing missed offers none.
func TestReportWithFailedOrNotAttemptedMembersOffersRetry(t *testing.T) {
	cases := []struct {
		name  string
		run   func(t *testing.T, w *testWorld)
		retry bool
	}{
		{"a member failed", func(t *testing.T, w *testWorld) {
			w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
			startPurge(t, w, "external")
			w.awaitActionEnd(t)
		}, true},
		{"a member not attempted", func(t *testing.T, w *testWorld) {
			hold := holdRoleWrites(t, w)
			startPurge(t, w, "external")
			hold.next(t)
			stopHeld(t, w, hold)
		}, true},
		{"nothing missed", func(t *testing.T, w *testWorld) {
			startPurge(t, w, "external")
			w.awaitActionEnd(t)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			tc.run(t, w)

			report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))

			if got := retryLink(report) != nil; got != tc.retry {
				t.Errorf("the report offers Retry: %v, want %v", got, tc.retry)
			}
		})
	}
}

// openRetry follows the Retry of the report at the top of the page, the
// way a browser follows the link, and returns the page it opens. It fails
// the test when the report offers no Retry.
func openRetry(t *testing.T, b *browser) *html.Node {
	t.Helper()
	link := retryLink(reportBlock(t, parseHTML(t, b.get(foxholePath))))
	if link == nil {
		t.Fatal("the report offers no Retry")
	}
	return parseHTML(t, b.get(attrOf(link, "href")))
}

// A removal's Retry opens the remove preview of the same role holding the
// members the removal failed on or never attempted, and no one it changed.
func TestRemovalRetryOpensTheRemovePreviewWithTheMisses(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.failRoleWrites(memberMarsh.ID, missingPermissions())
	hold := holdRoleWrites(t, w)
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "internal",
		memberDoe.ID, memberAsh.ID, memberMarsh.ID)))
	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	if write := hold.next(t); write.MemberID != memberMarsh.ID {
		t.Fatalf("the removal's first change was to %s, want Marsh, first by display name", write.MemberID)
	}
	hold.pass(t)
	if write := hold.next(t); write.MemberID != memberAsh.ID {
		t.Fatalf("the removal's second change was to %s, want Ash", write.MemberID)
	}
	stopHeld(t, w, hold)

	retry := removePreviewBlock(t, openRetry(t, w.b))

	if role := attrOf(retry, "data-role"); role != "internal" {
		t.Errorf("the Retry's remove preview is of role %q, want internal", role)
	}
	if got, want := namedIn(retry), sorted([]string{memberMarsh.ID, memberDoe.ID}); !slices.Equal(got, want) {
		t.Errorf("the Retry's remove preview names %v, want the failed and the not attempted members %v", got, want)
	}
}

// A paste-box add's Retry fills the paste box with a line for each member
// the add failed on or never attempted, and opens its preview for the same
// role: each of them gets the role, and no one the add changed is in it.
func TestAddRetryFillsThePasteBoxWithTheMisses(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.failRoleWrites(memberAsh.ID, missingPermissions())
	hold := holdRoleWrites(t, w)
	page := parseHTML(t, w.b.get(foxholePath))
	preview := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "rowan_ash\njdoe\nrvance"))
	assertRedirect(t, confirmAdd(t, w.b, preview, nil), foxholePath)
	if write := hold.next(t); write.MemberID != memberAsh.ID {
		t.Fatalf("the add's first change was to %s, want Ash, the first line", write.MemberID)
	}
	hold.pass(t)
	if write := hold.next(t); write.MemberID != memberDoe.ID {
		t.Fatalf("the add's second change was to %s, want Doe, the second line", write.MemberID)
	}
	stopHeld(t, w, hold)

	retry := addPreviewBlock(t, openRetry(t, w.b))

	if role := attrOf(retry, "data-role"); role != "external" {
		t.Errorf("the Retry's add preview is of role %q, want external", role)
	}
	results := map[string]string{}
	for _, row := range addRows(retry) {
		results[attrOf(row, "data-member")] = attrOf(row, "data-result")
	}
	if want := map[string]string{memberAsh.ID: "gets", memberVance.ID: "gets"}; !maps.Equal(results, want) {
		t.Errorf("the Retry's add preview rows give %v, want %v", results, want)
	}
}

// A roster add's Retry opens the roster preview of the same unit with a
// row for each trooper the add failed on or never attempted, and none for
// the troopers it changed or skipped.
func TestRosterAddRetryOpensTheRosterPreviewWithOnlyTheMisses(t *testing.T) {
	w := newFoxholeWorld(t)
	serveRoster(t, trooperVance, trooper("Kestrel.T", memberKestrel.ID), trooperDoe)
	w.discord.failRoleWrites(memberVance.ID, missingPermissions())
	assertRedirect(t, confirmRoster(t, w.b, parseHTML(t, previewRoster(t, w, "D/ACD"))), foxholePath)
	w.awaitActionEnd(t)

	retry := rosterPreviewBlock(t, openRetry(t, w.b))

	if unit := attrOf(retry, "data-unit"); unit != "D/ACD" {
		t.Errorf("the Retry's roster preview is of unit %q, want D/ACD", unit)
	}
	if got, want := slices.Sorted(maps.Keys(rosterRows(retry))), []string{"Vance.R"}; !slices.Equal(got, want) {
		t.Errorf("the Retry's roster preview has rows for %v, want only the failed trooper %v", got, want)
	}
}

// retryConfirmation returns the page's Retry confirmation of the action
// given, by its data-action marker, and fails the test when the page has
// none.
func retryConfirmation(t *testing.T, doc *html.Node, action string) *html.Node {
	t.Helper()
	confirm := findElement(doc, "", "data-field", "retry-confirm")
	if confirm == nil {
		t.Fatal("the page has no Retry confirmation")
	}
	if got := attrOf(confirm, "data-action"); got != action {
		t.Fatalf("the Retry confirmation is of action %q, want %q", got, action)
	}
	return confirm
}

// A re-add has no preview of its own, so its Retry opens a confirmation
// listing the approved collaborators the re-add failed on or never
// attempted, and none it gave External to.
func TestReAddRetryOpensAConfirmationListingTheMisses(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberDoe), namesOf(memberAsh), namesOf(memberVance))
	w.discord.failRoleWrites(memberAsh.ID, missingPermissions())
	pressReAdd(t, w.b)
	w.awaitActionEnd(t)

	confirm := retryConfirmation(t, openRetry(t, w.b), "re_add")

	if got, want := namedIn(confirm), []string{memberAsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the Retry confirmation names %v, want only the failed collaborator %v", got, want)
	}
}

// A Retry of a purge, a re-add or a roster add, confirmed by another
// manager, is a new run: the page's report is a new one, started by the
// manager who confirmed it, and each role change it makes carries the
// audit log reason of the action it retries, naming that manager.
func TestRetryIsANewRunNamedForWhoConfirmedItWithTheActionsReason(t *testing.T) {
	cases := []struct {
		name   string
		run    func(t *testing.T, w *testWorld) (missed string)
		reason string
	}{
		{"purge", func(t *testing.T, w *testWorld) string {
			w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
			startPurge(t, w, "external")
			return memberKestrel.ID
		}, "Panel: Foxhole purge by Jones.K (forum user 1357)"},
		{"re-add", func(t *testing.T, w *testWorld) string {
			seedApproved(t, w, namesOf(memberAsh))
			w.discord.failRoleWrites(memberAsh.ID, missingPermissions())
			pressReAdd(t, w.b)
			return memberAsh.ID
		}, "Panel: Foxhole re-add of approved collaborators by Jones.K (forum user 1357)"},
		{"roster add", func(t *testing.T, w *testWorld) string {
			serveRoster(t, trooperVance)
			w.discord.failRoleWrites(memberVance.ID, missingPermissions())
			assertRedirect(t, confirmRoster(t, w.b, parseHTML(t, previewRoster(t, w, "D/ACD"))), foxholePath)
			return memberVance.ID
		}, "Panel: Foxhole D/ACD roster add by Jones.K (forum user 1357)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			missed := tc.run(t, w)
			w.awaitActionEnd(t)
			w.discord.failRoleWrites(missed, nil)
			retried := attrOf(reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "data-report")
			jones := signedIn(t, w, "Jones.K", []int{testFoxholeGroupID})
			before := len(w.discord.roleChanges())

			assertRedirect(t, jones.postForm(foxholeRetryPath, formPosts(t, openRetry(t, jones), foxholeRetryPath)), foxholePath)
			w.awaitActionEnd(t)

			report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
			if got := attrOf(report, "data-report"); got == retried {
				t.Errorf("the page's report after the Retry is %s, the retried report's, want a new one", got)
			}
			if got := fieldText(t, report, "started-by"); got != "Jones.K" {
				t.Errorf("the Retry's report names %q as its starter, want Jones.K, who confirmed it", got)
			}
			writes := w.discord.roleChanges()[before:]
			if len(writes) == 0 {
				t.Fatal("the Retry changed no role")
			}
			for _, write := range writes {
				if write.Reason != tc.reason {
					t.Errorf("the Retry's change to %s carries the reason %q, want %q", write.MemberID, write.Reason, tc.reason)
				}
			}
		})
	}
}

// rolePairs returns the member and role of each role change given, as
// "<member ID> <role ID>", sorted.
func rolePairs(writes []fakeRoleWrite) []string {
	var out []string
	for _, write := range writes {
		out = append(out, write.MemberID+" "+write.RoleID)
	}
	slices.Sort(out)
	return out
}

// A purge's Retry stays a purge. Opened from the purge's change log entry
// after a re-add has replaced its report at the top, it lists each member
// the purge never reached with the role they still had to lose. Confirming
// takes just those roles: the collaborator re-added since keeps External,
// the approved collaborator who loses it stays approved, and the change log
// records the Retry as a purge.
func TestPurgeRetryPurgesJustTheMissesAndKeepsApprovals(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberKestrel), namesOf(memberMarsh))
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "both")
	if write := hold.next(t); write.MemberID != memberMarsh.ID || write.RoleID != roleExternal {
		t.Fatalf("the purge's first change was %s %s, want Marsh's External, first by display name", write.MemberID, write.RoleID)
	}
	stopHeld(t, w, hold)
	hold.open()
	pressReAdd(t, w.b)
	w.awaitActionEnd(t)
	entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "purge")
	if len(entries) != 1 || retryLink(entries[0]) == nil {
		t.Fatal("the purge's change log entry offers no Retry")
	}
	doc := parseHTML(t, w.b.get(attrOf(retryLink(entries[0]), "href")))

	confirm := retryConfirmation(t, doc, "purge")

	want := sorted([]string{memberKestrel.ID + " external", memberDoe.ID + " internal", memberAsh.ID + " internal", memberMarsh.ID + " internal"})
	if got := pairs(confirm); !slices.Equal(got, want) {
		t.Errorf("the purge's Retry confirmation lists %v, want the members it never reached with their roles %v", got, want)
	}
	before := len(w.discord.roleChanges())
	assertRedirect(t, w.b.postForm(foxholeRetryPath, formPosts(t, doc, foxholeRetryPath)), foxholePath)
	w.awaitActionEnd(t)
	wantWrites := sorted([]string{memberKestrel.ID + " " + roleExternal, memberDoe.ID + " " + roleInternal,
		memberAsh.ID + " " + roleInternal, memberMarsh.ID + " " + roleInternal})
	if got := rolePairs(w.discord.roleChanges()[before:]); !slices.Equal(got, wantWrites) {
		t.Errorf("the Retry changed %v, want just the roles the purge never took %v", got, wantWrites)
	}
	if got := w.discord.holdersOf(roleInternal); len(got) != 0 {
		t.Errorf("after the Retry %v hold Internal, want nobody", got)
	}
	if got, want := w.discord.holdersOf(roleExternal), []string{memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("after the Retry %v hold External, want only %v, re-added since the purge", got, want)
	}
	page := parseHTML(t, w.b.get(foxholePath))
	if got, want := approvedRows(t, page), sorted([]string{memberKestrel.ID, memberMarsh.ID}); !slices.Equal(got, want) {
		t.Errorf("after the Retry the approved collaborators are %v, want %v still", got, want)
	}
	if action, _ := newestChange(t, page); action != "purge" {
		t.Errorf("the change log's newest entry is a %s, want the Retry's purge", action)
	}
}

// A purge's Retry whose members have all lost their role since changes
// nobody, so its confirmation says so where Confirm would be and offers no
// Confirm.
func TestPurgeRetryWithNobodyLeftToPurgeOffersNoConfirm(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
	startPurge(t, w, "external")
	w.awaitActionEnd(t)
	w.discord.editMember(memberKestrel.ID, func(m *commands.ListedMember) {
		m.RoleIDs = slices.DeleteFunc(m.RoleIDs, func(id string) bool { return id == roleExternal })
	})

	confirm := retryConfirmation(t, openRetry(t, w.b), "purge")

	if findElement(confirm, "button", "data-field", "confirm") != nil {
		t.Error("the Retry confirmation offers Confirm with nobody left to purge")
	}
	if findElement(confirm, "", "data-field", "nobody-to-purge") == nil {
		t.Error("the Retry confirmation doesn't say there's nobody left to purge")
	}
}

// memberReason is the reason a list gives for the member with the ID
// given, empty when it names no such member or gives no reason.
func memberReason(list *html.Node, id string) string {
	entry := findElement(list, "", "data-member", id)
	if entry == nil {
		return ""
	}
	reason := findElement(entry, "", "data-field", "reason")
	if reason == nil {
		return ""
	}
	return strings.TrimSpace(textOf(reason))
}

// The page a Retry opens shows each member the action failed on with the
// reason Discord gave, as the report did, so a manager sees a missing
// permission or a deleted role before confirming another round.
func TestRetryShowsWhyEachMemberFailedLastTime(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
	startPurge(t, w, "external")
	w.awaitActionEnd(t)
	reported := memberReason(reportList(t, reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "failed"), memberKestrel.ID)
	if reported == "" {
		t.Fatal("the report gives no reason for the member who failed")
	}

	retrying := findElement(openRetry(t, w.b), "", "data-field", "retrying")

	if retrying == nil {
		t.Fatal("the page the Retry opens doesn't say what it retries")
	}
	if got := memberReason(retrying, memberKestrel.ID); got != reported {
		t.Errorf("the Retry gives the reason %q for the member who failed, want the report's, %q", got, reported)
	}
}

// A Retry confirmed for a report with nothing to retry, as from a stale or
// hand-made page, changes nothing and is refused with a reason on the
// page. It is no internal failure, so nothing reaches Sentry.
func TestRetryOfAReportWithNothingToRetryIsRefusedWithoutReachingSentry(t *testing.T) {
	cases := []struct {
		name   string
		report func(t *testing.T, w *testWorld) string
	}{
		{"a report that missed nobody", func(t *testing.T, w *testWorld) string {
			startPurge(t, w, "external")
			w.awaitActionEnd(t)
			return attrOf(reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "data-report")
		}},
		{"no such report", func(t *testing.T, w *testWorld) string { return "999999" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			report := tc.report(t, w)
			reported := recordSentry(t)
			before := len(w.discord.roleChanges())

			res := w.b.postForm(foxholeRetryPath, url.Values{fieldReport: {report}})

			if findLive(parseHTML(t, res), "", "data-error", "nothing-to-retry") == nil {
				t.Error("the page doesn't say there was nothing to retry")
			}
			if n := len(reported.sent()); n != 0 {
				t.Errorf("Sentry got %d events, want none", n)
			}
			if writes := w.discord.roleChanges()[before:]; len(writes) != 0 {
				t.Errorf("the refused Retry made the role changes %v, want none", writes)
			}
		})
	}
}
