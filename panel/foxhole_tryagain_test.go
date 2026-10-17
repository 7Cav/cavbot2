package panel

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// newFailingFoxholeWorld is newFoxholeWorld over a ctxStore, so the test
// can make the Foxhole page's store reads fail, and that store.
func newFailingFoxholeWorld(t *testing.T) (*testWorld, *ctxStore) {
	t.Helper()
	var st *ctxStore
	w := newFoxholeWorldWith(t, func(fake store.Store) store.Store {
		st = &ctxStore{Fake: fake.(*store.Fake)}
		return st
	})
	return w, st
}

// foxholePageRead is the store read the Foxhole page makes that no save
// and no Foxhole action start makes, so failing it fails only the page.
const foxholePageRead = "ListFoxholeChanges"

// followTryAgain checks res is the error page of a Foxhole page that
// failed to load, turns the failing read back on, and follows the page's
// Try again from the tab that shows it, the way a browser follows a link.
// It returns the page Try again lands on and the fragment its address ends
// with. Following it changes nothing: the store and Discord are as they
// were before the press.
func followTryAgain(t *testing.T, w *testWorld, st *ctxStore, res *http.Response) (*html.Node, string) {
	t.Helper()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the error page's 503", res.StatusCode)
	}
	tryAgain := retryOf(t, parseHTML(t, res))
	st.failRead("")
	before, writes := readNoteState(t, w.st), w.discord.roleChanges()

	doc, fragment := landing(t, w.b, tryAgain, w.b.get(tryAgain))

	if after := readNoteState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after Try again = %+v, want it as before, %+v", after, before)
	}
	if after := w.discord.roleChanges(); !slices.Equal(after, writes) {
		t.Errorf("Try again made the role changes %v, want none", after[len(writes):])
	}
	return doc, fragment
}

// shownView returns the search the page's search box holds and the filter
// whose link the page marks as the current one.
func shownView(t *testing.T, doc *html.Node) (query, filter string) {
	t.Helper()
	form := findElement(doc, "form", "data-field", "search")
	if form == nil {
		t.Fatal("the page has no search form")
	}
	query = attrOf(findElement(form, "input", "type", "search"), "value")
	eachLiveElement(doc, func(n *html.Node) {
		if name, ok := attrValue(n, "data-filter"); ok && n.Data == "a" && attrOf(n, "aria-current") == "page" {
			filter = name
		}
	})
	return query, filter
}

// landedAtTheTop checks a page opened at fragment opens at its top.
func landedAtTheTop(t *testing.T, _ *html.Node, fragment string) {
	t.Helper()
	if fragment != "" {
		t.Errorf("Try again opens the page at fragment %q, want its top", fragment)
	}
}

// Try again on the error page of a Foxhole page load that failed loads the
// same page afresh, by GET, from the tab that shows the error page: the
// confirmation, preview, form or list the load asked for, under the same
// search and filter, opened at that block's heading. The address the error
// page sits at is the failed load's own, so a Try again that names it with
// a fragment only scrolls the tab, and lands on the error page again.
func TestTryAgainAfterAFailedFoxholePageLoadRepeatsIt(t *testing.T) {
	cases := []struct {
		name string
		// open readies the world and returns the address whose load fails,
		// fragment and all, and the check of the page Try again lands on,
		// opened at fragment.
		open func(*testing.T, *testWorld) (string, func(t *testing.T, doc *html.Node, fragment string))
	}{
		{"a purge confirmation under a search and a filter", func(*testing.T, *testWorld) (string, func(*testing.T, *html.Node, string)) {
			return "/foxhole?q=kestrel&filter=external&purge=both#purge-confirm", func(t *testing.T, doc *html.Node, fragment string) {
				_, block := landedOnBlock(t, doc, fragment, "purge-confirm")
				if got := attrOf(block, "data-scope"); got != "both" {
					t.Errorf("the purge confirmation confirms %q, want both", got)
				}
				if query, filter := shownView(t, doc); query != "kestrel" || filter != "external" {
					t.Errorf("the page shows the search %q and the filter %q, want kestrel and external", query, filter)
				}
			}
		}},
		{"a remove preview the selection bar opened", func(*testing.T, *testWorld) (string, func(*testing.T, *html.Node, string)) {
			return "/foxhole?member=" + memberKestrel.ID + "&member=" + memberMarsh.ID + "&remove=external#remove-preview",
				func(t *testing.T, doc *html.Node, fragment string) {
					_, block := landedOnBlock(t, doc, fragment, "remove-preview")
					if got, want := namedIn(block), []string{memberKestrel.ID, memberMarsh.ID}; !slices.Equal(got, want) {
						t.Errorf("the remove preview names %v, want the selection %v", got, want)
					}
				}
		}},
		{"a Retry", func(t *testing.T, w *testWorld) (string, func(*testing.T, *html.Node, string)) {
			failedPurge(t, w)
			report := attrOf(reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "data-report")
			return "/foxhole?from=report&retry=" + report + "#retry", func(t *testing.T, doc *html.Node, fragment string) {
				_, block := landedOnBlock(t, doc, fragment, "retrying")
				if got := attrOf(block, "data-report"); got != report {
					t.Errorf("the page opens the Retry of report %q, want %q", got, report)
				}
			}
		}},
		{"a roster preview", func(t *testing.T, _ *testWorld) (string, func(*testing.T, *html.Node, string)) {
			serveRoster(t, trooperVance, trooperDoe)
			return "/foxhole?unit=D%2FACD#roster-preview", func(t *testing.T, doc *html.Node, fragment string) {
				_, block := landedOnBlock(t, doc, fragment, "roster-preview")
				if got := attrOf(block, "data-unit"); got != "D/ACD" {
					t.Errorf("the page opens the roster preview of %q, want D/ACD", got)
				}
			}
		}},
		{"a member's note form", func(*testing.T, *testWorld) (string, func(*testing.T, *html.Node, string)) {
			return "/foxhole?note=" + memberDoe.ID + "#note", func(t *testing.T, doc *html.Node, fragment string) {
				heading, _ := landedOnBlock(t, doc, fragment, "note-block")
				if got := textOf(heading); !strings.Contains(got, memberDoe.Username) {
					t.Errorf("the note block's heading reads %q, want it to name %s", got, memberDoe.Username)
				}
			}
		}},
		{"a change log entry older than the newest", func(t *testing.T, w *testWorld) (string, func(*testing.T, *html.Node, string)) {
			assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
			assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "allied group lead"), foxholePath)
			entries := entryElements(foxholeBlock(t, parseHTML(t, w.b.get(foxholePath)), "changes"))
			if len(entries) != 2 {
				t.Fatalf("the change log shows %d entries, want the two note saves", len(entries))
			}
			older := attrOf(entries[1], "data-entry")
			return "/foxhole?entry=" + older + "#entry-" + older, func(t *testing.T, doc *html.Node, fragment string) {
				landedOnEntry(t, doc, fragment, older)
			}
		}},
		{"the list after an Approve that skipped a member", func(t *testing.T, w *testWorld) (string, func(*testing.T, *html.Node, string)) {
			approveThatSkipsKestrel(t, w)
			return "/foxhole?skipped=" + memberKestrel.ID + "#holders", func(t *testing.T, doc *html.Node, fragment string) {
				_, block := landedOnBlock(t, doc, fragment, "holders")
				namedOnlyIn(t, doc, block, "data-skipped", memberKestrel.ID)
			}
		}},
		{"the no-role view after a note clear took a member off it", func(t *testing.T, w *testWorld) (string, func(*testing.T, *html.Node, string)) {
			assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "allied liaison, discharged 12 Sep"), foxholePath)
			setMembers(w, doeWithoutInternal(), memberAsh, memberKestrel, memberVance)
			clearNoRoleNoteOnDoe(t, w)
			return "/foxhole?cleared=" + memberDoe.ID + "&filter=no-role#no-role", func(t *testing.T, doc *html.Node, fragment string) {
				_, block := landedOnBlock(t, doc, fragment, "no-role")
				namedOnlyIn(t, doc, block, "data-cleared", memberDoe.ID)
			}
		}},
		{"a selection under a search and a filter", func(*testing.T, *testWorld) (string, func(*testing.T, *html.Node, string)) {
			return "/foxhole?filter=internal&member=" + memberAsh.ID + "&member=" + memberMarsh.ID + "&q=r#holders",
				func(t *testing.T, doc *html.Node, fragment string) {
					landedOnBlock(t, doc, fragment, "holders")
					if got, want := tickedRows(t, doc), []string{memberAsh.ID, memberMarsh.ID}; !slices.Equal(got, want) {
						t.Errorf("the holder list ticks %v, want the selection %v", got, want)
					}
				}
		}},
		{"a search and a filter", func(*testing.T, *testWorld) (string, func(*testing.T, *html.Node, string)) {
			return "/foxhole?filter=internal&q=r#holders", func(t *testing.T, doc *html.Node, fragment string) {
				landedOnBlock(t, doc, fragment, "holders")
			}
		}},
		{"the page with nothing open", func(*testing.T, *testWorld) (string, func(*testing.T, *html.Node, string)) {
			return foxholePath, landedAtTheTop
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, st := newFailingFoxholeWorld(t)
			address, landed := tc.open(t, w)
			st.failRead(foxholePageRead)

			doc, fragment := followTryAgain(t, w, st, w.b.get(address))

			landed(t, doc, fragment)
		})
	}
}

// landedOnBlockOf is the check that a page opened at fragment lands on the
// heading of the block whose data-field is field.
func landedOnBlockOf(field string) func(*testing.T, *html.Node, string) {
	return func(t *testing.T, doc *html.Node, fragment string) {
		t.Helper()
		landedOnBlock(t, doc, fragment, field)
	}
}

// Try again on the error page of a POST whose answer is the Foxhole page,
// such as a refused save or a preview, loads by GET the block the POST was
// made from: it never posts again. The pasted lines, the refusal and the
// typed note came in the posted form or the answer, so without script they
// don't come back, and the manager pastes again. An add's Try again names
// the role picked, as the add preview's address does. A roster add refused
// because the roster changed names its unit, as the roster preview's
// address does, so Try again opens that preview afresh.
func TestTryAgainAfterAFoxholePostWhosePageFailsOpensTheBlockItCameFrom(t *testing.T) {
	cases := []struct {
		name string
		// ready readies the world and returns the POST, made through the
		// page's own form the way a browser with no script makes it, and the
		// check of the page Try again lands on, opened at fragment.
		ready func(*testing.T, *testWorld) (func() *http.Response, func(t *testing.T, doc *html.Node, fragment string))
	}{
		{"a refused note save under a search and a filter", func(t *testing.T, w *testWorld) (func() *http.Response, func(*testing.T, *html.Node, string)) {
			link := editNoteLink(t, listUnderASearchAndAFilter(t, w), memberAsh.ID)
			assertRedirect(t, submitNote(t, w.b, parseHTML(t, w.b.get(link)), "allied liaison"), foxholePath)
			// Saving the same note again changes nothing, so it is refused.
			page := parseHTML(t, w.b.get(link))
			return func() *http.Response { return submitNote(t, w.b, page, "allied liaison") }, func(t *testing.T, doc *html.Node, fragment string) {
				heading, _ := landedOnBlock(t, doc, fragment, "note-block")
				if got := textOf(heading); !strings.Contains(got, memberAsh.Username) {
					t.Errorf("the note block's heading reads %q, want it to name %s", got, memberAsh.Username)
				}
				if query, filter := shownView(t, doc); query != "r" || filter != "internal" {
					t.Errorf("the page shows the search %q and the filter %q, want r and internal", query, filter)
				}
			}
		}},
		{"a refused Approve under a search and a filter", func(t *testing.T, w *testWorld) (func() *http.Response, func(*testing.T, *html.Node, string)) {
			// Ash is approved for the one Foxhole role they hold, so a second
			// Approve changes nobody.
			approveThroughThePage(t, w, memberAsh.ID)
			list := listUnderASearchAndAFilter(t, w)
			return func() *http.Response { return submitSelection(t, w.b, list, opApprove, memberAsh.ID) }, func(t *testing.T, doc *html.Node, fragment string) {
				landedOnBlock(t, doc, fragment, "holders")
				if got, want := tickedRows(t, doc), []string{memberAsh.ID}; !slices.Equal(got, want) {
					t.Errorf("the holder list ticks %v, want the selection %v", got, want)
				}
			}
		}},
		{"a refused Approve with nobody ticked", func(t *testing.T, w *testWorld) (func() *http.Response, func(*testing.T, *html.Node, string)) {
			// Only a browser without the page script sends it.
			page := parseHTML(t, w.b.get(foxholePath))
			return func() *http.Response { return submitSelection(t, w.b, page, opApprove) }, landedOnBlockOf("holders")
		}},
		{"a refused Confirm on a remove preview the selection bar opened", func(t *testing.T, w *testWorld) (func() *http.Response, func(*testing.T, *html.Node, string)) {
			preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))
			// Kestrel loses External before the Confirm, so it removes nobody.
			withoutFoxholeRoles(w)
			return func() *http.Response { return confirmRemoval(t, w.b, preview) }, func(t *testing.T, doc *html.Node, fragment string) {
				_, block := landedOnBlock(t, doc, fragment, "remove-preview")
				if got, want := namedIn(block), []string{memberKestrel.ID}; !slices.Equal(got, want) {
					t.Errorf("the remove preview names %v, want the selection %v", got, want)
				}
			}
		}},
		{"the Add members block's Preview", func(t *testing.T, w *testWorld) (func() *http.Response, func(*testing.T, *html.Node, string)) {
			page := parseHTML(t, w.b.get(foxholePath))
			return func() *http.Response { return previewPaste(t, w.b, page, addForm(t, page), "external", "jdoe") }, landedOnBlockOf("add-members")
		}},
		{"a refused add", func(t *testing.T, w *testWorld) (func() *http.Response, func(*testing.T, *html.Node, string)) {
			page := parseHTML(t, w.b.get(foxholePath))
			preview := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "jdoe"))
			// Doe gets External before the Confirm, so it adds nobody.
			doe := memberDoe
			doe.RoleIDs = []string{roleSGT, roleInternal, roleExternal}
			setMembers(w, doe, memberAsh, memberKestrel, memberMarsh, memberVance)
			return func() *http.Response { return confirmAdd(t, w.b, preview, nil) }, landedOnBlockOf("add-members")
		}},
		{"a roster add refused because the roster changed", func(t *testing.T, w *testWorld) (func() *http.Response, func(*testing.T, *html.Node, string)) {
			api := serveRoster(t, trooperVance)
			preview := parseHTML(t, previewRoster(t, w, "D/ACD"))
			api.setTroopers(trooperVance, trooper("Kestrel.T", memberKestrel.ID))
			return func() *http.Response { return confirmRoster(t, w.b, preview) }, landedOnBlockOf("roster-preview")
		}},
		{"a refused purge", func(t *testing.T, w *testWorld) (func() *http.Response, func(*testing.T, *html.Node, string)) {
			confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external")))
			// Kestrel and Marsh lose External before the Confirm, so it
			// purges nobody.
			kestrel, marsh := memberKestrel, memberMarsh
			kestrel.RoleIDs, marsh.RoleIDs = nil, []string{roleInternal, roleCPT}
			setMembers(w, memberDoe, memberAsh, kestrel, marsh, memberVance)
			return func() *http.Response { return confirmPurge(t, w.b, context.Background(), confirm) }, landedAtTheTop
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, st := newFailingFoxholeWorld(t)
			post, landed := tc.ready(t, w)
			st.failRead(foxholePageRead)

			doc, fragment := followTryAgain(t, w, st, post())

			landed(t, doc, fragment)
		})
	}
}
