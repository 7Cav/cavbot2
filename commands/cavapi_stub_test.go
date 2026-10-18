package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// stubLOA is one canned LOA record the stub 7Cav API serves. Dates are
// YYYY-MM-DD or empty, as the API sends them. The stub works out the state
// from the dates against its own UTC today, unless needsReview is set.
type stubLOA struct {
	threadID     string
	postDate     time.Time
	userID       string
	username     string
	start, end   string
	needsReview  bool
	reviewReason string
}

// stubCavAPI stands in for the 7Cav API in command tests. It serves the
// roster for every position search, and GET /loas from canned records by
// the window rules the API documents for that call. Its UTC today defaults
// to the UTC date of the test's pinned clock, and a test can move it apart.
type stubCavAPI struct {
	mu           sync.Mutex
	roster       utils.LiteRosterResponse
	rosterStatus int
	loas         []stubLOA
	loaStatus    int
	today        time.Time
	// delay holds back the answer to each roster and LOA call.
	delay time.Duration
	// rosterCall and loasCall pair the two calls when pairCalls has set them.
	rosterCall, loasCall *stubArrival
}

// stubArrival marks one call's arrival for its partner to wait on.
type stubArrival struct {
	once sync.Once
	ch   chan struct{}
}

// stubPairWait is how long a paired call waits for its partner.
const stubPairWait = 2 * time.Second

// awaitPartner marks mine as arrived and waits for partner, reporting
// whether partner arrived within stubPairWait. Unpaired calls never wait.
func awaitPartner(mine, partner *stubArrival) bool {
	if mine == nil {
		return true
	}
	mine.once.Do(func() { close(mine.ch) })
	select {
	case <-partner.ch:
		return true
	case <-time.After(stubPairWait):
		return false
	}
}

// serveCavAPI starts a stub 7Cav API serving roster and loas, with its
// today set to now's UTC date, and points the bot's API client at it for
// the rest of the test.
func serveCavAPI(t *testing.T, now time.Time, roster utils.LiteRosterResponse, loas ...stubLOA) *stubCavAPI {
	t.Helper()
	api := &stubCavAPI{roster: roster, loas: loas, today: stubUTCDate(now)}
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
	return api
}

// setToday sets the API's UTC today to date's UTC date.
func (a *stubCavAPI) setToday(date time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.today = stubUTCDate(date)
}

// setLOAs replaces the canned LOA records.
func (a *stubCavAPI) setLOAs(loas ...stubLOA) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loas = loas
}

// failLOAs makes GET /loas answer status with an error body.
func (a *stubCavAPI) failLOAs(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loaStatus = status
}

// pairCalls makes the roster call and the LOA call each wait until the other
// arrives, and answer 500 when the other hasn't come within stubPairWait. A
// bot that makes the two calls one after the other gets a 500 for the
// first.
func (a *stubCavAPI) pairCalls() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rosterCall = &stubArrival{ch: make(chan struct{})}
	a.loasCall = &stubArrival{ch: make(chan struct{})}
}

// delayCalls makes the stub hold back its answer to each roster and LOA
// call by d.
func (a *stubCavAPI) delayCalls(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.delay = d
}

// failRoster makes every position search answer status.
func (a *stubCavAPI) failRoster(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rosterStatus = status
}

func (a *stubCavAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	delay := a.delay
	a.mu.Unlock()
	time.Sleep(delay)
	switch {
	case strings.HasPrefix(r.URL.Path, "/milpacs/position/search/"):
		a.serveRoster(w)
	case r.URL.Path == "/loas":
		a.serveLOAs(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *stubCavAPI) serveRoster(w http.ResponseWriter) {
	a.mu.Lock()
	status, roster := a.rosterStatus, a.roster
	mine, partner := a.rosterCall, a.loasCall
	a.mu.Unlock()
	if !awaitPartner(mine, partner) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	writeStubJSON(w, http.StatusOK, roster)
}

// serveLOAs answers GET /loas as the API documents it. Without endedSince
// it serves open and upcoming LOAs, and needs-review LOAs posted in the 14
// days before today. With endedSince it adds the LOAs that ended on or after
// it and the needs-review LOAs posted on or after it, and it answers 400 to
// a date more than 365 days before today.
func (a *stubCavAPI) serveLOAs(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	status, today := a.loaStatus, a.today
	loas := append([]stubLOA(nil), a.loas...)
	mine, partner := a.loasCall, a.rosterCall
	a.mu.Unlock()
	if !awaitPartner(mine, partner) {
		writeStubError(w, http.StatusInternalServerError)
		return
	}
	if status != 0 {
		writeStubError(w, status)
		return
	}

	var endedSince time.Time
	if raw := r.URL.Query().Get("endedSince"); raw != "" {
		parsed, err := time.Parse(time.DateOnly, raw)
		if err != nil || today.Sub(parsed) > 365*24*time.Hour {
			writeStubError(w, http.StatusBadRequest)
			return
		}
		endedSince = parsed
	}

	served := []map[string]any{}
	for _, l := range loas {
		state := l.state(today)
		switch state {
		case "LOA_STATE_ENDED":
			if endedSince.IsZero() || l.end < endedSince.Format(time.DateOnly) {
				continue
			}
		case "LOA_STATE_NEEDS_REVIEW":
			posted := stubUTCDate(l.postDate)
			recent := !posted.Before(today.AddDate(0, 0, -14))
			sinceEnded := !endedSince.IsZero() && !posted.Before(endedSince)
			if !recent && !sinceEnded {
				continue
			}
		}
		reviewReason := "LOA_REVIEW_REASON_UNSPECIFIED"
		if l.needsReview {
			reviewReason = l.reviewReason
		}
		served = append(served, map[string]any{
			"threadId": l.threadID,
			"forumUrl": "",
			"postDate": l.postDate.Unix(),
			"forum":    "LOA_FORUM_1_7",
			"subject": map[string]any{
				"userId":   l.userID,
				"username": l.username,
				"milpacId": "0",
			},
			"startDate":    l.start,
			"endDate":      l.end,
			"state":        state,
			"reviewReason": reviewReason,
			"reason":       "",
		})
	}
	writeStubJSON(w, http.StatusOK, map[string]any{"loas": served})
}

// state is the LOA's state on today, counted in UTC calendar days: open from
// its start date through its end date, upcoming before, ended after.
func (l stubLOA) state(today time.Time) string {
	day := today.Format(time.DateOnly)
	switch {
	case l.needsReview:
		return "LOA_STATE_NEEDS_REVIEW"
	case l.end < day:
		return "LOA_STATE_ENDED"
	case l.start > day:
		return "LOA_STATE_UPCOMING"
	default:
		return "LOA_STATE_OPEN"
	}
}

// writeStubError answers status with the API's error body.
func writeStubError(w http.ResponseWriter, status int) {
	writeStubJSON(w, status, map[string]any{"code": 2, "message": "stub 7Cav API error", "details": []any{}})
}

func writeStubJSON(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(fmt.Sprintf("stub 7Cav API: marshal: %v", err))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func stubUTCDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
