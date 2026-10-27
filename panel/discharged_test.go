package panel

import (
	"slices"
	"testing"

	"github.com/7cav/cavbot2/commands"
)

// The Discharged role (#893, GLOSSARY.md): a member holding it is no Cav
// member, whatever rank role they hold.

// testDischargedRole is the forum's Discharged role, the one a guild has
// until a panel admin saves the Discharged role.
const testDischargedRole = "437749895785480193"

// dischargedDoe is the fixture's Doe, who holds Internal and SGT's rank
// role, with the Discharged role too.
func dischargedDoe() commands.ListedMember {
	doe := memberDoe
	doe.RoleIDs = append(slices.Clone(doe.RoleIDs), testDischargedRole)
	return doe
}

// An Internal holder who holds the Discharged role is flagged "discharged",
// not "no rank role", and the Rank role column still shows their rank. The
// Flagged filter lists them.
func TestDischargedInternalHolderIsFlaggedDischarged(t *testing.T) {
	w := newFoxholeWorld(t)
	setMembers(w, dischargedDoe(), memberAsh, memberKestrel, memberMarsh, memberVance)

	doc := parseHTML(t, w.b.get(foxholePath))

	row := holderRows(t, doc)[memberDoe.ID]
	if row == nil {
		t.Fatalf("the holder list has no row for %s", memberDoe.ID)
	}
	if got := flagsOf(row); !slices.Equal(got, []string{"discharged"}) {
		t.Errorf("Doe's flags = %v, want [discharged]", got)
	}
	if got := fieldText(t, row, "rank_role"); got != "Sergeant" {
		t.Errorf("Doe's rank role = %q, want Sergeant, the rank they still hold", got)
	}
	link := findElement(doc, "a", "data-filter", "flagged")
	if link == nil {
		t.Fatal("the page has no Flagged filter link")
	}
	href, _ := attrValue(link, "href")
	if flagged := holderRows(t, parseHTML(t, follow(t, w.b, w.b.get(href)))); flagged[memberDoe.ID] == nil {
		t.Errorf("the Flagged filter lists %v, want Doe among them", keys(flagged))
	}
}

// An add preview of Internal gives a discharged member the holder list's
// "discharged" flag, as the list will show them once the add gives them
// Internal. Vance holds SGT's rank role and no Foxhole role.
func TestAddPreviewOfInternalFlagsADischargedMember(t *testing.T) {
	w := newFoxholeWorld(t)
	vance := memberVance
	vance.RoleIDs = append(slices.Clone(vance.RoleIDs), testDischargedRole)
	setMembers(w, memberDoe, memberAsh, memberKestrel, memberMarsh, vance)

	row := pastedRow(t, pastePreview(t, w, "internal", memberVance.Username), 1)

	if got := flagsOf(row); !slices.Equal(got, []string{"discharged"}) {
		t.Errorf("the add preview of Internal flags %s %v, want [discharged]", memberVance.Username, got)
	}
}
