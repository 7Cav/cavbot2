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

// errNotInVoice: Rename, Lock and Unlock return it when the runtime has no
// current voice channel on record for the invoker (#317). A member with no record is in no
// voice channel. The record is reset at every GUILD_CREATE and kept current
// from every voice state event.
var errNotInVoice = errors.New("invoker is in no voice channel")

// notSpawnedChannelError: Rename, Lock and Unlock return it when the invoker
// sits in a voice channel no hub created, so the hub channel itself or any other voice
// channel the runtime does not track as spawned (#317). ChannelID is that
// channel, for the reply to name.
type notSpawnedChannelError struct {
	ChannelID string
}

func (e *notSpawnedChannelError) Error() string {
	return "channel " + e.ChannelID + " is not a spawned channel"
}

// notOwnerError: Rename, Lock and Unlock return it when the invoker holds no
// moderator role and is not the channel's owner. Owner is empty when the channel has none,
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

// errChannelGone: Rename, Lock and Unlock return it when Discord answered the
// edit with Unknown Channel, so someone deleted the channel while the invoker
// sat in it. The runtime has already untracked it and dropped its row.
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
func (t *TempVC) Rename(by Invoker, name string) (renameResult, error) {
	t.mu.Lock()
	channelID, err := t.invokerChannelLocked(by)
	if err != nil {
		t.mu.Unlock()
		return renameResult{}, err
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
	reason := fmt.Sprintf("renamed by %s", by.auditName())
	if _, err := t.mgr.ChannelEdit(channelID, &discordgo.ChannelEdit{Name: name}, reason); err != nil {
		return renameResult{}, t.renameFailed(channelID, now, err)
	}
	t.mu.Lock()
	delete(t.renameCaptured, t.channelHub[channelID])
	t.mu.Unlock()
	return renameResult{ChannelID: channelID, Before: before, After: name}, nil
}

// renameFailed gives back the rename slot and classifies the failure as
// every change to a spawned channel is (channelChangeFailed). The handler
// renders the returned error without the raw Discord body.
func (t *TempVC) renameFailed(channelID string, at time.Time, err error) error {
	t.mu.Lock()
	t.releaseRenameLocked(channelID, at)
	t.mu.Unlock()
	if t.channelChangeFailed(channelID, "rename", t.renameCaptured, err) {
		return errChannelGone
	}
	// A 429 is the limit the runtime counts against itself, so Discord's
	// retry_after is the wait the runtime would have computed, and the invoker
	// gets the window refusal with that wait (#264, #314). A 429 with no wait
	// stays the raw error, which the handler renders with no time.
	if fault := classifySpawnedChannelError(err); fault.retryAfter > 0 {
		return &renameWindowError{OpensAt: at.Add(fault.retryAfter)}
	}
	return err
}

// channelChangeFailed classifies a change to a spawned channel that Discord
// refused: a rename, a lock or unlock edit, a guest add. Unknown Channel
// means the channel is already gone, so the runtime untracks it and drops
// its row with no capture, and gone is true. Any other failure is one WARN
// line, and a 403, 5xx or transport failure also captures, once per streak
// per hub: captured is the path's streak set, which the path's next success
// on one of the hub's channels opens again. kv adds fields to the line and
// the capture.
func (t *TempVC) channelChangeFailed(channelID, action string, captured map[int64]struct{}, err error, kv ...any) (gone bool) {
	fault := classifySpawnedChannelError(err)
	t.mu.Lock()
	hubID := t.channelHub[channelID]
	if fault.gone {
		t.untrackLocked(channelID)
	}
	t.mu.Unlock()

	if fault.gone {
		utils.Info("Temp VC channel already gone at "+action+", untracked", "channel_id", channelID)
		t.deleteRow(channelID)
		return true
	}
	fields := slices.Concat([]any{"channel_id", channelID, "hub_id", hubID}, kv)
	utils.Warn("Temp VC "+action+" failed", slices.Concat(fields, []any{"error", err})...)
	if fault.capturesOnChannelChange() {
		t.captureOncePerStreak(captured, hubID, "Temp VC "+action+" failed", err,
			slices.Concat(fields, []any{"guild_id", t.guildID})...)
	}
	return false
}

// Invoker is the member a voice command or a lock notice press acts for.
// Roles are the roles the interaction carries for them, which every
// authority check reads. Username is for audit log reasons alone.
type Invoker struct {
	UserID   string
	Username string
	Roles    []string
}

// auditName names the invoker in an audit log reason: the username an
// admin can read, then the ID, which stays true after a rename. Discord
// shows the bot as the actor, so the reason is the only place the member
// appears.
func (i Invoker) auditName() string {
	if i.Username == "" {
		return i.UserID
	}
	return i.Username + " (" + i.UserID + ")"
}

// invokerChannelLocked resolves the spawned channel a voice command acts on:
// the one the invoker sits in, when they own it or hold one of its hub's
// effective moderator roles. /voice-rename, /voice-lock and /voice-unlock
// share it, so the three refuse alike. A moderator role passes for every
// spawned channel of a hub it covers, whoever owns it and whether anyone
// does. The owner is read only when the invoker holds none, so a moderator
// acts on an ownerless channel with no WARN, and no command changes the
// owner. Caller holds mu.
func (t *TempVC) invokerChannelLocked(by Invoker) (string, error) {
	channelID, inVoice := t.userChannel[by.UserID]
	if !inVoice {
		return "", errNotInVoice
	}
	if _, tracked := t.occupants[channelID]; !tracked {
		return "", &notSpawnedChannelError{ChannelID: channelID}
	}
	if !t.isModeratorLocked(channelID, by.Roles) {
		if owner := t.owners[channelID]; owner != by.UserID {
			return "", &notOwnerError{Owner: owner}
		}
	}
	return channelID, nil
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
	for _, r := range t.effectiveModeratorRolesLocked(channelID) {
		if _, ok := held[r]; ok {
			return true
		}
	}
	return false
}

// effectiveModeratorRolesLocked returns the moderator roles of a spawned
// channel's hub as they are now: the guild-wide roles and the hub's own, or
// the guild-wide roles alone when the hub row is gone. A lock writes a
// Connect allow for each. The result is a fresh slice the caller may keep.
// Caller holds mu.
func (t *TempVC) effectiveModeratorRolesLocked(channelID string) []string {
	effective := slices.Clone(t.guildModeratorRoles)
	if hub, ok := t.hubByIDLocked(t.channelHub[channelID]); ok {
		effective = append(effective, hub.ModeratorRoleIDs...)
	}
	return effective
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
