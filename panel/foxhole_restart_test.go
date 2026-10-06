package panel

import (
	"slices"
	"testing"
)

// restart builds a second panel, with a Foxhole runtime of its own, over the
// same store and forum as w, the way the bot comes back after a deploy or a
// crash: its gateway state holds the fixture's roles and the member list as
// w's holds it now. The browser is signed in as another Foxhole manager.
func restart(t *testing.T, w *testWorld) *testWorld {
	t.Helper()
	back := newTestWorldOver(t, w.st, w.forum)
	back.discord.addRoles(foxholeGuildRoles...)
	back.discord.setMemberList(w.discord.MemberList(testGuildID))
	back.b = signedIn(t, back, "Jones.K", []int{testFoxholeGroupID})
	return back
}

// A restart partway through an action leaves its report stopped by a
// restart, with what it changed and what it never attempted. The action
// doesn't resume: the page shows the report, not a progress block, and a
// new action can start.
func TestRestartMarksARunningActionStoppedByARestart(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	first := hold.next(t)
	hold.pass(t)
	hold.next(t)

	back := restart(t, w)

	doc := parseHTML(t, back.b.get(foxholePath))
	if findElement(doc, "", "data-field", "progress") != nil {
		t.Error("after the restart the page shows a progress block, want the report")
	}
	report := reportBlock(t, doc)
	if got := outcomeOf(report); got != "restart" {
		t.Errorf("the report's outcome is %q, want restart", got)
	}
	if got, want := pairs(reportList(t, report, "changed")), []string{first.MemberID + " internal"}; !slices.Equal(got, want) {
		t.Errorf("the report changed %v, want %v, the change made before the restart", got, want)
	}
	if got := fieldText(t, reportList(t, report, "not-attempted"), "count"); got != "2" {
		t.Errorf("the report counts %s not attempted, want the 2 Internal holders it hadn't recorded", got)
	}
	if button := findElement(purgeForm(t, doc), "button", "", ""); button == nil || disabled(button) {
		t.Error("after the restart the purge form's button is disabled, want a new action free to start")
	}
}
