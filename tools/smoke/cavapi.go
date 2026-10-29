//go:build unix

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// publicCavAPI is the 7Cav API's public origin, which the bot uses when
// CAV_API_BASE_URL is unset.
const publicCavAPI = "https://api.7cav.us"

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
	// changed gets the set states each time they change. It runs with mu
	// held, so it gets them in order, and must not wait.
	changed func(states map[string]string)

	mu     sync.Mutex
	states map[string]string
}

// newCavAPIStandIn forwards to upstream, the real API's origin, and hands
// changed the set states each time they change.
func newCavAPIStandIn(upstream string, logf func(string, ...any), changed func(map[string]string)) *cavAPIStandIn {
	return &cavAPIStandIn{upstream: upstream, logf: logf, changed: changed, states: map[string]string{}}
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
	c.changed(maps.Clone(c.states))
	c.mu.Unlock()
	c.logf("7Cav API stand-in: milpac for %s set to %s", id, state)
	_, _ = fmt.Fprintln(w, state)
}

// What `smoke milpac` fails with, besides a failed request.
var (
	errNoRun              = errors.New("no smoke run is up")
	errNoStandIn          = errors.New("the run has no 7Cav API stand-in: --env CAV_API_BASE_URL sent its lookups elsewhere, or it is still starting")
	errUnknownMilpacState = fmt.Errorf("the state must be one of %s", strings.Join(milpacStates, ", "))
)

func cmdMilpac(args []string) error {
	g, err := findGitPaths()
	if err != nil {
		return err
	}
	return setMilpacState(statePaths{g.stateDir()}, args)
}

// setMilpacState sets what the run's stand-in answers for a Discord ID's
// milpac, from args: the ID and a state.
func setMilpacState(p statePaths, args []string) error {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: go run ./tools/smoke milpac <discord-id> %s\n", strings.Join(milpacStates, "|"))
		return errUsage
	}
	if !slices.Contains(milpacStates, args[1]) {
		return fmt.Errorf("%q: %w", args[1], errUnknownMilpacState)
	}
	if held, _ := runHeld(p); !held {
		return errNoRun
	}
	st, _ := readRun(p.runFile())
	if st.CavAPIURL == "" {
		return errNoStandIn
	}
	body, err := postControl("the 7Cav API stand-in", st.CavAPIURL+"/smoke/milpac", url.Values{"id": {args[0]}, "state": {args[1]}})
	if err != nil {
		return err
	}
	fmt.Printf("milpac for %s: %s", args[0], body)
	return nil
}

// startCavAPI starts the stand-in for the run on a free loopback port,
// unless --env names the 7Cav API's origin. It forwards to the API the bot
// would reach without it: the env file's CAV_API_BASE_URL, or the public
// address.
func (s *supervisor) startCavAPI(file map[string]string) error {
	if origin, own := envValue(s.opts.extra, "CAV_API_BASE_URL"); own {
		s.logf("7Cav API at %s from --env, with no stand-in", origin)
		return nil
	}
	upstream := strings.TrimRight(file["CAV_API_BASE_URL"], "/")
	if upstream == "" {
		upstream = publicCavAPI
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("7Cav API stand-in: %w", err)
	}
	s.milpacs = make(chan map[string]string, 1)
	standIn := newCavAPIStandIn(upstream, s.logf, func(states map[string]string) { sendLatest(s.milpacs, states) })
	s.cavAPI = &http.Server{Handler: standIn.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.cavAPI.Serve(ln) }()
	s.st.CavAPIURL = "http://" + ln.Addr().String()
	s.save()
	s.logf("7Cav API stand-in on %s, forwarding to %s", s.st.CavAPIURL, upstream)
	return nil
}
