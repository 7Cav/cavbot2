package panel

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	sessionCookie = "__Host-panel_session"
	signinCookie  = "__Host-panel_signin"

	// pendingLifetime is how long a sign-in may take between the redirect to
	// the forum and the callback. The forum's own authorization code lives
	// five minutes, so a longer wait would fail at the exchange anyway.
	pendingLifetime = 5 * time.Minute
	// sessionLifetime matches the forum access token, two hours from sign-in.
	// No refresh token is stored, so the session cannot outlive the token.
	sessionLifetime = 2 * time.Hour
	// pruneInterval is how often the timer drops what has expired. Expiry is
	// checked on every request as well, so the timer only bounds memory.
	pruneInterval = time.Minute
)

// pendingSignin is one sign-in between the redirect to the forum and the
// callback: the state the callback must echo and the PKCE verifier the
// exchange must send.
type pendingSignin struct {
	state    string
	verifier string
	started  time.Time
}

func (p pendingSignin) expired(at time.Time) bool {
	return !at.Before(p.started.Add(pendingLifetime))
}

// session is one signed-in browser. It holds the access token the group check
// sends on every request and the identity shown in the rail.
type session struct {
	accessToken string
	userID      int
	username    string
	signedIn    time.Time
}

func (s session) expired(at time.Time) bool {
	return !at.Before(s.signedIn.Add(sessionLifetime))
}

// page is the page data for a screen this session sees: the rail shows the
// navigation and the identity block.
func (s session) page(title string) pageData {
	return pageData{Title: title, SignedIn: true, Username: s.username}
}

// sessions is the in-memory store of pending sign-ins and sessions, each
// keyed by a random 128-bit ID that is the cookie's whole value. A restart
// empties it, which is the spec's way of ending every session.
type sessions struct {
	mu      sync.Mutex
	pending map[string]pendingSignin
	active  map[string]session
}

func newSessions() *sessions {
	return &sessions{pending: map[string]pendingSignin{}, active: map[string]session{}}
}

// newID is 128 bits from the system's random source, base64url without
// padding, so it is safe in a cookie value as it is.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *sessions) addPending(p pendingSignin) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[id] = p
	return id, nil
}

// takePending returns the pending sign-in under the ID and removes it, so a
// callback is answered once whatever its outcome. An expired one is reported
// as absent.
func (s *sessions) takePending(id string, at time.Time) (pendingSignin, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok {
		return pendingSignin{}, false
	}
	delete(s.pending, id)
	if p.expired(at) {
		return pendingSignin{}, false
	}
	return p, true
}

func (s *sessions) add(sess session) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[id] = sess
	return id, nil
}

func (s *sessions) get(id string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.active[id]
	return sess, ok
}

// update replaces a session's identity after a group check, so the rail shows
// the username the forum reports now.
func (s *sessions) update(id string, sess session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.active[id]; ok {
		s.active[id] = sess
	}
}

func (s *sessions) end(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, id)
}

// prune drops every pending sign-in and session that has expired at the time
// given.
func (s *sessions) prune(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.pending {
		if p.expired(at) {
			delete(s.pending, id)
		}
	}
	for id, sess := range s.active {
		if sess.expired(at) {
			delete(s.active, id)
		}
	}
}

// setCookie writes one of the two panel cookies with the attribute set the
// spec fixes: Secure, HttpOnly, SameSite Lax, path /, no domain, no max-age.
// The __Host- prefix makes a browser enforce the first four itself. Secure
// stays on for local runs; browsers treat localhost as a secure context.
func setCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, panelCookie(name, value))
}

// clearCookie repeats the attributes with Max-Age=0, which is what a browser
// needs to drop a __Host- cookie.
func clearCookie(w http.ResponseWriter, name string) {
	c := panelCookie(name, "")
	c.MaxAge = -1
	http.SetCookie(w, c)
}

func panelCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}
