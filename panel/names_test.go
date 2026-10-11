package panel

import (
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// What a screen reader announces for a panel control: its accessible name
// and description, worked out from the page as the server renders it, with
// no script run. accessibleName follows the parts of the W3C accessible name
// computation the panel's markup can reach, in the order a browser tries
// them, so a control named any way a browser honours gets the name it would.

// accessibleName is n's name: the text of the elements aria-labelledby
// names, else aria-label, else for a form control its labels, else for a
// fieldset its legend, else for a button, link or summary its own text.
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
	case "button", "a", "summary":
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
