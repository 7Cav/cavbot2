package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

const (
	testBotUserID   = "bot-user-1"
	testAdminRoleID = "role-admin"
	testPlainRoleID = "role-plain"
)

// capturedEvent is one call through the Sentry seam.
type capturedEvent struct {
	err error
	kv  []any
}

// recordCaptures swaps the Sentry seam for a recorder and restores it after
// the test. The checks run synchronously in these tests, so no lock is needed.
func recordCaptures(t *testing.T) *[]capturedEvent {
	t.Helper()
	prev := captureError
	var events []capturedEvent
	captureError = func(_ string, err error, kv ...any) {
		events = append(events, capturedEvent{err: err, kv: kv})
	}
	t.Cleanup(func() { captureError = prev })
	return &events
}

// testerRank is the API's non-rank entry as the live endpoint served it on
// 2026-09-17: rankShort "32", rankFull "Tester", rankId "32", first in the list.
var testerRank = utils.Rank{RankShort: "32", RankFull: "Tester", RankID: "32"}

// apiRanks builds the body the live endpoint serves today: Tester first, then
// one entry per ladder row in ladder order. Built from the ladder, not a
// snapshot, so a ladder edit never reddens these tests by itself.
func apiRanks() []utils.Rank {
	out := []utils.Rank{testerRank}
	for _, rr := range tempVCRankRoles {
		out = append(out, utils.Rank{RankShort: rr.abbrev, RankFull: rr.abbrev, RankID: rr.roleID})
	}
	return out
}

// serveRanks points the 7Cav API client at a server that answers every
// request with the given ranks body.
func serveRanks(t *testing.T, ranks []utils.Rank) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(utils.RanksResponse{Ranks: ranks})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

// serveRanksStatus points the 7Cav API client at a server that answers every
// request with the given status and no body.
func serveRanksStatus(t *testing.T, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

// testGuild returns a guild whose role list holds one role with Administrator
// and one without. The @everyone role carries the guild's ID, as on Discord.
func testGuild() *discordgo.Guild {
	return &discordgo.Guild{
		ID: testTempVCGuild,
		Roles: []*discordgo.Role{
			{ID: testTempVCGuild, Permissions: discordgo.PermissionViewChannel},
			{ID: testAdminRoleID, Permissions: discordgo.PermissionAdministrator},
			{ID: testPlainRoleID, Permissions: discordgo.PermissionManageChannels},
		},
	}
}

// botMember returns the bot's own guild member holding the given roles.
func botMember(roles ...string) *discordgo.Member {
	return &discordgo.Member{User: &discordgo.User{ID: testBotUserID}, Roles: roles}
}

// healthyManager returns a fake whose guild and member say the bot holds
// Administrator.
func healthyManager() *fakeTempVCManager {
	f := newFakeTempVCManager()
	f.guild = testGuild()
	f.member = botMember(testAdminRoleID)
	return f
}

// rankIndex returns the position of abbrev in ranks, or fails the test so a
// mutation never silently serves an unchanged body.
func rankIndex(t *testing.T, ranks []utils.Rank, abbrev string) int {
	t.Helper()
	for i, r := range ranks {
		if r.RankShort == abbrev {
			return i
		}
	}
	t.Fatalf("rank %q is not in the served body", abbrev)
	return -1
}

// driftOf returns the drift list the capture carries, found by type so no
// key name is asserted, or fails the test when the capture carries none.
func driftOf(t *testing.T, ev capturedEvent) []rankDrift {
	t.Helper()
	for _, v := range ev.kv {
		if d, ok := v.([]rankDrift); ok {
			return d
		}
	}
	t.Fatalf("capture carries no drift list: %v", ev.kv)
	return nil
}

// singleCapture fails the test unless exactly one capture was recorded and it
// wraps want, then returns it.
func singleCapture(t *testing.T, events []capturedEvent, want error) capturedEvent {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("captures = %d, want 1", len(events))
	}
	if !errors.Is(events[0].err, want) {
		t.Fatalf("captured error = %v, want %v", events[0].err, want)
	}
	return events[0]
}

func runChecks(mgr TempVCManager) {
	RunStartupChecks(context.Background(), mgr, testTempVCGuild, testBotUserID)
}

func TestStartupChecksHealthyGuildCapturesNothing(t *testing.T) {
	events := recordCaptures(t)
	serveRanks(t, apiRanks())

	runChecks(healthyManager())

	if len(*events) != 0 {
		t.Fatalf("captures = %d, want 0", len(*events))
	}
}

func TestStartupChecksRenamedRankCapturesTheDifferingEntry(t *testing.T) {
	events := recordCaptures(t)
	ranks := apiRanks()
	ranks[rankIndex(t, ranks, "MAJ")].RankShort = "MJR"
	serveRanks(t, ranks)

	runChecks(healthyManager())

	ev := singleCapture(t, *events, errRankLadderDrift)
	want := []rankDrift{{Code: "MAJ", API: "MJR"}}
	if got := driftOf(t, ev); !reflect.DeepEqual(got, want) {
		t.Fatalf("drift = %v, want %v", got, want)
	}
}

func TestStartupChecksSwappedRanksCaptureBothEntries(t *testing.T) {
	events := recordCaptures(t)
	ranks := apiRanks()
	i, j := rankIndex(t, ranks, "SGT"), rankIndex(t, ranks, "CPL")
	ranks[i], ranks[j] = ranks[j], ranks[i]
	serveRanks(t, ranks)

	runChecks(healthyManager())

	ev := singleCapture(t, *events, errRankLadderDrift)
	// The two pairs as a set: which one the walk reports first is not a
	// contract.
	want := map[rankDrift]bool{{Code: "SGT", API: "CPL"}: true, {Code: "CPL", API: "SGT"}: true}
	got := make(map[rankDrift]bool)
	for _, d := range driftOf(t, ev) {
		got[d] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drift = %v, want %v", got, want)
	}
}

func TestStartupChecksRankMissingFromAPICapturesIt(t *testing.T) {
	events := recordCaptures(t)
	ranks := apiRanks()
	ranks = ranks[:rankIndex(t, ranks, "RCT")]
	serveRanks(t, ranks)

	runChecks(healthyManager())

	ev := singleCapture(t, *events, errRankLadderDrift)
	got := driftOf(t, ev)
	if len(got) != 1 || got[0].Code != "RCT" {
		t.Fatalf("drift = %v, want one entry with code RCT", got)
	}
}

func TestStartupChecksFailedRankFetchStillReportsMissingAdministrator(t *testing.T) {
	events := recordCaptures(t)
	serveRanksStatus(t, http.StatusInternalServerError)
	mgr := healthyManager()
	mgr.member = botMember(testPlainRoleID)

	runChecks(mgr)

	singleCapture(t, *events, errAdministratorMissing)
}

func TestStartupChecksFailedMemberOrGuildFetchCapturesNothing(t *testing.T) {
	cases := []struct {
		name string
		arm  func(*fakeTempVCManager)
	}{
		{"member fetch fails", func(f *fakeTempVCManager) { f.memberErr = errors.New("connection reset") }},
		{"guild fetch fails", func(f *fakeTempVCManager) { f.guildErr = errors.New("connection reset") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := recordCaptures(t)
			serveRanks(t, apiRanks())
			mgr := healthyManager()
			tc.arm(mgr)

			runChecks(mgr)

			if len(*events) != 0 {
				t.Fatalf("captures = %d, want 0", len(*events))
			}
		})
	}
}
