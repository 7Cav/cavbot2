package commands

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/7cav/cavbot2/store"
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

// errNotInVoice: Rename returns it when the runtime has no current voice
// channel on record for the invoker (#317). A member with no record is in no
// voice channel: the record is reset at every GUILD_CREATE and kept current
// from every voice state event.
var errNotInVoice = errors.New("invoker is in no voice channel")

// notSpawnedChannelError: Rename returns it when the invoker sits in a voice
// channel the runtime does not track as spawned, so a hub channel or any
// permanent channel (#317). ChannelID is that channel, for the reply to name.
type notSpawnedChannelError struct {
	ChannelID string
}

func (e *notSpawnedChannelError) Error() string {
	return "channel " + e.ChannelID + " is not a spawned channel"
}

// notOwnerError: Rename returns it when the invoker holds no moderator role
// and is not the channel's owner. Owner is empty when the channel has none,
// and the handler logs that at WARN, since the Discord gate or the rank-role
// assumption has failed.
type notOwnerError struct {
	Owner string
}

func (e *notOwnerError) Error() string {
	if e.Owner == "" {
		return "channel has no owner"
	}
	return "invoker is not the owner " + e.Owner
}

// renameWindowError: Rename returns it when the channel already had its two
// renames inside the window, and when Discord refused the edit with a 429
// that carries retry_after (a rename the runtime did not count). OpensAt is
// when the oldest rename leaves the window, or the attempt time plus
// retry_after.
type renameWindowError struct {
	OpensAt time.Time
}

func (e *renameWindowError) Error() string {
	return "rename window closed until " + e.OpensAt.UTC().Format(time.RFC3339)
}

// errChannelGone: Rename returns it when Discord answered the edit with
// Unknown Channel, so someone deleted the channel while the invoker sat in
// it. The runtime has already untracked it and dropped its row.
var errChannelGone = errors.New("channel no longer exists")

// renameResult is what a successful Rename reports, for the handler's log
// line.
type renameResult struct {
	ChannelID string
	Before    string
	After     string
}

// Rename renames the spawned channel the invoker is sitting in, on the
// invoker's behalf. No channel argument: the target is the invoker's current
// channel from the runtime's own occupancy tracking. The edit call carries an
// audit log reason naming the invoker and no retry on rate limit.
func (t *TempVC) Rename(userID string, memberRoles []string, name string) (renameResult, error) {
	t.mu.Lock()
	channelID, inVoice := t.userChannel[userID]
	if !inVoice {
		t.mu.Unlock()
		return renameResult{}, errNotInVoice
	}
	if _, tracked := t.occupants[channelID]; !tracked {
		t.mu.Unlock()
		return renameResult{}, &notSpawnedChannelError{ChannelID: channelID}
	}
	// A moderator role passes for every spawned channel of a hub it covers,
	// whoever owns it and whether anyone does. The owner is read only when
	// the invoker holds none, so a moderator renames an ownerless channel
	// with no WARN, and a rename never changes the owner.
	if !t.isModeratorLocked(channelID, memberRoles) {
		if owner := t.owners[channelID]; owner != userID {
			t.mu.Unlock()
			return renameResult{}, &notOwnerError{Owner: owner}
		}
	}
	// The slot is taken before the edit and given back if the edit fails, so
	// two renames in flight on one channel cannot both count as the second.
	now := tempVCNow()
	recent := t.renames[channelID]
	for len(recent) > 0 && now.Sub(recent[0]) >= renameWindow {
		recent = recent[1:]
	}
	if len(recent) >= renamesPerWindow {
		t.renames[channelID] = recent
		t.mu.Unlock()
		return renameResult{}, &renameWindowError{OpensAt: recent[0].Add(renameWindow)}
	}
	t.renames[channelID] = append(recent, now)
	t.mu.Unlock()

	// The name before the edit comes from the state cache, for the log line
	// the spec asks for. A cache miss leaves it empty and the rename goes
	// ahead.
	before := ""
	if ch, err := t.mgr.Channel(channelID); err == nil {
		before = ch.Name
	}
	reason := fmt.Sprintf("renamed by %s", userID)
	_, err := t.mgr.ChannelEdit(channelID, &discordgo.ChannelEdit{Name: name}, reason)
	if err != nil {
		return renameResult{}, t.renameFailed(channelID, now, err)
	}
	t.mu.Lock()
	delete(t.renameCaptured, t.channelHub[channelID])
	t.mu.Unlock()
	return renameResult{ChannelID: channelID, Before: before, After: name}, nil
}

// renameFailed gives back the rename slot and classifies the failure. Unknown
// Channel means the channel is already gone, so the runtime untracks it,
// drops its row and captures nothing. A 403, 5xx or transport failure
// captures once per streak per hub. Every other failure is one WARN line.
// The handler renders the returned error without the raw Discord body.
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
	if fault.capturesOnDeleteOrRename() {
		t.captureOncePerStreak(t.renameCaptured, hubID, "Temp VC rename failed", err,
			"channel_id", channelID, "hub_id", hubID, "guild_id", t.guildID)
	}
	// A 429 is the limit the runtime counts against itself, so Discord's
	// retry_after is the wait the runtime would have computed, and the invoker
	// gets the window refusal with that wait (#264, #314). A 429 with no wait
	// stays the raw error, which the handler renders with no time.
	if fault.retryAfter > 0 {
		return &renameWindowError{OpensAt: at.Add(fault.retryAfter)}
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
// moderator set of the spawned channel's hub, the union of the guild-wide
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
	effective := t.guildModeratorRoles
	if hub, ok := t.hubByIDLocked(t.channelHub[channelID]); ok {
		effective = append(slices.Clone(effective), hub.ModeratorRoleIDs...)
	}
	for _, r := range effective {
		if _, ok := held[r]; ok {
			return true
		}
	}
	return false
}

// hubByIDLocked finds a hub by its row ID. hubs is keyed by hub channel ID,
// which is what a join carries; a spawned channel remembers its hub by row
// ID, which survives the hub channel being re-registered. Caller holds mu.
func (t *TempVC) hubByIDLocked(id int64) (store.Hub, bool) {
	for _, h := range t.hubs {
		if h.ID == id {
			return h, true
		}
	}
	return store.Hub{}, false
}
