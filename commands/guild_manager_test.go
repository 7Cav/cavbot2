package commands

import (
	"net/http"
	"testing"
)

// A member role change that meets a rate limit waits out the wait Discord
// gives and goes through, so a Foxhole action over many members fails none
// of them on a bucket that resets in seconds. A regression pin: the role
// calls have retried a 429 since the production adapter took the session's
// default. The temp VC adapter turns the retry off on every call but one,
// and a role call that copied it would fail here.
func TestSessionGuildManagerRoleChangesWaitOutARateLimit(t *testing.T) {
	calls := map[string]func(GuildManager) error{
		"GuildMemberRoleAdd": func(gm GuildManager) error {
			return gm.GuildMemberRoleAdd("guild-1", "123456789012345678", "role-1", "Panel: Foxhole add by Doe.J (forum user 1234)")
		},
		"GuildMemberRoleRemove": func(gm GuildManager) error {
			return gm.GuildMemberRoleRemove("guild-1", "123456789012345678", "role-1", "Panel: Foxhole purge by Doe.J (forum user 1234)")
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			var limited bool
			api := &fakeDiscordAPI{answer: func(*http.Request, []byte) (int, []byte) {
				if !limited {
					limited = true
					return http.StatusTooManyRequests, []byte(`{"message":"You are being rate limited.","retry_after":0.01,"global":false}`)
				}
				return http.StatusNoContent, nil
			}}

			err := call(NewSessionGuildManager(stateSession(t, api)))

			if err != nil {
				t.Fatalf("%s after a rate limit: %v, want it to go through", name, err)
			}
		})
	}
}
