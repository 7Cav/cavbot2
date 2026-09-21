package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

const testGuildID = "guild-1"

// fakeDiscord is the manager fake beneath the panel: the guild's channel list
// the panel reads, and the create, move and delete calls the runtime makes
// when a join spawns. Mutex-guarded because the runtime is driven from the
// test goroutine while the suite runs under -race.
type fakeDiscord struct {
	mu       sync.Mutex
	channels []*discordgo.Channel
	// listErr, when set, is what GuildChannels returns: Discord not
	// answering the panel's read.
	listErr error
	// createErr, when set, is what every create returns.
	createErr error
	// editErr, when set, is what every edit returns.
	editErr error
	// premiumTier is the boost tier Guild reports.
	premiumTier discordgo.PremiumTier
	created     []fakeCreate
	edited      []fakeEdit
	deleted     []string
	spawned     int
	// voice stands in for the state cache's voice states: user ID to
	// channel ID, connected members only. The panel's tests never take the
	// guild away, so the guild is always present.
	voice map[string]string
}

// fakeEdit is one edit call as the fake recorded it: the channel, the name
// sent and the audit log reason.
type fakeEdit struct {
	ChannelID string
	Name      string
	Reason    string
}

// fakeCreate is one create call as the fake recorded it: the payload and
// the audit log reason.
type fakeCreate struct {
	Data   discordgo.GuildChannelCreateData
	Reason string
}

// testGuildRoles are the guild's roles as Discord returns them: the two
// eligible roles the moderator pickers offer, the managed role Discord made
// for a bot, and @everyone, whose ID is the guild's. A deleted role is any
// ID this list never had; the tests use role-gone.
var testGuildRoles = []*discordgo.Role{
	{ID: "role-mp", Name: "Military Police"},
	{ID: "role-hq", Name: "Regimental HQ"},
	{ID: "role-bot", Name: "CavBot", Managed: true},
	{ID: testGuildID, Name: "@everyone"},
}

// newFakeDiscord returns a guild with one category holding a voice channel
// the tests register as a hub, a second voice channel, a text channel, and a
// voice channel with no parent outside it.
func newFakeDiscord() *fakeDiscord {
	return &fakeDiscord{channels: []*discordgo.Channel{
		{ID: "cat-1", Name: "Arma Reforger", Type: discordgo.ChannelTypeGuildCategory},
		{ID: "hub-1", Name: "Join to create", Type: discordgo.ChannelTypeGuildVoice, ParentID: "cat-1"},
		{ID: "vc-2", Name: "Squad Join", Type: discordgo.ChannelTypeGuildVoice, ParentID: "cat-1"},
		{ID: "text-1", Name: "general", Type: discordgo.ChannelTypeGuildText, ParentID: "cat-1"},
		{ID: "vc-noparent", Name: "Lobby", Type: discordgo.ChannelTypeGuildVoice},
	}, voice: map[string]string{}}
}

func (f *fakeDiscord) Channel(channelID string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.channels {
		if ch.ID == channelID {
			return ch, nil
		}
	}
	return nil, discordgo.ErrStateNotFound
}

func (f *fakeDiscord) GuildChannels(_ string) ([]*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.channels, nil
}

// GuildChannelCreateComplex records the call and, as Discord would, puts
// the new channel in the guild's list so a later page load reads it.
func (f *fakeDiscord) GuildChannelCreateComplex(_ string, data discordgo.GuildChannelCreateData, reason string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, fakeCreate{Data: data, Reason: reason})
	f.spawned++
	ch := &discordgo.Channel{ID: fmt.Sprintf("spawn-%d", f.spawned), Name: data.Name, Type: data.Type, ParentID: data.ParentID}
	f.channels = append(f.channels, ch)
	return ch, nil
}

// ChannelDelete records the call and takes the channel out of the guild's
// list.
func (f *fakeDiscord) ChannelDelete(channelID, _ string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, channelID)
	f.channels = slices.DeleteFunc(f.channels, func(ch *discordgo.Channel) bool { return ch.ID == channelID })
	return &discordgo.Channel{ID: channelID}, nil
}

// ChannelEdit records the call and, as Discord would, renames the channel
// in the guild's list.
func (f *fakeDiscord) ChannelEdit(channelID string, data *discordgo.ChannelEdit, reason string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return nil, f.editErr
	}
	f.edited = append(f.edited, fakeEdit{ChannelID: channelID, Name: data.Name, Reason: reason})
	for _, ch := range f.channels {
		if ch.ID == channelID {
			ch.Name = data.Name
		}
	}
	return &discordgo.Channel{ID: channelID, Name: data.Name}, nil
}

func (f *fakeDiscord) GuildMemberMove(_, _ string, _ *string) error { return nil }

func (f *fakeDiscord) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error) {
	return &discordgo.Message{ChannelID: channelID, Content: data.Content}, nil
}

func (f *fakeDiscord) GuildMember(_, _ string) (*discordgo.Member, error) { return nil, nil }

// VoiceStates copies the fake cache: who is where, and the guild's
// channels. Any guild but the test guild is absent.
func (f *fakeDiscord) VoiceStates(guildID string) commands.VoiceSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if guildID != testGuildID {
		return commands.VoiceSnapshot{}
	}
	snap := commands.VoiceSnapshot{
		Present:       true,
		ChannelByUser: make(map[string]string, len(f.voice)),
		Channels:      make(map[string]struct{}, len(f.channels)),
	}
	for user, ch := range f.voice {
		snap.ChannelByUser[user] = ch
	}
	for _, ch := range f.channels {
		snap.Channels[ch.ID] = struct{}{}
	}
	return snap
}

// MemberRanks reads the payload through the runtime's shared helper. The
// panel's tests never run a sweep; the fake carries it for the interface.
func (f *fakeDiscord) MemberRanks(g *discordgo.Guild) map[string]int {
	return commands.GuildMemberRanks(g)
}

// setVoice puts a member in a channel in the fake cache, or disconnects
// them for an empty channel.
func (f *fakeDiscord) setVoice(userID, channelID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if channelID == "" {
		delete(f.voice, userID)
		return
	}
	f.voice[userID] = channelID
}

func (f *fakeDiscord) Guild(_ string) (*discordgo.Guild, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &discordgo.Guild{ID: testGuildID, Roles: testGuildRoles, PremiumTier: f.premiumTier}, nil
}

func (f *fakeDiscord) setEditErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.editErr = err
}

func (f *fakeDiscord) setPremiumTier(tier discordgo.PremiumTier) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.premiumTier = tier
}

// edits returns every edit call, in order.
func (f *fakeDiscord) edits() []fakeEdit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.edited)
}

func (f *fakeDiscord) setListErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listErr = err
}

func (f *fakeDiscord) setCreateErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createErr = err
}

func (f *fakeDiscord) createCount() int {
	return len(f.creates())
}

// creates returns every create payload sent, in order.
func (f *fakeDiscord) creates() []discordgo.GuildChannelCreateData {
	var out []discordgo.GuildChannelCreateData
	for _, c := range f.createCalls() {
		out = append(out, c.Data)
	}
	return out
}

// createCalls returns every create call, payload and reason, in order.
func (f *fakeDiscord) createCalls() []fakeCreate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.created)
}

// deletes returns the ID of every channel the runtime deleted, in order.
func (f *fakeDiscord) deletes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
}

// testWorld is the panel with everything beneath it: the fake forum, the
// store fake, the fake Discord and the real runtime built over both.
type testWorld struct {
	forum   *fakeForum
	st      *store.Fake
	discord *fakeDiscord
	runtime *commands.TempVC
	p       *Panel
	b       *browser
}

// newTestWorld builds the panel over a store holding the given hubs. The
// hubs go in before the runtime is built, the way startup loads them.
func newTestWorld(t *testing.T, hubs ...store.Hub) *testWorld {
	t.Helper()
	return newTestWorldWith(t, newFakeForum(t), hubs...)
}

func newTestWorldWith(t *testing.T, f *fakeForum, hubs ...store.Hub) *testWorld {
	t.Helper()
	st := store.NewFake()
	for _, h := range hubs {
		if _, err := st.UpsertHub(context.Background(), h); err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
	}
	return newTestWorldOver(t, st, f)
}

// newTestWorldOver builds the panel and the runtime over any store. st on
// the returned world is the store when it is the fake, and nil for a store
// that wraps it to refuse writes: such a test reads back through the fake
// it wrapped.
func newTestWorldOver(t *testing.T, st store.Store, f *fakeForum) *testWorld {
	t.Helper()
	discord := newFakeDiscord()
	runtime, err := commands.NewTempVC(discord, st, testGuildID)
	if err != nil {
		t.Fatalf("NewTempVC: %v", err)
	}
	p, err := New(testConfig(f), testVersion, Deps{Store: st, Runtime: runtime, Manager: discord, GuildID: testGuildID})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fake, _ := st.(*store.Fake)
	return &testWorld{forum: f, st: fake, discord: discord, runtime: runtime, p: p, b: newBrowser(t, p)}
}

// testHub is a stored hub on hub-1 with the defaults a register writes.
func testHub() store.Hub {
	return store.Hub{
		GuildID: testGuildID, HubChannelID: "hub-1", BaseString: "Arma Voice",
		PermissionSource: store.PermissionCategory, Bitrate: 64000, Enabled: true,
	}
}

// join feeds the runtime the gateway event for a member joining a channel,
// applied to the fake cache first, the order discordgo keeps.
func (w *testWorld) join(userID, channelID string) {
	w.discord.setVoice(userID, channelID)
	w.runtime.HandleVoiceStateUpdate(&discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: testGuildID, UserID: userID, ChannelID: channelID, Member: &discordgo.Member{},
	}})
}

// storedHubs lists the store's hub rows for the test guild.
func storedHubs(t *testing.T, st store.Store) []store.Hub {
	t.Helper()
	hubs, err := st.ListHubs(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("ListHubs: %v", err)
	}
	return hubs
}

// storedHubID reads back the surrogate ID the store gave the hub on a channel.
func storedHubID(t *testing.T, st store.Store, hubChannelID string) int64 {
	t.Helper()
	for _, h := range storedHubs(t, st) {
		if h.HubChannelID == hubChannelID {
			return h.ID
		}
	}
	t.Fatalf("no hub stored on %s", hubChannelID)
	return 0
}

// fieldText returns the text under data-field=name inside n, and fails the
// test when the element is absent.
func fieldText(t *testing.T, n *html.Node, name string) string {
	t.Helper()
	el := findElement(n, "", "data-field", name)
	if el == nil {
		t.Errorf("no element under data-field=%q", name)
		return ""
	}
	return textOf(el)
}

func TestHubListShowsEachHubWithLiveSpawnedCount(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.join("user-a", "hub-1")

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	id := strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)
	row := findElement(parseHTML(t, res), "", "data-hub", id)
	if row == nil {
		t.Fatalf("page has no element under data-hub=%q", id)
	}
	for field, want := range map[string]string{
		"spawned":       "1",
		"base_string":   "Arma Voice",
		"channel_name":  "Join to create",
		"category_name": "Arma Reforger",
	} {
		if got := fieldText(t, row, field); got != want {
			t.Errorf("data-field=%s shows %q, want %q", field, got, want)
		}
	}
}

func registerForm(channelID, baseString string) url.Values {
	return url.Values{"hub_channel": {channelID}, "base_string": {baseString}}
}

func TestRegisterWritesRowAndReachesRuntime(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice"))

	assertRedirect(t, res, "/")
	hubs := storedHubs(t, w.st)
	if len(hubs) != 1 {
		t.Fatalf("stored %d hubs, want 1", len(hubs))
	}
	h := hubs[0]
	if h.HubChannelID != "vc-2" || h.BaseString != "Squad Voice" {
		t.Errorf("stored hub = %+v, want vc-2 with base string Squad Voice", h)
	}
	if h.PermissionSource != store.PermissionCategory || h.Bitrate != 64000 || !h.Enabled {
		t.Errorf("stored hub = %+v, want permission source category, bitrate 64000, enabled", h)
	}

	w.join("user-a", "vc-2")
	if n := w.discord.createCount(); n != 1 {
		t.Errorf("a join to the registered channel made %d creates, want 1", n)
	}
}

// errorField returns the data-error value on the page's refusal note, and
// whether one is present.
func errorField(t *testing.T, res *http.Response) (string, bool) {
	t.Helper()
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode {
			if _, ok := attrValue(n, "data-error"); ok {
				found = n
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(parseHTML(t, res))
	if found == nil {
		return "", false
	}
	return attrValue(found, "data-error")
}

func TestRegisterRefusesWithTheFieldNamedAndWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		form  url.Values
		field string
	}{
		{"a text channel", registerForm("text-1", "Chat Voice"), "hub_channel"},
		{"a channel not in the guild", registerForm("vc-elsewhere", "Squad Voice"), "hub_channel"},
		{"a voice channel with no parent", registerForm("vc-noparent", "Lobby Voice"), "hub_channel"},
		{"a channel that is already a hub", registerForm("hub-1", "Arma Voice"), "hub_channel"},
		{"an empty base string", registerForm("vc-2", "   "), "base_string"},
		{"a base string of 91 characters", registerForm("vc-2", strings.Repeat("x", 91)), "base_string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)

			res := w.b.postForm("/hubs", tc.form)

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != tc.field {
				t.Errorf("data-error = %q (present %v), want %q", field, ok, tc.field)
			}
			if hubs := storedHubs(t, w.st); len(hubs) != 1 || hubs[0].HubChannelID != "hub-1" {
				t.Errorf("stored hubs = %+v, want the one pre-stored hub only", hubs)
			}
		})
	}
}

// pickerOptions returns the option values of the hub_channel select.
func pickerOptions(t *testing.T, res *http.Response) []string {
	t.Helper()
	sel := findElement(parseHTML(t, res), "select", "data-field", "hub_channel")
	if sel == nil {
		t.Fatal("page has no select under data-field=hub_channel")
	}
	var values []string
	for c := sel.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "option" {
			continue
		}
		if v, ok := attrValue(c, "value"); ok && v != "" {
			values = append(values, v)
		}
	}
	return values
}

func TestRegisterPickerOffersVoiceChannelsThatAreNotHubs(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.get("/")

	offered := pickerOptions(t, res)
	has := func(id string) bool {
		for _, v := range offered {
			if v == id {
				return true
			}
		}
		return false
	}
	if !has("vc-2") {
		t.Errorf("picker %v does not offer vc-2, a voice channel that is not a hub", offered)
	}
	if has("hub-1") {
		t.Errorf("picker %v offers hub-1, which is a hub", offered)
	}
	if has("text-1") {
		t.Errorf("picker %v offers text-1, a text channel", offered)
	}
}

func TestRegisterWithoutSessionRedirectsToSigninAndWritesNothing(t *testing.T) {
	w := newTestWorld(t)

	res := w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice"))

	assertRedirect(t, res, "/signin")
	if hubs := storedHubs(t, w.st); len(hubs) != 0 {
		t.Errorf("signed-out register stored %+v, want nothing", hubs)
	}
}

func TestHubPageWithGuildReadFailingIsAServerError(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setListErr(errors.New("discord: 503"))

	res := w.b.get("/")

	if !isServerError(res.StatusCode) {
		t.Errorf("GET / status = %d, want 5xx", res.StatusCode)
	}
}

// updateForm is the edit form as posted, every field carrying the value
// testHub stores, so a test changes one field and posts the rest unchanged.
func updateForm() url.Values {
	return url.Values{
		"channel_name":      {"Join to create"},
		"base_string":       {"Arma Voice"},
		"permission_source": {"category"},
		"user_limit":        {"0"},
		"bitrate":           {"64000"},
		"enabled":           {"on"},
	}
}

// hubPath is the update route of the stored hub on a channel.
func hubPath(t *testing.T, st store.Store, hubChannelID string) string {
	t.Helper()
	return "/hubs/" + strconv.FormatInt(storedHubID(t, st, hubChannelID), 10)
}

func TestUpdateWritesTheRowAndReachesTheRuntime(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	form := updateForm()
	form.Set("base_string", "Bravo Voice")
	form.Set("permission_source", "hub_channel")
	form["moderator_roles"] = []string{"role-mp"}
	form.Set("user_limit", "5")
	form.Set("bitrate", "96000")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	assertRedirect(t, res, "/")
	hubs := storedHubs(t, w.st)
	if len(hubs) != 1 {
		t.Fatalf("stored %d hubs, want 1", len(hubs))
	}
	h := hubs[0]
	if h.BaseString != "Bravo Voice" || h.PermissionSource != store.PermissionHubChannel ||
		!slices.Equal(h.ModeratorRoleIDs, []string{"role-mp"}) || h.UserLimit != 5 || h.Bitrate != 96000 || !h.Enabled {
		t.Errorf("stored hub = %+v, want Bravo Voice, hub_channel, [role-mp], limit 5, bitrate 96000, enabled", h)
	}

	w.join("user-a", "hub-1")
	creates := w.discord.creates()
	if len(creates) != 1 {
		t.Fatalf("a join after the update made %d creates, want 1", len(creates))
	}
	if !strings.Contains(creates[0].Name, "Bravo Voice") || creates[0].UserLimit != 5 {
		t.Errorf("create payload = name %q, user limit %d; want the new base string and limit 5", creates[0].Name, creates[0].UserLimit)
	}
}

func TestDisabledHubStopsSpawningAtOnceAndKeepsItsSettings(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	path := hubPath(t, w.st, "hub-1")
	off := updateForm()
	off.Del("enabled")

	assertRedirect(t, w.b.postForm(path, off), "/")
	w.join("user-a", "hub-1")
	if n := w.discord.createCount(); n != 0 {
		t.Errorf("a join to the disabled hub made %d creates, want 0", n)
	}
	if h := storedHubs(t, w.st)[0]; h.Enabled || h.BaseString != "Arma Voice" {
		t.Errorf("stored hub = %+v, want disabled with base string Arma Voice kept", h)
	}

	assertRedirect(t, w.b.postForm(path, updateForm()), "/")
	w.join("user-b", "hub-1")
	if n := w.discord.createCount(); n != 1 {
		t.Errorf("a join to the re-enabled hub made %d creates in all, want 1", n)
	}
}

// storedChangeLog lists the change log entries of a hub, newest first.
func storedChangeLog(t *testing.T, st store.Store, hubID int64) []store.ChangeLogEntry {
	t.Helper()
	entries, err := st.ListChangeLog(context.Background(), hubID, 10)
	if err != nil {
		t.Fatalf("ListChangeLog(%d): %v", hubID, err)
	}
	return entries
}

// sameHubSettings reports whether two hubs carry the same settings, ignoring
// the store-set times.
func sameHubSettings(a, b store.Hub) bool {
	return a.ID == b.ID && a.HubChannelID == b.HubChannelID && a.BaseString == b.BaseString &&
		a.PermissionSource == b.PermissionSource && slices.Equal(a.ModeratorRoleIDs, b.ModeratorRoleIDs) &&
		a.UserLimit == b.UserLimit && a.Bitrate == b.Bitrate && a.Enabled == b.Enabled
}

func TestUpdateRefusesWithTheFieldNamedAndWritesNothing(t *testing.T) {
	set := func(field, value string) url.Values {
		form := updateForm()
		form.Set(field, value)
		return form
	}
	cases := []struct {
		name  string
		form  url.Values
		field string
	}{
		{"an empty channel name", set("channel_name", "   "), "channel_name"},
		{"a channel name of 101 characters", set("channel_name", strings.Repeat("x", 101)), "channel_name"},
		{"an empty base string", set("base_string", "   "), "base_string"},
		{"a base string of 91 characters", set("base_string", strings.Repeat("x", 91)), "base_string"},
		{"a permission source outside the two", set("permission_source", "other"), "permission_source"},
		{"a user limit below 0", set("user_limit", "-1"), "user_limit"},
		{"a user limit above 99", set("user_limit", "100"), "user_limit"},
		{"a user limit that is not a number", set("user_limit", "abc"), "user_limit"},
		{"a bitrate below 8000", set("bitrate", "7999"), "bitrate"},
		{"a bitrate that is not a number", set("bitrate", "abc"), "bitrate"},
		{"a moderator role not in the guild", set("moderator_roles", "role-elsewhere"), "moderator_roles"},
		{"a managed moderator role", set("moderator_roles", "role-bot"), "moderator_roles"},
		{"@everyone as a moderator role", set("moderator_roles", testGuildID), "moderator_roles"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			before := storedHubs(t, w.st)[0]

			res := w.b.postForm(hubPath(t, w.st, "hub-1"), tc.form)

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != tc.field {
				t.Errorf("data-error = %q (present %v), want %q", field, ok, tc.field)
			}
			if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
				t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
			}
			if entries := storedChangeLog(t, w.st, before.ID); len(entries) != 0 {
				t.Errorf("a refused update appended %d change log entries, want 0", len(entries))
			}
			w.join("user-a", "hub-1")
			creates := w.discord.creates()
			if len(creates) != 1 || !strings.Contains(creates[0].Name, "Arma Voice") || creates[0].UserLimit != 0 || creates[0].Bitrate != 64000 {
				t.Errorf("a join after the refusal made creates %+v, want one with the stored settings", creates)
			}
		})
	}
}

func TestUnknownHubIsNotFound(t *testing.T) {
	for _, path := range []string{"/hubs/999", "/hubs/999/remove"} {
		t.Run(path, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)

			res := w.b.postForm(path, updateForm())

			if res.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", res.StatusCode)
			}
			if hubs := storedHubs(t, w.st); len(hubs) != 1 || hubs[0].BaseString != "Arma Voice" {
				t.Errorf("stored hubs = %+v, want the one pre-stored hub unchanged", hubs)
			}
			if entries := storedChangeLog(t, w.st, 0); len(entries) != 0 {
				t.Errorf("appended %d entries under no hub, want 0", len(entries))
			}
		})
	}
}

func TestRemoveDeletesTheRowLeavesTheChannelAndStopsSpawning(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.join("user-a", "hub-1")
	if n := w.discord.createCount(); n != 1 {
		t.Fatalf("the first join made %d creates, want 1", n)
	}

	res := w.b.postForm(hubPath(t, w.st, "hub-1")+"/remove", nil)

	assertRedirect(t, res, "/")
	if hubs := storedHubs(t, w.st); len(hubs) != 0 {
		t.Errorf("stored hubs after remove = %+v, want none", hubs)
	}
	if deleted := w.discord.deletes(); len(deleted) != 0 {
		t.Errorf("remove deleted channels %v, want none: the hub channel and its spawned channels stay", deleted)
	}
	rows, err := w.st.ListSpawnedChannels(context.Background())
	if err != nil {
		t.Fatalf("ListSpawnedChannels: %v", err)
	}
	if len(rows) != 1 || rows[0].ChannelID != "spawn-1" {
		t.Errorf("spawned rows after remove = %+v, want spawn-1 kept", rows)
	}
	w.join("user-b", "hub-1")
	if n := w.discord.createCount(); n != 1 {
		t.Errorf("a join after remove made %d creates in all, want 1: nothing spawns from a removed hub", n)
	}
}

// fieldChange is one field of a diff as a reader decodes it: the values are
// whatever JSON carried, null included.
type fieldChange struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// decodeDiff decodes an entry's diff the way the page does.
func decodeDiff(t *testing.T, e store.ChangeLogEntry) map[string]fieldChange {
	t.Helper()
	var diff map[string]fieldChange
	if err := json.Unmarshal(e.Diff, &diff); err != nil {
		t.Fatalf("decode diff %s: %v", e.Diff, err)
	}
	return diff
}

// wantDiffFields are the seven fields the spec says a register or remove entry
// carries, written out here so the test does not read the list from the code.
var wantDiffFields = []string{"hub_channel", "base_string", "permission_source", "moderator_roles", "user_limit", "bitrate", "enabled"}

// assertActor checks an entry names the signed-in test user.
func assertActor(t *testing.T, e store.ChangeLogEntry, action store.ChangeAction) {
	t.Helper()
	if e.Action != action || e.ForumUserID != testUserID || e.ForumUsername != testUsername {
		t.Errorf("entry = action %q by %d %q, want %q by %d %q", e.Action, e.ForumUserID, e.ForumUsername, action, testUserID, testUsername)
	}
}

func TestUpdateAppendsAnEntryWithTheChangedFieldsOnly(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	form := updateForm()
	form.Set("base_string", "Bravo Voice")

	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

	entries := storedChangeLog(t, w.st, storedHubID(t, w.st, "hub-1"))
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeUpdate)
	diff := decodeDiff(t, entries[0])
	if len(diff) != 1 {
		t.Errorf("diff has keys %v, want base_string alone", slices.Sorted(maps.Keys(diff)))
	}
	if got := diff["base_string"]; got.Before != "Arma Voice" || got.After != "Bravo Voice" {
		t.Errorf("diff base_string = %+v, want before Arma Voice, after Bravo Voice", got)
	}
}

func TestRegisterAppendsAnEntryWithNullBefore(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	assertRedirect(t, w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice")), "/")

	entries := storedChangeLog(t, w.st, storedHubID(t, w.st, "vc-2"))
	if len(entries) != 1 {
		t.Fatalf("the new hub has %d entries, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeRegister)
	diff := decodeDiff(t, entries[0])
	for _, field := range wantDiffFields {
		c, ok := diff[field]
		if !ok {
			t.Errorf("diff lacks %s", field)
			continue
		}
		if c.Before != nil {
			t.Errorf("diff %s before = %v, want null", field, c.Before)
		}
	}
	if diff["base_string"].After != "Squad Voice" || diff["hub_channel"].After != "vc-2" {
		t.Errorf("diff after: base_string %v, hub_channel %v; want Squad Voice and vc-2", diff["base_string"].After, diff["hub_channel"].After)
	}
}

func TestRemoveAppendsAnEntryWithNullAfter(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1")+"/remove", nil), "/")

	entries := storedChangeLog(t, w.st, 0)
	if len(entries) != 1 {
		t.Fatalf("%d entries under no hub, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeRemove)
	diff := decodeDiff(t, entries[0])
	for _, field := range wantDiffFields {
		c, ok := diff[field]
		if !ok {
			t.Errorf("diff lacks %s", field)
			continue
		}
		if c.After != nil {
			t.Errorf("diff %s after = %v, want null", field, c.After)
		}
	}
	if diff["base_string"].Before != "Arma Voice" {
		t.Errorf("diff base_string before = %v, want Arma Voice", diff["base_string"].Before)
	}
}

// editSection returns the edit area of one hub on the page: the section
// under data-hub=id, distinct from the list row that carries the same ID.
func editSection(t *testing.T, res *http.Response, hubID int64) *html.Node {
	t.Helper()
	return hubSection(t, parseHTML(t, res), hubID)
}

// entryIDs returns the data-entry values under n, in document order.
func entryIDs(t *testing.T, n *html.Node) []int64 {
	t.Helper()
	var ids []int64
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if raw, ok := attrValue(n, "data-entry"); ok {
				id, err := strconv.ParseInt(raw, 10, 64)
				if err != nil {
					t.Fatalf("data-entry %q is not an ID", raw)
				}
				ids = append(ids, id)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return ids
}

func TestHubFormShowsTheLastTenEntriesNewestFirst(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	for i := 1; i <= 11; i++ {
		form := updateForm()
		form.Set("user_limit", strconv.Itoa(i))
		assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")
	}

	res := w.b.get("/?hub=" + strconv.FormatInt(id, 10))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
	}
	sec := editSection(t, res, id)
	shown := entryIDs(t, sec)
	var want []int64
	for _, e := range storedChangeLog(t, w.st, id) {
		want = append(want, e.ID)
	}
	if len(want) != 10 || !slices.Equal(shown, want) {
		t.Fatalf("page shows entries %v, want the store's last ten in its order %v", shown, want)
	}
	first := findElement(sec, "", "data-entry", strconv.FormatInt(shown[0], 10))
	if action, _ := attrValue(first, "data-action"); action != "update" {
		t.Errorf("first entry data-action = %q, want update", action)
	}
	if findElement(first, "", "data-change", "user_limit") == nil {
		t.Error("first entry has no data-change=user_limit element")
	}
	if got := fieldText(t, first, "username"); got != testUsername {
		t.Errorf("first entry username = %q, want %q", got, testUsername)
	}
	at := findElement(first, "", "data-field", "at")
	if at == nil {
		t.Fatal("first entry has no data-field=at element")
	}
	if raw, _ := attrValue(at, "datetime"); raw == "" {
		t.Error("first entry's time carries no datetime attribute")
	} else if _, err := time.Parse(time.RFC3339, raw); err != nil {
		t.Errorf("first entry datetime %q is not RFC 3339: %v", raw, err)
	}
}

func TestUpdateAndRemoveWithoutSessionRedirectToSigninAndWriteNothing(t *testing.T) {
	for _, suffix := range []string{"", "/remove"} {
		t.Run("POST /hubs/{id}"+suffix, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			before := storedHubs(t, w.st)[0]
			form := updateForm()
			form.Set("base_string", "Bravo Voice")

			res := w.b.postForm(hubPath(t, w.st, "hub-1")+suffix, form)

			assertRedirect(t, res, "/signin")
			if hubs := storedHubs(t, w.st); len(hubs) != 1 || !sameHubSettings(hubs[0], before) {
				t.Errorf("stored hubs = %+v, want the one hub unchanged", hubs)
			}
			if n := len(storedChangeLog(t, w.st, before.ID)) + len(storedChangeLog(t, w.st, 0)); n != 0 {
				t.Errorf("a signed-out post appended %d entries, want 0", n)
			}
		})
	}
}

// createForm is the create form as posted: a category, the new hub
// channel's name and the base string.
func createForm(categoryID, channelName, baseString string) url.Values {
	return url.Values{"category": {categoryID}, "channel_name": {channelName}, "base_string": {baseString}}
}

func TestCreateMakesAChannelUnderTheCategoryAndWritesARow(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice"))

	assertRedirect(t, res, "/")
	calls := w.discord.createCalls()
	if len(calls) != 1 {
		t.Fatalf("made %d creates, want 1", len(calls))
	}
	data := calls[0].Data
	if data.Type != discordgo.ChannelTypeGuildVoice || data.ParentID != "cat-1" || data.Name != "Squad Join" {
		t.Errorf("create payload = type %d, parent %q, name %q; want voice under cat-1 named Squad Join", data.Type, data.ParentID, data.Name)
	}
	if len(data.PermissionOverwrites) != 0 {
		t.Errorf("create payload carries %d overwrites, want none so the channel takes the category's permissions", len(data.PermissionOverwrites))
	}
	if !strings.Contains(calls[0].Reason, testUsername) {
		t.Errorf("create audit reason %q does not name the panel user %q", calls[0].Reason, testUsername)
	}
	hubs := storedHubs(t, w.st)
	if len(hubs) != 1 {
		t.Fatalf("stored %d hubs, want 1", len(hubs))
	}
	h := hubs[0]
	if h.HubChannelID != "spawn-1" || h.BaseString != "Squad Voice" {
		t.Errorf("stored hub = %+v, want the created channel spawn-1 with base string Squad Voice", h)
	}
	if h.PermissionSource != store.PermissionCategory || h.UserLimit != 0 || h.Bitrate != 64000 || !h.Enabled {
		t.Errorf("stored hub = %+v, want permission source category, user limit 0, bitrate 64000, enabled", h)
	}

	w.join("user-a", "spawn-1")
	if n := w.discord.createCount(); n != 2 {
		t.Errorf("a join to the created hub made %d creates in all, want 2: the hub channel and one spawn", n)
	}
}

func TestCreateAppendsAnEntryWithNullBefore(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)

	assertRedirect(t, w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice")), "/")

	entries := storedChangeLog(t, w.st, storedHubID(t, w.st, "spawn-1"))
	if len(entries) != 1 {
		t.Fatalf("the new hub has %d entries, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeCreate)
	diff := decodeDiff(t, entries[0])
	for _, field := range wantDiffFields {
		c, ok := diff[field]
		if !ok {
			t.Errorf("diff lacks %s", field)
			continue
		}
		if c.Before != nil {
			t.Errorf("diff %s before = %v, want null", field, c.Before)
		}
	}
	if diff["base_string"].After != "Squad Voice" || diff["hub_channel"].After != "spawn-1" {
		t.Errorf("diff after: base_string %v, hub_channel %v; want Squad Voice and spawn-1", diff["base_string"].After, diff["hub_channel"].After)
	}
	if c := diff["channel_name"]; c.Before != nil || c.After != "Squad Join" {
		t.Errorf("diff channel_name = %+v, want before null, after Squad Join", c)
	}
}

func TestCreateRefusesWithTheFieldNamedAndWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		form  url.Values
		field string
	}{
		// An unpicked select posts the key with no value, and the key is
		// what tells a create from a register.
		{"an unpicked category still lands on create", createForm("", "Squad Join", "Squad Voice"), "category"},
		{"a category not in the guild", createForm("cat-elsewhere", "Squad Join", "Squad Voice"), "category"},
		{"a voice channel as the category", createForm("vc-2", "Squad Join", "Squad Voice"), "category"},
		{"an empty channel name", createForm("cat-1", "   ", "Squad Voice"), "channel_name"},
		{"a channel name of 101 characters", createForm("cat-1", strings.Repeat("x", 101), "Squad Voice"), "channel_name"},
		{"an empty base string", createForm("cat-1", "Squad Join", "   "), "base_string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			signIn(t, w.forum, w.b)

			res := w.b.postForm("/hubs", tc.form)

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != tc.field {
				t.Errorf("data-error = %q (present %v), want %q", field, ok, tc.field)
			}
			if n := w.discord.createCount(); n != 0 {
				t.Errorf("a refused create made %d channels, want 0", n)
			}
			if hubs := storedHubs(t, w.st); len(hubs) != 0 {
				t.Errorf("stored hubs = %+v, want none", hubs)
			}
			if entries := storedChangeLog(t, w.st, 0); len(entries) != 0 {
				t.Errorf("a refused create appended %d entries, want 0", len(entries))
			}
		})
	}
}

// failingStore is the store fake with every hub write refused: the
// database gone away between the channel create and the row write.
type failingStore struct {
	store.Store
}

func (failingStore) UpsertHub(context.Context, store.Hub) (store.Hub, error) {
	return store.Hub{}, errors.New("store: connection refused")
}

func TestCreateDeletesTheChannelWhenTheRowWriteFails(t *testing.T) {
	st := store.NewFake()
	w := newTestWorldOver(t, failingStore{st}, newFakeForum(t))
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice"))

	if !isServerError(res.StatusCode) {
		t.Errorf("status = %d, want 5xx", res.StatusCode)
	}
	calls := w.discord.createCalls()
	if len(calls) != 1 {
		t.Fatalf("made %d creates, want 1", len(calls))
	}
	if deleted := w.discord.deletes(); !slices.Contains(deleted, "spawn-1") {
		t.Errorf("deleted channels %v, want the created channel spawn-1 among them", deleted)
	}
	if entries := storedChangeLog(t, st, 0); len(entries) != 0 {
		t.Errorf("a failed create appended %d entries, want 0", len(entries))
	}
}

// restError is a Discord REST error with the given status and no body, the
// shape discordgo returns for a refused call.
func restError(status int) error {
	return &discordgo.RESTError{Response: &http.Response{StatusCode: status}, Message: &discordgo.APIErrorMessage{}}
}

// rateLimitError is the error discordgo returns on a 429 when the call never
// lets it retry, as every panel call does: a *RateLimitError with both
// embedded pointers set, not a *RESTError.
func rateLimitError(retryAfter time.Duration) error {
	return &discordgo.RateLimitError{RateLimit: &discordgo.RateLimit{
		TooManyRequests: &discordgo.TooManyRequests{Message: "You are being rate limited.", RetryAfter: retryAfter},
		URL:             "https://discord.com/api/v9/channels/1549294658339868692",
	}}
}

func TestCreateRefusedByDiscordShowsWhyAndWritesNothing(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)
	w.discord.setCreateErr(restError(http.StatusForbidden))

	res := w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice"))

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	if field, ok := errorField(t, res); !ok || field != "category" {
		t.Errorf("data-error = %q (present %v), want category: the cap and the bot's view are the category's", field, ok)
	}
	if hubs := storedHubs(t, w.st); len(hubs) != 0 {
		t.Errorf("stored hubs = %+v, want none", hubs)
	}
	if entries := storedChangeLog(t, w.st, 0); len(entries) != 0 {
		t.Errorf("a refused create appended %d entries, want 0", len(entries))
	}
}

// inputValue returns the value attribute of the input under data-field=name
// inside n, and fails the test when the input is absent.
func inputValue(t *testing.T, n *html.Node, name string) string {
	t.Helper()
	el := findElement(n, "input", "data-field", name)
	if el == nil {
		t.Errorf("no input under data-field=%q", name)
		return ""
	}
	v, _ := attrValue(el, "value")
	return v
}

func TestChangedHubChannelNameRenamesTheChannelNamingThePanelUser(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	if got := inputValue(t, editSection(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)), id), "channel_name"); got != "Join to create" {
		t.Errorf("the edit form's channel name input holds %q, want the live name Join to create", got)
	}
	form := updateForm()
	form.Set("channel_name", "Join here")
	form.Set("base_string", "Bravo Voice")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	assertRedirect(t, res, "/")
	edits := w.discord.edits()
	if len(edits) != 1 {
		t.Fatalf("made %d edits, want 1", len(edits))
	}
	if edits[0].ChannelID != "hub-1" || edits[0].Name != "Join here" {
		t.Errorf("edit = %+v, want hub-1 renamed to Join here", edits[0])
	}
	if !strings.Contains(edits[0].Reason, testUsername) {
		t.Errorf("edit audit reason %q does not name the panel user %q", edits[0].Reason, testUsername)
	}
	if h := storedHubs(t, w.st)[0]; h.BaseString != "Bravo Voice" {
		t.Errorf("stored base string = %q, want Bravo Voice", h.BaseString)
	}
	entries := storedChangeLog(t, w.st, id)
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	if c := decodeDiff(t, entries[0])["channel_name"]; c.Before != "Join to create" || c.After != "Join here" {
		t.Errorf("diff channel_name = %+v, want before Join to create, after Join here", c)
	}
}

func TestUnchangedHubChannelNameMakesNoEditCall(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	form := updateForm()
	form.Set("base_string", "Bravo Voice")

	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("an unchanged name made edits %+v, want none", edits)
	}
	if h := storedHubs(t, w.st)[0]; h.BaseString != "Bravo Voice" {
		t.Errorf("stored base string = %q, want Bravo Voice", h.BaseString)
	}
}

func TestRefusedHubChannelRenameWritesNothingAndShowsWhy(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setEditErr(restError(http.StatusForbidden))
	before := storedHubs(t, w.st)[0]
	form := updateForm()
	form.Set("channel_name", "Join here")
	form.Set("base_string", "Bravo Voice")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	if field, ok := errorField(t, res); !ok || field != "channel_name" {
		t.Errorf("data-error = %q (present %v), want channel_name", field, ok)
	}
	if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
		t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
	}
	if entries := storedChangeLog(t, w.st, before.ID); len(entries) != 0 {
		t.Errorf("a refused rename appended %d entries, want 0", len(entries))
	}
}

// A 429 on the rename is Discord's limit on channel renames, not a lost
// connection. The note names the wait in whole minutes (#340).
func TestRateLimitedHubChannelRenameShowsTheWait(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setEditErr(rateLimitError(7*time.Minute + 42*time.Second))
	before := storedHubs(t, w.st)[0]
	form := updateForm()
	form.Set("channel_name", "Join here")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	note := findElement(parseHTML(t, res), "", "data-error", "channel_name")
	if note == nil {
		t.Fatal("the page carries no data-error note on channel_name")
	}
	if text := textOf(note); !strings.Contains(text, "8 minutes") {
		t.Errorf("note %q, want the wait rounded up to 8 minutes", text)
	}
	if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
		t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
	}
	if entries := storedChangeLog(t, w.st, before.ID); len(entries) != 0 {
		t.Errorf("a refused rename appended %d entries, want 0", len(entries))
	}
}

func TestBitrateIsBoundedByTheBoostTierReadAtSave(t *testing.T) {
	cases := []struct {
		name    string
		tier    discordgo.PremiumTier
		bitrate string
		refused bool
	}{
		{"tier 0 refuses 96001", discordgo.PremiumTierNone, "96001", true},
		{"tier 1 accepts 128000", discordgo.PremiumTier1, "128000", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			w.discord.setPremiumTier(tc.tier)
			before := storedHubs(t, w.st)[0]
			form := updateForm()
			form.Set("bitrate", tc.bitrate)

			res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

			after := storedHubs(t, w.st)[0]
			if !tc.refused {
				assertRedirect(t, res, "/")
				if after.Bitrate != 128000 {
					t.Errorf("stored bitrate = %d, want 128000", after.Bitrate)
				}
				return
			}
			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != "bitrate" {
				t.Errorf("data-error = %q (present %v), want bitrate", field, ok)
			}
			if !sameHubSettings(after, before) {
				t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
			}
		})
	}
}

// breakChannel edits the fake guild so the hub channel is gone, or has no
// parent.
func (f *fakeDiscord) breakChannel(channelID string, gone bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gone {
		f.channels = slices.DeleteFunc(f.channels, func(ch *discordgo.Channel) bool { return ch.ID == channelID })
		return
	}
	for _, ch := range f.channels {
		if ch.ID == channelID {
			ch.ParentID = ""
		}
	}
}

// secondHub is a stored hub on vc-2, healthy beside a broken hub-1.
func secondHub() store.Hub {
	return store.Hub{
		GuildID: testGuildID, HubChannelID: "vc-2", BaseString: "Squad Voice",
		PermissionSource: store.PermissionCategory, Bitrate: 64000, Enabled: true,
	}
}

// hasField reports whether n holds an element under data-field=name.
func hasField(n *html.Node, name string) bool {
	return findElement(n, "", "data-field", name) != nil
}

func TestBrokenHubOffersRemoveOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		gone bool
	}{
		{"channel gone", true},
		{"channel with no category", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub(), secondHub())
			signIn(t, w.forum, w.b)
			w.discord.breakChannel("hub-1", tc.gone)
			broken := storedHubID(t, w.st, "hub-1")
			healthy := storedHubID(t, w.st, "vc-2")

			res := w.b.get("/")

			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET / status = %d, want 200", res.StatusCode)
			}
			doc := parseHTML(t, res)
			row := findElement(doc, "", "data-hub", strconv.FormatInt(broken, 10))
			if row == nil {
				t.Fatalf("page has no row under data-hub=%d", broken)
			}
			if !hasField(row, "broken") || !hasField(row, "remove") || hasField(row, "edit") {
				t.Errorf("broken row: broken %v, remove %v, edit %v; want broken and remove present, edit absent",
					hasField(row, "broken"), hasField(row, "remove"), hasField(row, "edit"))
			}
			other := findElement(doc, "", "data-hub", strconv.FormatInt(healthy, 10))
			if other == nil {
				t.Fatalf("page has no row under data-hub=%d", healthy)
			}
			if hasField(other, "broken") || hasField(other, "remove") {
				t.Errorf("healthy row: broken %v, remove %v; want neither", hasField(other, "broken"), hasField(other, "remove"))
			}

			sec := editSection(t, w.b.get("/?hub="+strconv.FormatInt(broken, 10)), broken)
			if !hasField(sec, "remove") || hasField(sec, "save") {
				t.Errorf("broken hub's section: remove %v, save %v; want the remove form and no edit form", hasField(sec, "remove"), hasField(sec, "save"))
			}
		})
	}
}

func TestUpdateOfABrokenHubIsRefusedBeforeAnyRename(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.breakChannel("hub-1", false)
	before := storedHubs(t, w.st)[0]
	form := updateForm()
	form.Set("channel_name", "Join here")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), form)

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
		t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
	}
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("an update of a broken hub made edits %+v, want none", edits)
	}
}

func TestHubListShowsTheLastSpawnFailureTheRuntimeHolds(t *testing.T) {
	w := newTestWorld(t, testHub(), secondHub())
	signIn(t, w.forum, w.b)
	w.discord.setCreateErr(restError(http.StatusInternalServerError))
	w.join("user-a", "hub-1")
	failed := storedHubID(t, w.st, "hub-1")
	fine := storedHubID(t, w.st, "vc-2")
	failure, ok := w.runtime.LastSpawnFailure(failed)
	if !ok {
		t.Fatal("the runtime holds no spawn failure for hub-1 after the failed join")
	}

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	doc := parseHTML(t, res)
	row := findElement(doc, "", "data-hub", strconv.FormatInt(failed, 10))
	if row == nil {
		t.Fatalf("page has no row under data-hub=%d", failed)
	}
	if got := fieldText(t, row, "failure_cause"); got != string(failure.Cause) {
		t.Errorf("data-field=failure_cause shows %q, want the runtime's cause %q", got, failure.Cause)
	}
	at := findElement(row, "", "data-field", "failure_at")
	if at == nil {
		t.Fatal("the row has no element under data-field=failure_at")
	}
	if raw, _ := attrValue(at, "datetime"); raw == "" {
		t.Error("the failure time carries no datetime attribute")
	} else if _, err := time.Parse(time.RFC3339, raw); err != nil {
		t.Errorf("failure datetime %q is not RFC 3339: %v", raw, err)
	}
	other := findElement(doc, "", "data-hub", strconv.FormatInt(fine, 10))
	if other == nil {
		t.Fatalf("page has no row under data-hub=%d", fine)
	}
	if hasField(other, "failure_cause") {
		t.Error("a hub with no failure shows a data-field=failure_cause element")
	}
}
