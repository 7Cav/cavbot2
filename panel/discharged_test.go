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

// With nothing saved, the Rank roles section's Discharged role picker holds
// the forum's Discharged role. The test guild lacks it, so the picker marks
// it deleted and still posts it, and a save of the rank roles keeps it.
func TestRankRolesSectionShowsTheForumsDischargedRoleWithNothingSaved(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	sec := rankRolesSection(t, parseHTML(t, w.b.get(settingsPath)))

	inputs := postedInputs(sec, fieldDischargedRole)
	if len(inputs) != 1 {
		t.Fatalf("the section posts %d Discharged roles, want the forum's one", len(inputs))
	}
	if got, _ := attrValue(inputs[0], "value"); got != testDischargedRole {
		t.Errorf("the section posts Discharged role %q, want %s", got, testDischargedRole)
	}
	if got, _ := attrValue(inputs[0], "data-unavailable"); got != string(unavailableDeleted) {
		t.Errorf("the Discharged role is marked data-unavailable=%q, want %s", got, unavailableDeleted)
	}
}

// A save that clears the Discharged role stops nobody counting as a Cav
// member, so it saves with no preview, even with the member list not loaded,
// and the store holds none.
func TestRankRolesSaveClearingTheDischargedRoleSavesWithNoPreview(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setListStatus(commands.MemberListArriving, true)
	form := loadedForm(t, w.b, settingsPath, "/rank-roles")
	form.Del(fieldDischargedRole)

	res := w.b.postForm("/rank-roles", form)

	assertRedirect(t, res, settingsPath)
	if got := storedRankRoles(t, w.st).DischargedRoleID; got == nil || *got != "" {
		t.Errorf("the stored Discharged role = %v, want none saved", got)
	}
}
