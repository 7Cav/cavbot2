// Package panel is the bot's web UI, an HTTP server inside the cavbot2 binary
// signed in through the forum's OAuth2; the decision and its reasons are in
// docs/temp-vc-decisions.md. It holds the sign-in, the panel session, the
// group check, and the hub page: the hub list with each hub's live spawned
// count, and the register form. Editing, removal and the change log arrive
// with later tickets.
package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"golang.org/x/oauth2"
)

// now is the package clock for the pending sign-in and session lifetimes. A
// package var so tests can move time without sleeping, the same arrangement
// as telemetryNow in the commands package.
var now = time.Now

// Panel holds the configuration, the parsed pages, the in-memory sessions and
// the service layer the hub page acts through.
type Panel struct {
	cfg     Config
	version string
	hubs    *hubService
	// forumURL is the forum's origin, derived from the authorize URL, for the
	// "Back to the forum" link and the wordmark. No extra variable to keep in
	// parity for a link.
	forumURL string
	pages    pages
	oauth    *oauth2.Config
	sessions *sessions
	// client makes every call to the forum, the token exchange included. A
	// timeout so a forum that accepts the connection and never answers is a
	// checkUnavailable and not a request that hangs a panel user.
	client *http.Client

	server    *http.Server
	stopPrune chan struct{}
}

// forumTimeout bounds one call to the forum.
const forumTimeout = 10 * time.Second

// New builds a panel from a config and what the hub page acts through. It
// parses the templates once, so a broken template fails here at startup and
// never at a request. Every field of deps is required: the hub page reads the
// store, the guild and the runtime on every load.
func New(cfg Config, version string, deps Deps) (*Panel, error) {
	if deps.Store == nil || deps.Runtime == nil || deps.Manager == nil || deps.GuildID == "" {
		return nil, fmt.Errorf("panel needs a store, a runtime, a manager and a guild ID")
	}
	pg, err := parsePages()
	if err != nil {
		return nil, err
	}
	forumURL, err := originOf(cfg.AuthorizeURL)
	if err != nil {
		return nil, fmt.Errorf("PANEL_OAUTH_AUTHORIZE_URL: %w", err)
	}
	return &Panel{
		cfg:      cfg,
		version:  version,
		hubs:     &hubService{deps: deps},
		forumURL: forumURL,
		pages:    pg,
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			// The forum reads the client credentials from the POST body and
			// treats HTTP Basic as a guest, so the style is fixed here rather
			// than left to the library's probe.
			Endpoint:    oauth2.Endpoint{AuthURL: cfg.AuthorizeURL, TokenURL: cfg.TokenURL, AuthStyle: oauth2.AuthStyleInParams},
			RedirectURL: cfg.BaseURL + "/auth/callback",
			Scopes:      []string{"user:read", "user:groups"},
		},
		sessions: newSessions(),
		client:   &http.Client{Timeout: forumTimeout},
	}, nil
}

// Start binds the listen address and serves in the background. The bind is
// synchronous so a port already in use fails here, at startup, and not in a
// goroutine nobody reads. It also starts the timer that prunes expired
// sessions and pending sign-ins.
func (p *Panel) Start() error {
	ln, err := net.Listen("tcp", p.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", p.cfg.Addr, err)
	}
	p.server = &http.Server{
		Handler:           p.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// net/http's own lines (a bad request line, a closed connection)
		// go through slog at WARN like everything else the bot logs.
		ErrorLog: slog.NewLogLogger(utils.Logger.Handler(), slog.LevelWarn),
	}
	p.stopPrune = make(chan struct{})
	go p.pruneLoop()
	go func() {
		defer utils.RecoverPanic("panel-serve")
		if err := p.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			utils.CaptureError("Panel server stopped", err)
		}
	}()
	utils.Info("Panel listening", "addr", ln.Addr().String(), "base_url", p.cfg.BaseURL, "group_ids", p.cfg.GroupIDs)
	return nil
}

// Stop closes the listener and waits for in-flight requests until ctx ends.
func (p *Panel) Stop(ctx context.Context) error {
	if p.server == nil {
		return nil
	}
	close(p.stopPrune)
	return p.server.Shutdown(ctx)
}

func (p *Panel) pruneLoop() {
	defer utils.RecoverPanic("panel-prune")
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.sessions.prune(now())
		case <-p.stopPrune:
			return
		}
	}
}

func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%q has no scheme or host", raw)
	}
	return u.Scheme + "://" + u.Host + "/", nil
}

// Handler is the panel's routes behind Go's cross-origin protection, which
// refuses a state-changing request a browser sends from another origin. The
// panel therefore carries no form token, and every state change is a POST.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /signin", p.signinPage)
	mux.HandleFunc("POST /auth/start", p.authStart)
	mux.HandleFunc("GET /auth/callback", p.authCallback)
	mux.HandleFunc("POST /auth/signout", p.authSignout)
	mux.HandleFunc("GET /{$}", p.withSession(p.homePage))
	mux.HandleFunc("POST /hubs", p.withSession(p.registerHub))
	mux.HandleFunc("POST /hubs/{id}", p.withSession(p.updateHub))
	mux.HandleFunc("POST /hubs/{id}/remove", p.withSession(p.removeHub))
	protected := http.NewCrossOriginProtection().Handler(mux)
	// A panic in a handler is recovered here, through the same path every
	// other goroutine uses (ADR 0001), before net/http's own recovery would
	// print it with the remote address through the stdlib logger.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer utils.RecoverPanic("panel-request", "path", r.URL.Path)
		protected.ServeHTTP(w, r)
	})
}

func (p *Panel) signinPage(w http.ResponseWriter, r *http.Request) {
	p.render(w, http.StatusOK, "signin", pageData{
		Title: "Sign in",
		Cause: causeFromQuery(r.URL.Query().Get("cause")),
	})
}

// authStart begins a sign-in: a fresh state and PKCE verifier, kept under a
// pending ID the sign-in cookie carries, and a redirect to the forum's consent
// page. The confidential client sends PKCE S256 anyway; the forum verifies it
// when present.
func (p *Panel) authStart(w http.ResponseWriter, r *http.Request) {
	state, err := newID()
	if err != nil {
		p.serverError(w, "sign-in start", err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	id, err := p.sessions.addPending(pendingSignin{state: state, verifier: verifier, started: now()})
	if err != nil {
		p.serverError(w, "sign-in start", err)
		return
	}
	setCookie(w, signinCookie, id)
	http.Redirect(w, r, p.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusSeeOther)
}

// serverError is a failure inside the panel itself, not a forum answer: the
// random source or a template. It captures to Sentry (ADR 0001) and answers
// a bare 500.
func (p *Panel) serverError(w http.ResponseWriter, step string, err error) {
	utils.CaptureError("Panel request failed", err, "step", step)
	http.Error(w, "the panel could not complete the request", http.StatusInternalServerError)
}

// authCallback is the one GET that creates state, defended by the state value
// the pending sign-in holds. The pending sign-in is consumed whatever the
// outcome, so a callback is answered once.
func (p *Panel) authCallback(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, signinCookie)
	c, err := r.Cookie(signinCookie)
	if err != nil {
		http.Error(w, "no sign-in in progress", http.StatusBadRequest)
		return
	}
	pending, ok := p.sessions.takePending(c.Value, now())
	if !ok {
		http.Error(w, "the sign-in took too long or was not started here", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	if q.Get("state") == "" || q.Get("state") != pending.state {
		utils.Warn("Panel sign-in refused: state mismatch")
		http.Error(w, "the sign-in did not come back the way it left", http.StatusBadRequest)
		return
	}
	if q.Get("error") != "" {
		// The user pressed Deny on the forum's consent page, or the forum
		// refused the request. Back to the plain sign-in page.
		utils.Info("Panel sign-in not completed", "forum_error", q.Get("error"))
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	code := q.Get("code")
	if code == "" {
		http.Error(w, "the forum sent no code", http.StatusBadRequest)
		return
	}

	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, p.client)
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(pending.verifier))
	if err != nil {
		// A 4xx from the token endpoint is the forum refusing this sign-in
		// (a stale code, a wrong secret): back to the sign-in page. Anything
		// else is the forum not answering.
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) && retrieve.Response.StatusCode < 500 {
			utils.Warn("Panel sign-in refused by the forum", "status", retrieve.Response.StatusCode, "body", string(retrieve.Body))
			http.Redirect(w, r, signinURL(causeNone), http.StatusSeeOther)
			return
		}
		utils.Warn("Panel token exchange failed", "error", err)
		p.forumUnavailable(w, nil)
		return
	}

	user, outcome, err := p.groupCheck(r.Context(), tok.AccessToken)
	switch outcome {
	case checkPassed:
		id, err := p.sessions.add(session{accessToken: tok.AccessToken, userID: user.UserID, username: user.Username, signedIn: now()})
		if err != nil {
			p.serverError(w, "session create", err)
			return
		}
		utils.Info("Panel sign-in", "username", user.Username, "forum_user_id", user.UserID)
		setCookie(w, sessionCookie, id)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case checkUnavailable:
		utils.Warn("Panel group check unavailable at sign-in", "error", err)
		p.forumUnavailable(w, nil)
	default:
		utils.Info("Panel sign-in refused", "cause", string(outcome.cause()), "username", user.Username, "forum_user_id", user.UserID)
		http.Redirect(w, r, signinURL(outcome.cause()), http.StatusSeeOther)
	}
}

// forumUnavailable renders the error page with the 502 the forum outage
// earns. With a session the page keeps the rail's identity and says the
// session is kept; without one, at sign-in, it says only to try again.
func (p *Panel) forumUnavailable(w http.ResponseWriter, sess *session) {
	data := pageData{Title: "The forum did not answer"}
	if sess != nil {
		data = sess.page(data.Title)
	}
	p.render(w, http.StatusBadGateway, "error", data)
}

// signinURL is the sign-in page with the cause of the ended session, which
// the page renders as the data-cause attribute and one sentence.
func signinURL(c cause) string {
	if c == causeNone {
		return "/signin"
	}
	return "/signin?cause=" + url.QueryEscape(string(c))
}

// withSession is the gate every signed-in page sits behind: the session
// cookie must name a live session, and the group check must pass on this
// request. Expiry by the clock ends the session before the forum is asked.
func (p *Panel) withSession(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Redirect(w, r, signinURL(causeNone), http.StatusSeeOther)
			return
		}
		sess, ok := p.sessions.get(c.Value)
		if !ok {
			clearCookie(w, sessionCookie)
			http.Redirect(w, r, signinURL(causeNone), http.StatusSeeOther)
			return
		}
		if sess.expired(now()) {
			p.endSession(w, c.Value, sess, string(causeExpired))
			http.Redirect(w, r, signinURL(causeExpired), http.StatusSeeOther)
			return
		}
		user, outcome, err := p.groupCheck(r.Context(), sess.accessToken)
		switch outcome {
		case checkPassed:
			sess.userID, sess.username = user.UserID, user.Username
			p.sessions.update(c.Value, sess)
			next(w, r, sess)
		case checkUnavailable:
			utils.Warn("Panel group check unavailable, session kept", "username", sess.username, "error", err)
			p.forumUnavailable(w, &sess)
		default:
			p.endSession(w, c.Value, sess, string(outcome.cause()))
			http.Redirect(w, r, signinURL(outcome.cause()), http.StatusSeeOther)
		}
	}
}

// authSignout ends the session the cookie names and nothing else: no group
// check first, so a user the forum no longer admits can still sign out, and
// no revoke call to the forum. A cookie that names no session still gets a
// clear and the redirect.
func (p *Panel) authSignout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if sess, ok := p.sessions.get(c.Value); ok {
			p.endSession(w, c.Value, sess, "sign-out")
		}
	}
	clearCookie(w, sessionCookie)
	http.Redirect(w, r, signinURL(causeNone), http.StatusSeeOther)
}

// endSession drops the session and its cookie and records why.
func (p *Panel) endSession(w http.ResponseWriter, id string, sess session, reason string) {
	p.sessions.end(id)
	clearCookie(w, sessionCookie)
	utils.Info("Panel session ended", "reason", reason, "username", sess.username, "forum_user_id", sess.userID)
}

// homePage is the hub page: the list, and the register form or, with a hub
// named in the query, that hub's edit form.
func (p *Panel) homePage(w http.ResponseWriter, r *http.Request, sess session) {
	var edit *editPage
	if raw := r.URL.Query().Get("hub"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			http.NotFound(w, r)
			return
		}
		edit, err = p.hubs.form(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			p.serverError(w, "hub form", err)
			return
		}
	}
	p.renderHubs(w, r, sess, http.StatusOK, hubPage{Edit: edit})
}

// renderHubs renders the hub page with the list read now and the forms as
// the caller set them: the register form as posted, or one hub's edit form,
// with the refusal when there is one.
func (p *Panel) renderHubs(w http.ResponseWriter, r *http.Request, sess session, status int, forms hubPage) {
	page, err := p.hubs.list(r.Context())
	if err != nil {
		p.serverError(w, "hub list", err)
		return
	}
	page.Form, page.Error, page.Edit = forms.Form, forms.Error, forms.Edit
	data := sess.page("Hubs")
	data.Hubs = page
	p.render(w, status, "home", data)
}

// registerHub is POST /hubs: one service call, then a redirect to the list
// where the new hub appears, or the page again with the refused field named.
func (p *Panel) registerHub(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := registerInput{ChannelID: r.PostForm.Get(fieldHubChannel), BaseString: r.PostForm.Get(fieldBaseString)}
	hub, err := p.hubs.register(r.Context(), in)
	if refusal, ok := asFieldError(err); ok {
		p.renderHubs(w, r, sess, http.StatusUnprocessableEntity, hubPage{Form: in, Error: refusal})
		return
	}
	if err != nil {
		p.serverError(w, "hub register", err)
		return
	}
	utils.Info("Panel hub registered", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID,
		"base_string", hub.BaseString, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// hubIDOf reads the hub ID from the route. A value that is not an ID names
// no hub, and the caller answers 404.
func hubIDOf(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// updateHub is POST /hubs/{id}: one service call, then a redirect to the
// hub's form where the saved values show, or the form again with the refused
// field named.
func (p *Panel) updateHub(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := hubIDOf(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := editInput{
		BaseString:       r.PostForm.Get(fieldBaseString),
		PermissionSource: r.PostForm.Get(fieldPermissionSource),
		ModeratorRoleIDs: r.PostForm[fieldModeratorRoles],
		UserLimit:        r.PostForm.Get(fieldUserLimit),
		Bitrate:          r.PostForm.Get(fieldBitrate),
		Enabled:          r.PostForm.Get(fieldEnabled) != "",
	}
	hub, err := p.hubs.update(r.Context(), id, in, actor{userID: sess.userID, username: sess.username})
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if refusal, ok := asFieldError(err); ok {
		// The form again, as posted, so nothing typed is lost. The fixed
		// values and the roles are read again; the store was not written.
		edit, err := p.hubs.form(r.Context(), id)
		if err != nil {
			p.serverError(w, "hub form", err)
			return
		}
		p.renderHubs(w, r, sess, http.StatusUnprocessableEntity, hubPage{Edit: edit.withForm(in), Error: refusal})
		return
	}
	if err != nil {
		p.serverError(w, "hub update", err)
		return
	}
	utils.Info("Panel hub updated", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID,
		"username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, "/?hub="+strconv.FormatInt(hub.ID, 10), http.StatusSeeOther)
}

// removeHub is POST /hubs/{id}/remove: one service call, then a redirect to
// the list the hub is gone from.
func (p *Panel) removeHub(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := hubIDOf(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	hub, err := p.hubs.remove(r.Context(), id, actor{userID: sess.userID, username: sess.username})
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		p.serverError(w, "hub remove", err)
		return
	}
	utils.Info("Panel hub removed", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID,
		"base_string", hub.BaseString, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
