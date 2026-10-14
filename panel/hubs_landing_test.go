package panel

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"golang.org/x/net/html"
)

// within reports whether n is el or sits inside it.
func within(n, el *html.Node) bool {
	for ; n != nil; n = n.Parent {
		if n == el {
			return true
		}
	}
	return false
}

// Every way the panel sends a panel admin to a hub's edit form lands on the
// form, not on the top of the Hubs page, three blocks above it: the hub
// list's Edit, the form's own Save, and Cancel on a remove preview opened
// from the form.
func TestEditFormOpensAtTheForm(t *testing.T) {
	cases := []struct {
		name string
		// open goes to the hub's edit form one way, and returns the address
		// the control it used names and the answer.
		open func(t *testing.T, w *testWorld, id int64) (control string, res *http.Response)
	}{
		{"Edit on the hub list", func(t *testing.T, w *testWorld, id int64) (string, *http.Response) {
			edit := findElement(listRow(t, parseHTML(t, w.b.get("/")), id), "a", "data-field", "edit")
			if edit == nil {
				t.Fatal("the hub's list row offers no Edit")
			}
			href := attrOf(edit, "href")
			return href, w.b.get(href)
		}},
		{"the edit form's Save", func(t *testing.T, w *testWorld, id int64) (string, *http.Response) {
			doc := parseHTML(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)))
			save := findElement(hubSection(t, doc, id), "", "data-field", "save")
			if save == nil {
				t.Fatal("the edit form has no Save")
			}
			form := save.Parent
			for form != nil && form.Data != "form" {
				form = form.Parent
			}
			if form == nil {
				t.Fatal("the edit form's Save sits in no form")
			}
			fields := formFields(doc, form)
			fields.Set("base_string", "Bravo Voice")
			action := attrOf(form, "action")
			return action, w.b.postForm(action, fields)
		}},
		{"Cancel on a remove preview opened from the form", func(t *testing.T, w *testWorld, id int64) (string, *http.Response) {
			href := cancelAddress(t, removePreviewBlock(t, openHubRemovePreview(t, w.b, id)))
			return href, w.b.get(href)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			id := storedHubID(t, w.st, "hub-1")
			control, res := tc.open(t, w, id)

			doc, fragment := landing(t, w.b, control, res)

			target := landedOn(doc, fragment)
			if target == nil {
				t.Fatalf("the page opens at fragment %q, which names nothing, want the hub's edit form", fragment)
			}
			section := hubSection(t, doc, id)
			if !within(target, section) {
				t.Error("the page lands outside the hub's edit section, want on it")
			}
			if findElement(section, "", "data-field", "save") == nil {
				t.Error("the page lands on the hub's section, which shows no edit form")
			}
		})
	}
}

// removeOpeners are the two controls that open a hub's remove preview.
// control loads the page hub-1's control is on and returns the address it
// names.
var removeOpeners = []struct {
	name    string
	control func(t *testing.T, w *testWorld) string
}{
	{"Remove on a broken hub's list row", func(t *testing.T, w *testWorld) string {
		w.discord.breakChannel("hub-1", true)
		return removeLinkAddress(t, listRow(t, parseHTML(t, w.b.get("/")), storedHubID(t, w.st, "hub-1")))
	}},
	{"Remove hub on the hub's edit form", func(t *testing.T, w *testWorld) string {
		doc := parseHTML(t, w.b.get(hubEditPage(t, w)))
		return removeLinkAddress(t, hubEditBlock(t, w, doc))
	}},
}

// cancelAddress is the address of a remove preview's Cancel.
func cancelAddress(t *testing.T, preview *html.Node) string {
	t.Helper()
	cancel := findElement(preview, "a", "data-field", "cancel")
	if cancel == nil {
		t.Fatal("the remove preview has no Cancel")
	}
	return attrOf(cancel, "href")
}

// removeLinkAddress is the address of the Remove link inside scope.
func removeLinkAddress(t *testing.T, scope *html.Node) string {
	t.Helper()
	remove := findElement(scope, "a", "data-field", "remove")
	if remove == nil {
		t.Fatal("no Remove link")
	}
	return attrOf(remove, "href")
}

// Remove opens the page at the remove preview's heading, which takes focus,
// not at the top of the Hubs page, where a panel admin's first Tab goes to
// the rail.
func TestRemoveOpensAtThePreviewsHeading(t *testing.T) {
	for _, tc := range removeOpeners {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			control := tc.control(t, w)

			doc, fragment := landing(t, w.b, control, w.b.get(control))

			landedHeading(t, doc, fragment, removePreviewBlock(t, doc), "the remove preview's heading")
		})
	}
}

// A Remove from a page loaded before another panel admin removed its hub
// answers that the hub is gone, the edit page's as well as the hub list's,
// though an edit page of a gone hub alone gets the not-found page. The page
// lands on the heading of the block that says so, whose description is the
// refusal, so the panel admin hears why there is nothing to confirm.
func TestRemoveOfAGoneHubLandsOnWhyNothingHappened(t *testing.T) {
	for _, tc := range removeOpeners {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			control := tc.control(t, w)
			assertRedirect(t, removeHubThroughThePreview(t, secondBrowser(t, w), storedHubID(t, w.st, "hub-1")), "/")

			res, fragment := arrive(t, w.b, control, w.b.get(control))

			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", res.StatusCode)
			}
			doc := parseHTML(t, res)
			block := hubGoneBlock(t, w, doc)
			heading := landedHeading(t, doc, fragment, block, "the heading of the block saying the hub is gone")
			if got := attrOf(refusalDescribing(t, doc, heading, block), "data-error"); got != refusalGone {
				t.Errorf("the block's refusal is %q, want %q", got, refusalGone)
			}
		})
	}
}

// refusedSave is one way a save of a Hubs page form is refused, driven the
// way a browser drives it: the form, its action and its fields come from
// the page the panel renders.
type refusedSave struct {
	name string
	// status is the refusal's: 422 for a refused field or a save that
	// changes nothing, 409 for a stale form, 404 for a hub that is gone.
	status int
	// refuse loads the page the form is on and returns the form's action
	// and the fields its refused save posts.
	refuse func(t *testing.T, w *testWorld) (action string, fields url.Values)
	// block is the form's block on the answer's page, or the block shown
	// in its place when the form's hub is gone.
	block func(t *testing.T, w *testWorld, doc *html.Node) *html.Node
}

// formIn returns the action and fields of the form inside block, one of
// the page doc's, as a browser would submit it untouched.
func formIn(t *testing.T, doc, block *html.Node) (string, url.Values) {
	t.Helper()
	form := findElement(block, "form", "", "")
	if form == nil {
		t.Fatal("the block has no form")
	}
	return attrOf(form, "action"), formFields(doc, form)
}

// firstWithAttr returns the first element under n carrying attr, whatever
// its value, nil for none.
func firstWithAttr(n *html.Node, attr string) *html.Node {
	var found *html.Node
	eachElement(n, func(el *html.Node) {
		if _, ok := attrValue(el, attr); ok && found == nil {
			found = el
		}
	})
	return found
}

// isHeading reports whether n is a heading a screen reader lists as one:
// an h1 to h6, or an element with the heading role.
func isHeading(n *html.Node) bool {
	return slices.Contains([]string{"h1", "h2", "h3", "h4", "h5", "h6"}, n.Data) || attrOf(n, "role") == "heading"
}

// landedHeading is the element a page opened at fragment lands on, which
// must be a heading inside block that takes focus without joining the tab
// order. want names the heading in a failure.
func landedHeading(t *testing.T, doc *html.Node, fragment string, block *html.Node, want string) *html.Node {
	t.Helper()
	heading := landedOn(doc, fragment)
	if heading == nil || !isHeading(heading) || !within(heading, block) {
		t.Fatalf("the page opens at fragment %q, want %s", fragment, want)
	}
	if got := attrOf(heading, "tabindex"); got != "-1" {
		t.Errorf("the landed heading has tabindex %q, want -1 so it takes focus without joining the tab order", got)
	}
	return heading
}

// refusalDescribing is the refusal note in block, which must be heading's
// accessible description, so a screen reader says why nothing changed as
// focus lands.
func refusalDescribing(t *testing.T, doc, heading, block *html.Node) *html.Node {
	t.Helper()
	note := firstWithAttr(block, "data-error")
	if note == nil {
		t.Fatal("the block carries no refusal note")
	}
	if got, want := accessibleDescription(doc, heading), announcedText(note); got != want {
		t.Errorf("the landed heading's description is %q, want the refusal note, %q", got, want)
	}
	return note
}

// hubEditBlock is the hub-1 edit section on a page.
func hubEditBlock(t *testing.T, w *testWorld, doc *html.Node) *html.Node {
	t.Helper()
	return hubSection(t, doc, storedHubID(t, w.st, "hub-1"))
}

// hubEditPage is the address of hub-1's edit form, opened at the top.
func hubEditPage(t *testing.T, w *testWorld) string {
	t.Helper()
	return "/?hub=" + strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)
}

// savedByAnother has another panel admin load page and save the form inside
// the block it finds, with change made to its fields, so a form loaded
// before it is stale.
func savedByAnother(t *testing.T, w *testWorld, page string, block func(*testing.T, *testWorld, *html.Node) *html.Node, change func(url.Values)) {
	t.Helper()
	b := secondBrowser(t, w)
	doc := parseHTML(t, b.get(page))
	action, fields := formIn(t, doc, block(t, w, doc))
	change(fields)
	if res := b.postForm(action, fields); res.StatusCode < 300 || res.StatusCode > 399 {
		t.Fatalf("the other panel admin's save answered %d, want it to go through", res.StatusCode)
	}
}

// sectionNamed is a block finder for the element a page marks with
// data-section=name: the moderator and recording roles sections, the hub
// list, and the create and register forms.
func sectionNamed(name string) func(*testing.T, *testWorld, *html.Node) *html.Node {
	return func(t *testing.T, _ *testWorld, doc *html.Node) *html.Node {
		t.Helper()
		block := findElement(doc, "", "data-section", name)
		if block == nil {
			t.Fatalf("page has nothing under data-section=%s", name)
		}
		return block
	}
}

var (
	moderatorsBlock     = sectionNamed(formModerators)
	recordingRolesBlock = sectionNamed(formRecordingRoles)
	createBlock         = sectionNamed(formCreate)
	registerBlock       = sectionNamed(formRegister)
)

// removePreviewOn is the remove preview on a page.
func removePreviewOn(t *testing.T, _ *testWorld, doc *html.Node) *html.Node {
	t.Helper()
	return removePreviewBlock(t, doc)
}

// hubGoneBlock is the block a page shows in place of a remove preview whose
// hub is gone.
func hubGoneBlock(t *testing.T, _ *testWorld, doc *html.Node) *html.Node {
	t.Helper()
	block := findElement(doc, "", "data-field", "hub-gone")
	if block == nil {
		t.Fatal("page has no block saying the hub is gone")
	}
	return block
}

var refusedSaves = []refusedSave{
	{
		name:   "a hub edit with a blank base string",
		status: http.StatusUnprocessableEntity,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get(hubEditPage(t, w)))
			action, fields := formIn(t, doc, hubEditBlock(t, w, doc))
			fields.Set("base_string", "   ")
			return action, fields
		},
		block: hubEditBlock,
	},
	{
		name:   "a stale hub edit",
		status: http.StatusConflict,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get(hubEditPage(t, w)))
			savedByAnother(t, w, hubEditPage(t, w), hubEditBlock, func(f url.Values) { f.Set("user_limit", "5") })
			action, fields := formIn(t, doc, hubEditBlock(t, w, doc))
			fields.Set("base_string", "Bravo Voice")
			return action, fields
		},
		block: hubEditBlock,
	},
	{
		name:   "moderator roles for every hub that change nothing",
		status: http.StatusUnprocessableEntity,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			return formIn(t, doc, moderatorsBlock(t, w, doc))
		},
		block: moderatorsBlock,
	},
	{
		name:   "stale moderator roles for every hub",
		status: http.StatusConflict,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			savedByAnother(t, w, "/", moderatorsBlock, func(f url.Values) { f.Add(fieldModeratorRoles, "role-mp") })
			return formIn(t, doc, moderatorsBlock(t, w, doc))
		},
		block: moderatorsBlock,
	},
	{
		name:   "recording roles that change nothing",
		status: http.StatusUnprocessableEntity,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			return formIn(t, doc, recordingRolesBlock(t, w, doc))
		},
		block: recordingRolesBlock,
	},
	{
		name:   "stale recording roles",
		status: http.StatusConflict,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			savedByAnother(t, w, "/", recordingRolesBlock, func(f url.Values) { f.Add(fieldRecordingRoles, "role-mp") })
			return formIn(t, doc, recordingRolesBlock(t, w, doc))
		},
		block: recordingRolesBlock,
	},
	{
		name:   "a create with no category",
		status: http.StatusUnprocessableEntity,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			action, fields := formIn(t, doc, createBlock(t, w, doc))
			fields.Set(fieldChannelName, "Squad Join")
			fields.Set(fieldBaseString, "Squad Voice")
			return action, fields
		},
		block: createBlock,
	},
	{
		name:   "a register with no channel",
		status: http.StatusUnprocessableEntity,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			action, fields := formIn(t, doc, registerBlock(t, w, doc))
			fields.Set(fieldBaseString, "Squad Voice")
			return action, fields
		},
		block: registerBlock,
	},
	{
		name:   "a stale remove preview's Confirm",
		status: http.StatusConflict,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := openHubRemovePreview(t, w.b, storedHubID(t, w.st, "hub-1"))
			savedByAnother(t, w, hubEditPage(t, w), hubEditBlock, func(f url.Values) { f.Set("user_limit", "5") })
			return formIn(t, doc, removePreviewOn(t, w, doc))
		},
		block: removePreviewOn,
	},
	{
		name:   "a remove preview's Confirm pressed again after it removed the hub",
		status: http.StatusNotFound,
		refuse: func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := openHubRemovePreview(t, w.b, storedHubID(t, w.st, "hub-1"))
			assertRedirect(t, confirmRemoval(t, w.b, removePreviewOn(t, w, doc)), "/")
			return formIn(t, doc, removePreviewOn(t, w, doc))
		},
		block: hubGoneBlock,
	},
}

// A refused save answers with the page, which opens at the refused form's
// block, or the block shown in its place when the form's hub is gone,
// rather than at the top, sections above it: the browser lands on
// the block's heading, which takes focus and whose description is the
// refusal's note, so the panel admin sees and hears why nothing saved
// (PRODUCT.md, Principle 1).
func TestARefusedSaveLandsOnItsForm(t *testing.T) {
	for _, tc := range refusedSaves {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			action, fields := tc.refuse(t, w)

			res, fragment := arrive(t, w.b, action, w.b.postForm(action, fields))

			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
			doc := parseHTML(t, res)
			block := tc.block(t, w, doc)
			heading := landedHeading(t, doc, fragment, block, "the heading of the block carrying the refusal")
			refusalDescribing(t, doc, heading, block)
		})
	}
}

// A hub edit refused because the hub turned broken after its form loaded
// answers with the Broken hub block in place of the form, and lands on
// that block's heading the way any refused edit lands on the form's.
func TestARefusedEditOfABrokenHubLandsOnTheBrokenHubBlock(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	doc := parseHTML(t, w.b.get(hubEditPage(t, w)))
	action, fields := formIn(t, doc, hubEditBlock(t, w, doc))
	fields.Set("base_string", "Bravo Voice")
	w.discord.breakChannel("hub-1", false)

	res, fragment := arrive(t, w.b, action, w.b.postForm(action, fields))

	if !isClientError(res.StatusCode) {
		t.Fatalf("status = %d, want the edit refused", res.StatusCode)
	}
	doc = parseHTML(t, res)
	section := hubEditBlock(t, w, doc)
	if findElement(section, "", "data-field", "broken") == nil {
		t.Fatal("the answer's hub section shows no Broken hub block")
	}
	landedHeading(t, doc, fragment, section, "the Broken hub block's heading")
}

// hubListBlock is the hub list's block on a page.
var hubListBlock = sectionNamed("hub-list")

// Every link back to the hub list lands on the list's heading, not on the
// top of the Hubs page, sections above the list: Cancel on a remove preview
// opened from a broken hub's list row, and Back to the list on a hub's edit
// form.
func TestLinksBackToTheHubListLandOnItsHeading(t *testing.T) {
	cases := []struct {
		name string
		// link loads the page the link is on and returns the address it
		// names.
		link func(t *testing.T, w *testWorld) string
	}{
		{"Cancel on a remove preview opened from a broken hub's list row", func(t *testing.T, w *testWorld) string {
			w.discord.breakChannel("hub-1", true)
			doc := parseHTML(t, w.b.get("/"))
			return cancelAddress(t, removePreviewBlock(t, parseHTML(t, pressRemove(t, w.b, doc, listRow(t, doc, storedHubID(t, w.st, "hub-1"))))))
		}},
		{"Back to the list on a hub's edit form", func(t *testing.T, w *testWorld) string {
			back := findElement(hubEditBlock(t, w, parseHTML(t, w.b.get(hubEditPage(t, w)))), "a", "data-field", "back")
			if back == nil {
				t.Fatal("the edit form has no Back to the list")
			}
			return attrOf(back, "href")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			link := tc.link(t, w)

			doc, fragment := landing(t, w.b, link, w.b.get(link))

			landedHeading(t, doc, fragment, hubListBlock(t, w, doc), "the hub list's heading")
		})
	}
}

// A Confirm refused because another panel admin saved the hub after its
// preview loaded answers with the preview again, and its Cancel still leads
// back to where Remove was pressed: the hub list's heading for Remove on a
// broken hub's list row, and for Remove hub on the hub's edit form, the
// form's heading, with the answer showing the form as the page Remove was
// pressed on did.
func TestAStaleConfirmsCancelLeadsBackToWhereRemoveWasPressed(t *testing.T) {
	cases := []struct {
		name string
		// open presses Remove on hub-1 from where the case says and returns
		// the preview's page.
		open func(t *testing.T, w *testWorld) *html.Node
		// back is the block, on the page Cancel opens, whose heading it
		// lands on.
		back func(t *testing.T, w *testWorld, doc *html.Node) *html.Node
		// onEdit is the refused Confirm's answer showing the hub's edit
		// form.
		onEdit bool
	}{
		{"Remove hub on the hub's edit form", func(t *testing.T, w *testWorld) *html.Node {
			return openHubRemovePreview(t, w.b, storedHubID(t, w.st, "hub-1"))
		}, hubEditBlock, true},
		{"Remove on a broken hub's list row", func(t *testing.T, w *testWorld) *html.Node {
			w.discord.breakChannel("hub-1", false)
			doc := parseHTML(t, w.b.get("/"))
			preview := parseHTML(t, pressRemove(t, w.b, doc, listRow(t, doc, storedHubID(t, w.st, "hub-1"))))
			// A broken hub shows no edit form. Its channel goes back into
			// its category, so another panel admin can save the hub.
			w.discord.moveIntoCategory("hub-1", "cat-1")
			return preview
		}, hubListBlock, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			preview := tc.open(t, w)
			savedByAnother(t, w, hubEditPage(t, w), hubEditBlock, func(f url.Values) { f.Set("user_limit", "5") })

			res := confirmRemoval(t, w.b, removePreviewBlock(t, preview))

			if res.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", res.StatusCode)
			}
			refused := parseHTML(t, res)
			id := strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)
			if tc.onEdit && findElement(refused, "section", "data-hub", id) == nil {
				t.Error("the answer shows no edit form for the hub, want the one Remove was pressed on")
			}
			href := cancelAddress(t, removePreviewBlock(t, refused))
			doc, fragment := landing(t, w.b, href, w.b.get(href))
			landedHeading(t, doc, fragment, tc.back(t, w, doc), "the heading of where Remove was pressed")
		})
	}
}

// A refused Confirm's answer never shows the edit form of a hub other than
// the one being removed, whatever hub the Confirm names beside its own
// route.
func TestAStaleConfirmShowsNoOtherHubsEditForm(t *testing.T) {
	w := newTestWorld(t, testHub(), secondHub())
	signIn(t, w.forum, w.b)
	doc := openHubRemovePreview(t, w.b, storedHubID(t, w.st, "hub-1"))
	savedByAnother(t, w, hubEditPage(t, w), hubEditBlock, func(f url.Values) { f.Set("user_limit", "5") })
	action, fields := formIn(t, doc, removePreviewBlock(t, doc))
	other := strconv.FormatInt(storedHubID(t, w.st, "vc-2"), 10)
	target, err := url.Parse(action)
	if err != nil {
		t.Fatalf("the Confirm's action %q: %v", action, err)
	}
	target.RawQuery = url.Values{"hub": {other}}.Encode()
	fields.Set("hub", other)

	res := w.b.postForm(target.String(), fields)

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	refused := parseHTML(t, res)
	// The answer shows hub-1's own edit form, so the lookup below would
	// find another hub's form if the page showed one.
	hubSection(t, refused, storedHubID(t, w.st, "hub-1"))
	if findElement(refused, "section", "data-hub", other) != nil {
		t.Error("the refused Confirm's answer shows the edit form of a hub other than the one being removed")
	}
}

// A save that goes through lands on the heading of what it changed, though
// its form's action names the form's own block for a refusal to land on: a
// section's save on the section, an added hub on the hub list naming it,
// and a removal on the hub list. A hub edit's save is
// TestEditFormOpensAtTheForm's.
func TestASaveThatGoesThroughLandsOnWhatItChanged(t *testing.T) {
	cases := []struct {
		name string
		// save loads the page the form is on and returns the form's action
		// and the fields of a save that goes through.
		save func(t *testing.T, w *testWorld) (action string, fields url.Values)
		// block is the block of the page the save lands on.
		block func(t *testing.T, w *testWorld, doc *html.Node) *html.Node
		// added is the base string of the hub the block names as added,
		// empty for a save that adds none.
		added string
	}{
		{"the moderator roles for every hub", func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			action, fields := formIn(t, doc, moderatorsBlock(t, w, doc))
			fields.Add(fieldModeratorRoles, "role-mp")
			return action, fields
		}, moderatorsBlock, ""},
		{"the recording roles", func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			action, fields := formIn(t, doc, recordingRolesBlock(t, w, doc))
			fields.Add(fieldRecordingRoles, "role-mp")
			return action, fields
		}, recordingRolesBlock, ""},
		{"a create", func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			action, fields := formIn(t, doc, createBlock(t, w, doc))
			fields.Set(fieldCategory, "cat-1")
			fields.Set(fieldChannelName, "Squad Join")
			fields.Set(fieldBaseString, "Squad Voice")
			return action, fields
		}, hubListBlock, "Squad Voice"},
		{"a register", func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := parseHTML(t, w.b.get("/"))
			action, fields := formIn(t, doc, registerBlock(t, w, doc))
			fields.Set(fieldHubChannel, "vc-2")
			fields.Set(fieldBaseString, "Squad Voice")
			return action, fields
		}, hubListBlock, "Squad Voice"},
		{"a removal", func(t *testing.T, w *testWorld) (string, url.Values) {
			doc := openHubRemovePreview(t, w.b, storedHubID(t, w.st, "hub-1"))
			return formIn(t, doc, removePreviewOn(t, w, doc))
		}, hubListBlock, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			action, fields := tc.save(t, w)

			doc, fragment := landing(t, w.b, action, w.b.postForm(action, fields))

			block := tc.block(t, w, doc)
			landedHeading(t, doc, fragment, block, "the heading of the block it changed")
			if tc.added == "" {
				return
			}
			line := findElement(block, "", "data-field", "added")
			if line == nil {
				t.Fatal("the hub list it lands on names no hub as added")
			}
			if got := fieldText(t, line, "base_string"); got != tc.added {
				t.Errorf("the hub list names %q as added, want %q", got, tc.added)
			}
		})
	}
}
