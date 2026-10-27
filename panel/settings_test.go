package panel

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// The Settings page (#680, GLOSSARY.md): where a panel admin sets the
// guild-wide settings that belong to no hub, the rank roles and the
// recording roles.

// settingsLink returns the rail's link to the Settings page, or nil.
func settingsLink(doc *html.Node) *html.Node {
	return findElement(doc, "a", "data-rail", "settings")
}

// A panel admin finds the Settings page in the rail and opens it. The brief
// names the page "Settings" verbatim, so the link and the title say so.
func TestPanelAdminOpensTheSettingsPageFromTheRail(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	link := settingsLink(parseHTML(t, follow(t, w.b, w.b.get("/"))))
	if link == nil {
		t.Fatal("the panel admin's rail has no data-rail=settings link")
	}
	if got := textOf(link); got != "Settings" {
		t.Errorf("the rail's link reads %q, want Settings", got)
	}
	href, _ := attrValue(link, "href")

	res := w.b.get(href)

	doc := parseHTML(t, res)
	if got := pageOf(t, doc); res.StatusCode != http.StatusOK || got != "settings" {
		t.Errorf("the rail's link %s answers page %q with status %d, want settings with 200", href, got, res.StatusCode)
	}
	if h1 := findElement(doc, "h1", "", ""); h1 == nil || textOf(h1) != "Settings" {
		t.Error("the page's title is not Settings")
	}
}

// A Foxhole manager and an outsider find no Settings link in the rail of
// the page they land on, and the page's address answers them with the
// no-access page.
func TestSettingsPageIsForPanelAdminsAlone(t *testing.T) {
	visitors := []struct {
		name string
		add  func(f *fakeForum) *forumAccount
	}{
		{"Foxhole manager", addFoxholeManager},
		{"outsider", addOutsider},
	}
	for _, v := range visitors {
		t.Run(v.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			landed := follow(t, w.b, signInAs(t, w.forum, w.b, v.add(w.forum)))
			if settingsLink(parseHTML(t, landed)) != nil {
				t.Error("the rail links to the Settings page")
			}

			res := w.b.get(settingsPath)

			if res.StatusCode != http.StatusForbidden {
				t.Errorf("GET %s status = %d, want 403", settingsPath, res.StatusCode)
			}
			assertNoAccessSays(t, parseHTML(t, res), noAccessRefused)
		})
	}
}

// rankRows returns the Rank roles section's rows, in the page's order.
func rankRows(t *testing.T, doc *html.Node) []*html.Node {
	t.Helper()
	sec := findElement(doc, "section", "data-section", "rank-roles")
	if sec == nil {
		t.Fatal("page has no section under data-section=rank-roles")
	}
	var rows []*html.Node
	eachElement(sec, func(n *html.Node) {
		if _, ok := attrValue(n, "data-rank"); ok {
			rows = append(rows, n)
		}
	})
	return rows
}

// With nothing saved, the Rank roles section lists the ladder's ranks, most
// senior first, each with its role ID in code. A role the guild lacks is
// marked deleted, and one it has is not.
func TestRankRolesSectionShowsTheLadderInCodeWithNothingSaved(t *testing.T) {
	w := newTestWorld(t, testHub())
	w.discord.addRoles(&discordgo.Role{ID: testRankSGT, Name: "Sergeant"})
	signIn(t, w.forum, w.b)

	rows := rankRows(t, parseHTML(t, w.b.get(settingsPath)))

	ladder := commands.DefaultRankLadder().Ranks()
	if len(rows) != len(ladder) {
		t.Fatalf("section lists %d ranks, want the ladder's %d", len(rows), len(ladder))
	}
	for i, rank := range ladder {
		if got, _ := attrValue(rows[i], "data-rank"); got != rank.Abbrev {
			t.Errorf("row %d is rank %q, want %q", i, got, rank.Abbrev)
			continue
		}
		inputs := postedInputs(rows[i], rankRoleField(rank.Abbrev))
		if len(inputs) != 1 {
			t.Errorf("rank %s posts %d roles, want its one role in code", rank.Abbrev, len(inputs))
			continue
		}
		if got, _ := attrValue(inputs[0], "value"); got != rank.RoleID {
			t.Errorf("rank %s posts role %q, want %q", rank.Abbrev, got, rank.RoleID)
		}
		want := string(unavailableDeleted)
		if rank.RoleID == testRankSGT {
			want = ""
		}
		if got, _ := attrValue(inputs[0], "data-unavailable"); got != want {
			t.Errorf("rank %s role is marked data-unavailable=%q, want %q", rank.Abbrev, got, want)
		}
	}
}

// storedRankRoles reads the rank roles back through the store, the read the
// runtime makes at startup.
func storedRankRoles(t *testing.T, st store.Store) store.RankRoles {
	t.Helper()
	roles, err := st.GetRankRoles(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("GetRankRoles: %v", err)
	}
	return roles
}

// rankRolesForm is the Rank roles section's form as a page loaded now posts
// it, with the changes given made. Each rank posts its role as the stored
// ladder gives it, a changed rank posts its change's role, and a change to
// empty posts no role. The Discharged role posts as stored.
func rankRolesForm(t *testing.T, st store.Store, changes map[string]string) url.Values {
	t.Helper()
	stored := storedRankRoles(t, st)
	form := url.Values{"version": {strconv.FormatInt(stored.Version, 10)}}
	ladder := commands.RankLadderOf(stored)
	if id := ladder.DischargedRole(); id != "" {
		form.Set(fieldDischargedRole, id)
	}
	for _, rank := range ladder.Ranks() {
		id := rank.RoleID
		if changed, ok := changes[rank.Abbrev]; ok {
			id = changed
		}
		if id != "" {
			form.Set(rankRoleField(rank.Abbrev), id)
		}
	}
	return form
}

// A save that gives SGT a role makes its holders Cav members at once. One
// who joins a hub owns the channel it spawns.
func TestRankRolesSaveMakesARoleHolderOwnTheirSpawnedChannel(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq"}))

	assertRedirect(t, res, settingsPath)
	w.joinAs("user-hq", "hub-1", "role-hq")
	w.joinAs("user-hq", "spawn-1", "role-hq")
	if owner, _ := w.runtime.Owner("spawn-1"); owner != "user-hq" {
		t.Errorf("Owner(spawn-1) = %q, want user-hq, who holds the role saved for SGT", owner)
	}
}

// A save that gives RCT a role shows on the Foxhole page at once. An
// Internal holder of that role shows it as their rank role, unflagged.
func TestRankRolesSaveShowsOnTheFoxholePageAtOnce(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.addRoles(&discordgo.Role{ID: "role-recruit", Name: "Recruit"})
	w.discord.editMember(memberAsh.ID, func(m *commands.ListedMember) { m.RoleIDs = append(m.RoleIDs, "role-recruit") })
	admin := secondBrowser(t, w)

	assertRedirect(t, admin.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"RCT": "role-recruit"})), settingsPath)

	row := holderRows(t, parseHTML(t, w.b.get(foxholePath)))[memberAsh.ID]
	if row == nil {
		t.Fatalf("no holder row for %s", memberAsh.ID)
	}
	if got := fieldText(t, row, "rank_role"); got != "Recruit" {
		t.Errorf("Ash's rank role = %q, want Recruit, the role saved for RCT", got)
	}
	if flags := valuesOf(row, "data-flag"); slices.Contains(flags, "no-rank-role") {
		t.Errorf("Ash's row is flagged %v, want no no-rank-role flag", flags)
	}
}

// rankRolesSection returns the Rank roles section of a parsed page.
func rankRolesSection(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	sec := findElement(doc, "section", "data-section", "rank-roles")
	if sec == nil {
		t.Fatal("page has no section under data-section=rank-roles")
	}
	return sec
}

// refusalIn returns the data-error of the refusal under n, and whether
// there is one.
func refusalIn(n *html.Node) (string, bool) {
	var kind string
	found := false
	eachElement(n, func(el *html.Node) {
		if v, ok := attrValue(el, "data-error"); ok && !found {
			kind, found = v, true
		}
	})
	return kind, found
}

// A save naming one role for two ranks is refused, and so is one adding
// @everyone or a role an integration manages. Each answers with the section
// carrying a refusal, and writes nothing.
func TestRankRolesSaveRefusesARoleNoRankMayHave(t *testing.T) {
	cases := []struct {
		name    string
		changes map[string]string
	}{
		{"one role for two ranks", map[string]string{"SGT": "role-hq", "CPL": "role-hq"}},
		{"@everyone", map[string]string{"SGT": testGuildID}},
		{"a managed role", map[string]string{"SGT": "role-bot"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			before := readSavedState(t, w.st)

			res := w.b.postForm("/rank-roles", rankRolesForm(t, w.st, tc.changes))

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			if _, ok := refusalIn(rankRolesSection(t, parseHTML(t, res))); !ok {
				t.Error("the Rank roles section carries no refusal")
			}
			if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
				t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
			}
		})
	}
}

// A save that changes one rank keeps each other rank's role as stored, an
// unavailable one included. While nothing is saved that covers the role
// the code holds for CPT, which the guild lacks. Once saved it covers a
// role saved for CPL and deleted in Discord since.
func TestRankRolesSaveKeepsAnotherRanksUnavailableRole(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	codeCPT := commands.DefaultRankLadder().Roles()["CPT"]

	assertRedirect(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"CPL": "role-mp"})), settingsPath)

	if got := storedRankRoles(t, w.st).Roles["CPT"]; got != codeCPT {
		t.Errorf("CPT's role after the first save = %q, want the code's %s, kept though the guild lacks it", got, codeCPT)
	}

	w.discord.removeRole("role-mp")
	assertRedirect(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq"})), settingsPath)

	roles := storedRankRoles(t, w.st).Roles
	if roles["CPL"] != "role-mp" || roles["CPT"] != codeCPT {
		t.Errorf("after the second save CPL = %q and CPT = %q, want role-mp and %s, both kept", roles["CPL"], roles["CPT"], codeCPT)
	}
}

// previewIn returns the Rank roles section's preview, or nil.
func previewIn(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	return findElement(rankRolesSection(t, doc), "", "data-field", "rank-roles-preview")
}

// previewCounts reads a preview's member count and channel count, each as
// its data-* value carries it.
func previewCounts(t *testing.T, preview *html.Node) (dropped, channels string) {
	t.Helper()
	eachElement(preview, func(n *html.Node) {
		if v, ok := attrValue(n, "data-dropped"); ok {
			dropped = v
		}
		if v, ok := attrValue(n, "data-channels"); ok {
			channels = v
		}
	})
	return dropped, channels
}

// testRankPVT is the PVT role ID of the ladder in code, as testRankSGT is
// its SGT one.
const testRankPVT = "899328617081864202"

// withSergeantInSpawn is the test world with a member holding the code's
// SGT role in the member list, who owns the channel their hub join spawned,
// spawn-1, where a PVT sits with them.
func withSergeantInSpawn(t *testing.T) *testWorld {
	t.Helper()
	w := newTestWorld(t, testHub())
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{testBotMember,
			{ID: "user-sgt", Username: "sgt", RoleIDs: []string{testRankSGT}},
			{ID: "user-pvt", Username: "pvt", RoleIDs: []string{testRankPVT}}}})
	w.joinAs("user-sgt", "hub-1", testRankSGT)
	w.joinAs("user-sgt", "spawn-1", testRankSGT)
	w.joinAs("user-pvt", "spawn-1", testRankPVT)
	signIn(t, w.forum, w.b)
	return w
}

// A save that would stop a member counting as a Cav member shows a preview
// naming how many members stop and how many channels change owner, and
// changes nothing. Its Confirm makes the save.
func TestRankRolesSaveThatDropsACavMemberWaitsOnItsPreview(t *testing.T) {
	w := withSergeantInSpawn(t)
	before := readSavedState(t, w.st)

	res := follow(t, w.b, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": ""})))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the preview", res.StatusCode)
	}
	preview := previewIn(t, parseHTML(t, res))
	if preview == nil {
		t.Fatal("the Rank roles section shows no preview")
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

	if got := storedRankRoles(t, w.st).Roles["SGT"]; got != "" {
		t.Errorf("SGT's role after Confirm = %q, want none", got)
	}
	if owner, _ := w.runtime.Owner("spawn-1"); owner != "user-pvt" {
		t.Errorf("Owner(spawn-1) after Confirm = %q, want user-pvt", owner)
	}
}

// A save that stops nobody counting as a Cav member saves straight away. So
// does one that keeps every Cav member, and with the member list not
// loaded, a save that takes a role off the ladder previews with the member
// count unknown, since nothing can rule a drop out.
func TestRankRolesSaveSkipsThePreviewOnlyWhenItDropsNobody(t *testing.T) {
	w := withSergeantInSpawn(t)

	assertRedirect(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"RCT": "role-hq"})), settingsPath)

	w.discord.setListStatus(commands.MemberListArriving, true)
	res := follow(t, w.b, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"CPL": ""})))

	preview := previewIn(t, parseHTML(t, res))
	if preview == nil {
		t.Fatalf("status %d with no preview, want the preview while the member list loads", res.StatusCode)
	}
	if dropped, _ := previewCounts(t, preview); dropped != "unknown" {
		t.Errorf("preview's member count = %q, want unknown", dropped)
	}
}

// Each rank roles save writes one change log entry under no hub, naming
// each rank whose role changed with its role before and after, and no
// other rank. The Rank roles section lists it with those ranks.
func TestRankRolesSaveWritesOneEntryTheSectionShows(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	codeCPL := commands.DefaultRankLadder().Roles()["CPL"]

	assertRedirect(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq", "CPL": ""})), settingsPath)

	entries, err := w.st.ListRankRoleChanges(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRankRoleChanges: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d rank roles entries, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeRankRoles)
	want := map[string]fieldChange{
		rankRoleField("SGT"): {Before: testRankSGT, After: "role-hq"},
		rankRoleField("CPL"): {Before: codeCPL, After: nil},
	}
	if got := decodeDiff(t, entries[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("diff = %+v, want %+v", got, want)
	}

	sec := rankRolesSection(t, parseHTML(t, w.b.get(settingsPath)))
	entry := findElement(sec, "", "data-entry", strconv.FormatInt(entries[0].ID, 10))
	if entry == nil {
		t.Fatal("the Rank roles section does not list the save's entry")
	}
	if got := valuesOf(entry, "data-change"); !sameSet(got, []string{rankRoleField("SGT"), rankRoleField("CPL")}) {
		t.Errorf("the listed entry shows changes %v, want SGT's and CPL's", got)
	}
}

// A save from a Rank roles form loaded before another save of the rank
// roles is stale, so it is refused with a 409 and writes nothing. So is a save that
// changes no rank's role, with its own refusal.
func TestRankRolesSaveRefusesAStaleFormAndAnUnchangedSave(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	loaded := rankRolesForm(t, w.st, map[string]string{"SGT": "role-mp"})
	assertRedirect(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq"})), settingsPath)
	before := readSavedState(t, w.st)

	res := w.b.postForm("/rank-roles", loaded)

	if res.StatusCode != http.StatusConflict {
		t.Errorf("stale save status = %d, want 409", res.StatusCode)
	}
	if kind, _ := refusalIn(rankRolesSection(t, parseHTML(t, res))); kind != refusalStale {
		t.Errorf("stale save's refusal is data-error=%q, want %s", kind, refusalStale)
	}
	if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the stale save = %+v, want it as before, %+v", after, before)
	}

	res = w.b.postForm("/rank-roles", rankRolesForm(t, w.st, nil))

	if !isClientError(res.StatusCode) {
		t.Errorf("unchanged save status = %d, want 4xx", res.StatusCode)
	}
	if kind, _ := refusalIn(rankRolesSection(t, parseHTML(t, res))); kind != refusalUnchanged {
		t.Errorf("unchanged save's refusal is data-error=%q, want %s", kind, refusalUnchanged)
	}
	if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the unchanged save = %+v, want it as before, %+v", after, before)
	}
}

// holdersOf reads the holder count a rank's row carries in data-holders.
func holdersOf(t *testing.T, doc *html.Node, abbrev string) string {
	t.Helper()
	for _, row := range rankRows(t, doc) {
		if got, _ := attrValue(row, "data-rank"); got != abbrev {
			continue
		}
		var holders string
		eachElement(row, func(n *html.Node) {
			if v, ok := attrValue(n, "data-holders"); ok {
				holders = v
			}
		})
		return holders
	}
	t.Fatalf("no row for rank %s", abbrev)
	return ""
}

// Each rank's row counts the members holding its role, from the member
// list. While the list has not loaded, the count is unknown, never 0.
func TestRankRolesSectionCountsEachRolesHolders(t *testing.T) {
	w := newTestWorld(t, testHub())
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{testBotMember,
			{ID: "user-1", Username: "one", RoleIDs: []string{testRankSGT}},
			{ID: "user-2", Username: "two", RoleIDs: []string{"role-hq", testRankSGT}},
			{ID: "user-3", Username: "three", RoleIDs: []string{testRankPVT}}}})
	signIn(t, w.forum, w.b)

	doc := parseHTML(t, w.b.get(settingsPath))

	for abbrev, want := range map[string]string{"SGT": "2", "PVT": "1", "CPT": "0"} {
		if got := holdersOf(t, doc, abbrev); got != want {
			t.Errorf("%s holders = %q, want %s", abbrev, got, want)
		}
	}

	w.discord.setListStatus(commands.MemberListArriving, true)
	doc = parseHTML(t, w.b.get(settingsPath))

	if got := holdersOf(t, doc, "SGT"); got != "unknown" {
		t.Errorf("SGT holders while the member list loads = %q, want unknown", got)
	}
}

// The recording roles section is on the Settings page, and the hub page no
// longer has it. A save of it lands back on the Settings page, which shows
// the saved set.
func TestRecordingRolesSectionMovedToTheSettingsPage(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	if findElement(parseHTML(t, w.b.get("/")), "section", "data-section", "recording-roles") != nil {
		t.Error("the hub page still has the recording roles section")
	}
	form := loadedForm(t, w.b, settingsPath, "/recording-roles")
	form["recording_roles"] = []string{"role-hq"}

	res := w.b.postForm("/recording-roles", form)

	assertRedirect(t, res, settingsPath)
	if got := storedRecordingRoles(t, w.st).RoleIDs; !sameSet(got, []string{"role-hq"}) {
		t.Errorf("stored recording roles = %v, want role-hq", got)
	}
	sec := recordingRolesSection(t, parseHTML(t, follow(t, w.b, res)))
	if got := postedControls(sec, "recording_roles"); !sameSet(got, []string{"role-hq"}) {
		t.Errorf("the Settings page's recording roles section holds %v, want role-hq", got)
	}
}

// rowOfRank returns the Rank roles section's row for a rank.
func rowOfRank(t *testing.T, doc *html.Node, abbrev string) *html.Node {
	t.Helper()
	for _, row := range rankRows(t, doc) {
		if got, _ := attrValue(row, "data-rank"); got == abbrev {
			return row
		}
	}
	t.Fatalf("no row for rank %s", abbrev)
	return nil
}

// noRoleVerbatim is what a rank's row reads while its picker holds no tag.
// The brief fixes it verbatim (#756), so tests quote it.
const noRoleVerbatim = "No role"

// A rank whose role a save cleared reads "No role" in its row at load, and
// a rank with a role does not (#756). The test browser applies no
// stylesheet, so this checks the page as a browser without the
// stylesheet's :has rules shows it.
func TestRankRolesSectionSaysWhenARankHasNoRole(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq", "CPL": ""})), settingsPath)

	doc := parseHTML(t, w.b.get(settingsPath))

	if shown := shownText(rowOfRank(t, doc, "CPL")); !strings.Contains(shown, noRoleVerbatim) {
		t.Errorf("CPL's row, with no role, reads %q, want it to say %s", shown, noRoleVerbatim)
	}
	if shown := shownText(rowOfRank(t, doc, "SGT")); strings.Contains(shown, noRoleVerbatim) {
		t.Errorf("SGT's row, with a role, reads %q, which says %s", shown, noRoleVerbatim)
	}
}

// A rank roles preview's Confirm acts on what the preview showed. When a
// second SGT spawns a channel between the preview and its Confirm, the save
// would hand over one more channel than the preview counted, so the Confirm
// is refused and writes nothing, and the preview shows again with the
// channels as they stand now. Its Confirm then saves.
func TestRankRolesConfirmIsRefusedWhenItsPreviewMoved(t *testing.T) {
	w := withSergeantInSpawn(t)
	res := follow(t, w.b, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": ""})))
	shown := previewIn(t, parseHTML(t, res))
	if shown == nil {
		t.Fatal("the save shows no preview")
	}
	confirm := formPosts(t, shown, "/rank-roles")
	w.joinAs("user-sgt2", "hub-1", testRankSGT)
	w.joinAs("user-sgt2", "spawn-2", testRankSGT)
	before := readSavedState(t, w.st)

	res = w.b.postForm("/rank-roles", confirm)

	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if kind, _ := refusalIn(rankRolesSection(t, doc)); kind != "changed" {
		t.Errorf("refusal is data-error=%q, want changed", kind)
	}
	again := previewIn(t, doc)
	if again == nil {
		t.Fatal("the refused Confirm shows no preview")
	}
	if _, channels := previewCounts(t, again); channels != "2" {
		t.Errorf("the preview shown again counts %s channels, want 2", channels)
	}
	if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the refused Confirm = %+v, want it as before, %+v", after, before)
	}
	if owner, _ := w.runtime.Owner("spawn-1"); owner != "user-sgt" {
		t.Errorf("Owner(spawn-1) after the refused Confirm = %q, want user-sgt still", owner)
	}

	assertRedirect(t, w.b.postForm("/rank-roles", formPosts(t, again, "/rank-roles")), settingsPath)

	if got := storedRankRoles(t, w.st).Roles["SGT"]; got != "" {
		t.Errorf("SGT's role after the second Confirm = %q, want none", got)
	}
}

// rankFields is the rank roles a Rank roles form posts, keyed by field, and
// the version it posts beside them.
func rankFields(form url.Values) (map[string]string, string) {
	roles := map[string]string{}
	for field, values := range form {
		if strings.HasPrefix(field, rankRoleFieldPrefix) {
			roles[field] = values[0]
		}
	}
	return roles, form.Get(fieldVersion)
}

// rankRoleEntries is how many entries the Rank roles section's change log
// shows.
func rankRoleEntries(t *testing.T, doc *html.Node) int {
	t.Helper()
	return len(valuesOf(rankRolesSection(t, doc), "data-entry"))
}

// A rank roles save that waits on its preview answers with a redirect to
// the address that shows the preview (ADR 0019), so a reload of the
// preview loads it again and never saves. The address carries what the
// save posted: in a browser that holds nothing else, the preview's Confirm
// posts every rank's role and the version as the save did. With every rank
// filled the address stays under the request line the proxy in front of
// the panel allows.
func TestRankRolesSaveThatWaitsOnItsPreviewRedirectsToItsAddress(t *testing.T) {
	w := withSergeantInSpawn(t)
	form := rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq"})
	posted, version := rankFields(form)
	if len(posted) != len(commands.DefaultRankLadder().Ranks()) {
		t.Fatalf("the save posts %d ranks' roles, want every rank filled", len(posted))
	}

	res := w.b.postForm("/rank-roles", form)

	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusSeeOther)
	}
	address := res.Header.Get("Location")
	if len(address) >= proxyRequestLine {
		t.Errorf("the preview's address is %d bytes long, want it under the proxy's %d-byte request line", len(address), proxyRequestLine)
	}
	fresh := newBrowser(t, w.p)
	signIn(t, w.forum, fresh)
	preview := previewIn(t, parseHTML(t, follow(t, fresh, fresh.get(address))))
	if preview == nil {
		t.Fatalf("a GET of %s shows no preview", address)
	}
	if dropped, channels := previewCounts(t, preview); dropped != "1" || channels != "1" {
		t.Errorf("the preview counts %s members and %s channels, want 1 and 1", dropped, channels)
	}
	confirmed, confirmedVersion := rankFields(formPosts(t, preview, "/rank-roles"))
	if !maps.Equal(confirmed, posted) || confirmedVersion != version {
		t.Errorf("the preview's Confirm posts %v at version %q, want what the save posted, %v at version %q", confirmed, confirmedVersion, posted, version)
	}
}

// A reload of a rank roles preview loads it again from its address and
// saves nothing, even once the save would stop nobody counting as a Cav
// member. The preview then shows that no member stops, with its Confirm.
func TestRankRolesPreviewReloadedOnceNobodyDropsShowsZeroAndSavesNothing(t *testing.T) {
	w := withSergeantInSpawn(t)
	address := location(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq"}))).String()
	entries := rankRoleEntries(t, parseHTML(t, w.b.get(settingsPath)))
	w.discord.dropMember("user-sgt")

	preview := previewIn(t, parseHTML(t, follow(t, w.b, w.b.get(address))))

	if preview == nil {
		t.Fatalf("a GET of %s shows no preview", address)
	}
	if dropped, _ := previewCounts(t, preview); dropped != "0" {
		t.Errorf("the preview counts %s members, want 0", dropped)
	}
	if findElement(preview, "", "data-field", "confirm") == nil {
		t.Error("the preview offers no Confirm")
	}
	if got := rankRoleEntries(t, parseHTML(t, w.b.get(settingsPath))); got != entries {
		t.Errorf("the Rank roles change log shows %d entries after the reload, want %d: nothing saved", got, entries)
	}
}

// The preview's address carries the version the save posted, so a Confirm
// pressed after another panel admin saved the rank roles is refused as
// stale and leaves their save in place.
func TestRankRolesConfirmFromAStaleAddressIsRefusedAsStale(t *testing.T) {
	w := withSergeantInSpawn(t)
	address := location(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": "role-hq"}))).String()
	preview := previewIn(t, parseHTML(t, follow(t, w.b, w.b.get(address))))
	if preview == nil {
		t.Fatalf("a GET of %s shows no preview", address)
	}
	other := secondBrowser(t, w)
	assertRedirect(t, other.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"RCT": "role-mp"})), settingsPath)

	res := w.b.postForm("/rank-roles", formPosts(t, preview, "/rank-roles"))

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	if kind, _ := refusalIn(rankRolesSection(t, parseHTML(t, res))); kind != refusalStale {
		t.Errorf("refusal is data-error=%q, want %s", kind, refusalStale)
	}
	stored, _ := rankFields(formPosts(t, parseHTML(t, w.b.get(settingsPath)), "/rank-roles"))
	if stored[rankRoleField("RCT")] != "role-mp" || stored[rankRoleField("SGT")] != testRankSGT {
		t.Errorf("the Settings page shows RCT %q and SGT %q, want the other admin's save: role-mp and %s still", stored[rankRoleField("RCT")], stored[rankRoleField("SGT")], testRankSGT)
	}
}
