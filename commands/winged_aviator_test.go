package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The winged aviators (#869, GLOSSARY.md): the Winged Aviator role follows
// each winged aviator's milpac. These tests drive the runtime through its
// command, its gateway handlers and its passes, with the 7Cav API, Discord's
// role calls, the member list and the timers stood in for.

const (
	aviatorGuild      = "guild-wa"
	wingedAviatorRole = "role-wa"
	aviatorRankRole   = "role-rank"
	aviatorRankRole2  = "role-rank-2"
)

// The primary positions the 7Cav API serves, as its milpacs by Discord ID
// answered them on 2026-10-25. A trooper on a roster other than Past
// members has a position like aviatorActive.
const (
	aviatorActive       = "Trooper 1/1/A/1-7"
	aviatorDischarged   = "Discharged"
	aviatorDishonorable = "Dishonorably Discharged"
	aviatorRetired      = "Retired"
)

// roleChangeCall is one role change the runtime asked Discord for.
type roleChangeCall struct {
	op     string // "add" or "remove"
	userID string
	roleID string
	reason string
}

// fakeAviatorGuild records every role change the runtime asks Discord for,
// and fails a member's adds or removals with the error set for them.
type fakeAviatorGuild struct {
	mu        sync.Mutex
	calls     []roleChangeCall
	addErrs   map[string]error
	removeErr map[string]error
}

func (g *fakeAviatorGuild) GuildMemberRoleAdd(_, userID, roleID, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, roleChangeCall{op: "add", userID: userID, roleID: roleID, reason: reason})
	return g.addErrs[userID]
}

func (g *fakeAviatorGuild) GuildMemberRoleRemove(_, userID, roleID, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, roleChangeCall{op: "remove", userID: userID, roleID: roleID, reason: reason})
	return g.removeErr[userID]
}

// roleCalls returns every role change asked for so far, and forgets them,
// so a test reads each step's calls on their own.
func (g *fakeAviatorGuild) roleCalls() []roleChangeCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	calls := g.calls
	g.calls = nil
	return calls
}

// stubAviatorAPI stands in for the 7Cav API's milpac lookup by Discord ID.
// A member it has a position for gets a milpac with that primary position,
// one with a status set gets that status, and anyone else a 404, as the
// API answers a Discord ID no milpac names.
type stubAviatorAPI struct {
	mu        sync.Mutex
	positions map[string]string
	statuses  map[string]int
}

// serveAviatorMilpacs starts the stub and points the bot's API client at it
// for the rest of the test.
func serveAviatorMilpacs(t *testing.T) *stubAviatorAPI {
	t.Helper()
	api := &stubAviatorAPI{positions: map[string]string{}, statuses: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
	return api
}

// setPosition gives the member a milpac with this primary position.
func (a *stubAviatorAPI) setPosition(discordID, position string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.statuses, discordID)
	a.positions[discordID] = position
}

// failFor makes the member's lookup answer status.
func (a *stubAviatorAPI) failFor(discordID string, status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statuses[discordID] = status
}

func (a *stubAviatorAPI) serve(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutPrefix(r.URL.Path, "/milpac/discord/")
	a.mu.Lock()
	status, failed := a.statuses[id]
	position, known := a.positions[id]
	a.mu.Unlock()
	switch {
	case !ok:
		w.WriteHeader(http.StatusNotFound)
	case failed:
		w.WriteHeader(status)
	case !known:
		writeStubJSON(w, http.StatusNotFound, map[string]any{"code": 5, "message": "no user found for discordid: " + id})
	default:
		roster := "ROSTER_TYPE_COMBAT"
		if position == aviatorDischarged || position == aviatorDishonorable || position == aviatorRetired {
			roster = "ROSTER_TYPE_PAST_MEMBERS"
		}
		writeStubJSON(w, http.StatusOK, utils.ProfileResponse{
			DiscordID: id,
			Roster:    roster,
			Primary:   utils.Position{PositionTitle: position},
		})
	}
}

// fakeAviatorTimers holds every wait the runtime schedules, until the test
// fires it.
type fakeAviatorTimers struct {
	mu      sync.Mutex
	pending []func()
}

func (f *fakeAviatorTimers) afterFunc(_ time.Duration, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, fn)
}

// firePending runs each wait pending now, once, on the test's goroutine. A
// wait one of them schedules stays pending.
func (f *fakeAviatorTimers) firePending() {
	f.mu.Lock()
	due := f.pending
	f.pending = nil
	f.mu.Unlock()
	for _, fn := range due {
		fn()
	}
}

// aviatorScene is one guild with the runtime over its fakes.
type aviatorScene struct {
	t      *testing.T
	guild  *fakeAviatorGuild
	list   *fakeTempVCManager
	store  *store.Fake
	api    *stubAviatorAPI
	timers *fakeAviatorTimers
	wa     *WingedAviators
}

// newAviatorScene builds the runtime with the Winged Aviator role
// configured, over an empty store and a complete, empty member list.
func newAviatorScene(t *testing.T) *aviatorScene {
	t.Helper()
	sc := &aviatorScene{
		t:      t,
		guild:  &fakeAviatorGuild{addErrs: map[string]error{}, removeErr: map[string]error{}},
		list:   newFakeTempVCManager(),
		store:  store.NewFake(),
		api:    serveAviatorMilpacs(t),
		timers: &fakeAviatorTimers{},
	}
	prev := wingedAviatorAfterFunc
	wingedAviatorAfterFunc = sc.timers.afterFunc
	t.Cleanup(func() { wingedAviatorAfterFunc = prev })
	sc.wa = sc.build(wingedAviatorRole)
	return sc
}

// build makes a runtime over the scene's fakes with roleID configured.
func (sc *aviatorScene) build(roleID string) *WingedAviators {
	return NewWingedAviators(sc.guild, sc.list, sc.store, aviatorGuild, roleID)
}

// seedAviator makes the member a winged aviator in the store.
func (sc *aviatorScene) seedAviator(id string) {
	sc.t.Helper()
	if err := sc.store.AddWingedAviator(context.Background(), aviatorGuild, id); err != nil {
		sc.t.Fatalf("seed winged aviator %s: %v", id, err)
	}
}

// rejoin delivers the member's GUILD_MEMBER_ADD, holding roles.
func (sc *aviatorScene) rejoin(id string, roles ...string) {
	sc.wa.memberJoined(&discordgo.GuildMemberAdd{Member: aviatorMember(id, roles...)})
}

// rolesChange delivers the member's GUILD_MEMBER_UPDATE, from before's roles
// to after's.
func (sc *aviatorScene) rolesChange(id string, before, after []string) {
	sc.wa.memberUpdated(&discordgo.GuildMemberUpdate{
		Member:       aviatorMember(id, after...),
		BeforeUpdate: aviatorMember(id, before...),
	})
}

func aviatorMember(id string, roles ...string) *discordgo.Member {
	return &discordgo.Member{GuildID: aviatorGuild, User: &discordgo.User{ID: id, Username: "name-" + id}, Roles: roles}
}

// wantRoleCalls fails the test unless calls are exactly want, in order.
func wantRoleCalls(t *testing.T, step string, calls []roleChangeCall, want ...roleChangeCall) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("%s: role calls = %+v, want %+v", step, calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("%s: role call %d = %+v, want %+v", step, i, calls[i], want[i])
		}
	}
}

// A winged aviator who rejoins the server gets the role back, by the rejoin
// reason, unless their milpac shows a discharge. One whose milpac the bot
// can't find gets it too, since no discharge can reach them.
func TestWingedAviatorWhoRejoinsGetsTheRoleBackUnlessDischarged(t *testing.T) {
	cases := []struct {
		name     string
		position string
		given    bool
	}{
		{"active milpac", aviatorActive, true},
		{"no milpac", "", true},
		{"discharged", aviatorDischarged, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := newAviatorScene(t)
			sc.seedAviator("user-1")
			if tc.position != "" {
				sc.api.setPosition("user-1", tc.position)
			}

			sc.rejoin("user-1")

			var want []roleChangeCall
			if tc.given {
				want = append(want, roleChangeCall{op: "add", userID: "user-1", roleID: wingedAviatorRole, reason: wingedAviatorRejoinReason})
			}
			wantRoleCalls(t, "rejoin", sc.guild.roleCalls(), want...)
		})
	}
}

// aviatorInvoker is the staff member who runs /winged-aviator in the tests.
const aviatorInvoker = "999"

// run runs /winged-aviator sub on target as aviatorInvoker, answered the
// way Discord answers, and returns the reply only the invoker sees. A
// target in the server comes resolved with roles, as Discord's member
// picker sends them, and one who isn't comes as a user alone.
func (sc *aviatorScene) run(sub, target string, inServer bool, roles ...string) string {
	sc.t.Helper()
	return runAviatorCommand(sc.t, sc.wa, sub, target, inServer, roles...)
}

func runAviatorCommand(t *testing.T, wa *WingedAviators, sub, target string, inServer bool, roles ...string) string {
	t.Helper()
	resolved := &discordgo.ApplicationCommandInteractionDataResolved{
		Users: map[string]*discordgo.User{target: {ID: target, Username: "name-" + target}},
	}
	if inServer {
		resolved.Members = map[string]*discordgo.Member{target: {Roles: roles}}
	}
	interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:    discordgo.InteractionApplicationCommand,
		GuildID: aviatorGuild,
		Member:  &discordgo.Member{User: &discordgo.User{ID: aviatorInvoker, Username: "tester"}},
		Data: discordgo.ApplicationCommandInteractionData{
			Name: wingedAviatorCommandName,
			Options: []*discordgo.ApplicationCommandInteractionDataOption{{
				Name: sub, Type: discordgo.ApplicationCommandOptionSubCommand,
				Options: []*discordgo.ApplicationCommandInteractionDataOption{userOption("member", target)},
			}},
			Resolved: resolved,
		},
	}}
	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runWingedAviator(f, wa, interaction)
	return ephemeralRecordReply(t, f)
}

// wantReply fails the test unless the reply opens with verdict, names the
// member and carries phrase.
func wantReply(t *testing.T, reply, verdict, memberID, phrase string) {
	t.Helper()
	if !strings.HasPrefix(reply, verdict) {
		t.Errorf("reply = %q, want a %s verdict", reply, verdict)
	}
	if !strings.Contains(reply, "<@"+memberID+">") {
		t.Errorf("reply = %q, want it to name <@%s>", reply, memberID)
	}
	if !strings.Contains(reply, phrase) {
		t.Errorf("reply = %q, want it to say %q", reply, phrase)
	}
}

// wantCommandRoleCall fails the test unless calls are one op of the role
// on the member, with an audit log reason naming the staff member who ran
// the command.
func wantCommandRoleCall(t *testing.T, calls []roleChangeCall, op, memberID string) {
	t.Helper()
	if len(calls) != 1 || calls[0].op != op || calls[0].userID != memberID || calls[0].roleID != wingedAviatorRole {
		t.Fatalf("role calls = %+v, want the role's %s for %s alone", calls, op, memberID)
	}
	if !strings.Contains(calls[0].reason, aviatorInvoker) {
		t.Errorf("audit log reason = %q, want it to name the invoker %s", calls[0].reason, aviatorInvoker)
	}
}

// add records a member whose milpac shows no discharge and gives them the
// role, with a reason naming who ran it. A rejoin then gives the role
// back, which only a winged aviator gets.
func TestAddMakesAMemberWithNoDischargeAWingedAviatorWithTheRole(t *testing.T) {
	sc := newAviatorScene(t)
	sc.api.setPosition("user-1", aviatorActive)

	reply := sc.run("add", "user-1", true, aviatorRankRole)

	wantReply(t, reply, verdictDone, "user-1", aviatorAddedPhrase)
	wantCommandRoleCall(t, sc.guild.roleCalls(), "add", "user-1")

	sc.rejoin("user-1")
	wantRoleCalls(t, "rejoin", sc.guild.roleCalls(),
		roleChangeCall{op: "add", userID: "user-1", roleID: wingedAviatorRole, reason: wingedAviatorRejoinReason})
}

// add refuses a member no milpac names and changes nothing: no role, and
// no status, so a rejoin gives nothing.
func TestAddRefusesAMemberNoMilpacNames(t *testing.T) {
	sc := newAviatorScene(t)

	reply := sc.run("add", "user-1", true)

	wantReply(t, reply, verdictFailed, "user-1", aviatorNoMilpacPhrase)
	wantRoleCalls(t, "add", sc.guild.roleCalls())
	sc.rejoin("user-1")
	wantRoleCalls(t, "rejoin", sc.guild.roleCalls())
}

// add whose milpac read fails says so and changes nothing.
func TestAddWhoseMilpacReadFailsChangesNothing(t *testing.T) {
	sc := newAviatorScene(t)
	sc.api.failFor("user-1", http.StatusInternalServerError)

	reply := sc.run("add", "user-1", true)

	wantReply(t, reply, verdictFailed, "user-1", aviatorReadFailedPhrase)
	wantRoleCalls(t, "add", sc.guild.roleCalls())
	sc.api.setPosition("user-1", aviatorActive)
	sc.rejoin("user-1")
	wantRoleCalls(t, "rejoin", sc.guild.roleCalls())
}

// add records a member whose milpac shows a discharge without giving the
// role, and says the role comes when they re-enlist. It does: a
// re-enlistment, which changes their roles, gives it.
func TestAddMakesADischargedMemberAWingedAviatorWithoutTheRole(t *testing.T) {
	sc := newAviatorScene(t)
	sc.api.setPosition("user-1", aviatorDischarged)

	reply := sc.run("add", "user-1", true)

	wantReply(t, reply, verdictLeftOver, "user-1", aviatorReEnlistPhrase)
	wantRoleCalls(t, "add", sc.guild.roleCalls())

	sc.api.setPosition("user-1", aviatorActive)
	sc.rolesChange("user-1", nil, []string{aviatorRankRole})
	wantRoleCalls(t, "re-enlistment", sc.guild.roleCalls(),
		roleChangeCall{op: "add", userID: "user-1", roleID: wingedAviatorRole, reason: wingedAviatorReEnlistReason})
}

// add whose role Discord refuses says the role couldn't be given, and the
// member doesn't become a winged aviator: a rejoin asks for nothing more.
// The reply never carries Discord's own answer.
func TestAddWhoseRoleDiscordRefusesMakesNoWingedAviator(t *testing.T) {
	sc := newAviatorScene(t)
	sc.api.setPosition("user-1", aviatorActive)
	sc.guild.addErrs["user-1"] = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)

	reply := sc.run("add", "user-1", true)

	wantReply(t, reply, verdictFailed, "user-1", aviatorNotGivenPhrase)
	if strings.Contains(reply, rawBodyMarker) {
		t.Errorf("reply = %q, carries Discord's answer", reply)
	}
	sc.guild.roleCalls()
	delete(sc.guild.addErrs, "user-1")
	sc.rejoin("user-1")
	wantRoleCalls(t, "rejoin", sc.guild.roleCalls())
}

// add of a member who already is a winged aviator says so, and gives them
// the role they lack, since their milpac shows no discharge.
func TestAddOfAWingedAviatorSaysSoAndGivesTheRoleBack(t *testing.T) {
	sc := newAviatorScene(t)
	sc.seedAviator("user-1")
	sc.api.setPosition("user-1", aviatorActive)

	reply := sc.run("add", "user-1", true, aviatorRankRole)

	wantReply(t, reply, verdictDone, "user-1", aviatorAlreadyPhrase)
	wantCommandRoleCall(t, sc.guild.roleCalls(), "add", "user-1")
}

// remove takes the role, with a reason naming who ran it, and ends the
// status: a re-enlistment after it gives nothing back.
func TestRemoveTakesTheRoleAndEndsTheStatus(t *testing.T) {
	sc := newAviatorScene(t)
	sc.seedAviator("user-1")
	sc.api.setPosition("user-1", aviatorActive)

	reply := sc.run("remove", "user-1", true, aviatorRankRole, wingedAviatorRole)

	wantReply(t, reply, verdictDone, "user-1", aviatorRemovedPhrase)
	wantCommandRoleCall(t, sc.guild.roleCalls(), "remove", "user-1")

	sc.rolesChange("user-1", []string{aviatorRankRole}, []string{aviatorRankRole2})
	wantRoleCalls(t, "re-enlistment", sc.guild.roleCalls())
}

// remove of a member who is neither a winged aviator nor a role holder
// says so.
func TestRemoveOfANonAviatorSaysSo(t *testing.T) {
	sc := newAviatorScene(t)

	reply := sc.run("remove", "user-1", true, aviatorRankRole)

	wantReply(t, reply, verdictLeftOver, "user-1", aviatorNeitherPhrase)
	wantRoleCalls(t, "remove", sc.guild.roleCalls())
}

// A winged aviator whose milpac moves to a discharge loses the role when
// the forum moves their roles, by the discharge reason. The update the
// bot's own removal causes keeps the status, so a re-enlistment gives the
// role back.
func TestDischargeTakesTheRoleAndKeepsTheStatus(t *testing.T) {
	for _, position := range []string{aviatorDischarged, aviatorDishonorable} {
		t.Run(position, func(t *testing.T) {
			sc := newAviatorScene(t)
			sc.seedAviator("user-1")
			sc.api.setPosition("user-1", position)

			sc.rolesChange("user-1", []string{aviatorRankRole, wingedAviatorRole}, []string{wingedAviatorRole})
			wantRoleCalls(t, "discharge", sc.guild.roleCalls(),
				roleChangeCall{op: "remove", userID: "user-1", roleID: wingedAviatorRole, reason: wingedAviatorDischargeReason})
			sc.rolesChange("user-1", []string{wingedAviatorRole}, nil)
			wantRoleCalls(t, "the bot's own removal", sc.guild.roleCalls())

			sc.api.setPosition("user-1", aviatorActive)
			sc.rolesChange("user-1", nil, []string{aviatorRankRole})
			wantRoleCalls(t, "re-enlistment", sc.guild.roleCalls(),
				roleChangeCall{op: "add", userID: "user-1", roleID: wingedAviatorRole, reason: wingedAviatorReEnlistReason})
		})
	}
}

// A winged aviator who retires keeps the role, though the forum moves
// their other roles: a retirement is no discharge.
func TestRetirementKeepsTheRole(t *testing.T) {
	sc := newAviatorScene(t)
	sc.seedAviator("user-1")
	sc.api.setPosition("user-1", aviatorRetired)

	sc.rolesChange("user-1", []string{aviatorRankRole, wingedAviatorRole}, []string{aviatorRankRole2, wingedAviatorRole})

	wantRoleCalls(t, "retirement", sc.guild.roleCalls())
}

// A winged aviator whose milpac leaves a discharge gets the role back when
// the forum moves their roles, with no command run, by the re-enlistment
// reason.
func TestReEnlistmentGivesTheRoleBack(t *testing.T) {
	sc := newAviatorScene(t)
	sc.seedAviator("user-1")
	sc.api.setPosition("user-1", aviatorActive)

	sc.rolesChange("user-1", nil, []string{aviatorRankRole})

	wantRoleCalls(t, "re-enlistment", sc.guild.roleCalls(),
		roleChangeCall{op: "add", userID: "user-1", roleID: wingedAviatorRole, reason: wingedAviatorReEnlistReason})
}

// A role change on a winged aviator whose milpac read fails changes
// nothing: they keep the role they hold.
func TestRoleChangeWhoseMilpacReadFailsChangesNothing(t *testing.T) {
	sc := newAviatorScene(t)
	sc.seedAviator("user-1")
	sc.api.failFor("user-1", http.StatusInternalServerError)

	sc.rolesChange("user-1", []string{aviatorRankRole, wingedAviatorRole}, []string{aviatorRankRole2, wingedAviatorRole})

	wantRoleCalls(t, "role change", sc.guild.roleCalls())
}

// A member someone gives the role by hand becomes a winged aviator, with or
// without a milpac, and a discharge then takes the role.
func TestRoleGivenByHandMakesAWingedAviator(t *testing.T) {
	sc := newAviatorScene(t)
	sc.api.setPosition("user-1", aviatorActive)

	sc.rolesChange("user-1", []string{aviatorRankRole}, []string{aviatorRankRole, wingedAviatorRole})
	wantRoleCalls(t, "hand give", sc.guild.roleCalls())

	sc.api.setPosition("user-1", aviatorDischarged)
	sc.rolesChange("user-1", []string{aviatorRankRole, wingedAviatorRole}, []string{wingedAviatorRole})
	wantRoleCalls(t, "discharge", sc.guild.roleCalls(),
		roleChangeCall{op: "remove", userID: "user-1", roleID: wingedAviatorRole, reason: wingedAviatorDischargeReason})
}

// A winged aviator someone takes the role from by hand is one no more: the
// bot doesn't give it back, and a rejoin gives nothing.
func TestRoleRemovedByHandEndsTheStatus(t *testing.T) {
	sc := newAviatorScene(t)
	sc.seedAviator("user-1")
	sc.api.setPosition("user-1", aviatorActive)

	sc.rolesChange("user-1", []string{aviatorRankRole, wingedAviatorRole}, []string{aviatorRankRole})
	wantRoleCalls(t, "hand removal", sc.guild.roleCalls())

	sc.rejoin("user-1")
	wantRoleCalls(t, "rejoin", sc.guild.roleCalls())
}

// listMember puts the member in the guild's member list, holding roles.
func (sc *aviatorScene) listMember(id string, roles ...string) {
	sc.list.setMember(ListedMember{ID: id, Username: "name-" + id, RoleIDs: roles})
}

// The startup pass makes every role holder the store doesn't list a winged
// aviator, and gives the role back, by the startup reason, to every winged
// aviator in the server whose milpac shows no discharge. A discharged one
// and one who left get nothing.
func TestStartupRecordsEveryHolderAndGivesTheRoleBack(t *testing.T) {
	sc := newAviatorScene(t)
	sc.listMember("user-h", aviatorRankRole, wingedAviatorRole)
	sc.api.setPosition("user-h", aviatorActive)
	for _, id := range []string{"user-w", "user-d", "user-l"} {
		sc.seedAviator(id)
		sc.api.setPosition(id, aviatorActive)
	}
	sc.listMember("user-w", aviatorRankRole)
	sc.listMember("user-d")
	sc.api.setPosition("user-d", aviatorDischarged)

	sc.wa.Start()

	wantRoleCalls(t, "startup", sc.guild.roleCalls(),
		roleChangeCall{op: "add", userID: "user-w", roleID: wingedAviatorRole, reason: wingedAviatorStartupReason})
	sc.rejoin("user-h")
	wantRoleCalls(t, "the holder's rejoin", sc.guild.roleCalls(),
		roleChangeCall{op: "add", userID: "user-h", roleID: wingedAviatorRole, reason: wingedAviatorRejoinReason})
}

// On the first start, a holder whose milpac shows a discharge becomes a
// winged aviator and loses the role, by the discharge reason.
func TestStartupTakesTheRoleFromADischargedHolder(t *testing.T) {
	sc := newAviatorScene(t)
	sc.listMember("user-h", wingedAviatorRole)
	sc.api.setPosition("user-h", aviatorDischarged)

	sc.wa.Start()

	wantRoleCalls(t, "startup", sc.guild.roleCalls(),
		roleChangeCall{op: "remove", userID: "user-h", roleID: wingedAviatorRole, reason: wingedAviatorDischargeReason})
}

// A start while the member list is still arriving runs the startup pass
// once the list is complete: the holder is recorded, and the winged
// aviator who lacks the role gets it by the startup reason.
func TestStartupPassWaitsForTheWholeMemberList(t *testing.T) {
	sc := newAviatorScene(t)
	sc.listMember("user-h", wingedAviatorRole)
	sc.api.setPosition("user-h", aviatorActive)
	sc.seedAviator("user-w")
	sc.listMember("user-w")
	sc.api.setPosition("user-w", aviatorActive)
	sc.list.setListArriving(true)

	sc.wa.Start()
	sc.list.setListArriving(false)
	sc.timers.firePending()

	wantRoleCalls(t, "startup", sc.guild.roleCalls(),
		roleChangeCall{op: "add", userID: "user-w", roleID: wingedAviatorRole, reason: wingedAviatorStartupReason})
	sc.rejoin("user-h")
	wantRoleCalls(t, "the holder's rejoin", sc.guild.roleCalls(),
		roleChangeCall{op: "add", userID: "user-h", roleID: wingedAviatorRole, reason: wingedAviatorRejoinReason})
}

// The hourly pass checks every winged aviator again: one whose milpac now
// shows a discharge loses the role, and one whose read fails gets nothing,
// though their milpac showed a discharge before.
func TestHourlyPassChecksEveryWingedAviator(t *testing.T) {
	sc := newAviatorScene(t)
	sc.seedAviator("user-w")
	sc.listMember("user-w", wingedAviatorRole)
	sc.api.setPosition("user-w", aviatorActive)
	sc.seedAviator("user-x")
	sc.listMember("user-x")
	sc.api.setPosition("user-x", aviatorDischarged)
	sc.wa.Start()
	wantRoleCalls(t, "startup", sc.guild.roleCalls())

	sc.api.setPosition("user-w", aviatorDischarged)
	sc.api.failFor("user-x", http.StatusInternalServerError)
	sc.timers.firePending()

	wantRoleCalls(t, "hourly pass", sc.guild.roleCalls(),
		roleChangeCall{op: "remove", userID: "user-w", roleID: wingedAviatorRole, reason: wingedAviatorDischargeReason})
}

// With no Winged Aviator role configured, the command refuses as not set
// up, and the bot tracks nobody: its startup pass records no holder and
// gives no winged aviator the role.
func TestWithNoRoleConfiguredTheCommandRefusesAndNothingIsTracked(t *testing.T) {
	sc := newAviatorScene(t)
	off := sc.build("")
	sc.listMember("user-h", wingedAviatorRole)
	sc.api.setPosition("user-h", aviatorActive)
	sc.seedAviator("user-w")
	sc.listMember("user-w")
	sc.api.setPosition("user-w", aviatorActive)
	sc.api.setPosition("user-1", aviatorActive)

	if reply := runAviatorCommand(t, off, "add", "user-1", true); reply != wingedAviatorsOffRefusal {
		t.Errorf("reply = %q, want %q", reply, wingedAviatorsOffRefusal)
	}
	off.Start()
	sc.timers.firePending()
	wantRoleCalls(t, "startup", sc.guild.roleCalls())

	sc.rejoin("user-h")
	wantRoleCalls(t, "the holder's rejoin", sc.guild.roleCalls())
}

// /winged-aviator sets no default member permissions: like every command
// here, Server Settings decides who may run it.
func TestWingedAviatorCommandDeclaresNoDefaultMemberPermissions(t *testing.T) {
	for _, def := range NewRegistry(nil, nil, nil, nil).GetCommands() {
		if def.Name != wingedAviatorCommandName {
			continue
		}
		if def.DefaultMemberPermissions != nil {
			t.Errorf("default member permissions = %d, want none set", *def.DefaultMemberPermissions)
		}
		return
	}
	t.Fatalf("the registry declares no /%s", wingedAviatorCommandName)
}

// add of a winged aviator no milpac names, such as one given the role by
// hand, says they already are one and gives the role they lack: no
// discharge can reach them.
func TestAddOfAWingedAviatorWithNoMilpacSaysSoAndGivesTheRole(t *testing.T) {
	sc := newAviatorScene(t)
	sc.seedAviator("user-1")

	reply := sc.run("add", "user-1", true, aviatorRankRole)

	wantReply(t, reply, verdictDone, "user-1", aviatorAlreadyPhrase)
	wantCommandRoleCall(t, sc.guild.roleCalls(), "add", "user-1")
}

// add of a discharged member who holds the role makes them a winged
// aviator without it: the command takes it, and the role comes when they
// re-enlist.
func TestAddOfADischargedHolderTakesTheRole(t *testing.T) {
	sc := newAviatorScene(t)
	sc.api.setPosition("user-1", aviatorDischarged)

	reply := sc.run("add", "user-1", true, wingedAviatorRole)

	wantReply(t, reply, verdictLeftOver, "user-1", aviatorReEnlistPhrase)
	wantCommandRoleCall(t, sc.guild.roleCalls(), "remove", "user-1")
}
