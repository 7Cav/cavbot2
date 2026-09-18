package commands

import (
	"errors"
	"fmt"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Discord's rename limit per channel: renamesPerWindow inside renameWindow.
// The runtime counts renames itself and refuses the one that would exceed it.
const (
	renameWindow     = 10 * time.Minute
	renamesPerWindow = 2
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

// renameWindowError is returned when the channel already had its two renames
// inside the window. OpensAt is when the oldest of them leaves the window.
type renameWindowError struct {
	OpensAt time.Time
}

func (e *renameWindowError) Error() string {
	return "rename window closed until " + e.OpensAt.UTC().Format(time.RFC3339)
}

// errChannelGone is returned when Discord answered the edit with Unknown
// Channel: the channel was deleted while the invoker sat in it. The runtime
// has already untracked it and dropped its row.
var errChannelGone = errors.New("channel no longer exists")

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
	// The slot is taken before the edit and given back if the edit fails, so
	// two renames in flight on one channel cannot both count as the second.
	t.mu.Lock()
	now := tempVCNow()
	recent := t.renames[channelID]
	for len(recent) > 0 && now.Sub(recent[0]) >= renameWindow {
		recent = recent[1:]
	}
	if len(recent) >= renamesPerWindow {
		t.renames[channelID] = recent
		t.mu.Unlock()
		return renamed{}, &renameWindowError{OpensAt: recent[0].Add(renameWindow)}
	}
	t.renames[channelID] = append(recent, now)
	t.mu.Unlock()

	// The name before the edit comes from the state cache, for the log line
	// only. A cache miss leaves it empty; the rename goes ahead regardless.
	before := ""
	if ch, err := t.mgr.Channel(channelID); err == nil {
		before = ch.Name
	}
	reason := fmt.Sprintf("renamed by %s", userID)
	_, err := t.mgr.ChannelEdit(channelID, &discordgo.ChannelEdit{Name: name}, reason)
	if err != nil {
		return renamed{}, t.renameFailed(channelID, now, err)
	}
	t.mu.Lock()
	delete(t.renameCaptured, t.channelHub[channelID])
	t.mu.Unlock()
	return renamed{ChannelID: channelID, Before: before, After: name}, nil
}

// renameFailed gives back the rename slot and classifies the failure. Unknown
// Channel means the channel is already gone: it is untracked, its row is
// dropped, and nothing is captured. A 403, 5xx or transport failure captures
// once per streak per hub. Every failure is one WARN line. The error the
// handler gets carries no raw Discord body in its reply; the classifier
// there decides the phrase.
func (t *TempVC) renameFailed(channelID string, at time.Time, err error) error {
	fault := classifySpawnedChannelError(err)
	t.mu.Lock()
	t.releaseRenameLocked(channelID, at)
	hubID := t.channelHub[channelID]
	if fault.gone {
		t.untrackLocked(channelID)
	}
	t.mu.Unlock()

	if fault.gone {
		utils.Info("Temp VC channel already gone at rename, untracked", "channel_id", channelID)
		t.deleteRow(channelID)
		return errChannelGone
	}
	utils.Warn("Temp VC rename failed", "channel_id", channelID, "hub_id", hubID, "error", err)
	if fault.capturesOnRename() {
		t.captureOncePerStreak(t.renameCaptured, hubID, "Temp VC rename failed", err,
			"channel_id", channelID, "hub_id", hubID, "guild_id", t.guildID)
	}
	return err
}

// releaseRenameLocked gives back the slot a failed rename took, so only
// renames Discord accepted count against the window. Caller holds mu.
func (t *TempVC) releaseRenameLocked(channelID string, at time.Time) {
	recent := t.renames[channelID]
	for i := len(recent) - 1; i >= 0; i-- {
		if recent[i].Equal(at) {
			t.renames[channelID] = append(recent[:i], recent[i+1:]...)
			return
		}
	}
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
