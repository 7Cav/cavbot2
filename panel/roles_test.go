package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// Eligible roles and unavailable moderator roles (#315, ADR 0012): both
// moderator pickers offer eligible roles only, a stored role that is no
// longer eligible renders as unavailable with its reason, and only a person
// removes it. The data-unavailable attribute is the test contract; labels
// and element order are not.

// roleControl is one moderator_roles checkbox as the page renders it.
type roleControl struct {
	ID       string
	Checked  bool
	Disabled bool
	// Unavailable is the data-unavailable value, empty on an eligible role.
	Unavailable string
}

// roleControls returns the moderator_roles checkboxes under n, in document
// order.
func roleControls(n *html.Node) []roleControl {
	var out []roleControl
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "input" {
			typ, _ := attrValue(n, "type")
			name, _ := attrValue(n, "name")
			if typ == "checkbox" && name == "moderator_roles" {
				c := roleControl{}
				c.ID, _ = attrValue(n, "value")
				_, c.Checked = attrValue(n, "checked")
				_, c.Disabled = attrValue(n, "disabled")
				c.Unavailable, _ = attrValue(n, "data-unavailable")
				out = append(out, c)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// pickerIDs returns the IDs a picker's checkboxes carry, in document order.
func pickerIDs(n *html.Node) []string {
	var out []string
	for _, c := range roleControls(n) {
		out = append(out, c.ID)
	}
	return out
}

// hubSection returns the edit area of one hub on a parsed page, the section
// under data-hub=id.
func hubSection(t *testing.T, doc *html.Node, hubID int64) *html.Node {
	t.Helper()
	sec := findElement(doc, "section", "data-hub", strconv.FormatInt(hubID, 10))
	if sec == nil {
		t.Fatalf("page has no section under data-hub=%d", hubID)
	}
	return sec
}

func TestPickersOfferEligibleRolesOnly(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")

	res := w.b.get("/?hub=" + strconv.FormatInt(id, 10))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
	}
	doc := parseHTML(t, res)
	pickers := []struct {
		name string
		node *html.Node
	}{
		{"the guild-wide section", moderatorsSection(t, doc)},
		{"the hub form", hubSection(t, doc, id)},
	}
	for _, p := range pickers {
		offered := pickerIDs(p.node)
		if !slices.Contains(offered, "role-mp") {
			t.Errorf("%s offers %v, want role-mp among them", p.name, offered)
		}
		if slices.Contains(offered, "role-bot") {
			t.Errorf("%s offers %v, want the managed role role-bot left out", p.name, offered)
		}
	}
}

func TestModeratorsSaveRefusesAnIneligibleRoleAndWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		roleID string
	}{
		{"a role not in the guild", "role-gone"},
		{"a managed role", "role-bot"},
		{"@everyone", testGuildID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			assertRedirect(t, w.b.postForm("/moderators", moderatorsForm("role-mp")), "/")

			res := w.b.postForm("/moderators", moderatorsForm("role-mp", tc.roleID))

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			note := findElement(moderatorsSection(t, parseHTML(t, res)), "", "data-error", "moderator_roles")
			if note == nil {
				t.Error("the moderators form carries no data-error=moderator_roles")
			}
			if got := storedGuildRoles(t, w.st); !sameSet(got, []string{"role-mp"}) {
				t.Errorf("stored guild roles = %v, want role-mp alone, unchanged", got)
			}
			if entries := storedChangeLog(t, w.st, 0); len(entries) != 1 {
				t.Errorf("%d entries under no hub, want the first save's alone", len(entries))
			}
		})
	}
}

// The two moderator forms a table case can name.
const (
	onHubForm   = "hub form"
	onGuildWide = "guild-wide section"
)

// newTestWorldModerated builds the panel over a store holding the hubs and
// the guild-wide moderator set, both in before the runtime is built, the
// way startup loads them.
func newTestWorldModerated(t *testing.T, guildWide []string, hubs ...store.Hub) *testWorld {
	t.Helper()
	st := store.NewFake()
	if err := st.SetGuildModeratorRoles(context.Background(), testGuildID, guildWide); err != nil {
		t.Fatalf("SetGuildModeratorRoles: %v", err)
	}
	for _, h := range hubs {
		if _, err := st.UpsertHub(context.Background(), h); err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
	}
	return newTestWorldOver(t, st, newFakeForum(t))
}

// worldStoring builds a world where one form's stored set holds role-mp and
// one more role: the hub on hub-1 for the hub form, the guild-wide set
// otherwise.
func worldStoring(t *testing.T, form, roleID string) *testWorld {
	t.Helper()
	hub := testHub()
	var guildWide []string
	if form == onHubForm {
		hub.ModeratorRoleIDs = []string{"role-mp", roleID}
	} else {
		guildWide = []string{"role-mp", roleID}
	}
	return newTestWorldModerated(t, guildWide, hub)
}

// pickerOn returns the named form's picker area on a parsed page.
func pickerOn(t *testing.T, doc *html.Node, form string, hubID int64) *html.Node {
	t.Helper()
	if form == onHubForm {
		return hubSection(t, doc, hubID)
	}
	return moderatorsSection(t, doc)
}

// controlFor returns the moderator_roles checkbox carrying the ID under n,
// and fails the test when there is none.
func controlFor(t *testing.T, n *html.Node, roleID string) roleControl {
	t.Helper()
	for _, c := range roleControls(n) {
		if c.ID == roleID {
			return c
		}
	}
	t.Fatalf("no moderator_roles checkbox carries %s; the picker has %v", roleID, pickerIDs(n))
	return roleControl{}
}

func TestStoredRoleNoLongerEligibleRendersAsUnavailable(t *testing.T) {
	cases := []struct {
		form   string
		roleID string
		reason string
	}{
		{onHubForm, "role-gone", "deleted"},
		{onHubForm, "role-bot", "managed"},
		{onGuildWide, "role-gone", "deleted"},
		{onGuildWide, "role-bot", "managed"},
	}
	for _, tc := range cases {
		t.Run(tc.reason+" on the "+tc.form, func(t *testing.T) {
			w := worldStoring(t, tc.form, tc.roleID)
			signIn(t, w.forum, w.b)
			id := storedHubID(t, w.st, "hub-1")

			res := w.b.get("/?hub=" + strconv.FormatInt(id, 10))

			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
			}
			c := controlFor(t, pickerOn(t, parseHTML(t, res), tc.form, id), tc.roleID)
			if !c.Checked || c.Disabled || c.Unavailable != tc.reason {
				t.Errorf("control for %s = %+v, want checked, enabled, data-unavailable=%q", tc.roleID, c, tc.reason)
			}
		})
	}
}

// postRoles posts a form's moderator set: the hub on hub-1's edit form with
// every other field as stored, or the guild-wide section's form.
func (w *testWorld) postRoles(t *testing.T, form string, roleIDs ...string) *http.Response {
	t.Helper()
	if form == onHubForm {
		f := updateForm()
		f["moderator_roles"] = roleIDs
		return w.b.postForm(hubPath(t, w.st, "hub-1"), f)
	}
	return w.b.postForm("/moderators", moderatorsForm(roleIDs...))
}

// storedRolesOn reads a form's stored set back through the store.
func storedRolesOn(t *testing.T, w *testWorld, form string) []string {
	t.Helper()
	if form == onHubForm {
		return storedHubs(t, w.st)[0].ModeratorRoleIDs
	}
	return storedGuildRoles(t, w.st)
}

// newestRoleChange decodes the moderator_roles change of a form's newest
// change log entry: the hub's own log, or the log under no hub.
func newestRoleChange(t *testing.T, w *testWorld, form string) fieldChange {
	t.Helper()
	var hubID int64
	if form == onHubForm {
		hubID = storedHubID(t, w.st, "hub-1")
	}
	entries := storedChangeLog(t, w.st, hubID)
	if len(entries) == 0 {
		t.Fatalf("the %s has no change log entry", form)
	}
	c, ok := decodeDiff(t, entries[0])["moderator_roles"]
	if !ok {
		t.Fatalf("the newest %s entry has no moderator_roles change", form)
	}
	return c
}

func TestUnavailableRoleIsKeptOrRemovedOnlyByASave(t *testing.T) {
	// A member can hold a managed role, the booster role for one, and never
	// a deleted one, so the runtime is asked through a rename in the managed
	// rows alone. The untick rows pass since #315's fix to the pickers; the
	// keep rows are what the validator's stored exception turns green.
	cases := []struct {
		form   string
		roleID string
		keep   bool
	}{
		{onHubForm, "role-gone", true},
		{onHubForm, "role-bot", true},
		{onGuildWide, "role-gone", true},
		{onGuildWide, "role-bot", true},
		{onHubForm, "role-gone", false},
		{onHubForm, "role-bot", false},
		{onGuildWide, "role-bot", false},
	}
	for _, tc := range cases {
		action := "unticked"
		if tc.keep {
			action = "kept"
		}
		t.Run(tc.roleID+" "+action+" on the "+tc.form, func(t *testing.T) {
			w := worldStoring(t, tc.form, tc.roleID)
			signIn(t, w.forum, w.b)
			// A sergeant spawns a channel from the hub and owns it; a holder
			// of the stored role sits in it too.
			w.joinAs("user-owner", "hub-1", testRankSGT)
			w.joinAs("user-owner", "spawn-1", testRankSGT)
			w.joinAs("user-mod", "spawn-1", tc.roleID)
			managed := tc.roleID == "role-bot"
			if managed && !tc.keep {
				if _, err := w.runtime.Rename("user-mod", []string{tc.roleID}, "Alpha"); err != nil {
					t.Fatalf("a rename by a holder of the stored role was refused before the save: %v", err)
				}
			}
			posted := []string{"role-mp"}
			if tc.keep {
				posted = append(posted, tc.roleID)
			}

			res := w.postRoles(t, tc.form, posted...)

			if res.StatusCode < 300 || res.StatusCode > 399 {
				t.Fatalf("status = %d, want a redirect", res.StatusCode)
			}
			if got := storedRolesOn(t, w, tc.form); !sameSet(got, posted) {
				t.Errorf("stored roles = %v, want %v", got, posted)
			}
			_, renameErr := w.runtime.Rename("user-mod", []string{tc.roleID}, "Bravo")
			if tc.keep {
				if managed && renameErr != nil {
					t.Errorf("a rename by a holder of the kept role was refused after the save: %v", renameErr)
				}
				return
			}
			c := newestRoleChange(t, w, tc.form)
			if before := roleSet(t, c.Before); !slices.Contains(before, tc.roleID) {
				t.Errorf("newest entry before = %v, want %s among them", before, tc.roleID)
			}
			if after := roleSet(t, c.After); slices.Contains(after, tc.roleID) {
				t.Errorf("newest entry after = %v, want %s gone", after, tc.roleID)
			}
			if managed && renameErr == nil {
				t.Error("a rename by a holder of the removed role passed after the save, want a refusal")
			}
		})
	}
}

func TestARoleStoredOnAnotherRecordIsRefusedForThisOne(t *testing.T) {
	// ADR 0012: the exception is per record. The guild-wide set is shown on
	// every hub form, so a hub save unioning it in is the likeliest slip.
	cases := []struct {
		name  string
		world func(t *testing.T) *testWorld
		// hubChannelID is the hub the post goes to.
		hubChannelID string
		channelName  string
	}{
		{"stored on hub A, posted to hub B", func(t *testing.T) *testWorld {
			hubA := testHub()
			hubA.ModeratorRoleIDs = []string{"role-gone"}
			return newTestWorldModerated(t, nil, hubA, secondHub())
		}, "vc-2", "Squad Join"},
		{"stored guild-wide, posted to a hub", func(t *testing.T) *testWorld {
			return newTestWorldModerated(t, []string{"role-gone"}, testHub())
		}, "hub-1", "Join to create"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world(t)
			signIn(t, w.forum, w.b)
			hubID := storedHubID(t, w.st, tc.hubChannelID)
			form := updateForm()
			form.Set("channel_name", tc.channelName)
			form["moderator_roles"] = []string{"role-mp", "role-gone"}

			res := w.b.postForm("/hubs/"+strconv.FormatInt(hubID, 10), form)

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if field, ok := errorField(t, res); !ok || field != "moderator_roles" {
				t.Errorf("data-error = %q (present %v), want moderator_roles", field, ok)
			}
			for _, h := range storedHubs(t, w.st) {
				if h.ID == hubID && len(h.ModeratorRoleIDs) != 0 {
					t.Errorf("the posted hub stores %v, want none, unchanged", h.ModeratorRoleIDs)
				}
			}
			if entries := storedChangeLog(t, w.st, hubID); len(entries) != 0 {
				t.Errorf("a refused update appended %d change log entries, want 0", len(entries))
			}
		})
	}
}

// unavailableTags returns the data-unavailable value of each data-role tag
// under n, keyed by role ID.
func unavailableTags(n *html.Node) map[string]string {
	out := map[string]string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id, ok := attrValue(n, "data-role"); ok {
				out[id], _ = attrValue(n, "data-unavailable")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func TestHubFormTagsAGuildWideRoleThatIsUnavailable(t *testing.T) {
	w := newTestWorldModerated(t, []string{"role-hq", "role-gone", "role-bot"}, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")

	res := w.b.get("/?hub=" + strconv.FormatInt(id, 10))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /?hub= status = %d, want 200", res.StatusCode)
	}
	guildWide := findElement(hubSection(t, parseHTML(t, res), id), "", "data-field", "guild_moderator_roles")
	if guildWide == nil {
		t.Fatal("the hub form has no element under data-field=guild_moderator_roles")
	}
	tags := unavailableTags(guildWide)
	want := map[string]string{"role-hq": "", "role-gone": "deleted", "role-bot": "managed"}
	for id, reason := range want {
		got, ok := tags[id]
		if !ok {
			t.Errorf("the hub form has no tag for %s; it tags %v", id, tags)
			continue
		}
		if got != reason {
			t.Errorf("tag for %s has data-unavailable=%q, want %q", id, got, reason)
		}
	}
}

func TestChangeLogNamesAManagedRoleAnOlderEntryStored(t *testing.T) {
	w := newTestWorld(t, testHub())
	// An entry from before managed roles left the pickers: it names the
	// bot's own role.
	err := w.st.AppendChangeLog(context.Background(), store.ChangeLogEntry{
		ForumUserID: testUserID, ForumUsername: testUsername, Action: store.ChangeModerators,
		Diff: json.RawMessage(`{"moderator_roles":{"before":[],"after":["role-bot"]}}`),
	})
	if err != nil {
		t.Fatalf("AppendChangeLog: %v", err)
	}
	signIn(t, w.forum, w.b)

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	sec := moderatorsSection(t, parseHTML(t, res))
	change := findElement(sec, "", "data-change", "moderator_roles")
	if change == nil {
		t.Fatal("the section shows no data-change=moderator_roles element")
	}
	if got := fieldText(t, change, "after"); !strings.Contains(got, "CavBot") {
		t.Errorf("entry after = %q, want the managed role named CavBot", got)
	}
}
