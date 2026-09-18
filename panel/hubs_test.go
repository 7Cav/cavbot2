package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

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
	created []discordgo.GuildChannelCreateData
	deleted []string
	spawned int
}

// testGuildRoles are the guild's roles the moderator picker offers.
var testGuildRoles = []*discordgo.Role{
	{ID: "role-mp", Name: "Military Police"},
	{ID: "role-hq", Name: "Regimental HQ"},
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
	}}
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

func (f *fakeDiscord) GuildChannelCreateComplex(_ string, data discordgo.GuildChannelCreateData, _ string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, data)
	f.spawned++
	return &discordgo.Channel{ID: fmt.Sprintf("spawn-%d", f.spawned), Name: data.Name}, nil
}

func (f *fakeDiscord) ChannelDelete(channelID, _ string) (*discordgo.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, channelID)
	return &discordgo.Channel{ID: channelID}, nil
}

func (f *fakeDiscord) ChannelEdit(channelID string, data *discordgo.ChannelEdit, _ string) (*discordgo.Channel, error) {
	return &discordgo.Channel{ID: channelID, Name: data.Name}, nil
}

func (f *fakeDiscord) GuildMemberMove(_, _ string, _ *string) error { return nil }

func (f *fakeDiscord) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error) {
	return &discordgo.Message{ChannelID: channelID, Content: data.Content}, nil
}

func (f *fakeDiscord) GuildMember(_, _ string) (*discordgo.Member, error) { return nil, nil }

func (f *fakeDiscord) Guild(_ string) (*discordgo.Guild, error) {
	return &discordgo.Guild{ID: testGuildID, Roles: testGuildRoles}, nil
}

func (f *fakeDiscord) setListErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listErr = err
}

func (f *fakeDiscord) createCount() int {
	return len(f.creates())
}

// creates returns every create payload the runtime sent, in order.
func (f *fakeDiscord) creates() []discordgo.GuildChannelCreateData {
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
	discord := newFakeDiscord()
	runtime, err := commands.NewTempVC(discord, st, testGuildID)
	if err != nil {
		t.Fatalf("NewTempVC: %v", err)
	}
	p, err := New(testConfig(f), "test", Deps{Store: st, Runtime: runtime, Manager: discord, GuildID: testGuildID})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testWorld{forum: f, st: st, discord: discord, runtime: runtime, p: p, b: newBrowser(t, p)}
}

// testHub is a stored hub on hub-1 with the defaults a register writes.
func testHub() store.Hub {
	return store.Hub{
		GuildID: testGuildID, HubChannelID: "hub-1", BaseString: "Arma Voice",
		PermissionSource: store.PermissionCategory, Bitrate: 64000, Enabled: true,
	}
}

// join feeds the runtime the gateway event for a member joining a channel.
func (w *testWorld) join(userID, channelID string) {
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
		{"an empty base string", set("base_string", "   "), "base_string"},
		{"a base string of 91 characters", set("base_string", strings.Repeat("x", 91)), "base_string"},
		{"a permission source outside the two", set("permission_source", "other"), "permission_source"},
		{"a user limit below 0", set("user_limit", "-1"), "user_limit"},
		{"a user limit above 99", set("user_limit", "100"), "user_limit"},
		{"a user limit that is not a number", set("user_limit", "abc"), "user_limit"},
		{"a bitrate below 8000", set("bitrate", "7999"), "bitrate"},
		{"a bitrate that is not a number", set("bitrate", "abc"), "bitrate"},
		{"a moderator role not in the guild", set("moderator_roles", "role-elsewhere"), "moderator_roles"},
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
