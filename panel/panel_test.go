package panel

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"
)

const (
	testClientID     = "1234567890123456"
	testClientSecret = "secret-of-the-panel"
	testBaseURL      = "https://panel.test"
	testAccessToken  = "access-token-1"
	testCode         = "code-1"
	testUserID       = 1234
	testUsername     = "Doe.J"
)

// fakeForum plays the forum's token and userinfo endpoints over a real
// listener, reached through the same URL settings production uses. The token
// endpoint enforces what Xenforo enforces (client credentials in the body, the
// code it issued, a PKCE verifier matching the challenge) so a test asserts the
// sign-in outcome and never the request's form body. The panel does not know
// the forum is fake.
type fakeForum struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// challenge is the code_challenge the test read off the start redirect;
	// the token endpoint refuses a verifier that does not hash to it.
	challenge     string
	tokenRequests int

	userinfoStatus   int
	userinfoBody     string
	userinfoRequests int
	// userinfoDrop and tokenDrop make that endpoint close the connection
	// with no answer, the transport error a forum outage produces.
	userinfoDrop bool
	tokenDrop    bool
}

func newFakeForum(t *testing.T) *fakeForum {
	t.Helper()
	f := &fakeForum{t: t, userinfoStatus: http.StatusOK, userinfoBody: userinfoJSON(2, []int{35, 47, 72})}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/oauth2/token", f.token)
	mux.HandleFunc("GET /api/me", f.userinfo)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// userinfoJSON is the /api/me envelope with the two fields the user:groups
// scope adds.
func userinfoJSON(primary int, secondary []int) string {
	body, _ := json.Marshal(map[string]any{"me": map[string]any{
		"user_id":             testUserID,
		"username":            testUsername,
		"user_group_id":       primary,
		"secondary_group_ids": secondary,
	}})
	return string(body)
}

func (f *fakeForum) setChallenge(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.challenge = c
}

func (f *fakeForum) setUserinfo(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userinfoStatus, f.userinfoBody = status, body
}

func (f *fakeForum) setUserinfoDrop(drop bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userinfoDrop = drop
}

func (f *fakeForum) setTokenDrop(drop bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenDrop = drop
}

// drop closes the client's connection with no answer.
func (f *fakeForum) drop(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		f.t.Errorf("hijack: %v", err)
		return
	}
	_ = conn.Close()
}

func (f *fakeForum) tokenRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenRequests
}

func (f *fakeForum) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenRequests++
	if f.tokenDrop {
		f.drop(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	refuse := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":"api_error.%s","params":[]}]}`, code, code)
	}
	for _, key := range []string{"client_id", "client_secret", "grant_type"} {
		if r.PostForm.Get(key) == "" {
			refuse("required_input_missing")
			return
		}
	}
	if r.PostForm.Get("client_id") != testClientID || r.PostForm.Get("client_secret") != testClientSecret {
		refuse("invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != testCode {
		refuse("invalid_grant")
		return
	}
	verifier := r.PostForm.Get("code_verifier")
	if verifier == "" || s256(verifier) != f.challenge {
		refuse("invalid_grant")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"refresh-1","token_type":"bearer","expires_in":7200,"scope":"user:read user:groups"}`, testAccessToken)
}

func (f *fakeForum) userinfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userinfoRequests++
	if f.userinfoDrop {
		f.drop(w)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testAccessToken {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"code":"unauthorized","message":"api_error.unauthorized","params":[]}]}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.userinfoStatus)
	_, _ = io.WriteString(w, f.userinfoBody)
}

// s256 is the PKCE transform: base64url without padding of the SHA-256 of the
// verifier.
func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func testConfig(f *fakeForum) Config {
	return Config{
		Addr:         ":0",
		BaseURL:      testBaseURL,
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		AuthorizeURL: f.srv.URL + "/oauth2/authorize",
		TokenURL:     f.srv.URL + "/api/oauth2/token",
		UserinfoURL:  f.srv.URL + "/api/me",
		GroupIDs:     []int{71, 47, 44},
	}
}

// newTestPanel is the panel over an empty store, for the sign-in tests.
func newTestPanel(t *testing.T, f *fakeForum) *Panel {
	t.Helper()
	return newTestWorldWith(t, f).p
}

// browser drives the panel's handler the way a browser would: it carries the
// cookies each response sets into the next request and drops a cookie the
// response deletes, whichever deletion idiom the response used. It never
// follows a redirect; a test reads the Location itself.
type browser struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]string
}

func newBrowser(t *testing.T, p *Panel) *browser {
	t.Helper()
	return &browser{t: t, h: p.Handler(), cookies: map[string]string{}}
}

func (b *browser) do(method, target string, header http.Header, body io.Reader) *http.Response {
	b.t.Helper()
	req := httptest.NewRequest(method, testBaseURL+target, body)
	for k, v := range header {
		req.Header[k] = v
	}
	for name, value := range b.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	res := rec.Result()
	b.absorbCookies(res)
	return res
}

// absorbCookies carries the cookies a response sets into the next request
// and drops the ones it deletes.
func (b *browser) absorbCookies(res *http.Response) {
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(now())) {
			delete(b.cookies, c.Name)
			continue
		}
		b.cookies[c.Name] = c.Value
	}
}

func (b *browser) get(target string) *http.Response {
	b.t.Helper()
	return b.do(http.MethodGet, target, nil, nil)
}

// post submits an empty form, the way the sign-in and sign-out buttons do.
func (b *browser) post(target string) *http.Response {
	b.t.Helper()
	return b.postForm(target, nil)
}

// postForm submits a form the way a browser does.
func (b *browser) postForm(target string, form url.Values) *http.Response {
	b.t.Helper()
	return b.do(http.MethodPost, target, http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, strings.NewReader(form.Encode()))
}

// setCookie is a cookie the test sets by hand, such as one from a session the
// panel has already ended.
func (b *browser) setCookie(name, value string) {
	b.cookies[name] = value
}

func cookieNamed(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// assertRedirect checks the response is a redirect whose Location path is
// the one given, and returns the parsed Location. The exact 3xx code is not
// a contract.
func assertRedirect(t *testing.T, res *http.Response, wantPath string) *url.URL {
	t.Helper()
	if res.StatusCode < 300 || res.StatusCode > 399 {
		t.Fatalf("status = %d, want a redirect to %s", res.StatusCode, wantPath)
	}
	loc := location(t, res)
	if loc.Path != wantPath {
		t.Fatalf("redirects to %q, want %s", loc.Path, wantPath)
	}
	return loc
}

func isClientError(status int) bool { return status >= 400 && status <= 499 }
func isServerError(status int) bool { return status >= 500 && status <= 599 }

// assertDeleted checks a cookie is deleted the way the spec fixes: the same
// attribute set with Max-Age=0, which Go parses as MaxAge -1.
func assertDeleted(t *testing.T, res *http.Response, name string) {
	t.Helper()
	c := cookieNamed(res, name)
	if c == nil {
		t.Fatalf("no %s cookie in the response, want a deletion", name)
	}
	if c.MaxAge != -1 {
		t.Errorf("%s: MaxAge = %d, want -1 (Max-Age=0)", name, c.MaxAge)
	}
	assertCookieShape(t, c)
}

func location(t *testing.T, res *http.Response) *url.URL {
	t.Helper()
	u, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatalf("Location %q: %v", res.Header.Get("Location"), err)
	}
	return u
}

func parseHTML(t *testing.T, res *http.Response) *html.Node {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	doc, err := html.Parse(res.Body)
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	return doc
}

// findElement walks the document for the first element with the tag, or with
// the attribute equal to the value when attr is set.
func findElement(n *html.Node, tag, attr, value string) *html.Node {
	if n.Type == html.ElementNode && (tag == "" || n.Data == tag) {
		if attr == "" {
			return n
		}
		for _, a := range n.Attr {
			if a.Key == attr && a.Val == value {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag, attr, value); found != nil {
			return found
		}
	}
	return nil
}

func attrValue(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

// mainCause returns the data-cause on <main>, and whether it is present.
func mainCause(t *testing.T, res *http.Response) (string, bool) {
	t.Helper()
	doc := parseHTML(t, res)
	m := findElement(doc, "main", "", "")
	if m == nil {
		t.Fatal("page has no <main>")
	}
	return attrValue(m, "data-cause")
}

// pinClock fixes the package clock at a start time and returns a function that
// moves it forward.
func pinClock(t *testing.T) func(time.Duration) {
	t.Helper()
	start := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	current := start
	prev := now
	now = func() time.Time { return current }
	t.Cleanup(func() { now = prev })
	return func(d time.Duration) { current = current.Add(d) }
}

func TestSigninPagePlainVisitHasNoCause(t *testing.T) {
	b := newBrowser(t, newTestPanel(t, newFakeForum(t)))

	res := b.get("/signin")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if cause, ok := mainCause(t, res); ok {
		t.Fatalf("plain visit carries data-cause=%q, want none", cause)
	}
}

func TestSigninStartRedirectsToForumWithFreshStateAndChallenge(t *testing.T) {
	f := newFakeForum(t)
	b := newBrowser(t, newTestPanel(t, f))

	res := b.post("/auth/start")

	loc := assertRedirect(t, res, "/oauth2/authorize")
	if got, want := loc.Scheme+"://"+loc.Host+loc.Path, f.srv.URL+"/oauth2/authorize"; got != want {
		t.Fatalf("redirect target = %q, want %q", got, want)
	}
	q := loc.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             testClientID,
		"redirect_uri":          testBaseURL + "/auth/callback",
		"code_challenge_method": "S256",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	scopes := strings.Fields(q.Get("scope"))
	for _, want := range []string{"user:read", "user:groups"} {
		found := false
		for _, s := range scopes {
			found = found || s == want
		}
		if !found {
			t.Errorf("scope %q lacks %q", q.Get("scope"), want)
		}
	}
	if q.Get("state") == "" {
		t.Error("state is empty")
	}
	if q.Get("code_challenge") == "" {
		t.Error("code_challenge is empty")
	}

	c := cookieNamed(res, "__Host-panel_signin")
	if c == nil {
		t.Fatal("no __Host-panel_signin cookie set")
	}
	assertCookieAttributes(t, c)
	if c.Value == "" {
		t.Error("signin cookie value is empty")
	}

	second := newBrowser(t, newTestPanel(t, f)).post("/auth/start")
	if location(t, second).Query().Get("state") == q.Get("state") {
		t.Error("two sign-in starts share one state")
	}
}

// assertCookieAttributes checks the attribute set the spec gives both panel
// cookies when set: Secure, HttpOnly, SameSite Lax, path /, no domain, no
// max-age.
func assertCookieAttributes(t *testing.T, c *http.Cookie) {
	t.Helper()
	assertCookieShape(t, c)
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Errorf("%s: MaxAge = %d, Expires = %v, want a session cookie", c.Name, c.MaxAge, c.Expires)
	}
}

// assertCookieShape checks the attributes a set and a deletion share.
func assertCookieShape(t *testing.T, c *http.Cookie) {
	t.Helper()
	if !c.Secure {
		t.Errorf("%s: not Secure", c.Name)
	}
	if !c.HttpOnly {
		t.Errorf("%s: not HttpOnly", c.Name)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("%s: SameSite = %v, want Lax", c.Name, c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("%s: Path = %q, want /", c.Name, c.Path)
	}
	if c.Domain != "" {
		t.Errorf("%s: Domain = %q, want none", c.Name, c.Domain)
	}
}

// signIn walks a browser through the start redirect and the callback, with
// the fake forum enforcing its rules, and returns the callback response.
func signIn(t *testing.T, f *fakeForum, b *browser) *http.Response {
	t.Helper()
	q := assertRedirect(t, b.post("/auth/start"), "/oauth2/authorize").Query()
	f.setChallenge(q.Get("code_challenge"))
	return b.get("/auth/callback?code=" + testCode + "&state=" + url.QueryEscape(q.Get("state")))
}

func TestCallbackWithMatchingStateSignsIn(t *testing.T) {
	f := newFakeForum(t)
	b := newBrowser(t, newTestPanel(t, f))

	res := signIn(t, f, b)

	assertRedirect(t, res, "/")
	sess := cookieNamed(res, sessionCookie)
	if sess == nil || sess.Value == "" {
		t.Fatalf("callback set no %s cookie with a value", sessionCookie)
	}
	assertCookieAttributes(t, sess)
	assertDeleted(t, res, signinCookie)

	home := b.get("/")
	if home.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", home.StatusCode)
	}
	name := findElement(parseHTML(t, home), "", "data-field", "username")
	if name == nil {
		t.Fatal("signed-in page has no element under data-field=username")
	}
	if got := textOf(name); got != testUsername {
		t.Errorf("username shown = %q, want %q", got, testUsername)
	}
}

func TestCallbackRefusedWithoutMatchingPendingSignin(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, f *fakeForum, b *browser) *http.Response
	}{
		{"state differs from the pending one", func(t *testing.T, f *fakeForum, b *browser) *http.Response {
			start := b.post("/auth/start")
			f.setChallenge(location(t, start).Query().Get("code_challenge"))
			return b.get("/auth/callback?code=" + testCode + "&state=not-the-one-issued")
		}},
		{"no sign-in cookie", func(t *testing.T, f *fakeForum, b *browser) *http.Response {
			return b.get("/auth/callback?code=" + testCode + "&state=some-state")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeForum(t)
			b := newBrowser(t, newTestPanel(t, f))

			res := tc.run(t, f, b)

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if c := cookieNamed(res, sessionCookie); c != nil && c.Value != "" {
				t.Errorf("refused callback set a session cookie %q", c.Value)
			}
			if n := f.tokenRequestCount(); n != 0 {
				t.Errorf("refused callback made %d token requests, want 0", n)
			}
		})
	}
}

func TestGroupCheckPassesOnAllowlistedGroup(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"primary group allowlisted, no secondary", userinfoJSON(71, nil)},
		{"secondary group allowlisted, primary not", userinfoJSON(2, []int{35, 47, 72})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeForum(t)
			f.setUserinfo(http.StatusOK, tc.body)
			b := newBrowser(t, newTestPanel(t, f))
			signIn(t, f, b)

			res := b.get("/")

			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET / status = %d, want 200", res.StatusCode)
			}
		})
	}
}

func TestGroupCheckEndsSessionWithCause(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		cause  string
	}{
		{"forum refuses the token", http.StatusUnauthorized, `{"errors":[{"code":"unauthorized"}]}`, "expired"},
		{"no allowlisted group", http.StatusOK, userinfoJSON(2, []int{35, 72}), "no-group"},
		{"forum answers 403", http.StatusForbidden, `{"errors":[{"code":"permission_denied"}]}`, "no-group"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeForum(t)
			b := newBrowser(t, newTestPanel(t, f))
			signIn(t, f, b)
			old := b.cookies[sessionCookie]
			f.setUserinfo(tc.status, tc.body)

			res := b.get("/")

			loc := assertRedirect(t, res, "/signin")
			page := b.get(loc.RequestURI())
			if cause, ok := mainCause(t, page); !ok || cause != tc.cause {
				t.Errorf("sign-in page data-cause = %q (present %v), want %q", cause, ok, tc.cause)
			}

			f.setUserinfo(http.StatusOK, userinfoJSON(2, []int{47}))
			b.setCookie(sessionCookie, old)
			assertRedirect(t, b.get("/"), "/signin")
		})
	}
}

func TestGroupCheckUnavailableKeepsSession(t *testing.T) {
	cases := []struct {
		name    string
		outage  func(*fakeForum)
		recover func(*fakeForum)
	}{
		{"forum answers 500",
			func(f *fakeForum) { f.setUserinfo(http.StatusInternalServerError, "boom") },
			func(f *fakeForum) { f.setUserinfo(http.StatusOK, userinfoJSON(2, []int{47})) }},
		{"forum drops the connection",
			func(f *fakeForum) { f.setUserinfoDrop(true) },
			func(f *fakeForum) { f.setUserinfoDrop(false) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeForum(t)
			b := newBrowser(t, newTestPanel(t, f))
			signIn(t, f, b)
			tc.outage(f)

			res := b.get("/")

			if !isServerError(res.StatusCode) {
				t.Errorf("GET / status = %d, want 5xx", res.StatusCode)
			}
			if c := cookieNamed(res, sessionCookie); c != nil {
				t.Errorf("outage response touched the session cookie: %+v", c)
			}

			tc.recover(f)
			again := b.get("/")
			if again.StatusCode != http.StatusOK {
				t.Errorf("after the outage GET / status = %d, want 200", again.StatusCode)
			}
		})
	}
}

func TestSignoutEndsSessionWithoutTheForum(t *testing.T) {
	f := newFakeForum(t)
	b := newBrowser(t, newTestPanel(t, f))
	signIn(t, f, b)
	old := b.cookies[sessionCookie]
	f.setUserinfoDrop(true)

	res := b.post("/auth/signout")

	assertRedirect(t, res, "/signin")
	assertDeleted(t, res, sessionCookie)

	f.setUserinfoDrop(false)
	b.setCookie(sessionCookie, old)
	assertRedirect(t, b.get("/"), "/signin")
}

func TestHomeWithoutSessionIsPlainSignin(t *testing.T) {
	b := newBrowser(t, newTestPanel(t, newFakeForum(t)))

	res := b.get("/")

	loc := assertRedirect(t, res, "/signin")
	if cause, ok := mainCause(t, b.get(loc.RequestURI())); ok {
		t.Errorf("plain visit carries data-cause=%q, want none", cause)
	}
}

func TestCrossOriginPostIsRefused(t *testing.T) {
	f := newFakeForum(t)
	b := newBrowser(t, newTestPanel(t, f))
	signIn(t, f, b)

	res := b.do(http.MethodPost, "/auth/signout", http.Header{
		"Content-Type":   {"application/x-www-form-urlencoded"},
		"Sec-Fetch-Site": {"cross-site"},
	}, nil)

	if !isClientError(res.StatusCode) {
		t.Errorf("cross-origin sign-out status = %d, want 4xx", res.StatusCode)
	}
	if again := b.get("/"); again.StatusCode != http.StatusOK {
		t.Errorf("session did not survive the refused request: GET / status = %d, want 200", again.StatusCode)
	}
}

func TestPendingSigninDroppedAfterFiveMinutes(t *testing.T) {
	advance := pinClock(t)
	f := newFakeForum(t)
	b := newBrowser(t, newTestPanel(t, f))
	start := b.post("/auth/start")
	q := location(t, start).Query()
	f.setChallenge(q.Get("code_challenge"))
	advance(5*time.Minute + time.Second)

	res := b.get("/auth/callback?code=" + testCode + "&state=" + url.QueryEscape(q.Get("state")))

	if !isClientError(res.StatusCode) {
		t.Errorf("late callback status = %d, want 4xx", res.StatusCode)
	}
	if n := f.tokenRequestCount(); n != 0 {
		t.Errorf("late callback made %d token requests, want 0", n)
	}
}

func TestSessionEndsTwoHoursAfterSignin(t *testing.T) {
	advance := pinClock(t)
	f := newFakeForum(t)
	b := newBrowser(t, newTestPanel(t, f))
	signIn(t, f, b)
	advance(2*time.Hour + time.Second)

	res := b.get("/")

	loc := assertRedirect(t, res, "/signin")
	if cause, ok := mainCause(t, b.get(loc.RequestURI())); !ok || cause != "expired" {
		t.Errorf("sign-in page data-cause = %q (present %v), want expired", cause, ok)
	}
}

func TestCallbackWithForumErrorReturnsToSignin(t *testing.T) {
	f := newFakeForum(t)
	b := newBrowser(t, newTestPanel(t, f))
	q := assertRedirect(t, b.post("/auth/start"), "/oauth2/authorize").Query()

	res := b.get("/auth/callback?error=access_denied&state=" + url.QueryEscape(q.Get("state")))

	loc := assertRedirect(t, res, "/signin")
	if cause, ok := mainCause(t, b.get(loc.RequestURI())); ok {
		t.Errorf("a denied consent carries data-cause=%q, want none", cause)
	}
	if c := cookieNamed(res, sessionCookie); c != nil && c.Value != "" {
		t.Errorf("denied consent set a session cookie %q", c.Value)
	}
	if n := f.tokenRequestCount(); n != 0 {
		t.Errorf("denied consent made %d token requests, want 0", n)
	}
}

func TestCallbackWithForumDownIsAnErrorWithNoSession(t *testing.T) {
	f := newFakeForum(t)
	b := newBrowser(t, newTestPanel(t, f))
	f.setTokenDrop(true)

	res := signIn(t, f, b)

	if !isServerError(res.StatusCode) {
		t.Errorf("callback status = %d, want 5xx", res.StatusCode)
	}
	if c := cookieNamed(res, sessionCookie); c != nil && c.Value != "" {
		t.Errorf("outage at sign-in set a session cookie %q", c.Value)
	}
	f.setTokenDrop(false)
	assertRedirect(t, b.get("/"), "/signin")
}
