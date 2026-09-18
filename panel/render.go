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
	causeNoGroup cause = "no-group"
)

// causeFromQuery narrows a query value to the enum, so an unknown value is a
// plain sign-in page and never a data-cause the tests could not predict.
func causeFromQuery(raw string) cause {
	switch cause(raw) {
	case causeExpired:
		return causeExpired
	case causeNoGroup:
		return causeNoGroup
	}
	return causeNone
}

// pageData is what every template renders from. SignedIn switches the rail
// between the navigation with the identity block and the forum link.
type pageData struct {
	Title    string
	Version  string
	ForumURL string
	SignedIn bool
	Username string
	Cause    cause
}

// pages are the templates, one per screen, each parsed with the shared layout
// so a page defines only its title and its content block.
type pages map[string]*template.Template

func parsePages() (pages, error) {
	out := pages{}
	for _, name := range []string{"signin", "home", "error"} {
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

// staticHandler serves the embedded stylesheet and images under /static/.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}
