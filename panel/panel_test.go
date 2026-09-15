package panel_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/panel"
	"golang.org/x/net/html"
)

// forumStub plays the forum's token and userinfo endpoints. The test scripts
// the userinfo answer per request; the token endpoint verifies the PKCE
// verifier against the challenge the test read from the authorize redirect.
type forumStub struct {
	srv *httptest.Server

	mu sync.Mutex
	// challenge is the code_challenge the panel sent to the authorize URL,
	// recorded by the test from the redirect. Empty means do not verify.
	challenge string
	// meStatus and meBody script the next userinfo answer.
	meStatus int
	meBody   string
	// tokenRequests counts code exchanges.
	tokenRequests int
}

const (
	stubAccessToken = "access-token-1"
	stubUsername    = "Doe.J"
	stubUserID      = 1234
	allowlistedID   = 47
)

func newForumStub(t *testing.T) *forumStub {
	t.Helper()
	f := &forumStub{meStatus: http.StatusOK, meBody: meBody(2, []int{35, allowlistedID, 72})}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tokenRequests++
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if f.challenge != "" && s256(r.PostForm.Get("code_verifier")) != f.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + stubAccessToken + `","token_type":"bearer","expires_in":7200,"scope":"user:read user:groups"}`))
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.meStatus)
		_, _ = w.Write([]byte(f.meBody))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *forumStub) setChallenge(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.challenge = c
}

func (f *forumStub) scriptMe(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meStatus, f.meBody = status, body
}

// meBody renders a userinfo answer with the given primary and secondary groups.
func meBody(primary int, secondary []int) string {
	b, _ := json.Marshal(map[string]any{"me": map[string]any{
		"user_id":             stubUserID,
		"username":            stubUsername,
		"user_group_id":       primary,
		"secondary_group_ids": secondary,
	}})
	return string(b)
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

const testBaseURL = "https://panel.test"

// newPanel builds a Server against the stub, reached through the same URL
// settings production reads from the environment.
func newPanel(t *testing.T) (*panel.Server, *forumStub) {
	t.Helper()
	forum := newForumStub(t)
	srv := panel.New(panel.Config{
		Addr:         ":0",
		BaseURL:      testBaseURL,
		ClientID:     "client-1",
		ClientSecret: "secret-1",
		AuthorizeURL: forum.srv.URL + "/oauth2/authorize",
		TokenURL:     forum.srv.URL + "/api/oauth2/token",
		UserinfoURL:  forum.srv.URL + "/api/me",
		GroupIDs:     []int{71, allowlistedID, 44},
	})
	return srv, forum
}

// do serves one request against the panel and returns the recorder. Cookies
// are carried by the caller, request by request, since the panel's Secure
// cookies would never be sent by a jar over plain http.
func do(t *testing.T, srv *panel.Server, method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, testBaseURL+target, nil)
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// setCookie returns the Set-Cookie with the given name from a response, or
// nil.
func setCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// location returns the parsed Location header of a redirect, failing when the
// response is not one.
func location(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if rec.Code < 300 || rec.Code > 399 {
		t.Fatalf("status = %d, want a redirect; body: %s", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location %q: %v", rec.Header().Get("Location"), err)
	}
	return u
}

// parseHTML parses a response body and fails on a parse error.
func parseHTML(t *testing.T, rec *httptest.ResponseRecorder) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(rec.Body.String()))
	if err != nil {
		t.Fatalf("parse HTML: %v", err)
	}
	return doc
}

// attr returns an element's attribute value and whether it is present.
func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// findAll returns every element node for which match returns true.
func findAll(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && match(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// text returns the concatenated text under a node, trimmed.
func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// hasForm reports whether the document holds a form posting to action.
func hasForm(doc *html.Node, action string) bool {
	forms := findAll(doc, func(n *html.Node) bool {
		if n.Data != "form" {
			return false
		}
		a, _ := attr(n, "action")
		m, _ := attr(n, "method")
		return a == action && strings.EqualFold(m, "post")
	})
	return len(forms) > 0
}

// dataCause returns the document's data-cause value and whether any element
// carries the attribute.
func dataCause(doc *html.Node) (string, bool) {
	nodes := findAll(doc, func(n *html.Node) bool {
		_, ok := attr(n, "data-cause")
		return ok
	})
	if len(nodes) == 0 {
		return "", false
	}
	v, _ := attr(nodes[0], "data-cause")
	return v, true
}

func TestSignedOutVisitorIsSentToSignIn(t *testing.T) {
	srv, _ := newPanel(t)

	rec := do(t, srv, http.MethodGet, "/")
	if loc := location(t, rec); loc.Path != "/signin" {
		t.Fatalf("GET / redirected to %q, want /signin", loc)
	}

	rec = do(t, srv, http.MethodGet, "/signin")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /signin status = %d, want 200", rec.Code)
	}
	doc := parseHTML(t, rec)
	if !hasForm(doc, "/auth/start") {
		t.Errorf("sign-in page has no form posting to /auth/start")
	}
	if cause, present := dataCause(doc); present {
		t.Errorf("plain sign-in page carries data-cause=%q; want none", cause)
	}
}

// assertPanelCookie checks the attributes both panel cookies carry: the
// __Host- contract (Secure, Path /, no Domain), HttpOnly, SameSite Lax, and no
// Max-Age on a set cookie.
func assertPanelCookie(t *testing.T, c *http.Cookie) {
	t.Helper()
	if c == nil {
		t.Fatal("cookie missing")
	}
	if !c.Secure {
		t.Errorf("%s is not Secure", c.Name)
	}
	if !c.HttpOnly {
		t.Errorf("%s is not HttpOnly", c.Name)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("%s SameSite = %v, want Lax", c.Name, c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("%s Path = %q, want /", c.Name, c.Path)
	}
	if c.Domain != "" {
		t.Errorf("%s Domain = %q, want none", c.Name, c.Domain)
	}
	if c.MaxAge != 0 {
		t.Errorf("%s MaxAge = %d, want no Max-Age", c.Name, c.MaxAge)
	}
	if c.Value == "" {
		t.Errorf("%s has an empty value", c.Name)
	}
}

// startSignIn posts to /auth/start and returns the authorize redirect's query
// and the sign-in cookie.
func startSignIn(t *testing.T, srv *panel.Server) (url.Values, *http.Cookie) {
	t.Helper()
	rec := do(t, srv, http.MethodPost, "/auth/start")
	loc := location(t, rec)
	return loc.Query(), setCookie(rec, "__Host-panel_signin")
}

func TestSignInStartRedirectsToTheForumWithStateAndPKCE(t *testing.T) {
	srv, forum := newPanel(t)

	rec := do(t, srv, http.MethodPost, "/auth/start")
	loc := location(t, rec)
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != forum.srv.URL+"/oauth2/authorize" {
		t.Errorf("redirected to %q, want the authorize URL", got)
	}
	q := loc.Query()
	if q.Get("client_id") != "client-1" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != testBaseURL+"/auth/callback" {
		t.Errorf("redirect_uri = %q, want %q", q.Get("redirect_uri"), testBaseURL+"/auth/callback")
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q", q.Get("response_type"))
	}
	scopes := strings.Fields(q.Get("scope"))
	for _, want := range []string{"user:read", "user:groups"} {
		if !slices.Contains(scopes, want) {
			t.Errorf("scope %q lacks %q", q.Get("scope"), want)
		}
	}
	if q.Get("state") == "" {
		t.Errorf("no state in the authorize URL")
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("PKCE challenge = %q method = %q, want an S256 challenge", q.Get("code_challenge"), q.Get("code_challenge_method"))
	}
	assertPanelCookie(t, setCookie(rec, "__Host-panel_signin"))

	second, _ := startSignIn(t, srv)
	if second.Get("state") == q.Get("state") {
		t.Errorf("two sign-in starts share state %q", q.Get("state"))
	}
}

// assertDeletedCookie checks a cookie the response tells the browser to drop:
// the same attributes as a set cookie (a __Host- deletion without them is
// ignored) and either a negative Max-Age or an Expires in the past.
func assertDeletedCookie(t *testing.T, c *http.Cookie) {
	t.Helper()
	if c == nil {
		t.Fatal("deletion cookie missing")
	}
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
		t.Errorf("%s deletion attributes = secure %v httponly %v samesite %v path %q domain %q", c.Name, c.Secure, c.HttpOnly, c.SameSite, c.Path, c.Domain)
	}
	expiresInPast := !c.Expires.IsZero() && c.Expires.Before(time.Now())
	if c.MaxAge >= 0 && !expiresInPast {
		t.Errorf("%s is not deleted: MaxAge %d, Expires %v", c.Name, c.MaxAge, c.Expires)
	}
}

// signIn runs the whole flow against the stub and returns the session cookie.
func signIn(t *testing.T, srv *panel.Server, forum *forumStub) *http.Cookie {
	t.Helper()
	q, signInCookie := startSignIn(t, srv)
	forum.setChallenge(q.Get("code_challenge"))
	rec := do(t, srv, http.MethodGet, "/auth/callback?code=code-1&state="+url.QueryEscape(q.Get("state")), signInCookie)
	if loc := location(t, rec); loc.Path != "/" {
		t.Fatalf("callback redirected to %q, want /", loc)
	}
	c := setCookie(rec, "__Host-panel_session")
	if c == nil {
		t.Fatalf("callback set no session cookie")
	}
	return c
}

// usernameOn returns the value under data-field="username" on a page.
func usernameOn(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	nodes := findAll(parseHTML(t, rec), func(n *html.Node) bool {
		v, _ := attr(n, "data-field")
		return v == "username"
	})
	if len(nodes) == 0 {
		t.Fatalf("no element carries data-field=\"username\"")
	}
	return text(nodes[0])
}

func TestCallbackWithMatchingStateCreatesASession(t *testing.T) {
	srv, forum := newPanel(t)
	q, signInCookie := startSignIn(t, srv)
	forum.setChallenge(q.Get("code_challenge"))

	rec := do(t, srv, http.MethodGet, "/auth/callback?code=code-1&state="+url.QueryEscape(q.Get("state")), signInCookie)

	if loc := location(t, rec); loc.Path != "/" {
		t.Fatalf("callback redirected to %q, want /", loc)
	}
	sessionCookie := setCookie(rec, "__Host-panel_session")
	assertPanelCookie(t, sessionCookie)
	assertDeletedCookie(t, setCookie(rec, "__Host-panel_signin"))

	home := do(t, srv, http.MethodGet, "/", sessionCookie)
	if home.Code != http.StatusOK {
		t.Fatalf("GET / with the session status = %d, want 200; body %s", home.Code, home.Body.String())
	}
	if got := usernameOn(t, home); got != stubUsername {
		t.Errorf("username on the page = %q, want %q", got, stubUsername)
	}
}

func TestCallbackWithAWrongVerifierIsRefusedByTheForum(t *testing.T) {
	srv, forum := newPanel(t)
	q, signInCookie := startSignIn(t, srv)
	forum.setChallenge("not-the-challenge-the-panel-sent")

	rec := do(t, srv, http.MethodGet, "/auth/callback?code=code-1&state="+url.QueryEscape(q.Get("state")), signInCookie)

	if rec.Code < 400 {
		t.Fatalf("status = %d, want a refusal when the token exchange fails", rec.Code)
	}
	if setCookie(rec, "__Host-panel_session") != nil {
		t.Errorf("a failed exchange set a session cookie")
	}
}

func TestCallbackRefusesAStateMismatch(t *testing.T) {
	srv, forum := newPanel(t)
	q, signInCookie := startSignIn(t, srv)
	forum.setChallenge(q.Get("code_challenge"))

	rec := do(t, srv, http.MethodGet, "/auth/callback?code=code-1&state=not-the-state", signInCookie)

	if rec.Code < 400 || rec.Code > 499 {
		t.Fatalf("status = %d, want a 4xx refusal", rec.Code)
	}
	if setCookie(rec, "__Host-panel_session") != nil {
		t.Errorf("a state mismatch set a session cookie")
	}
}

func TestCallbackRefusesAPendingSignInOlderThanFiveMinutes(t *testing.T) {
	srv, forum := newPanel(t)
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	panel.SetClockForTest(t, func() time.Time { return start })
	q, signInCookie := startSignIn(t, srv)
	forum.setChallenge(q.Get("code_challenge"))

	panel.SetClockForTest(t, func() time.Time { return start.Add(6 * time.Minute) })
	rec := do(t, srv, http.MethodGet, "/auth/callback?code=code-1&state="+url.QueryEscape(q.Get("state")), signInCookie)

	if rec.Code < 400 || rec.Code > 499 {
		t.Fatalf("status = %d, want a 4xx refusal", rec.Code)
	}
	if setCookie(rec, "__Host-panel_session") != nil {
		t.Errorf("a stale pending sign-in set a session cookie")
	}
}

func TestGroupCheckPassesOnPrimaryOrSecondaryGroup(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "allowlisted secondary group only", body: meBody(2, []int{35, allowlistedID, 72})},
		{name: "allowlisted primary group only", body: meBody(allowlistedID, []int{35, 72})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, forum := newPanel(t)
			forum.scriptMe(http.StatusOK, tc.body)
			sessionCookie := signIn(t, srv, forum)

			rec := do(t, srv, http.MethodGet, "/", sessionCookie)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET / status = %d, want 200", rec.Code)
			}
		})
	}
}

// assertSessionEnded checks the response that ends a session: a redirect to
// the sign-in page carrying cause, the session cookie deleted, and a repeat
// visit with the old cookie sent to plain sign-in because the session is gone.
func assertSessionEnded(t *testing.T, srv *panel.Server, forum *forumStub, rec *httptest.ResponseRecorder, old *http.Cookie, cause string) {
	t.Helper()
	loc := location(t, rec)
	if loc.Path != "/signin" || loc.Query().Get("cause") != cause {
		t.Fatalf("redirected to %q, want /signin?cause=%s", loc, cause)
	}
	assertDeletedCookie(t, setCookie(rec, "__Host-panel_session"))

	page := do(t, srv, http.MethodGet, loc.String())
	if got, present := dataCause(parseHTML(t, page)); !present || got != cause {
		t.Errorf("sign-in page data-cause = %q (present %v), want %q", got, present, cause)
	}

	// Whatever the forum says now, the session no longer exists.
	forum.scriptMe(http.StatusOK, meBody(2, []int{allowlistedID}))
	again := do(t, srv, http.MethodGet, "/", old)
	if loc := location(t, again); loc.Path != "/signin" || loc.Query().Get("cause") != "" {
		t.Errorf("repeat visit with the old cookie redirected to %q, want plain /signin", loc)
	}
}

func TestForum401EndsTheSessionAsExpired(t *testing.T) {
	srv, forum := newPanel(t)
	sessionCookie := signIn(t, srv, forum)
	forum.scriptMe(http.StatusUnauthorized, `{"errors":[{"code":"unauthorized"}]}`)

	rec := do(t, srv, http.MethodGet, "/", sessionCookie)

	assertSessionEnded(t, srv, forum, rec, sessionCookie, "expired")
}

func TestNoAllowlistedGroupEndsTheSessionAsNoGroup(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "200 with no allowlisted group", status: http.StatusOK, body: meBody(2, []int{35, 72})},
		{name: "403", status: http.StatusForbidden, body: `{"errors":[{"code":"forbidden"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, forum := newPanel(t)
			sessionCookie := signIn(t, srv, forum)
			forum.scriptMe(tc.status, tc.body)

			rec := do(t, srv, http.MethodGet, "/", sessionCookie)

			assertSessionEnded(t, srv, forum, rec, sessionCookie, "no-group")
		})
	}
}

func TestForumOutageKeepsTheSession(t *testing.T) {
	srv, forum := newPanel(t)
	sessionCookie := signIn(t, srv, forum)
	forum.scriptMe(http.StatusInternalServerError, "down")

	rec := do(t, srv, http.MethodGet, "/", sessionCookie)

	if rec.Code < 500 || rec.Code > 599 {
		t.Fatalf("GET / during an outage status = %d, want a 5xx error page", rec.Code)
	}
	if c := setCookie(rec, "__Host-panel_session"); c != nil {
		t.Errorf("an outage touched the session cookie: %v", c)
	}

	forum.scriptMe(http.StatusOK, meBody(2, []int{allowlistedID}))
	again := do(t, srv, http.MethodGet, "/", sessionCookie)
	if again.Code != http.StatusOK {
		t.Errorf("GET / after the outage status = %d, want 200 with the same session", again.Code)
	}
}

func TestSessionEndsTwoHoursAfterSignIn(t *testing.T) {
	srv, forum := newPanel(t)
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	panel.SetClockForTest(t, func() time.Time { return start })
	sessionCookie := signIn(t, srv, forum)

	panel.SetClockForTest(t, func() time.Time { return start.Add(2*time.Hour + time.Minute) })
	rec := do(t, srv, http.MethodGet, "/", sessionCookie)

	if loc := location(t, rec); loc.Path != "/signin" || loc.Query().Get("cause") != "expired" {
		t.Fatalf("redirected to %q, want /signin?cause=expired", loc)
	}
}

func TestSignOutEndsTheSession(t *testing.T) {
	srv, forum := newPanel(t)
	sessionCookie := signIn(t, srv, forum)

	rec := do(t, srv, http.MethodPost, "/auth/signout", sessionCookie)

	if loc := location(t, rec); loc.Path != "/signin" {
		t.Fatalf("sign-out redirected to %q, want /signin", loc)
	}
	assertDeletedCookie(t, setCookie(rec, "__Host-panel_session"))
	again := do(t, srv, http.MethodGet, "/", sessionCookie)
	if loc := location(t, again); loc.Path != "/signin" {
		t.Errorf("GET / with the signed-out cookie redirected to %q, want /signin", loc)
	}
}

func TestCrossSitePostIsRefused(t *testing.T) {
	srv, forum := newPanel(t)
	sessionCookie := signIn(t, srv, forum)

	req := httptest.NewRequest(http.MethodPost, testBaseURL+"/auth/signout", nil)
	req.AddCookie(sessionCookie)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code < 400 || rec.Code > 499 {
		t.Fatalf("cross-site POST status = %d, want a 4xx refusal", rec.Code)
	}
	again := do(t, srv, http.MethodGet, "/", sessionCookie)
	if again.Code != http.StatusOK {
		t.Errorf("GET / after the refused POST status = %d, want 200 with the session intact", again.Code)
	}
}
