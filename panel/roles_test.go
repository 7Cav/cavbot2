package panel

import (
	"context"
	"net/http"
	"slices"
	"strconv"
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
