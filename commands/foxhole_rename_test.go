package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The rename moves /warden to /foxhole and /warden-bulkadd-internal to
// /foxhole-bulkadd-internal. The old names stay registered on the same
// handlers until the cutoff, so their command IDs, and the Integrations
// overrides Discord keys by those IDs, survive the release.

// oldFoxholeNames maps each old command name to the name that replaced it.
var oldFoxholeNames = map[string]string{
	"warden":                  "foxhole",
	"warden-bulkadd-internal": "foxhole-bulkadd-internal",
}

// registeredDefinitions returns the production registry's definitions by name.
func registeredDefinitions() map[string]*discordgo.ApplicationCommand {
	defs := map[string]*discordgo.ApplicationCommand{}
	for _, def := range NewRegistry(nil).GetCommands() {
		defs[def.Name] = def
	}
	return defs
}

func hasOptionNamed(def *discordgo.ApplicationCommand, name string) bool {
	for _, opt := range def.Options {
		if opt.Name == name {
			return true
		}
	}
	return false
}

// The new names carry the options managers already type, and each old name
// carries the same options as its new name.
func TestFoxholeCommandsRegisterWithTheOptionsTheOldNamesHad(t *testing.T) {
	defs := registeredDefinitions()

	for name, options := range map[string][]string{
		"foxhole":                  {"command", "flag", "discordname"},
		"foxhole-bulkadd-internal": {"unit"},
	} {
		def, ok := defs[name]
		if !ok {
			t.Errorf("/%s is not registered", name)
			continue
		}
		for _, option := range options {
			if !hasOptionNamed(def, option) {
				t.Errorf("/%s has no %q option", name, option)
			}
		}
	}

	for oldName, newName := range oldFoxholeNames {
		old, ok := defs[oldName]
		if !ok {
			t.Errorf("/%s is not registered", oldName)
			continue
		}
		renamed, ok := defs[newName]
		if !ok {
			continue
		}
		if !reflect.DeepEqual(old.Options, renamed.Options) {
			t.Errorf("/%s options differ from /%s's", oldName, newName)
		}
	}
}

// commandNamePattern matches a slash command name as text names it.
var commandNamePattern = regexp.MustCompile(`/([a-z0-9_-]+)`)

// commandsNamedIn returns the slash command names text mentions, in order.
func commandsNamedIn(text string) []string {
	var names []string
	for _, match := range commandNamePattern.FindAllStringSubmatch(text, -1) {
		names = append(names, match[1])
	}
	return names
}

// An old command's description sends a manager browsing the picker to its
// own new name, and dates the old one's end in plain text, since Discord
// renders no markup in a description. Discord refuses a description over
// 100 characters, which would stop the bot at registration.
func TestOldFoxholeNamesDescribeTheirNewNameAndCutoff(t *testing.T) {
	defs := registeredDefinitions()
	day := regexp.MustCompile(`\b` + strconv.Itoa(renameCutoff.Day()) + `\b`)

	for oldName, newName := range oldFoxholeNames {
		def, ok := defs[oldName]
		if !ok {
			t.Errorf("/%s is not registered", oldName)
			continue
		}
		description := def.Description
		if named := commandsNamedIn(description); !reflect.DeepEqual(named, []string{newName}) {
			t.Errorf("/%s description %q names %v, want only /%s", oldName, description, named, newName)
		}
		if !day.MatchString(description) {
			t.Errorf("/%s description %q lacks the cutoff's day", oldName, description)
		}
		for _, part := range []string{renameCutoff.Month().String(), strconv.Itoa(renameCutoff.Year())} {
			if !strings.Contains(description, part) {
				t.Errorf("/%s description %q lacks %q", oldName, description, part)
			}
		}
		if n := utf8.RuneCountInString(description); n > 100 {
			t.Errorf("/%s description is %d characters, over Discord's 100", oldName, n)
		}
	}
}

// foxholeGuildAPI answers the REST calls a Foxhole command makes over a real
// session: the guild holds the default Internal role, and one member.
func foxholeGuildAPI(t *testing.T) *fakeDiscordAPI {
	t.Helper()
	roles, err := json.Marshal([]*discordgo.Role{guildRole("r-int", defaultInternalRoleName)})
	if err != nil {
		t.Fatal(err)
	}
	member, err := json.Marshal(&discordgo.Member{User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeDiscordAPI{answer: func(r *http.Request, _ []byte) (int, []byte) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/roles"):
			return http.StatusOK, roles
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/members/"):
			return http.StatusOK, member
		case r.Method == http.MethodPatch:
			return http.StatusOK, []byte(`{"id":"message-1"}`)
		default:
			return http.StatusNoContent, nil
		}
	}}
}

// sessionSlash is a slash command interaction as Discord delivers it to the
// dispatcher, under the given registered name.
func sessionSlash(name string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:      "1",
		AppID:   "app-1",
		Token:   "token-1",
		Type:    discordgo.InteractionApplicationCommand,
		GuildID: "guild-1",
		Member:  &discordgo.Member{User: &discordgo.User{ID: "999", Username: "tester"}},
		Data:    discordgo.ApplicationCommandInteractionData{Name: name, Options: opts},
	}}
}

// roleGrants returns the member role grants a fake API received, as
// guild/member/role path suffixes.
func roleGrants(api *fakeDiscordAPI) []string {
	var grants []string
	for _, req := range api.received() {
		if req.method != http.MethodPut {
			continue
		}
		if at := strings.Index(req.path, "/guilds/"); at >= 0 {
			grants = append(grants, req.path[at:])
		}
	}
	return grants
}

// An old name dispatches the way main.go's dispatcher reaches every command,
// by its registered name, and grants the same roles its new name grants. Its
// one telemetry line carries the name it ran under.
func TestOldFoxholeNamesGrantWhatTheirNewNamesGrant(t *testing.T) {
	serveRosterAndProfiles(t, liteRoster(
		liteMember("Trooper.A", "111111111111111111"),
		liteMember("Trooper.B", "222222222222222222"),
	), http.StatusOK, nil)

	cases := []struct {
		oldName, newName string
		options          []*discordgo.ApplicationCommandInteractionDataOption
		wantGrants       []string
	}{
		{
			oldName: "warden", newName: "foxhole",
			options: []*discordgo.ApplicationCommandInteractionDataOption{
				stringOption("command", "add"),
				stringOption("flag", "internal"),
				stringOption("discordname", "123456789012345678"),
			},
			wantGrants: []string{"/guilds/guild-1/members/123456789012345678/roles/r-int"},
		},
		{
			oldName: "warden-bulkadd-internal", newName: "foxhole-bulkadd-internal",
			options: []*discordgo.ApplicationCommandInteractionDataOption{stringOption("unit", "D/ACD")},
			wantGrants: []string{
				"/guilds/guild-1/members/111111111111111111/roles/r-int",
				"/guilds/guild-1/members/222222222222222222/roles/r-int",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.oldName, func(t *testing.T) {
			registry := NewRegistry(nil)
			run := func(name string) (grants []string, telemetry []map[string]string) {
				handler, ok := registry.GetHandler(name)
				if !ok {
					t.Fatalf("no handler registered for /%s", name)
				}
				api := foxholeGuildAPI(t)
				lines := captureTelemetryLines(t)
				handler(stateSession(t, api), sessionSlash(name, tc.options...))
				grants = roleGrants(api)
				slices.Sort(grants)
				return grants, lines()
			}

			newGrants, _ := run(tc.newName)
			oldGrants, telemetry := run(tc.oldName)

			if !reflect.DeepEqual(newGrants, tc.wantGrants) {
				t.Errorf("/%s granted %v, want %v", tc.newName, newGrants, tc.wantGrants)
			}
			if !reflect.DeepEqual(oldGrants, newGrants) {
				t.Errorf("/%s granted %v, /%s granted %v", tc.oldName, oldGrants, tc.newName, newGrants)
			}
			if len(telemetry) != 1 || telemetry[0]["command"] != tc.oldName {
				t.Errorf("/%s telemetry records = %v, want one with command=%s", tc.oldName, telemetry, tc.oldName)
			}
		})
	}
}

// timestampPattern matches Discord timestamp markup in any style.
var timestampPattern = regexp.MustCompile(`<t:(-?\d+)(?::[tTdDfFR])?>`)

// assertRenameNotice fails unless content's last line is the rename notice:
// it names exactly oldName then newName, and dates the cutoff with timestamp
// markup, so each manager's client shows it in their own time zone.
func assertRenameNotice(t *testing.T, content, oldName, newName string) {
	t.Helper()
	lastLine := content[strings.LastIndex(content, "\n")+1:]
	if named := commandsNamedIn(lastLine); !reflect.DeepEqual(named, []string{oldName, newName}) {
		t.Errorf("last line %q names %v, want /%s then /%s\nreply: %q", lastLine, named, oldName, newName, content)
	}
	match := timestampPattern.FindStringSubmatch(lastLine)
	if match == nil {
		t.Errorf("last line %q carries no timestamp markup", lastLine)
		return
	}
	if unix, err := strconv.ParseInt(match[1], 10, 64); err != nil || unix != renameCutoff.Unix() {
		t.Errorf("last line %q dates %s, want the cutoff's Unix time %d", lastLine, match[1], renameCutoff.Unix())
	}
}

// slashNamed builds a guild interaction registered under name, the way the
// dispatcher hands it to a run function.
func slashNamed(name string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	i := foxholeInteraction("guild-1", opts...)
	i.Data = discordgo.ApplicationCommandInteractionData{Name: name, Options: opts}
	return i
}

// trooperGuild is a guild with the default Internal role and one member
// reachable by ID.
func trooperGuild() *fakeGuildManager {
	return &fakeGuildManager{
		roles:       []*discordgo.Role{guildRole("r-int", defaultInternalRoleName)},
		membersByID: map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
		searchResults: map[string][]*discordgo.Member{
			"good": {{User: &discordgo.User{ID: "111", Username: "good"}}},
		},
	}
}

// Every reply under an old name still says what it said before, then ends
// with the notice naming the old command, its new name and the cutoff.
func TestRepliesUnderOldFoxholeNamesEndWithTheRenameNotice(t *testing.T) {
	t.Run("immediate reply", func(t *testing.T) {
		f := &fakeResponder{}
		runFoxhole(f, trooperGuild(), slashNamed("warden",
			stringOption("command", "bogus"), stringOption("flag", "internal")))

		assertRenameNotice(t, lastResponseContent(f.Calls()), "warden", "foxhole")
	})

	t.Run("deferred reply", func(t *testing.T) {
		f := &fakeResponder{}
		runFoxhole(f, trooperGuild(), slashNamed("warden",
			stringOption("command", "add"), stringOption("flag", "internal"),
			stringOption("discordname", "123456789012345678")))

		assertRenameNotice(t, lastEditContent(f.Calls()), "warden", "foxhole")
	})

	t.Run("deferred reply with the added members", func(t *testing.T) {
		f := &fakeResponder{}
		runFoxhole(f, trooperGuild(), slashNamed("warden",
			stringOption("command", "bulkadd"), stringOption("flag", "internal"),
			stringOption("discordname", "good")))

		calls := f.Calls()
		assertRenameNotice(t, lastEditContent(calls), "warden", "foxhole")
		if embed := lastEditEmbed(calls); embed == nil || !strings.Contains(embed.Description, "<@111>") {
			t.Errorf("reply embed %+v no longer names the added member", embed)
		}
	})

	t.Run("roster add's immediate reply", func(t *testing.T) {
		f := &fakeResponder{}
		runFoxholeBulkAddInternal(f, trooperGuild(), slashNamed("warden-bulkadd-internal",
			stringOption("unit", "not-a-unit")))

		assertRenameNotice(t, lastResponseContent(f.Calls()), "warden-bulkadd-internal", "foxhole-bulkadd-internal")
	})

	t.Run("purge summary posted to the channel after the token expired", func(t *testing.T) {
		f := &fakeResponder{EditErrs: []error{tokenExpiredRESTError()}}
		gm := &fakeGuildManager{}
		i := slashNamed("warden", stringOption("command", "purge"))
		i.ChannelID = "chan-9"

		deliverPurgeSummary(f, gm, i, "✅ Purge complete.")

		assertRenameNotice(t, gm.lastChannelMessage(), "warden", "foxhole")
	})
}

// A reply under a new name carries no rename notice.
func TestRepliesUnderNewFoxholeNamesCarryNoRenameNotice(t *testing.T) {
	f := &fakeResponder{}
	runFoxhole(f, trooperGuild(), slashNamed("foxhole",
		stringOption("command", "add"), stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678")))

	reply := lastEditContent(f.Calls())
	if strings.Contains(reply, "<t:"+strconv.FormatInt(renameCutoff.Unix(), 10)) {
		t.Errorf("/foxhole reply %q carries the rename notice", reply)
	}
}

// Discord refuses a message over 2000 characters, so a reply that already
// fills the limit makes room for the notice instead of failing to arrive.
func TestFullReplyUnderAnOldFoxholeNameStillFitsWithTheNotice(t *testing.T) {
	unlinked := make([]utils.LiteProfileResponse, 200)
	for idx := range unlinked {
		unlinked[idx] = liteMember(fmt.Sprintf("Trooper.%03d", idx), "")
	}
	serveRosterAndProfiles(t, liteRoster(unlinked...), http.StatusOK, nil)
	f := &fakeResponder{}

	runFoxholeBulkAddInternal(f, trooperGuild(), slashNamed("warden-bulkadd-internal", stringOption("unit", "D/ACD")))

	reply := lastEditContent(f.Calls())
	if n := utf8.RuneCountInString(reply); n > 2000 {
		t.Errorf("reply is %d characters, over Discord's 2000", n)
	}
	assertRenameNotice(t, reply, "warden-bulkadd-internal", "foxhole-bulkadd-internal")
}

// unsetEnv unsets key for the rest of the test and restores it afterwards.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// liveRoleGuild holds the Internal role under the live name and under the
// old default, and one member reachable by ID.
func liveRoleGuild() *fakeGuildManager {
	return &fakeGuildManager{
		roles: []*discordgo.Role{
			guildRole("r-old-default", "Verified Warden Internal"),
			guildRole("r-live", "Verified Foxhole Internal"),
			guildRole("r-legacy", "Verified Legacy Internal"),
		},
		membersByID: map[string]*discordgo.Member{"123456789012345678": {User: &discordgo.User{ID: "123456789012345678", Username: "trooper"}}},
	}
}

// grantedRoleIDs runs /foxhole add internal against gm and returns the role
// IDs the member was granted.
func grantedRoleIDs(t *testing.T, gm *fakeGuildManager) []string {
	t.Helper()
	runFoxhole(&fakeResponder{}, gm, slashNamed("foxhole",
		stringOption("command", "add"), stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678")))
	var ids []string
	for _, add := range gm.roleAddCalls() {
		ids = append(ids, add.roleID)
	}
	return ids
}

// The host never receives the repo's compose file, so a host that passes no
// base name runs on the default, which matches the live roles.
func TestFoxholeRoleBaseNameDefaultsToTheLiveRoleNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(t *testing.T)
	}{
		{"unset", func(t *testing.T) { unsetEnv(t, "FOXHOLE_ROLE_BASE_NAME") }},
		{"empty", func(t *testing.T) { t.Setenv("FOXHOLE_ROLE_BASE_NAME", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, "WARDEN_ROLE_BASE_NAME")
			tc.set(t)

			if got := grantedRoleIDs(t, liveRoleGuild()); !reflect.DeepEqual(got, []string{"r-live"}) {
				t.Errorf("granted %v, want [r-live], the role named Verified Foxhole Internal", got)
			}
		})
	}
}

// carriesValue reports whether any field of a decoded log record holds value.
func carriesValue(record map[string]string, value string) bool {
	for _, v := range record {
		if v == value {
			return true
		}
	}
	return false
}

// A host still passing only the old variable keeps its roles through the
// rename, and the startup log tells the maintainer which variable to rename.
func TestFoxholeRoleBaseNameFallsBackToTheOldVariable(t *testing.T) {
	unsetEnv(t, "FOXHOLE_ROLE_BASE_NAME")
	t.Setenv("WARDEN_ROLE_BASE_NAME", "Verified Legacy")

	if got := grantedRoleIDs(t, liveRoleGuild()); !reflect.DeepEqual(got, []string{"r-legacy"}) {
		t.Errorf("granted %v, want [r-legacy], the role named from the old variable", got)
	}

	logs := captureLogs(t)
	LogFoxholeRoleBaseName()

	var warned, resolved bool
	for _, record := range decodeLogRecords(t, logs) {
		if record["level"] == "WARN" && carriesValue(record, "WARDEN_ROLE_BASE_NAME") {
			warned = true
		}
		if record["base_name"] == "Verified Legacy" {
			resolved = true
		}
	}
	if !warned {
		t.Errorf("no startup warning names WARDEN_ROLE_BASE_NAME; logs:\n%s", logs.String())
	}
	if !resolved {
		t.Errorf("no startup record logs base_name=Verified Legacy; logs:\n%s", logs.String())
	}
}

// With both variables set, the new one decides and the old one goes unmentioned.
func TestNewFoxholeRoleBaseNameWinsOverTheOldOne(t *testing.T) {
	t.Setenv("FOXHOLE_ROLE_BASE_NAME", "Verified Foxhole")
	t.Setenv("WARDEN_ROLE_BASE_NAME", "Verified Legacy")

	if got := grantedRoleIDs(t, liveRoleGuild()); !reflect.DeepEqual(got, []string{"r-live"}) {
		t.Errorf("granted %v, want [r-live], the role named from the new variable", got)
	}

	logs := captureLogs(t)
	LogFoxholeRoleBaseName()

	for _, record := range decodeLogRecords(t, logs) {
		if carriesValue(record, "WARDEN_ROLE_BASE_NAME") {
			t.Errorf("startup record %v names the old variable while the new one is set", record)
		}
	}
}

// Sentry promotes a capture's command to a tag, and per-command error rate
// reads it, so a Foxhole capture names the registered command the run came in
// under, old name included.
func TestFoxholeCapturesNameTheCommandTheyRanUnder(t *testing.T) {
	add := []*discordgo.ApplicationCommandInteractionDataOption{
		stringOption("command", "add"), stringOption("flag", "internal"),
		stringOption("discordname", "123456789012345678"),
	}
	for _, name := range []string{"foxhole", "warden"} {
		t.Run(name+" role lookup fault", func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			gm := trooperGuild()
			gm.RolesErrs = []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)}

			runFoxhole(&fakeResponder{}, gm, slashNamed(name, add...))

			if got, _ := kvValue(rec.lastKV, "command"); rec.count != 1 || got != name {
				t.Errorf("captures = %d, command = %v; want one capture with command=%s", rec.count, got, name)
			}
		})
		t.Run(name+" lost reply", func(t *testing.T) {
			rec := &captureRecorder{}
			rec.install(t)
			f := &fakeResponder{EditErrs: []error{errors.New("503 service unavailable")}}

			runFoxhole(f, trooperGuild(), slashNamed(name, add...))

			if got, _ := kvValue(rec.lastKV, "command"); rec.count != 1 || got != name {
				t.Errorf("captures = %d, command = %v; want one capture with command=%s", rec.count, got, name)
			}
		})
	}
}
