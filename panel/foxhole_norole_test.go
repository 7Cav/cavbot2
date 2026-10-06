package panel

import (
	"slices"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// newNoRoleWorld is the Foxhole world once two members with a note are off
// the holder list. Notes were saved through the page while their members
// held their roles: Doe's and Kestrel's mention an ally, Marsh's doesn't.
// Then Doe lost Internal and stays in the server, Marsh left the server,
// and Kestrel still holds External.
func newNoRoleWorld(t *testing.T) *testWorld {
	t.Helper()
	w := newFoxholeWorld(t)
	notes := []struct{ id, note string }{
		{memberDoe.ID, "allied liaison, discharged 12 Sep"},
		{memberMarsh.ID, "ask before re-adding"},
		{memberKestrel.ID, "allied group lead"},
	}
	for _, n := range notes {
		assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, n.id), n.note), foxholePath)
	}
	setMembers(w, doeWithoutInternal(), memberAsh, memberKestrel, memberVance)
	return w
}

// doeWithoutInternal is Doe once Internal came off: in the server, with
// their rank role and no Foxhole role.
func doeWithoutInternal() commands.ListedMember {
	doe := memberDoe
	doe.RoleIDs = []string{roleSGT}
	return doe
}

// setMembers makes the member list complete with these members alone.
func setMembers(w *testWorld, members ...commands.ListedMember) {
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true, Members: members})
}

// noRoleLink returns the page's link to the no-role view, and fails the
// test when the page has none.
func noRoleLink(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	link := findElement(doc, "a", "data-filter", filterNoRole)
	if link == nil {
		t.Fatal("the page has no link to the no-role view")
	}
	return link
}

// openNoRoleView follows the page's link to the no-role view from a fresh
// load of the Foxhole page, and returns the page it lands on.
func openNoRoleView(t *testing.T, b *browser) *html.Node {
	t.Helper()
	href, _ := attrValue(noRoleLink(t, parseHTML(t, b.get(foxholePath))), "href")
	return parseHTML(t, follow(t, b, b.get(href)))
}

// noRoleRows returns the no-role view's rows keyed by member ID, and fails
// the test when the page has no no-role view.
func noRoleRows(t *testing.T, doc *html.Node) map[string]*html.Node {
	t.Helper()
	view := findElement(doc, "", "data-field", "no-role")
	if view == nil {
		t.Fatal("the page has no no-role view")
	}
	rows := map[string]*html.Node{}
	eachLiveElement(view, func(n *html.Node) {
		if id, ok := attrValue(n, "data-member"); ok {
			rows[id] = n
		}
	})
	return rows
}

// Once members with a note hold no Foxhole role, the link after the filter
// links counts them and opens a view listing exactly them, the one who left
// the server among them. All keeps listing the holders alone,
// and a member with no role and no note is in neither.
func TestNoRoleViewListsTheMembersWithANoteAndNoFoxholeRole(t *testing.T) {
	w := newNoRoleWorld(t)
	page := parseHTML(t, w.b.get(foxholePath))

	link := noRoleLink(t, page)
	if got, err := strconv.Atoi(fieldText(t, link, "count")); err != nil || got != 2 {
		t.Errorf("the no-role link's count = %q, want 2", fieldText(t, link, "count"))
	}
	if got, want := keys(holderRows(t, page)), []string{memberAsh.ID, memberKestrel.ID}; !slices.Equal(got, want) {
		t.Errorf("All lists %v, want the holders alone, %v", got, want)
	}
	if got, want := keys(noRoleRows(t, openNoRoleView(t, w.b))), []string{memberDoe.ID, memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the no-role view lists %v, want %v", got, want)
	}
}

// A member with a note who left the server stays in the no-role view,
// flagged "not in the server", under the display name and username the
// panel last saw. One still in the server carries no such flag.
func TestNoRoleViewFlagsAMemberWhoLeftTheServerUnderTheirLastNames(t *testing.T) {
	w := newNoRoleWorld(t)

	rows := noRoleRows(t, openNoRoleView(t, w.b))

	marsh, doe := rows[memberMarsh.ID], rows[memberDoe.ID]
	if marsh == nil || doe == nil {
		t.Fatalf("the no-role view lists %v, want rows for %s and %s", keys(rows), memberMarsh.ID, memberDoe.ID)
	}
	if !slices.Contains(valuesOf(marsh, "data-flag"), "not-in-server") {
		t.Errorf("%s's flags = %v, want not-in-server", memberMarsh.ID, valuesOf(marsh, "data-flag"))
	}
	if got := fieldText(t, marsh, "display_name"); got != "CPT Marsh.E" {
		t.Errorf("%s's display name = %q, want the last one seen, CPT Marsh.E", memberMarsh.ID, got)
	}
	if got := fieldText(t, marsh, "username"); got != "emarsh" {
		t.Errorf("%s's username = %q, want the last one seen, emarsh", memberMarsh.ID, got)
	}
	if slices.Contains(valuesOf(doe, "data-flag"), "not-in-server") {
		t.Errorf("%s, still in the server, is flagged not-in-server", memberDoe.ID)
	}
}

// openNoRoleNote follows a no-role row's Edit link from a fresh load of the
// no-role view, as a browser with no script does, and returns the page it
// lands on.
func openNoRoleNote(t *testing.T, b *browser, memberID string) *html.Node {
	t.Helper()
	row, ok := noRoleRows(t, openNoRoleView(t, b))[memberID]
	if !ok {
		t.Fatalf("the no-role view has no row for %s", memberID)
	}
	link := findElement(row, "a", "data-field", "edit_note")
	if link == nil {
		t.Fatalf("%s's row has no Edit link", memberID)
	}
	href, _ := attrValue(link, "href")
	return parseHTML(t, follow(t, b, b.get(href)))
}

// noteHint reports whether the page's note form carries the hint that an
// empty note takes the member off the page, and fails the test when the
// page has no note form.
func noteHint(t *testing.T, doc *html.Node) bool {
	t.Helper()
	form := findLive(doc, "form", "data-field", "note-form")
	if form == nil {
		t.Fatal("the page has no note form")
	}
	return findLive(form, "", "data-field", "note-hint") != nil
}

// The note form a no-role row's Edit link opens warns that an empty note
// takes the member off the page, since saving one does. A holder row's form
// doesn't: a holder with no note stays listed.
func TestNoteFormOnANoRoleRowWarnsThatAnEmptyNoteTakesTheMemberOff(t *testing.T) {
	w := newNoRoleWorld(t)

	if !noteHint(t, openNoRoleNote(t, w.b, memberDoe.ID)) {
		t.Errorf("the note form on %s's no-role row carries no hint", memberDoe.ID)
	}
	if noteHint(t, openNote(t, w.b, memberKestrel.ID)) {
		t.Errorf("the note form on holder %s's row carries the hint", memberKestrel.ID)
	}
}

// Saving an empty note on a no-role row takes the member off the page with
// no confirmation step. The page the save lands on lists them no more and
// says their note was cleared, and the change log keeps the old text, so
// the note can be put back.
func TestClearingANoRoleNoteTakesTheMemberOffAndSaysSo(t *testing.T) {
	w := newNoRoleWorld(t)

	page := parseHTML(t, follow(t, w.b, submitNote(t, w.b, openNoRoleNote(t, w.b, memberDoe.ID), "")))

	if _, listed := noRoleRows(t, page)[memberDoe.ID]; listed {
		t.Errorf("the no-role view still lists %s", memberDoe.ID)
	}
	if result := findLive(page, "", "data-field", "note-result"); result == nil {
		t.Error("the page shows no result of the save")
	} else if cleared, _ := attrValue(result, "data-cleared"); cleared != memberDoe.ID {
		t.Errorf("the result names member %q, want %s", cleared, memberDoe.ID)
	}
	want := foxholeEntry{Action: "note", Username: "Smith.F", Touched: memberDoe.ID, Before: "allied liaison, discharged 12 Sep", After: ""}
	if got := foxholeChangeLog(t, page); len(got) == 0 || got[0] != want {
		t.Errorf("the change log shows %+v, want the newest entry %+v", got, want)
	}
}

// Once a no-role member's note is cleared, nobody can start a note on them
// until they hold a Foxhole role again. A regression pin: the rule predates
// the no-role view. It goes red if the clear left a record behind, as one
// way of naming the member in the save's result would.
func TestNoteCanNotStartOnAMemberWhoseNoRoleNoteWasCleared(t *testing.T) {
	w := newNoRoleWorld(t)
	follow(t, w.b, submitNote(t, w.b, openNoRoleNote(t, w.b, memberDoe.ID), ""))
	before := readNoteState(t, w.st)

	res := w.b.postForm(foxholeNotesPath, noteSaveForm(memberDoe.ID, "", "back for the next war"))

	assertRefused(t, res, "not-holder", w.st, before)
}

// alsoMatchesLine returns the line under a search's results that counts the
// other view's matches: its count and its link. ok is false when the page
// shows no such line.
func alsoMatchesLine(t *testing.T, doc *html.Node) (count int, href string, ok bool) {
	t.Helper()
	line := findLive(doc, "", "data-field", "also-matches")
	if line == nil {
		return 0, "", false
	}
	count, err := strconv.Atoi(fieldText(t, line, "count"))
	if err != nil {
		t.Errorf("the also-matches line's count %q is no number", fieldText(t, line, "count"))
	}
	link := findElement(line, "a", "", "")
	if link == nil {
		t.Fatal("the also-matches line has no link")
	}
	href, _ = attrValue(link, "href")
	return count, href, true
}

// A search in the holder list says, under its results, how many members
// with no role also match, whether or not the holder list found anyone. A
// search the no-role view doesn't match shows no such line.
func TestHolderSearchSaysWhenTheNoRoleViewAlsoMatches(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		holders []string
		also    int
	}{
		{"both views match", "allied", []string{memberKestrel.ID}, 1},
		{"only the no-role view matches", "discharged", nil, 1},
		{"only the holder list matches", "group lead", []string{memberKestrel.ID}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newNoRoleWorld(t)

			page := parseHTML(t, submitSearch(t, w.b, parseHTML(t, w.b.get(foxholePath)), tc.query))

			if got := keys(holderRows(t, page)); !slices.Equal(got, tc.holders) {
				t.Errorf("search %q lists %v, want %v", tc.query, got, tc.holders)
			}
			count, _, ok := alsoMatchesLine(t, page)
			switch {
			case tc.also == 0 && ok:
				t.Errorf("search %q shows an also-matches line counting %d, want none", tc.query, count)
			case tc.also > 0 && (!ok || count != tc.also):
				t.Errorf("search %q: also-matches line shown %v counting %d, want one counting %d", tc.query, ok, count, tc.also)
			}
		})
	}
}

// The also-matches line links to the other view with the search kept, both
// ways: from the holder list to the no-role view, and back.
func TestAlsoMatchesLinksCarryTheSearchToTheOtherView(t *testing.T) {
	w := newNoRoleWorld(t)
	holders := parseHTML(t, submitSearch(t, w.b, parseHTML(t, w.b.get(foxholePath)), "allied"))
	_, toNoRole, ok := alsoMatchesLine(t, holders)
	if !ok {
		t.Fatal("the holder list's search shows no also-matches line")
	}

	noRole := parseHTML(t, follow(t, w.b, w.b.get(toNoRole)))

	if got, want := keys(noRoleRows(t, noRole)), []string{memberDoe.ID}; !slices.Equal(got, want) {
		t.Errorf("the link to %s lists %v, want %v", toNoRole, got, want)
	}
	count, back, ok := alsoMatchesLine(t, noRole)
	if !ok || count != 1 {
		t.Fatalf("the no-role view's search: also-matches line shown %v counting %d, want one counting 1", ok, count)
	}
	if got, want := keys(holderRows(t, parseHTML(t, follow(t, w.b, w.b.get(back))))), []string{memberKestrel.ID}; !slices.Equal(got, want) {
		t.Errorf("the link back to %s lists %v, want %v", back, got, want)
	}
}

// The no-role view reads the member list like the rest of the page. While
// the list is partial it can't tell who holds no role, so the member-list
// notice takes the view's place, though the store holds every note. A
// regression pin: a partial list showed the notice whatever the address
// before this view, and only the link this starts from is new.
func TestNoRoleViewShowsTheMemberListNoticeWhileTheListIsPartial(t *testing.T) {
	w := newNoRoleWorld(t)
	href, _ := attrValue(noRoleLink(t, parseHTML(t, w.b.get(foxholePath))), "href")
	w.p.pageBudget = partialListBudget
	w.discord.setMemberList(arrivingList)

	doc := parseHTML(t, w.b.get(href))

	if findElement(doc, "", "data-notice", "member-list") == nil {
		t.Error("the no-role view shows no member list notice")
	}
	if findElement(doc, "", "data-field", "no-role") != nil {
		t.Error("the no-role view shows its rows while the member list is partial")
	}
}

// The result line says what is true when the page loads. Going back to the
// address a clear landed on, once the member has a note again and the
// no-role view lists them, shows no result line about them.
func TestClearedResultIsGoneOnceTheMemberHasANoteAgain(t *testing.T) {
	w := newNoRoleWorld(t)
	res := submitNote(t, w.b, openNoRoleNote(t, w.b, memberDoe.ID), "")
	landed := location(t, res).RequestURI()
	follow(t, w.b, res)
	setMembers(w, memberDoe, memberAsh, memberKestrel, memberVance)
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "back for the next war"), foxholePath)
	setMembers(w, doeWithoutInternal(), memberAsh, memberKestrel, memberVance)

	page := parseHTML(t, w.b.get(landed))

	if _, listed := noRoleRows(t, page)[memberDoe.ID]; !listed {
		t.Fatalf("the no-role view at %s doesn't list %s, want them listed with their new note", landed, memberDoe.ID)
	}
	if findLive(page, "", "data-cleared", memberDoe.ID) != nil {
		t.Errorf("%s still says %s's note was cleared and they're no longer listed", landed, memberDoe.ID)
	}
}

// Clearing a holder's note leaves them on the holder list, so the page the
// save lands on lists them with no note and says nothing about them leaving.
// A regression pin: the save sent no result for a holder before the check
// moved to the page load.
func TestClearingAHoldersNoteShowsNoClearedResult(t *testing.T) {
	w := newNoRoleWorld(t)

	page := parseHTML(t, follow(t, w.b, submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "")))

	if got := noteOf(t, page, memberKestrel.ID); got != "" {
		t.Errorf("%s's row shows the note %q, want none", memberKestrel.ID, got)
	}
	if findLive(page, "", "data-cleared", memberKestrel.ID) != nil {
		t.Errorf("the page says %s is no longer listed, above their row", memberKestrel.ID)
	}
}
