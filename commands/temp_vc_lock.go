package commands

import (
	"errors"
	"fmt"
	"slices"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Locking a spawned channel (spec #347, #349). A lock turns the channel into
// a guest list: whoever is inside when it locks can leave and rejoin, the
// hub's moderator roles still get in, and everyone else sees Discord's
// padlock and cannot join or read the chat. Unlock puts the hub's permission
// source back, which drops every guest overwrite.
//
// The runtime owns the decision for the reason it owns renames: which
// spawned channel the invoker is in, who owns it, its hub and its lock all
// sit under its lock. The /voice-lock and /voice-unlock handlers in
// voice_lock.go call Lock and Unlock and render the outcome.
//
// The lock is the runtime's record and the channel's row, never something
// read back from Discord's permissions. A lock and an unlock are each one
// channel edit carrying the whole overwrite list, so a refused edit changes
// nothing. The record changes only once Discord accepts the edit, then the
// row follows through its own store call (SetSpawnedChannelLock), which no
// handover or other row write touches.

// errLockingNotAllowed: Lock returns it when the channel's hub has "Locking
// allowed" off, or its hub row is gone.
var errLockingNotAllowed = errors.New("locking is not allowed on the channel's hub")

// errAlreadyLocked: Lock returns it for a channel that is locked.
var errAlreadyLocked = errors.New("channel is already locked")

// errNotLocked: Unlock returns it for a channel that is not locked.
var errNotLocked = errors.New("channel is not locked")

// errLockInFlight: Lock and Unlock return it while another lock or unlock of
// the same channel is still in flight.
var errLockInFlight = errors.New("a lock or unlock of the channel is in flight")

// errChannelNotCached: Lock returns it when the state cache does not hold
// the channel, so its current overwrites cannot be read. Nothing is sent.
var errChannelNotCached = errors.New("channel is not in the state cache")

// errSourceUnreadable: Unlock wraps it when the hub's permission source
// cannot be read: the hub row is gone, or the hub channel or its category is
// missing from the cache. The channel stays locked (Q21): no fallback list
// could be trusted not to open it wider than its hub allowed.
var errSourceUnreadable = errors.New("the hub's permission source cannot be read")

// lockRecord is a locked channel's lock as the runtime holds it.
type lockRecord struct {
	// locker is the member who locked the channel.
	locker string
	// noticeMessageID is the lock notice's message ID, empty until a notice
	// is posted.
	noticeMessageID string
}

// row is the lock as the channel's row records it.
func (r lockRecord) row() store.ChannelLock {
	return store.ChannelLock{Locked: true, LockerUserID: r.locker, NoticeMessageID: r.noticeMessageID}
}

// lockResult is what a successful Lock or Unlock reports to its handler.
type lockResult struct {
	ChannelID string
	HubID     int64
}

// Lock locks the spawned channel the invoker sits in, on the invoker's
// behalf, when they own it or hold one of its hub's moderator roles and the
// hub has "Locking allowed" on. The edit carries an audit log reason naming
// the invoker and no retry on rate limit. A refused edit changes nothing.
func (t *TempVC) Lock(userID string, memberRoles []string) (lockResult, error) {
	t.mu.Lock()
	channelID, err := t.invokerChannelLocked(userID, memberRoles)
	if err != nil {
		t.mu.Unlock()
		return lockResult{}, err
	}
	hubID := t.channelHub[channelID]
	if hub, ok := t.hubByIDLocked(hubID); !ok || !hub.LockingAllowed {
		t.mu.Unlock()
		return lockResult{}, errLockingNotAllowed
	}
	if _, locked := t.locks[channelID]; locked {
		t.mu.Unlock()
		return lockResult{}, errAlreadyLocked
	}
	if _, busy := t.lockBusy[channelID]; busy {
		t.mu.Unlock()
		return lockResult{}, errLockInFlight
	}
	current, err := t.mgr.Channel(channelID)
	if err != nil {
		t.mu.Unlock()
		utils.Warn("Temp VC lock refused, channel not in the state cache",
			"channel_id", channelID, "hub_id", hubID, "user_id", userID, "error", err)
		return lockResult{}, errChannelNotCached
	}
	// The guest list is whoever is inside at this check. A member whose join
	// lands between here and the end of the lock is held and added when it
	// ends (joinGuestsLocked).
	guests := t.insideLocked(channelID)
	overwrites := lockOverwrites(current.PermissionOverwrites, t.guildID,
		t.effectiveModeratorRolesLocked(channelID), guests)
	t.lockBusy[channelID] = struct{}{}
	t.mu.Unlock()

	reason := fmt.Sprintf("locked by %s", userID)
	if _, err := t.mgr.ChannelEdit(channelID, &discordgo.ChannelEdit{PermissionOverwrites: overwrites}, reason); err != nil {
		return lockResult{}, t.lockEditFailed(channelID, "lock", err)
	}

	record := lockRecord{locker: userID}
	t.mu.Lock()
	_, tracked := t.occupants[channelID]
	if tracked {
		t.locks[channelID] = record
	}
	delete(t.lockCaptured, hubID)
	t.mu.Unlock()
	defer t.endLockOp(channelID)
	// The channel was deleted while the edit was in flight: its row went
	// with it, and there is nothing left to lock.
	if !tracked {
		return lockResult{}, errChannelGone
	}
	t.writeLock(channelID, record.row())
	utils.Info("Temp VC locked", "channel_id", channelID, "hub_id", hubID, "user_id", userID)
	return lockResult{ChannelID: channelID, HubID: hubID}, nil
}

// Unlock unlocks the spawned channel the invoker sits in, on the invoker's
// behalf, when they own it or hold one of its hub's moderator roles. It
// works whatever the hub's "Locking allowed" says now.
func (t *TempVC) Unlock(userID string, memberRoles []string) (lockResult, error) {
	return t.unlock(userID, func() (string, error) {
		return t.invokerChannelLocked(userID, memberRoles)
	})
}

// unlock puts a locked channel's permission source back, on userID's
// behalf. resolve names the channel and applies the caller's authority rule;
// it runs under mu. /voice-unlock resolves the invoker's own channel. A
// caller with another rule, a button naming its channel for instance,
// passes its own. The edit replaces the whole overwrite list with the
// source as the cache holds it now, which drops every guest overwrite.
func (t *TempVC) unlock(userID string, resolve func() (string, error)) (lockResult, error) {
	t.mu.Lock()
	channelID, err := resolve()
	if err != nil {
		t.mu.Unlock()
		return lockResult{}, err
	}
	if _, busy := t.lockBusy[channelID]; busy {
		t.mu.Unlock()
		return lockResult{}, errLockInFlight
	}
	if _, locked := t.locks[channelID]; !locked {
		t.mu.Unlock()
		return lockResult{}, errNotLocked
	}
	hubID := t.channelHub[channelID]
	hub, ok := t.hubByIDLocked(hubID)
	if !ok {
		t.mu.Unlock()
		utils.Warn("Temp VC unlock refused, hub row gone",
			"channel_id", channelID, "hub_id", hubID, "user_id", userID)
		return lockResult{}, fmt.Errorf("%w: hub row %d is gone", errSourceUnreadable, hubID)
	}
	source, err := t.permissionSourceOverwrites(hub)
	if err != nil {
		t.mu.Unlock()
		utils.Warn("Temp VC unlock refused, permission source unreadable",
			"channel_id", channelID, "hub_id", hubID, "user_id", userID, "error", err)
		return lockResult{}, fmt.Errorf("%w: %w", errSourceUnreadable, err)
	}
	overwrites := unlockOverwrites(source, t.guildID)
	t.lockBusy[channelID] = struct{}{}
	t.mu.Unlock()

	reason := fmt.Sprintf("unlocked by %s", userID)
	if _, err := t.mgr.ChannelEdit(channelID, &discordgo.ChannelEdit{PermissionOverwrites: overwrites}, reason); err != nil {
		return lockResult{}, t.lockEditFailed(channelID, "unlock", err)
	}

	t.mu.Lock()
	_, tracked := t.occupants[channelID]
	delete(t.locks, channelID)
	delete(t.lockCaptured, hubID)
	t.mu.Unlock()
	defer t.endLockOp(channelID)
	if !tracked {
		return lockResult{}, errChannelGone
	}
	t.writeLock(channelID, store.ChannelLock{})
	utils.Info("Temp VC unlocked", "channel_id", channelID, "hub_id", hubID, "user_id", userID)
	return lockResult{ChannelID: channelID, HubID: hubID}, nil
}

// insideLocked returns who is inside a spawned channel for a lock's guest
// list: the record's occupants and every member a fresh snapshot of the
// cache shows there, sorted. The union leaves out nobody whose join handler
// has not run yet, and a member whose leave has not landed is a guest who
// was inside a moment ago. Caller holds mu.
func (t *TempVC) insideLocked(channelID string) []string {
	inside := make(map[string]struct{}, len(t.occupants[channelID]))
	for userID := range t.occupants[channelID] {
		inside[userID] = struct{}{}
	}
	for userID, ch := range t.mgr.VoiceStates(t.guildID).ChannelByUser {
		if ch == channelID {
			inside[userID] = struct{}{}
		}
	}
	out := make([]string, 0, len(inside))
	for userID := range inside {
		out = append(out, userID)
	}
	slices.Sort(out)
	return out
}

// lockOverwrites builds a lock's overwrite list from the channel's current
// one:
//   - Connect denied on @everyone, whose overwrite ID is the guild's, added
//     when the channel has none;
//   - Connect denied on every other role overwrite except the moderator
//     roles';
//   - a Connect allow on each moderator role, added where the channel has no
//     overwrite for it;
//   - a member Connect allow for each guest, merged into the guest's own
//     overwrite when there is one.
//
// Discord applies @everyone first, then role allows over role denies, then
// the member, so a moderator or a guest still joins and every other role
// holder is stopped. The bot changes the Connect bit alone, inside
// TempVCOverwriteCeiling; every other bit an overwrite carried is kept. The
// current list is copied, never changed: it is the state cache's.
func lockOverwrites(current []*discordgo.PermissionOverwrite, everyoneID string, moderatorRoles, guests []string) []*discordgo.PermissionOverwrite {
	out := make([]*discordgo.PermissionOverwrite, 0, len(current)+len(moderatorRoles)+len(guests)+1)
	byID := make(map[string]*discordgo.PermissionOverwrite, cap(out))
	for _, o := range current {
		c := *o
		out = append(out, &c)
		byID[c.ID] = &c
	}
	ensure := func(id string, kind discordgo.PermissionOverwriteType) *discordgo.PermissionOverwrite {
		if o, ok := byID[id]; ok {
			return o
		}
		o := &discordgo.PermissionOverwrite{ID: id, Type: kind}
		out = append(out, o)
		byID[id] = o
		return o
	}
	for _, o := range out {
		if o.Type == discordgo.PermissionOverwriteTypeRole && !slices.Contains(moderatorRoles, o.ID) {
			denyConnect(o)
		}
	}
	denyConnect(ensure(everyoneID, discordgo.PermissionOverwriteTypeRole))
	for _, id := range moderatorRoles {
		allowConnect(ensure(id, discordgo.PermissionOverwriteTypeRole))
	}
	for _, id := range guests {
		allowConnect(ensure(id, discordgo.PermissionOverwriteTypeMember))
	}
	return out
}

// denyConnect denies Connect on one overwrite. Within one overwrite Discord
// applies the deny and then the allow, so the allow bit goes too.
func denyConnect(o *discordgo.PermissionOverwrite) {
	o.Deny |= discordgo.PermissionVoiceConnect
	o.Allow &^= discordgo.PermissionVoiceConnect
}

// allowConnect allows Connect on one overwrite and clears any Connect deny.
func allowConnect(o *discordgo.PermissionOverwrite) {
	o.Allow |= discordgo.PermissionVoiceConnect
	o.Deny &^= discordgo.PermissionVoiceConnect
}

// unlockOverwrites is the list an unlock sends: the permission source,
// copied. A source with no overwrites is sent as one @everyone overwrite
// that allows and denies nothing, the same permissions as an empty list.
// ChannelEdit drops an empty list from the request (omitempty), which would
// leave the lock in place.
func unlockOverwrites(source []*discordgo.PermissionOverwrite, everyoneID string) []*discordgo.PermissionOverwrite {
	if len(source) == 0 {
		return []*discordgo.PermissionOverwrite{{ID: everyoneID, Type: discordgo.PermissionOverwriteTypeRole}}
	}
	out := make([]*discordgo.PermissionOverwrite, len(source))
	for i, o := range source {
		c := *o
		out[i] = &c
	}
	return out
}

// lockEditFailed ends a lock or unlock whose edit Discord refused, and
// classifies the failure the way renameFailed does. Unknown Channel means
// the channel is already gone, so the runtime untracks it and drops its row
// with no capture. A 403, 5xx or transport failure captures once per streak
// per hub. Anything else is one WARN line. Nothing else changes: the edit
// carried the whole list, so Discord applied none of it, and the lock stays
// as it was. A member who joined while an unlock was in flight is a guest
// of the channel that stayed locked.
func (t *TempVC) lockEditFailed(channelID, action string, err error) error {
	fault := classifySpawnedChannelError(err)
	t.mu.Lock()
	hubID := t.channelHub[channelID]
	if fault.gone {
		t.untrackLocked(channelID)
	}
	held := t.endLockOpLocked(channelID)
	t.mu.Unlock()
	t.admitGuests(channelID, held, guestReasonJoined)

	if fault.gone {
		utils.Info("Temp VC channel already gone at "+action+", untracked", "channel_id", channelID)
		t.deleteRow(channelID)
		return errChannelGone
	}
	utils.Warn("Temp VC "+action+" failed", "channel_id", channelID, "hub_id", hubID, "error", err)
	if fault.capturesOnDeleteOrRename() {
		t.captureOncePerStreak(t.lockCaptured, hubID, "Temp VC "+action+" failed", err,
			"channel_id", channelID, "hub_id", hubID, "guild_id", t.guildID)
	}
	return err
}

// endLockOp ends a lock or unlock in flight. Whoever joined the channel
// meanwhile goes on its guest list if it is locked now.
func (t *TempVC) endLockOp(channelID string) {
	t.mu.Lock()
	held := t.endLockOpLocked(channelID)
	t.mu.Unlock()
	t.admitGuests(channelID, held, guestReasonJoined)
}

// writeLock records a spawned channel's lock on its row. A failed write
// captures, and the runtime keeps the lock it holds: its record stays the
// truth while the process lives, and the restart sweep keeps it on a
// reconnect. Only a restart reads the stale row.
func (t *TempVC) writeLock(channelID string, lock store.ChannelLock) {
	ctx, cancel := t.storeContext()
	defer cancel()
	if err := t.st.SetSpawnedChannelLock(ctx, channelID, lock); err != nil {
		captureError("Temp VC lock row write failed", err, "channel_id", channelID, "locked", lock.Locked)
	}
}

// restoreLockLocked sets a swept channel's lock. A channel the record
// tracked before the sweep keeps the lock the record held: every lock and
// unlock changes the record before its row, so the record is never the
// older of the two, and a failed row write must not undo a lock on a
// reconnect. Any other channel takes its row's lock, which on a restart is
// all there is. wasTracked and held are the record's occupants and locks
// from before the rebuild. Caller holds mu.
func (t *TempVC) restoreLockLocked(row store.SpawnedChannel, wasTracked map[string]map[string]struct{}, held map[string]lockRecord) {
	if _, known := wasTracked[row.ChannelID]; known {
		if lock, ok := held[row.ChannelID]; ok {
			t.locks[row.ChannelID] = lock
		}
		return
	}
	if row.Lock.Locked {
		t.locks[row.ChannelID] = lockRecord{locker: row.Lock.LockerUserID, noticeMessageID: row.Lock.NoticeMessageID}
	}
}
