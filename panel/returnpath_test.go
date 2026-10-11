package panel

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
)

// A visit to a panel page from a browser that isn't signed in goes to the
// sign-in page, and signing in there comes back to that page, its query
// included: a recording notice's link opened in a fresh browser, and a
// hub's edit form opened after the session ran out.
func TestSigninComesBackToThePageVisited(t *testing.T) {
	cases := []struct {
		name string
		// visit readies the world and returns the browser that visits, the
		// page it visits and the code the forum hands back for its user.
		visit func(t *testing.T, w *testWorld) (b *browser, page, code string)
	}{
		{"a starter opens their recording's link", func(t *testing.T, w *testWorld) (*browser, string, string) {
			starter := w.addStarter(5001, "Able.A", "discord-a")
			return newBrowser(t, w.p), commands.RecordingPath(w.record(t, starter.discordID)), starter.account.code
		}},
		{"a panel admin's session ran out", func(t *testing.T, w *testWorld) (*browser, string, string) {
			signIn(t, w.forum, w.b)
			w.clock.advance(sessionLifetime + time.Second)
			return w.b, "/?hub=" + strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10), testCode
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			b, page, code := tc.visit(t, w)

			signinPage := parseHTML(t, follow(t, b, b.get(page)))
			res := signInFrom(t, w.forum, b, signinPage, code)

			if got := location(t, res).String(); got != page {
				t.Errorf("signing in leads to %q, want %s", got, page)
			}
			if again := b.get(page); again.StatusCode != http.StatusOK {
				t.Errorf("GET %s after signing in: status %d, want 200", page, again.StatusCode)
			}
		})
	}
}

// A sign-in that doesn't finish keeps the address it was to come back to:
// signing in from the page it ends on comes back there.
func TestSigninThatDoesNotFinishKeepsThePageVisited(t *testing.T) {
	cases := []struct {
		name string
		// fail answers the forum's consent page, which the sign-in start's
		// answer start sent the browser to, so the sign-in doesn't finish.
		fail func(t *testing.T, f *fakeForum, b *browser, start *http.Response) *http.Response
	}{
		{"the forum user presses Deny", func(t *testing.T, _ *fakeForum, b *browser, start *http.Response) *http.Response {
			q := assertRedirect(t, start, "/oauth2/authorize").Query()
			return b.get("/auth/callback?error=access_denied&state=" + url.QueryEscape(q.Get("state")))
		}},
		{"the sign-in takes too long", func(t *testing.T, f *fakeForum, b *browser, start *http.Response) *http.Response {
			installClock(t).advance(pendingLifetime + time.Second)
			return consent(t, f, b, start, testCode)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeForum(t)
			b := newBrowser(t, newTestPanel(t, f))
			signinPage := parseHTML(t, b.get("/signin?"+url.Values{paramNext: {foxholePath}}.Encode()))
			start := b.postForm("/auth/start", formPosts(t, signinPage, "/auth/start"))

			ended := parseHTML(t, follow(t, b, tc.fail(t, f, b, start)))
			res := signInFrom(t, f, b, ended, testCode)

			if got := res.Header.Get("Location"); got != foxholePath {
				t.Errorf("signing in again leads to %q, want %s", got, foxholePath)
			}
		})
	}
}

// A link to the sign-in page can name any address to come back to. Signing
// in from it comes back to the panel's root unless the address is a path on
// the panel itself, so a crafted link can't send a signed-in browser to
// another site.
func TestSigninNeverComesBackToAnotherSite(t *testing.T) {
	for _, next := range []string{
		"https://evil.example/x",
		"//evil.example/x",
		// Browsers read a backslash in a URL as a slash.
		`/\evil.example/x`,
		// Browsers drop a tab from a URL.
		"/\t/evil.example",
	} {
		t.Run(next, func(t *testing.T) {
			f := newFakeForum(t)
			b := newBrowser(t, newTestPanel(t, f))
			signinPage := parseHTML(t, b.get("/signin?"+url.Values{paramNext: {next}}.Encode()))

			res := signInFrom(t, f, b, signinPage, testCode)

			if got := res.Header.Get("Location"); got != "/" {
				t.Errorf("signing in leads to %q, want /", got)
			}
		})
	}
}
