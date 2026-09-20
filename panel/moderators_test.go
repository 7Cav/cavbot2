package panel

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// Guild-wide moderator roles (#296): the section at the top of the hub page
// saves the roles that moderate every hub, change-logs the save under no
// hub, and applies the set to the runtime at once.

// moderatorsForm is the guild-wide section's form as posted.
func moderatorsForm(roleIDs ...string) url.Values {
	return url.Values{"moderator_roles": roleIDs}
}

// storedGuildRoles reads the guild-wide set back through the store.
func storedGuildRoles(t *testing.T, st store.Store) []string {
	t.Helper()
	roles, err := st.GetGuildModeratorRoles(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("GetGuildModeratorRoles: %v", err)
	}
	return roles
}

// sameSet reports whether two role lists hold the same IDs, in any order.
func sameSet(a, b []string) bool {
	a, b = slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))
	return slices.Equal(a, b)
}

// roleSet reads a decoded diff value as a role set: a JSON list of strings,
// or null, which is the empty set.
func roleSet(t *testing.T, v any) []string {
	t.Helper()
	if v == nil {
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("diff value %v is not a list", v)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("diff list item %v is not a string", item)
		}
		out = append(out, s)
	}
	return out
}

func TestModeratorsSaveWritesTheSetAndAppendsAnEntryUnderNoHub(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/moderators", moderatorsForm("role-mp", "role-hq"))

	assertRedirect(t, res, "/")
	if got := storedGuildRoles(t, w.st); !sameSet(got, []string{"role-hq", "role-mp"}) {
		t.Errorf("stored guild roles = %v, want role-hq and role-mp", got)
	}
	entries := storedChangeLog(t, w.st, 0)
	if len(entries) != 1 {
		t.Fatalf("%d entries under no hub, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeModerators)
	diff := decodeDiff(t, entries[0])
	c, ok := diff["moderator_roles"]
	if !ok {
		t.Fatalf("diff %v lacks moderator_roles", diff)
	}
	if before := roleSet(t, c.Before); len(before) != 0 {
		t.Errorf("diff before = %v, want no roles", before)
	}
	if after := roleSet(t, c.After); !sameSet(after, []string{"role-hq", "role-mp"}) {
		t.Errorf("diff after = %v, want role-hq and role-mp", after)
	}
}

func TestModeratorsSecondSaveRecordsTheEarlierSetAsBefore(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	assertRedirect(t, w.b.postForm("/moderators", moderatorsForm("role-mp")), "/")
	assertRedirect(t, w.b.postForm("/moderators", moderatorsForm("role-hq")), "/")

	if got := storedGuildRoles(t, w.st); !sameSet(got, []string{"role-hq"}) {
		t.Errorf("stored guild roles = %v, want role-hq alone", got)
	}
	entries := storedChangeLog(t, w.st, 0)
	if len(entries) != 2 {
		t.Fatalf("%d entries under no hub, want 2", len(entries))
	}
	c := decodeDiff(t, entries[0])["moderator_roles"]
	if before := roleSet(t, c.Before); !sameSet(before, []string{"role-mp"}) {
		t.Errorf("newest entry before = %v, want role-mp", before)
	}
	if after := roleSet(t, c.After); !sameSet(after, []string{"role-hq"}) {
		t.Errorf("newest entry after = %v, want role-hq", after)
	}
}

// moderatorsSection returns the guild-wide section of a parsed page, the
// element under data-section=moderators.
func moderatorsSection(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	sec := findElement(doc, "section", "data-section", "moderators")
	if sec == nil {
		t.Fatal("page has no section under data-section=moderators")
	}
	return sec
}

// checkedBoxes returns the values of the checked checkboxes named name
// under n, in document order.
func checkedBoxes(n *html.Node, name string) []string {
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "input" {
			typ, _ := attrValue(n, "type")
			got, _ := attrValue(n, "name")
			if typ == "checkbox" && got == name {
				if _, checked := attrValue(n, "checked"); checked {
					value, _ := attrValue(n, "value")
					out = append(out, value)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// dataRoles returns the data-role values under n, in document order.
func dataRoles(n *html.Node) []string {
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id, ok := attrValue(n, "data-role"); ok {
				out = append(out, id)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func TestHubFormListsTheGuildWideRolesApartFromItsOwn(t *testing.T) {
	hub := testHub()
	hub.ModeratorRoleIDs = []string{"role-mp"}
	w := newTestWorld(t, hub)
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/moderators", moderatorsForm("role-hq")), "/")
	id := storedHubID(t, w.st, "hub-1")

	res := w.b.get("/?hub=" + strconv.FormatInt(id, 10))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if got := checkedBoxes(moderatorsSection(t, doc), "moderator_roles"); !slices.Equal(got, []string{"role-hq"}) {
		t.Errorf("guild-wide section has %v checked, want role-hq alone", got)
	}
	sec := findElement(doc, "section", "data-hub", strconv.FormatInt(id, 10))
	if sec == nil {
		t.Fatalf("page has no section under data-hub=%d", id)
	}
	guildWide := findElement(sec, "", "data-field", "guild_moderator_roles")
	if guildWide == nil {
		t.Fatal("the hub form has no element under data-field=guild_moderator_roles")
	}
	if got := dataRoles(guildWide); !slices.Equal(got, []string{"role-hq"}) {
		t.Errorf("the hub form lists guild-wide roles %v, want role-hq alone", got)
	}
	if got := checkedBoxes(sec, "moderator_roles"); !slices.Equal(got, []string{"role-mp"}) {
		t.Errorf("the hub's own picker has %v checked, want role-mp alone", got)
	}
}

func TestModeratorsSectionShowsItsOwnLastTenEntriesNewestFirst(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	// Saves alternate between the two roles, so every save changes the set
	// and the tenth is Regimental HQ, the eleventh Military Police.
	for i := 1; i <= 11; i++ {
		role := "role-hq"
		if i%2 == 1 {
			role = "role-mp"
		}
		assertRedirect(t, w.b.postForm("/moderators", moderatorsForm(role)), "/")
	}
	// A remove lands under no hub too, newest of all, and must not show here.
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1")+"/remove", nil), "/")

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	sec := moderatorsSection(t, parseHTML(t, res))
	shown := entryIDs(t, sec)
	if len(shown) != 10 {
		t.Fatalf("the section shows %d entries, want 10", len(shown))
	}
	first := findElement(sec, "", "data-entry", strconv.FormatInt(shown[0], 10))
	change := findElement(first, "", "data-change", "moderator_roles")
	if change == nil {
		t.Fatal("the first entry has no data-change=moderator_roles element")
	}
	if got := fieldText(t, change, "before"); got != "Regimental HQ" {
		t.Errorf("first entry before = %q, want Regimental HQ (the tenth save)", got)
	}
	if got := fieldText(t, change, "after"); got != "Military Police" {
		t.Errorf("first entry after = %q, want Military Police (the eleventh save)", got)
	}
}

// testRankSGT is a real rank role ID from the ladder in code, so a member
// holding it owns the channel they spawn.
const testRankSGT = "899328273752928318"

// joinAs feeds the runtime the gateway event for a member with roles joining
// a channel.
func (w *testWorld) joinAs(userID, channelID string, roles ...string) {
	w.runtime.HandleVoiceStateUpdate(&discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: testGuildID, UserID: userID, ChannelID: channelID, Member: &discordgo.Member{Roles: roles},
	}})
}

func TestModeratorsSaveReachesTheRuntimeAtOnce(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	// A sergeant joins the hub, gets the spawned channel and owns it; a
	// holder of Regimental HQ, which no hub names, sits in it too.
	w.joinAs("user-owner", "hub-1", testRankSGT)
	w.joinAs("user-owner", "spawn-1", testRankSGT)
	w.joinAs("user-mod", "spawn-1", "role-hq")

	if _, err := w.runtime.Rename("user-mod", []string{"role-hq"}, "Alpha"); err == nil {
		t.Fatal("a rename by a holder of an unsaved role passed, want a refusal")
	}
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Fatalf("edits before the save = %+v, want none", edits)
	}

	assertRedirect(t, w.b.postForm("/moderators", moderatorsForm("role-hq")), "/")

	if _, err := w.runtime.Rename("user-mod", []string{"role-hq"}, "Alpha"); err != nil {
		t.Fatalf("a rename by a holder of the saved role was refused: %v", err)
	}
	edits := w.discord.edits()
	if len(edits) != 1 || edits[0].ChannelID != "spawn-1" {
		t.Errorf("edits after the save = %+v, want one on spawn-1", edits)
	}
	if owner, tracked := w.runtime.Owner("spawn-1"); !tracked || owner != "user-owner" {
		t.Errorf("Owner(spawn-1) = %q, %v; want user-owner, still tracked", owner, tracked)
	}
}
