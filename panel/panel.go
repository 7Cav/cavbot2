// Package panel is the bot's web UI, an HTTP server inside the cavbot2 binary
// signed in through the forum's OAuth2; the decision and its reasons are in
// docs/temp-vc-decisions.md. It holds the sign-in, the panel session, the
// group check, the no-access page for a request the groups don't allow,
// the hub page, the Settings page and the Foxhole page. The hub page is the
// guild-wide moderator section with its change log, the hub list with each hub's live spawned count, its last spawn
// failure and its broken hub state, the create and register forms, each
// hub's edit form with its change log, and the remove action. The Foxhole
// page lists the Foxhole role holders from the member list, with search,
// filters and each holder's note, starts a purge and a re-add of the
// approved Foxholers through the Foxhole runtime and shows its progress
// and report, and keeps its own change log of saves and reports. The
// Settings page is the rank roles and the recording roles, each with its
// change log.
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
	"strings"
	"sync"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"golang.org/x/oauth2"
)

// Panel holds the configuration, the parsed pages, the in-memory sessions and
// the service layers the hub page and the Foxhole page read through.
type Panel struct {
	cfg     Config
	version string
	hubs    *hubService
	foxhole foxholeService
	// recordings lists the recordings and streams their files.
	recordings *commands.RecordingLibrary
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

// hubPageBudget is the hub page's time budget: one deadline over every
// store read the page makes, and over any wait for Discord to send the bot
// the guild's data. A save's own wait for that data is bounded by it too.
// It must stay under the reverse proxy's 90 s read timeout. A page the
// proxy cuts off first looks like an abandoned page load and never reaches
// Sentry. The slowest page load is the 10 s group check, then the budget:
// the page reads the guild from the gateway state and makes no Discord
// call.
const hubPageBudget = 10 * time.Second

// storeTimeout bounds one store call a save makes, the way the runtime bounds
// its own. The save as a whole has no deadline: it outlives the request, and a
// database that stops answering fails it at the call that stalled.
const storeTimeout = 5 * time.Second

// New builds a panel from a config and what the hub page acts through. It
// parses the templates once, so a broken template fails here at startup and
// never at a request. Every field of deps is required: the hub page reads the
// store, the guild and the runtime on every load.
func New(cfg Config, version string, deps Deps) (*Panel, error) {
	if deps.Store == nil || deps.Runtime == nil || deps.Manager == nil || deps.Foxhole == nil || deps.Recordings == nil || deps.GuildID == "" {
		return nil, fmt.Errorf("panel needs a store, a runtime, a manager, a Foxhole runtime, a recording library and a guild ID")
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
		cfg:        cfg,
		version:    version,
		hubs:       &hubService{deps: deps, saveLock: &sync.Mutex{}},
		foxhole:    foxholeService{manager: deps.Manager, store: deps.Store, actions: deps.Foxhole, runtime: deps.Runtime, guildID: deps.GuildID},
		recordings: deps.Recordings,
		forumURL:   forumURL,
		pages:      pg,
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
	utils.Info("Panel listening", "addr", ln.Addr().String(), "base_url", p.cfg.BaseURL,
		"admin_group_ids", p.cfg.AdminGroupIDs, "foxhole_group_id", p.cfg.FoxholeGroupID)
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
	ticker := time.NewTicker(pruneInterval) //nolint:forbidigo // how often to prune, on the wall clock; what has expired is read on panelClock
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.sessions.prune(panelClock.Now())
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

// route is one of the panel's routes: a ServeMux pattern and the handler
// behind it, its gate included.
type route struct {
	pattern string
	handler http.Handler
}

// routes is every route the panel serves, each behind its gate. Handler
// registers these and no others, so a test that reads the list reads what
// the panel serves. forbidigo's route rule in .golangci.yml holds the
// panel's production code to that.
func (p *Panel) routes() []route {
	return []route{
		{"GET /static/", staticHandler()},
		{"GET /signin", http.HandlerFunc(p.signinPage)},
		{"POST /auth/start", http.HandlerFunc(p.authStart)},
		{"GET /auth/callback", http.HandlerFunc(p.authCallback)},
		{"POST /auth/signout", http.HandlerFunc(p.authSignout)},
		{"GET " + recordingStatusPath, http.HandlerFunc(p.recordingStatus)},
		{"GET /{$}", p.withPanelAdmin(p.homePage)},
		{"POST /hubs", p.withPanelAdmin(p.createOrRegisterHub)},
		{"GET /hubs/{id}", p.withPanelAdmin(p.openHub)},
		{"POST /hubs/{id}", p.withPanelAdmin(p.updateHub)},
		{"POST /hubs/{id}/remove", p.withPanelAdmin(p.removeHub)},
		{"POST /moderators", p.withPanelAdmin(p.saveModerators)},
		{"POST /recording-roles", p.withPanelAdmin(p.saveRecordingRoles)},
		{"GET " + settingsPath, p.withSettingsPage(p.settingsPage)},
		{"POST /rank-roles", p.withPanelAdmin(p.saveRankRoles)},
		{"GET " + recordingsPath, p.withSession(p.recordingsPage)},
		{"GET " + recordingsPath + "/{id}", p.withSession(p.recordingPage)},
		{"GET " + recordingsPath + "/{id}/mix", p.withSession(p.downloadMix)},
		{"GET " + recordingsPath + "/{id}/zip", p.withSession(p.downloadZip)},
		{"GET " + recordingsPath + "/{id}/info", p.withSession(p.downloadInfo)},
		{"GET " + recordingsPath + "/{id}/delete", p.withSession(p.deletePreview)},
		{"POST " + recordingsPath + "/{id}/delete", p.withSession(p.deleteRecording)},
		// A Foxhole form registers its route here. Each page a Foxhole form
		// answers with names an address that shows it by GET, which the page
		// script puts in the tab, so a reload never sends the form again (ADR
		// 0019). A new form answers through renderFoxhole, which names it.
		{"GET " + foxholePath, p.withFoxholePage(p.foxholePage)},
		{"GET " + foxholeReopenPath, p.withFoxholePage(p.reopenPreview)},
		{"GET " + foxholeRetryCancelPath, p.withFoxholePage(p.cancelRetry)},
		{"POST " + foxholeNotesPath, p.withFoxholePage(p.saveNote)},
		{"POST " + foxholeApprovalsPath, p.withFoxholePage(p.saveApprovals)},
		{"POST " + foxholePurgePath, p.withFoxholePage(p.startPurge)},
		{"POST " + foxholeAddPreviewPath, p.withFoxholePage(p.previewAdd)},
		{"POST " + foxholeAddPath, p.withFoxholePage(p.startAdd)},
		// Preview roster's address before it became a GET, kept one release
		// for tabs loaded before then.
		{"POST " + foxholeRosterPreviewPath, p.withFoxholePage(p.previewRoster)},
		{"POST " + foxholeRosterPath, p.withFoxholePage(p.startRosterAdd)},
		{"POST " + foxholeRemovePath, p.withFoxholePage(p.startRemoval)},
		{"POST " + foxholeReAddPath, p.withFoxholePage(p.startReAdd)},
		{"POST " + foxholeStopPath, p.withFoxholePage(p.stopAction)},
		{"POST " + foxholeRetryPath, p.withFoxholePage(p.startRetry)},
		// Every other GET: the not-found page, the same to any signed-in
		// user.
		{"GET /", p.withSession(p.noPage)},
	}
}

// Handler is the panel's routes behind Go's cross-origin protection, which
// refuses a state-changing request a browser sends from another origin. The
// panel therefore carries no form token, and every state change is a POST.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux() //nolint:forbidigo // the one mux the panel serves, holding routes() alone
	for _, rt := range p.routes() {
		mux.Handle(rt.pattern, rt.handler) //nolint:forbidigo // registers routes(), the one list of the panel's routes
	}
	protected := http.NewCrossOriginProtection().Handler(mux)
	// A panic in a handler is recovered here and reported through the same
	// path every other goroutine uses (ADR 0001), before net/http's own
	// recovery would print it with the remote address through the stdlib
	// logger. The request still gets an answer: serverErrorText, as plain
	// text, since the error page might not render either, and with no
	// capture, so the panic is one event. The recovery runs before the
	// gate, and the answer needs no session. A panic can come after a store
	// write, so the answer never says nothing changed.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				utils.ReportPanic("panel-request", v, "path", r.URL.Path)
				http.Error(w, serverErrorText, http.StatusInternalServerError)
			}
		}()
		protected.ServeHTTP(w, r)
	})
}

// paramNext is the query parameter, and the sign-in form's field, that
// carries the address a sign-in comes back to.
const paramNext = "next"

// signinPage is the sign-in page. Its form posts the address the query
// names to come back to as it stands: authStart checks it, since a form
// posted from anywhere else skips this page.
func (p *Panel) signinPage(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, http.StatusOK, "signin", pageData{
		Title: "Sign in",
		Cause: causeFromQuery(r.URL.Query().Get("cause")),
		Next:  r.URL.Query().Get(paramNext),
	})
}

// returnPath is the address a sign-in whose form posted raw comes back to:
// raw's path and query when they are on the panel's own origin, and "/"
// otherwise. They start with one "/": a second would name another host.
// They hold no "\", which a browser reads as "/", anywhere: http.Redirect
// cleans the path, so "/./\host" leaves as "/\host". net/url parses them,
// which refuses control characters, since a browser drops a tab or a line
// break from an address and would read "/\t/host" as "//host". raw's
// fragment comes back after them when it holds idChars alone. returnPath
// drops any other fragment, since with no query http.Redirect cleans the
// fragment as part of the path, and "/foxhole#/../\host" would leave as
// "/\host".
func returnPath(raw string) string {
	pathAndQuery, fragment, _ := strings.Cut(raw, "#")
	if pathAndQuery == "" || pathAndQuery[0] != '/' || len(pathAndQuery) > 1 && pathAndQuery[1] == '/' || strings.Contains(pathAndQuery, `\`) {
		return "/"
	}
	if _, err := url.Parse(pathAndQuery); err != nil {
		return "/"
	}
	if fragment == "" || strings.Trim(fragment, idChars) != "" {
		return pathAndQuery
	}
	return pathAndQuery + "#" + fragment
}

// idChars is every character a fragment may hold and still come back from
// a sign-in: letters, digits, "-" and "_". Neither a browser nor
// http.Redirect treats any of them as a separator in an address, and every
// heading ID the panel writes uses only these.
const idChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"

// authStart begins a sign-in: a fresh state and PKCE verifier, kept under a
// pending ID the sign-in cookie carries with the address the form posts to
// come back to, and a redirect to the forum's consent page. The
// confidential client sends PKCE S256 anyway; the forum verifies it when
// present.
func (p *Panel) authStart(w http.ResponseWriter, r *http.Request) {
	next := returnPath(r.PostFormValue(paramNext))
	state, err := newID()
	if err != nil {
		p.serverError(w, r, nil, "sign-in start", signinURL(causeNone, next), err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	id, err := p.sessions.addPending(pendingSignin{state: state, verifier: verifier, started: panelClock.Now(), next: next})
	if err != nil {
		p.serverError(w, r, nil, "sign-in start", signinURL(causeNone, next), err)
		return
	}
	setCookie(w, signinCookie, id)
	http.Redirect(w, r, p.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusSeeOther)
}

// serverErrorText is the plain-text answer to a request the panel failed
// where no page answers: a handler that panicked, and the recording status
// the deploy workflow reads. It says nothing of whether a change took
// effect.
const serverErrorText = "the panel could not complete the request"

// serverError is a failure inside the panel itself, not a forum answer: the
// random source or a store write. It captures to Sentry (ADR 0001) and
// answers with failedInside's page.
func (p *Panel) serverError(w http.ResponseWriter, r *http.Request, sess *session, step, retry string, err error) {
	utils.CaptureError("Panel request failed", err, "step", step)
	p.failedInside(w, r, sess, retry)
}

// failedInside renders the error page with a 500 for a request the panel
// itself failed, whose failure is already captured. Try again leads to
// retry, the GET address of the page the request was made from, so a save
// is never posted twice, and a page answering a POST names it as its
// address (ADR 0019). The failure can come after a store write, so the
// page never says nothing changed. sess is the request's panel session,
// nil for a request made signed out, whose page keeps the signed-out rail.
func (p *Panel) failedInside(w http.ResponseWriter, r *http.Request, sess *session, retry string) {
	const title = "The panel could not finish this"
	data := pageData{Title: title}
	if sess != nil {
		data = sess.page(title)
	}
	data.Failure, data.Retry, data.Address = failureServer, retry, retry
	data.Message = "Cavbot2 hit a fault of its own and stopped before it finished. Try again shows the page as it stands now."
	p.render(w, r, http.StatusInternalServerError, "error", data)
}

// authCallback is the one GET that creates state, defended by the state value
// the pending sign-in holds. The pending sign-in is consumed whatever the
// outcome, so a callback is answered once.
func (p *Panel) authCallback(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, signinCookie)
	c, err := r.Cookie(signinCookie)
	if err != nil {
		p.signinFailed(w, r, failureSigninNotStarted, "")
		return
	}
	pending, ok := p.sessions.takePending(c.Value, panelClock.Now())
	if !ok {
		p.signinFailed(w, r, failureSigninTooSlow, "")
		return
	}
	q := r.URL.Query()
	if q.Get("state") == "" || q.Get("state") != pending.state {
		utils.Warn("Panel sign-in refused: state mismatch")
		p.signinFailed(w, r, failureSigninMismatch, pending.next)
		return
	}
	if q.Get("error") != "" {
		// The user pressed Deny on the forum's consent page, or the forum
		// refused the request. Back to the sign-in page, which says the
		// sign-in didn't finish.
		utils.Info("Panel sign-in not completed", "forum_error", q.Get("error"))
		http.Redirect(w, r, signinURL(causeUnfinished, pending.next), http.StatusSeeOther)
		return
	}
	code := q.Get("code")
	if code == "" {
		p.signinFailed(w, r, failureSigninNoCode, pending.next)
		return
	}

	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, p.client)
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(pending.verifier))
	if err != nil {
		// A 4xx from the token endpoint is the forum refusing this sign-in
		// (a stale code, a wrong secret): back to the sign-in page, whose
		// sign-in comes back to the page this one was for. Anything else is
		// the forum not answering.
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) && retrieve.Response.StatusCode < 500 {
			utils.Warn("Panel sign-in refused by the forum", "status", retrieve.Response.StatusCode, "body", string(retrieve.Body))
			http.Redirect(w, r, signinURL(causeNone, pending.next), http.StatusSeeOther)
			return
		}
		utils.Warn("Panel token exchange failed", "error", err)
		p.signinForumUnavailable(w, r, pending.next)
		return
	}

	user, outcome, err := p.groupCheck(r.Context(), tok.AccessToken)
	switch outcome {
	case checkPassed:
		sess := session{accessToken: tok.AccessToken, signedIn: panelClock.Now(), discord: p.discordAtSignin(r.Context(), user.Username)}
		sess.identify(user, p.accessOf(user))
		id, err := p.sessions.add(sess)
		if err != nil {
			p.serverError(w, r, nil, "session create", signinURL(causeNone, pending.next), err)
			return
		}
		utils.Info("Panel sign-in", "username", user.Username, "forum_user_id", user.UserID, "discord_id", sess.discord.id,
			"panel_admin", sess.access.panelAdmin, "foxhole_manager", sess.access.foxholeManager)
		setCookie(w, sessionCookie, id)
		http.Redirect(w, r, pending.next, http.StatusSeeOther)
	case checkUnavailable:
		utils.Warn("Panel group check unavailable at sign-in", "error", err)
		p.signinForumUnavailable(w, r, pending.next)
	default:
		utils.Info("Panel sign-in refused", "cause", string(outcome.cause()), "username", user.Username, "forum_user_id", user.UserID)
		http.Redirect(w, r, signinURL(outcome.cause(), pending.next), http.StatusSeeOther)
	}
}

// signinFailures is the title and the sentence of each callback the panel
// can't finish.
var signinFailures = map[failure]struct{ title, message string }{
	failureSigninNotStarted: {"This sign-in didn't start here",
		"This browser has no sign-in in progress, so this one can't finish here."},
	failureSigninTooSlow: {"The sign-in took too long",
		"A sign-in has five minutes to come back from the forum, so you are not signed in."},
	failureSigninMismatch: {"The sign-in didn't match",
		"The forum sent back a sign-in this browser didn't start, so you are not signed in."},
	failureSigninNoCode: {"The forum didn't finish the sign-in",
		"The forum sent you back without signing you in, so you are not signed in."},
}

// signinFailed answers a callback the panel can't finish with the error
// page for the failure and a 400. Its Sign in again starts a fresh sign-in
// that comes back to next, the pending sign-in's address when the callback
// found one.
func (p *Panel) signinFailed(w http.ResponseWriter, r *http.Request, f failure, next string) {
	utils.Info("Panel sign-in not completed", "failure", string(f))
	text := signinFailures[f]
	p.render(w, r, http.StatusBadRequest, "error", pageData{Title: text.title, Message: text.message, Failure: f, SignInAgain: true, Next: next})
}

// forumUnavailableTitle is the title of the error page a forum outage
// earns, at sign-in or in a session.
const forumUnavailableTitle = "The forum did not answer"

// forumUnavailable renders the error page with the 502 the forum outage
// earns to a signed-in visitor. The page keeps the rail's identity and says
// the session is kept. A page answering a save names Try again's address,
// whose GET meets the same outage until the forum answers again.
func (p *Panel) forumUnavailable(w http.ResponseWriter, r *http.Request, sess session) {
	data := sess.page(forumUnavailableTitle)
	data.Message = "Cavbot2 could not reach the forum. Nothing changed and you are still signed in."
	data.Retry, data.Address = "/", "/"
	data.Failure = failureForum
	p.render(w, r, http.StatusBadGateway, "error", data)
}

// signinForumUnavailable renders the 502 page for a sign-in the forum did
// not answer. Its Sign in again starts a fresh sign-in that comes back to
// next, the pending sign-in's address, and carries no next for the landing.
func (p *Panel) signinForumUnavailable(w http.ResponseWriter, r *http.Request, next string) {
	if !namesAPage(next) {
		next = ""
	}
	p.render(w, r, http.StatusBadGateway, "error", pageData{Title: forumUnavailableTitle,
		Message: "The forum did not answer while you were signing in, so you are not signed in. Sign in again takes you back to the forum.",
		Failure: failureForum, SignInAgain: true, Next: next})
}

// signinURL is the sign-in page with the cause of the ended session, which
// the page renders as the data-cause attribute and one sentence, and the
// address its sign-in comes back to when that is not the landing.
func signinURL(c cause, next string) string {
	q := url.Values{}
	if c != causeNone {
		q.Set("cause", string(c))
	}
	if namesAPage(next) {
		q.Set(paramNext, next)
	}
	if len(q) == 0 {
		return "/signin"
	}
	return "/signin?" + q.Encode()
}

// namesAPage reports whether a sign-in's address names a page to come back
// to. The landing does not, since a sign-in with no next lands there, so a
// sign-in form carries no next for it.
func namesAPage(next string) bool {
	return next != "" && next != "/"
}

// returnAddressOf is the address a request's sign-in comes back to: a
// GET's path and query, the page it asked for. Any other request asks for
// no page, so it has none, and its sign-in lands on the landing.
func returnAddressOf(r *http.Request) string {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return ""
	}
	return r.URL.RequestURI()
}

// withSession is the gate every signed-in page sits behind: the session
// cookie must name a live session, and the group check must pass on this
// request. Expiry by the clock ends the session before the forum is asked.
// The session next sees carries what this request's group check lets it
// open. A request the gate sends to sign in carries the page it asked for,
// for the sign-in to come back to.
func (p *Panel) withSession(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Redirect(w, r, signinURL(causeNone, returnAddressOf(r)), http.StatusSeeOther)
			return
		}
		sess, ok := p.sessions.get(c.Value)
		if !ok {
			clearCookie(w, sessionCookie)
			http.Redirect(w, r, signinURL(causeNone, returnAddressOf(r)), http.StatusSeeOther)
			return
		}
		if sess.expired(panelClock.Now()) {
			p.endSession(w, c.Value, sess, string(causeExpired))
			http.Redirect(w, r, signinURL(causeExpired, returnAddressOf(r)), http.StatusSeeOther)
			return
		}
		user, outcome, err := p.groupCheck(r.Context(), sess.accessToken)
		switch outcome {
		case checkPassed:
			sess.identify(user, p.accessOf(user))
			p.sessions.update(c.Value, sess)
			next(w, r, sess)
		case checkUnavailable:
			utils.Warn("Panel group check unavailable, session kept", "username", sess.username, "error", err)
			p.forumUnavailable(w, r, sess)
		default:
			p.endSession(w, c.Value, sess, string(outcome.cause()))
			http.Redirect(w, r, signinURL(outcome.cause(), returnAddressOf(r)), http.StatusSeeOther)
		}
	}
}

// withPanelAdmin is the gate every settings page sits behind: this
// request's group check must find a panel admin.
func (p *Panel) withPanelAdmin(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return p.withAccess(func(a access) bool { return a.panelAdmin }, p.refuse, next)
}

// withSettingsPage is the gate the Settings page sits behind. This
// request's group check must find a panel admin. Anyone else gets the
// no-access page at the page's address, a page load included, where the
// hub page sends them on to the page they land on.
func (p *Panel) withSettingsPage(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return p.withAccess(func(a access) bool { return a.panelAdmin }, func(w http.ResponseWriter, r *http.Request, sess session) {
		p.noAccess(w, r, sess, noAccessRefused, settingsPath)
	}, next)
}

// withFoxholePage is the gate the Foxhole page sits behind: this request's
// group check must find a panel admin or a Foxhole manager.
func (p *Panel) withFoxholePage(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return p.withAccess(access.foxholePage, p.refuse, next)
}

// withAccess is withSession, and opens must allow what this request's group
// check found. Anyone else gets refuse's answer and the request does
// nothing.
func (p *Panel) withAccess(opens func(access) bool, refuse, next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return p.withSession(func(w http.ResponseWriter, r *http.Request, sess session) {
		if !opens(sess.access) {
			refuse(w, r, sess)
			return
		}
		next(w, r, sess)
	})
}

// refuse answers a request the session's groups don't allow. A page load
// goes to the page the user lands on: the hub page sits at the panel's
// root, where sign-in and the rail's mark lead, so a Foxhole manager who
// reaches it gets the Foxhole page, and anyone else the Recordings page.
// Every save gets the no-access page, saying the session's forum groups
// don't allow it and nothing changed, and does nothing. Its address is the
// Settings page's: every gate admits a panel admin, so the Settings page
// gives the same no-access page to anyone refused here.
func (p *Panel) refuse(w http.ResponseWriter, r *http.Request, sess session) {
	if r.Method == http.MethodGet {
		http.Redirect(w, r, sess.access.landing(), http.StatusSeeOther)
		return
	}
	// A panel admin passes every gate, so a refused save, a note save
	// included, is never one's, and the line keeps the name log searches
	// already use.
	if r.Method == http.MethodPost {
		utils.Info("Panel save refused: not a panel admin", "path", r.URL.Path,
			"username", sess.username, "forum_user_id", sess.userID)
	}
	p.noAccess(w, r, sess, noAccessRefused, settingsPath)
}

// The not-found page's titles and sentences: an address with no page, one
// naming a hub no hub has the ID of, and a save to such a hub, which says
// nothing changed.
const (
	noPageTitle      = "Page not found"
	noPageMessage    = "The panel has no page at this address."
	noHubTitle       = "Hub not found"
	noHubMessage     = "No hub has this address. Someone may have removed it."
	noHubSaveMessage = "No hub has this address, so nothing changed. Someone may have removed it."
)

// noPage is any GET the panel has no route for.
func (p *Panel) noPage(w http.ResponseWriter, r *http.Request, sess session) {
	p.notFound(w, r, sess, noPageTitle, noPageMessage)
}

// notFound renders the not-found page with a 404 and the title and
// sentence given. The rail stays, and the way back leads to the page the
// session lands on. A GET of the address a save was posted to finds nothing
// either, so a page answering a save names it.
func (p *Panel) notFound(w http.ResponseWriter, r *http.Request, sess session, title, message string) {
	data := sess.page(title)
	data.Failure, data.Message, data.Address = failureNotFound, message, r.URL.Path
	data.Back, data.BackLabel = sess.access.landing(), sess.access.landingLabel()
	p.render(w, r, http.StatusNotFound, "error", data)
}

// The no-access page's sentences, one for each reason the page is shown.
// The page says the one its caller gives, so what it says follows why the
// visitor is there and not which pages the rail links to.
const (
	// noAccessRefused refuses a request the visitor's forum groups don't
	// allow: a settings save, or a Foxhole page POST.
	noAccessRefused = "Your forum groups don't allow this. Nothing changed."
	// noAccessRecording refuses a request about a recording, its page, a
	// download or its delete, from someone who is neither its starter nor
	// a panel admin.
	noAccessRecording = "This recording is open only to its starter and panel admins. Nothing changed."
)

// noAccess renders the no-access page, saying message, one of the
// no-access page's sentences: the session's groups don't open what was
// asked for. address is a GET address that gives the session the same
// page, which the page names when it answers a POST (ADR 0019).
func (p *Panel) noAccess(w http.ResponseWriter, r *http.Request, sess session, message, address string) {
	data := sess.page("No access")
	data.Message, data.Address = message, address
	p.render(w, r, http.StatusForbidden, "noaccess", data)
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
	http.Redirect(w, r, signinURL(causeSignedOut, ""), http.StatusSeeOther)
}

// endSession drops the session and its cookie and records why.
func (p *Panel) endSession(w http.ResponseWriter, id string, sess session, reason string) {
	p.sessions.end(id)
	clearCookie(w, sessionCookie)
	utils.Info("Panel session ended", "reason", reason, "username", sess.username, "forum_user_id", sess.userID)
}

// homePage is the hub page: the list, and the register form or, with a hub
// named in the query, that hub's edit form. With a hub named to remove, its
// remove preview shows first, with a removal named, the list names the hub
// that went, and with a hub named as added, the list names it. A value in
// any of them that isn't an ID gets the not-found page. A save named, with
// the version it left its form's record at, has that form say it saved.
func (p *Panel) homePage(w http.ResponseWriter, r *http.Request, sess session) {
	var req pageRequest
	params := []struct {
		name string
		id   *int64
	}{{"hub", &req.HubID}, {"remove", &req.Remove}, {"removed", &req.Removed}, {"added", &req.Added}}
	for _, param := range params {
		raw := r.URL.Query().Get(param.name)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			p.notFound(w, r, sess, noHubTitle, noHubMessage)
			return
		}
		*param.id = id
	}
	req.Saved = savedFromQuery(r.URL.Query().Get(paramSaved))
	// A version that isn't a number matches no record, so the page says
	// nothing saved.
	req.SavedVersion, _ = strconv.ParseInt(r.URL.Query().Get(paramVersion), 10, 64)
	p.renderHubs(w, r, sess, http.StatusOK, req)
}

// renderHubs renders the hub page read now: the list, and the form the
// request asks for with its refusal when there is one. A request for a hub
// that does not exist is 404: the edit form's hub gets the not-found page,
// and a remove preview's, opened from the hub list or the edit form,
// renders the page saying the hub is gone.
func (p *Panel) renderHubs(w http.ResponseWriter, r *http.Request, sess session, status int, req pageRequest) {
	// The budget's start is taken before its deadline is set, so a page that
	// fails on the deadline never reports less time than the budget.
	start := panelClock.Now()
	ctx, cancel := panelClock.WithTimeout(r.Context(), hubPageBudget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	reads := newPageReads(start, deadline)
	page, err := p.hubs.forPage(reads).page(ctx, req)
	switch {
	case errors.Is(err, store.ErrNotFound):
		p.notFound(w, r, sess, noHubTitle, noHubMessage)
		return
	case errors.Is(err, context.Canceled):
		// An abandoned page load: the connection closed before the page
		// answered and before its budget ran out, which fails a read with
		// the request's own cancellation. Expected, so no Sentry event, and
		// nobody is left to answer.
		utils.Info("Panel page abandoned", "step", "hub page", "username", sess.username, "forum_user_id", sess.userID)
		return
	case errors.Is(err, errNoGuildData):
		p.noGuildData(w, r, sess, "hub page", pageAddress(req), err)
		return
	case err != nil:
		var report *budgetReport
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			report = reads.report(hubPageBudget)
		}
		p.pageFailed(w, r, sess, "hub page", pageAddress(req), err, report)
		return
	}
	if page.Error == errHubGone {
		status = http.StatusNotFound
	}
	data := sess.page("Hubs")
	data.Page, data.Hubs, data.Address = pageHubs, page, req.hubPageAddress()
	p.render(w, r, status, "home", data)
}

// hubPageAddress is the GET address that shows the hub page the request
// asks for as it stands then, for a page that answers a form (ADR 0019): the page
// with the hub's edit form and the remove preview the request names,
// opened at the form that was refused, or at the remove preview. The
// refusal and the form as posted came with the POST, so they don't come
// back.
func (req pageRequest) hubPageAddress() string {
	q := url.Values{}
	if req.HubID != 0 {
		q.Set("hub", strconv.FormatInt(req.HubID, 10))
	}
	anchor := req.Refused
	if req.Remove != 0 {
		q.Set("remove", strconv.FormatInt(req.Remove, 10))
		anchor = formRemove
	}
	return (&url.URL{Path: "/", RawQuery: q.Encode(), Fragment: anchor}).String()
}

// pageFailed reports a page read that failed to Sentry (ADR 0001) and
// renders the error page with the 503 it earns: the page took too long when
// the time budget ran out, and could not load for any other failure. Try
// again leads to retry, the page's GET address, so a refused save's page is
// loaded afresh and never posted twice. report, set when the hub page's
// budget had run out by the failure, goes on the event as time_budget. A
// page answering a POST names retry as its address too, so a reload loads
// what Try again does.
func (p *Panel) pageFailed(w http.ResponseWriter, r *http.Request, sess session, step, retry string, err error, report *budgetReport) {
	kv := []any{"step", step}
	if report != nil {
		kv = append(kv, "time_budget", report)
	}
	utils.CaptureError("Panel request failed", err, kv...)
	title, kind, message := "The panel could not load this page", failureReadFailed,
		"Cavbot2 could not load this page. Nothing changed and you are still signed in."
	if errors.Is(err, context.DeadlineExceeded) {
		title, kind, message = "The panel took too long", failureTooSlow,
			"Cavbot2 took too long to load this page. Nothing changed and you are still signed in."
	}
	data := sess.page(title)
	data.Failure, data.Message, data.Retry, data.Address = kind, message, retry, retry
	p.render(w, r, http.StatusServiceUnavailable, "error", data)
}

// pageAddress is the GET address of the page a request shows: the hub list,
// or the address that opens the hub's edit form at the form. Try again
// leads there, so a save is never posted twice.
func pageAddress(req pageRequest) string {
	if req.HubID != 0 {
		return hubAddress(req.HubID)
	}
	return "/"
}

// openHub is GET /hubs/{id}, a hub's edit form's Try again: a redirect to
// the hub's edit form. The error page Try again sits on can be at the edit
// form's own address, when its load failed, and a link straight there would
// fetch nothing: the browser only scrolls to the fragment.
func (p *Panel) openHub(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := hubIDOf(r)
	if !ok {
		p.notFound(w, r, sess, noHubTitle, noHubMessage)
		return
	}
	http.Redirect(w, r, hubEditAddress(id), http.StatusSeeOther)
}

// noGuildData renders the error page, with a 503, for a page load or a save
// that found no data for the guild in the gateway state. Discord has not
// sent it to the bot: for a moment after a deploy, or through an outage.
// That is Discord's delay, which the bot's reconnect logs already record,
// and nothing on-call can fix, so it is one INFO line and no Sentry event.
// retry is the GET address Try again leads to, and the address a page
// answering a save names, so a reload loads what Try again does.
func (p *Panel) noGuildData(w http.ResponseWriter, r *http.Request, sess session, step, retry string, err error) {
	logNoGuildData(sess, step, err)
	data := sess.page("Discord has not sent the server's data")
	data.Failure, data.Retry, data.Address = failureNoGuildData, retry, retry
	data.Message = "Discord hasn't sent Cavbot2 this server's channels and roles. That happens for a moment after the bot restarts, " +
		"and while Discord has trouble. Nothing changed and you are still signed in."
	p.render(w, r, http.StatusServiceUnavailable, "error", data)
}

// logNoGuildData is the one INFO line of a page load or a save that found
// no data for the guild in the gateway state, and no Sentry event: the
// delay is Discord's.
func logNoGuildData(sess session, step string, err error) {
	utils.Info("Panel has no guild data", "step", step, "reason", err.Error(),
		"username", sess.username, "forum_user_id", sess.userID)
}

// createOrRegisterHub is POST /hubs. The create and register forms post to
// one route and are told apart by their fields: the create form carries a
// category, the register form a hub channel. An unpicked category still
// posts the key, empty, so a create with no category is refused as a
// create and never mistaken for a register.
func (p *Panel) createOrRegisterHub(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	if r.PostForm.Has(fieldCategory) {
		p.createHub(w, r, sess)
		return
	}
	p.registerHub(w, r, sess)
}

// createHub is the create half of POST /hubs: one service call, then a
// redirect to the list, where the new hub appears and a line names it, or
// the page again with the refused field named. The form is already parsed.
func (p *Panel) createHub(w http.ResponseWriter, r *http.Request, sess session) {
	in := createInput{
		CategoryID:  r.PostForm.Get(fieldCategory),
		ChannelName: r.PostForm.Get(fieldChannelName),
		BaseString:  r.PostForm.Get(fieldBaseString),
	}
	ctx, hubs := p.saving(r)
	hub, err := hubs.create(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderRefused(w, r, sess, pageRequest{Create: in, Error: refusal, Refused: formCreate})
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "hub create", "/", err)
		return
	}
	utils.Info("Panel hub created", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID,
		"base_string", hub.BaseString, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, addedAddress(hub.ID), http.StatusSeeOther)
}

// registerHub is the register half of POST /hubs: one service call, then a
// redirect to the list, where the new hub appears and a line names it, or
// the page again with the refused field named. The form is already parsed.
func (p *Panel) registerHub(w http.ResponseWriter, r *http.Request, sess session) {
	in := registerInput{ChannelID: r.PostForm.Get(fieldHubChannel), BaseString: r.PostForm.Get(fieldBaseString)}
	ctx, hubs := p.saving(r)
	hub, err := hubs.register(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderRefused(w, r, sess, pageRequest{Register: in, Error: refusal, Refused: formRegister})
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "hub register", "/", err)
		return
	}
	utils.Info("Panel hub registered", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID,
		"base_string", hub.BaseString, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, addedAddress(hub.ID), http.StatusSeeOther)
}

// saving is what a save handler runs its save through. A save begins once
// the group check has found a panel admin and runs to its end whether or
// not the browser waits. So its context is the request's without the
// cancellation net/http sends when the connection closes, and its service
// gives each store call the save makes a deadline of its own, and its wait
// for the guild's data the page's time budget.
func (p *Panel) saving(r *http.Request) (context.Context, *hubService) {
	return context.WithoutCancel(r.Context()), p.hubs.forSave(hubPageBudget)
}

// saveFailed answers a save that failed without a refusal. One that found
// no data for the guild wrote nothing and gets the page that says so; any
// other failure is the panel's own. retry is the GET address of the page
// the save was made from, where either page's Try again leads.
func (p *Panel) saveFailed(w http.ResponseWriter, r *http.Request, sess session, step, retry string, err error) {
	if errors.Is(err, errNoGuildData) {
		p.noGuildData(w, r, sess, step, retry, err)
		return
	}
	p.serverError(w, r, &sess, step, retry, err)
}

// renderRefused answers a refused save with the page again, the form as
// posted and the refusal's note on it. The answer is at the form's action,
// whose fragment names the form's heading, so it opens at the form. A stale
// form, whose save met another save, answers 409 and logs one INFO line with
// the signed-in user and kv, the hub for a hub's form (#373). Every other
// refusal answers 422.
func (p *Panel) renderRefused(w http.ResponseWriter, r *http.Request, sess session, req pageRequest, kv ...any) {
	p.renderHubs(w, r, sess, refusalStatus(sess, req.Error, kv...), req)
}

// refusalStatus is the status a refused save's page answers with. A stale
// form answers 409 and logs one INFO line with the signed-in user and kv,
// and so does a rank roles Confirm whose preview moved. Every other refusal
// answers 422.
func refusalStatus(sess session, refusal *fieldError, kv ...any) int {
	switch {
	case refusal.stale():
		utils.Info("Panel save refused: stale form", append(kv, "username", sess.username, "forum_user_id", sess.userID)...)
		return http.StatusConflict
	case refusal.kind == refusalChanged:
		return http.StatusConflict
	}
	return http.StatusUnprocessableEntity
}

// hubIDOf reads the hub ID from the route. A value that is not an ID names
// no hub, and the caller answers 404.
func hubIDOf(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// updateHub is POST /hubs/{id}: one service call, then a redirect to the
// hub's form, opened at the form, where the saved values show and the form
// says it saved, or the form again with the refused field named.
func (p *Panel) updateHub(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := hubIDOf(r)
	if !ok {
		p.notFound(w, r, sess, noHubTitle, noHubSaveMessage)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := editInput{
		Version:           r.PostForm.Get(fieldVersion),
		LoadedChannelName: r.PostForm.Get(fieldLoadedChannelName),
		ChannelName:       r.PostForm.Get(fieldChannelName),
		BaseString:        r.PostForm.Get(fieldBaseString),
		PermissionSource:  r.PostForm.Get(fieldPermissionSource),
		ModeratorRoleIDs:  r.PostForm[fieldModeratorRoles],
		UserLimit:         r.PostForm.Get(fieldUserLimit),
		Bitrate:           r.PostForm.Get(fieldBitrate),
		DeleteDelay:       r.PostForm.Get(fieldDeleteDelay),
		Enabled:           r.PostForm.Get(fieldEnabled) != "",
		RenamingAllowed:   r.PostForm.Get(fieldRenamingAllowed) != "",
		LockingAllowed:    r.PostForm.Get(fieldLockingAllowed) != "",
	}
	if !r.PostForm.Has(fieldLoadedChannelName) {
		// A form from before the field existed: the name it posts counts as
		// the one it loaded, so neither it nor its re-render renames.
		in.LoadedChannelName = in.ChannelName
	}
	ctx, hubs := p.saving(r)
	hub, err := hubs.update(ctx, id, in, sess.actor())
	if errors.Is(err, store.ErrNotFound) {
		p.notFound(w, r, sess, noHubTitle, noHubSaveMessage)
		return
	}
	if refusal, ok := asFieldError(err); ok {
		p.renderRefused(w, r, sess, pageRequest{HubID: id, Edit: &in, Error: refusal, Refused: formEdit}, "hub_id", id)
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "hub update", pageAddress(pageRequest{HubID: id}), err)
		return
	}
	utils.Info("Panel hub updated", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID,
		"username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, savedAddress(formEdit, hub.Version, hub.ID), http.StatusSeeOther)
}

// removeHub is POST /hubs/{id}/remove, the remove preview's Confirm: one
// service call, then a redirect to the list the hub is gone from, which
// names it by the removal's entry and opens at the list, or the preview
// again with the refusal on it, over the page Remove was pressed on.
func (p *Panel) removeHub(w http.ResponseWriter, r *http.Request, sess session) {
	id, ok := hubIDOf(r)
	if !ok {
		p.notFound(w, r, sess, noHubTitle, noHubSaveMessage)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	// The preview over the page Remove was pressed on. Remove pressed on
	// the hub's edit form answers with that form again. It is the removed
	// hub's own, whatever the Confirm posts.
	preview := pageRequest{Remove: id}
	if r.PostForm.Get(fieldRemoveFrom) == formEdit {
		preview.HubID = id
	}
	ctx, hubs := p.saving(r)
	hub, entryID, err := hubs.remove(ctx, id, r.PostForm.Get(fieldVersion), sess.actor())
	if errors.Is(err, store.ErrNotFound) {
		p.renderHubs(w, r, sess, http.StatusNotFound, pageRequest{Remove: id})
		return
	}
	if refusal, ok := asFieldError(err); ok {
		req := preview
		req.Error, req.Refused = refusal, formRemove
		p.renderRefused(w, r, sess, req, "hub_id", id)
		return
	}
	if err != nil {
		p.serverError(w, r, &sess, "hub remove", preview.hubPageAddress(), err)
		return
	}
	utils.Info("Panel hub removed", "hub_id", hub.ID, "hub_channel_id", hub.HubChannelID,
		"base_string", hub.BaseString, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, "/?removed="+strconv.FormatInt(entryID, 10)+"#"+hubListAnchor, http.StatusSeeOther)
}

// saveModerators is POST /moderators: one service call, then a redirect to
// the page, opened at the guild-wide section, where the saved set shows and
// the section says it saved, or the page again with the refused field named
// on the guild-wide section.
func (p *Panel) saveModerators(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := moderatorsInput{Version: r.PostForm.Get(fieldVersion), RoleIDs: r.PostForm[fieldModeratorRoles]}
	ctx, hubs := p.saving(r)
	saved, err := hubs.setModerators(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderRefused(w, r, sess, pageRequest{Moderators: &in, Error: refusal, Refused: formModerators})
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "moderators save", "/", err)
		return
	}
	utils.Info("Panel guild moderator roles saved", "role_ids", saved.RoleIDs, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, savedAddress(formModerators, saved.Version, 0), http.StatusSeeOther)
}

// saveRecordingRoles is POST /recording-roles: one service call, then a
// redirect to the Settings page, opened at the recording roles section,
// where the saved set shows and the section says it saved, or the page
// again with the refusal on the recording roles section.
func (p *Panel) saveRecordingRoles(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := recordingRolesInput{Version: r.PostForm.Get(fieldVersion), RoleIDs: r.PostForm[fieldRecordingRoles]}
	ctx, hubs := p.saving(r)
	saved, err := hubs.setRecordingRoles(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderSettings(w, r, sess, refusalStatus(sess, refusal), pageRequest{RecordingRoles: &in, Error: refusal, Refused: formRecordingRoles})
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "recording roles save", settingsPath, err)
		return
	}
	utils.Info("Panel recording roles saved", "role_ids", saved.RoleIDs, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, settingsSavedAddress(formRecordingRoles, saved.Version), http.StatusSeeOther)
}
