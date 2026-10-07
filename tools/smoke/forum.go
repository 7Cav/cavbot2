//go:build unix

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// The forum groups the personas belong to. adminGroupID and foxholeGroupID
// are the production values of PANEL_GROUP_IDS (its first entry) and
// FOXHOLE_GROUP_ID. In fake mode up sets the bot to exactly these, so each
// persona means the same thing to the bot whatever the env file holds.
const (
	registeredGroupID = 2
	adminGroupID      = 71
	foxholeGroupID    = 323
)

// The OAuth client the fake forum accepts. It guards nothing: the fake
// listens on loopback only and exists for local smoke tests.
const (
	fakeClientID     = "cavbot2-smoke"
	fakeClientSecret = "cavbot2-smoke-secret"
)

// codeLifetime is how long a code from the consent page stays exchangeable.
const codeLifetime = 10 * time.Minute

// persona is a forum user the fake signs in, one per kind of panel access.
type persona struct {
	Key       string
	Label     string
	UserID    int
	Username  string
	Primary   int
	Secondary []int
}

var personas = []persona{
	{Key: "admin", Label: "Panel admin", UserID: 900001, Username: "Smoke Admin", Primary: registeredGroupID, Secondary: []int{adminGroupID}},
	{Key: "foxhole", Label: "Foxhole manager", UserID: 900002, Username: "Smoke Foxhole", Primary: registeredGroupID, Secondary: []int{foxholeGroupID}},
	{Key: "member", Label: "No access", UserID: 900003, Username: "Smoke Member", Primary: registeredGroupID, Secondary: []int{}},
}

// Groups is the persona's groups as the consent page shows them.
func (p persona) Groups() string {
	ids := []string{fmt.Sprint(p.Primary)}
	for _, id := range p.Secondary {
		ids = append(ids, fmt.Sprint(id))
	}
	return strings.Join(ids, ", ")
}

// The answers the fake can give, set with `smoke forum <mode>`. ok signs
// people in; the others are the forum states the panel turns into its
// expired, refused and forum-unavailable pages.
const (
	modeOK      = "ok"
	modeExpired = "expired" // /api/me answers 401
	modeRefused = "refused" // /api/me answers 403
	modeDown    = "down"    // the token endpoint and /api/me close the connection unanswered
)

var forumModes = []string{modeOK, modeExpired, modeRefused, modeDown}

type grant struct {
	persona     persona
	challenge   string
	redirectURI string
	issued      time.Time
}

// fakeForum is the three XenForo OAuth2 endpoints the panel calls, with a
// consent page that offers the personas instead of a password. It checks
// what the real forum checks: the client, the redirect URI character for
// character, a single-use code and the PKCE verifier.
type fakeForum struct {
	redirectURI string
	logf        func(format string, args ...any)

	mu     sync.Mutex
	mode   string
	codes  map[string]grant
	tokens map[string]persona
}

func newFakeForum(redirectURI string, logf func(string, ...any)) *fakeForum {
	return &fakeForum{
		redirectURI: redirectURI,
		logf:        logf,
		mode:        modeOK,
		codes:       map[string]grant{},
		tokens:      map[string]persona{},
	}
}

func (f *fakeForum) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth2/authorize", f.consentPage)
	mux.HandleFunc("POST /oauth2/authorize", f.consent)
	mux.HandleFunc("POST /api/oauth2/token", f.token)
	mux.HandleFunc("GET /api/me", f.me)
	mux.HandleFunc("GET /smoke/mode", f.getMode)
	mux.HandleFunc("POST /smoke/mode", f.setMode)
	return mux
}

var consentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Fake forum sign-in</title>
<style>
body { font-family: system-ui, sans-serif; max-width: 32rem; margin: 3rem auto; padding: 0 1rem; line-height: 1.4; }
button { display: block; width: 100%; margin: .5rem 0; padding: .75rem; font-size: 1rem; text-align: left; cursor: pointer; }
small { color: #555; }
</style>
</head>
<body>
<h1>Fake forum</h1>
<p>Local smoke-test sign-in for the cavbot2 panel. Pick who to sign in as.</p>
<form method="post" action="/oauth2/authorize">
{{range $name, $value := .Hidden}}<input type="hidden" name="{{$name}}" value="{{$value}}">
{{end}}{{range .Personas}}<button type="submit" name="persona" value="{{.Key}}">{{.Label}}: {{.Username}} <small>(groups {{.Groups}})</small></button>
{{end}}<button type="submit" name="deny" value="1">Deny</button>
</form>
</body>
</html>
`))

// authorizeParams are the authorize request's parameters the consent page
// carries through its form.
var authorizeParams = []string{"response_type", "client_id", "redirect_uri", "state", "scope", "code_challenge", "code_challenge_method"}

// checkAuthorize refuses an authorize request the real forum would refuse,
// so a wrong PANEL_BASE_URL shows up here as it would against the forum.
func (f *fakeForum) checkAuthorize(v url.Values) error {
	switch {
	case v.Get("response_type") != "code":
		return fmt.Errorf("response_type must be code")
	case v.Get("client_id") != fakeClientID:
		return fmt.Errorf("unknown client_id")
	case v.Get("redirect_uri") != f.redirectURI:
		return fmt.Errorf("the redirect URI %q does not match the registered %q", v.Get("redirect_uri"), f.redirectURI)
	case v.Get("state") == "":
		return fmt.Errorf("no state")
	case v.Get("code_challenge") != "" && v.Get("code_challenge_method") != "S256":
		return fmt.Errorf("code_challenge_method must be S256")
	}
	return nil
}

func (f *fakeForum) consentPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := f.checkAuthorize(q); err != nil {
		http.Error(w, "fake forum: "+err.Error(), http.StatusBadRequest)
		return
	}
	hidden := map[string]string{}
	for _, name := range authorizeParams {
		hidden[name] = q.Get(name)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = consentTemplate.Execute(w, map[string]any{"Hidden": hidden, "Personas": personas})
}

func (f *fakeForum) consent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "fake forum: "+err.Error(), http.StatusBadRequest)
		return
	}
	form := r.PostForm
	if err := f.checkAuthorize(form); err != nil {
		http.Error(w, "fake forum: "+err.Error(), http.StatusBadRequest)
		return
	}
	back := url.Values{"state": {form.Get("state")}}
	if form.Get("deny") != "" {
		f.logf("fake forum: consent denied")
		back.Set("error", "access_denied")
		http.Redirect(w, r, f.redirectURI+"?"+back.Encode(), http.StatusSeeOther)
		return
	}
	i := slices.IndexFunc(personas, func(p persona) bool { return p.Key == form.Get("persona") })
	if i < 0 {
		http.Error(w, "fake forum: unknown persona", http.StatusBadRequest)
		return
	}
	code := randomID()
	f.mu.Lock()
	f.codes[code] = grant{persona: personas[i], challenge: form.Get("code_challenge"), redirectURI: form.Get("redirect_uri"), issued: time.Now()}
	f.mu.Unlock()
	f.logf("fake forum: consent given as %s (%s)", personas[i].Username, personas[i].Label)
	back.Set("code", code)
	http.Redirect(w, r, f.redirectURI+"?"+back.Encode(), http.StatusSeeOther)
}

func (f *fakeForum) token(w http.ResponseWriter, r *http.Request) {
	if f.currentMode() == modeDown {
		dropConnection(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	form := r.PostForm
	if form.Get("client_id") != fakeClientID || form.Get("client_secret") != fakeClientSecret {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	if form.Get("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	f.mu.Lock()
	g, ok := f.codes[form.Get("code")]
	delete(f.codes, form.Get("code"))
	f.mu.Unlock()
	switch {
	case !ok, time.Since(g.issued) > codeLifetime, form.Get("redirect_uri") != g.redirectURI:
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	case g.challenge != "" && pkceChallenge(form.Get("code_verifier")) != g.challenge:
		oauthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	tok := randomID()
	f.mu.Lock()
	f.tokens[tok] = g.persona
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": tok,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"scope":        "user:read user:groups",
	})
}

func (f *fakeForum) me(w http.ResponseWriter, r *http.Request) {
	switch f.currentMode() {
	case modeDown:
		dropConnection(w)
		return
	case modeExpired:
		oauthError(w, http.StatusUnauthorized, "invalid_token")
		return
	case modeRefused:
		oauthError(w, http.StatusForbidden, "insufficient_scope")
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	p, known := f.tokens[tok]
	f.mu.Unlock()
	if !ok || !known {
		oauthError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"me": map[string]any{
		"user_id":             p.UserID,
		"username":            p.Username,
		"user_group_id":       p.Primary,
		"secondary_group_ids": p.Secondary,
	}})
}

func (f *fakeForum) currentMode() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode
}

func (f *fakeForum) getMode(w http.ResponseWriter, _ *http.Request) {
	_, _ = fmt.Fprintln(w, f.currentMode())
}

func (f *fakeForum) setMode(w http.ResponseWriter, r *http.Request) {
	mode := r.FormValue("mode")
	if !slices.Contains(forumModes, mode) {
		http.Error(w, fmt.Sprintf("mode must be one of %s", strings.Join(forumModes, ", ")), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.mode = mode
	f.mu.Unlock()
	f.logf("fake forum: mode set to %s", mode)
	_, _ = fmt.Fprintln(w, mode)
}

// dropConnection closes the connection with no answer, the transport error a
// forum outage gives the panel.
func dropConnection(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "forum down", http.StatusServiceUnavailable)
		return
	}
	conn, _, err := hj.Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
