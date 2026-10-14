package panel

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// While a Foxhole action runs, from the page or from a role-changing
// /foxhole command, every control on the Foxhole page it turns off says why
// in words, and every preview it turns off says how to get a working
// Confirm once the action ends (issue #582). These tests hold a /foxhole
// add mid-run, as holdFoxholeAdd does, and open each preview from a page
// loaded before it started, as a manager with the page open in another tab
// does.

// busyPreview is a preview or confirmation whose Confirm a running Foxhole
// action turns off.
type busyPreview struct {
	name string
	// ready readies the world before the action starts, as a Retry needs
	// its report. Nil for nothing to ready.
	ready func(t *testing.T, w *testWorld)
	// page is the address of the Foxhole page loaded before the action
	// started, empty for the page with nothing open.
	page string
	// open presses the control on before, the page loaded before the
	// action started, that opens the preview, and returns the page it
	// opens.
	open func(t *testing.T, w *testWorld, before *html.Node) *html.Node
	// block is the preview's data-field marker.
	block string
}

// The previews whose Confirm a running Foxhole action turns off, each
// opened the way a manager opens it.
var (
	purgeBusy = busyPreview{name: "purge confirmation", block: "purge-confirm", open: func(t *testing.T, w *testWorld, before *html.Node) *html.Node {
		return parseHTML(t, openPurge(t, w.b, before, "external"))
	}}
	removeBusy = busyPreview{name: "remove preview", block: "remove-preview", open: func(t *testing.T, w *testWorld, before *html.Node) *html.Node {
		return parseHTML(t, submitSelection(t, w.b, before, "internal", memberAsh.ID, memberKestrel.ID))
	}}
	retryBusy = busyPreview{name: "Retry confirmation", block: "retry-confirm", ready: failedPurge, open: openRetryOf}
	addBusy   = busyPreview{name: "add preview", block: "add-preview", open: func(t *testing.T, w *testWorld, before *html.Node) *html.Node {
		// Doe doesn't hold External, so the add gives it to them.
		return parseHTML(t, previewPaste(t, w.b, before, addForm(t, before), "external", "jdoe"))
	}}
	rosterBusy = busyPreview{name: "roster preview", block: "roster-preview",
		ready: func(t *testing.T, _ *testWorld) { serveRoster(t, trooperVance, trooperDoe) },
		open: func(t *testing.T, w *testWorld, _ *html.Node) *html.Node {
			return parseHTML(t, previewRoster(t, w, "D/ACD"))
		}}
)

// busyPreviews are every preview with a Confirm a running Foxhole action
// turns off.
var busyPreviews = []busyPreview{purgeBusy, removeBusy, retryBusy, addBusy, rosterBusy}

// failedPurge runs a purge of External to its end with Discord refusing
// Kestrel's change, so its report offers Retry of Kestrel, who still holds
// External.
func failedPurge(t *testing.T, w *testWorld) {
	t.Helper()
	w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
	startPurge(t, w, "external")
	w.awaitActionEnd(t)
}

// openRetryOf follows the Retry of the report on page, and returns the
// page it opens.
func openRetryOf(t *testing.T, w *testWorld, page *html.Node) *html.Node {
	t.Helper()
	link := retryLink(reportBlock(t, page))
	if link == nil {
		t.Fatal("the report offers no Retry")
	}
	return parseHTML(t, w.b.get(attrOf(link, "href")))
}

// openBusy readies the world for the preview, loads the Foxhole page at the
// preview's page address, holds a /foxhole add mid-run, then opens the
// preview from the page loaded before it. It returns the page the preview
// opens on and the preview's block. The command runs until held.release.
func openBusy(t *testing.T, w *testWorld, p busyPreview) (doc, block *html.Node, held heldCommand) {
	t.Helper()
	if p.ready != nil {
		p.ready(t, w)
	}
	before := parseHTML(t, w.b.get(cmp.Or(p.page, foxholePath)))
	held = holdFoxholeAdd(t, w)
	doc = p.open(t, w, before)
	return doc, pendingBlock(t, doc, p.block), held
}

// pendingBlock returns the page's preview or confirmation with the
// data-field marker given, and fails the test when the page has none.
func pendingBlock(t *testing.T, doc *html.Node, field string) *html.Node {
	t.Helper()
	block := findElement(doc, "", "data-field", field)
	if block == nil {
		t.Fatalf("the page has no %s", field)
	}
	return block
}

// While a Foxhole action runs, each preview's Confirm is off, and the
// preview says why inside its own block, not only in the progress or
// command block further down the page.
func TestABusyPreviewSaysWhyItsConfirmIsOff(t *testing.T) {
	for _, p := range busyPreviews {
		t.Run(p.name, func(t *testing.T) {
			w := newFoxholeWorld(t)

			_, block, _ := openBusy(t, w, p)

			if button := findElement(block, "", "data-field", "confirm"); button == nil || !disabled(button) {
				t.Error("the Confirm is missing or on while a command runs, want it shown and off")
			}
			if findElement(block, "", "data-field", "busy") == nil {
				t.Error("nothing in the block says another Foxhole action is running")
			}
		})
	}
}

// busyReopen is a preview opened while a Foxhole action runs, and what
// makes it the same preview when its reopen control opens it again.
type busyReopen struct {
	busyPreview
	// same reads what the preview on doc confirms: its scope, role and
	// members, report or unit, as markers on the page.
	same func(t *testing.T, doc *html.Node) []string
	// reopen presses the preview's reopen control and returns the page it
	// opens. Nil presses the block's control marked reopen.
	reopen func(t *testing.T, w *testWorld, doc, block *html.Node) *html.Node
}

// pressReopen presses the control in a busy preview's block that opens the
// preview again, and returns the page the browser lands on.
func pressReopen(t *testing.T, w *testWorld, doc, block *html.Node) *html.Node {
	t.Helper()
	page, _ := landReopen(t, w, doc, block)
	return page
}

// landReopen presses the control in a busy preview's block that opens the
// preview again, link or button, the way a browser does with no script,
// and returns the page it lands on and the fragment its address ends with.
func landReopen(t *testing.T, w *testWorld, doc, block *html.Node) (*html.Node, string) {
	t.Helper()
	control := findElement(block, "", "data-field", "reopen")
	if control == nil {
		t.Fatal("the block offers no control to open it again")
	}
	target, res := press(t, w.b, doc, control)
	return landing(t, w.b, target, res)
}

// press presses a link or a form's button the way a browser does with no
// script: it follows the link, or submits the form that owns the button,
// with the button's own formaction and formmethod where it has them. It
// returns the address the press sent the browser to and the answer.
func press(t *testing.T, b *browser, doc, control *html.Node) (string, *http.Response) {
	t.Helper()
	switch control.Data {
	case "a":
		href := attrOf(control, "href")
		return href, b.get(href)
	case "button":
		var form *html.Node
		eachElement(doc, func(n *html.Node) {
			if form == nil && n.Data == "form" && ownedBy(control, n) {
				form = n
			}
		})
		if form == nil {
			t.Fatal("the button belongs to no form")
		}
		fields := formFields(doc, form)
		if name := attrOf(control, "name"); name != "" {
			fields.Set(name, attrOf(control, "value"))
		}
		action := cmp.Or(attrOf(control, "formaction"), attrOf(form, "action"))
		if strings.EqualFold(cmp.Or(attrOf(control, "formmethod"), attrOf(form, "method")), http.MethodPost) {
			return action, b.postForm(action, fields)
		}
		return action, b.getForm(action, fields)
	}
	t.Fatalf("the control is a <%s>, no link or button", control.Data)
	return "", nil
}

// markersOf reads the attribute key of the block with the data-field
// marker given, failing the test when the page has no such block.
func markersOf(t *testing.T, doc *html.Node, field string, keys ...string) []string {
	t.Helper()
	block := pendingBlock(t, doc, field)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+attrOf(block, key))
	}
	return out
}

// busyLinks are the previews whose busy sentence carries the control that
// opens them again: each preview with a Confirm a running action turned
// off, opened as a manager opens it, and as a Retry does where that gives
// the preview another address.
var busyLinks = []busyReopen{
	{busyPreview: purgeBusy, same: func(t *testing.T, doc *html.Node) []string {
		return markersOf(t, doc, "purge-confirm", "data-scope")
	}},
	{busyPreview: busyPreview{name: "remove preview from a searched and filtered view", block: "remove-preview",
		page: "/foxhole?filter=internal&q=a",
		open: func(t *testing.T, w *testWorld, before *html.Node) *html.Node {
			// Marsh holds External and Ash doesn't, so the preview lists one
			// member losing it and one skipped.
			return parseHTML(t, submitSelection(t, w.b, before, "external", memberAsh.ID, memberMarsh.ID))
		}}, same: removeConfirms},
	{busyPreview: busyPreview{name: "remove preview a removal's Retry opened", block: "remove-preview", ready: failedRemoval, open: openRetryOf},
		same: func(t *testing.T, doc *html.Node) []string {
			return append(markersOf(t, doc, "retrying", "data-report"), removeConfirms(t, doc)...)
		}},
	{busyPreview: retryBusy, same: func(t *testing.T, doc *html.Node) []string {
		return markersOf(t, doc, "retry-confirm", "data-report", "data-action")
	}},
	{busyPreview: busyPreview{name: "roster preview a roster add's Retry opened", block: "roster-preview", ready: failedRosterAdd, open: openRetryOf},
		same: func(t *testing.T, doc *html.Node) []string {
			return append(markersOf(t, doc, "retrying", "data-report"), markersOf(t, doc, "roster-preview", "data-unit")...)
		}},
}

// busyReopens are the ways back to a working Confirm: the busy sentence's
// control, and the Preview again of the add preview and the whole roster's
// preview.
var busyReopens = slices.Concat(busyLinks, []busyReopen{
	{busyPreview: addBusy, same: func(t *testing.T, doc *html.Node) []string {
		return append(markersOf(t, doc, "add-preview", "data-role"), "lines="+formFields(doc, addPreviewForm(t, doc)).Get(fieldLines))
	}, reopen: pressPreviewAgain},
	{busyPreview: rosterBusy, same: func(t *testing.T, doc *html.Node) []string {
		return markersOf(t, doc, "roster-preview", "data-unit")
	}},
})

// failedRosterAdd runs a roster add of D/ACD to its end with Discord
// refusing Vance's change, so its report offers Retry of Vance, who still
// lacks Internal.
func failedRosterAdd(t *testing.T, w *testWorld) {
	t.Helper()
	serveRoster(t, trooperVance, trooperDoe)
	w.discord.failRoleWrites(memberVance.ID, missingPermissions())
	assertRedirect(t, confirmRoster(t, w.b, parseHTML(t, previewRoster(t, w, "D/ACD"))), foxholePath)
	w.awaitActionEnd(t)
}

// addPreviewForm returns the paste box in the page's add preview, whose
// Preview again previews the add again, and fails the test when there's
// none.
func addPreviewForm(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	form := findElement(pendingBlock(t, doc, "add-preview"), "form", "data-field", "add-form")
	if form == nil {
		t.Fatal("the add preview has no paste box")
	}
	return form
}

// pressPreviewAgain presses Preview again in a busy add preview's paste box
// as the box stands, the way a browser posts it.
func pressPreviewAgain(t *testing.T, w *testWorld, doc, _ *html.Node) *html.Node {
	t.Helper()
	form := addPreviewForm(t, doc)
	res := w.b.postForm(attrOf(form, "action"), formFields(doc, form))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("Preview again answered %d, want 200", res.StatusCode)
	}
	return parseHTML(t, res)
}

// failedRemoval runs a removal of Internal from Ash and Doe to its end with
// Discord refusing Ash's change, so its report offers Retry of Ash, who
// still holds Internal.
func failedRemoval(t *testing.T, w *testWorld) {
	t.Helper()
	w.discord.failRoleWrites(memberAsh.ID, missingPermissions())
	page := parseHTML(t, w.b.get(foxholePath))
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, page, "internal", memberAsh.ID, memberDoe.ID)))
	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)
}

// removeConfirms reads the remove preview's role, every member it names,
// and where its Cancel leads: the list view the selection was made in.
func removeConfirms(t *testing.T, doc *html.Node) []string {
	t.Helper()
	block := pendingBlock(t, doc, "remove-preview")
	cancel := findElement(block, "a", "data-field", "cancel")
	if cancel == nil {
		t.Fatal("the remove preview has no Cancel")
	}
	return append(markersOf(t, doc, "remove-preview", "data-role"), "cancel="+attrOf(cancel, "href"), "members="+strings.Join(namedIn(block), ","))
}

// A preview whose Confirm a running Foxhole action turned off says how to
// get a working one: once the action ends, its reopen control opens the
// same preview again, read from Discord as it is then, with its Confirm on.
// The Confirm never turns back on in place, since a purge acts on whoever
// holds the role when it starts, not on the count its confirmation showed.
func TestABusyPreviewReopensWithItsConfirmOnOnceTheActionEnds(t *testing.T) {
	for _, r := range busyReopens {
		t.Run(r.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			doc, block, held := openBusy(t, w, r.busyPreview)
			want := r.same(t, doc)
			held.release()

			reopen := r.reopen
			if reopen == nil {
				reopen = pressReopen
			}

			reopened := reopen(t, w, doc, block)

			if !enabledConfirm(pendingBlock(t, reopened, r.block)) {
				t.Error("the reopened preview's Confirm is missing or off once the action ended, want it on")
			}
			if got := r.same(t, reopened); !slices.Equal(got, want) {
				t.Errorf("the reopened preview confirms %v, want the busy one's %v", got, want)
			}
		})
	}
}

// A press of a busy preview's control while the action still runs opens
// the busy preview again, and the control on the page it opens still
// works: pressed once the action has ended, it opens the same preview with
// its Confirm on. That holds for a tab already at the address the control
// opens, as one is after the first press.
func TestABusyPreviewOpenedAgainWhileTheActionRunsReopensOnceItEnds(t *testing.T) {
	for _, r := range busyLinks {
		t.Run(r.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			doc, block, held := openBusy(t, w, r.busyPreview)
			want := r.same(t, doc)
			still := pressReopen(t, w, doc, block)
			held.release()

			reopened := pressReopen(t, w, still, pendingBlock(t, still, r.block))

			if !enabledConfirm(pendingBlock(t, reopened, r.block)) {
				t.Error("the reopened preview's Confirm is missing or off once the action ended, want it on")
			}
			if got := r.same(t, reopened); !slices.Equal(got, want) {
				t.Errorf("the reopened preview confirms %v, want the busy one's %v", got, want)
			}
		})
	}
}

// Once the action ends, a busy preview's control opens the page at the
// preview's heading, where the control that opened the preview landed: a
// Retry's at the block above its preview that says what it retries.
func TestABusyPreviewReopensAtItsHeading(t *testing.T) {
	for _, r := range busyLinks {
		t.Run(r.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			doc, block, held := openBusy(t, w, r.busyPreview)
			held.release()

			reopened, fragment := landReopen(t, w, doc, block)

			target := landedOn(reopened, fragment)
			if target == nil {
				t.Fatalf("the page opens at fragment %q, which names nothing, want the %s", fragment, r.block)
			}
			retrying := findElement(reopened, "", "data-field", "retrying")
			if !within(target, pendingBlock(t, reopened, r.block)) && (retrying == nil || !within(target, retrying)) {
				t.Errorf("the page opens at fragment %q, outside the %s and any Retry above it, want on it", fragment, r.block)
			}
		})
	}
}

// While a Foxhole action runs, Re-add approved Foxholers is off and
// says why beside its button, as After a war's Purge does. Which hint shows
// follows what a press would do: with nobody to re-add the nobody hint
// shows instead, the reason that holds once the action ends too, and with
// no approved Foxholers neither shows. Doe, approved without External,
// is someone to re-add; Kestrel, approved holding it, is not.
func TestReAddSaysWhyItIsOffWhileAnActionRuns(t *testing.T) {
	cases := []struct {
		name     string
		approved []store.MemberNames
		// hint is the data-field marker of the hint beside the button, empty
		// for none.
		hint string
	}{
		{"someone to re-add", []store.MemberNames{namesOf(memberDoe)}, "busy"},
		{"nobody to re-add", []store.MemberNames{namesOf(memberKestrel)}, "nobody-to-re-add"},
		{"no approved Foxholers", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			seedApproved(t, w, tc.approved...)
			holdFoxholeAdd(t, w)

			block := reAddBlock(t, parseHTML(t, w.b.get(foxholePath)))

			if button := findElement(block, "button", "", ""); button == nil || !disabled(button) {
				t.Error("the Re-add button is missing or on while a command runs, want it shown and off")
			}
			for _, hint := range []string{"busy", "nobody-to-re-add"} {
				if shown := findElement(block, "", "data-field", hint) != nil; shown != (hint == tc.hint) {
					t.Errorf("the %s hint shows: %v, want %v", hint, shown, hint == tc.hint)
				}
			}
		})
	}
}

// A preview that would change nobody offers no Confirm, busy or not, so a
// running Foxhole action adds no hint to it: it says only why there's
// nobody to change, the reason that still holds once the action ends.
func TestABusyPreviewThatWouldChangeNobodySaysOnlyThat(t *testing.T) {
	cases := []struct {
		busyPreview
		// nobody is the data-field marker of the preview's nobody hint.
		nobody string
	}{
		{busyPreview{name: "purge confirmation", block: "purge-confirm",
			// Nobody left holds External.
			ready: func(_ *testing.T, w *testWorld) { setMembers(w, memberDoe, memberAsh, memberVance) },
			open:  purgeBusy.open}, "nobody-to-purge"},
		{busyPreview{name: "remove preview", block: "remove-preview",
			open: func(t *testing.T, w *testWorld, before *html.Node) *html.Node {
				// Doe doesn't hold External.
				return parseHTML(t, submitSelection(t, w.b, before, "external", memberDoe.ID))
			}}, "nobody-to-remove"},
		{busyPreview{name: "Retry confirmation", block: "retry-confirm",
			ready: func(t *testing.T, w *testWorld) {
				failedPurge(t, w)
				// Kestrel, whom the purge failed on, no longer holds External.
				w.discord.editMember(memberKestrel.ID, func(m *commands.ListedMember) {
					m.RoleIDs = slices.DeleteFunc(m.RoleIDs, func(id string) bool { return id == roleExternal })
				})
			},
			open: openRetryOf}, "nobody-to-purge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)

			_, block, _ := openBusy(t, w, tc.busyPreview)

			if findElement(block, "", "data-field", tc.nobody) == nil {
				t.Errorf("the block doesn't say there's nobody to change, by its %s hint", tc.nobody)
			}
			if findElement(block, "", "data-field", "busy") != nil {
				t.Error("the block says another Foxhole action is running, want only its nobody hint")
			}
			if findElement(block, "", "data-field", "confirm") != nil {
				t.Error("the block offers a Confirm, want none")
			}
		})
	}
}

// While the purge confirmation is open, its Purge is the page's one way to
// start a purge, so After a war points at it in place of its own busy
// hint, even while another Foxhole action runs: the confirmation says why
// its Purge is off.
func TestAfterAWarPointsAtAnOpenPurgeConfirmationWhileAnActionRuns(t *testing.T) {
	w := newFoxholeWorld(t)

	doc, _, _ := openBusy(t, w, purgeBusy)

	form := purgeForm(t, doc)
	if findElement(form, "", "data-field", "purge-open") == nil {
		t.Error("After a war doesn't point at the open purge confirmation")
	}
	if findElement(form, "", "data-field", "busy") != nil {
		t.Error("After a war gives its busy hint, want its pointer to the open purge confirmation in its place")
	}
}
