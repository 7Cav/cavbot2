package panel

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// submit posts a form the way a browser does, with the values of its
// inputs, and returns the response.
func submit(t *testing.T, b *browser, form *html.Node) *http.Response {
	t.Helper()
	return submitContext(t, b, context.Background(), form)
}

// submitContext is submit with the request's context ctx.
func submitContext(t *testing.T, b *browser, ctx context.Context, form *html.Node) *http.Response {
	t.Helper()
	action, _ := attrValue(form, "action")
	return b.doContext(ctx, http.MethodPost, action, http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		strings.NewReader(formValues(form).Encode()))
}

// formValues is what a browser posts for a form: the values of its inputs.
func formValues(form *html.Node) url.Values {
	fields := url.Values{}
	eachElement(form, func(n *html.Node) {
		if name, ok := attrValue(n, "name"); ok && n.Data == "input" {
			value, _ := attrValue(n, "value")
			fields.Add(name, value)
		}
	})
	return fields
}

// stopForm returns the Stop form on a progress block, and fails the test
// when the block has none.
func stopForm(t *testing.T, progress *html.Node) *html.Node {
	t.Helper()
	form := findElement(progress, "form", "data-field", "stop")
	if form == nil {
		t.Fatal("the progress block has no Stop")
	}
	return form
}

// signedIn is a second browser signed in as a forum user with the username
// and secondary groups given.
func signedIn(t *testing.T, w *testWorld, username string, secondary []int) *browser {
	t.Helper()
	b := newBrowser(t, w.p)
	signInAs(t, w.forum, b, w.forum.addUser(1357, username, 2, secondary))
	return b
}

// Any Foxhole manager or panel admin stops a running action from its
// progress block, whoever started it, with no confirmation. The change in
// flight finishes, and the report names who stopped it and lists the rest as
// not attempted. A purge of both roles stopped at its first change has
// changed one External holder and attempted no Internal holder.
func TestStopEndsAPurgeAfterTheChangeInFlight(t *testing.T) {
	cases := []struct {
		name      string
		username  string
		secondary []int
	}{
		{"another Foxhole manager", "Jones.K", []int{testFoxholeGroupID}},
		{"a panel admin who is no Foxhole manager", "Admin.A", []int{47}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			hold := holdRoleWrites(t, w)
			startPurge(t, w, "both")
			first := hold.next(t)
			stopper := signedIn(t, w, tc.username, tc.secondary)

			res := submit(t, stopper, stopForm(t, progressBlock(t, parseHTML(t, stopper.get(foxholePath)))))

			assertRedirect(t, res, foxholePath)
			hold.pass(t)
			w.awaitActionEnd(t)
			report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
			if got := outcomeOf(report); got != "stopped" {
				t.Errorf("the report's outcome is %q, want stopped", got)
			}
			if got := fieldText(t, report, "stopped-by"); got != tc.username {
				t.Errorf("the report says %q stopped it, want %q", got, tc.username)
			}
			if first.RoleID != roleExternal {
				t.Fatalf("the purge's first change was to role %s, want External", first.RoleID)
			}
			if got, want := pairs(reportList(t, report, "changed")), []string{first.MemberID + " external"}; !slices.Equal(got, want) {
				t.Errorf("the report changed %v, want only %v, the change in flight", got, want)
			}
			otherExternal := memberKestrel.ID
			if first.MemberID == memberKestrel.ID {
				otherExternal = memberMarsh.ID
			}
			want := []string{
				otherExternal + " external",
				memberDoe.ID + " internal", memberAsh.ID + " internal", memberMarsh.ID + " internal",
			}
			slices.Sort(want)
			if got := pairs(reportList(t, report, "not-attempted")); !slices.Equal(got, want) {
				t.Errorf("the report lists %v as not attempted, want %v", got, want)
			}
			if writes := w.discord.roleChanges(); len(writes) != 1 {
				t.Errorf("Discord got %d role changes, want only the one in flight when Stop was pressed", len(writes))
			}
		})
	}
}

// Between Stop and the end of the change in flight, the progress block says
// the action is stopping and who pressed Stop, and offers no second Stop.
func TestProgressBlockSaysStoppingUntilTheChangeInFlightEnds(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "both")
	hold.next(t)
	stopper := signedIn(t, w, "Jones.K", []int{testFoxholeGroupID})
	assertRedirect(t, submit(t, stopper, stopForm(t, progressBlock(t, parseHTML(t, stopper.get(foxholePath))))), foxholePath)

	progress := progressBlock(t, parseHTML(t, w.b.get(foxholePath)))

	stopping := findElement(progress, "", "data-field", "stopping")
	if stopping == nil {
		t.Fatal("the progress block doesn't say the action is stopping")
	}
	if got := fieldText(t, stopping, "stopped-by"); got != "Jones.K" {
		t.Errorf("the progress block says %q pressed Stop, want Jones.K", got)
	}
	if findElement(progress, "form", "data-field", "stop") != nil {
		t.Error("the progress block still offers Stop while the action stops")
	}
}

// A Stop from a page loaded while an earlier action ran stops nothing that
// started since: a later purge runs on to its end.
func TestStopFromAnEndedActionsProgressBlockLeavesTheNextActionRunning(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "external")
	hold.next(t)
	stale := stopForm(t, progressBlock(t, parseHTML(t, w.b.get(foxholePath))))
	hold.pass(t)
	hold.next(t) // the second of the 2 External holders
	hold.pass(t)
	w.awaitActionEnd(t)
	startPurge(t, w, "internal")
	hold.next(t)

	assertRedirect(t, submit(t, w.b, stale), foxholePath)

	hold.open()
	w.awaitActionEnd(t)
	report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
	if got := outcomeOf(report); got != "done" {
		t.Errorf("the later purge's outcome is %q, want done", got)
	}
	if got := fieldText(t, reportList(t, report, "changed"), "count"); got != "3" {
		t.Errorf("the later purge changed %s members, want all 3 Internal holders", got)
	}
}
