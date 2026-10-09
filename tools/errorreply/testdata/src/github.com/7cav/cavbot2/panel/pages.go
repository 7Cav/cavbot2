package panel

import (
	"errors"
	"html/template"
	"net/http"
)

// page is what a template renders.
type page struct {
	Title   string
	Message string
	Refusal *refusal
}

var tmpl = template.Must(template.New("page").Parse("{{.Title}}: {{.Message}}"))

func rendersTheError(w http.ResponseWriter, err error) {
	_ = tmpl.Execute(w, page{Title: "Hubs", Message: "Failed: " + err.Error()}) // want "panel"
}

func render(w http.ResponseWriter, data page) {
	_ = tmpl.ExecuteTemplate(w, "page", data)
}

func rendersTheErrorThroughAWrapper(w http.ResponseWriter, err error) {
	render(w, page{Title: "Hubs", Message: "Failed: " + err.Error()}) // want "panel"
}

// refusal is a refused save: the field refused and why. The page shows it.
type refusal struct {
	Field   string
	Message string
}

func (e *refusal) Error() string { return e.Field + ": " + e.Message }

func makeHub(name string) error {
	if name == "" {
		return &refusal{Field: "channel", Message: "That channel is already a hub."}
	}
	return nil
}

func showsARefusalWithFixedText(w http.ResponseWriter, name string) {
	var refused *refusal
	if errors.As(makeHub(name), &refused) {
		render(w, page{Title: "Hubs", Refusal: refused})
	}
}

func renameHub(err error) error {
	return &refusal{Field: "channel", Message: "Discord did not rename the channel: " + err.Error()} // want "panel"
}

func showsARefusalBuiltFromTheError(w http.ResponseWriter, err error) {
	var refused *refusal
	if errors.As(renameHub(err), &refused) {
		render(w, page{Title: "Hubs", Refusal: refused})
	}
}
