package panel

import (
	"net/http"
	"net/url"
	"slices"
	"testing"

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
