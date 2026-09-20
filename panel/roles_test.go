package panel

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

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
