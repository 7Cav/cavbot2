package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/7cav/cavbot2/store"
)

// The change log (spec #285): every panel save appends one entry saying who
// saved, when, which action, and a diff of the fields. The diff is a JSON
// object keyed by form field name, each value {"before": x, "after": y}. A
// register carries every field with a null before, a remove every field
// with a null after, an update the changed fields only.

// change is one field of a diff.
type change struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// diff is a change log diff keyed by form field name.
type diff map[string]change

// fieldValues returns a hub's settings keyed by form field name, as the
// diff records them. Moderator roles are sorted, so two sets compare and
// read the same whatever order the store returned them in.
func fieldValues(h store.Hub) map[string]any {
	roles := slices.Clone(h.ModeratorRoleIDs)
	if roles == nil {
		roles = []string{}
	}
	slices.Sort(roles)
	return map[string]any{
		fieldHubChannel:       h.HubChannelID,
		fieldBaseString:       h.BaseString,
		fieldPermissionSource: string(h.PermissionSource),
		fieldModeratorRoles:   roles,
		fieldUserLimit:        h.UserLimit,
		fieldBitrate:          h.Bitrate,
		fieldEnabled:          h.Enabled,
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
	for _, field := range []string{fieldHubChannel, fieldBaseString, fieldPermissionSource,
		fieldModeratorRoles, fieldUserLimit, fieldBitrate, fieldEnabled} {
		if before != nil && after != nil && reflect.DeepEqual(was[field], is[field]) {
			continue
		}
		d[field] = change{Before: was[field], After: is[field]}
	}
	return d
}

// appendChange records one save in the change log. hubID is zero for a save
// about no hub, which a remove is once the row is gone.
func (s *hubService) appendChange(ctx context.Context, hubID int64, action store.ChangeAction, d diff, by actor) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encode change log diff: %w", err)
	}
	if err := s.deps.Store.AppendChangeLog(ctx, store.ChangeLogEntry{
		HubID:         hubID,
		ForumUserID:   by.userID,
		ForumUsername: by.username,
		Action:        action,
		Diff:          raw,
	}); err != nil {
		return fmt.Errorf("append change log: %w", err)
	}
	return nil
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

// fieldView is one changed field of an entry, its values rendered as text.
type fieldView struct {
	Field  string
	Before string
	After  string
}

// diffFieldOrder is the order the form shows a diff's fields in: the
// form's own.
var diffFieldOrder = []string{fieldHubChannel, fieldBaseString, fieldPermissionSource,
	fieldModeratorRoles, fieldUserLimit, fieldBitrate, fieldEnabled}

// changeViews decodes stored entries for the form. Moderator roles are
// stored as IDs and shown by name where the guild still has the role;
// roleNames maps the guild's roles as read at this page load. An entry
// whose diff does not decode is shown with no fields rather than dropped:
// the save happened.
func changeViews(entries []store.ChangeLogEntry, roleNames map[string]string) []changeView {
	views := make([]changeView, 0, len(entries))
	for _, e := range entries {
		v := changeView{ID: e.ID, Username: e.ForumUsername, At: e.At, Action: e.Action}
		var d map[string]struct {
			Before json.RawMessage `json:"before"`
			After  json.RawMessage `json:"after"`
		}
		if err := json.Unmarshal(e.Diff, &d); err == nil {
			for _, field := range diffFieldOrder {
				c, ok := d[field]
				if !ok {
					continue
				}
				names := roleNames
				if field != fieldModeratorRoles {
					names = nil
				}
				v.Fields = append(v.Fields, fieldView{Field: field, Before: valueText(c.Before, names), After: valueText(c.After, names)})
			}
		}
		views = append(views, v)
	}
	return views
}

// valueText renders one diff value for the form: a string as itself, a
// list joined with commas, a bool as on or off, null and an empty list as
// none, a number as written. names, when set, replaces each list item that
// is a key of it; an item with no name shows as itself.
func valueText(raw json.RawMessage, names map[string]string) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	switch v := v.(type) {
	case nil:
		return "none"
	case string:
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
		return string(raw)
	}
}
