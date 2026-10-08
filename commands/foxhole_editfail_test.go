package commands

import (
	"errors"
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// countMethod returns how many recorded calls used the given responder method.
func countMethod(calls []recordedCall, method string) int {
	n := 0
	for _, c := range calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// kvToMap folds a captured kv slice (key, value, key, value, ...) into a map for
// readable assertions on individual context fields.
func kvToMap(kv []any) map[string]any {
	m := map[string]any{}
	for idx := 0; idx+1 < len(kv); idx += 2 {
		if key, ok := kv[idx].(string); ok {
			m[key] = kv[idx+1]
		}
	}
	return m
}

// A Foxhole command whose reply, the edit of its deferred response, fails
// has already acknowledged the interaction, so it can neither send the
// edit again nor respond anew. It pages Sentry once, and makes no second
// attempt: one edit, and no response besides the acknowledgement. The
// capture names the registered command, and the subcommand and guild where
// the run has them, so on-call can tell which run lost its reply. Each row
// is one way a reply goes out: a plain edit, and an edit carrying the added
// members embed.
func TestFoxholeLostReplyIsCapturedOnceWithNoRetry(t *testing.T) {
	cases := []struct {
		name string
		gm   func() *fakeGuildManager
		run  func(*fakeResponder, *fakeGuildManager)
		want map[string]any
	}{
		{
			name: "remove",
			gm:   func() *fakeGuildManager { return foxholeRoleAddGM(nil) },
			run: func(f *fakeResponder, gm *fakeGuildManager) {
				runFoxhole(f, gm, nil, foxholeRemoveInteraction())
			},
			want: map[string]any{"command": "foxhole", "subcommand": "remove", "guild_id": "guild-1"},
		},
		{
			name: "bulkadd, with the added members embed",
			gm: func() *fakeGuildManager {
				gm := foxholeRoleAddGM(nil)
				gm.searchResults = map[string][]*discordgo.Member{"good": {{User: &discordgo.User{ID: "111", Username: "good"}}}}
				return gm
			},
			run: func(f *fakeResponder, gm *fakeGuildManager) {
				runFoxhole(f, gm, nil, bulkAddInteraction("internal", "good"))
			},
			want: map[string]any{"command": "foxhole", "subcommand": "bulkadd", "guild_id": "guild-1"},
		},
		{
			name: "roster add, with the added members embed",
			gm:   internalRoleGM,
			run: func(f *fakeResponder, gm *fakeGuildManager) {
				runFoxholeBulkAddInternal(f, gm, nil, slashNamed("foxhole-bulkadd-internal", stringOption("unit", "D/ACD")))
			},
			want: map[string]any{"command": "foxhole-bulkadd-internal", "guild_id": "guild-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serveRosterAndProfiles(t, liteRoster(liteMember("Trooper.A", "111111111111111111")), http.StatusOK, nil)
			rec := &captureRecorder{}
			rec.install(t)
			f := &fakeResponder{EditErrs: []error{errors.New("503 service unavailable")}}

			tc.run(f, tc.gm())

			calls := f.Calls()
			if got := countMethod(calls, "Edit"); got != 1 {
				t.Errorf("expected exactly 1 Edit (the failing call), got %d: %v", got, calls)
			}
			if got := countMethod(calls, "Respond"); got != 1 {
				t.Errorf("expected only the acknowledgement's Respond, got %d: %v", got, calls)
			}
			if rec.count != 1 {
				t.Fatalf("expected exactly 1 Sentry capture, got %d", rec.count)
			}
			got := kvToMap(rec.lastKV)
			for key, want := range tc.want {
				if got[key] != want {
					t.Errorf("capture context %s = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}
