package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

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
