package commands

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// One Foxhole action started on the Foxhole page never runs alongside a
// role-changing Foxhole command (spec #434, "Running a Foxhole action").
// These tests start the page action through the Foxhole runtime the panel
// uses, and hold it mid-run inside the fake's Discord write.

// The Foxhole roles' IDs in the page's guild.
const (
	pageInternal = "role-fx-internal"
	pageExternal = "role-fx-external"
)

// pageGuild is the guild's gateway state as a Foxhole page action reads
// it: the two Foxhole roles, named from the default base name, and a
// complete member list in which one member holds External and two hold
// Internal, so a purge of both makes 3 changes.
type pageGuild struct{}

func (pageGuild) MemberList(string) MemberListSnapshot {
	return MemberListSnapshot{Status: MemberListComplete, Connected: true, Members: []ListedMember{
		{ID: "100000000000000001", Username: "kestrel", RoleIDs: []string{pageExternal}},
		{ID: "100000000000000002", Username: "ash", RoleIDs: []string{pageInternal}},
		{ID: "100000000000000003", Username: "doe", RoleIDs: []string{pageInternal}},
	}}
}

func (pageGuild) GuildData(string) GuildSnapshot {
	return GuildSnapshot{Status: GuildDataPresent, Connected: true, Roles: []*discordgo.Role{
		guildRole(pageInternal, foxholeRoleBaseNameDefault+" Internal"),
		guildRole(pageExternal, foxholeRoleBaseNameDefault+" External"),
	}}
}

// pageHold is the Discord a page action changes roles through. It holds
// each member role change inside the write until the test lets it answer.
type pageHold struct {
	entered chan string
	release chan struct{}
	free    chan struct{}
	once    sync.Once
}

func (h *pageHold) GuildMemberRoleAdd(_, userID, _, _ string) error    { return h.write(userID) }
func (h *pageHold) GuildMemberRoleRemove(_, userID, _, _ string) error { return h.write(userID) }

func (h *pageHold) write(userID string) error {
	h.entered <- userID
	select {
	case <-h.release:
	case <-h.free:
	}
	return nil
}

// next waits for the page action's next role change to reach Discord, and
// leaves it held.
func (h *pageHold) next(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no page role change reached Discord")
	}
}

// pass lets the held role change answer.
func (h *pageHold) pass(t *testing.T) {
	t.Helper()
	select {
	case h.release <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("no page role change is held")
	}
}

// open lets the held role change and every one after it answer.
func (h *pageHold) open() { h.once.Do(func() { close(h.free) }) }

// pageStarter is the forum user who starts the page action. The name holds
// no digit, so a reply's numbers are its progress.
var pageStarter = ForumUser{ID: 1357, Username: "Smith.F"}

// newPageRuntime is the Foxhole runtime the panel would be built over, on
// the page's guild and a store fake, with each page role change held. The
// Foxhole role base name is left at its default.
func newPageRuntime(t *testing.T) (*FoxholeRuntime, *pageHold) {
	t.Helper()
	t.Setenv(foxholeRoleBaseNameEnv, "")
	t.Setenv(foxholeRoleBaseNameOldEnv, "")
	hold := &pageHold{entered: make(chan string, 16), release: make(chan struct{}), free: make(chan struct{})}
	t.Cleanup(hold.open)
	fx, err := NewFoxholeRuntime(pageGuild{}, hold, store.NewFake(), "guild-1")
	if err != nil {
		t.Fatalf("NewFoxholeRuntime: %v", err)
	}
	return fx, hold
}

// holdPagePurge starts a page purge of both roles, as the Foxhole page's
// Confirm does, and holds it inside its second role change: 1 of its 3
// changes done.
func holdPagePurge(t *testing.T, fx *FoxholeRuntime, hold *pageHold) {
	t.Helper()
	if err := fx.Purge(context.Background(), PurgeBoth, pageStarter); err != nil {
		t.Fatalf("the page purge didn't start: %v", err)
	}
	hold.next(t)
	hold.pass(t)
	hold.next(t)
}

// commandRun runs a Foxhole command the way its registered handler does.
type commandRun func(utils.InteractionResponder, GuildManager, *FoxholeRuntime, *discordgo.InteractionCreate)

// roleCommand is a role-changing Foxhole command a member sends.
type roleCommand struct {
	name        string
	run         commandRun
	interaction func() *discordgo.InteractionCreate
}

// roleCommands are the role-changing Foxhole commands under their new
// names: /foxhole add, remove, bulkadd and purge, and the roster add. Each
// changes a member of trooperGuild, and the roster add adds the roster
// serveRoleCommandRoster serves.
var roleCommands = []roleCommand{
	{"add", runFoxhole, func() *discordgo.InteractionCreate {
		return slashNamed("foxhole", stringOption("command", "add"), stringOption("flag", "internal"),
			stringOption("discordname", "123456789012345678"))
	}},
	{"remove", runFoxhole, func() *discordgo.InteractionCreate {
		return slashNamed("foxhole", stringOption("command", "remove"), stringOption("flag", "internal"),
			stringOption("discordname", "123456789012345678"))
	}},
	{"bulkadd", runFoxhole, func() *discordgo.InteractionCreate {
		return slashNamed("foxhole", stringOption("command", "bulkadd"), stringOption("flag", "internal"),
			stringOption("discordname", "good"))
	}},
	{"purge", runFoxhole, func() *discordgo.InteractionCreate {
		return slashNamed("foxhole", stringOption("command", "purge"), stringOption("flag", "internal"))
	}},
	{"roster add", runFoxholeBulkAddInternal, func() *discordgo.InteractionCreate {
		return slashNamed("foxhole-bulkadd-internal", stringOption("unit", "D/ACD"))
	}},
}

// serveRoleCommandRoster serves the roster add's unit roster: one trooper,
// the member trooperGuild holds.
func serveRoleCommandRoster(t *testing.T) {
	t.Helper()
	serveRosterAndProfiles(t, liteRoster(liteMember("Trooper.A", "123456789012345678")), http.StatusOK, nil)
}

// memberReply returns the reply a run showed its member, and fails the test
// unless only they could see it: an ephemeral response's content, or the
// edit of a deferred ephemeral response.
func memberReply(t *testing.T, calls []recordedCall) string {
	t.Helper()
	ephemeral, content := false, ""
	for _, c := range calls {
		switch {
		case c.Method == "Respond" && c.Response != nil && c.Response.Data != nil:
			ephemeral = c.Response.Data.Flags&discordgo.MessageFlagsEphemeral != 0
			content = c.Response.Data.Content
		case c.Method == "Edit" && c.Edit != nil && c.Edit.Content != nil:
			content = *c.Edit.Content
		}
	}
	if !ephemeral {
		t.Errorf("the reply %q isn't ephemeral; calls %+v", content, calls)
	}
	return content
}

// numbersIn returns the whole numbers a text holds, in order.
func numbersIn(text string) []int {
	var out []int
	for _, digits := range regexp.MustCompile(`\d+`).FindAllString(text, -1) {
		n, _ := strconv.Atoi(digits)
		out = append(out, n)
	}
	return out
}

// A role-changing command sent while a page action runs is refused with a
// reply only its member sees. The reply names the action, the forum user
// who started it and how far it has got, and the command changes nothing.
func TestRoleCommandsAreRefusedWhileAPageActionRuns(t *testing.T) {
	for _, tc := range roleCommands {
		t.Run(tc.name, func(t *testing.T) {
			noOverwriteDelay(t)
			serveRoleCommandRoster(t)
			fx, hold := newPageRuntime(t)
			holdPagePurge(t, fx, hold)
			gm := trooperGuild()
			f := &fakeResponder{}

			tc.run(f, gm, fx, tc.interaction())

			reply := memberReply(t, f.Calls())
			for _, want := range []string{FoxholeActionName(store.ChangePurge, ActionReport{Scope: PurgeBoth}), pageStarter.Username} {
				if !strings.Contains(reply, want) {
					t.Errorf("the reply %q doesn't name %q", reply, want)
				}
			}
			numbers := numbersIn(reply)
			done := slices.Index(numbers, 1)
			if done < 0 || !slices.Contains(numbers[done+1:], 3) {
				t.Errorf("the reply %q doesn't say 1 of the 3 changes is done", reply)
			}
			if writes := gm.guildWrites(); len(writes) != 0 {
				t.Errorf("the command changed the guild: %+v", writes)
			}
		})
	}
}

// heldRoleAdd is a guild whose member role adds hold inside the write,
// after entered signals, until release closes.
type heldRoleAdd struct {
	*fakeGuildManager
	entered chan struct{}
	release chan struct{}
}

func newHeldRoleAdd(t *testing.T, gm *fakeGuildManager) heldRoleAdd {
	t.Helper()
	g := heldRoleAdd{fakeGuildManager: gm, entered: make(chan struct{}, 16), release: make(chan struct{})}
	t.Cleanup(func() { closeOnce(g.release) })
	return g
}

func (g heldRoleAdd) GuildMemberRoleAdd(guildID, userID, roleID, auditReason string) error {
	g.entered <- struct{}{}
	<-g.release
	return g.fakeGuildManager.GuildMemberRoleAdd(guildID, userID, roleID, auditReason)
}

// heldPurgeCreate is a guild whose role creates, a purge's first change,
// hold inside the write, after entered signals, until release closes.
type heldPurgeCreate struct {
	*fakeGuildManager
	entered chan struct{}
	release chan struct{}
}

func (g heldPurgeCreate) GuildRoleCreate(guildID string, data *discordgo.RoleParams, auditReason string) (*discordgo.Role, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.fakeGuildManager.GuildRoleCreate(guildID, data, auditReason)
}

// closeOnce closes ch unless it is closed already.
func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// awaitSignal waits for ch, and fails the test when what never happens.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(what + " never happened")
	}
}

// Regression pin: two role-changing commands from different members still
// run together, as before the Foxhole page, while no page action runs.
func TestTwoRoleCommandsRunTogether(t *testing.T) {
	fx, _ := newPageRuntime(t)
	first := newHeldRoleAdd(t, trooperGuild())
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		runFoxhole(&fakeResponder{}, first, fx, ranBy(slashNamed("foxhole", stringOption("command", "add"),
			stringOption("flag", "internal"), stringOption("discordname", "123456789012345678")), reasonInvoker))
	}()
	awaitSignal(t, first.entered, "the first command's role add")
	second := trooperGuild()

	runFoxhole(&fakeResponder{}, second, fx, slashNamed("foxhole", stringOption("command", "add"),
		stringOption("flag", "internal"), stringOption("discordname", "123456789012345678")))

	want := []roleAddCall{{guildID: "guild-1", userID: "123456789012345678", roleID: "r-int"}}
	if got := second.roleAddCalls(); !slices.Equal(got, want) {
		t.Errorf("the second command's role adds = %+v while the first held, want %+v", got, want)
	}
	close(first.release)
	awaitSignal(t, firstDone, "the first command's end")
	if got := first.roleAddCalls(); !slices.Equal(got, want) {
		t.Errorf("the first command's role adds = %+v, want %+v", got, want)
	}
}

// startsOnceFree starts a page purge, as the Foxhole page's Confirm does,
// trying again while another Foxhole action holds the rule, the way a
// manager would, and fails the test unless it starts within 5 seconds.
func startsOnceFree(t *testing.T, fx *FoxholeRuntime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := fx.Purge(context.Background(), PurgeBoth, pageStarter)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrActionRunning) || time.Now().After(deadline) {
			t.Fatalf("the page purge didn't start after the command ended: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A page action started while a role-changing command runs is refused, and
// starts once the command's run has ended: the add's and the roster add's
// when they reply, the purge's when its background run has ended.
func TestPageActionIsRefusedWhileARoleCommandRuns(t *testing.T) {
	cases := []struct {
		name  string
		guild func(t *testing.T) (GuildManager, chan struct{}, chan struct{})
		run   func(GuildManager, *FoxholeRuntime)
	}{
		{"add", heldAddGuild, func(gm GuildManager, fx *FoxholeRuntime) {
			runFoxhole(&fakeResponder{}, gm, fx, slashNamed("foxhole", stringOption("command", "add"),
				stringOption("flag", "internal"), stringOption("discordname", "123456789012345678")))
		}},
		{"purge", func(t *testing.T) (GuildManager, chan struct{}, chan struct{}) {
			g := heldPurgeCreate{fakeGuildManager: trooperGuild(), entered: make(chan struct{}, 4), release: make(chan struct{})}
			t.Cleanup(func() { closeOnce(g.release) })
			return g, g.entered, g.release
		}, func(gm GuildManager, fx *FoxholeRuntime) {
			runFoxhole(&fakeResponder{}, gm, fx, slashNamed("foxhole", stringOption("command", "purge"),
				stringOption("flag", "internal")))
		}},
		{"roster add", heldAddGuild, func(gm GuildManager, fx *FoxholeRuntime) {
			runFoxholeBulkAddInternal(&fakeResponder{}, gm, fx, slashNamed("foxhole-bulkadd-internal", stringOption("unit", "D/ACD")))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noOverwriteDelay(t)
			serveRoleCommandRoster(t)
			fx, _ := newPageRuntime(t)
			gm, entered, release := tc.guild(t)
			go tc.run(gm, fx)
			awaitSignal(t, entered, "the command's first change")

			err := fx.Purge(context.Background(), PurgeBoth, pageStarter)

			if !errors.Is(err, ErrActionRunning) {
				t.Fatalf("the page purge started while the command ran: %v, want ErrActionRunning", err)
			}
			close(release)
			startsOnceFree(t, fx)
		})
	}
}

// heldAddGuild is trooperGuild with its member role adds held.
func heldAddGuild(t *testing.T) (GuildManager, chan struct{}, chan struct{}) {
	t.Helper()
	g := newHeldRoleAdd(t, trooperGuild())
	return g, g.entered, g.release
}

// The registry hands the Foxhole runtime to each Foxhole command it
// registers, under the new names and the old ones: dispatched the way
// main.go's dispatcher reaches them, each is refused while a page action
// runs, and no role grant reaches Discord.
func TestRegisteredFoxholeCommandsAreRefusedWhileAPageActionRuns(t *testing.T) {
	add := []*discordgo.ApplicationCommandInteractionDataOption{stringOption("command", "add"),
		stringOption("flag", "internal"), stringOption("discordname", "123456789012345678")}
	roster := []*discordgo.ApplicationCommandInteractionDataOption{stringOption("unit", "D/ACD")}
	for name, opts := range map[string][]*discordgo.ApplicationCommandInteractionDataOption{
		"foxhole": add, "warden": add, "foxhole-bulkadd-internal": roster, "warden-bulkadd-internal": roster,
	} {
		t.Run(name, func(t *testing.T) {
			serveRoleCommandRoster(t)
			fx, hold := newPageRuntime(t)
			holdPagePurge(t, fx, hold)
			handler, ok := NewRegistry(nil, fx).GetHandler(name)
			if !ok {
				t.Fatalf("no handler registered for /%s", name)
			}
			api := foxholeGuildAPI(t)

			handler(stateSession(t, api), sessionSlash(name, opts...))

			if grants := roleGrants(api); len(grants) != 0 {
				t.Errorf("/%s granted %v while a page action ran, want nothing", name, grants)
			}
		})
	}
}
