//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// The milpac states `smoke milpac` sets for an account. Each but
// milpacReal sets what the account's Discord ID lookup answers.
const (
	milpacActive                 = "active"
	milpacDischarged             = "discharged"
	milpacDishonorablyDischarged = "dishonorably-discharged"
	milpacRetired                = "retired"
	milpacNone                   = "none"       // 404
	milpacUnreadable             = "unreadable" // 500
	milpacReal                   = "real"       // the lookup forwards again
)

var milpacStates = []string{milpacActive, milpacDischarged, milpacDishonorablyDischarged, milpacRetired, milpacNone, milpacUnreadable, milpacReal}

// smokePositions are the primary positions of the smoke milpacs. A
// discharge's are the milpacs' own, verbatim (GLOSSARY.md).
var smokePositions = map[string]string{
	milpacActive:                 "2/B/1-7",
	milpacDischarged:             "Discharged",
	milpacDishonorablyDischarged: "Dishonorably Discharged",
	milpacRetired:                "Retired",
}

// cavAPIStandIn stands in for the 7Cav API during a run. It forwards every
// request to the real API and passes the answer back, except a Discord ID
// milpac lookup for an account the pass has set a state for.
type cavAPIStandIn struct {
	upstream string
	logf     func(format string, args ...any)

	mu     sync.Mutex
	states map[string]string
}

// newCavAPIStandIn forwards to upstream, the real API's origin.
func newCavAPIStandIn(upstream string, logf func(string, ...any)) *cavAPIStandIn {
	return &cavAPIStandIn{upstream: upstream, logf: logf, states: map[string]string{}}
}

func (c *cavAPIStandIn) handler() http.Handler {
	target, _ := url.Parse(c.upstream)
	forward := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
	}}
	mux := http.NewServeMux()
	mux.Handle("/", forward)
	mux.HandleFunc("GET /api/v1/milpac/discord/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c.mu.Lock()
		state, set := c.states[id]
		c.mu.Unlock()
		if !set {
			forward.ServeHTTP(w, r)
			return
		}
		c.answer(w, id, state)
	})
	mux.HandleFunc("POST /smoke/milpac", c.setState)
	return mux
}

// answer is what a set account's Discord ID lookup gets.
func (c *cavAPIStandIn) answer(w http.ResponseWriter, discordID, state string) {
	switch state {
	case milpacNone:
		http.Error(w, "smoke: no milpac", http.StatusNotFound)
		return
	case milpacUnreadable:
		http.Error(w, "smoke: milpac unreadable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(smokeMilpac(discordID, smokePositions[state]))
}

// smokeMilpac is a milpac in the 7Cav API's shape, naming the account and
// holding position, with a placeholder name and rank.
func smokeMilpac(discordID, position string) map[string]any {
	return map[string]any{
		"user":      map[string]string{"username": "Smoke Milpac"},
		"realName":  "Smoke Milpac",
		"rank":      map[string]string{"rankShort": "PVT", "rankFull": "Private"},
		"primary":   map[string]string{"positionTitle": position},
		"discordId": discordID,
	}
}

func (c *cavAPIStandIn) setState(w http.ResponseWriter, r *http.Request) {
	id, state := r.FormValue("id"), r.FormValue("state")
	if !slices.Contains(milpacStates, state) {
		http.Error(w, fmt.Sprintf("state must be one of %s", strings.Join(milpacStates, ", ")), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	if state == milpacReal {
		delete(c.states, id)
	} else {
		c.states[id] = state
	}
	c.mu.Unlock()
	c.logf("7Cav API stand-in: milpac for %s set to %s", id, state)
	_, _ = fmt.Fprintln(w, state)
}
