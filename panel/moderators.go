package panel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/7cav/cavbot2/store"
)

// Guild-wide moderator roles (spec #285, #296): the roles that may rename
// any spawned channel of every hub, set once in the section at the top of
// the hub page. A hub's effective moderators are the union of these and its
// own; there is no per-hub exclusion.

// moderatorsInput is the guild-wide section's form as posted.
type moderatorsInput struct {
	// Version is the set's version when the section loaded, from a hidden
	// input. A save whose version the set is no longer at is a stale form.
	Version string
	RoleIDs []string
}

// errStaleModerators is the refusal a save from a stale guild-wide section
// gets, answered the way errStaleHub is.
var errStaleModerators = &fieldError{refusalStale, "Someone saved the moderator roles for every hub after you opened this page, " +
	"so your changes were not saved. Their save is at the top of the change log below. " +
	"Your roles are still in the picker. Save again to keep them."}

// moderatorsPage is the guild-wide section as the page renders it: the
// picker with the stored set as tags, or the form as posted when a save
// was refused, the version the section posts, and the section's last
// entries, newest first.
type moderatorsPage struct {
	Version string
	Picker  pickerView
	Changes []changeView
}

// moderatorsSection builds the guild-wide section from the guild read and
// the store: the picker with the stored set as tags, or the form as posted
// back after a refusal, and the section's last saves, newest first. The
// version follows editForm's rule: the posted one after a refusal, the
// stored one after a stale refusal or with nothing posted.
func (s *hubService) moderatorsSection(ctx context.Context, guild guildInfo, stored store.GuildModeratorRoles, posted *moderatorsInput, stale bool) (moderatorsPage, error) {
	selected, version := stored.RoleIDs, strconv.FormatInt(stored.Version, 10)
	if posted != nil {
		selected = posted.RoleIDs
		if !stale {
			version = posted.Version
		}
	}
	entries, err := s.deps.Store.ListModeratorChanges(ctx, changeLogLimit)
	if err != nil {
		return moderatorsPage{}, fmt.Errorf("list moderator changes: %w", err)
	}
	return moderatorsPage{Version: version, Picker: rolePicker(guild, stored.RoleIDs, selected), Changes: changeViews(entries, guild.names)}, nil
}

// setModerators saves the guild-wide moderator roles: it validates the
// posted IDs against the guild read now and the stored set, writes the
// guild settings row with a change log entry under no hub, the moderators
// action and a diff in the shape a hub save writes, in one store write, then
// applies the set to the runtime, so the voice commands honour it at once.
// A refusal is a *fieldError naming the roles field, and nothing is written.
// A section loaded at a version the set is no longer at is refused first,
// as errStaleModerators, before the guild read.
func (s *hubService) setModerators(ctx context.Context, in moderatorsInput, by actor) ([]string, error) {
	s.saveTurn.Lock()
	defer s.saveTurn.Unlock()
	// The stored set is read before validation: it is what an unavailable
	// role may be kept from, and its version is what the section must have
	// loaded.
	before, err := s.deps.Store.GetGuildModeratorRoles(ctx, s.deps.GuildID)
	if err != nil {
		return nil, fmt.Errorf("read guild moderator roles: %w", err)
	}
	if !formIsCurrent(in.Version, before.Version) {
		return nil, errStaleModerators
	}
	guild, err := s.readGuild()
	if err != nil {
		return nil, err
	}
	roles, err := acceptedRoles(in.RoleIDs, guild, before.RoleIDs)
	if err != nil {
		return nil, err
	}
	d := diff{fieldModeratorRoles: {Before: sortedRoles(before.RoleIDs), After: sortedRoles(roles)}}
	entry, err := changeEntry(store.ChangeModerators, d, by)
	if err != nil {
		return nil, err
	}
	saved := store.GuildModeratorRoles{RoleIDs: roles, Version: before.Version}
	err = s.deps.Store.SaveGuildModeratorRoles(ctx, s.deps.GuildID, saved, entry)
	if errors.Is(err, store.ErrStale) {
		// Another process saved the set after this save's read.
		return nil, errStaleModerators
	}
	if err != nil {
		return nil, fmt.Errorf("write guild moderator roles: %w", err)
	}
	s.deps.Runtime.ApplyGuildModeratorRoles(roles)
	return roles, nil
}

// acceptedRoles checks each posted role ID and returns the set with
// duplicates dropped: a role posted twice is stored once. The check accepts
// an ID on two grounds. The role is eligible now. Or stored, the set the
// record being saved holds, read in this request, already has the ID. So a
// save keeps an unavailable moderator role and never adds one (ADR 0012),
// and an ID another record stores is refused for this one. A refusal is a
// *fieldError on the roles field. The hub form's own picker and the
// guild-wide section validate through here alike.
func acceptedRoles(posted []string, guild guildInfo, stored []string) ([]string, error) {
	roles := make([]string, 0, len(posted))
	for _, id := range posted {
		if !guild.isEligible(id) && !slices.Contains(stored, id) {
			return nil, &fieldError{fieldModeratorRoles, "One of those roles cannot be a moderator role. Choose again."}
		}
		if !slices.Contains(roles, id) {
			roles = append(roles, id)
		}
	}
	return roles, nil
}
