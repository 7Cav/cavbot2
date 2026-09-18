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

// driftOf returns the drift list the capture's key/values carry, found by
// type so no key name is asserted, or fails the test when there is none.
func driftOf(t *testing.T, kv []any) []rankDrift {
	t.Helper()
	for _, v := range kv {
		if d, ok := v.([]rankDrift); ok {
			return d
		}
	}
	t.Fatalf("capture carries no drift list: %v", kv)
	return nil
}

// recordCaptures swaps the Sentry seam for a recorder for the rest of the
// test. The checks run synchronously here, so no lock is needed.
func recordCaptures(t *testing.T) *captureRecorder {
	t.Helper()
	rec := &captureRecorder{}
	rec.install(t)
	return rec
}

// singleCapture fails the test unless exactly one capture was recorded and
// its error wraps want, then returns that capture's key/values.
func singleCapture(t *testing.T, rec *captureRecorder, want error) []any {
	t.Helper()
	if rec.count != 1 {
		t.Fatalf("captures = %d, want 1", rec.count)
	}
	if !errors.Is(rec.errs[0], want) {
		t.Fatalf("captured error = %v, want %v", rec.errs[0], want)
	}
	return rec.kvs[0]
}

func runChecks(mgr TempVCManager) {
	RunStartupChecks(context.Background(), mgr, testTempVCGuild, testBotUserID)
}

func TestStartupChecksHealthyGuildCapturesNothing(t *testing.T) {
	rec := recordCaptures(t)
	serveRanks(t, apiRanks())

	runChecks(healthyManager())

	if rec.count != 0 {
		t.Fatalf("captures = %d, want 0", rec.count)
	}
}

func TestStartupChecksRenamedRankCapturesTheDifferingEntry(t *testing.T) {
	rec := recordCaptures(t)
	ranks := apiRanks()
	ranks[rankIndex(t, ranks, "MAJ")].RankShort = "MJR"
	serveRanks(t, ranks)

	runChecks(healthyManager())

	kv := singleCapture(t, rec, errRankLadderDrift)
	want := []rankDrift{{Code: "MAJ", API: "MJR"}}
	if got := driftOf(t, kv); !reflect.DeepEqual(got, want) {
		t.Fatalf("drift = %v, want %v", got, want)
	}
}

func TestStartupChecksSwappedRanksCaptureBothEntries(t *testing.T) {
	rec := recordCaptures(t)
	ranks := apiRanks()
	i, j := rankIndex(t, ranks, "SGT"), rankIndex(t, ranks, "CPL")
	ranks[i], ranks[j] = ranks[j], ranks[i]
	serveRanks(t, ranks)

	runChecks(healthyManager())

	kv := singleCapture(t, rec, errRankLadderDrift)
	// The two pairs as a set. Which one the walk reports first is not a
	// contract.
	want := map[rankDrift]bool{{Code: "SGT", API: "CPL"}: true, {Code: "CPL", API: "SGT"}: true}
	got := make(map[rankDrift]bool)
	for _, d := range driftOf(t, kv) {
		got[d] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drift = %v, want %v", got, want)
	}
}

func TestStartupChecksRankMissingFromAPICapturesIt(t *testing.T) {
	rec := recordCaptures(t)
	ranks := apiRanks()
	// Drop the last entry by position, so a rank added below the current
	// lowest one never reddens this test.
	last := ranks[len(ranks)-1].RankShort
	serveRanks(t, ranks[:len(ranks)-1])

	runChecks(healthyManager())

	kv := singleCapture(t, rec, errRankLadderDrift)
	got := driftOf(t, kv)
	if len(got) != 1 || got[0].Code != last {
		t.Fatalf("drift = %v, want one entry with code %s", got, last)
	}
}

func TestStartupChecksFailedRankFetchStillReportsMissingAdministrator(t *testing.T) {
	rec := recordCaptures(t)
	serveRanksStatus(t, http.StatusInternalServerError)
	mgr := healthyManager()
	mgr.member = botMember(testPlainRoleID)

	runChecks(mgr)

	singleCapture(t, rec, errAdministratorMissing)
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
			rec := recordCaptures(t)
			serveRanks(t, apiRanks())
			mgr := healthyManager()
			tc.arm(mgr)

			runChecks(mgr)

			if rec.count != 0 {
				t.Fatalf("captures = %d, want 0", rec.count)
			}
		})
	}
}
