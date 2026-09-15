package panel

import (
	"context"
	"net/http"

	"github.com/7cav/cavbot2/utils"
	"golang.org/x/oauth2"
)

// callbackPath is appended to BaseURL to form the redirect URI. It must match
// the URI registered on the forum's OAuth client character for character.
const callbackPath = "/auth/callback"

// newOAuthConfig builds the forum client. AuthStyleInParams puts the client
// credentials in the token request body, which is where Xenforo reads them;
// left at the default the library tries HTTP Basic first and fails once per
// process.
func newOAuthConfig(cfg Config) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.BaseURL + callbackPath,
		Scopes:       []string{"user:read", "user:groups"},
		Endpoint: oauth2.Endpoint{
			AuthURL:   cfg.AuthorizeURL,
			TokenURL:  cfg.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// Cookie names. The __Host- prefix makes the browser refuse them unless they
// are Secure, carry Path=/ and no Domain, which is what pins them to this
// origin. Secure stays on for local runs; browsers treat localhost as secure.
const (
	sessionCookie = "__Host-panel_session"
	signInCookie  = "__Host-panel_signin"
)

type contextKey int

const sessionKey contextKey = iota

// panelCookie builds one panel cookie with the attributes the __Host- prefix
// requires. maxAge 0 writes no Max-Age, so a set cookie lives for the
// browser session and the server-side entry's expiry is what ends it; a
// negative maxAge tells the browser to drop it.
func panelCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}

// setCookie writes a panel cookie holding an opaque key.
func setCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, panelCookie(name, value, 0))
}

// deleteCookie tells the browser to drop a panel cookie. The attributes
// repeat the set cookie's, since a __Host- deletion without them is ignored.
func deleteCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, panelCookie(name, "", -1))
}

// handleAuthStart creates a pending sign-in and sends the browser to the
// forum's consent page with a fresh state and PKCE challenge.
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	verifier := oauth2.GenerateVerifier()
	state := newID()
	id := s.pending.put(pendingSignIn{
		state:    state,
		verifier: verifier,
		expires:  panelNow().Add(pendingLifetime),
	})
	setCookie(w, signInCookie, id)
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusSeeOther)
}

// handleAuthCallback is the one GET that creates state, defended by the
// state value the pending sign-in holds. It exchanges the code, runs the
// group check on the new token, and only then creates the session.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(signInCookie)
	if err != nil {
		http.Error(w, "No sign-in in progress. Start again from the sign-in page.", http.StatusBadRequest)
		return
	}
	pending, _, live := s.pending.get(c.Value)
	if !live || pending.state == "" || r.URL.Query().Get("state") != pending.state {
		deleteCookie(w, signInCookie)
		http.Error(w, "This sign-in has expired or does not match. Start again from the sign-in page.", http.StatusBadRequest)
		return
	}
	// The pending sign-in is single use whatever happens next.
	s.pending.delete(c.Value)
	deleteCookie(w, signInCookie)

	if errCode := r.URL.Query().Get("error"); errCode != "" {
		utils.Warn("Panel sign-in refused by the forum", "error", errCode)
		http.Redirect(w, r, signInURL(""), http.StatusSeeOther)
		return
	}

	token, err := s.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(pending.verifier))
	if err != nil {
		utils.Warn("Panel token exchange failed", "error", err)
		http.Error(w, "The forum did not accept the sign-in. Try again.", http.StatusBadGateway)
		return
	}

	user, outcome := s.groupCheck(r.Context(), token.AccessToken)
	if outcome != groupPass {
		s.refuse(w, r, outcome, "")
		return
	}

	id := s.sessions.put(session{
		accessToken: token.AccessToken,
		userID:      user.UserID,
		username:    user.Username,
		expires:     panelNow().Add(sessionLifetime),
	})
	setCookie(w, sessionCookie, id)
	utils.Info("Panel sign-in", "forum_user_id", user.UserID, "forum_username", user.Username)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// sessionFrom returns the session requireSession stored on the request.
func sessionFrom(r *http.Request) session {
	s, _ := r.Context().Value(sessionKey).(session)
	return s
}

// requireSession is the middleware every signed-in page sits behind. No
// cookie or an unknown session sends the visitor to plain sign-in. A session
// past its lifetime ends as expired. Then the group check runs against the
// forum: a refused token or a lost group ends the session with its cause,
// and a forum outage keeps the session and shows the error page.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Redirect(w, r, signInURL(""), http.StatusSeeOther)
			return
		}
		sess, found, live := s.sessions.get(c.Value)
		if !found {
			http.Redirect(w, r, signInURL(""), http.StatusSeeOther)
			return
		}
		if !live {
			s.endSession(w, c.Value)
			http.Redirect(w, r, signInURL(causeExpired), http.StatusSeeOther)
			return
		}

		user, outcome := s.groupCheck(r.Context(), sess.accessToken)
		if outcome != groupPass {
			if outcome != groupUnavailable {
				s.endSession(w, c.Value)
				utils.Info("Panel session ended", "forum_user_id", sess.userID, "cause", outcome.cause())
			}
			s.refuse(w, r, outcome, sess.username)
			return
		}
		// The forum's current username wins over the one stored at sign-in.
		sess.username = user.Username
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

// refuse answers a failed group check: the error page for a forum outage,
// otherwise a redirect to sign-in carrying the cause. Ending the session,
// where one exists, is the caller's.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, outcome groupOutcome, username string) {
	if outcome == groupUnavailable {
		s.render(w, http.StatusBadGateway, "error.html", page{Username: username})
		return
	}
	http.Redirect(w, r, signInURL(outcome.cause()), http.StatusSeeOther)
}

// handleSignOut ends the panel session and nothing else: no call to the
// forum's revoke endpoint. It needs no group check; a session the forum has
// already refused can still be signed out.
func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.endSession(w, c.Value)
	}
	http.Redirect(w, r, signInURL(""), http.StatusSeeOther)
}

// endSession drops the server-side session and tells the browser to drop
// the cookie.
func (s *Server) endSession(w http.ResponseWriter, id string) {
	s.sessions.delete(id)
	deleteCookie(w, sessionCookie)
}
