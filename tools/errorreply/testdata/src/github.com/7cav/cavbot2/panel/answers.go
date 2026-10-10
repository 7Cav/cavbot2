// Package panel stands in for cavbot2's panel, which answers a browser with
// a bare answer, a redirect or a rendered page.
package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func answersWithTheError(w http.ResponseWriter, err error) {
	http.Error(w, "bad: "+err.Error(), http.StatusInternalServerError) // want "panel"
}

func redirectsWithTheError(w http.ResponseWriter, r *http.Request, err error) {
	http.Redirect(w, r, "/signin?cause="+err.Error(), http.StatusSeeOther) // want "panel"
}

func answersWithWhatFollowsTheErrorsFirstColon(w http.ResponseWriter, err error) {
	http.Error(w, "bad: "+strings.Split(err.Error(), ": ")[1], http.StatusBadGateway) // want "panel"
}

func answersWithTheErrorTrimmedByACallThatReturnsTwoResults(w http.ResponseWriter, err error) {
	detail, _ := strings.CutPrefix(err.Error(), "HTTP ")
	http.Error(w, "bad: "+detail, http.StatusInternalServerError) // want "panel"
}

func answersWithWhatAClosureKept(w http.ResponseWriter, err error) {
	var reason string
	func() { reason = err.Error() }()
	http.Error(w, "bad: "+reason, http.StatusInternalServerError) // want "panel"
}

func writesTheError(w http.ResponseWriter, err error) {
	_, _ = w.Write([]byte("Failed: " + err.Error())) // want "panel"
}

func printsTheError(w http.ResponseWriter, err error) {
	fmt.Fprintf(w, "Failed: %v", err) // want "panel"
}

func encodesTheError(w http.ResponseWriter, err error) {
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) // want "panel"
}
