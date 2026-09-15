package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// apiRank is one entry of the milpacs ranks endpoint as the test serves it.
type apiRank struct {
	RankShort        string `json:"rankShort"`
	RankFull         string `json:"rankFull"`
	RankID           string `json:"rankId"`
	RankDisplayOrder int    `json:"rankDisplayOrder"`
}

// apiLadder builds the ranks endpoint body from the code ladder: the 29
// abbreviations in ladder order with ascending display orders, plus the
// Tester entry the endpoint carries first, so a fixture never restates the
// ladder by hand.
func apiLadder() []apiRank {
	ranks := []apiRank{{RankShort: "32", RankFull: "Tester", RankID: "32", RankDisplayOrder: 1}}
	for i, rr := range tempVCRankRoles {
		ranks = append(ranks, apiRank{
			RankShort:        rr.abbrev,
			RankFull:         rr.abbrev + " full",
			RankID:           fmt.Sprint(i + 1),
			RankDisplayOrder: (i + 1) * 10,
		})
	}
	return ranks
}

// serveRanks points the 7Cav API client at a server that answers the ranks
// endpoint with status and body.
func serveRanks(t *testing.T, status int, ranks []apiRank) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/milpacs/ranks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"ranks": ranks})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
}

func TestCheckRankLadder_MatchingLadderDoesNotCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRanks(t, http.StatusOK, apiLadder())

	checkRankLadder(context.Background())

	if rec.count != 0 {
		t.Fatalf("a ladder that matches the API captured %d times; want 0 (kv %v)", rec.count, rec.lastKV)
	}
}

// kvMentions reports whether any value in a key/value slice, rendered with
// %v, contains needle. It checks that an entry reached the event without
// pinning the key it travelled under.
func kvMentions(kv []any, needle string) bool {
	for i := 1; i < len(kv); i += 2 {
		if strings.Contains(fmt.Sprintf("%v", kv[i]), needle) {
			return true
		}
	}
	return false
}

func TestCheckRankLadder_DriftCapturesOnceWithTheDifferingEntry(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]apiRank)
		expect string
	}{
		{
			name: "abbreviation replaced",
			mutate: func(ranks []apiRank) {
				for i := range ranks {
					if ranks[i].RankShort == "SGT" {
						ranks[i].RankShort = "SGX"
					}
				}
			},
			expect: "SGX",
		},
		{
			name: "two adjacent display orders swapped",
			mutate: func(ranks []apiRank) {
				var cpl, spc int
				for i := range ranks {
					switch ranks[i].RankShort {
					case "CPL":
						cpl = i
					case "SPC":
						spc = i
					}
				}
				ranks[cpl].RankDisplayOrder, ranks[spc].RankDisplayOrder = ranks[spc].RankDisplayOrder, ranks[cpl].RankDisplayOrder
			},
			expect: "SPC",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			ranks := apiLadder()
			tc.mutate(ranks)
			serveRanks(t, http.StatusOK, ranks)

			checkRankLadder(context.Background())

			if rec.count != 1 {
				t.Fatalf("drift captured %d times; want exactly 1", rec.count)
			}
			if !kvMentions(rec.lastKV, tc.expect) {
				t.Fatalf("capture does not carry the differing entry %q; kv %v", tc.expect, rec.lastKV)
			}
		})
	}
}

func TestCheckRankLadder_FetchFailureDoesNotCapture(t *testing.T) {
	rec := &captureRecorder{}
	rec.install(t)
	serveRanks(t, http.StatusInternalServerError, nil)

	checkRankLadder(context.Background())

	if rec.count != 0 {
		t.Fatalf("a failed ranks fetch captured %d times; want 0 (the check could not run)", rec.count)
	}
}

func TestCheckAdministrator(t *testing.T) {
	const botID = "bot-user"
	adminRole := &discordgo.Role{ID: "role-admin", Permissions: discordgo.PermissionAdministrator}
	plainRole := &discordgo.Role{ID: "role-plain", Permissions: discordgo.PermissionViewChannel}
	guild := &discordgo.Guild{ID: testTempVCGuild, OwnerID: "someone-else", Roles: []*discordgo.Role{adminRole, plainRole}}

	cases := []struct {
		name         string
		member       *discordgo.Member
		memberErr    error
		wantCaptures int
	}{
		{
			name:         "a role with Administrator",
			member:       &discordgo.Member{User: &discordgo.User{ID: botID}, Roles: []string{"role-plain", "role-admin"}},
			wantCaptures: 0,
		},
		{
			name:         "no role with Administrator",
			member:       &discordgo.Member{User: &discordgo.User{ID: botID}, Roles: []string{"role-plain"}},
			wantCaptures: 1,
		},
		{
			name:         "member fetch fails",
			memberErr:    errors.New("dial tcp: connection refused"),
			wantCaptures: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			mgr := newFakeTempVCManager()
			mgr.member = tc.member
			mgr.memberErr = tc.memberErr
			mgr.guild = guild

			checkAdministrator(mgr, testTempVCGuild, botID)

			if rec.count != tc.wantCaptures {
				t.Fatalf("captured %d times; want %d (kv %v)", rec.count, tc.wantCaptures, rec.lastKV)
			}
		})
	}
}
