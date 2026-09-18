package commands

import (
	"errors"
	"fmt"

	"github.com/bwmarrin/discordgo"
)

// Renaming a spawned channel (spec #285, #292). The runtime owns the decision
// because every fact it needs sits under its lock: which spawned channel the
// invoker is in, who owns it, and the hub it came from. The /voice-rename
// handler in voice_rename.go parses the interaction, calls Rename and renders
// the outcome.

// errNotInSpawnedChannel is returned when the invoker is not sitting in a
// spawned channel the runtime tracks: in a hub channel, in any other voice
// channel, or in none.
var errNotInSpawnedChannel = errors.New("invoker is not in a spawned channel")

// notOwnerError is returned when the invoker holds no moderator role and is
// not the channel's owner. Owner is empty when the channel has none, which
// the handler logs at WARN: the Discord gate or the rank-role assumption has
// failed.
type notOwnerError struct {
	Owner string
}

func (e *notOwnerError) Error() string {
	if e.Owner == "" {
		return "channel has no owner"
	}
	return "invoker is not the owner " + e.Owner
}

// renamed is what a successful Rename reports, for the handler's log line.
type renamed struct {
	ChannelID string
	Before    string
	After     string
}

// Rename renames the spawned channel the invoker is sitting in, on the
// invoker's behalf. No channel argument: the target is the invoker's current
// channel from the runtime's own occupancy tracking. The edit call carries an
// audit log reason naming the invoker and no retry on rate limit.
func (t *TempVC) Rename(userID string, memberRoles []string, name string) (renamed, error) {
	t.mu.Lock()
	channelID := t.userChannel[userID]
	_, tracked := t.occupants[channelID]
	owner := t.owners[channelID]
	moderator := t.isModeratorLocked(channelID, memberRoles)
	t.mu.Unlock()
	if !tracked {
		return renamed{}, errNotInSpawnedChannel
	}
	// A moderator role passes for every spawned channel of a hub it covers,
	// whoever owns it and whether anyone does. It never reads the owner, so a
	// moderator renames an ownerless channel with no WARN, and it never
	// changes the owner.
	if !moderator && owner != userID {
		return renamed{}, &notOwnerError{Owner: owner}
	}

	// The name before the edit comes from the state cache, for the log line
	// only. A cache miss leaves it empty; the rename goes ahead regardless.
	before := ""
	if ch, err := t.mgr.Channel(channelID); err == nil {
		before = ch.Name
	}
	reason := fmt.Sprintf("renamed by %s", userID)
	if _, err := t.mgr.ChannelEdit(channelID, &discordgo.ChannelEdit{Name: name}, reason); err != nil {
		return renamed{}, err
	}
	return renamed{ChannelID: channelID, Before: before, After: name}, nil
}

// isModeratorLocked reports whether the member's roles meet the effective
// moderator set of the spawned channel's hub: the union of the guild-wide
// roles and the hub's own. A channel whose hub row is gone has the guild-wide
// roles alone. Caller holds mu.
func (t *TempVC) isModeratorLocked(channelID string, memberRoles []string) bool {
	if len(memberRoles) == 0 {
		return false
	}
	held := make(map[string]struct{}, len(memberRoles))
	for _, r := range memberRoles {
		held[r] = struct{}{}
	}
	for _, r := range t.guildModeratorRoles {
		if _, ok := held[r]; ok {
			return true
		}
	}
	hubID := t.channelHub[channelID]
	for _, h := range t.hubs {
		if h.ID != hubID {
			continue
		}
		for _, r := range h.ModeratorRoleIDs {
			if _, ok := held[r]; ok {
				return true
			}
		}
	}
	return false
}
