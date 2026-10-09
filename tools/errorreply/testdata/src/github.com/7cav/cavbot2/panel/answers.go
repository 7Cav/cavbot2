// Package panel stands in for cavbot2's panel, which answers a browser with
// a bare answer, a redirect or a rendered page.
package panel

import "net/http"

func answersWithTheError(w http.ResponseWriter, err error) {
	http.Error(w, "bad: "+err.Error(), http.StatusInternalServerError) // want "panel"
}

func redirectsWithTheError(w http.ResponseWriter, r *http.Request, err error) {
	http.Redirect(w, r, "/signin?cause="+err.Error(), http.StatusSeeOther) // want "panel"
}
