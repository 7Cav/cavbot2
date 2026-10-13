package panel

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/7cav/cavbot2/utils"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// cause is why a panel session ended, shown on the sign-in page. The values
// are the data-cause attribute on <main>, a test contract; the sentences are
// not.
type cause string

const (
	causeNone    cause = ""
	causeExpired cause = "expired"
	causeRefused cause = "refused"
	// causeUnfinished is a sign-in the forum sent back without finishing:
	// Deny on its consent page, or a request it refused.
	causeUnfinished cause = "unfinished"
	// causeSignedOut is the session the user ended with Sign out.
	causeSignedOut cause = "signed-out"
)

// causeFromQuery narrows a query value to the enum, so an unknown value is a
// plain sign-in page and never a data-cause the tests could not predict.
func causeFromQuery(raw string) cause {
	switch cause(raw) {
	case causeExpired:
		return causeExpired
	case causeRefused:
		return causeRefused
	case causeUnfinished:
		return causeUnfinished
	case causeSignedOut:
		return causeSignedOut
	}
	return causeNone
}

// failure is which failure the error page reports. The values are the
// data-failure attribute on <main>, a test contract; the sentences are not.
type failure string

const (
	// failureForum is the forum not answering the group check or the token
	// exchange.
	failureForum failure = "forum"
	// failureReadFailed is a hub page read that failed.
	failureReadFailed failure = "read-failed"
	// failureTooSlow is a hub page whose time budget ran out.
	failureTooSlow failure = "too-slow"
	// failureNoGuildData is a hub page or a save that found no data for the
	// guild in the gateway state: Discord has not sent it to the bot.
	failureNoGuildData failure = "no-guild-data"
	// failureNotFound is an address that names nothing in the panel: no
	// page, or no hub with the ID it names.
	failureNotFound failure = "not-found"
	// failureSigninNotStarted is a callback from a browser with no sign-in
	// in progress: it carries no sign-in cookie.
	failureSigninNotStarted failure = "signin-not-started"
	// failureSigninTooSlow is a callback whose pending sign-in is gone: its
	// five minutes ran out, or the bot restarted since it began.
	failureSigninTooSlow failure = "signin-too-slow"
	// failureSigninMismatch is a callback whose state is not the one its
	// pending sign-in sent to the forum.
	failureSigninMismatch failure = "signin-mismatch"
	// failureSigninNoCode is a callback that carries neither a code nor the
	// forum's error.
	failureSigninNoCode failure = "signin-no-code"
)

// pageData is what every template renders from. SignedIn switches the rail
// between the identity block and the forum link, and Rail lists the pages
// the rail links to beside the identity block. Page names the page shown, for
// its rail link and its data-page. Hubs is filled for the hub page alone,
// Foxhole for the Foxhole page alone, and Failure, Message and Retry for the
// error page alone.
type pageData struct {
	Title    string
	Version  string
	ForumURL string
	SignedIn bool
	Rail     railPages
	Page     string
	Username string
	Cause    cause
	// Next is the address the sign-in form, on the sign-in page or the
	// error page's Sign in again, posts for its sign-in to come back to.
	Next    string
	Hubs    hubPage
	Foxhole foxholeView
	// Recordings is filled for the Recordings page alone.
	Recordings recordingsView
	Failure    failure
	// Message is the one sentence of the error page, and of the no-access
	// page.
	Message string
	// Retry is where the error page's Try again link leads.
	Retry string
	// SignInAgain puts Sign in again on the error page in place of Try
	// again, for a sign-in that didn't finish.
	SignInAgain bool
	// Back, on the not-found page, is where its way back leads in place of
	// Try again, and BackLabel names it.
	Back      string
	BackLabel string
}

// railPages is which pages the rail links to, the ones this request's
// group check lets the session open. Every signed-in visitor opens the
// Recordings page, which lists the recordings they started or says they
// have none yet. Every session gets its link, so the rail doesn't read the
// store to learn who started a recording.
type railPages struct {
	Hubs       bool
	Foxhole    bool
	Recordings bool
}

// The pages the rail links to, as Page names them.
const (
	pageHubs       = "hubs"
	pageFoxhole    = "foxhole"
	pageRecordings = "recordings"
)

// pages are the templates, one per screen, each parsed with the shared layout
// so a page defines only its title and its content block.
type pages map[string]*template.Template

func parsePages() (pages, error) {
	out := pages{}
	for _, name := range []string{"signin", "home", "error", "noaccess", "foxhole", "recordings"} {
		t, err := template.New("layout.html").ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}

// render writes one page with the status. A template failure after the header
// is written cannot be undone, so the page renders to a buffer first.
func (p *Panel) render(w http.ResponseWriter, status int, page string, data pageData) {
	data.Version = p.version
	data.ForumURL = p.forumURL
	var buf bytes.Buffer
	if err := p.pages[page].ExecuteTemplate(&buf, "layout.html", data); err != nil {
		utils.CaptureError("Panel page failed to render", err, "page", page)
		http.Error(w, "page failed to render", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// staticHandler serves the embedded stylesheet, script, font and images
// under /static/, all from the binary, so no page fetches from a third
// party (ADR 0013).
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}
