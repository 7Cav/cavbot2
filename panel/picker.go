package panel

import (
	"fmt"
	"slices"
	"sort"
)

// Tag pickers (spec #343): a picker shows the record's selected set as
// tags, each with a remove control and the hidden input the form posts,
// and an add control that opens a search list of candidates. The list is
// rendered at load and hidden until opened; the script in static/panel.js
// filters it, adds a tag for a chosen row and removes a tag. A page that
// never runs the script posts the tags as rendered (ADR 0013).

// pickerView is one tag picker as the template renders it.
type pickerView struct {
	// Field is the form field name every tag posts under, and the
	// data-field on the picker, a test contract.
	Field string
	// Single makes the picker hold one tag at most: choosing a candidate
	// replaces it.
	Single bool
	// Dots is whether the items show a colour dot: roles do, channels do
	// not.
	Dots bool
	// AddLabel is the add control's text.
	AddLabel string
	// SearchLabel is the search input's accessible name.
	SearchLabel string
	Tags        []pickerItem
	Candidates  []pickerItem
}

// pickerItem is one item a picker shows, as a tag or as a search row. Field
// and Dot are the picker's, copied onto each item so the tag template, which
// renders one item, knows what it posts under and whether to draw a dot.
type pickerItem struct {
	Field string
	ID    string
	// Label is the item's text: a role's name, or its ID for a deleted
	// role, whose name nothing remembers; a channel's name with its
	// category's.
	Label string
	Dot   bool
	// Colour is a role's Discord colour as a CSS hex, and empty when the
	// role has none or the item is a channel.
	Colour string
	// Unavailable is why a stored moderator role is no longer eligible, and
	// empty otherwise. It is the data-unavailable attribute on the control
	// that posts the role's ID, a test contract; the note's copy is not.
	Unavailable unavailableReason
}

// item is an item of the picker with the ID and label.
func (v pickerView) item(id, label string) pickerItem {
	return pickerItem{Field: v.Field, ID: id, Label: label, Dot: v.Dots}
}

// Blank is an empty item of the picker, rendered inside its <template>.
// The script clones and fills it to make the tag for a chosen candidate.
func (v pickerView) Blank() pickerItem {
	return v.item("", "")
}

// rolePicker builds a moderator picker. The tags are the selected roles:
// the eligible ones highest position first, then the unavailable moderator
// roles by ID, each with its reason. The candidates are the eligible roles
// not selected, highest position first, so the role search never offers a
// selected role or an unavailable one (ADR 0012). stored is the record's
// set, what an unavailable role may be shown from; selected decides the
// tags. A page load passes the stored set as selected. A refusal passes the
// form as posted, so a removed unavailable role stays removed until the
// page is loaded again, and a posted ID the record never stored renders no
// tag.
func rolePicker(guild guildInfo, stored, selected []string) pickerView {
	view := pickerView{Field: fieldModeratorRoles, Dots: true, AddLabel: "Add a role", SearchLabel: "Search roles"}
	for _, r := range guild.eligible {
		item := view.item(r.ID, r.Name)
		item.Colour = roleColour(r.Color)
		if slices.Contains(selected, r.ID) {
			view.Tags = append(view.Tags, item)
		} else {
			view.Candidates = append(view.Candidates, item)
		}
	}
	var kept []pickerItem
	for _, id := range stored {
		reason := guild.unavailability(id)
		if reason == "" || !slices.Contains(selected, id) {
			continue
		}
		item := view.item(id, id)
		item.Unavailable = reason
		if r, ok := guild.live[id]; ok {
			item.Label, item.Colour = r.Name, roleColour(r.Color)
		}
		kept = append(kept, item)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].ID < kept[j].ID })
	view.Tags = append(view.Tags, kept...)
	return view
}

// roleColour is a Discord role colour as a CSS hex. Discord stores the
// colour as one integer, 0 meaning the role has none, which renders as no
// colour rather than black.
func roleColour(c int) string {
	if c == 0 {
		return ""
	}
	return fmt.Sprintf("#%06x", c)
}

// channelPicker builds the register picker: every voice channel that is
// not a hub as a candidate, labelled with its category and ordered by
// category then name, and the chosen channel as the one tag when it is
// among them. chosenID is the form as posted back after a refusal, and
// empty on a plain page load.
func channelPicker(sn snapshot, chosenID string) pickerView {
	view := pickerView{Field: fieldHubChannel, Single: true, AddLabel: "Choose a voice channel", SearchLabel: "Search voice channels"}
	type candidate struct {
		id, name, category string
	}
	var found []candidate
	for _, ch := range sn.guild.voiceChannels() {
		if _, taken := sn.hubOn(ch.ID); taken {
			continue
		}
		found = append(found, candidate{ch.ID, ch.Name, sn.guild.categoryName(ch)})
	}
	sort.Slice(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.category != b.category {
			return a.category < b.category
		}
		return a.name < b.name
	})
	for _, c := range found {
		label := c.name + " (no category)"
		if c.category != "" {
			label = c.name + " (" + c.category + ")"
		}
		item := view.item(c.id, label)
		view.Candidates = append(view.Candidates, item)
		if c.id == chosenID {
			view.Tags = append(view.Tags, item)
		}
	}
	return view
}
