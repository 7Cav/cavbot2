package panel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
)

// The change log (spec #285): every panel save that takes effect has one
// entry saying who saved, when, which action, and a diff of the fields,
// written in the same store call as the save's settings (#362). The diff is
// a JSON object keyed by form field name, each value {"before": x, "after":
// y}. A register carries every field with a null before, a remove every
// field with a null after, an update the changed fields only.

// change is one field of a diff.
type change struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// diff is a change log diff keyed by form field name.
type diff map[string]change

// diffFields are the stored fields a diff can carry, in the form's order:
// the order a create, register or remove entry lists them and the form
// shows them. The hub channel name is not stored, so it is not here; an
// update that renamed the channel adds it to its diff, and shownFields is
// the order the form renders.
var diffFields = []string{fieldHubChannel, fieldBaseString, fieldPermissionSource,
	fieldModeratorRoles, fieldUserLimit, fieldBitrate, fieldDeleteDelay, fieldEnabled, fieldRenamingAllowed,
	fieldLockingAllowed}

// rankRoleFields are the fields of a rank roles save's diff, one per rank,
// most senior first.
var rankRoleFields = func() []string {
	var fields []string
	for _, rank := range commands.DefaultRankLadder().Ranks() {
		fields = append(fields, rankRoleField(rank.Abbrev))
	}
	return fields
}()

// shownFields are every field a diff can carry, in the form's order: a
// hub's fields, then the recording roles, the one field of their section's
// diffs, then the rank roles.
var shownFields = slices.Concat([]string{fieldChannelName}, diffFields, []string{fieldRecordingRoles}, rankRoleFields)

// roleFields are the fields whose values are role IDs, shown by name.
var roleFields = slices.Concat([]string{fieldModeratorRoles, fieldRecordingRoles}, rankRoleFields)

// fieldLabels name the fields a diff can carry as the change log shows
// them: in the words of the form's label for each, as the Foxhole page's
// change log names its own fields.
var fieldLabels = map[string]string{
	fieldChannelName:      "hub channel name",
	fieldHubChannel:       "hub channel",
	fieldBaseString:       "base string",
	fieldPermissionSource: "permission source",
	fieldModeratorRoles:   "moderator roles",
	fieldUserLimit:        "user limit",
	fieldBitrate:          "bitrate",
	fieldDeleteDelay:      "delete delay in minutes",
	fieldEnabled:          "enabled",
	fieldRenamingAllowed:  "renaming allowed",
	fieldLockingAllowed:   "locking allowed",
	fieldRecordingRoles:   "recording roles",
}

// permissionSourceNames are the permission source's stored values as the
// form's options name them.
var permissionSourceNames = map[string]string{
	string(store.PermissionCategory):   "category",
	string(store.PermissionHubChannel): "hub channel",
}

// sortedRoles is a role set as a diff records it: sorted, and never null,
// so two sets compare and read the same whatever order the store returned
// them in.
func sortedRoles(ids []string) []string {
	roles := slices.Clone(ids)
	if roles == nil {
		roles = []string{}
	}
	slices.Sort(roles)
	return roles
}

// fieldValues returns a hub's settings keyed by form field name, as the
// diff records them.
func fieldValues(h store.Hub) map[string]any {
	return map[string]any{
		fieldHubChannel:       h.HubChannelID,
		fieldBaseString:       h.BaseString,
		fieldPermissionSource: string(h.PermissionSource),
		fieldModeratorRoles:   sortedRoles(h.ModeratorRoleIDs),
		fieldUserLimit:        h.UserLimit,
		fieldBitrate:          h.Bitrate,
		fieldDeleteDelay:      h.DeleteDelayMinutes,
		fieldEnabled:          h.Enabled,
		fieldRenamingAllowed:  h.RenamingAllowed,
		fieldLockingAllowed:   h.LockingAllowed,
	}
}

// diffHubs builds the diff between two states of a hub. A nil before is a
// hub that did not exist (a register), a nil after a hub that no longer
// does (a remove); either way every field is carried. With both set, only
// the fields whose values differ are.
func diffHubs(before, after *store.Hub) diff {
	d := diff{}
	var was, is map[string]any
	if before != nil {
		was = fieldValues(*before)
	}
	if after != nil {
		is = fieldValues(*after)
	}
	for _, field := range diffFields {
		if before != nil && after != nil && reflect.DeepEqual(was[field], is[field]) {
			continue
		}
		d[field] = change{Before: was[field], After: is[field]}
	}
	return d
}

// changeEntry is a save's change log entry: who saved, which action, and
// the diff. The store writes it together with the save's settings and sets
// its hub reference.
func changeEntry(action store.ChangeAction, d diff, by actor) (store.ChangeLogEntry, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return store.ChangeLogEntry{}, fmt.Errorf("encode change log diff: %w", err)
	}
	return store.ChangeLogEntry{
		ForumUserID:   by.userID,
		ForumUsername: by.username,
		Action:        action,
		Diff:          raw,
	}, nil
}

// changeLogLimit is how many entries a hub's form shows, newest first.
const changeLogLimit = 10

// changeView is one entry as the hub form shows it.
type changeView struct {
	ID       int64
	Username string
	At       time.Time
	Action   store.ChangeAction
	Fields   []fieldView
}

// Label is the entry's action as the change log names it. A save of the
// guild-wide moderator roles or of the recording roles updates the one set
// its section holds, and its field already names the set.
func (c changeView) Label() string {
	switch c.Action {
	case store.ChangeModerators, store.ChangeRecordingRoles, store.ChangeRankRoles:
		return string(store.ChangeUpdate)
	}
	return string(c.Action)
}

// fieldView is one changed field of an entry, its values rendered as text.
type fieldView struct {
	Field  string
	Before string
	After  string
}

// Label is the field as the change log names it. A rank's field is named
// by its rank.
func (f fieldView) Label() string {
	if label, ok := fieldLabels[f.Field]; ok {
		return label
	}
	if abbrev, ok := strings.CutPrefix(f.Field, rankRoleFieldPrefix); ok {
		return abbrev + " rank role"
	}
	return f.Field
}

// changeViews decodes stored entries for the form. Moderator and recording
// roles and the hub channel are stored as IDs, and each is shown by name
// where the guild still has it, as guild was read at this page load. A
// permission source shows as its form option names it. An entry whose diff
// does not decode is shown with no fields rather than dropped: the save
// happened.
func changeViews(entries []store.ChangeLogEntry, guild guildInfo) []changeView {
	views := make([]changeView, 0, len(entries))
	for _, e := range entries {
		v := changeView{ID: e.ID, Username: e.ForumUsername, At: e.At, Action: e.Action}
		var d diff
		if err := json.Unmarshal(e.Diff, &d); err == nil {
			for _, field := range shownFields {
				c, ok := d[field]
				if !ok {
					continue
				}
				var names map[string]string
				switch {
				case slices.Contains(roleFields, field):
					names = guild.roleNames
				case field == fieldHubChannel:
					names = guild.channelNames
				case field == fieldPermissionSource:
					names = permissionSourceNames
				}
				v.Fields = append(v.Fields, fieldView{Field: field, Before: valueText(c.Before, names), After: valueText(c.After, names)})
			}
		}
		views = append(views, v)
	}
	return views
}

// valueText renders one decoded diff value for the form: a string as
// itself, a list joined with commas, a bool as on or off, null and an empty
// list as none, a number as written. names, when set, replaces a string or
// each list item that is a key of it; one with no name shows as itself.
func valueText(v any, names map[string]string) string {
	switch v := v.(type) {
	case nil:
		return "none"
	case string:
		if name, ok := names[v]; ok {
			return name
		}
		return v
	case bool:
		if v {
			return "on"
		}
		return "off"
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			text := fmt.Sprint(item)
			if name, ok := names[text]; ok {
				text = name
			}
			parts = append(parts, text)
		}
		if len(parts) == 0 {
			return "none"
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(v)
	}
}
