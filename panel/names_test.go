package panel

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// What a screen reader announces for a panel control: its accessible name
// and description, worked out from the page as the server renders it, with
// no script run. accessibleName follows the parts of the W3C accessible name
// computation the panel's markup can reach, in the order a browser tries
// them, so a control named any way a browser honours gets the name it would.

// accessibleName is n's name: the text of the elements aria-labelledby
// names, else aria-label, else for a form control its labels, else for a
// fieldset its legend, else for a button, link, summary or column header
// its own text.
func accessibleName(doc, n *html.Node) string {
	if ids, ok := attrValue(n, "aria-labelledby"); ok {
		return textOfIDs(doc, ids)
	}
	if label, ok := attrValue(n, "aria-label"); ok && strings.TrimSpace(label) != "" {
		return collapseSpace(label)
	}
	switch n.Data {
	case "input", "select", "textarea":
		return labelsOf(doc, n)
	case "fieldset":
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "legend" {
				return announcedText(c)
			}
		}
	case "button", "a", "summary", "th":
		return announcedText(n)
	}
	return ""
}

// accessibleDescription is n's description: the text of the elements
// aria-describedby names.
func accessibleDescription(doc, n *html.Node) string {
	ids, _ := attrValue(n, "aria-describedby")
	return textOfIDs(doc, ids)
}

// describedByResolves checks that every ID in every aria-describedby on the
// page names an element the page carries, so no description points at
// nothing.
func describedByResolves(t *testing.T, doc *html.Node) {
	t.Helper()
	ids := map[string]bool{}
	eachLiveElement(doc, func(n *html.Node) {
		if id, ok := attrValue(n, "id"); ok {
			ids[id] = true
		}
	})
	eachLiveElement(doc, func(n *html.Node) {
		refs, _ := attrValue(n, "aria-describedby")
		for id := range strings.FieldsSeq(refs) {
			if !ids[id] {
				t.Errorf("a <%s> is described by %q, which names nothing on the page", n.Data, id)
			}
		}
	})
}

// labelsOf is the text of a form control's labels: each <label> naming its
// ID, and the <label> around it.
func labelsOf(doc, control *html.Node) string {
	var parts []string
	if id, ok := attrValue(control, "id"); ok && id != "" {
		eachLiveElement(doc, func(n *html.Node) {
			if n.Data == "label" {
				if f, _ := attrValue(n, "for"); f == id {
					parts = append(parts, announcedText(n))
				}
			}
		})
	}
	for p := control.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "label" {
			if _, named := attrValue(p, "for"); !named {
				parts = append(parts, announcedText(p))
			}
			break
		}
	}
	return collapseSpace(strings.Join(parts, " "))
}

// textOfIDs joins the announced text of the elements a space-separated ID
// list names, in its order.
func textOfIDs(doc *html.Node, ids string) string {
	var parts []string
	for id := range strings.FieldsSeq(ids) {
		if el := findElement(doc, "", "id", id); el != nil {
			parts = append(parts, announcedText(el))
		}
	}
	return collapseSpace(strings.Join(parts, " "))
}

// announcedText is the text a screen reader reads from n's subtree: an
// image's alt text stands in for it, and a hidden subtree and the form
// controls inside a label read nothing.
func announcedText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			sb.WriteString(n.Data)
			return
		case html.ElementNode:
			if _, hidden := attrValue(n, "hidden"); hidden {
				return
			}
			if v, _ := attrValue(n, "aria-hidden"); v == "true" {
				return
			}
			switch n.Data {
			case "img":
				alt, _ := attrValue(n, "alt")
				sb.WriteString(" ")
				sb.WriteString(alt)
				sb.WriteString(" ")
				return
			case "input", "select", "textarea", "template", "script", "style":
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return collapseSpace(sb.String())
}

// shownText is the text a sighted user reads in n's subtree: a hidden
// subtree shows nothing, and neither do the words the panel's
// visually-hidden class keeps for a screen reader alone.
func shownText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			sb.WriteString(n.Data)
			return
		case html.ElementNode:
			if _, hidden := attrValue(n, "hidden"); hidden {
				return
			}
			if class, _ := attrValue(n, "class"); slices.Contains(strings.Fields(class), "visually-hidden") {
				return
			}
			switch n.Data {
			case "img", "input", "select", "textarea", "template", "script", "style":
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return collapseSpace(sb.String())
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// nearestGroup is the group a screen reader announces on entering n: its
// nearest ancestor that is a fieldset or carries the group or radiogroup
// role. It is nil when n sits in none.
func nearestGroup(n *html.Node) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		role, _ := attrValue(p, "role")
		if p.Data == "fieldset" || role == "group" || role == "radiogroup" {
			return p
		}
	}
	return nil
}

// A hub's edit page holds three Saves and three add controls: the edit
// form's own, and those of the two sections above it. A screen reader user
// moving by buttons tells them apart by name alone.
func TestHubEditPageGivesEachSaveAndAddControlItsOwnName(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)

	doc := parseHTML(t, w.b.get("/?hub="+id))

	main := findElement(doc, "main", "", "")
	if main == nil {
		t.Fatal("page has no <main>")
	}
	counts := map[string]int{}
	controls := 0
	eachLiveElement(main, func(n *html.Node) {
		typ, typed := attrValue(n, "type")
		_, add := attrValue(n, "data-add")
		submit := n.Data == "button" && (typ == "submit" || !typed)
		if !submit && !add {
			return
		}
		controls++
		counts[accessibleName(doc, n)]++
	})
	if controls < 2 {
		t.Fatalf("edit page has %d submit buttons and add controls, want several", controls)
	}
	for name, n := range counts {
		if n > 1 {
			t.Errorf("%d controls share the name %q", n, name)
		}
	}
}

// A sighted user reads which set a picker edits from the label above it. A
// screen reader user hears the same label as the name of the group they
// enter at the picker's first control.
func TestEachHubPagePickerIsAGroupNamedForWhatItEdits(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	list := parseHTML(t, w.b.get("/"))
	edit := parseHTML(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)))

	for _, tc := range []struct {
		picker string
		doc    *html.Node
		within func(*testing.T, *html.Node) *html.Node
	}{
		{"moderator roles for every hub", edit, moderatorsSection},
		{"recording roles", edit, recordingRolesSection},
		{"a hub's own moderator roles", edit, func(t *testing.T, doc *html.Node) *html.Node { return hubSection(t, doc, id) }},
		{"the register form's voice channel", list, func(t *testing.T, doc *html.Node) *html.Node { return pickerRoot(t, doc, fieldHubChannel) }},
	} {
		t.Run(tc.picker, func(t *testing.T) {
			add := findElement(tc.within(t, tc.doc), "", "data-add", "")
			if add == nil {
				t.Fatal("picker has no add control")
			}
			group := nearestGroup(add)
			if group == nil {
				t.Fatal("the picker's add control sits in no group")
			}
			if name := accessibleName(tc.doc, group); name == "" {
				t.Error("the picker's group has no name")
			}
		})
	}
}

// controlNamed is the first control under n that posts under name and
// shows on the page, and fails the test when there is none.
func controlNamed(t *testing.T, n *html.Node, name string) *html.Node {
	t.Helper()
	var found *html.Node
	eachLiveElement(n, func(el *html.Node) {
		got, _ := attrValue(el, "name")
		typ, _ := attrValue(el, "type")
		if found == nil && got == name && typ != "hidden" {
			found = el
		}
	})
	if found == nil {
		t.Fatalf("no control posts under %s", name)
	}
	return found
}

// formHolding is the form around the element under data-field=field.
func formHolding(t *testing.T, doc *html.Node, field string) *html.Node {
	t.Helper()
	el := findElement(doc, "", "data-field", field)
	for p := el; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "form" {
			return p
		}
	}
	t.Fatalf("no form holds data-field=%s", field)
	return nil
}

// A field's hint says what to type or what the setting does. A screen
// reader user hears it after the field's name, as its description, and the
// name stays the short label a sighted user reads above the field.
func TestFieldHintsAreTheirFieldsDescriptionNotPartOfItsName(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	list := parseHTML(t, w.b.get("/"))
	edit := parseHTML(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)))
	fx := newFoxholeWorld(t)
	foxhole := parseHTML(t, fx.b.get("/foxhole"))

	for _, tc := range []struct {
		field string
		doc   *html.Node
		find  func(*testing.T, *html.Node) *html.Node
	}{
		{"Create's base string", list, func(t *testing.T, doc *html.Node) *html.Node {
			return controlNamed(t, formHolding(t, doc, fieldCategory), fieldBaseString)
		}},
		{"Register's base string", list, func(t *testing.T, doc *html.Node) *html.Node {
			return controlNamed(t, formHolding(t, doc, fieldHubChannel), fieldBaseString)
		}},
		{"user limit", edit, posting(fieldUserLimit)},
		{"bitrate", edit, posting(fieldBitrate)},
		{"delete delay", edit, posting(fieldDeleteDelay)},
		{"renaming allowed", edit, posting(fieldRenamingAllowed)},
		{"locking allowed", edit, posting(fieldLockingAllowed)},
		{"members to add", foxhole, posting(fieldLines)},
		{"validated unit", foxhole, posting(fieldUnit)},
	} {
		t.Run(tc.field, func(t *testing.T) {
			control := tc.find(t, tc.doc)
			name, desc := accessibleName(tc.doc, control), accessibleDescription(tc.doc, control)
			if name == "" {
				t.Error("the field has no name")
			}
			if desc == "" {
				t.Fatalf("the field has no description; its name is %q", name)
			}
			if strings.Contains(name, desc) {
				t.Errorf("the field's name %q carries its description %q", name, desc)
			}
		})
	}
}

// posting finds the control posting under name anywhere on the page.
func posting(name string) func(*testing.T, *html.Node) *html.Node {
	return func(t *testing.T, doc *html.Node) *html.Node { return controlNamed(t, doc, name) }
}

// A sighted manager reads what a set of radios chooses from the label
// above it. A screen reader user hears the same label as the name of the
// group the radios form, on reaching the first of them.
func TestEachSetOfFoxholeRadiosIsAGroupNamedForWhatItChooses(t *testing.T) {
	w := newFoxholeWorld(t)

	doc := parseHTML(t, w.b.get("/foxhole"))

	for _, form := range []string{"add-form", "purge"} {
		t.Run(form, func(t *testing.T) {
			radios := 0
			eachLiveElement(formHolding(t, doc, form), func(n *html.Node) {
				if typ, _ := attrValue(n, "type"); n.Data != "input" || typ != "radio" {
					return
				}
				radios++
				value, _ := attrValue(n, "value")
				group := nearestGroup(n)
				if group == nil {
					t.Errorf("radio %q sits in no group", value)
					return
				}
				if accessibleName(doc, group) == "" {
					t.Errorf("radio %q sits in a group with no name", value)
				}
			})
			if radios == 0 {
				t.Fatal("form has no radios")
			}
		})
	}
}

// hubLink is what a link on the Hubs page opens: a hub's removal preview,
// or its edit form.
type hubLink struct {
	hub     int64
	preview bool
}

// hubOpened is what the link to href opens, and false for a link that opens
// neither a hub's edit form nor its removal preview.
func hubOpened(href string) (hubLink, bool) {
	u, err := url.Parse(href)
	if err != nil || u.Path != "/" {
		return hubLink{}, false
	}
	// An edit page's remove link names the hub twice, and opens the
	// removal preview over the edit form.
	if v := u.Query().Get("remove"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		return hubLink{hub: id, preview: true}, err == nil
	}
	if v := u.Query().Get("hub"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		return hubLink{hub: id}, err == nil
	}
	return hubLink{}, false
}

// eachHubLink calls visit with each live link under n that opens a hub's
// edit form or removal preview, and what it opens.
func eachHubLink(n *html.Node, visit func(a *html.Node, link hubLink)) {
	eachLiveElement(n, func(el *html.Node) {
		href, _ := attrValue(el, "href")
		if link, ok := hubOpened(href); el.Data == "a" && ok {
			visit(el, link)
		}
	})
}

// A screen reader user picking from the page's links list hears each
// link's name alone. A link that opens a hub's edit form or removal preview
// names the hub by its base string, so six hubs' Edit links read as six
// hubs.
func TestEachLinkThatOpensAHubNamesIt(t *testing.T) {
	w := newTestWorld(t, testHub(), secondHub())
	signIn(t, w.forum, w.b)
	w.discord.breakChannel("hub-1", false)
	broken, healthy := storedHubID(t, w.st, "hub-1"), storedHubID(t, w.st, "vc-2")
	landed, _ := landing(t, w.b, "/hubs", w.b.postForm("/hubs", createForm("cat-1", "Ops Join", "Ops Voice")))
	bases := map[int64]string{}
	var created int64
	for _, h := range storedHubs(t, w.st) {
		bases[h.ID] = h.BaseString
		if h.BaseString == "Ops Voice" {
			created = h.ID
		}
	}

	main := func(doc *html.Node) *html.Node { return findElement(doc, "main", "", "") }
	list := parseHTML(t, w.b.get("/"))
	edit := parseHTML(t, w.b.get("/?hub="+strconv.FormatInt(healthy, 10)))
	previewed := parseHTML(t, w.b.get(fmt.Sprintf("/?hub=%d&remove=%d", healthy, healthy)))
	for _, page := range []struct {
		name string
		doc  *html.Node
		// within is the part of the page the test reads.
		within *html.Node
		// want are the links it must hold, so the test never passes on a
		// page with none.
		want []hubLink
	}{
		{"the hub list", list, main(list), []hubLink{{healthy, false}, {broken, true}}},
		{"a hub's edit page", edit, main(edit), []hubLink{{healthy, true}}},
		{"the page a create lands on", landed, main(landed), []hubLink{{created, false}}},
		// Its Cancel leads back to the hub's edit form.
		{"the removal preview opened from a hub's edit page", previewed, removePreviewBlock(t, previewed), []hubLink{{healthy, false}}},
	} {
		t.Run(page.name, func(t *testing.T) {
			found := map[hubLink]bool{}
			eachHubLink(page.within, func(a *html.Node, link hubLink) {
				found[link] = true
				base, stored := bases[link.hub]
				if !stored {
					t.Errorf("link to %s opens hub %d, which the store doesn't hold", attrOf(a, "href"), link.hub)
					return
				}
				if name := accessibleName(page.doc, a); !strings.Contains(name, base) {
					t.Errorf("link to %s is named %q, which doesn't name hub %s", attrOf(a, "href"), name, base)
				}
			})
			for _, want := range page.want {
				if !found[want] {
					t.Errorf("no link opens hub %s (removal preview: %t)", bases[want.hub], want.preview)
				}
			}
		})
	}
}

// A refused edit save shows the form as posted, base string included. Its
// remove link still opens the removal preview of the hub as stored, so it
// names the hub as the store holds it, the way that preview does.
func TestARefusedEditsRemoveLinkNamesTheHubAsStored(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	stale := updateForm(t, w.st)
	another := updateForm(t, w.st)
	another.Set("user_limit", "5")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), another), "/")
	stale.Set("base_string", "Typed Voice")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), stale)

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for the stale form", res.StatusCode)
	}
	doc := parseHTML(t, res)
	var names []string
	eachHubLink(hubSection(t, doc, id), func(a *html.Node, link hubLink) {
		if link == (hubLink{id, true}) {
			names = append(names, accessibleName(doc, a))
		}
	})
	if len(names) == 0 {
		t.Fatal("the refused form has no link to the hub's removal preview")
	}
	for _, name := range names {
		if !strings.Contains(name, "Arma Voice") {
			t.Errorf("the remove link is named %q, want it to name hub Arma Voice as stored", name)
		}
	}
}

// A screen reader moving along a hub list row, or down a column, announces
// each column's header. Every column has one, the column of Edit and Remove
// links included.
func TestEachHubListColumnHasAHeader(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	doc := parseHTML(t, w.b.get("/"))

	headers := 0
	eachLiveElement(findElement(doc, "main", "", ""), func(n *html.Node) {
		if n.Data != "th" {
			return
		}
		headers++
		if accessibleName(doc, n) == "" {
			t.Errorf("column header %d has no name", headers)
		}
	})
	if headers == 0 {
		t.Fatal("the hub list has no column headers")
	}
}

// One role can sit in all three role pickers of a hub's edit page. A screen
// reader user moving by buttons hears each tag's remove control name alone,
// so each names the set its picker edits, and removes the role from the set
// they meant.
func TestEachRoleTagsRemoveControlNamesItsPickersSet(t *testing.T) {
	hub := testHub()
	hub.ModeratorRoleIDs = []string{"role-mp"}
	w := newTestWorld(t, hub)
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/moderators", moderatorsForm(t, w.st, "role-mp")), "/")
	assertRedirect(t, w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-mp")), "/")
	id := storedHubID(t, w.st, "hub-1")

	doc := parseHTML(t, w.b.get("/?hub="+strconv.FormatInt(id, 10)))

	named := map[string]string{}
	for _, tc := range []struct {
		set    string
		within func(*testing.T, *html.Node) *html.Node
	}{
		{setGuildModerators, moderatorsSection},
		{setRecordingRoles, recordingRolesSection},
		{setHubModerators, func(t *testing.T, doc *html.Node) *html.Node { return hubSection(t, doc, id) }},
	} {
		t.Run(tc.set, func(t *testing.T) {
			tag := findElement(tc.within(t, doc), "", "data-tag", "role-mp")
			if tag == nil {
				t.Fatal("the picker has no tag for Military Police")
			}
			remove := findElement(tag, "", "data-remove", "")
			if remove == nil {
				t.Fatal("the tag has no remove control")
			}
			name := accessibleName(doc, remove)
			if !strings.Contains(name, "Military Police") || !strings.Contains(name, tc.set) {
				t.Errorf("the remove control is named %q, want it to name Military Police and %s", name, tc.set)
			}
			if other, taken := named[name]; taken {
				t.Errorf("the remove control shares the name %q with the one in %s", name, other)
			}
			named[name] = tc.set
		})
	}
}

// A screen reader user picking from the Foxhole page's links list hears
// each link's name alone. Each row's note link names its member, whether or
// not they have a note, so a list of holders reads as its members and not
// as "Add a note" over and over.
func TestEachNoteLinkNamesItsMember(t *testing.T) {
	w := newNoRoleWorld(t)
	holders := parseHTML(t, w.b.get(foxholePath))
	noRole := openNoRoleView(t, w.b)

	for _, view := range []struct {
		name string
		doc  *html.Node
		rows map[string]*html.Node
		// want maps each member the view lists to their display name.
		want map[string]string
	}{
		// Kestrel has a note and Ash has none.
		{"the holder list", holders, holderRows(t, holders), map[string]string{memberAsh.ID: "Rowan Ash", memberKestrel.ID: "kestrel_tlr"}},
		{"the no-role view", noRole, noRoleRows(t, noRole), map[string]string{memberDoe.ID: "SGT Doe.J", memberMarsh.ID: "CPT Marsh.E"}},
	} {
		t.Run(view.name, func(t *testing.T) {
			named := map[string]string{}
			for id, display := range view.want {
				row, ok := view.rows[id]
				if !ok {
					t.Errorf("the view has no row for %s", display)
					continue
				}
				name := accessibleName(view.doc, noteLink(t, row))
				if !strings.Contains(name, display) {
					t.Errorf("%s's note link is named %q, which doesn't name them", display, name)
				}
				if other, taken := named[name]; taken {
					t.Errorf("%s's note link shares the name %q with %s's", display, name, other)
				}
				named[name] = display
			}
		})
	}
}

// A sighted manager reads a note link's label as before: one label on a
// member with a note and another on one without. The words naming the
// member are for a screen reader alone.
func TestNoteLinkShowsALabelMatchingTheNote(t *testing.T) {
	w := newFoxholeWorld(t)
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)

	rows := holderRows(t, parseHTML(t, w.b.get(foxholePath)))

	shown := map[string]string{}
	for id, display := range map[string]string{memberDoe.ID: "SGT Doe.J", memberAsh.ID: "Rowan Ash"} {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("the holder list has no row for %s", display)
		}
		shown[id] = shownText(noteLink(t, row))
		if shown[id] == "" || strings.Contains(shown[id], display) {
			t.Errorf("%s's note link shows %q, want a label without their name", display, shown[id])
		}
	}
	if shown[memberDoe.ID] == shown[memberAsh.ID] {
		t.Errorf("the note links of a member with a note and one without both show %q", shown[memberDoe.ID])
	}
}

// recordingLinks are the links on a recording's block that act on it, by
// data-field: its mix download, its track download and its Delete.
var recordingLinks = []string{"mix-download", "zip-download", "delete"}

// shownAs is how a recording's block on the list names it to a sighted
// user: the title its heading shows and the start time its facts show. It
// fails the test when the block shows either empty.
func shownAs(t *testing.T, block *html.Node) (title, started string) {
	t.Helper()
	title, started = fieldText(t, block, "title"), fieldText(t, block, "started")
	if title == "" || started == "" {
		t.Fatalf("recording %s shows the title %q and the start time %q, want both", attrOf(block, "data-recording"), title, started)
	}
	return title, started
}

// A screen reader user picking from the Recordings page's links list hears
// each link's name alone. Each download and Delete names its recording by
// title and by its start time as the page shows them, so two untitled
// recordings' links read apart, and no two links on the page share a name.
func TestEachRecordingLinkNamesItsRecording(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	now := time.Now()
	// stored maps each recording to the title it was started with, empty
	// for none.
	stored := map[int64]string{
		w.storeRecording(t, a.discordID, "", now.Add(-3*time.Hour)):              "",
		w.storeRecording(t, a.discordID, "", now.Add(-2*time.Hour)):              "",
		w.storeRecording(t, a.discordID, "Weekly briefing", now.Add(-time.Hour)): "Weekly briefing",
	}
	signInAs(t, w.forum, w.b, a.account)

	doc := parseHTML(t, w.b.get(recordingsPath))

	listed := listedRecordings(t, doc)
	for id, title := range stored {
		block, ok := listed[id]
		if !ok {
			t.Errorf("the page doesn't list recording %d", id)
			continue
		}
		heading, started := shownAs(t, block)
		for _, field := range recordingLinks {
			link := findElement(block, "a", "data-field", field)
			if link == nil {
				t.Errorf("recording %d has no %s link", id, field)
				continue
			}
			name := accessibleName(doc, link)
			// An untitled recording's empty title is in every name.
			for _, want := range []string{heading, started, title} {
				if !strings.Contains(name, want) {
					t.Errorf("recording %d's %s link is named %q, which doesn't name %q", id, field, name, want)
				}
			}
		}
	}
	counts := map[string]int{}
	eachLiveElement(doc, func(n *html.Node) {
		if n.Data == "a" {
			counts[accessibleName(doc, n)]++
		}
	})
	for name, n := range counts {
		if n > 1 {
			t.Errorf("%d links on the page share the name %q", n, name)
		}
	}
}

// A recording's delete preview offers its downloads beside the warning that
// it goes for good. Their names say which recording they fetch, as the
// list's do.
func TestDeletePreviewsDownloadLinksNameTheRecording(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.storeRecording(t, a.discordID, "", time.Now().Add(-time.Hour))
	signInAs(t, w.forum, w.b, a.account)
	page := parseHTML(t, w.b.get(recordingsPath))
	listed, ok := listedRecordings(t, page)[rec]
	if !ok {
		t.Fatalf("the page doesn't list recording %d", rec)
	}
	title, started := shownAs(t, listed)

	doc := parseHTML(t, w.b.get(linkHref("delete")(t, page, rec)))

	block, ok := listedRecordings(t, doc)[rec]
	if !ok || attrOf(block, "data-field") != "delete-preview" {
		t.Fatalf("the page doesn't show recording %d's delete preview", rec)
	}
	for _, field := range []string{"mix-download", "zip-download"} {
		link := findElement(block, "a", "data-field", field)
		if link == nil {
			t.Errorf("the delete preview has no %s link", field)
			continue
		}
		if name := accessibleName(doc, link); !strings.Contains(name, title) || !strings.Contains(name, started) {
			t.Errorf("the preview's %s link is named %q, want it to name %s, started %s", field, name, title, started)
		}
	}
}

// A sighted user reads a recording's download and Delete links as before.
// The words naming the recording are for a screen reader alone.
func TestRecordingLinksShowNoWordsNamingTheRecording(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.storeRecording(t, a.discordID, "Weekly briefing", time.Now().Add(-time.Hour))
	signInAs(t, w.forum, w.b, a.account)

	block, ok := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]

	if !ok {
		t.Fatalf("the page doesn't list recording %d", rec)
	}
	title, started := shownAs(t, block)
	for _, field := range recordingLinks {
		link := findElement(block, "a", "data-field", field)
		if link == nil {
			t.Errorf("recording %d has no %s link", rec, field)
			continue
		}
		if shown := shownText(link); strings.Contains(shown, title) || strings.Contains(shown, started) {
			t.Errorf("the %s link shows %q, want neither the title nor the start time in sight", field, shown)
		}
	}
}

// A recording with no track offers its info file in the zip's place
// (#648). That download names its recording to a screen reader as the
// others do.
func TestInfoDownloadLinkNamesItsRecording(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.recordSilence(t, a.discordID)
	signInAs(t, w.forum, w.b, a.account)

	doc := parseHTML(t, w.b.get(recordingsPath))

	block, ok := listedRecordings(t, doc)[rec]
	if !ok {
		t.Fatalf("the page doesn't list recording %d", rec)
	}
	title, started := shownAs(t, block)
	link := findElement(block, "a", "data-field", "info-download")
	if link == nil {
		t.Fatalf("recording %d has no info-download link", rec)
	}
	if name := accessibleName(doc, link); !strings.Contains(name, title) || !strings.Contains(name, started) {
		t.Errorf("recording %d's info-download link is named %q, want it to name %s, started %s", rec, name, title, started)
	}
}
