package commands

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// The wiring of the winged aviators (#869): a session opened against a
// stand-in for Discord's gateway hands discordgo's own GUILD_MEMBER_ADD to
// the handlers Listen registers. The other winged aviator tests call the
// runtime's handlers directly. This one goes red if they're never wired,
// and its second case shows a bot with no Winged Aviator role tracks nobody
// on the path main takes.
func TestWingedAviatorsHearTheSessionsMemberEvents(t *testing.T) {
	cases := []struct {
		name   string
		roleID string
		want   []aviatorRoleCall
	}{
		{"configured", aviatorRole, []aviatorRoleCall{
			{op: "add", userID: "user-1", roleID: aviatorRole, reason: wingedAviatorRejoinReason},
		}},
		{"no role configured", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := newAviatorScene(t)
			sc.seedAviator("user-1")
			sc.api.setPosition("user-1", aviatorActive)
			wa := sc.build(tc.roleID)

			gw := newFakeGateway(t)
			api := &fakeDiscordAPI{answer: func(r *http.Request, _ []byte) (int, []byte) {
				if strings.HasSuffix(r.URL.Path, "/gateway") {
					return http.StatusOK, []byte(`{"url":"` + gw.url() + `"}`)
				}
				t.Errorf("sent %s %s to Discord's API, want the gateway lookup alone", r.Method, r.URL.Path)
				return http.StatusInternalServerError, []byte(`{"message":"no request expected"}`)
			}}
			dg := stateSession(t, api)
			// Handlers run one after another on the event loop, in the order
			// they were added, so heard closes once the runtime's are done.
			dg.SyncEvents = true
			wa.Listen(dg)
			heard := make(chan struct{})
			dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.GuildMemberAdd) { close(heard) })

			ready := make(chan error, 1)
			go func() { ready <- dg.Open() }()
			if f := gw.next(); f.Op != 2 {
				t.Fatalf("the bot's first frame is op %d, want Identify", f.Op)
			}
			gw.dispatch("READY", `{"v":10,"session_id":"session-1","user":{"id":"user-bot","username":"cavbot"},`+
				`"guilds":[{"id":"`+aviatorGuild+`","unavailable":true}]}`)
			if err := <-ready; err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = dg.Close() })

			gw.dispatch("GUILD_MEMBER_ADD", `{"guild_id":"`+aviatorGuild+`","user":{"id":"user-1","username":"name-user-1"},"roles":[]}`)
			select {
			case <-heard:
			case <-time.After(readDeadline):
				t.Fatal("the session never handed GUILD_MEMBER_ADD to its handlers")
			}

			wantRoleCalls(t, "rejoin", sc.guild.roleCalls(), tc.want...)
		})
	}
}
