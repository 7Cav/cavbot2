package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// removedHubsBlock returns the Removed hubs block of a Hubs page, or nil
// when the page has none.
func removedHubsBlock(doc *html.Node) *html.Node {
	return findElement(doc, "", "data-section", "removed-hubs")
}

// removedRowOf returns the Removed hubs block's row of a removed hub.
func removedRowOf(t *testing.T, doc *html.Node, hubID int64) *html.Node {
	t.Helper()
	block := removedHubsBlock(doc)
	if block == nil {
		t.Fatal("the page has no Removed hubs block")
	}
	row := findElement(block, "", "data-removed-hub", strconv.FormatInt(hubID, 10))
	if row == nil {
		t.Fatalf("the Removed hubs block has no row for hub %d", hubID)
	}
	return row
}

// removedHubFacts returns a removed hub's row line: the facts the row shows
// before it is opened, apart from the change log it opens to.
func removedHubFacts(t *testing.T, row *html.Node) *html.Node {
	t.Helper()
	facts := findElement(row, "summary", "", "")
	if facts == nil {
		t.Fatal("the removed hub's row has no line of its own")
	}
	return facts
}

// actionsOf returns the data-action of each change log entry under n, in
// document order.
func actionsOf(n *html.Node) []string {
	var actions []string
	for _, entry := range entryElements(n) {
		action, _ := attrValue(entry, "data-action")
		actions = append(actions, action)
	}
	return actions
}

// everySetting is every setting of a hub, as a removal's entry carries each
// with its value at the removal. Written out so the test does not read the
// list from the code.
var everySetting = []string{"hub_channel", "base_string", "permission_source", "moderator_roles", "user_limit", "bitrate",
	"delete_delay_minutes", "enabled", "renaming_allowed", "locking_allowed"}

// changedFields returns the data-change of each field an entry shows, in
// document order.
func changedFields(entry *html.Node) []string {
	var fields []string
	eachLiveElement(entry, func(n *html.Node) {
		if field, ok := attrValue(n, "data-change"); ok {
			fields = append(fields, field)
		}
	})
	return fields
}

// #579: a removed hub's row on the Hubs page names the hub as it was at
// its removal, who removed it and when, and opens to the hub's change log,
// the removal first with every setting it had, then the save before it.
func TestRemovedHubsBlockListsARemovedHubWithItsChangeLog(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/hubs", registerForm("hub-1", "Arma Voice")), "/")
	form := updateForm(t, w.st)
	form.Set("user_limit", "5")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")
	id := storedHubID(t, w.st, "hub-1")
	removing := time.Now().Truncate(time.Second)
	assertRedirect(t, removeHubThroughThePreview(t, w.b, id), "/")
	removed := time.Now()

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	row := removedRowOf(t, parseHTML(t, res), id)
	facts := removedHubFacts(t, row)
	if got := fieldText(t, facts, "base_string"); got != "Arma Voice" {
		t.Errorf("the row names the hub %q, want Arma Voice", got)
	}
	if got := fieldText(t, facts, "removed_by"); got != testUsername {
		t.Errorf("the row says %q removed the hub, want %q", got, testUsername)
	}
	if at := findElement(facts, "", "data-field", "removed_at"); at == nil {
		t.Error("the row says nothing of when the hub was removed")
	} else if when, err := time.Parse(time.RFC3339, attrOf(at, "datetime")); err != nil {
		t.Errorf("the removal's datetime %q is not RFC 3339: %v", attrOf(at, "datetime"), err)
	} else if when.Before(removing) || when.After(removed) {
		t.Errorf("the row says the hub was removed at %v, want between %v and %v", when, removing, removed)
	}
	entries := entryElements(row)
	if got := actionsOf(row); !slices.Equal(got, []string{"remove", "update", "register"}) {
		t.Fatalf("the row opens to entries %v, want [remove update register]: the removal, then the hub's earlier saves", got)
	}
	removal := entries[0]
	if got := changedFields(removal); !sameSet(got, everySetting) {
		t.Errorf("the removal shows the settings %v, want every setting %v", got, everySetting)
	}
	for field, want := range map[string]string{"base_string": "Arma Voice", "user_limit": "5"} {
		change := findElement(removal, "", "data-change", field)
		if change == nil {
			continue
		}
		if got := fieldText(t, change, "before"); got != want {
			t.Errorf("the removal shows %s was %q, want %q", field, got, want)
		}
	}
}

// #579: a Hubs page with no removed hub to list has no Removed hubs block.
func TestHubsPageWithNoRemovedHubsHasNoRemovedHubsBlock(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	if removedHubsBlock(parseHTML(t, res)) != nil {
		t.Error("the page has a Removed hubs block, want none with no hub removed")
	}
}

// #579: a removed hub's row names its voice channel while the channel is
// in Discord, and says the channel is not in the server once it's gone.
func TestRemovedHubRowNamesItsChannelWhileItIsInDiscord(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	assertRedirect(t, removeHubThroughThePreview(t, w.b, id), "/")

	facts := removedHubFacts(t, removedRowOf(t, parseHTML(t, w.b.get("/")), id))

	if got := fieldText(t, facts, "channel_name"); got != "Join to create" {
		t.Errorf("the row names the channel %q, want Join to create", got)
	}
	if hasField(facts, "channel_gone") {
		t.Error("the row says the channel is gone while it is in Discord")
	}

	w.discord.breakChannel("hub-1", true)
	facts = removedHubFacts(t, removedRowOf(t, parseHTML(t, w.b.get("/")), id))

	if !hasField(facts, "channel_gone") {
		t.Error("the row does not say the channel is gone once it is")
	}
	if hasField(facts, "channel_name") {
		t.Errorf("the row names the channel %q once it is gone, want no name", fieldText(t, facts, "channel_name"))
	}
}

// #579: the Removed hubs block lists the ten newest removals, newest
// first, so after eleven the first removal's row is gone. The base strings
// sort apart from the removal order, "Hub 10" before "Hub 2".
func TestRemovedHubsBlockListsTheTenNewestRemovalsNewestFirst(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)
	var removed []string
	for i := 1; i <= 11; i++ {
		assertRedirect(t, w.b.postForm("/hubs", registerForm("hub-1", "Hub "+strconv.Itoa(i))), "/")
		id := storedHubID(t, w.st, "hub-1")
		assertRedirect(t, removeHubThroughThePreview(t, w.b, id), "/")
		removed = append(removed, strconv.FormatInt(id, 10))
	}

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	block := removedHubsBlock(parseHTML(t, res))
	if block == nil {
		t.Fatal("the page has no Removed hubs block")
	}
	want := slices.Clone(removed[1:])
	slices.Reverse(want)
	if got := valuesOf(block, "data-removed-hub"); !slices.Equal(got, want) {
		t.Errorf("the block lists removed hubs %v, want %v: the ten newest removals, newest first, and not the first, hub %s", got, want, removed[0])
	}
}

// #579: registering a removed hub's channel again makes a new hub whose
// change log starts at its register, and the removed hub keeps its row and
// its own entries. Neither shows the other's.
func TestRegisteringARemovedHubsChannelAgainStartsANewChangeLog(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/hubs", registerForm("hub-1", "Arma Voice")), "/")
	form := updateForm(t, w.st)
	form.Set("user_limit", "5")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")
	removed := storedHubID(t, w.st, "hub-1")
	assertRedirect(t, removeHubThroughThePreview(t, w.b, removed), "/")

	assertRedirect(t, w.b.postForm("/hubs", registerForm("hub-1", "Arma Voice")), "/")

	again := storedHubID(t, w.st, "hub-1")
	res := w.b.get("/?hub=" + strconv.FormatInt(again, 10))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if got := actionsOf(hubSection(t, doc, again)); !slices.Equal(got, []string{"register"}) {
		t.Errorf("the new hub's form lists entries %v, want its register alone", got)
	}
	if got := actionsOf(removedRowOf(t, doc, removed)); !slices.Equal(got, []string{"remove", "update", "register"}) {
		t.Errorf("the removed hub's row lists entries %v, want its own: [remove update register]", got)
	}
}

// #579: a removal written before #579 references no hub, so it makes no
// row, and the page loads as it would with no removal at all.
func TestRemovalWithNoHubReferenceMakesNoRow(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	if err := w.st.AppendChangeLog(context.Background(), store.ChangeLogEntry{
		ForumUserID: 1234, ForumUsername: "Doe.J", Action: store.ChangeRemove,
		Diff: json.RawMessage(`{"hub_channel": {"before": "vc-2", "after": null}, "base_string": {"before": "Squad Voice", "after": null}}`),
	}); err != nil {
		t.Fatalf("AppendChangeLog: %v", err)
	}

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	if block := removedHubsBlock(parseHTML(t, res)); block != nil {
		t.Errorf("the page lists removed hubs %v, want no Removed hubs block", valuesOf(block, "data-removed-hub"))
	}
}
