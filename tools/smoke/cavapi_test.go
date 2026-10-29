//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// The bearer token the bot's 7Cav API client sends in these tests.
const testBearer = "smoke-test-bearer"

// upstreamTrooper is the position the fake upstream's milpacs hold, so a
// read can tell the real API's answer from a smoke milpac.
const upstreamTrooper = "1/A/1-7"

// fakeUpstream answers in the real 7Cav API's place. A Discord ID lookup
// gets a milpac naming that ID; any other path gets one body every lookup
// decodes, marked "upstream" in each lookup's own field.
type fakeUpstream struct {
	srv *httptest.Server

	mu     sync.Mutex
	bearer string
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/milpac/discord/{id}", func(w http.ResponseWriter, r *http.Request) {
		u.saw(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"discordId": r.PathValue("id"),
			"realName":  "Upstream Trooper",
			"primary":   map[string]string{"positionTitle": upstreamTrooper},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u.saw(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"realName": "upstream",
			"profiles": map[string]any{"upstream": map[string]any{}},
			"loas":     []map[string]string{{"threadId": "upstream"}},
			"ranks":    []map[string]string{{"rankShort": "upstream"}},
		})
	})
	u.srv = httptest.NewServer(mux)
	t.Cleanup(u.srv.Close)
	return u
}

func (u *fakeUpstream) saw(r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.bearer = r.Header.Get("Authorization")
}

func (u *fakeUpstream) lastBearer() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.bearer
}

// pointBotAt sends the bot's 7Cav API lookups to origin, as CAV_API_BASE_URL
// does in a run, with testBearer as their token. The lookups' log lines go
// nowhere.
func pointBotAt(t *testing.T, origin string) {
	t.Helper()
	prevLogger := utils.Logger
	t.Cleanup(func() { utils.Logger = prevLogger })
	utils.Logger = slog.New(slog.DiscardHandler)
	t.Setenv("BEARER", testBearer)
	t.Setenv("CAV_API_BASE_URL", origin)
	utils.InitAPIBaseURL()
	t.Cleanup(utils.InitAPIBaseURL)
}

// newStandInUnderTest is the stand-in behind a test server, forwarding to a
// fake upstream, with the bot's client pointed at it.
func newStandInUnderTest(t *testing.T) (*httptest.Server, *fakeUpstream) {
	t.Helper()
	up := newFakeUpstream(t)
	srv := httptest.NewServer(newCavAPIStandIn(up.srv.URL, t.Logf, func(map[string]string) {}).handler())
	t.Cleanup(srv.Close)
	pointBotAt(t, srv.URL)
	return srv, up
}

func TestStandInGivesTheRealAnswerForAnAccountWithNoSetState(t *testing.T) {
	_, up := newStandInUnderTest(t)

	milpac, err := utils.GetMilpacByDiscordID(context.Background(), "111")
	if err != nil {
		t.Fatal(err)
	}
	if milpac.DiscordID != "111" || milpac.Primary.PositionTitle != upstreamTrooper {
		t.Errorf("read %q at %q, want the upstream's milpac for 111", milpac.DiscordID, milpac.Primary.PositionTitle)
	}
	if got := up.lastBearer(); got != "Bearer "+testBearer {
		t.Errorf("upstream got Authorization %q, want the bot's bearer token", got)
	}
}

// setMilpac sets what the stand-in at srv answers for the account, as
// `smoke milpac` does.
func setMilpac(t *testing.T, srv *httptest.Server, discordID, state string) {
	t.Helper()
	res, err := http.PostForm(srv.URL+"/smoke/milpac", url.Values{"id": {discordID}, "state": {state}})
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set %s to %s answered %d", discordID, state, res.StatusCode)
	}
}

// isDischarge is a discharge as GLOSSARY.md defines it: a milpac whose
// position is Discharged or Dishonorably Discharged.
func isDischarge(position string) bool {
	return position == "Discharged" || position == "Dishonorably Discharged"
}

func TestStandInAnswersASetAccountsLookupFromItsState(t *testing.T) {
	for _, tc := range []struct {
		state         string
		wantDischarge bool
	}{
		{"active", false},
		{"discharged", true},
		{"dishonorably-discharged", true},
		{"retired", false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			srv, _ := newStandInUnderTest(t)
			setMilpac(t, srv, "111", tc.state)

			milpac, err := utils.GetMilpacByDiscordID(context.Background(), "111")
			if err != nil {
				t.Fatal(err)
			}
			if milpac.DiscordID != "111" {
				t.Errorf("milpac names Discord ID %q, want 111", milpac.DiscordID)
			}
			if milpac.Primary.PositionTitle == upstreamTrooper {
				t.Errorf("read the upstream's milpac, want a smoke milpac")
			}
			if got := isDischarge(milpac.Primary.PositionTitle); got != tc.wantDischarge {
				t.Errorf("position %q is a discharge: %v, want %v", milpac.Primary.PositionTitle, got, tc.wantDischarge)
			}
		})
	}
	t.Run("discharged and dishonorably-discharged are each their own discharge", func(t *testing.T) {
		srv, _ := newStandInUnderTest(t)
		setMilpac(t, srv, "111", "discharged")
		setMilpac(t, srv, "222", "dishonorably-discharged")
		for id, want := range map[string]string{"111": "Discharged", "222": "Dishonorably Discharged"} {
			milpac, err := utils.GetMilpacByDiscordID(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if milpac.Primary.PositionTitle != want {
				t.Errorf("%s: position %q, want %q", id, milpac.Primary.PositionTitle, want)
			}
		}
	})
	t.Run("none", func(t *testing.T) {
		srv, _ := newStandInUnderTest(t)
		setMilpac(t, srv, "111", "none")
		if _, err := utils.GetMilpacByDiscordID(context.Background(), "111"); !errors.Is(err, utils.ErrNotFound) {
			t.Errorf("read gave %v, want no milpac found", err)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		srv, _ := newStandInUnderTest(t)
		setMilpac(t, srv, "111", "unreadable")
		if _, err := utils.GetMilpacByDiscordID(context.Background(), "111"); err == nil || errors.Is(err, utils.ErrNotFound) {
			t.Errorf("read gave %v, want a failed read", err)
		}
	})
}

func TestStandInGivesTheRealAnswerAgainOnceAnAccountIsSetToReal(t *testing.T) {
	srv, _ := newStandInUnderTest(t)
	setMilpac(t, srv, "111", "discharged")
	setMilpac(t, srv, "111", "real")

	milpac, err := utils.GetMilpacByDiscordID(context.Background(), "111")
	if err != nil {
		t.Fatal(err)
	}
	if milpac.Primary.PositionTitle != upstreamTrooper {
		t.Errorf("read position %q, want the upstream's milpac", milpac.Primary.PositionTitle)
	}
}

// Each lookup that takes an identifier gets the set account's Discord ID as
// it, so a stand-in that matched on the ID alone would answer it.
func TestStandInForwardsEveryOtherLookupForASetAccountToo(t *testing.T) {
	srv, _ := newStandInUnderTest(t)
	setMilpac(t, srv, "111", "none")
	ctx := context.Background()

	for _, tc := range []struct {
		lookup string
		read   func() (string, error)
	}{
		{"username", func() (string, error) {
			p, err := utils.GetMilpacByUsername(ctx, "111")
			if err != nil {
				return "", err
			}
			return p.RealName, nil
		}},
		{"gamertag", func() (string, error) {
			p, err := utils.GetUserByGamertag(ctx, "111")
			if err != nil {
				return "", err
			}
			return p.RealName, nil
		}},
		{"roster", func() (string, error) {
			r, err := utils.GetRosterByFuzzyPositionSearch(ctx, "111")
			if err != nil {
				return "", err
			}
			for key := range r.LiteProfiles {
				return key, nil
			}
			return "", nil
		}},
		{"LOAs", func() (string, error) {
			r, err := utils.GetLOAs(ctx, time.Now())
			if err != nil || len(r.LOAs) == 0 {
				return "", err
			}
			return r.LOAs[0].ThreadID, nil
		}},
		{"ranks", func() (string, error) {
			r, err := utils.GetRanks(ctx)
			if err != nil || len(r.Ranks) == 0 {
				return "", err
			}
			return r.Ranks[0].RankShort, nil
		}},
	} {
		got, err := tc.read()
		if err != nil || got != "upstream" {
			t.Errorf("%s lookup read %q (%v), want the upstream's answer", tc.lookup, got, err)
		}
	}
}

func TestSmokeMilpacFailsWhenNoRunIsUp(t *testing.T) {
	p := statePaths{t.TempDir()}
	if err := setMilpacState(p, []string{"111", "discharged"}); !errors.Is(err, errNoRun) {
		t.Errorf("smoke milpac with no run up gave %v, want %v", err, errNoRun)
	}
}

func TestSmokeMilpacListsTheValidStatesForAnUnknownOne(t *testing.T) {
	p := statePaths{t.TempDir()}
	err := setMilpacState(p, []string{"111", "gone"})
	if !errors.Is(err, errUnknownMilpacState) {
		t.Fatalf("smoke milpac with an unknown state gave %v, want %v", err, errUnknownMilpacState)
	}
	for _, state := range []string{"active", "discharged", "dishonorably-discharged", "retired", "none", "unreadable", "real"} {
		if !strings.Contains(err.Error(), state) {
			t.Errorf("%q does not list the state %s", err, state)
		}
	}
}

// holdRunLock holds the run lock in p for the test, as a run's supervisor
// does for its whole life.
func holdRunLock(t *testing.T, p statePaths) {
	t.Helper()
	lock, ok, err := tryLock(p.lock())
	if err != nil || !ok {
		t.Fatalf("take the run lock: ok %v, %v", ok, err)
	}
	t.Cleanup(func() { _ = lock.Close() })
}

// startedSupervisor is a run's supervisor, holding the run lock, that has
// started the 7Cav API stand-in as up does with these --env flags and env
// file. The test's end stops the run.
func startedSupervisor(t *testing.T, extra []string, file map[string]string) *supervisor {
	t.Helper()
	s := &supervisor{opts: upOptions{extra: extra}, paths: statePaths{t.TempDir()}}
	holdRunLock(t, s.paths)
	s.save()
	if err := s.startCavAPI(file); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.teardown)
	return s
}

func TestSmokeMilpacFailsOnARunWhoseEnvNamesThe7CavAPI(t *testing.T) {
	s := startedSupervisor(t, []string{"CAV_API_BASE_URL=http://127.0.0.1:1"}, nil)
	if err := setMilpacState(s.paths, []string{"111", "discharged"}); !errors.Is(err, errNoStandIn) {
		t.Errorf("smoke milpac gave %v, want %v", err, errNoStandIn)
	}
}

// standInOrigin is the stand-in's origin as the run file records it for
// `smoke milpac` and the bot's environment.
func standInOrigin(t *testing.T, s *supervisor) string {
	t.Helper()
	st, ok := readRun(s.paths.runFile())
	if !ok || st.CavAPIURL == "" {
		t.Fatalf("the run file names no stand-in: %+v", st)
	}
	return st.CavAPIURL
}

// The stand-in forwards to the API the bot would have reached without it,
// which the env file can name.
func TestStandInForwardsToTheEnvFiles7CavAPI(t *testing.T) {
	up := newFakeUpstream(t)
	s := startedSupervisor(t, nil, map[string]string{"CAV_API_BASE_URL": up.srv.URL + "/"})
	pointBotAt(t, standInOrigin(t, s))

	milpac, err := utils.GetMilpacByDiscordID(context.Background(), "111")
	if err != nil {
		t.Fatal(err)
	}
	if milpac.Primary.PositionTitle != upstreamTrooper {
		t.Errorf("read position %q, want the env file's API's milpac", milpac.Primary.PositionTitle)
	}
}

func TestStandInListensOnLoopbackOnly(t *testing.T) {
	s := startedSupervisor(t, nil, nil)
	origin, err := url.Parse(standInOrigin(t, s))
	if err != nil {
		t.Fatal(err)
	}
	port := origin.Port()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), time.Second)
	if err != nil {
		t.Fatalf("the stand-in is not reachable on loopback: %v", err)
	}
	_ = conn.Close()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	tried := 0
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		tried++
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort(ipnet.IP.String(), port), time.Second); err == nil {
			_ = conn.Close()
			t.Errorf("the stand-in answers on %s", ipnet.IP)
		}
	}
	if tried == 0 {
		t.Skip("this host has no address off loopback to try")
	}
}

// statusHasLine reports whether some line of status output names every
// one of words.
func statusHasLine(out string, words ...string) bool {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		all := true
		for _, w := range words {
			all = all && slices.Contains(fields, w)
		}
		if all {
			return true
		}
	}
	return false
}

func TestStatusListsEachSetMilpacStateWhileTheRunIsUp(t *testing.T) {
	s := newStubSupervisor(t)
	holdRunLock(t, s.paths)
	s.save()
	if err := s.startCavAPI(nil); err != nil {
		t.Fatal(err)
	}
	watchStub(t, s, stubLateLogin, nil)
	for _, set := range [][]string{{"111", "discharged"}, {"222", "none"}, {"333", "retired"}, {"333", "real"}} {
		if err := setMilpacState(s.paths, set); err != nil {
			t.Fatalf("smoke milpac %v: %v", set, err)
		}
	}

	var out strings.Builder
	listed := eventually(stubWait, func() bool {
		out.Reset()
		if err := printStatus(&out, s.paths); err != nil {
			t.Fatal(err)
		}
		return statusHasLine(out.String(), "111", "discharged") && statusHasLine(out.String(), "222", "none")
	})
	if !listed {
		t.Errorf("status does not list 111 discharged and 222 none:\n%s", out.String())
	}
	if strings.Contains(out.String(), "333") {
		t.Errorf("status lists 333, which is back to real:\n%s", out.String())
	}
}
