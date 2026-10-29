package panel

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
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

// withDischargeableSergeant is the test world with an SGT who also holds
// role-hq, and a PVT, in the member list. The SGT owns spawn-1, which their
// hub join spawned and where the PVT sits with them.
func withDischargeableSergeant(t *testing.T) *testWorld {
	t.Helper()
	w := newTestWorld(t, testHub())
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{testBotMember,
			{ID: "user-sgt", Username: "sgt", RoleIDs: []string{testRankSGT, "role-hq"}},
			{ID: "user-pvt", Username: "pvt", RoleIDs: []string{testRankPVT}}}})
	w.joinAs("user-sgt", "hub-1", testRankSGT, "role-hq")
	w.joinAs("user-sgt", "spawn-1", testRankSGT, "role-hq")
	w.joinAs("user-pvt", "spawn-1", testRankPVT)
	signIn(t, w.forum, w.b)
	return w
}

// A save that changes the Discharged role to role-hq stops the SGT who
// holds it counting as a Cav member, so it shows a preview counting one
// member and the one channel they own, and changes nothing. Its Confirm
// saves the role, the PVT takes the SGT's channel, and the section's change
// log lists the save with the Discharged role's change.
func TestRankRolesSaveChangingTheDischargedRoleWaitsOnItsPreview(t *testing.T) {
	w := withDischargeableSergeant(t)
	before := readSavedState(t, w.st)
	form := loadedForm(t, w.b, settingsPath, "/rank-roles")
	form.Set(fieldDischargedRole, "role-hq")

	res := follow(t, w.b, w.b.postForm("/rank-roles", form))

	preview := previewIn(t, parseHTML(t, res))
	if preview == nil {
		t.Fatalf("status %d with no preview in the Rank roles section, want the preview", res.StatusCode)
	}
	if dropped, channels := previewCounts(t, preview); dropped != "1" || channels != "1" {
		t.Errorf("preview counts %s members and %s channels, want 1 and 1", dropped, channels)
	}
	if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the preview = %+v, want it as before, %+v", after, before)
	}
	if owner, _ := w.runtime.Owner("spawn-1"); owner != "user-sgt" {
		t.Errorf("Owner(spawn-1) after the preview = %q, want user-sgt still", owner)
	}

	assertRedirect(t, w.b.postForm("/rank-roles", formPosts(t, preview, "/rank-roles")), settingsPath)

	if got := storedRankRoles(t, w.st).DischargedRoleID; got == nil || *got != "role-hq" {
		t.Errorf("the stored Discharged role after Confirm = %v, want role-hq", got)
	}
	if owner, _ := w.runtime.Owner("spawn-1"); owner != "user-pvt" {
		t.Errorf("Owner(spawn-1) after Confirm = %q, want user-pvt", owner)
	}
	entries, err := w.st.ListRankRoleChanges(context.Background(), 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListRankRoleChanges = %d entries, %v, want the Confirm's one", len(entries), err)
	}
	entry := findElement(rankRolesSection(t, parseHTML(t, w.b.get(settingsPath))), "", "data-entry", strconv.FormatInt(entries[0].ID, 10))
	if entry == nil {
		t.Fatal("the Rank roles section does not list the save's entry")
	}
	if got := valuesOf(entry, "data-change"); !slices.Equal(got, []string{fieldDischargedRole}) {
		t.Errorf("the listed entry shows changes %v, want the Discharged role's alone", got)
	}
}

// A save that names as the Discharged role a role the same save gives a
// rank is refused, with the refusal on the Discharged role saying that
// role is a rank role, and saves nothing.
func TestRankRolesSaveRefusesARankRoleAsTheDischargedRole(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	before := readSavedState(t, w.st)
	form := loadedForm(t, w.b, settingsPath, "/rank-roles")
	form.Set(rankRoleField("SGT"), "role-hq")
	form.Set(fieldDischargedRole, "role-hq")

	res := w.b.postForm("/rank-roles", form)

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	refusal := findElement(rankRolesSection(t, parseHTML(t, res)), "", "data-error", fieldDischargedRole)
	if refusal == nil {
		t.Fatal("the Rank roles section carries no refusal on the Discharged role")
	}
	if got := textOf(refusal); !strings.Contains(got, rankRoleAsDischargedRefusal) {
		t.Errorf("the refusal says %q, want it to carry %q", got, rankRoleAsDischargedRefusal)
	}
	if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
	}
}

// With the member list not loaded, a save that sets the Discharged role
// previews with the member count unknown, since nothing can rule a drop
// out.
func TestRankRolesSaveSettingTheDischargedRoleBeforeTheListLoadsPreviews(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setListStatus(commands.MemberListArriving, true)
	form := loadedForm(t, w.b, settingsPath, "/rank-roles")
	form.Set(fieldDischargedRole, "role-hq")

	res := follow(t, w.b, w.b.postForm("/rank-roles", form))

	preview := previewIn(t, parseHTML(t, res))
	if preview == nil {
		t.Fatalf("status %d with no preview, want the preview while the member list loads", res.StatusCode)
	}
	if dropped, _ := previewCounts(t, preview); dropped != "unknown" {
		t.Errorf("preview's member count = %q, want unknown", dropped)
	}
}

// A save that changes a rank keeps the stored Discharged role though the
// guild lacks it, as it keeps a rank's unavailable role (ADR 0012). The
// test guild lacks the forum's Discharged role, stored while nothing is
// saved. A save naming a role an integration manages as the Discharged
// role is refused and saves nothing.
func TestRankRolesSaveKeepsAnUnavailableDischargedRoleAndRefusesANewOne(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	form := loadedForm(t, w.b, settingsPath, "/rank-roles")
	form.Set(rankRoleField("SGT"), "role-hq")

	assertRedirect(t, w.b.postForm("/rank-roles", form), settingsPath)

	if got := storedRankRoles(t, w.st).DischargedRoleID; got == nil || *got != testDischargedRole {
		t.Errorf("the stored Discharged role after a save of SGT = %v, want %s, kept though the guild lacks it", got, testDischargedRole)
	}

	before := readSavedState(t, w.st)
	form = loadedForm(t, w.b, settingsPath, "/rank-roles")
	form.Set(fieldDischargedRole, "role-bot")

	res := w.b.postForm("/rank-roles", form)

	if !isClientError(res.StatusCode) {
		t.Errorf("status of a save naming a managed role = %d, want 4xx", res.StatusCode)
	}
	if findElement(rankRolesSection(t, parseHTML(t, res)), "", "data-error", fieldDischargedRole) == nil {
		t.Error("the Rank roles section carries no refusal on the Discharged role")
	}
	if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
	}
}

// dischargedHoldersOf reads the holder count the Rank roles section's
// Discharged role field carries in data-holders, and whether it carries
// one.
func dischargedHoldersOf(t *testing.T, doc *html.Node) (string, bool) {
	t.Helper()
	field := findElement(rankRolesSection(t, doc), "", "aria-labelledby", "discharged-role-label")
	if field == nil {
		t.Fatal("the Rank roles section has no Discharged role field")
	}
	var holders string
	found := false
	eachElement(field, func(n *html.Node) {
		if v, ok := attrValue(n, "data-holders"); ok {
			holders, found = v, true
		}
	})
	return holders, found
}

// saveDischargedRole saves the Discharged role as id, none when empty,
// through the Rank roles form, confirming its preview when it shows one.
func saveDischargedRole(t *testing.T, w *testWorld, id string) {
	t.Helper()
	form := loadedForm(t, w.b, settingsPath, "/rank-roles")
	form.Set(fieldDischargedRole, id)
	if id == "" {
		form.Del(fieldDischargedRole)
	}
	res := follow(t, w.b, w.b.postForm("/rank-roles", form))
	if preview := previewIn(t, parseHTML(t, res)); preview != nil {
		res = follow(t, w.b, w.b.postForm("/rank-roles", formPosts(t, preview, "/rank-roles")))
	}
	if got := storedRankRoles(t, w.st).DischargedRoleID; got == nil || *got != id {
		t.Fatalf("the stored Discharged role after the save = %v, want %q", got, id)
	}
}

// The Discharged role field counts every member holding the saved
// Discharged role, from the member list, whether or not they hold a rank
// role (#920). A saved role nobody holds counts 0.
func TestRankRolesSectionCountsTheDischargedRolesHolders(t *testing.T) {
	w := newTestWorld(t, testHub())
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{testBotMember,
			{ID: "user-1", Username: "one", RoleIDs: []string{testRankSGT, "role-mp"}},
			{ID: "user-2", Username: "two", RoleIDs: []string{"role-mp"}}}})
	signIn(t, w.forum, w.b)

	saveDischargedRole(t, w, "role-mp")

	if got, _ := dischargedHoldersOf(t, parseHTML(t, w.b.get(settingsPath))); got != "2" {
		t.Errorf("the Discharged role's holders = %q, want 2", got)
	}

	saveDischargedRole(t, w, "role-hq")

	if got, _ := dischargedHoldersOf(t, parseHTML(t, w.b.get(settingsPath))); got != "0" {
		t.Errorf("the Discharged role's holders with nobody holding it = %q, want 0", got)
	}
}

// With nothing saved, the Discharged role field counts the members holding
// the forum's Discharged role, though the test guild lacks it and the
// picker marks it deleted (ADR 0012).
func TestRankRolesSectionCountsTheForumsDischargedRolesHoldersWithNothingSaved(t *testing.T) {
	w := newTestWorld(t, testHub())
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{testBotMember,
			{ID: "user-1", Username: "one", RoleIDs: []string{testDischargedRole}},
			{ID: "user-2", Username: "two", RoleIDs: []string{testRankSGT}}}})
	signIn(t, w.forum, w.b)

	if got, _ := dischargedHoldersOf(t, parseHTML(t, w.b.get(settingsPath))); got != "1" {
		t.Errorf("the forum's Discharged role's holders = %q, want 1", got)
	}
}

// With the Discharged role saved as none, its field shows no count.
func TestRankRolesSectionShowsNoDischargedCountWithTheRoleSavedAsNone(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	saveDischargedRole(t, w, "")

	if got, ok := dischargedHoldersOf(t, parseHTML(t, w.b.get(settingsPath))); ok {
		t.Errorf("the Discharged role saved as none shows holders %q, want no count", got)
	}
}

// While the member list has not loaded, the Discharged role's count is
// unknown, never 0.
func TestRankRolesSectionDischargedCountIsUnknownWhileTheListLoads(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setListStatus(commands.MemberListArriving, true)

	if got, _ := dischargedHoldersOf(t, parseHTML(t, w.b.get(settingsPath))); got != holdersUnknown {
		t.Errorf("the Discharged role's holders while the member list loads = %q, want %s", got, holdersUnknown)
	}
}

// The Discharged role's count is the saved role's. When a preview or a
// refusal shows the form as posted, with another role in the Discharged
// role's picker, the count stays the saved role's. Saved role-hq has one
// holder and the posted role-mp two.
func TestRankRolesSectionCountsTheSavedDischargedRoleWhenTheFormShowsAnother(t *testing.T) {
	w := newTestWorld(t, testHub())
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{testBotMember,
			{ID: "user-1", Username: "one", RoleIDs: []string{testRankSGT, "role-mp"}},
			{ID: "user-2", Username: "two", RoleIDs: []string{"role-mp"}},
			{ID: "user-3", Username: "three", RoleIDs: []string{testRankPVT, "role-hq"}}}})
	signIn(t, w.forum, w.b)
	saveDischargedRole(t, w, "role-hq")
	form := loadedForm(t, w.b, settingsPath, "/rank-roles")
	form.Set(fieldDischargedRole, "role-mp")

	doc := parseHTML(t, follow(t, w.b, w.b.postForm("/rank-roles", form)))

	if previewIn(t, doc) == nil {
		t.Fatal("the save shows no preview")
	}
	if got, _ := dischargedHoldersOf(t, doc); got != "1" {
		t.Errorf("the Discharged role's holders beside the preview = %q, want 1, the saved role's", got)
	}

	form.Set(rankRoleField("SGT"), "role-mp")
	res := w.b.postForm("/rank-roles", form)

	if !isClientError(res.StatusCode) {
		t.Fatalf("status of a save naming a rank role as the Discharged role = %d, want 4xx", res.StatusCode)
	}
	if got, _ := dischargedHoldersOf(t, parseHTML(t, res)); got != "1" {
		t.Errorf("the Discharged role's holders beside the refusal = %q, want 1, the saved role's", got)
	}
}
