package panel

import (
	"fmt"
	"net/url"
	"slices"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// Cancel on a preview an action block opened lands on that block's
// heading. The action blocks sit below the latest report, and under
// 1100px they stack in one column, so the page's top is far from the block
// the manager came from. That holds with and without anything to confirm.
func TestCancelOnAnActionBlocksPreviewLandsOnTheBlock(t *testing.T) {
	cases := []struct {
		name string
		// open opens the preview as a browser with no script does, and
		// returns the page it opens.
		open func(*testing.T, *testWorld) *html.Node
		// preview is the preview's data-field marker, and block its action
		// block's.
		preview, block string
		// confirm is the preview offering a Confirm: something to confirm.
		confirm bool
	}{
		{"purge confirmation", func(t *testing.T, w *testWorld) *html.Node {
			return parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external"))
		}, "purge-confirm", "after-a-war", true},
		{"purge confirmation with nobody to purge", func(t *testing.T, w *testWorld) *html.Node {
			setMembers(w, memberVance)
			return parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "both"))
		}, "purge-confirm", "after-a-war", false},
		{"add preview", func(t *testing.T, w *testWorld) *html.Node {
			page := parseHTML(t, w.b.get(foxholePath))
			return parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "jdoe"))
		}, "add-preview", "add-members", true},
		{"add preview with nothing to add", func(t *testing.T, w *testWorld) *html.Node {
			// Kestrel holds External already.
			page := parseHTML(t, w.b.get(foxholePath))
			return parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "kestrel_tlr"))
		}, "add-preview", "add-members", false},
		{"roster preview", func(t *testing.T, w *testWorld) *html.Node {
			serveRoster(t, trooperVance, trooperDoe)
			return parseHTML(t, previewRoster(t, w, "D/ACD"))
		}, "roster-preview", "add-roster", true},
		{"roster preview with nobody to add", func(t *testing.T, w *testWorld) *html.Node {
			// Doe holds Internal already.
			serveRoster(t, trooperDoe)
			return parseHTML(t, previewRoster(t, w, "D/ACD"))
		}, "roster-preview", "add-roster", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			preview := pendingBlock(t, tc.open(t, w), tc.preview)
			if got := findElement(preview, "", "data-field", "confirm") != nil; got != tc.confirm {
				t.Fatalf("the preview offers a Confirm: %v, want %v", got, tc.confirm)
			}
			cancel := cancelAddress(t, preview)

			doc, fragment := landing(t, w.b, cancel, w.b.get(cancel))

			landedOnBlock(t, doc, fragment, tc.block)
		})
	}
}

// failedAdd runs an add of External to Doe to its end with Discord
// refusing Doe's change, so its report offers Retry of Doe, who still
// lacks External.
func failedAdd(t *testing.T, w *testWorld) {
	t.Helper()
	w.discord.failRoleWrites(memberDoe.ID, missingPermissions())
	page := parseHTML(t, w.b.get(foxholePath))
	preview := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "jdoe"))
	assertRedirect(t, confirmAdd(t, w.b, preview, nil), foxholePath)
	w.awaitActionEnd(t)
}

// loggedEntry returns the change log entry with the ID given, and fails
// the test when the change log shows none.
func loggedEntry(t *testing.T, doc *html.Node, id string) *html.Node {
	t.Helper()
	for _, entry := range entryElements(foxholeBlock(t, doc, "changes")) {
		if attrOf(entry, "data-entry") == id {
			return entry
		}
	}
	t.Fatalf("the change log shows no entry %s", id)
	return nil
}

// retryFromItsEntry makes the latest report's change log entry older than
// the newest with a note save, then follows the Retry in that entry, the
// way a browser follows the link. It returns the page the Retry opens and
// the report's ID.
func retryFromItsEntry(t *testing.T, w *testWorld) (page *html.Node, report string) {
	t.Helper()
	report = attrOf(reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "data-report")
	seedNote(t, w, namesOf(memberMarsh), "keeps External through the next war")
	link := retryLink(loggedEntry(t, parseHTML(t, w.b.get(foxholePath)), report))
	if link == nil {
		t.Fatal("the report's change log entry offers no Retry")
	}
	return parseHTML(t, w.b.get(attrOf(link, "href"))), report
}

// landedOnEntry checks a page opened at fragment lands on the summary line
// of the change log entry with the ID given, which is open, and that the
// newest entry is open too.
func landedOnEntry(t *testing.T, doc *html.Node, fragment, entry string) {
	t.Helper()
	summary := landedOn(doc, fragment)
	if summary == nil || summary.Data != "summary" || attrOf(summary.Parent, "data-entry") != entry {
		t.Fatalf("the page opens at fragment %q, want the summary line of change log entry %s", fragment, entry)
	}
	if _, open := attrValue(summary.Parent, "open"); !open {
		t.Errorf("change log entry %s is folded, want it open", entry)
	}
	if _, open := attrValue(entryElements(foxholeBlock(t, doc, "changes"))[0], "open"); !open {
		t.Error("the newest change log entry is folded, want it open")
	}
}

// Cancel on a Retry's confirmation or preview lands where the Retry was
// pressed. Pressed in a change log entry, Cancel lands on that entry's
// summary line with the entry open, and the newest entry stays open. The
// entry here is the latest report's own, made older than the newest by a
// note save, so a Cancel that forgot where the Retry was pressed would land
// on the latest report instead. That holds for every confirmation and
// preview a Retry opens, and for the Cancel shown when nobody is left to
// change.
func TestCancelOnARetryPressedInAChangeLogEntryLandsOnTheEntry(t *testing.T) {
	cases := []struct {
		name string
		// fail runs an action to its end with a member it failed on, so its
		// report offers Retry.
		fail func(*testing.T, *testWorld)
		// since changes the guild once the action has ended, nil for
		// nothing.
		since func(*testWorld)
		// preview is the data-field marker of what the Retry opens.
		preview string
		// confirm is the preview offering a Confirm: someone left to change.
		confirm bool
	}{
		{"purge Retry confirmation", failedPurge, nil, "retry-confirm", true},
		{"purge Retry confirmation with nobody left to purge", failedPurge, withoutFoxholeRoles, "retry-confirm", false},
		{"add preview", failedAdd, nil, "add-preview", true},
		{"roster preview", failedRosterAdd, nil, "roster-preview", true},
		{"remove preview", failedRemoval, nil, "remove-preview", true},
		{"remove preview with nobody left to remove", failedRemoval, func(w *testWorld) {
			ash := memberAsh
			ash.RoleIDs = nil
			setMembers(w, memberDoe, ash, memberKestrel, memberMarsh, memberVance)
		}, "remove-preview", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			tc.fail(t, w)
			if tc.since != nil {
				tc.since(w)
			}
			page, report := retryFromItsEntry(t, w)
			preview := pendingBlock(t, page, tc.preview)
			if got := findElement(preview, "", "data-field", "confirm") != nil; got != tc.confirm {
				t.Fatalf("the preview offers a Confirm: %v, want %v", got, tc.confirm)
			}
			cancel := cancelAddress(t, preview)

			doc, fragment := landing(t, w.b, cancel, w.b.get(cancel))

			landedOnEntry(t, doc, fragment, report)
		})
	}
}

// cancelOfAPurgeRetry runs a purge of External that fails on Kestrel, then
// opens the Retry its latest report offers, the way a browser follows the
// link. It returns the address of the Retry confirmation's Cancel and the
// report's ID. Another action then starts in a later step, before the
// manager presses Cancel.
func cancelOfAPurgeRetry(t *testing.T, w *testWorld) (cancel, report string) {
	t.Helper()
	failedPurge(t, w)
	report = attrOf(reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "data-report")
	return cancelAddress(t, pendingBlock(t, openRetry(t, w.b), "retry-confirm")), report
}

// A Retry pressed in the latest report cancels to that report's change log
// entry when another action has started since, so the latest report is a
// different one by the time the manager presses Cancel.
func TestCancelOnARetryWhoseReportIsNoLongerLatestLandsOnItsEntry(t *testing.T) {
	w := newFoxholeWorld(t)
	cancel, report := cancelOfAPurgeRetry(t, w)
	startPurge(t, w, "internal")
	w.awaitActionEnd(t)

	doc, fragment := landing(t, w.b, cancel, w.b.get(cancel))

	landedOnEntry(t, doc, fragment, report)
}

// Once the report's entry has also dropped off the change log, which shows
// only the newest changes, Cancel on its Retry lands on the Change log's
// heading.
func TestCancelOnARetryWhoseEntryLeftTheChangeLogLandsOnTheChangeLog(t *testing.T) {
	w := newFoxholeWorld(t)
	cancel, report := cancelOfAPurgeRetry(t, w)
	startPurge(t, w, "internal")
	w.awaitActionEnd(t)
	// A note save on each of as many members as the change log shows.
	for i := range changeLogLimit {
		seedNote(t, w, store.MemberNames{MemberID: fmt.Sprintf("2%017d", i), DisplayName: fmt.Sprintf("Member %d", i),
			Username: fmt.Sprintf("member_%d", i)}, "left before the war")
	}
	if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "purge"); slices.ContainsFunc(entries, func(e *html.Node) bool {
		return attrOf(e, "data-entry") == report
	}) {
		t.Fatal("the change log still shows the report's entry")
	}

	doc, fragment := landing(t, w.b, cancel, w.b.get(cancel))

	landedOnBlock(t, doc, fragment, "changes")
}

// A Retry address that doesn't say where the Retry was pressed, such as
// one kept in the browser's history from before Retry said so, cancels as
// if pressed in the latest report.
func TestCancelOnARetryWhoseAddressDoesntSayWhereLandsOnTheLatestReport(t *testing.T) {
	w := newFoxholeWorld(t)
	failedPurge(t, w)
	retry, err := url.Parse(attrOf(retryLink(reportBlock(t, parseHTML(t, w.b.get(foxholePath)))), "href"))
	if err != nil {
		t.Fatalf("Retry address: %v", err)
	}
	q := retry.Query()
	q.Del(paramRetryFrom)
	retry.RawQuery = q.Encode()
	cancel := cancelAddress(t, pendingBlock(t, parseHTML(t, w.b.get(retry.String())), "retry-confirm"))

	doc, fragment := landing(t, w.b, cancel, w.b.get(cancel))

	landedOnBlock(t, doc, fragment, "report")
}
