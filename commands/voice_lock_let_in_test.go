package commands

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// Let someone in (#352). The lock notice's second button opens a member
// picker, and submitting it puts the picked members on the guest list. The
// button's CustomID is read off the notice the fake recorded, and the
// picker's off the components of the reply to that press. A let-in is
// judged by who can join the channel afterwards and by who the chat line it
// posts may ping. Nothing here asserts reply wording, a CustomID's format,
// the picker's limit or a log line.

// letInBot is a bot account holding the member role. It sees the channel,
// so only the bot rule keeps it off the guest list.
var letInBot = permMember{id: "user-bot", roles: []string{permRoleMember}}

// routeWith delivers a component interaction to the handler main.go's
// dispatcher routes its CustomID to, answering through f. The route must be
// a registered command, or Discord's interaction would reach nothing.
func routeWith(t *testing.T, f *fakeResponder, tv *TempVC, i *discordgo.InteractionCreate) {
	t.Helper()
	customID := i.MessageComponentData().CustomID
	prefix := strings.Split(customID, "::")[0]
	if _, ok := NewRegistry(nil).GetHandler(prefix); !ok {
		t.Fatalf("CustomID %q routes to %q, which is not a registered command", customID, prefix)
	}
	run, ok := componentRoutes[prefix]
	if !ok {
		t.Fatalf("CustomID %q routes to %q, which takes no component", customID, prefix)
	}
	run(f, tv, i)
}

// userSelectsOf lists the user selects in a message's components, rows
// included.
func userSelectsOf(components []discordgo.MessageComponent) []discordgo.SelectMenu {
	var out []discordgo.SelectMenu
	for _, c := range components {
		switch c := c.(type) {
		case discordgo.ActionsRow:
			out = append(out, userSelectsOf(c.Components)...)
		case *discordgo.ActionsRow:
			out = append(out, userSelectsOf(c.Components)...)
		case discordgo.SelectMenu:
			if c.MenuType == discordgo.UserSelectMenu {
				out = append(out, c)
			}
		case *discordgo.SelectMenu:
			if c.MenuType == discordgo.UserSelectMenu {
				out = append(out, *c)
			}
		}
	}
	return out
}

// pressLetIn presses a Let someone in button as m, checks the answer is a
// new ephemeral message, and returns the CustomID of the member picker it
// carries, or false when it carries none.
func pressLetIn(t *testing.T, tv *TempVC, button string, m permMember) (string, bool) {
	t.Helper()
	f := &fakeResponder{}
	pressWith(t, f, tv, button, m)
	ephemeralReply(t, f)
	edit := f.Calls()[1].Edit
	if edit.Components == nil {
		return "", false
	}
	selects := userSelectsOf(*edit.Components)
	if len(selects) != 1 {
		return "", false
	}
	return selects[0].CustomID, true
}

// openPicker presses the Let someone in button on chan-1's lock notice as
// m and returns the picker's CustomID, failing the test when m gets none.
func openPicker(t *testing.T, tv *TempVC, fake *fakeTempVCManager, m permMember) string {
	t.Helper()
	picker, ok := pressLetIn(t, tv, noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeLetIn), m)
	if !ok {
		t.Fatalf("%s's press on Let someone in opened no member picker", m.id)
	}
	return picker
}

// pickInteraction builds the interaction a submission of the member picker
// with the given CustomID sends from m, picking members. Discord resolves
// each picked user, bot flag included, and each picked member's roles; a
// resolved member is partial and carries no user.
func pickInteraction(customID string, m permMember, picked []permMember) *discordgo.InteractionCreate {
	i := pressInteraction(customID, m)
	data := discordgo.MessageComponentInteractionData{
		CustomID:      customID,
		ComponentType: discordgo.UserSelectMenuComponent,
		Resolved: discordgo.MessageComponentInteractionDataResolved{
			Users:   map[string]*discordgo.User{},
			Members: map[string]*discordgo.Member{},
		},
	}
	for _, p := range picked {
		data.Values = append(data.Values, p.id)
		data.Resolved.Users[p.id] = &discordgo.User{ID: p.id, Bot: p.id == letInBot.id}
		data.Resolved.Members[p.id] = &discordgo.Member{Roles: slices.Clone(p.roles)}
	}
	i.Data = data
	return i
}

// pick submits the member picker as m, checks the answer is a new
// ephemeral message, and returns it.
func pick(t *testing.T, tv *TempVC, picker string, m permMember, picked ...permMember) string {
	t.Helper()
	f := &fakeResponder{}
	routeWith(t, f, tv, pickInteraction(picker, m, picked))
	return ephemeralReply(t, f)
}

// letInPingsIn lists a channel's messages that may ping someone: the chat
// lines let-ins post. The ownership notice and the lock notice ping nobody.
func letInPingsIn(fake *fakeTempVCManager, channelID string) []fakeMessage {
	var out []fakeMessage
	for _, m := range fake.recordedMessages() {
		if m.channelID == channelID && !pingsNobody(m.data.AllowedMentions) {
			out = append(out, m)
		}
	}
	return out
}

// onlyPing returns the one let-in ping in a channel, failing the test when
// there is not exactly one.
func onlyPing(t *testing.T, fake *fakeTempVCManager, channelID string) fakeMessage {
	t.Helper()
	pings := letInPingsIn(fake, channelID)
	if len(pings) != 1 {
		t.Fatalf("let-in pings in %s = %+v, want one", channelID, pings)
	}
	return pings[0]
}

// assertPingsExactly checks a message may ping the given members and
// nobody else: no other user, no role, and no parsed mention. A nil value
// lets Discord parse every mention.
func assertPingsExactly(t *testing.T, msg fakeMessage, want ...permMember) {
	t.Helper()
	am := msg.data.AllowedMentions
	if am == nil {
		t.Fatalf("message %s allowed mentions = nil, which pings every mention", msg.id)
	}
	if len(am.Parse) != 0 || len(am.Roles) != 0 {
		t.Errorf("message %s allowed mentions = %+v, want no parse and no roles", msg.id, am)
	}
	wantIDs := make([]string, 0, len(want))
	for _, m := range want {
		wantIDs = append(wantIDs, m.id)
	}
	slices.Sort(wantIDs)
	got := slices.Sorted(slices.Values(am.Users))
	if !slices.Equal(got, wantIDs) {
		t.Errorf("message %s may ping users %v, want exactly %v", msg.id, got, wantIDs)
	}
}

// A moderator from outside the channel, or whoever locked it, lets M in: M
// can join, and the chat line pings M and nobody else, naming who let M in.
func TestLetInPutsThePickedMemberOnTheGuestList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		presser permMember
	}{
		{"by the locker", lockOwner},
		{"by a moderator outside the channel", permMOD},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, _, tv := newLockScene(t, store.PermissionCategory)
			lockAs(t, tv, lockOwner)
			assertJoins(t, fake, "chan-1", "locked", nil, []permMember{permM})

			pick(t, tv, openPicker(t, tv, fake, tc.presser), tc.presser, permM)

			assertJoins(t, fake, "chan-1", "after the let-in", []permMember{permM}, []permMember{permV, permN})
			ping := onlyPing(t, fake, "chan-1")
			assertPingsExactly(t, ping, permM)
			for _, who := range []permMember{permM, tc.presser} {
				if token := "<@" + who.id + ">"; !strings.Contains(ping.data.Content, token) {
					t.Errorf("ping content %q does not mention %s", ping.data.Content, token)
				}
			}
		})
	}
}

// Letting someone in never shows them a channel they could not see. N holds
// no role: picked with M, N still cannot join and the chat line leaves N
// out. V sees the channel, and the source alone does not let V join; V
// picked is let in.
func TestLetInSkipsWhoCannotSeeTheChannel(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)

	pick(t, tv, openPicker(t, tv, fake, lockOwner), lockOwner, permN, permM, permV)

	assertJoins(t, fake, "chan-1", "after the let-in", []permMember{permM, permV}, []permMember{permN})
	assertPingsExactly(t, onlyPing(t, fake, "chan-1"), permM, permV)
}

// A bot is never let in, even one that sees the channel, and a current
// guest is not let in again: the chat line pings the new guests alone.
func TestLetInSkipsBotsAndCurrentGuests(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)

	pick(t, tv, openPicker(t, tv, fake, lockOwner), lockOwner, letInBot, permG, permM)

	assertJoins(t, fake, "chan-1", "after the let-in", []permMember{permM, permG}, []permMember{letInBot})
	assertPingsExactly(t, onlyPing(t, fake, "chan-1"), permM)
}

// A let-in where nobody picked can be let in posts no chat line.
func TestLetInOfNobodyNewPostsNothing(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)

	pick(t, tv, openPicker(t, tv, fake, lockOwner), lockOwner, permN, permG)

	if pings := letInPingsIn(fake, "chan-1"); len(pings) != 0 {
		t.Errorf("let-in pings = %+v, want none when nobody was let in", pings)
	}
}

// Only a moderator of the channel's hub or whoever locked it can let
// someone in. Anyone else gets no picker from the button, and a submission
// of a picker someone else opened changes nothing: no join verdict, row,
// message or edit. The owner who did not lock the channel is anyone else
// here.
func TestLetInRefusedToAnyoneButAModeratorOrTheLocker(t *testing.T) {
	for _, tc := range []struct {
		name            string
		locker, presser permMember
	}{
		{"a member who is neither", lockOwner, permM},
		{"the owner, who did not lock it", permMOD, lockOwner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, st, tv := newLockScene(t, store.PermissionCategory)
			if tc.locker.id != lockOwner.id {
				enter(tv, fake, tc.locker, "chan-1")
			}
			lockAs(t, tv, tc.locker)
			button := noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeLetIn)
			picker := openPicker(t, tv, fake, tc.locker)
			before := sceneOf(t, fake, st, "chan-1")

			if _, ok := pressLetIn(t, tv, button, tc.presser); ok {
				t.Errorf("%s's press opened a member picker, want a refusal", tc.presser.id)
			}
			pick(t, tv, picker, tc.presser, permV)

			assertUnchanged(t, fake, st, "chan-1", tc.presser.id+"'s let-in", before)
		})
	}
}

// A button or picker left over from a lock that has ended lets nobody in.
// The button comes from the notice as first sent, and the picker was opened
// while the channel was locked.
func TestLetInLeftoverFromAnEndedLockChangesNothing(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	button := noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeLetIn)
	picker := openPicker(t, tv, fake, lockOwner)
	unlockAs(t, tv, lockOwner)
	before := sceneOf(t, fake, st, "chan-1")

	if _, ok := pressLetIn(t, tv, button, lockOwner); ok {
		t.Error("the leftover button opened a member picker, want a refusal")
	}
	pick(t, tv, picker, lockOwner, permV)

	assertUnchanged(t, fake, st, "chan-1", "the leftover let-in", before)
}

// Let someone in keeps working on a locked channel whose hub has since
// turned "Locking allowed" off.
func TestLetInWorksWithLockingTurnedOff(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	pushHub(t, st, tv, func(h *store.Hub) { h.LockingAllowed = false })

	pick(t, tv, openPicker(t, tv, fake, permMOD), permMOD, permV)

	assertJoins(t, fake, "chan-1", "after the let-in", []permMember{permV}, []permMember{permM})
}

// A member the bot cannot let in, because Discord refuses the guest add or
// the check whether they see the channel fails, stays out. Neither reaches
// Sentry. The presser's reply names them and is not the reply a let-in that
// went through gets, and no chat line pings them.
func TestLetInThatFailsNamesWhoDidNotGetIn(t *testing.T) {
	okFake, _, okTV := newLockScene(t, store.PermissionCategory)
	lockAs(t, okTV, lockOwner)
	letIn := pick(t, okTV, openPicker(t, okTV, okFake, lockOwner), lockOwner, permV)

	for _, tc := range []struct {
		name  string
		fails func(f *fakeTempVCManager)
	}{
		{"guest add refused by Discord", func(f *fakeTempVCManager) {
			f.permissionSetErr = restError(http.StatusInternalServerError, 0, rawBodyMarker)
		}},
		{"visibility check failed", func(f *fakeTempVCManager) {
			f.canSeeErr = discordgo.ErrStateNotFound
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, _, tv := newLockScene(t, store.PermissionCategory)
			lockAs(t, tv, lockOwner)
			picker := openPicker(t, tv, fake, lockOwner)
			captures := countCaptures(t)
			tc.fails(fake)

			reply := pick(t, tv, picker, lockOwner, permV)

			if reply == letIn {
				t.Errorf("reply %q is the reply to a let-in that went through", reply)
			}
			if token := "<@" + permV.id + ">"; !strings.Contains(reply, token) {
				t.Errorf("reply %q does not name %s, who did not get in", reply, token)
			}
			if strings.Contains(reply, rawBodyMarker) {
				t.Errorf("reply %q leaks the raw Discord error", reply)
			}
			if *captures != 0 {
				t.Errorf("captures = %d after the failed let-in, want 0", *captures)
			}
			assertJoins(t, fake, "chan-1", "after the failed let-in", nil, []permMember{permV})
			if pings := letInPingsIn(fake, "chan-1"); len(pings) != 0 {
				t.Errorf("let-in pings = %+v, want none when nobody got in", pings)
			}
		})
	}
}

// A chat line that fails to post leaves the let-in standing: V can join,
// and the presser's reply is not the one a let-in with its chat line gets.
func TestLetInPingThatFailsToPostLeavesTheGuestIn(t *testing.T) {
	okFake, _, okTV := newLockScene(t, store.PermissionCategory)
	lockAs(t, okTV, lockOwner)
	withPing := pick(t, okTV, openPicker(t, okTV, okFake, lockOwner), lockOwner, permV)

	fake, _, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	picker := openPicker(t, tv, fake, lockOwner)
	fake.messageErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)

	reply := pick(t, tv, picker, lockOwner, permV)

	if reply == withPing {
		t.Errorf("reply %q is the reply to a let-in whose chat line posted", reply)
	}
	if strings.Contains(reply, rawBodyMarker) {
		t.Errorf("reply %q leaks the raw Discord error", reply)
	}
	assertJoins(t, fake, "chan-1", "after the let-in", []permMember{permV}, []permMember{permM})
}

// An unlock pressed while a let-in is landing waits for it, then unlocks,
// and never lets the new guest past it: afterwards every member has the
// verdict the source alone gives, and V, let in meanwhile, cannot join.
func TestLetInUnlockPressedWhileALetInIsInFlight(t *testing.T) {
	fake, mgr, st, tv := newWindowScene(t)
	lockAs(t, tv, lockOwner)
	unlockButton := noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeUnlock)
	picker := openPicker(t, tv, fake, lockOwner)
	var unlock *started
	mgr.onSet(func() {
		unlock = startInteraction(func(f *fakeResponder) {
			runVoiceLock(f, tv, pressInteraction(unlockButton, permMOD))
		})
	})

	pick(t, tv, picker, lockOwner, permV)
	unlock.reply(t)

	assertUnlocked(t, fake, st, "chan-1", "after the let-in and the unlock")
}

// A let-in submitted while an unlock is landing waits for it, and then lets
// nobody in: afterwards V has the source's verdict and cannot join, and no
// chat line pings anyone.
func TestLetInSubmittedWhileAnUnlockIsInFlight(t *testing.T) {
	fake, mgr, _, tv := newWindowScene(t)
	lockAs(t, tv, lockOwner)
	picker := openPicker(t, tv, fake, lockOwner)
	var letIn *started
	mgr.onEdit(func() {
		letIn = startInteraction(func(f *fakeResponder) {
			runVoiceLock(f, tv, pickInteraction(picker, lockOwner, []permMember{permV}))
		})
	})

	unlockAs(t, tv, lockOwner)
	letIn.reply(t)

	assertJoinsLikeSource(t, fake, "chan-1", testTempVCCategory, "after the unlock")
	if pings := letInPingsIn(fake, "chan-1"); len(pings) != 0 {
		t.Errorf("let-in pings = %+v, want none for a let-in during the unlock", pings)
	}
}

// A let-in submitted while another is landing waits for it and then lets
// its own pick in, so both picks can join.
func TestLetInSubmittedWhileALetInIsInFlight(t *testing.T) {
	fake, mgr, _, tv := newWindowScene(t)
	lockAs(t, tv, lockOwner)
	picker := openPicker(t, tv, fake, lockOwner)
	var second *started
	mgr.onSet(func() {
		second = startInteraction(func(f *fakeResponder) {
			runVoiceLock(f, tv, pickInteraction(picker, permMOD, []permMember{permM}))
		})
	})

	pick(t, tv, picker, lockOwner, permV)
	second.reply(t)

	assertJoins(t, fake, "chan-1", "after both let-ins", []permMember{permV, permM}, []permMember{permN})
}

// The production adapter judges whether a member sees a channel with
// discordgo's permission calculation over the state cache's guild roles and
// channel, and the roles the caller passes. The cache here holds no member
// at all, as it holds no offline member of a large guild, and every verdict
// still comes out: M and V see the channel, N does not, and neither does
// H, whom a member overwrite hides. A channel missing from the cache is an
// error.
func TestSessionTempVCManagerCanSeeChannelJudgesByTheGivenRoles(t *testing.T) {
	hidden := permMember{id: "user-h", roles: []string{permRoleMember}}
	dg, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	overwrites := append(permCategoryOverwrites(), &discordgo.PermissionOverwrite{
		ID: hidden.id, Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionViewChannel,
	})
	feed(t, dg, &discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID: testTempVCGuild,
		Roles: []*discordgo.Role{
			{ID: testTempVCGuild, Permissions: permJoin},
			{ID: permRoleMember},
			{ID: permRoleViewer},
			{ID: permRoleMod},
		},
		Channels: []*discordgo.Channel{{
			ID: "chan-1", GuildID: testTempVCGuild, Type: discordgo.ChannelTypeGuildVoice,
			PermissionOverwrites: overwrites,
		}},
	}})
	mgr := NewSessionTempVCManager(dg)

	for _, tc := range []struct {
		m    permMember
		sees bool
	}{
		{permM, true},
		{permV, true},
		{permN, false},
		{hidden, false},
	} {
		got, err := mgr.CanSeeChannel("chan-1", tc.m.id, tc.m.roles)
		if err != nil {
			t.Errorf("CanSeeChannel(%s): %v", tc.m.id, err)
			continue
		}
		if got != tc.sees {
			t.Errorf("CanSeeChannel(%s) = %v, want %v", tc.m.id, got, tc.sees)
		}
	}
	if _, err := mgr.CanSeeChannel("chan-gone", permV.id, permV.roles); err == nil {
		t.Error("CanSeeChannel of a channel missing from the cache has no error, want one")
	}
}
