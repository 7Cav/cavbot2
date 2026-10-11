package panel

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// The Foxhole page shows whatever is pending first: a preview or
// confirmation a Foxhole action waits on opens above the last report.

// stoppedPurge is a world whose last report is a purge of both roles that
// a manager stopped partway. It set out to make six changes, External
// first: the two External holders' and the four Internal holders', Vance
// holding Internal too. Discord refused Kestrel's, and Stop came while the
// fourth change was in flight, so the report counts 3 changed, 1 failed, 0
// skipped and 2 not attempted, and offers Retry.
func stoppedPurge(t *testing.T) *testWorld {
	t.Helper()
	w := newFoxholeWorld(t)
	vance := memberVance
	vance.RoleIDs = append(slices.Clone(memberVance.RoleIDs), roleInternal)
	setMembers(w, memberDoe, memberAsh, memberKestrel, memberMarsh, vance)
	w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "both")
	for range 3 {
		hold.next(t)
		hold.pass(t)
	}
	hold.next(t)
	stopHeld(t, w, hold)
	return w
}

// pendingOpener is a control on the Foxhole page that opens a preview or a
// confirmation, and the data-field hooks of the blocks it opens.
type pendingOpener struct {
	name string
	// open presses the control on doc the way a manager does, and returns
	// the address the control names and the answer.
	open   func(t *testing.T, w *testWorld, doc *html.Node) (control string, res *http.Response)
	blocks []string
}

// pendingOpeners are the five controls that open what a Foxhole action
// waits on, run against stoppedPurge's world.
var pendingOpeners = []pendingOpener{
	{"purge", func(t *testing.T, w *testWorld, doc *html.Node) (string, *http.Response) {
		action := attrOf(purgeForm(t, doc), "action")
		return action, w.b.getForm(action, url.Values{paramPurge: {"both"}})
	}, []string{"purge-confirm"}},
	{"remove", func(t *testing.T, w *testWorld, doc *html.Node) (string, *http.Response) {
		control := attrOf(removeButton(t, doc, "internal"), "formaction")
		return control, submitSelection(t, w.b, doc, "internal", memberDoe.ID)
	}, []string{"remove-preview"}},
	{"add", func(t *testing.T, w *testWorld, doc *html.Node) (string, *http.Response) {
		form := addForm(t, doc)
		return attrOf(form, "action"), previewPaste(t, w.b, doc, form, "external", "jdoe")
	}, []string{"add-preview"}},
	{"roster add", func(t *testing.T, w *testWorld, doc *html.Node) (string, *http.Response) {
		serveRoster(t, trooperVance, trooperDoe)
		form := findElement(rosterBlock(t, doc), "form", "data-field", "roster-form")
		if form == nil {
			t.Fatal("the Add a unit's roster block has no form")
		}
		fields := formFields(doc, form)
		fields.Set(fieldUnit, "D/ACD")
		action := attrOf(form, "action")
		return action, w.b.postForm(action, fields)
	}, []string{"roster-preview"}},
	{"retry", func(t *testing.T, w *testWorld, doc *html.Node) (string, *http.Response) {
		link := retryLink(reportBlock(t, doc))
		if link == nil {
			t.Fatal("the report offers no Retry")
		}
		href := attrOf(link, "href")
		return href, w.b.get(href)
	}, []string{"retrying", "retry-confirm"}},
}

// Each preview or confirmation opens above the last report, so a manager
// sees what waits on them without scrolling past last war's report.
func TestAPreviewOpensAboveTheLastReport(t *testing.T) {
	for _, opener := range pendingOpeners {
		t.Run(opener.name, func(t *testing.T) {
			w := stoppedPurge(t)

			_, res := opener.open(t, w, parseHTML(t, w.b.get(foxholePath)))

			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			doc := parseHTML(t, res)
			report := reportBlock(t, doc)
			for _, field := range opener.blocks {
				block := findElement(doc, "", "data-field", field)
				if block == nil {
					t.Fatalf("the page has no %s", field)
				}
				if !precedes(doc, block, report) {
					t.Errorf("the %s comes after the last report, want it above", field)
				}
			}
		})
	}
}

// foldedSummary returns a report's summary line when the report is folded
// to it, a closed <details>, and nil when the report shows in full.
func foldedSummary(report *html.Node) *html.Node {
	if _, open := attrValue(report, "open"); report.Data != "details" || open {
		return nil
	}
	return findElement(report, "summary", "", "")
}

// While a preview is open, the last report folds to one line naming the
// action, how it ended, and how many members it changed, skipped, failed
// on and never attempted. With nothing open it shows in full.
func TestTheLastReportFoldsToOneLineWhileAPreviewIsOpen(t *testing.T) {
	w := stoppedPurge(t)
	page := parseHTML(t, w.b.get(foxholePath))
	if foldedSummary(reportBlock(t, page)) != nil {
		t.Error("with nothing open the report is folded, want it in full")
	}

	summary := foldedSummary(reportBlock(t, parseHTML(t, openPurge(t, w.b, page, "external"))))

	if summary == nil {
		t.Fatal("with the purge confirmation open the report shows in full, want it folded")
	}
	if got := fieldText(t, summary, "stopped-by"); got != managerUsername {
		t.Errorf("the folded report says it was stopped by %q, want %q", got, managerUsername)
	}
	if name := commands.FoxholeActionName(store.ChangePurge, commands.ActionReport{Scope: commands.PurgeBoth}); !strings.Contains(textOf(summary), name) {
		t.Errorf("the folded report reads %q, want it to name the action, %q", textOf(summary), name)
	}
	counts := map[string]string{}
	eachElement(summary, func(n *html.Node) {
		if list, ok := attrValue(n, "data-count"); ok {
			counts[list] = textOf(n)
		}
	})
	if want := map[string]string{"changed": "3", "failed": "1", "skipped": "0", "not-attempted": "2"}; !maps.Equal(counts, want) {
		t.Errorf("the folded report counts %v, want %v", counts, want)
	}
}

// The address each preview's control opens names the preview in its
// fragment, so the browser lands on the block the manager asked for, with
// or without the page script. A Retry lands on the block that says what it
// retries, above the confirmation it opens.
func TestOpeningAPreviewLandsOnIt(t *testing.T) {
	for _, opener := range pendingOpeners {
		t.Run(opener.name, func(t *testing.T) {
			w := stoppedPurge(t)
			control, res := opener.open(t, w, parseHTML(t, w.b.get(foxholePath)))

			doc, fragment := landing(t, w.b, control, res)

			target := landedOn(doc, fragment)
			if target == nil {
				t.Fatalf("the page opens at fragment %q, which names no block, want the %s", fragment, opener.blocks[0])
			}
			if got := attrOf(target, "data-field"); got != opener.blocks[0] {
				t.Errorf("the page lands on %q, want the %s", got, opener.blocks[0])
			}
		})
	}
}

// live reports whether a button takes a press: it carries no disabled.
func live(button *html.Node) bool {
	_, disabled := attrValue(button, "disabled")
	return !disabled
}

// purgeButton returns the After a war block's Purge, the purge form's
// button, and fails the test when the form has none.
func purgeButton(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	button := findElement(purgeForm(t, doc), "button", "", "")
	if button == nil {
		t.Fatal("the purge form has no button")
	}
	return button
}

// While the purge confirmation is open, its Confirm is the one live Purge
// on the page: the After a war block's own Purge is off. With nothing open
// and nothing running, the block's Purge is live.
func TestOpenPurgeConfirmationLeavesNoSecondLivePurge(t *testing.T) {
	w := newFoxholeWorld(t)
	page := parseHTML(t, w.b.get(foxholePath))
	if !live(purgeButton(t, page)) {
		t.Error("with nothing open the After a war block's Purge is off, want it live")
	}

	doc := parseHTML(t, openPurge(t, w.b, page, "both"))

	if live(purgeButton(t, doc)) {
		t.Error("with the purge confirmation open the After a war block's Purge is live, want it off")
	}
	if confirm := findElement(purgeConfirmation(t, doc), "", "data-field", "confirm"); confirm == nil || !live(confirm) {
		t.Error("the purge confirmation's Confirm is missing or off, want it live")
	}
}
