package commands

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// /warn, its notice's Remove button and /infractions (#971), driven through
// their handlers with the bot store, the interaction and Discord's channel
// and command permission calls stood in for.

const (
	warnGuild     = "100"
	warnApp       = "200"
	warnCommandID = "300"
	warnChannel   = "400"
	warnModerator = "999"
	warnTarget    = "500"
	warnModRole   = "600"
	warnImmune    = "700"
)

// fakeWarnDiscord records the notices posted and edited, and answers the
// command permission read with perms. Cached channels come from a session
// cache, through the session's own lookup, and a channel fetch from
// fetched or fetchErr.
type fakeWarnDiscord struct {
	sessionWarnDiscord
	mu       sync.Mutex
	sent     []*discordgo.MessageSend
	sentTo   []string
	edits    []*discordgo.MessageEdit
	editErr  error
	sendErr  error
	perms    []*discordgo.GuildApplicationCommandPermissions
	permsErr error
	fetched  map[string]*discordgo.Channel
	fetchErr error
}

func (f *fakeWarnDiscord) Channel(channelID string, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	if c, ok := f.fetched[channelID]; ok && f.fetchErr == nil {
		return c, nil
	}
	return nil, f.fetchErr
}

func (f *fakeWarnDiscord) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.sent = append(f.sent, data)
	f.sentTo = append(f.sentTo, channelID)
	return &discordgo.Message{ID: fmt.Sprintf("msg-%d", len(f.sent)), ChannelID: channelID}, nil
}

func (f *fakeWarnDiscord) ChannelMessageEditComplex(m *discordgo.MessageEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, m)
	if f.editErr != nil {
		return nil, f.editErr
	}
	return &discordgo.Message{ID: m.ID}, nil
}

func (f *fakeWarnDiscord) GuildApplicationCommandsPermissions(_, _ string, _ ...discordgo.RequestOption) ([]*discordgo.GuildApplicationCommandPermissions, error) {
	return f.perms, f.permsErr
}

// failingWarnStore fails the store calls given an error.
type failingWarnStore struct {
	*store.Fake
	immuneErr, addErr, removeErr, listErr, noticeErr error
}

func (f *failingWarnStore) SetWarningNotice(ctx context.Context, guildID string, id int64, messageID string) error {
	if f.noticeErr != nil {
		return f.noticeErr
	}
	return f.Fake.SetWarningNotice(ctx, guildID, id, messageID)
}

func (f *failingWarnStore) ListWarnings(ctx context.Context, guildID, memberID string) ([]store.Warning, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.Fake.ListWarnings(ctx, guildID, memberID)
}

func (f *failingWarnStore) GetWarningImmuneRoles(ctx context.Context, guildID string) (store.WarningImmuneRoles, error) {
	if f.immuneErr != nil {
		return store.WarningImmuneRoles{}, f.immuneErr
	}
	return f.Fake.GetWarningImmuneRoles(ctx, guildID)
}

func (f *failingWarnStore) AddWarning(ctx context.Context, w store.Warning) (store.Warning, error) {
	if f.addErr != nil {
		return store.Warning{}, f.addErr
	}
	return f.Fake.AddWarning(ctx, w)
}

func (f *failingWarnStore) RemoveWarning(ctx context.Context, guildID string, id int64) (store.Warning, error) {
	if f.removeErr != nil {
		return store.Warning{}, f.removeErr
	}
	return f.Fake.RemoveWarning(ctx, guildID, id)
}

// warnScene is a guild where /warn is registered and only warnModRole may
// use it, by a command-level override. warnThread is a thread of
// warnChannel.
type warnScene struct {
	t     *testing.T
	store *store.Fake
	fails *failingWarnStore
	d     *fakeWarnDiscord
	w     *warnings
}

const warnThread = "410"

func newWarnScene(t *testing.T) *warnScene {
	state := discordgo.NewState()
	guild := &discordgo.Guild{ID: warnGuild}
	if err := state.GuildAdd(guild); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*discordgo.Channel{
		{ID: warnChannel, GuildID: warnGuild, Type: discordgo.ChannelTypeGuildText},
		{ID: warnThread, GuildID: warnGuild, Type: discordgo.ChannelTypeGuildPublicThread, ParentID: warnChannel},
	} {
		if err := state.ChannelAdd(c); err != nil {
			t.Fatal(err)
		}
	}
	sc := &warnScene{t: t, store: store.NewFake(), d: &fakeWarnDiscord{sessionWarnDiscord: sessionWarnDiscord{&discordgo.Session{State: state}}}}
	sc.fails = &failingWarnStore{Fake: sc.store}
	sc.w = &warnings{store: sc.fails}
	sc.w.commandID.Store(warnCommandID)
	sc.d.perms = []*discordgo.GuildApplicationCommandPermissions{{
		ID: warnCommandID,
		Permissions: []*discordgo.ApplicationCommandPermissions{
			{ID: warnGuild, Type: discordgo.ApplicationCommandPermissionTypeRole, Permission: false},
			{ID: warnModRole, Type: discordgo.ApplicationCommandPermissionTypeRole, Permission: true},
		},
	}}
	return sc
}

// warnTargetIs describes the member /warn picks, as Discord resolves them.
type warnTargetIs struct {
	notInServer bool
	bot         bool
	permissions int64
	roles       []string
}

// warn runs /warn on warnTarget as warnModerator and returns the reply only
// the moderator sees.
func (sc *warnScene) warn(target warnTargetIs, reason string) string {
	sc.t.Helper()
	resolved := &discordgo.ApplicationCommandInteractionDataResolved{
		Users: map[string]*discordgo.User{warnTarget: {ID: warnTarget, Username: "rookie", Bot: target.bot}},
	}
	if !target.notInServer {
		resolved.Members = map[string]*discordgo.Member{warnTarget: {Roles: target.roles, Permissions: target.permissions}}
	}
	interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:      discordgo.InteractionApplicationCommand,
		GuildID:   warnGuild,
		ChannelID: warnChannel,
		Member:    &discordgo.Member{User: &discordgo.User{ID: warnModerator, Username: "sergeant"}},
		Data: discordgo.ApplicationCommandInteractionData{
			Name:     warnCommandName,
			Options:  []*discordgo.ApplicationCommandInteractionDataOption{stringOption("reason", reason), userOption("member", warnTarget)},
			Resolved: resolved,
		},
	}}
	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runWarn(f, sc.d, sc.w, interaction)
	return ephemeralRecordReply(sc.t, f)
}

// press presses the Remove button of the notice sent[n] as a member with
// roles and permissions, and returns the reply only they see.
func (sc *warnScene) press(n int, roles []string, permissions int64) string {
	sc.t.Helper()
	return sc.pressIn(warnChannel, n, roles, permissions)
}

// pressIn presses it as press does, with the notice in channelID.
func (sc *warnScene) pressIn(channelID string, n int, roles []string, permissions int64) string {
	sc.t.Helper()
	notice := sc.d.sent[n]
	button := notice.Components[0].(discordgo.ActionsRow).Components[0].(discordgo.Button)
	interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:      discordgo.InteractionMessageComponent,
		AppID:     warnApp,
		GuildID:   warnGuild,
		ChannelID: channelID,
		Member:    &discordgo.Member{User: &discordgo.User{ID: "presser", Username: "presser"}, Roles: roles, Permissions: permissions},
		Message:   &discordgo.Message{ID: fmt.Sprintf("msg-%d", n+1), ChannelID: channelID, Embeds: notice.Embeds},
		Data:      discordgo.MessageComponentInteractionData{CustomID: button.CustomID, ComponentType: discordgo.ButtonComponent},
	}}
	f := &fakeResponder{}
	runWarn(f, sc.d, sc.w, interaction)
	return ephemeralRecordReply(sc.t, f)
}

func (sc *warnScene) warnings() []store.Warning {
	sc.t.Helper()
	list, err := sc.store.ListWarnings(context.Background(), warnGuild, warnTarget)
	if err != nil {
		sc.t.Fatal(err)
	}
	return list
}

func TestWarnSavesTheWarningAndPostsANoticeThatNamesNoModerator(t *testing.T) {
	for _, target := range []warnTargetIs{{}, {notInServer: true}} {
		sc := newWarnScene(t)

		reply := sc.warn(target, "Spamming comms")

		wantReply(t, reply, "✅", warnTarget, warnedPhrase)
		list := sc.warnings()
		if len(list) != 1 {
			t.Fatalf("warnings = %+v, want one", list)
		}
		w := list[0]
		if w.ModeratorID != warnModerator || w.ChannelID != warnChannel || w.Reason != "Spamming comms" || w.MessageID != "msg-1" {
			t.Errorf("warning = %+v, want the moderator, the channel, the reason and the notice's message", w)
		}
		if len(sc.d.sent) != 1 || sc.d.sentTo[0] != warnChannel {
			t.Fatalf("notices = %v to %v, want one in the warning's channel", sc.d.sent, sc.d.sentTo)
		}
		notice := sc.d.sent[0]
		embed := notice.Embeds[0]
		if !strings.Contains(embed.Author.Name, "rookie") || !strings.Contains(embed.Description, "Spamming comms") {
			t.Errorf("notice embed = %+v, want the member's name and the reason", embed)
		}
		shown := fmt.Sprintf("%s %+v %+v", notice.Content, *embed, *embed.Author)
		if strings.Contains(shown, warnModerator) || strings.Contains(shown, "sergeant") {
			t.Errorf("notice %q names the moderator", shown)
		}
		if notice.AllowedMentions == nil || len(notice.AllowedMentions.Parse) != 0 {
			t.Errorf("notice allowed mentions = %+v, want none", notice.AllowedMentions)
		}
		customID := notice.Components[0].(discordgo.ActionsRow).Components[0].(discordgo.Button).CustomID
		if id, ok := parseWarnRemoveCustomID(customID); !ok || id != w.ID {
			t.Errorf("button CustomID = %q, want it to carry warning %d", customID, w.ID)
		}
		prefix, _, _ := strings.Cut(customID, customIDSeparator)
		if _, routed := NewRegistry(nil, nil, nil, nil, nil).GetHandler(prefix); !routed {
			t.Errorf("CustomID %q routes to %q, which is not a registered command", customID, prefix)
		}
	}
}

// The notice is up, so the warning stands. /infractions lists it without
// a link.
func TestWarnWhoseNoticeGoesUnrecordedStillWarns(t *testing.T) {
	sc := newWarnScene(t)
	sc.fails.noticeErr = errFirstRespond

	reply := sc.warn(warnTargetIs{}, "Reason")

	wantReply(t, reply, "✅", warnTarget, warnedPhrase)
	if list := sc.warnings(); len(list) != 1 || list[0].MessageID != "" || len(sc.d.sent) != 1 {
		t.Errorf("warnings %+v and notices %+v, want the warning kept with no notice recorded, and its notice up", list, sc.d.sent)
	}
}

func TestWarnRefusesAndSavesNothing(t *testing.T) {
	cases := []struct {
		name   string
		target warnTargetIs
		phrase string
		named  bool
	}{
		{name: "a bot", target: warnTargetIs{bot: true}, phrase: warnBotPhrase},
		{name: "an admin", target: warnTargetIs{permissions: discordgo.PermissionAdministrator}, phrase: warnAdminPhrase, named: true},
		{name: "an immune role holder", target: warnTargetIs{roles: []string{"other", warnImmune}}, phrase: warnImmunePhrase, named: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := newWarnScene(t)
			if err := sc.store.SaveWarningImmuneRoles(context.Background(), warnGuild,
				store.WarningImmuneRoles{RoleIDs: []string{warnImmune}}, store.ChangeLogEntry{Diff: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}

			reply := sc.warn(c.target, "Reason")

			if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, c.phrase) {
				t.Errorf("reply = %q, want a ❌ saying %q", reply, c.phrase)
			}
			if c.named && !strings.Contains(reply, mention(warnTarget)) {
				t.Errorf("reply = %q, want it to name the member", reply)
			}
			if list := sc.warnings(); len(list) != 0 || len(sc.d.sent) != 0 {
				t.Errorf("warnings %+v and notices %+v, want none", list, sc.d.sent)
			}
		})
	}
}

func TestWarnWithNoStoreRefuses(t *testing.T) {
	sc := newWarnScene(t)
	sc.w = &warnings{}

	reply := sc.warn(warnTargetIs{}, "Reason")

	if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, warningsOffPhrase) || len(sc.d.sent) != 0 {
		t.Errorf("reply = %q with notices %+v, want the off refusal and no notice", reply, sc.d.sent)
	}
}

func TestWarnWhoseNoticeFailsSavesNothing(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		kept     bool
		phrase   string
		captured bool
	}{
		{name: "no access", err: restError(http.StatusForbidden, discordgo.ErrCodeMissingAccess, "Missing Access"), phrase: warnCantPostPhrase},
		{name: "a Discord fault", err: restError(http.StatusInternalServerError, 0, "boom"), phrase: warnNotPostedPhrase, captured: true},
		{name: "a warning the store can't take back", err: restError(http.StatusInternalServerError, 0, "boom"), kept: true, phrase: warnOrphanPhrase, captured: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := newWarnScene(t)
			sc.d.sendErr = c.err
			if c.kept {
				sc.fails.removeErr = errFirstRespond
			}
			captures := recordCaptures(t)

			reply := sc.warn(warnTargetIs{}, "Reason")

			if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, c.phrase) {
				t.Errorf("reply = %q, want a ❌ saying %q", reply, c.phrase)
			}
			if list := sc.warnings(); (len(list) == 1) != c.kept {
				t.Errorf("warnings = %+v, want it kept only when the store can't remove it", list)
			}
			if (captures.count > 0) != c.captured {
				t.Errorf("captures = %d, want Sentry to hear of a Discord fault only", captures.count)
			}
		})
	}
}

func TestRemoveButtonByAMemberWhoMayWarnRemovesTheWarning(t *testing.T) {
	sc := newWarnScene(t)
	sc.warn(warnTargetIs{}, "Spamming comms")

	reply := sc.press(0, []string{warnModRole}, discordgo.PermissionUseApplicationCommands)

	wantReply(t, reply, "✅", warnTarget, warnRemovedPhrase)
	if list := sc.warnings(); len(list) != 0 {
		t.Errorf("warnings = %+v, want none", list)
	}
	if len(sc.d.edits) != 1 {
		t.Fatalf("notice edits = %+v, want one", sc.d.edits)
	}
	edit := sc.d.edits[0]
	if edit.ID != "msg-1" || edit.Channel != warnChannel {
		t.Errorf("edited %s in %s, want the notice", edit.ID, edit.Channel)
	}
	if edit.Components == nil || len(*edit.Components) != 0 {
		t.Errorf("edited components = %+v, want the button gone", edit.Components)
	}
	embed := (*edit.Embeds)[0]
	if embed.Footer == nil || embed.Footer.Text != warnRemovedFooter ||
		!strings.Contains(embed.Description, "Spamming comms") ||
		embed.Author == nil || !strings.Contains(embed.Author.Name, "rookie") {
		t.Errorf("edited embed = %+v, want the member, the reason struck through, and the removed footer", embed)
	}
}

func TestRemoveButtonRefusedChangesNothing(t *testing.T) {
	mod := []string{warnModRole}
	cases := []struct {
		name    string
		setup   func(sc *warnScene)
		channel string
		roles   []string
		phrase  string
	}{
		{name: "a member who may not warn", roles: []string{"other"}, phrase: warnNotPermittedPhrase},
		{name: "no store", setup: func(sc *warnScene) { sc.w = &warnings{} }, roles: mod, phrase: warningsOffPhrase},
		{
			name: "a thread of a channel /warn is denied in",
			setup: func(sc *warnScene) {
				cmd := sc.d.perms[0]
				cmd.Permissions = append(cmd.Permissions, &discordgo.ApplicationCommandPermissions{
					ID: warnChannel, Type: discordgo.ApplicationCommandPermissionTypeChannel, Permission: false,
				})
			},
			channel: warnThread, roles: mod, phrase: warnNotPermittedPhrase,
		},
		{
			name:    "a thread the cache lacks and Discord won't give",
			setup:   func(sc *warnScene) { sc.d.fetchErr = errFirstRespond },
			channel: "420", roles: mod, phrase: warnUnreadablePhrase,
		},
		{
			name: "a thread the cache lacks, of a channel /warn is denied in",
			setup: func(sc *warnScene) {
				sc.d.fetched = map[string]*discordgo.Channel{"420": {ID: "420", Type: discordgo.ChannelTypeGuildPublicThread, ParentID: warnChannel}}
				cmd := sc.d.perms[0]
				cmd.Permissions = append(cmd.Permissions, &discordgo.ApplicationCommandPermissions{
					ID: warnChannel, Type: discordgo.ApplicationCommandPermissionTypeChannel, Permission: false,
				})
			},
			channel: "420", roles: mod, phrase: warnNotPermittedPhrase,
		},
		{name: "before /warn's ID is known", setup: func(sc *warnScene) { sc.w = &warnings{store: sc.fails} }, roles: mod, phrase: warnStartingPhrase},
		{name: "permissions unreadable", setup: func(sc *warnScene) { sc.d.permsErr = errFirstRespond }, roles: mod, phrase: warnUnreadablePhrase},
		{name: "a store that can't remove it", setup: func(sc *warnScene) { sc.fails.removeErr = errFirstRespond }, roles: mod, phrase: warnNotRemovedPhrase},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := newWarnScene(t)
			sc.warn(warnTargetIs{}, "Reason")
			if c.setup != nil {
				c.setup(sc)
			}
			channel := c.channel
			if channel == "" {
				channel = warnChannel
			}

			reply := sc.pressIn(channel, 0, c.roles, discordgo.PermissionUseApplicationCommands)

			if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, c.phrase) {
				t.Errorf("reply = %q, want a ❌ saying %q", reply, c.phrase)
			}
			if list := sc.warnings(); len(list) != 1 || len(sc.d.edits) != 0 {
				t.Errorf("warnings %+v and edits %+v, want the warning and its notice untouched", list, sc.d.edits)
			}
		})
	}
}

func TestWarnWhoseStoreFailsPostsNoNotice(t *testing.T) {
	cases := []struct {
		name   string
		fail   func(f *failingWarnStore)
		phrase string
	}{
		{name: "immune roles unreadable", fail: func(f *failingWarnStore) { f.immuneErr = errFirstRespond }, phrase: warnUnreadablePhrase},
		{name: "the warning not saved", fail: func(f *failingWarnStore) { f.addErr = errFirstRespond }, phrase: warnNotSavedPhrase},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := newWarnScene(t)
			c.fail(sc.fails)

			reply := sc.warn(warnTargetIs{}, "Reason")

			if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, c.phrase) {
				t.Errorf("reply = %q, want a ❌ saying %q", reply, c.phrase)
			}
			if list := sc.warnings(); len(list) != 0 || len(sc.d.sent) != 0 {
				t.Errorf("warnings %+v and notices %+v, want none", list, sc.d.sent)
			}
		})
	}
}

// The press marks the notice removed, which repairs one whose edit at the
// removal failed.
func TestRemoveButtonOfARemovedWarningSaysSoAndMarksTheNoticeRemoved(t *testing.T) {
	sc := newWarnScene(t)
	sc.warn(warnTargetIs{}, "Reason")
	if _, err := sc.store.RemoveWarning(context.Background(), warnGuild, sc.warnings()[0].ID); err != nil {
		t.Fatal(err)
	}

	reply := sc.press(0, []string{warnModRole}, discordgo.PermissionUseApplicationCommands)

	if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, warnAlreadyRemovedPhrase) {
		t.Errorf("reply = %q, want a ❌ saying %q", reply, warnAlreadyRemovedPhrase)
	}
	if len(sc.d.edits) != 1 {
		t.Fatalf("notice edits = %+v, want one", sc.d.edits)
	}
	edit := sc.d.edits[0]
	if edit.Components == nil || len(*edit.Components) != 0 || edit.Embeds == nil ||
		(*edit.Embeds)[0].Footer == nil || (*edit.Embeds)[0].Footer.Text != warnRemovedFooter {
		t.Errorf("notice edit = %+v, want the button gone and the removed footer", edit)
	}
}

// infractions runs /infractions on warnTarget and returns the reply.
func infractions(t *testing.T, st warnStore) string {
	t.Helper()
	interaction := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:    discordgo.InteractionApplicationCommand,
		GuildID: warnGuild,
		Member:  &discordgo.Member{User: &discordgo.User{ID: warnModerator, Username: "sergeant"}},
		Data: discordgo.ApplicationCommandInteractionData{
			Name:    infractionsCommandName,
			Options: []*discordgo.ApplicationCommandInteractionDataOption{userOption("member", warnTarget)},
		},
	}}
	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runInfractions(f, st, interaction)
	return ephemeralRecordReply(t, f)
}

// addWarnings saves n warnings of warnTarget, oldest first, each with a
// notice but the first, and returns them.
func addWarnings(t *testing.T, st *store.Fake, n int, reason string) []store.Warning {
	t.Helper()
	var out []store.Warning
	for i := range n {
		w, err := st.AddWarning(context.Background(), store.Warning{
			GuildID: warnGuild, MemberID: warnTarget, ModeratorID: warnModerator, ChannelID: warnChannel,
			Reason: fmt.Sprintf("%s %d", reason, i),
		})
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			w.MessageID = fmt.Sprintf("notice-%d", w.ID)
			if err := st.SetWarningNotice(context.Background(), warnGuild, w.ID, w.MessageID); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, w)
	}
	return out
}

func TestInfractionsOfAMemberWithNoWarningsSaysSo(t *testing.T) {
	wantReply(t, infractions(t, store.NewFake()), "✅", warnTarget, noWarningsPhrase)
}

func TestInfractionsListsWarningsNewestFirstWithLinks(t *testing.T) {
	st := store.NewFake()
	added := addWarnings(t, st, 2, "Reason")

	reply := infractions(t, st)

	wantReply(t, reply, "⚠️", warnTarget, "2 warnings")
	lines := strings.Split(reply, "\n")
	if len(lines) != 3 {
		t.Fatalf("reply = %q, want a header and two lines", reply)
	}
	newest, oldest := lines[1], lines[2]
	link := "https://discord.com/channels/" + warnGuild + "/" + warnChannel + "/" + added[1].MessageID
	if !strings.Contains(newest, added[1].Reason) || !strings.Contains(newest, link) || !strings.Contains(newest, mention(warnModerator)) {
		t.Errorf("first line = %q, want the newest warning, its moderator and its notice's link", newest)
	}
	if !strings.Contains(oldest, added[0].Reason) || strings.Contains(oldest, "https://") {
		t.Errorf("second line = %q, want the oldest warning, with no link since it has no notice", oldest)
	}
}

func TestInfractionsThatOverflowEndWithACountOfTheRest(t *testing.T) {
	st := store.NewFake()
	const n = 20
	addWarnings(t, st, n, strings.Repeat("x", warnReasonLimit-3))

	reply := infractions(t, st)

	if got := len(reply); got > discordMessageLimit {
		t.Fatalf("reply is %d characters, over Discord's %d", got, discordMessageLimit)
	}
	lines := strings.Split(reply, "\n")
	listed := len(lines) - 2
	tail := lines[len(lines)-1]
	if listed < 1 || !strings.Contains(tail, fmt.Sprintf("%d %ss", n-listed, olderWarningsPhrase)) {
		t.Errorf("reply = %q, want as many lines as fit and a count of the %d left", reply, n-listed)
	}
	if !strings.Contains(lines[1], fmt.Sprintf(" %d · ", n-1)) {
		t.Errorf("first line = %q, want the newest warning", lines[1])
	}
}

func TestInfractionsWithNoReadableStoreSaysSo(t *testing.T) {
	cases := []struct {
		name   string
		st     warnStore
		phrase string
	}{
		{name: "no store", phrase: warningsOffPhrase},
		{name: "a failed read", st: &failingWarnStore{Fake: store.NewFake(), listErr: errFirstRespond}, phrase: warnUnreadablePhrase},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reply := infractions(t, c.st)
			if !strings.HasPrefix(reply, "❌") || !strings.Contains(reply, c.phrase) {
				t.Errorf("reply = %q, want a ❌ saying %q", reply, c.phrase)
			}
		})
	}
}

// A notice Discord won't edit leaves the press's reply saying so, whether
// the press removed the warning or found it already removed.
func TestRemoveButtonWhoseNoticeEditFailsSaysSo(t *testing.T) {
	cases := []struct {
		name           string
		alreadyRemoved bool
		verdict        string
	}{
		{name: "a removal", verdict: "✅"},
		{name: "a warning already removed", alreadyRemoved: true, verdict: "❌"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := newWarnScene(t)
			sc.warn(warnTargetIs{}, "Reason")
			if c.alreadyRemoved {
				if _, err := sc.store.RemoveWarning(context.Background(), warnGuild, sc.warnings()[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			sc.d.editErr = errFirstRespond

			reply := sc.press(0, []string{warnModRole}, discordgo.PermissionUseApplicationCommands)

			if !strings.HasPrefix(reply, c.verdict) || !strings.Contains(reply, warnNoticeStalePhrase) {
				t.Errorf("reply = %q, want a %s saying %q", reply, c.verdict, warnNoticeStalePhrase)
			}
			if list := sc.warnings(); len(list) != 0 {
				t.Errorf("warnings = %+v, want the warning gone", list)
			}
		})
	}
}
