package commands

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// Discord's audit log names the bot as the actor of every change a Foxhole
// command makes, so the reason is where a moderator reads who ran it.

// reasonInvoker is the member who runs each command in these tests. No
// target member in them shares its username or ID, so a reason built from
// the member being changed names neither.
var reasonInvoker = &discordgo.User{ID: "424242424242424242", Username: "invoker.k"}

// ranBy sets the member who ran interaction.
func ranBy(interaction *discordgo.InteractionCreate, user *discordgo.User) *discordgo.InteractionCreate {
	interaction.Member = &discordgo.Member{User: user}
	return interaction
}

// assertReasonNames fails unless write's reason names each of names.
func assertReasonNames(t *testing.T, write guildWrite, names ...string) {
	t.Helper()
	for _, name := range names {
		if !strings.Contains(write.reason, name) {
			t.Errorf("%s reason %q does not name %q", write.method, write.reason, name)
		}
	}
}

// Each role change an add, a removal or a bulkadd makes names the command
// as typed, under whichever name it ran, and the member who ran it.
func TestFoxholeRoleChangesNameTheCommandAndWhoRanIt(t *testing.T) {
	cases := []struct {
		name, command, subcommand, discordname string
		typed                                  string
	}{
		{"add", "foxhole", "add", "123456789012345678", "/foxhole add"},
		{"remove", "foxhole", "remove", "123456789012345678", "/foxhole remove"},
		{"bulkadd", "foxhole", "bulkadd", "good", "/foxhole bulkadd"},
		{"add under the old name", "warden", "add", "123456789012345678", "/warden add"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gm := trooperGuild()
			interaction := ranBy(slashNamed(tc.command,
				stringOption("command", tc.subcommand),
				stringOption("flag", "internal"),
				stringOption("discordname", tc.discordname),
			), reasonInvoker)

			runFoxhole(&fakeResponder{}, gm, interaction)

			writes := gm.guildWrites()
			if len(writes) == 0 {
				t.Fatalf("the run changed nothing; calls %v", gm.Calls())
			}
			for _, write := range writes {
				assertReasonNames(t, write, tc.typed, reasonInvoker.Username, reasonInvoker.ID)
			}
		})
	}
}

// purgeGuild is a guild whose Internal role has an overwrite on two
// channels.
func purgeGuild() *fakeGuildManager {
	return &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("old-int", defaultInternalRoleName)},
		channels: []*discordgo.Channel{
			{ID: "chan-1", PermissionOverwrites: []*discordgo.PermissionOverwrite{
				{ID: "old-int", Type: discordgo.PermissionOverwriteTypeRole, Allow: 1},
			}},
			{ID: "chan-2", PermissionOverwrites: []*discordgo.PermissionOverwrite{
				{ID: "old-int", Type: discordgo.PermissionOverwriteTypeRole, Deny: 1},
			}},
		},
	}
}

// purgeBy is a /foxhole purge of Internal run by reasonInvoker.
func purgeBy() *discordgo.InteractionCreate {
	return ranBy(slashNamed("foxhole",
		stringOption("command", "purge"),
		stringOption("flag", "internal"),
	), reasonInvoker)
}

// A purge's role create, its overwrite copies and its delete of the old role
// each name the purge and the member who ran it.
func TestFoxholePurgeChangesNameThePurgeAndWhoRanIt(t *testing.T) {
	noOverwriteDelay(t)
	gm := purgeGuild()

	runFoxholePurge(&fakeResponder{}, gm, purgeBy(), "guild-1", "internal")

	made := map[string]bool{}
	for _, write := range gm.guildWrites() {
		made[write.method] = true
		assertReasonNames(t, write, "/foxhole purge", reasonInvoker.Username, reasonInvoker.ID)
	}
	for _, method := range []string{"GuildRoleCreate", "ChannelPermissionSet", "GuildRoleDelete"} {
		if !made[method] {
			t.Errorf("the purge made no %s; calls %v", method, gm.Calls())
		}
	}
}

// When copying an overwrite fails, the purge deletes the role it just
// created. That delete names the purge and the member who ran it, and reads
// apart from the purge's other changes, so a moderator can tell a role
// created and deleted moments apart was the purge undoing itself.
func TestFoxholePurgeUndoingAFailedRecreateSaysSo(t *testing.T) {
	noOverwriteDelay(t)
	gm := purgeGuild()
	gm.ChannelPermSetErrs = []error{restError(http.StatusInternalServerError, 0, "")}

	runFoxholePurge(&fakeResponder{}, gm, purgeBy(), "guild-1", "internal")

	var create, undo *guildWrite
	for _, write := range gm.guildWrites() {
		switch write.method {
		case "GuildRoleCreate":
			create = &write
		case "GuildRoleDelete":
			undo = &write
		}
	}
	if create == nil || undo == nil {
		t.Fatalf("the purge did not create and then delete a role; calls %v", gm.Calls())
	}
	assertReasonNames(t, *undo, "/foxhole purge", reasonInvoker.Username, reasonInvoker.ID)
	if undo.reason == create.reason {
		t.Errorf("the undoing delete's reason %q reads the same as the create's", undo.reason)
	}
}

// Each role add a roster bulk add makes names the command as typed, under
// whichever name it ran, the unit, and the member who ran it.
func TestFoxholeRosterAddNamesTheCommandUnitAndWhoRanIt(t *testing.T) {
	for _, command := range []string{"foxhole-bulkadd-internal", "warden-bulkadd-internal"} {
		t.Run(command, func(t *testing.T) {
			serveRosterAndProfiles(t, liteRoster(
				liteMember("Trooper.A", "111111111111111111"),
				liteMember("Trooper.B", "222222222222222222"),
			), http.StatusOK, nil)
			gm := internalRoleGM()
			interaction := ranBy(slashNamed(command, stringOption("unit", "D/ACD")), reasonInvoker)

			runFoxholeBulkAddInternal(&fakeResponder{}, gm, interaction)

			writes := gm.guildWrites()
			if len(writes) == 0 {
				t.Fatalf("the run changed nothing; calls %v", gm.Calls())
			}
			for _, write := range writes {
				assertReasonNames(t, write, "/"+command, "D/ACD", reasonInvoker.Username, reasonInvoker.ID)
			}
		})
	}
}

// assertReadsAsWritten fails unless header, the audit log reason header a
// call sent, reads as want. Discord takes the header as "URL-encoded UTF-8
// characters" and documents nothing about '+', so the header has to be
// printable ASCII and decode to want whether '+' reads as itself or as a
// space.
func assertReadsAsWritten(t *testing.T, call, header, want string) {
	t.Helper()
	for i := 0; i < len(header); i++ {
		if header[i] < 0x20 || header[i] > 0x7e {
			t.Errorf("%s sent the reason header %q, which is not URL-encoded", call, header)
			return
		}
	}
	decoders := []struct {
		name   string
		decode func(string) (string, error)
	}{
		{"keeping +", url.PathUnescape},
		{"reading + as a space", url.QueryUnescape},
	}
	for _, d := range decoders {
		if got, err := d.decode(header); err != nil || got != want {
			t.Errorf("%s sent the reason header %q, which reads %q (err %v) %s, want %q", call, header, got, err, d.name, want)
		}
	}
}

// Every change the production adapter sends carries its reason so that
// Discord reads it as written, an accented letter and a % included. Sent
// unencoded, the é arrives as raw bytes, %41 reads as A and + may read as a
// space.
func TestSessionGuildManagerSendsReasonsDiscordReadsAsWritten(t *testing.T) {
	const reason = "/foxhole add by josé%41+1 (424242424242424242)"
	calls := []struct {
		name string
		call func(GuildManager) error
	}{
		{"GuildRoleCreate", func(gm GuildManager) error {
			_, err := gm.GuildRoleCreate("guild-1", &discordgo.RoleParams{Name: defaultInternalRoleName}, reason)
			return err
		}},
		{"GuildRoleDelete", func(gm GuildManager) error {
			return gm.GuildRoleDelete("guild-1", "role-1", reason)
		}},
		{"ChannelPermissionSet", func(gm GuildManager) error {
			return gm.ChannelPermissionSet("chan-1", "role-1", discordgo.PermissionOverwriteTypeRole, 1, 0, reason)
		}},
		{"GuildMemberRoleAdd", func(gm GuildManager) error {
			return gm.GuildMemberRoleAdd("guild-1", "123456789012345678", "role-1", reason)
		}},
		{"GuildMemberRoleRemove", func(gm GuildManager) error {
			return gm.GuildMemberRoleRemove("guild-1", "123456789012345678", "role-1", reason)
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeDiscordAPI{answer: func(r *http.Request, _ []byte) (int, []byte) {
				if r.Method == http.MethodPost {
					return http.StatusOK, []byte(`{"id":"role-1"}`)
				}
				return http.StatusNoContent, nil
			}}

			if err := tc.call(NewSessionGuildManager(stateSession(t, api))); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			requests := api.received()
			if len(requests) != 1 {
				t.Fatalf("requests = %+v, want one", requests)
			}
			assertReadsAsWritten(t, tc.name, requests[0].auditReason, reason)
		})
	}
}
