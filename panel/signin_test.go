package panel

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// startSignin presses Sign in with the forum on a page with no session and
// readies the fake forum for the challenge it sent, and returns the state
// the forum would hand back.
func startSignin(t *testing.T, f *fakeForum, b *browser) string {
	t.Helper()
	return startSigninWith(t, f, b, nil)
}

// startSigninWith is startSignin through a sign-in form that posts form.
func startSigninWith(t *testing.T, f *fakeForum, b *browser, form url.Values) string {
	t.Helper()
	q := assertRedirect(t, b.postForm("/auth/start", form), "/oauth2/authorize").Query()
	f.setChallenge(q.Get("code_challenge"))
	return q.Get("state")
}

// forumSendsBack is the forum's consent page handing the default user's code
// back with the state given.
func forumSendsBack(b *browser, state string) *http.Response {
	b.t.Helper()
	return b.get("/auth/callback?code=" + url.QueryEscape(testCode) + "&state=" + url.QueryEscape(state))
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

// signInThrough follows res to the page it leads to, the sign-in page or an
// error page, and signs in from it the way a browser does, as the forum's
// default user: the page's sign-in button posts its form, and the forum
// hands back its code. It returns the callback's answer.
func signInThrough(t *testing.T, w *testWorld, res *http.Response) *http.Response {
	t.Helper()
	doc := parseHTML(t, follow(t, w.b, res))
	return forumSendsBack(w.b, startSigninWith(t, w.forum, w.b, formPosts(t, doc, "/auth/start")))
}

// assertOnHubEditForm checks a page shows the hub's edit form: an element
// carrying the hub's data-hub holds the form's Save.
func assertOnHubEditForm(t *testing.T, doc *html.Node, hubID int64) {
	t.Helper()
	found := false
	eachElement(doc, func(n *html.Node) {
		if attrOf(n, "data-hub") == strconv.FormatInt(hubID, 10) && findElement(n, "", "data-field", "save") != nil {
			found = true
		}
	})
	if !found {
		t.Fatalf("the page shows no edit form for hub %d", hubID)
	}
}

// A visit to a panel page with no session goes through sign-in and comes
// back to that page, whether the browser never signed in or its session
// ended.
func TestSigninComesBackToThePageThatSentThere(t *testing.T) {
	cases := []struct {
		name  string
		visit func(t *testing.T, w *testWorld, address string) *http.Response
	}{
		{"never signed in", func(t *testing.T, w *testWorld, address string) *http.Response {
			return w.b.get(address)
		}},
		{"the session ran out", func(t *testing.T, w *testWorld, address string) *http.Response {
			signIn(t, w.forum, w.b)
			w.clock.advance(2*time.Hour + time.Second)
			return w.b.get(address)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			id := storedHubID(t, w.st, "hub-1")

			res := signInThrough(t, w, tc.visit(t, w, "/?hub="+strconv.FormatInt(id, 10)))

			assertOnHubEditForm(t, parseHTML(t, follow(t, w.b, res)), id)
		})
	}
}

// A save posted with no session is not a page to come back to, so its
// sign-in lands where any sign-in does: a panel admin's on the Hubs page.
func TestSigninFromASaveWithNoSessionLandsOnTheLanding(t *testing.T) {
	w := newTestWorld(t, testHub())
	id := storedHubID(t, w.st, "hub-1")

	res := signInThrough(t, w, w.b.postForm("/hubs/"+strconv.FormatInt(id, 10), updateForm(t, w.st)))

	if got := pageOf(t, parseHTML(t, follow(t, w.b, res))); got != pageHubs {
		t.Errorf("the sign-in lands on page %q, want %s", got, pageHubs)
	}
}

// codeNeverIssued is the forum handing back a code it never issued, which
// its token endpoint refuses with a 400.
func codeNeverIssued(w *testWorld, state string) *http.Response {
	return w.b.get("/auth/callback?code=never-issued&state=" + url.QueryEscape(state))
}

// tokenExchangeUnanswered is the forum handing back the default user's code
// while its token endpoint closes the connection with no answer, as it
// answers again once the callback is done.
func tokenExchangeUnanswered(w *testWorld, state string) *http.Response {
	w.forum.setTokenDrop(true)
	defer w.forum.setTokenDrop(false)
	return forumSendsBack(w.b, state)
}

// groupCheckAnswers is the forum handing back the default user's code while
// its userinfo endpoint answers that user with status, as it answers again
// once the callback is done.
func groupCheckAnswers(status int) func(w *testWorld, state string) *http.Response {
	return func(w *testWorld, state string) *http.Response {
		w.forum.setStatus(w.forum.accounts[0], status)
		defer w.forum.setStatus(w.forum.accounts[0], http.StatusOK)
		return forumSendsBack(w.b, state)
	}
}

// A first try that didn't finish keeps the page the sign-in is for, and the
// heading its link opened at. After Deny or a refusal at the forum, the
// sign-in page the callback sends to, and after a state that didn't match
// or a forum that didn't answer, the error page's Sign in again, each sign
// in back to it.
func TestSigninThatDidNotFinishKeepsThePage(t *testing.T) {
	cases := []struct {
		name string
		// fail answers the forum's consent the way it didn't finish, given
		// the state the forum was sent.
		fail func(w *testWorld, state string) *http.Response
	}{
		{"Deny on the forum", func(w *testWorld, state string) *http.Response {
			return w.b.get("/auth/callback?error=access_denied&state=" + url.QueryEscape(state))
		}},
		{"a state that didn't match", func(w *testWorld, _ string) *http.Response {
			return w.b.get("/auth/callback?code=" + testCode + "&state=not-the-one-issued")
		}},
		{"the token endpoint refuses a code it never issued", codeNeverIssued},
		{"the group check at sign-in gets a 401", groupCheckAnswers(http.StatusUnauthorized)},
		{"the group check at sign-in gets a 403", groupCheckAnswers(http.StatusForbidden)},
		{"the token exchange gets no answer", tokenExchangeUnanswered},
		{"the group check at sign-in gets no answer", func(w *testWorld, state string) *http.Response {
			w.forum.setUserinfoDrop(true)
			defer w.forum.setUserinfoDrop(false)
			return forumSendsBack(w.b, state)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			id := storedHubID(t, w.st, "hub-1")
			hub := "hub=" + strconv.FormatInt(id, 10)
			signin := parseHTML(t, follow(t, w.b, w.b.get("/?"+hub+"#edit")))
			// The sign-in page's script puts the fragment of the address the
			// gate answered on next, as the visit through the edit link has.
			form := formPosts(t, signin, "/auth/start")
			form.Set(paramNext, form.Get(paramNext)+"#edit")
			state := startSigninWith(t, w.forum, w.b, form)

			res := signInThrough(t, w, tc.fail(w, state))

			if back := location(t, res); back.Path != "/" || back.RawQuery != hub || back.Fragment != "edit" {
				t.Errorf("the second sign-in comes back to %q, want /?%s#edit", res.Header.Get("Location"), hub)
			}
			assertOnHubEditForm(t, parseHTML(t, follow(t, w.b, res)), id)
		})
	}
}

// A sign-in for the landing that didn't finish leaves the sign-in page, or
// the 502 page, with no page to come back to, and its second sign-in lands
// on the landing.
func TestSigninForTheLandingThatDidNotFinishLandsOnTheLanding(t *testing.T) {
	cases := []struct {
		name string
		fail func(w *testWorld, state string) *http.Response
	}{
		{"the token endpoint refuses a code it never issued", codeNeverIssued},
		{"the token exchange gets no answer", tokenExchangeUnanswered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			state := startSigninWith(t, w.forum, w.b, formPosts(t, parseHTML(t, w.b.get("/signin")), "/auth/start"))

			page := parseHTML(t, follow(t, w.b, tc.fail(w, state)))

			form := formPosts(t, page, "/auth/start")
			if next, ok := form[paramNext]; ok {
				t.Errorf("the page's sign-in comes back to %q, want no page", next)
			}
			res := forumSendsBack(w.b, startSigninWith(t, w.forum, w.b, form))
			if got := pageOf(t, parseHTML(t, follow(t, w.b, res))); got != pageHubs {
				t.Errorf("the second sign-in lands on page %q, want %s", got, pageHubs)
			}
		})
	}
}

// A sign-in the group check refuses at the forum's 401 or 403 lands on the
// sign-in page saying why, while it keeps a page to come back to.
func TestSigninTheGroupCheckRefusesSaysWhy(t *testing.T) {
	cases := []struct {
		status int
		cause  string
	}{
		{http.StatusUnauthorized, "expired"},
		{http.StatusForbidden, "refused"},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			w := newTestWorld(t)
			state := startSigninWith(t, w.forum, w.b, url.Values{paramNext: {foxholePath}})

			res := groupCheckAnswers(tc.status)(w, state)

			if cause, ok := mainCause(t, follow(t, w.b, res)); !ok || cause != tc.cause {
				t.Errorf("sign-in page data-cause = %q (present %v), want %q", cause, ok, tc.cause)
			}
		})
	}
}

// A sign-in started from a form that names an address off the panel's own
// origin, or one a browser would read as off it, lands on the landing. A
// form posted from anywhere can name any address, so the check holds
// without the sign-in page.
func TestSigninNeverComesBackOffThePanel(t *testing.T) {
	for _, next := range []string{
		"https://evil.example/",
		"//evil.example",
		`/./\evil.example`,
		"/\t/evil.example",
	} {
		t.Run(next, func(t *testing.T) {
			w := newTestWorld(t)
			state := startSigninWith(t, w.forum, w.b, url.Values{"next": {next}})

			res := forumSendsBack(w.b, state)

			if got := res.Header.Get("Location"); got != "/" {
				t.Errorf("the sign-in comes back to %q, want /", got)
			}
		})
	}
}

// A sign-in comes back to the path and query of the address its form
// posted, whatever fragment follows them, and with the fragment when it
// names a heading. The panel may drop any other fragment, but the fragment
// never costs the path, the query or the panel's origin.
func TestSigninComesBackToThePathAndQueryWhateverTheFragment(t *testing.T) {
	cases := []struct {
		name, next string
		// back is the address the sign-in comes back to. Where it names no
		// fragment, the panel may drop the one posted.
		back string
	}{
		{"a backslash", `/foxhole?filter=internal#hold\ers`, "/foxhole?filter=internal"},
		{"dot segments then a backslash", `/foxhole#/../\evil.example`, "/foxhole"},
		{"dot segments then two slashes", "/foxhole#/..//evil.example", "/foxhole"},
		{"a heading after a query", "/recordings?view=all#recording-12", "/recordings?view=all#recording-12"},
		{"a heading on the landing", "/#hubs", "/#hubs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			state := startSigninWith(t, w.forum, w.b, url.Values{"next": {tc.next}})

			res := forumSendsBack(w.b, state)

			want, err := url.Parse(tc.back)
			if err != nil {
				t.Fatalf("back %q: %v", tc.back, err)
			}
			got := location(t, res)
			if got.Scheme != "" || got.Host != "" || got.Path != want.Path || got.RawQuery != want.RawQuery || want.Fragment != "" && got.Fragment != want.Fragment {
				t.Errorf("the sign-in comes back to %q, want %s", res.Header.Get("Location"), tc.back)
			}
		})
	}
}
