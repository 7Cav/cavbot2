package panel

import (
	"context"
	"fmt"
	"slices"

	"github.com/7cav/cavbot2/store"
)

// Guild-wide moderator roles (spec #285, #296): the roles that may rename
// any spawned channel of every hub, set once in the section at the top of
// the hub page. A hub's effective moderators are the union of these and its
// own; there is no per-hub exclusion.

// moderatorsInput is the guild-wide section's form as posted.
type moderatorsInput struct {
	RoleIDs []string
}

// moderatorsPage is the guild-wide section as the page renders it: the
// guild's roles with the stored set checked, or the form as posted when a
// save was refused, and the section's last entries, newest first.
type moderatorsPage struct {
	Roles   []guildRole
	Changes []changeView
}

// moderatorsSection builds the guild-wide section from the guild read and
// the store: every role with the stored set checked, or the form as posted
// back after a refusal, and the section's last saves, newest first.
func (s *hubService) moderatorsSection(ctx context.Context, guild guildInfo, stored []string, posted *moderatorsInput) (moderatorsPage, error) {
	checked := stored
	if posted != nil {
		checked = posted.RoleIDs
	}
	entries, err := s.deps.Store.ListModeratorChanges(ctx, changeLogLimit)
	if err != nil {
		return moderatorsPage{}, fmt.Errorf("list moderator changes: %w", err)
	}
	return moderatorsPage{Roles: rolePicker(guild, checked), Changes: changeViews(entries, guild.names)}, nil
}

// setModerators saves the guild-wide moderator roles: it validates the
// posted IDs against the guild's roles read now, writes the guild settings
// row, applies the set to the runtime, so /voice-rename honours it at once,
// and appends a change log entry under no hub with the moderators action
// and a diff in the shape a hub save writes. A refusal is a *fieldError
// naming the roles field, and nothing is written.
func (s *hubService) setModerators(ctx context.Context, in moderatorsInput, by actor) ([]string, error) {
	guild, err := s.readGuild()
	if err != nil {
		return nil, err
	}
	roles, err := validRoleSet(in.RoleIDs, guild)
	if err != nil {
		return nil, err
	}
	before, err := s.deps.Store.GetGuildModeratorRoles(ctx, s.deps.GuildID)
	if err != nil {
		return nil, fmt.Errorf("read guild moderator roles: %w", err)
	}
	if err := s.deps.Store.SetGuildModeratorRoles(ctx, s.deps.GuildID, roles); err != nil {
		return nil, fmt.Errorf("write guild moderator roles: %w", err)
	}
	s.deps.Runtime.ApplyGuildModeratorRoles(roles)
	d := diff{fieldModeratorRoles: {Before: sortedRoles(before), After: sortedRoles(roles)}}
	if err := s.appendChange(ctx, 0, store.ChangeModerators, d, by); err != nil {
		return nil, err
	}
	return roles, nil
}

// validRoleSet checks each posted role ID against the guild's roles and
// returns the set with duplicates dropped: a role posted twice is stored
// once. A refusal is a *fieldError on the roles field. The hub form's own
// picker and the guild-wide section validate through here alike.
func validRoleSet(posted []string, guild guildInfo) ([]string, error) {
	known := make(map[string]struct{}, len(guild.eligible))
	for _, r := range guild.eligible {
		known[r.ID] = struct{}{}
	}
	roles := make([]string, 0, len(posted))
	for _, id := range posted {
		if _, ok := known[id]; !ok {
			return nil, &fieldError{fieldModeratorRoles, "One of those roles is no longer in the server. Choose again."}
		}
		if !slices.Contains(roles, id) {
			roles = append(roles, id)
		}
	}
	return roles, nil
}
