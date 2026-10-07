//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

const testRedirectURI = "http://localhost:8080/auth/callback"

// forumUnderTest is the fake forum behind a test server, and the OAuth
// client the panel builds against it: same auth style, scopes and PKCE.
type forumUnderTest struct {
	t      *testing.T
	srv    *httptest.Server
	oauth  *oauth2.Config
	client *http.Client
}

func newForumUnderTest(t *testing.T) *forumUnderTest {
	t.Helper()
	srv := httptest.NewServer(newFakeForum(testRedirectURI, t.Logf).handler())
	t.Cleanup(srv.Close)
	return &forumUnderTest{
		t:   t,
		srv: srv,
		oauth: &oauth2.Config{
			ClientID:     fakeClientID,
			ClientSecret: fakeClientSecret,
			Endpoint:     oauth2.Endpoint{AuthURL: srv.URL + "/oauth2/authorize", TokenURL: srv.URL + "/api/oauth2/token", AuthStyle: oauth2.AuthStyleInParams},
			RedirectURL:  testRedirectURI,
			Scopes:       []string{"user:read", "user:groups"},
		},
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

// consent opens the consent page the panel redirects to, submits it with
// choice (persona=<key> or deny=1), and returns where the forum sends the
// browser back.
func (f *forumUnderTest) consent(verifier, choice string) *url.URL {
	f.t.Helper()
	authURL, err := url.Parse(f.oauth.AuthCodeURL("the-state", oauth2.S256ChallengeOption(verifier)))
	if err != nil {
		f.t.Fatal(err)
	}
	page, err := f.client.Get(authURL.String())
	if err != nil {
		f.t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	if page.StatusCode != http.StatusOK {
		f.t.Fatalf("consent page answered %d: %s", page.StatusCode, body)
	}
	form := authURL.Query()
	name, value, _ := strings.Cut(choice, "=")
	form.Set(name, value)
	res, err := f.client.PostForm(f.srv.URL+"/oauth2/authorize", form)
	if err != nil {
		f.t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		f.t.Fatalf("consent answered %d, want a redirect", res.StatusCode)
	}
	back, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		f.t.Fatal(err)
	}
	return back
}

// signIn runs the panel's whole sign-in as the persona and returns the
// access token.
func (f *forumUnderTest) signIn(key string) string {
	f.t.Helper()
	verifier := oauth2.GenerateVerifier()
	back := f.consent(verifier, "persona="+key)
	tok, err := f.oauth.Exchange(context.Background(), back.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		f.t.Fatalf("exchange: %v", err)
	}
	return tok.AccessToken
}

// me is the /api/me call the panel's group check makes.
func (f *forumUnderTest) me(token string) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return http.DefaultClient.Do(req)
}

func (f *forumUnderTest) setMode(mode string) {
	f.t.Helper()
	res, err := http.PostForm(f.srv.URL+"/smoke/mode", url.Values{"mode": {mode}})
	if err != nil {
		f.t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("set mode %s answered %d", mode, res.StatusCode)
	}
}

type meBody struct {
	Me struct {
		UserID    int    `json:"user_id"`
		Username  string `json:"username"`
		Primary   int    `json:"user_group_id"`
		Secondary []int  `json:"secondary_group_ids"`
	} `json:"me"`
}

// Each persona must come out of the bot's group check as its label says,
// given the group IDs a fake-forum run hands the bot.
func TestFakeForumSignsInEachPersonaWithTheAccessTheBotIsConfiguredFor(t *testing.T) {
	overrides := smokeOverrides(forumFake, pgDSN)
	adminGroup, _ := strconv.Atoi(overrides["PANEL_GROUP_IDS"])
	foxholeGroup, _ := strconv.Atoi(overrides["FOXHOLE_GROUP_ID"])
	in := func(groups []int, id int) bool { return slices.Contains(groups, id) }

	tests := []struct {
		key                    string
		wantAdmin, wantFoxhole bool
	}{
		{key: "admin", wantAdmin: true},
		{key: "foxhole", wantFoxhole: true},
		{key: "member"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			f := newForumUnderTest(t)
			res, err := f.me(f.signIn(tt.key))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("/api/me answered %d", res.StatusCode)
			}
			var body meBody
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			groups := append([]int{body.Me.Primary}, body.Me.Secondary...)
			if got := in(groups, adminGroup); got != tt.wantAdmin {
				t.Errorf("panel admin = %v, want %v (groups %v)", got, tt.wantAdmin, groups)
			}
			if got := in(groups, foxholeGroup); got != tt.wantFoxhole {
				t.Errorf("Foxhole manager = %v, want %v (groups %v)", got, tt.wantFoxhole, groups)
			}
			if body.Me.UserID == 0 || body.Me.Username == "" {
				t.Errorf("/api/me named no user: %+v", body.Me)
			}
		})
	}
}

// The panel's callback base is the forum client's registered local redirect
// URI, so the fake refuses any other, as the forum does.
func TestFakeForumRefusesARedirectURIOtherThanTheRegisteredOne(t *testing.T) {
	f := newForumUnderTest(t)
	f.oauth.RedirectURL = "http://localhost/auth/callback"
	res, err := f.client.Get(f.oauth.AuthCodeURL("the-state", oauth2.S256ChallengeOption(oauth2.GenerateVerifier())))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("authorize answered %d, want 400", res.StatusCode)
	}
}

func TestFakeForumDenySendsAccessDeniedBackWithTheState(t *testing.T) {
	f := newForumUnderTest(t)
	q := f.consent(oauth2.GenerateVerifier(), "deny=1").Query()
	if q.Get("error") != "access_denied" || q.Get("state") != "the-state" || q.Get("code") != "" {
		t.Fatalf("deny came back with %v", q)
	}
}

func TestFakeForumCodeNeedsTheVerifierAndWorksOnce(t *testing.T) {
	f := newForumUnderTest(t)
	verifier := oauth2.GenerateVerifier()
	code := f.consent(verifier, "persona=admin").Query().Get("code")

	_, err := f.oauth.Exchange(context.Background(), code, oauth2.VerifierOption(oauth2.GenerateVerifier()))
	var retrieve *oauth2.RetrieveError
	if !errors.As(err, &retrieve) || retrieve.Response.StatusCode != http.StatusBadRequest {
		t.Fatalf("exchange with the wrong verifier: %v, want a 400", err)
	}

	code = f.consent(verifier, "persona=admin").Query().Get("code")
	if _, err := f.oauth.Exchange(context.Background(), code, oauth2.VerifierOption(verifier)); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if _, err := f.oauth.Exchange(context.Background(), code, oauth2.VerifierOption(verifier)); !errors.As(err, &retrieve) {
		t.Fatalf("second exchange of one code: %v, want a refusal", err)
	}
}

// The modes are the forum states behind the panel's expired, refused and
// forum-unavailable pages: a 401, a 403, and no answer at all.
func TestFakeForumModesAnswerAsTheForumStatesThePanelHandles(t *testing.T) {
	f := newForumUnderTest(t)
	token := f.signIn("admin")
	for _, tt := range []struct {
		mode       string
		wantStatus int
	}{
		{modeExpired, http.StatusUnauthorized},
		{modeRefused, http.StatusForbidden},
		{modeOK, http.StatusOK},
	} {
		f.setMode(tt.mode)
		res, err := f.me(token)
		if err != nil {
			t.Fatalf("%s: %v", tt.mode, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != tt.wantStatus {
			t.Errorf("%s: /api/me answered %d, want %d", tt.mode, res.StatusCode, tt.wantStatus)
		}
	}

	f.setMode(modeDown)
	if res, err := f.me(token); err == nil {
		_ = res.Body.Close()
		t.Errorf("down: /api/me answered %d, want no answer", res.StatusCode)
	}
	verifier := oauth2.GenerateVerifier()
	code := f.consent(verifier, "persona=admin").Query().Get("code")
	_, err := f.oauth.Exchange(context.Background(), code, oauth2.VerifierOption(verifier))
	var retrieve *oauth2.RetrieveError
	if err == nil || errors.As(err, &retrieve) {
		t.Errorf("down: exchange gave %v, want a transport error", err)
	}
}
