package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// hubChannelChange returns the hub channel row of a change log entry.
func hubChannelChange(t *testing.T, entry *html.Node) *html.Node {
	t.Helper()
	change := findElement(entry, "", "data-change", "hub_channel")
	if change == nil {
		t.Fatal("the entry shows no data-change=hub_channel row")
	}
	return change
}

// newestEntryOnTheForm returns the newest change log entry a hub's edit
// form shows.
func newestEntryOnTheForm(t *testing.T, b *browser, hubID int64) *html.Node {
	t.Helper()
	res := b.get("/?hub=" + strconv.FormatInt(hubID, 10))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
	}
	entries := entryElements(editSection(t, res, hubID))
	if len(entries) == 0 {
		t.Fatal("the hub's form shows no change log entry")
	}
	return entries[0]
}

// #593: a hub made through the Hubs page leaves an entry whose hub channel
// row names the voice channel as the guild names it, not by its Discord ID.
func TestHubFormChangeLogNamesTheHubChannel(t *testing.T) {
	cases := []struct {
		name      string
		form      url.Values
		channelID string
		want      string
	}{
		{"register", registerForm("vc-2", "Squad Voice"), "vc-2", "Squad Join"},
		{"create", createForm("cat-1", "Alpha Join", "Alpha Voice"), "spawn-1", "Alpha Join"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			signIn(t, w.forum, w.b)
			assertRedirect(t, w.b.postForm("/hubs", tc.form), "/")

			entry := newestEntryOnTheForm(t, w.b, storedHubID(t, w.st, tc.channelID))

			if got := fieldText(t, hubChannelChange(t, entry), "after"); got != tc.want {
				t.Errorf("the %s entry's hub channel reads %q, want the channel's name %q", tc.name, got, tc.want)
			}
		})
	}
}

// #593: an entry written before the change log named the hub channel
// stores the channel's ID, as every entry does, and shows the channel's
// name too: the name is looked up when the entry is shown.
func TestHubFormChangeLogNamesTheHubChannelOfAnOlderEntry(t *testing.T) {
	w := newTestWorld(t, secondHub())
	id := storedHubID(t, w.st, "vc-2")
	// A register's entry as the panel writes one: every setting, each with
	// a null before.
	err := w.st.AppendChangeLog(context.Background(), store.ChangeLogEntry{
		HubID: id, ForumUserID: testUserID, ForumUsername: testUsername, Action: store.ChangeRegister,
		Diff: json.RawMessage(`{"hub_channel":{"before":null,"after":"vc-2"},"base_string":{"before":null,"after":"Squad Voice"},` +
			`"permission_source":{"before":null,"after":"category"},"moderator_roles":{"before":null,"after":[]},` +
			`"user_limit":{"before":null,"after":0},"bitrate":{"before":null,"after":64000},` +
			`"delete_delay_minutes":{"before":null,"after":0},"enabled":{"before":null,"after":true},` +
			`"renaming_allowed":{"before":null,"after":false},"locking_allowed":{"before":null,"after":false}}`),
	})
	if err != nil {
		t.Fatalf("AppendChangeLog: %v", err)
	}
	signIn(t, w.forum, w.b)

	entry := newestEntryOnTheForm(t, w.b, id)

	if got := fieldText(t, hubChannelChange(t, entry), "after"); got != "Squad Join" {
		t.Errorf("the older entry's hub channel reads %q, want the channel's name Squad Join", got)
	}
}

// #593: a hub whose channel the guild no longer has, a broken hub, keeps
// its form, and its entry's hub channel row shows the ID it stored, as a
// role row shows a role the guild no longer has.
func TestHubFormChangeLogShowsTheIDOfAChannelTheGuildLost(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice")), "/")
	w.discord.breakChannel("vc-2", true)

	entry := newestEntryOnTheForm(t, w.b, storedHubID(t, w.st, "vc-2"))

	if got := fieldText(t, hubChannelChange(t, entry), "after"); got != "vc-2" {
		t.Errorf("the entry's hub channel reads %q, want the stored ID vc-2", got)
	}
}

// #593: a removed hub's row on the Hubs page opens to its removal, whose
// hub channel row names the channel the hub was on while Discord still has
// it, since a removal leaves the channel in place.
func TestRemovedHubChangeLogNamesTheHubChannel(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	assertRedirect(t, removeHubThroughThePreview(t, w.b, id), "/")

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	entries := entryElements(removedRowOf(t, parseHTML(t, res), id))
	if len(entries) == 0 {
		t.Fatal("the removed hub's row shows no change log entry")
	}
	if got := fieldText(t, hubChannelChange(t, entries[0]), "before"); got != "Join to create" {
		t.Errorf("the removal's hub channel reads %q, want the channel's name Join to create", got)
	}
}
