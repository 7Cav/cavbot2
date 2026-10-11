package panel

import (
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// pressRemove presses the Remove control under scope the way a browser
// does: a link is followed, and a form's button submits its form by the
// form's method, with the fields the form owns on doc.
func pressRemove(t *testing.T, b *browser, doc, scope *html.Node) *http.Response {
	t.Helper()
	control := findElement(scope, "", "data-field", "remove")
	if control == nil {
		t.Fatal("no Remove control")
	}
	if control.Data == "a" {
		return b.get(attrOf(control, "href"))
	}
	form := control.Parent
	for form != nil && form.Data != "form" {
		form = form.Parent
	}
	if form == nil {
		t.Fatal("the Remove control is neither a link nor a form's button")
	}
	fields := formFields(doc, form)
	action := attrOf(form, "action")
	if strings.EqualFold(attrOf(form, "method"), http.MethodPost) {
		return b.postForm(action, fields)
	}
	return b.get(action + "?" + fields.Encode())
}

// removeForm is the remove preview's Confirm of the hub on hub-1 as a
// preview loaded now posts it: the version the store holds for the hub
// now.
func removeForm(t *testing.T, st store.Store) url.Values {
	t.Helper()
	return url.Values{"version": {storedVersion(t, st, "hub-1")}}
}

// listRow returns the hub list's row of a hub.
func listRow(t *testing.T, doc *html.Node, hubID int64) *html.Node {
	t.Helper()
	row := findElement(doc, "tr", "data-hub", strconv.FormatInt(hubID, 10))
	if row == nil {
		t.Fatalf("the hub list has no row for hub %d", hubID)
	}
	return row
}

// openHubRemovePreview opens a hub's remove preview the way a panel admin
// does, from Remove on the hub's edit page, and returns its page.
func openHubRemovePreview(t *testing.T, b *browser, hubID int64) *html.Node {
	t.Helper()
	doc := parseHTML(t, b.get("/?hub="+strconv.FormatInt(hubID, 10)))
	res := pressRemove(t, b, doc, hubSection(t, doc, hubID))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("opening the remove preview: status = %d, want 200", res.StatusCode)
	}
	return parseHTML(t, res)
}

// removeHubThroughThePreview opens a hub's remove preview and presses its
// Confirm.
func removeHubThroughThePreview(t *testing.T, b *browser, hubID int64) *http.Response {
	t.Helper()
	return confirmRemoval(t, b, removePreviewBlock(t, openHubRemovePreview(t, b, hubID)))
}

// Remove, on a broken hub's list row or on a hub's edit page, opens the
// remove preview of that hub and removes nothing (PRODUCT.md, Principle 2).
func TestRemoveOpensThePreviewAndRemovesNothing(t *testing.T) {
	cases := []struct {
		name   string
		broken bool
		page   func(id int64) string
		scope  func(t *testing.T, doc *html.Node, id int64) *html.Node
	}{
		{"a broken hub's list row", true, func(int64) string { return "/" }, listRow},
		{"a hub's edit page", false, func(id int64) string { return "/?hub=" + strconv.FormatInt(id, 10) }, hubSection},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			if tc.broken {
				w.discord.breakChannel("hub-1", true)
			}
			id := storedHubID(t, w.st, "hub-1")
			doc := parseHTML(t, w.b.get(tc.page(id)))

			res := pressRemove(t, w.b, doc, tc.scope(t, doc, id))

			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			preview := removePreviewBlock(t, parseHTML(t, res))
			if got := attrOf(preview, "data-remove-hub"); got != strconv.FormatInt(id, 10) {
				t.Errorf("the preview removes hub %q, want %d", got, id)
			}
			if hubs := storedHubs(t, w.st); len(hubs) != 1 {
				t.Errorf("stored hubs = %+v, want hub-1 still stored", hubs)
			}
		})
	}
}

// A Confirm acts on the hub its preview showed (CODING_STANDARDS.md). One
// from a preview loaded before another save of the hub is refused as stale
// and removes nothing, and the page shows the preview again, whose Confirm
// then removes the hub.
func TestStalePreviewIsRefusedThenItsRenderedPreviewRemoves(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	preview := removePreviewBlock(t, openHubRemovePreview(t, w.b, storedHubID(t, w.st, "hub-1")))
	form := updateForm(t, w.st)
	form.Set("user_limit", "5")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), form), "/")

	res := confirmRemoval(t, w.b, preview)

	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
	if hubs := storedHubs(t, w.st); len(hubs) != 1 {
		t.Fatalf("stored hubs = %+v, want hub-1 still stored", hubs)
	}
	doc := parseHTML(t, res)
	if findElement(doc, "", "data-error", refusalStale) == nil {
		t.Error("the page carries no stale refusal")
	}
	assertRedirect(t, confirmRemoval(t, w.b, removePreviewBlock(t, doc)), "/")
	if hubs := storedHubs(t, w.st); len(hubs) != 0 {
		t.Errorf("stored hubs after the second Confirm = %+v, want none", hubs)
	}
}

// A Confirm that carries no version, as a one-click Remove form cached from
// before the preview existed posts, is refused as stale and removes nothing.
func TestConfirmWithNoVersionIsRefusedAsStale(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.postForm(hubPath(t, w.st, "hub-1")+"/remove", nil)

	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
	if hubs := storedHubs(t, w.st); len(hubs) != 1 {
		t.Errorf("stored hubs = %+v, want hub-1 still stored", hubs)
	}
}

// Pressing a preview's Confirm again after it removed the hub, as a double
// click does, finds the hub gone: the page says so, and nothing more
// changes.
func TestSecondConfirmFindsTheHubGoneAndChangesNothing(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	preview := removePreviewBlock(t, openHubRemovePreview(t, w.b, storedHubID(t, w.st, "hub-1")))
	assertRedirect(t, confirmRemoval(t, w.b, preview), "/")
	before := readSavedState(t, w.st)

	res := confirmRemoval(t, w.b, preview)

	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	if findElement(parseHTML(t, res), "", "data-error", refusalGone) == nil {
		t.Error("the page does not say the hub is gone")
	}
	if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
		t.Errorf("the store after the second Confirm = %+v, want it as before, %+v", after, before)
	}
}

// The remove preview of a hub that is gone, opened from a page loaded
// before someone removed it, says the hub is gone and offers no Confirm.
func TestRemovePreviewOfAGoneHubSaysSoAndOffersNoConfirm(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.get("/?remove=999")

	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if findElement(doc, "", "data-error", refusalGone) == nil {
		t.Error("the page does not say the hub is gone")
	}
	if confirm := findElement(doc, "", "data-field", "confirm"); confirm != nil {
		t.Error("the page offers a Confirm for a hub that is gone")
	}
}

// Cancel on a remove preview returns to the page Remove was pressed on: the
// hub's edit page, or the hub list with no preview open.
func TestCancelReturnsToWhereRemoveWasPressed(t *testing.T) {
	cases := []struct {
		name   string
		broken bool
		page   func(id int64) string
		scope  func(t *testing.T, doc *html.Node, id int64) *html.Node
		onEdit bool
	}{
		{"a hub's edit page", false, func(id int64) string { return "/?hub=" + strconv.FormatInt(id, 10) }, hubSection, true},
		{"a broken hub's list row", true, func(int64) string { return "/" }, listRow, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			if tc.broken {
				w.discord.breakChannel("hub-1", true)
			}
			id := storedHubID(t, w.st, "hub-1")
			doc := parseHTML(t, w.b.get(tc.page(id)))
			preview := removePreviewBlock(t, parseHTML(t, pressRemove(t, w.b, doc, tc.scope(t, doc, id))))
			cancel := findElement(preview, "a", "data-field", "cancel")
			if cancel == nil {
				t.Fatal("the remove preview has no Cancel")
			}

			res := w.b.get(attrOf(cancel, "href"))

			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			landed := parseHTML(t, res)
			if findElement(landed, "", "data-field", "remove-preview") != nil {
				t.Error("Cancel lands on a page with the remove preview still open")
			}
			if onEdit := findElement(landed, "section", "data-hub", strconv.FormatInt(id, 10)) != nil; onEdit != tc.onEdit {
				t.Errorf("Cancel lands on the hub's edit page: %v, want %v", onEdit, tc.onEdit)
			}
		})
	}
}

// The remove preview says what the removal takes and what it leaves: the
// hub's own moderator roles, which lose their say over its spawned
// channels, how many spawned channels are open now, and the hub channel,
// which stays in Discord.
func TestRemovePreviewNamesWhatGoesAndWhatStays(t *testing.T) {
	hub := testHub()
	hub.ModeratorRoleIDs = []string{"role-hq"}
	w := newTestWorld(t, hub)
	signIn(t, w.forum, w.b)
	w.join("user-a", "hub-1")

	preview := removePreviewBlock(t, openHubRemovePreview(t, w.b, storedHubID(t, w.st, "hub-1")))

	goes := findElement(preview, "", "data-list", "goes")
	if goes == nil {
		t.Fatal("the preview lists nothing that goes")
	}
	if got := valuesOf(goes, "data-role"); !slices.Equal(got, []string{"role-hq"}) {
		t.Errorf("the preview lists the moderator roles %v as going, want [role-hq]", got)
	}
	if got := fieldText(t, preview, "spawned"); got != "1" {
		t.Errorf("the preview counts %q spawned channels, want 1", got)
	}
	stays := findElement(preview, "", "data-list", "stays")
	if stays == nil {
		t.Fatal("the preview lists nothing that stays")
	}
	if got := fieldText(t, stays, "channel_name"); got != "Join to create" {
		t.Errorf("the preview names %q as the channel that stays, want Join to create", got)
	}
}

// After a removal the hub list names the hub that went, and a reload of the
// page the removal landed on still does: the page reads the removal back
// rather than carrying it.
func TestHubListNamesTheRemovedHubAfterARemoval(t *testing.T) {
	w := newTestWorld(t, testHub(), secondHub())
	signIn(t, w.forum, w.b)
	res := removeHubThroughThePreview(t, w.b, storedHubID(t, w.st, "hub-1"))
	landing := location(t, res).RequestURI()

	for load := 1; load <= 2; load++ {
		page := w.b.get(landing)
		if page.StatusCode != http.StatusOK {
			t.Fatalf("load %d: status = %d, want 200", load, page.StatusCode)
		}
		line := findElement(parseHTML(t, page), "", "data-field", "removed")
		if line == nil {
			t.Fatalf("load %d: the hub list names no removed hub", load)
		}
		if got := fieldText(t, line, "base_string"); got != "Arma Voice" {
			t.Errorf("load %d: the hub list names %q as removed, want Arma Voice", load, got)
		}
	}
}
