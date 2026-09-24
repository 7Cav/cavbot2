package commands

import (
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The lock notice and its Unlock button (#351). Every press uses a CustomID
// read off the components the fake recorded on the notice, and goes through
// the handler main.go's dispatcher would pick for it. A press is judged by
// who can join the channel afterwards, the row's lock and the notice's
// edit. Nothing here asserts reply wording, the CustomID's format or a log
// line.

// componentRoutes are the handlers a press can reach, by the registered
// command name main.go's dispatcher reads off a CustomID's first segment.
var componentRoutes = map[string]func(utils.InteractionResponder, *TempVC, *discordgo.InteractionCreate){
	voiceLockCommandName:   runVoiceLock,
	voiceUnlockCommandName: runVoiceUnlock,
}

// pressInteraction builds the interaction a press on a button with the
// given CustomID sends from m.
func pressInteraction(customID string, m permMember) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:    discordgo.InteractionMessageComponent,
		GuildID: testTempVCGuild,
		Data:    discordgo.MessageComponentInteractionData{CustomID: customID, ComponentType: discordgo.ButtonComponent},
		Member:  &discordgo.Member{User: &discordgo.User{ID: m.id, Username: "tester"}, Roles: slices.Clone(m.roles)},
	}}
}

// pressWith delivers m's press to the handler main.go's dispatcher routes
// the CustomID to, answering through f. The route must be a registered
// command, or Discord's press would reach nothing.
func pressWith(t *testing.T, f *fakeResponder, tv *TempVC, customID string, m permMember) {
	t.Helper()
	prefix := strings.Split(customID, "::")[0]
	if _, ok := NewRegistry(nil).GetHandler(prefix); !ok {
		t.Fatalf("CustomID %q routes to %q, which is not a registered command", customID, prefix)
	}
	run, ok := componentRoutes[prefix]
	if !ok {
		t.Fatalf("CustomID %q routes to %q, which takes no button", customID, prefix)
	}
	run(f, tv, pressInteraction(customID, m))
}

// press delivers m's press, checks the answer is a new ephemeral message
// that leaves the notice alone, and returns it.
func press(t *testing.T, tv *TempVC, customID string, m permMember) string {
	t.Helper()
	f := &fakeResponder{}
	pressWith(t, f, tv, customID, m)
	return ephemeralReply(t, f)
}

// buttonsOf lists the buttons in a message's components, rows included.
func buttonsOf(components []discordgo.MessageComponent) []discordgo.Button {
	var out []discordgo.Button
	for _, c := range components {
		switch c := c.(type) {
		case discordgo.ActionsRow:
			out = append(out, buttonsOf(c.Components)...)
		case *discordgo.ActionsRow:
			out = append(out, buttonsOf(c.Components)...)
		case discordgo.Button:
			out = append(out, c)
		case *discordgo.Button:
			out = append(out, *c)
		}
	}
	return out
}

// buttonFor returns the CustomID of a message's button for a lock notice
// action, or false when it carries none.
func buttonFor(msg fakeMessage, action string) (string, bool) {
	for _, b := range buttonsOf(msg.data.Components) {
		if a, _, ok := parseLockNoticeCustomID(b.CustomID); ok && a == action {
			return b.CustomID, true
		}
	}
	return "", false
}

// noticeButton returns the CustomID of a lock notice's button for an
// action, and fails the test when the notice carries none.
func noticeButton(t *testing.T, notice fakeMessage, action string) string {
	t.Helper()
	id, ok := buttonFor(notice, action)
	if !ok {
		t.Fatalf("notice %s carries no %s button: %+v", notice.id, action, notice.data.Components)
	}
	return id
}

// lockNoticesIn lists the lock notices posted in a channel: its messages
// that carry an Unlock button.
func lockNoticesIn(fake *fakeTempVCManager, channelID string) []fakeMessage {
	var out []fakeMessage
	for _, m := range fake.recordedMessages() {
		if _, ok := buttonFor(m, lockNoticeUnlock); ok && m.channelID == channelID {
			out = append(out, m)
		}
	}
	return out
}

// lockNotice returns the last lock notice posted in a channel, as sent.
func lockNotice(t *testing.T, fake *fakeTempVCManager, channelID string) fakeMessage {
	t.Helper()
	notices := lockNoticesIn(fake, channelID)
	if len(notices) == 0 {
		t.Fatalf("no lock notice in %s", channelID)
	}
	return notices[len(notices)-1]
}

// noticeEdit returns the last edit made to a notice, and fails the test when
// there is none.
func noticeEdit(t *testing.T, fake *fakeTempVCManager, notice fakeMessage) discordgo.MessageEdit {
	t.Helper()
	var found []discordgo.MessageEdit
	for _, e := range fake.recordedMessageEdits() {
		if e.Channel == notice.channelID && e.ID == notice.id {
			found = append(found, e)
		}
	}
	if len(found) == 0 {
		t.Fatalf("notice %s in %s was never edited", notice.id, notice.channelID)
	}
	return found[len(found)-1]
}

// pingsNobody reports whether allowed mentions permit no user, no role and
// no parsed mention. A nil value lets Discord parse every mention.
func pingsNobody(am *discordgo.MessageAllowedMentions) bool {
	return am != nil && len(am.Parse) == 0 && len(am.Users) == 0 && len(am.Roles) == 0
}

// channelScene is what a refused press must leave as it was: the edits,
// messages and message edits sent so far, the row's lock, and who can join.
type channelScene struct {
	edits, messages, messageEdits int
	lock                          store.ChannelLock
	joins                         map[string]bool
}

func sceneOf(t *testing.T, fake *fakeTempVCManager, st store.Store, channelID string) channelScene {
	t.Helper()
	return channelScene{
		edits:        len(fake.recordedEdits()),
		messages:     len(fake.recordedMessages()),
		messageEdits: len(fake.recordedMessageEdits()),
		lock:         rowLock(t, st, channelID),
		joins:        joinVerdicts(t, fake, channelID),
	}
}

// assertUnchanged checks a scene against the one taken before.
func assertUnchanged(t *testing.T, fake *fakeTempVCManager, st store.Store, channelID, what string, before channelScene) {
	t.Helper()
	if after := sceneOf(t, fake, st, channelID); !reflect.DeepEqual(before, after) {
		t.Errorf("%s changed %s from %+v to %+v, want nothing changed", what, channelID, before, after)
	}
}

// assertUnlocked checks a channel has its source's verdicts back and its
// row reads unlocked.
func assertUnlocked(t *testing.T, fake *fakeTempVCManager, st store.Store, channelID, when string) {
	t.Helper()
	assertJoinsLikeSource(t, fake, channelID, testTempVCCategory, when)
	if lock := rowLock(t, st, channelID); lock != (store.ChannelLock{}) {
		t.Errorf("%s: row lock = %+v, want unlocked", when, lock)
	}
}

// unlockPaths are the two ways to unlock: /voice-unlock from inside the
// channel, and the lock notice's Unlock button.
var unlockPaths = []struct {
	name   string
	unlock func(t *testing.T, tv *TempVC, fake *fakeTempVCManager, m permMember)
}{
	{"by /voice-unlock", func(t *testing.T, tv *TempVC, _ *fakeTempVCManager, m permMember) {
		unlockAs(t, tv, m)
	}},
	{"by the Unlock button", func(t *testing.T, tv *TempVC, fake *fakeTempVCManager, m permMember) {
		press(t, tv, noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeUnlock), m)
	}},
}

// The lock notice names who locked the channel, pings nobody and carries an
// Unlock button. At unlock, by either path, it is edited in place to name
// who unlocked the channel, still pinging nobody, and loses its buttons.
func TestLockNoticeNamesTheLockerAndThenTheUnlocker(t *testing.T) {
	for _, path := range unlockPaths {
		t.Run(path.name, func(t *testing.T) {
			fake, st, tv := newLockScene(t, store.PermissionCategory)
			lockAs(t, tv, lockOwner)

			notice := lockNotice(t, fake, "chan-1")
			if want := "<@" + lockOwner.id + ">"; !strings.Contains(notice.data.Content, want) {
				t.Errorf("notice content %q does not mention the locker %s", notice.data.Content, want)
			}
			if !pingsNobody(notice.data.AllowedMentions) {
				t.Errorf("notice allowed mentions = %+v, want nobody pingable", notice.data.AllowedMentions)
			}

			enter(tv, fake, permMOD, "chan-1")
			path.unlock(t, tv, fake, permMOD)

			assertUnlocked(t, fake, st, "chan-1", "after the unlock")
			edit := noticeEdit(t, fake, notice)
			if want := "<@" + permMOD.id + ">"; edit.Content == nil || !strings.Contains(*edit.Content, want) {
				t.Errorf("notice edit content = %v, want it to mention the unlocker %s", edit.Content, want)
			}
			// A nil list leaves the buttons in place; an empty one removes them.
			if edit.Components == nil || len(*edit.Components) != 0 {
				t.Errorf("notice edit components = %v, want an empty list", edit.Components)
			}
			if !pingsNobody(edit.AllowedMentions) {
				t.Errorf("notice edit allowed mentions = %+v, want nobody pingable", edit.AllowedMentions)
			}
		})
	}
}

// Unlock answers to a moderator of the channel's hub and to whoever locked
// the channel, and to nobody else. A moderator presses from outside the
// channel. A refused press answers only the presser and changes nothing.
func TestLockNoticeUnlockAnswersToModeratorsAndTheLocker(t *testing.T) {
	for _, tc := range []struct {
		name            string
		locker, presser permMember
		unlocks         bool
	}{
		{"the locker, who owns the channel", lockOwner, lockOwner, true},
		{"a moderator who did not lock it", lockOwner, permMOD, true},
		{"the owner, who did not lock it", permMOD, lockOwner, false},
		{"a member who is neither", lockOwner, permM, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, st, tv := newLockScene(t, store.PermissionCategory)
			if tc.locker.id != lockOwner.id {
				enter(tv, fake, tc.locker, "chan-1")
			}
			lockAs(t, tv, tc.locker)
			button := noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeUnlock)
			before := sceneOf(t, fake, st, "chan-1")

			press(t, tv, button, tc.presser)

			if tc.unlocks {
				assertUnlocked(t, fake, st, "chan-1", "after the press")
			} else {
				assertUnchanged(t, fake, st, "chan-1", tc.presser.id+"'s press", before)
			}
		})
	}
}

// The buttons stay with whoever locked the channel through a handover. The
// owner locks and leaves, a non-moderator takes over, and the old owner
// comes back as a guest. The new owner's press is refused. The old owner's
// press unlocks, and so does the new owner's /voice-unlock.
func TestLockNoticeUnlockStaysWithTheLockerAfterAHandover(t *testing.T) {
	next := permMember{id: "user-p", roles: []string{permRoleMember, testRankPVT}}
	for _, tc := range []struct {
		name   string
		unlock func(t *testing.T, tv *TempVC, button string)
	}{
		{"the old owner presses Unlock", func(t *testing.T, tv *TempVC, button string) { press(t, tv, button, lockOwner) }},
		{"the new owner runs /voice-unlock", func(t *testing.T, tv *TempVC, _ string) { unlockAs(t, tv, next) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTempVCManager()
			installPermFixture(fake)
			st := seedStore(t, lockingHub(store.PermissionCategory))
			tv := newTestTempVC(t, fake, st)
			spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
			enter(tv, fake, next, "chan-1")
			lockAs(t, tv, lockOwner)
			button := noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeUnlock)
			enter(tv, fake, lockOwner, "")
			if owner, _ := tv.Owner("chan-1"); owner != next.id {
				t.Fatalf("owner after the handover = %q, want %s", owner, next.id)
			}
			assertJoins(t, fake, "chan-1", "after the handover", []permMember{lockOwner}, []permMember{permM})
			enter(tv, fake, lockOwner, "chan-1")
			before := sceneOf(t, fake, st, "chan-1")

			press(t, tv, button, next)
			assertUnchanged(t, fake, st, "chan-1", "the new owner's press", before)

			tc.unlock(t, tv, button)
			assertUnlocked(t, fake, st, "chan-1", "after the unlock")
		})
	}
}

// A button left over from a lock that has ended answers only the presser
// and changes nothing, whoever presses it. Its CustomID comes from the
// notice as first sent, before the unlock took its buttons away.
func TestLockNoticeLeftoverButtonChangesNothing(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	button := noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeUnlock)
	unlockAs(t, tv, lockOwner)

	for _, m := range []permMember{lockOwner, permMOD} {
		before := sceneOf(t, fake, st, "chan-1")
		press(t, tv, button, m)
		assertUnchanged(t, fake, st, "chan-1", m.id+"'s press on the leftover button", before)
	}
}

// A lock whose notice fails to post still stands, and the invoker's reply
// is not the one a lock with its notice gets. /voice-unlock still ends it.
func TestLockNoticeThatFailsToPostLeavesTheLockStanding(t *testing.T) {
	_, _, posted := newLockScene(t, store.PermissionCategory)
	withNotice := lockAs(t, posted, lockOwner)

	fake, st, tv := newLockScene(t, store.PermissionCategory)
	fake.messageErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)

	reply := lockAs(t, tv, lockOwner)

	if reply == withNotice {
		t.Errorf("reply %q is the reply to a lock whose notice posted, want it to say the notice did not", reply)
	}
	if strings.Contains(reply, rawBodyMarker) {
		t.Errorf("reply %q leaks the raw Discord error", reply)
	}
	assertJoins(t, fake, "chan-1", "locked with no notice", []permMember{permG, permMOD}, []permMember{permM})
	if lock := rowLock(t, st, "chan-1"); !lock.Locked {
		t.Errorf("row lock = %+v, want locked", lock)
	}

	fake.messageErr = nil
	unlockAs(t, tv, lockOwner)
	assertUnlocked(t, fake, st, "chan-1", "after the unlock")
}

// A notice edit Discord refuses never holds up the unlock, by either path.
func TestLockNoticeEditThatFailsDoesNotHoldUpTheUnlock(t *testing.T) {
	for _, path := range unlockPaths {
		t.Run(path.name, func(t *testing.T) {
			fake, st, tv := newLockScene(t, store.PermissionCategory)
			lockAs(t, tv, lockOwner)
			fake.messageEditErr = restError(http.StatusNotFound, discordgo.ErrCodeUnknownMessage, rawBodyMarker)

			path.unlock(t, tv, fake, lockOwner)

			assertUnlocked(t, fake, st, "chan-1", "after the unlock")
		})
	}
}

// A lock Discord refuses posts no notice.
func TestLockNoticeNotPostedWhenDiscordRefusesTheLock(t *testing.T) {
	fake, _, tv := newLockScene(t, store.PermissionCategory)
	fake.editErr = restError(http.StatusInternalServerError, 0, rawBodyMarker)

	lockAs(t, tv, lockOwner)

	if notices := lockNoticesIn(fake, "chan-1"); len(notices) != 0 {
		t.Errorf("lock notices = %+v after a refused lock, want none", notices)
	}
}

// The notice's message ID and the locker are stored with the lock. After a
// restart the sweep posts nothing, and the button from before the restart
// still unlocks for whoever locked the channel, editing that same notice.
func TestLockNoticeOutlivesARestart(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	notice := lockNotice(t, fake, "chan-1")
	sent := len(fake.recordedMessages())

	restarted := newTestTempVC(t, fake, st)
	fake.deliverGuildCreate(restarted, permFixturePayload([]string{"chan-1"},
		map[string]string{permG.id: "chan-1", lockOwner.id: "chan-1"}))

	if got := len(fake.recordedMessages()); got != sent {
		t.Errorf("messages = %d after the sweep, want %d: the sweep posts nothing", got, sent)
	}
	press(t, restarted, noticeButton(t, notice, lockNoticeUnlock), lockOwner)
	assertUnlocked(t, fake, st, "chan-1", "after the press")
	if edit := noticeEdit(t, fake, notice); edit.Content == nil || !strings.Contains(*edit.Content, "<@"+lockOwner.id+">") {
		t.Errorf("notice edit content = %v, want it to mention the unlocker %s", edit.Content, lockOwner.id)
	}
}

// Discord refuses a message carrying a CustomID over 100 characters. A
// channel ID long enough to push one past that gets no such button, and the
// lock stands.
func TestLockNoticeCustomIDsStayWithinDiscordsCap(t *testing.T) {
	long := strings.Repeat("9", 100)
	fake := newFakeTempVCManager()
	installPermFixture(fake)
	st := seedStore(t, lockingHub(store.PermissionCategory))
	tv := newTestTempVC(t, fake, st)
	spawnInto(tv, fake, lockOwner.id, long, lockOwner.discordMember())

	lockAs(t, tv, lockOwner)

	for _, m := range fake.recordedMessages() {
		for _, b := range buttonsOf(m.data.Components) {
			if len(b.CustomID) > 100 {
				t.Errorf("message %s carries CustomID %q, %d characters", m.id, b.CustomID, len(b.CustomID))
			}
		}
	}
	assertJoins(t, fake, long, "locked", []permMember{lockOwner, permMOD}, []permMember{permM})
}

// A press the bot cannot acknowledge changes nothing, and nothing is sent
// that would overwrite the notice the button sits on.
func TestLockNoticePressThatCannotBeAcknowledgedChangesNothing(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	button := noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeUnlock)
	countCaptures(t)
	before := sceneOf(t, fake, st, "chan-1")
	f := &fakeResponder{RespondErrs: []error{errors.New("unknown interaction")}}

	pressWith(t, f, tv, button, lockOwner)

	for _, c := range f.Calls() {
		if c.Method == "Respond" && c.Response != nil &&
			(c.Response.Type == discordgo.InteractionResponseUpdateMessage ||
				c.Response.Type == discordgo.InteractionResponseDeferredMessageUpdate) {
			t.Errorf("response %+v updates the notice the button sits on", c.Response)
		}
	}
	assertUnchanged(t, fake, st, "chan-1", "the unacknowledged press", before)
}

// A reply to a press that Discord loses is captured, and the unlock it
// answered stands.
func TestLockNoticeLostReplyIsCaptured(t *testing.T) {
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	button := noticeButton(t, lockNotice(t, fake, "chan-1"), lockNoticeUnlock)
	captures := countCaptures(t)
	f := &fakeResponder{EditErrs: []error{errors.New("unknown webhook")}}

	pressWith(t, f, tv, button, lockOwner)

	if *captures != 1 {
		t.Errorf("captures = %d, want 1 for the lost reply", *captures)
	}
	assertUnlocked(t, fake, st, "chan-1", "after the press")
}

// A button with nothing to act on answers only the presser: one from a
// build whose action is gone changes nothing, and on a host with no bot
// store the press is refused.
func TestLockNoticeButtonWithNothingToDo(t *testing.T) {
	retired, err := lockNoticeCustomID("retired", "chan-1")
	if err != nil {
		t.Fatalf("lockNoticeCustomID: %v", err)
	}
	fake, st, tv := newLockScene(t, store.PermissionCategory)
	lockAs(t, tv, lockOwner)
	before := sceneOf(t, fake, st, "chan-1")

	press(t, tv, retired, lockOwner)

	assertUnchanged(t, fake, st, "chan-1", "a press on a retired button", before)

	unlock, err := lockNoticeCustomID(lockNoticeUnlock, "chan-1")
	if err != nil {
		t.Fatalf("lockNoticeCustomID: %v", err)
	}
	press(t, nil, unlock, permMOD)
}
