// Package smoke stands in for cavbot2's smoke tool, whose fake forum answers
// the browser of the maintainer running a smoke pass.
package smoke

import "net/http"

func answersTheMaintainerWithTheError(w http.ResponseWriter, err error) {
	http.Error(w, "fake forum: "+err.Error(), http.StatusBadRequest)
}
