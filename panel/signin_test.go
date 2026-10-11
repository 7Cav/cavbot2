package panel

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// startSignin presses Sign in with the forum on a page with no session and
// readies the fake forum for the challenge it sent, and returns the state
// the forum would hand back.
func startSignin(t *testing.T, f *fakeForum, b *browser) string {
	t.Helper()
	q := assertRedirect(t, b.post("/auth/start"), "/oauth2/authorize").Query()
	f.setChallenge(q.Get("code_challenge"))
	return q.Get("state")
}

// A callback the panel can't finish answers with a panel page that says
// which way it failed, and its Sign in again starts a fresh sign-in at the
// forum.
func TestCallbackFailureOffersSignInAgain(t *testing.T) {
	cases := []struct {
		name    string
		failure string
		run     func(t *testing.T, w *testWorld) *http.Response
	}{
		{"no sign-in in progress", "signin-not-started", func(t *testing.T, w *testWorld) *http.Response {
			return w.b.get("/auth/callback?code=" + testCode + "&state=some-state")
		}},
		{"the sign-in took too long", "signin-too-slow", func(t *testing.T, w *testWorld) *http.Response {
			state := startSignin(t, w.forum, w.b)
			w.clock.advance(5*time.Minute + time.Second)
			return w.b.get("/auth/callback?code=" + testCode + "&state=" + url.QueryEscape(state))
		}},
		{"the state differs from the one sent", "signin-mismatch", func(t *testing.T, w *testWorld) *http.Response {
			startSignin(t, w.forum, w.b)
			return w.b.get("/auth/callback?code=" + testCode + "&state=not-the-one-issued")
		}},
		{"the forum sent no code", "signin-no-code", func(t *testing.T, w *testWorld) *http.Response {
			state := startSignin(t, w.forum, w.b)
			return w.b.get("/auth/callback?state=" + url.QueryEscape(state))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)

			res := tc.run(t, w)

			doc := parseHTML(t, res)
			if got := failureOf(t, doc); got != tc.failure {
				t.Errorf("failure page = %q, want %s", got, tc.failure)
			}
			again := w.b.postForm("/auth/start", formPosts(t, doc, "/auth/start"))
			assertRedirect(t, again, "/oauth2/authorize")
		})
	}
}
