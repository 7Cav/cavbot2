package panel

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// editNoteLink returns the href of a holder row's Edit link, and fails the
// test when the row or its link is missing.
func editNoteLink(t *testing.T, doc *html.Node, memberID string) string {
	t.Helper()
	row, ok := holderRows(t, doc)[memberID]
	if !ok {
		t.Fatalf("the holder list has no row for %s", memberID)
	}
	link := findElement(row, "a", "data-field", "edit_note")
	if link == nil {
		t.Fatalf("%s's row has no Edit link", memberID)
	}
	href, _ := attrValue(link, "href")
	return href
}

// openNote follows a holder row's Edit link from a fresh load of the
// Foxhole page, as a browser with no script does, and returns the page it
// lands on.
func openNote(t *testing.T, b *browser, memberID string) *html.Node {
	t.Helper()
	href := editNoteLink(t, parseHTML(t, b.get(foxholePath)), memberID)
	res := follow(t, b, b.get(href))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the Edit link to %s answers %d, want 200", href, res.StatusCode)
	}
	return parseHTML(t, res)
}

// findLive is findElement over the elements a browser puts in the
// document, so a form held in a <template> for the script is never taken
// for the page's own.
func findLive(n *html.Node, tag, attr, value string) *html.Node {
	var found *html.Node
	eachLiveElement(n, func(el *html.Node) {
		if found != nil || (tag != "" && el.Data != tag) {
			return
		}
		if v, ok := attrValue(el, attr); ok && v == value {
			found = el
		}
	})
	return found
}

// submitNote submits the page's note form with text typed in its note box,
// the way a browser posts a form.
func submitNote(t *testing.T, b *browser, doc *html.Node, text string) *http.Response {
	t.Helper()
	if findLive(doc, "form", "data-field", "note-form") == nil {
		t.Fatal("the page has no note form")
	}
	fields := formPosts(t, doc, foxholeNotesPath)
	fields.Set(fieldNote, text)
	return b.postForm(foxholeNotesPath, fields)
}

// noteOf returns the note a holder row shows: the text of its note field,
// empty for a member with none.
func noteOf(t *testing.T, doc *html.Node, memberID string) string {
	t.Helper()
	row, ok := holderRows(t, doc)[memberID]
	if !ok {
		t.Fatalf("the holder list has no row for %s", memberID)
	}
	return fieldText(t, row, "note")
}

// foxholeEntry is one entry of the Foxhole page's change log as the page
// shows it: who saved, the member it touched, and the note's old and new
// text.
type foxholeEntry struct {
	Action, Username, Touched, Before, After string
}

// foxholeChangeLog returns the entries of the Foxhole page's change log,
// newest first, and fails the test when the page has none.
func foxholeChangeLog(t *testing.T, doc *html.Node) []foxholeEntry {
	t.Helper()
	section := findElement(doc, "", "data-field", "changes")
	if section == nil {
		t.Fatal("the page has no change log")
	}
	var out []foxholeEntry
	for _, el := range entryElements(section) {
		e := foxholeEntry{Username: fieldText(t, el, "username"), Before: fieldText(t, el, "before"), After: fieldText(t, el, "after")}
		e.Action, _ = attrValue(el, "data-action")
		if touched := valuesOf(el, "data-touched"); len(touched) == 1 {
			e.Touched = touched[0]
		}
		out = append(out, e)
	}
	return out
}

// A Foxhole manager with no script starts a note on a holder and then edits
// it, each time through the row's Edit link and the server-rendered form it
// opens. The row shows the note saved, and the page's change log names the
// member each save touched, with the note's old and new text.
func TestNoteSavedThroughTheEditLinkShowsOnTheRowAndInTheChangeLog(t *testing.T) {
	w := newFoxholeWorld(t)

	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
	res := submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep, ask before re-adding")

	assertRedirect(t, res, foxholePath)
	page := parseHTML(t, follow(t, w.b, res))
	if got := noteOf(t, page, memberDoe.ID); got != "discharged 12 Sep, ask before re-adding" {
		t.Errorf("%s's row shows the note %q, want the one saved", memberDoe.ID, got)
	}
	want := []foxholeEntry{
		{Action: "note", Username: "Smith.F", Touched: memberDoe.ID, Before: "discharged 12 Sep", After: "discharged 12 Sep, ask before re-adding"},
		{Action: "note", Username: "Smith.F", Touched: memberDoe.ID, Before: "", After: "discharged 12 Sep"},
	}
	got := foxholeChangeLog(t, page)
	if len(got) != len(want) {
		t.Fatalf("the change log shows %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change log entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A note save makes no Discord call: it reads the member from the member
// list in the gateway state, never from Discord's API, and a note changes
// no role. The count runs across the save alone, not the page it redirects
// to. A regression pin: the save read the member list from its first case.
func TestNoteSaveMakesNoDiscordCall(t *testing.T) {
	w := newFoxholeWorld(t)
	form := openNote(t, w.b, memberDoe.ID)
	before := w.discord.apiReadCount()

	res := submitNote(t, w.b, form, "discharged 12 Sep")

	assertRedirect(t, res, foxholePath)
	if n := w.discord.apiReadCount() - before; n != 0 {
		t.Errorf("the note save made %d reads of Discord's API, want 0", n)
	}
}

// noteSaveForm is a note save as a form posts it, with no view to return
// to.
func noteSaveForm(memberID, loaded, note string) url.Values {
	return url.Values{fieldMember: {memberID}, fieldLoaded: {loaded}, fieldNote: {note}}
}

// noteState is everything a note save can write, read back through the
// store: the test guild's Foxhole records, by member ID, and the Foxhole
// change log.
type noteState struct {
	Records []store.FoxholeRecord
	Entries []store.ChangeLogEntry
}

func readNoteState(t *testing.T, st store.Store) noteState {
	t.Helper()
	records, err := st.ListFoxholeRecords(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("ListFoxholeRecords: %v", err)
	}
	slices.SortFunc(records, func(a, b store.FoxholeRecord) int { return strings.Compare(a.MemberID, b.MemberID) })
	entries, err := st.ListFoxholeChanges(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListFoxholeChanges: %v", err)
	}
	return noteState{Records: records, Entries: entries}
}

// assertRefused checks a note save answered with the page naming the
// refusal's reason, and that the store holds what it held before. It
// returns the page.
func assertRefused(t *testing.T, res *http.Response, reason string, st store.Store, before noteState) *html.Node {
	t.Helper()
	doc := parseHTML(t, res)
	if findLive(doc, "", "data-error", reason) == nil {
		t.Errorf("the answer (status %d) names no refusal %q", res.StatusCode, reason)
	}
	if after := readNoteState(t, st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
	}
	return doc
}

// A note starts only on a Foxhole role holder. A save that would start one
// on a member who holds no Foxhole role, posted however, is refused with
// that reason, and the store holds neither a note nor an entry.
func TestNoteStartOnAMemberWithNoFoxholeRoleIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	before := readNoteState(t, w.st)

	res := w.b.postForm(foxholeNotesPath, noteSaveForm(memberVance.ID, "", "ask before adding"))

	assertRefused(t, res, "not-holder", w.st, before)
}

// arrivingList is a member list whose parts are still on their way, as the
// production adapter gives it: no members until it is complete.
var arrivingList = commands.MemberListSnapshot{Status: commands.MemberListArriving, Connected: true, PartsReceived: 3, PartsExpected: 10}

// A note edit stays open while the member list is partial: editing a note
// a member already has reads nothing from the list, and the record keeps
// the names it holds. A regression pin: the holder check never applied to a
// member with a record.
func TestNoteEditSavesWhileTheMemberListIsPartial(t *testing.T) {
	w := newFoxholeWorld(t)
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
	w.discord.setMemberList(arrivingList)

	res := w.b.postForm(foxholeNotesPath, noteSaveForm(memberDoe.ID, "discharged 12 Sep", "rejoined, fine to re-add"))

	assertRedirect(t, res, foxholePath)
	want := []store.FoxholeRecord{{MemberID: memberDoe.ID, Note: "rejoined, fine to re-add", DisplayName: "SGT Doe.J", Username: "jdoe"}}
	if got := readNoteState(t, w.st).Records; !reflect.DeepEqual(got, want) {
		t.Errorf("records = %+v, want %+v", got, want)
	}
}

// While the member list is partial, a note can't start: the save can't see
// whether the member holds a Foxhole role. It is refused with that reason,
// not the one a member found holding no role gets, since the manager can
// save once the list arrives, and it writes nothing.
func TestNoteStartWhileTheMemberListIsPartialIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.setMemberList(arrivingList)
	before := readNoteState(t, w.st)

	res := w.b.postForm(foxholeNotesPath, noteSaveForm(memberVance.ID, "", "ask before adding"))

	assertRefused(t, res, "member-list", w.st, before)
}

// A note form another save got to first is stale. Its save is refused with
// that reason and writes nothing, so it never writes over a note its
// manager didn't see. The form comes back holding the text they typed, and
// saving it again writes over the note as it stands now.
func TestStaleNoteFormIsRefusedAndItsNextSaveLands(t *testing.T) {
	w := newFoxholeWorld(t)
	stale := openNote(t, w.b, memberDoe.ID)
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
	before := readNoteState(t, w.st)

	doc := assertRefused(t, submitNote(t, w.b, stale, "rejoined, fine to re-add"), "stale", w.st, before)

	box := findLive(doc, "input", "name", fieldNote)
	if box == nil {
		t.Fatal("the refused save's page has no note box")
	}
	if typed, _ := attrValue(box, "value"); typed != "rejoined, fine to re-add" {
		t.Errorf("the note box holds %q, want the text typed", typed)
	}
	assertRedirect(t, submitNote(t, w.b, doc, "rejoined, fine to re-add"), foxholePath)
	want := []store.FoxholeRecord{{MemberID: memberDoe.ID, Note: "rejoined, fine to re-add", DisplayName: "SGT Doe.J", Username: "jdoe"}}
	if got := readNoteState(t, w.st).Records; !reflect.DeepEqual(got, want) {
		t.Errorf("records after the second save = %+v, want %+v", got, want)
	}
}

// Saving an empty note clears it: the row shows no note, and the change log
// keeps the old text, so the note can be put back. A regression pin: the
// save wrote an empty note like any other from its first case.
func TestClearedNoteLeavesTheRowAndKeepsTheOldTextInTheChangeLog(t *testing.T) {
	w := newFoxholeWorld(t)
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)

	res := submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "")

	assertRedirect(t, res, foxholePath)
	page := parseHTML(t, follow(t, w.b, res))
	if got := noteOf(t, page, memberDoe.ID); got != "" {
		t.Errorf("%s's row shows the note %q, want none", memberDoe.ID, got)
	}
	got := foxholeChangeLog(t, page)
	want := foxholeEntry{Action: "note", Username: "Smith.F", Touched: memberDoe.ID, Before: "discharged 12 Sep", After: ""}
	if len(got) == 0 || got[0] != want {
		t.Errorf("the change log shows %+v, want the newest entry %+v", got, want)
	}
}

// Search also matches note text, ignoring case: a word from one holder's
// note lists that holder alone.
func TestHolderSearchMatchesNoteText(t *testing.T) {
	w := newFoxholeWorld(t)
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "allied group lead, ask before re-adding"), foxholePath)
	page := parseHTML(t, w.b.get(foxholePath))

	rows := holderRows(t, parseHTML(t, submitSearch(t, w.b, page, "Before Re-Adding")))

	if got, want := keys(rows), []string{memberKestrel.ID}; !slices.Equal(got, want) {
		t.Errorf("the search lists %v, want %v", got, want)
	}
}

// A page load refreshes the stored names of each member with a record
// whose display name or username changed in the server, so a member who
// leaves later shows under the names the panel saw last.
func TestPageLoadRefreshesTheLastSeenNamesOfAMemberWithANote(t *testing.T) {
	w := newFoxholeWorld(t)
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
	renamed := memberDoe
	renamed.Nick, renamed.Username = "CPL Doe.J", "jdoe_cav"
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{renamed, memberAsh, memberKestrel, memberMarsh, memberVance}})

	w.b.get(foxholePath)

	want := []store.FoxholeRecord{{MemberID: memberDoe.ID, Note: "discharged 12 Sep", DisplayName: "CPL Doe.J", Username: "jdoe_cav"}}
	if got := readNoteState(t, w.st).Records; !reflect.DeepEqual(got, want) {
		t.Errorf("records after the page load = %+v, want %+v", got, want)
	}
}

// A note save sits behind the Foxhole page's gate. An outsider posts one
// and gets the no-access page, and the store holds what it held. A panel
// admin, who opens every page, posts the same form and it saves. A
// regression pin: the route sat behind that gate from the first note case;
// one behind the session gate alone fails here.
func TestNoteSaveByAnOutsiderIsRefusedAndAPanelAdminsSaves(t *testing.T) {
	w := newFoxholeWorld(t)
	outsider := newBrowser(t, w.p)
	signInAs(t, w.forum, outsider, addOutsider(w.forum))
	form := noteSaveForm(memberDoe.ID, "", "discharged 12 Sep")
	before := readNoteState(t, w.st)

	res := outsider.postForm(foxholeNotesPath, form)

	assertNoAccessPage(t, parseHTML(t, follow(t, outsider, res)))
	if after := readNoteState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the refused save = %+v, want it as before, %+v", after, before)
	}
	admin := newBrowser(t, w.p)
	signIn(t, w.forum, admin)
	assertRedirect(t, admin.postForm(foxholeNotesPath, form), foxholePath)
	if got := readNoteState(t, w.st).Records; len(got) != 1 || got[0].Note != "discharged 12 Sep" {
		t.Errorf("records after the panel admin's save = %+v, want %s's note", got, memberDoe.ID)
	}
}

// A Foxhole page whose store read fails shows the could-not-load page with
// a 503, still signed in, and reports the failure to Sentry once.
func TestFoxholePageWhoseStoreReadFailsShowsTheCouldNotLoadPage(t *testing.T) {
	t.Setenv("FOXHOLE_ROLE_BASE_NAME", "")
	t.Setenv("WARDEN_ROLE_BASE_NAME", "")
	w, st := newCtxWorld(t)
	w.discord.addRoles(foxholeGuildRoles...)
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{memberDoe, memberKestrel}})
	signInAs(t, w.forum, w.b, addFoxholeManager(w.forum))
	reported := recordSentry(t)
	st.failRead("ListFoxholeRecords")

	res := w.b.get(foxholePath)

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if got := failureOf(t, doc); got != "read-failed" {
		t.Errorf("failure page = %q, want read-failed", got)
	}
	if name := findElement(doc, "", "data-field", "username"); name == nil || textOf(name) != "Smith.F" {
		t.Error("the page shows no signed-in Smith.F, want the session kept")
	}
	if n := len(reported.recorded()); n != 1 {
		t.Errorf("sent %d Sentry events, want 1", n)
	}
}

// A note save that changes nothing writes nothing: the note form posted
// with the note it loaded lands back on the list, and the change log gains
// no entry with the same text before and after.
func TestNoteSaveThatChangesNothingWritesNoEntry(t *testing.T) {
	w := newFoxholeWorld(t)
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
	before := readNoteState(t, w.st)

	res := submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep")

	assertRedirect(t, res, foxholePath)
	if after := readNoteState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the unchanged save = %+v, want it as before, %+v", after, before)
	}
}

// The name refresh is bookkeeping for a later load: a page load whose
// refresh write fails still shows the holder list with the notes, and
// reports the failure to Sentry once.
func TestFoxholePageWhoseNameRefreshFailsStillShowsTheHolderList(t *testing.T) {
	t.Setenv("FOXHOLE_ROLE_BASE_NAME", "")
	t.Setenv("WARDEN_ROLE_BASE_NAME", "")
	w, st := newCtxWorld(t)
	w.discord.addRoles(foxholeGuildRoles...)
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{memberDoe, memberKestrel}})
	signInAs(t, w.forum, w.b, addFoxholeManager(w.forum))
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
	renamed := memberDoe
	renamed.Nick = "CPL Doe.J"
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{renamed, memberKestrel}})
	reported := recordSentry(t)
	st.failRead("SetFoxholeRecordNames")

	res := w.b.get(foxholePath)

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if got := noteOf(t, parseHTML(t, res), memberDoe.ID); got != "discharged 12 Sep" {
		t.Errorf("%s's row shows the note %q, want the one saved", memberDoe.ID, got)
	}
	if n := len(reported.recorded()); n != 1 {
		t.Errorf("sent %d Sentry events, want 1", n)
	}
}
