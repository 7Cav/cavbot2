package panel

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// signInFrom signs in from a page's sign-in form as the account given, the
// way a browser does, and returns the page the sign-in lands on.
func signInFrom(t *testing.T, w *testWorld, page *html.Node, a *forumAccount) *html.Node {
	t.Helper()
	state := startSigninWith(t, w.forum, w.b, formPosts(t, page, "/auth/start"))
	res := w.b.get("/auth/callback?code=" + url.QueryEscape(a.code) + "&state=" + url.QueryEscape(state))
	return parseHTML(t, follow(t, w.b, res))
}

// The panel turns away a note saved from a panel session that has ended
// before the save changes anything, whichever way the session ended. The
// sign-in page the save lands on has no page to come back to, since a save
// is not a page. The page script reads that to tell a turned-away save from one
// that went through, so a return address here would show a lost note as
// saved (#688). Signed in again, the manager finds the old note on its row
// and the change log as it was.
func TestNoteSaveFromAnEndedSessionChangesNothingAndHasNoPageToComeBackTo(t *testing.T) {
	cases := []struct {
		name string
		end  func(w *testWorld)
		// resume undoes what end did at the forum, so the manager can sign
		// in again.
		resume func(w *testWorld)
	}{
		{"signed out in another tab",
			func(w *testWorld) { w.b.post("/auth/signout") },
			func(*testWorld) {}},
		{"two hours after sign-in",
			func(w *testWorld) { w.clock.advance(sessionLifetime + time.Second) },
			func(*testWorld) {}},
		{"the forum refuses the token",
			func(w *testWorld) { w.forum.setStatus(w.manager, http.StatusUnauthorized) },
			func(w *testWorld) { w.forum.setStatus(w.manager, http.StatusOK) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
			form := openNote(t, w.b, memberDoe.ID)
			before := foxholeChangeLog(t, form)
			tc.end(w)

			signin := parseHTML(t, follow(t, w.b, submitNote(t, w.b, form, "on leave until 3 Oct")))

			if next := formPosts(t, signin, "/auth/start").Get(paramNext); next != "" {
				t.Errorf("the sign-in after the save comes back to %q, want no page", next)
			}
			tc.resume(w)
			page := signInFrom(t, w, signin, w.manager)
			if got := noteOf(t, page, memberDoe.ID); got != "discharged 12 Sep" {
				t.Errorf("the row's note after signing in again = %q, want the old note", got)
			}
			if after := foxholeChangeLog(t, page); !reflect.DeepEqual(after, before) {
				t.Errorf("the change log after signing in again = %+v, want it as before the save, %+v", after, before)
			}
		})
	}
}

// A Foxhole view opened from a session the forum stopped accepting sends
// the manager to sign in, and the sign-in comes back to that view. A note
// save that went through redirects to its view, so when the session ends
// between the two, the sign-in page has the view to come back to. The page
// script reads that to show the note as saved, not turned away (#688).
func TestFoxholeViewOpenedAfterTheForumRefusesTheTokenSignsInBackToIt(t *testing.T) {
	w := newFoxholeWorld(t)
	w.forum.setStatus(w.manager, http.StatusUnauthorized)

	signin := parseHTML(t, follow(t, w.b, w.b.get(foxholeURL("", filterNoRole, ""))))

	w.forum.setStatus(w.manager, http.StatusOK)
	if page := signInFrom(t, w, signin, w.manager); findElement(page, "", "data-field", "no-role") == nil {
		t.Errorf("the sign-in lands on page %q with no no-role view, want the no-role view it left", pageOf(t, page))
	}
}
