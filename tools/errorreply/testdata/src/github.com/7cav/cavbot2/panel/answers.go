// Package panel stands in for cavbot2's panel, which answers a browser with
// a bare answer, a redirect or a rendered page.
package panel

import (
	"net/http"
	"strings"
)

func answersWithTheError(w http.ResponseWriter, err error) {
	http.Error(w, "bad: "+err.Error(), http.StatusInternalServerError) // want "panel"
}

func redirectsWithTheError(w http.ResponseWriter, r *http.Request, err error) {
	http.Redirect(w, r, "/signin?cause="+err.Error(), http.StatusSeeOther) // want "panel"
}

func answersWithTheErrorTrimmedByACallThatReturnsTwoResults(w http.ResponseWriter, err error) {
	detail, _ := strings.CutPrefix(err.Error(), "HTTP ")
	http.Error(w, "bad: "+detail, http.StatusInternalServerError) // want "panel"
}
