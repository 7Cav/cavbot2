package panel

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"time"

	"github.com/7cav/cavbot2/utils"
	"golang.org/x/oauth2"
)

//go:embed templates/*.html
var templateFS embed.FS

// Server is the panel's HTTP server state: configuration, the OAuth client,
// the in-memory sessions and pending sign-ins, and the parsed templates.
type Server struct {
	cfg      Config
	oauth    *oauth2.Config
	http     *http.Client
	sessions *memoryStore[session]
	pending  *memoryStore[pendingSignIn]
	tmpl     *template.Template
	handler  http.Handler
}

// New builds a Server. The handler is ready to serve; Run starts the prune
// timer.
func New(cfg Config) *Server {
	s := &Server{
		cfg:      cfg,
		oauth:    newOAuthConfig(cfg),
		http:     &http.Client{Timeout: userinfoTimeout},
		sessions: newMemoryStore(func(v session) time.Time { return v.expires }),
		pending:  newMemoryStore(func(v pendingSignIn) time.Time { return v.expires }),
		tmpl:     template.Must(template.ParseFS(templateFS, "templates/*.html")),
	}
	s.handler = s.routes()
	return s
}

// Handler returns the panel's HTTP handler: the mux wrapped in Go's
// cross-origin protection, so every state-changing POST from another origin
// is refused without form tokens.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Run prunes expired sessions and pending sign-ins until ctx ends.
func (s *Server) Run(ctx context.Context) {
	defer utils.RecoverPanic("panel-prune")
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sessions.prune()
			s.pending.prune()
		}
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /signin", s.handleSignIn)
	mux.HandleFunc("POST /auth/start", s.handleAuthStart)
	mux.HandleFunc("GET "+callbackPath, s.handleAuthCallback)
	mux.HandleFunc("POST /auth/signout", s.handleSignOut)
	mux.Handle("GET /{$}", s.requireSession(http.HandlerFunc(s.handleHome)))

	protection := http.NewCrossOriginProtection()
	return protection.Handler(mux)
}

// page is what every template renders with.
type page struct {
	// Username is the signed-in forum username, empty on the sign-in page.
	Username string
	// Cause is why the last session ended, or empty on a plain visit.
	Cause string
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		utils.Error("Panel template render failed", "template", name, "error", err)
	}
}

func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "signin.html", page{Cause: knownCause(r.URL.Query().Get("cause"))})
}

// knownCause keeps only the cause values the sign-in page has a sentence for,
// so an arbitrary query value never reaches the template.
func knownCause(v string) string {
	switch v {
	case causeExpired, causeNoGroup:
		return v
	default:
		return ""
	}
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "home.html", page{Username: sessionFrom(r).username})
}
