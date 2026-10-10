package panel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/7cav/cavbot2/store"
)

// The recording roles (#383, GLOSSARY.md): the roles whose holders may start
// a recording, set once for the guild in the section beside the guild-wide
// moderator roles. The recording runtime reads them from the store at each
// start, so a save has nothing running to apply to.

// recordingRolesInput is the recording roles section's form as posted.
type recordingRolesInput struct {
	// Version is the set's version when the section loaded, from a hidden
	// input. A save whose version the set is no longer at is a stale form.
	Version string
	RoleIDs []string
}

// errStaleRecordingRoles is the refusal a save from a stale recording roles
// section gets, answered the way errStaleModerators is.
var errStaleRecordingRoles = &fieldError{stale: true, Message: "Someone saved the recording roles after you opened this page, " +
	"so your changes were not saved. Their save is at the top of the change log below. " +
	"Your roles are still in the picker. Save again to keep them."}

// errIneligibleRecordingRole is the refusal a recording roles save gets
// when it posts a role it may not add.
var errIneligibleRecordingRole = &fieldError{Field: fieldRecordingRoles, Message: "One of those roles cannot be a recording role. Choose again."}

// errRecordingRolesUnchanged is the refusal a recording roles save gets
// when it posts the stored set: a save that would change nothing says so
// and writes nothing, not even a change log entry.
var errRecordingRolesUnchanged = &fieldError{unchanged: true, Message: "Those are already the recording roles, so nothing was saved."}

// recordingRolesPage is the recording roles section as the page renders it:
// the picker with the stored set as tags, or the form as posted when a save
// was refused, the version the section posts, and the section's last
// entries, newest first.
type recordingRolesPage struct {
	Version string
	Picker  pickerView
	Changes []changeView
}

// recordingRolesSection builds the recording roles section from the guild
// read and the store: the picker with the stored set as tags, or the form
// as posted back after a refusal with the version postedBackVersion gives
// it, and the section's last saves, newest first.
func (s *hubService) recordingRolesSection(ctx context.Context, guild guildInfo, req pageRequest) (recordingRolesPage, error) {
	stored, err := s.deps.Store.GetRecordingRoles(ctx, s.deps.GuildID)
	if err != nil {
		return recordingRolesPage{}, fmt.Errorf("%s: %w", recordingRolesRead, err)
	}
	selected, version := stored.RoleIDs, strconv.FormatInt(stored.Version, 10)
	if posted := req.RecordingRoles; posted != nil {
		selected = posted.RoleIDs
		version = postedBackVersion(stored.Version, posted.Version, req.staleFor(formRecordingRoles))
	}
	entries, err := s.deps.Store.ListRecordingRoleChanges(ctx, changeLogLimit)
	if err != nil {
		return recordingRolesPage{}, fmt.Errorf("%s: %w", recordingRoleChangesRead, err)
	}
	return recordingRolesPage{Version: version, Picker: rolePicker(fieldRecordingRoles, guild, stored.RoleIDs, selected),
		Changes: changeViews(entries, guild.names)}, nil
}

// setRecordingRoles saves the recording roles: it writes the set with a
// change log entry under no hub, the recording roles action and a diff in
// the shape the guild-wide moderator save writes, in one store write. It
// validates the posted IDs against the guild read now and the stored set,
// and a refusal is a *fieldError naming the roles field, with nothing
// written. A valid set that is the stored one is refused as
// errRecordingRolesUnchanged. A section loaded at a version the set is no longer at is refused
// first, as errStaleRecordingRoles, before the guild read.
func (s *hubService) setRecordingRoles(ctx context.Context, in recordingRolesInput, by actor) ([]string, error) {
	unlock, err := s.lockSave(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	before, err := s.deps.Store.GetRecordingRoles(ctx, s.deps.GuildID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", recordingRolesRead, err)
	}
	if !formIsCurrent(in.Version, before.Version) {
		return nil, errStaleRecordingRoles
	}
	guild, err := s.readGuild(ctx)
	if err != nil {
		return nil, err
	}
	roles, err := acceptedRoles(in.RoleIDs, guild.info, before.RoleIDs, errIneligibleRecordingRole)
	if err != nil {
		return nil, err
	}
	if slices.Equal(sortedRoles(roles), sortedRoles(before.RoleIDs)) {
		return nil, errRecordingRolesUnchanged
	}
	d := diff{fieldRecordingRoles: {Before: sortedRoles(before.RoleIDs), After: sortedRoles(roles)}}
	entry, err := changeEntry(store.ChangeRecordingRoles, d, by)
	if err != nil {
		return nil, err
	}
	saved := store.RecordingRoles{RoleIDs: roles, Version: before.Version}
	err = s.deps.Store.SaveRecordingRoles(ctx, s.deps.GuildID, saved, entry)
	if errors.Is(err, store.ErrStale) {
		// Another process saved the set after this save's read.
		return nil, errStaleRecordingRoles
	}
	if err != nil {
		return nil, fmt.Errorf("write recording roles: %w", err)
	}
	return roles, nil
}
