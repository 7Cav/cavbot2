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
//
// A lock, an unlock and a let-in (temp_vc_let_in.go) each change who can
// join the channel, so they are access changes, and one channel has at most
// one in flight. One that arrives while another is in flight waits for it to
// end, then decides afresh and answers as it would have alone: a second
// lock hears "Already locked.", an unlock that met a lock unlocks. Waiting
// also keeps the order Discord sees: an unlock's edit never lands before a
// guest add that started first.

// errLockingNotAllowed: Lock returns it when the channel's hub has "Locking
// allowed" off, or its hub row is gone.
var errLockingNotAllowed = errors.New("locking is not allowed on the channel's hub")

// errAlreadyLocked: Lock returns it for a channel that is locked.
var errAlreadyLocked = errors.New("channel is already locked")

// errNotLocked: Unlock returns it for a channel that is not locked.
var errNotLocked = errors.New("channel is not locked")

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
	// NoticeFailed is set by a Lock whose lock notice did not post. The
	// lock stands all the same.
	NoticeFailed bool
}

// accessChange is a lock, unlock or let-in in flight on a spawned channel.
type accessChange struct {
	// done is closed when the change ends, which wakes every access change
	// waiting on the channel.
	done chan struct{}
	// joins holds the members who joined the channel while the change was
	// in flight. When it ends they go on the guest list if the channel is
	// locked then, and are dropped if not (endAccessChange).
	joins map[string]struct{}
}

// idleChannelLocked names the channel an access change acts on and waits
// until no other access change is in flight on it. resolve names the
// channel and applies the caller's authority rule. After a wait it runs
// again, because the invoker may have moved or lost a role meanwhile. On a
// nil error mu has been held since the channel was last seen idle, so the
// caller can begin its change (beginAccessChangeLocked) with nothing in
// between. Caller holds mu, which is released while waiting.
func (t *TempVC) idleChannelLocked(resolve func() (string, error)) (string, error) {
	for {
		channelID, err := resolve()
		if err != nil {
			return "", err
		}
		change, inFlight := t.accessChanges[channelID]
		if !inFlight {
			return channelID, nil
		}
		t.mu.Unlock()
		<-change.done
		t.mu.Lock()
	}
}

// beginAccessChangeLocked marks an access change in flight on an idle
// channel (idleChannelLocked). Whoever begins one ends it with
// endAccessChange. Caller holds mu.
func (t *TempVC) beginAccessChangeLocked(channelID string) {
	t.accessChanges[channelID] = &accessChange{done: make(chan struct{})}
}

// endAccessChange ends the access change in flight on a channel. The
// members who joined meanwhile go on the guest list first, if the channel is
// locked, while the change still holds the channel. So an unlock waiting on
// it sends its edit only after their guest adds are answered, and none of
// them lands after the unlock. A member who joins during those adds is held
// and added the same way. Then every access change waiting on the channel
// wakes.
func (t *TempVC) endAccessChange(channelID string) {
	t.mu.Lock()
	change := t.accessChanges[channelID]
	for len(change.joins) > 0 {
		held := sortedIDs(change.joins)
		change.joins = nil
		if _, locked := t.locks[channelID]; !locked {
			break
		}
		t.mu.Unlock()
		t.addGuests(channelID, held, guestReasonJoined)
		t.mu.Lock()
	}
	delete(t.accessChanges, channelID)
	t.mu.Unlock()
	close(change.done)
}

// Lock locks the spawned channel the invoker sits in, on the invoker's
// behalf, when they own it or hold one of its hub's moderator roles and the
// hub has "Locking allowed" on. The edit carries an audit log reason naming
// the invoker and no retry on rate limit. A refused edit changes nothing.
// Once Discord accepts it, the lock notice posts (temp_vc_lock_notice.go);
// a notice that does not post leaves the lock standing and sets
// NoticeFailed.
func (t *TempVC) Lock(by Invoker) (lockResult, error) {
	t.mu.Lock()
	channelID, err := t.idleChannelLocked(func() (string, error) {
		return t.authorizedInvokerChannelLocked(by)
	})
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
	current, err := t.mgr.Channel(channelID)
	if err != nil {
		t.mu.Unlock()
		utils.Warn("Temp VC lock refused, channel not in the state cache",
			"channel_id", channelID, "hub_id", hubID, "user_id", by.UserID, "error", err)
		return lockResult{}, errChannelNotCached
	}
	// The guest list is whoever is inside at this check. A member whose join
	// lands between here and the end of the lock is held and added when it
	// ends (joinGuestsLocked).
	guests := t.insideLocked(channelID)
	overwrites := lockOverwrites(current.PermissionOverwrites, t.guildID,
		t.effectiveModeratorRolesLocked(channelID), guests)
	t.beginAccessChangeLocked(channelID)
	t.mu.Unlock()
	defer t.endAccessChange(channelID)

	record := lockRecord{locker: by.UserID}
	reason := fmt.Sprintf("locked by %s", by.auditName())
	if err := t.sendLockEdit(channelID, hubID, "lock", reason, overwrites, func() {
		t.locks[channelID] = record
	}); err != nil {
		return lockResult{}, err
	}
	// The notice posts while the lock is still in flight, so the one row
	// write below carries its message ID, and a press that lands before the
	// ID is recorded waits for it.
	noticeID, posted := t.postLockNotice(channelID, by.UserID)
	record.noticeMessageID = noticeID
	t.mu.Lock()
	if _, held := t.locks[channelID]; held {
		t.locks[channelID] = record
	}
	t.mu.Unlock()
	t.writeLock(channelID, record.row())
	utils.Info("Temp VC locked", "channel_id", channelID, "hub_id", hubID, "user_id", by.UserID)
	return lockResult{NoticeFailed: !posted}, nil
}

// Unlock unlocks the spawned channel the invoker sits in, on the invoker's
// behalf, when they own it or hold one of its hub's moderator roles. It
// works whatever the hub's "Locking allowed" says now.
func (t *TempVC) Unlock(by Invoker) (lockResult, error) {
	return t.unlock(by, func() (string, error) {
		return t.authorizedInvokerChannelLocked(by)
	})
}

// unlock puts a locked channel's permission source back, on the invoker's
// behalf. resolve names the channel and applies the caller's authority rule;
// it runs under mu. /voice-unlock resolves the invoker's own channel. A
// caller with another rule, a button naming its channel for instance,
// passes its own. The edit replaces the whole overwrite list with the
// source as the cache holds it now, which drops every guest overwrite.
func (t *TempVC) unlock(by Invoker, resolve func() (string, error)) (lockResult, error) {
	t.mu.Lock()
	channelID, err := t.idleChannelLocked(resolve)
	if err != nil {
		t.mu.Unlock()
		return lockResult{}, err
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
			"channel_id", channelID, "hub_id", hubID, "user_id", by.UserID)
		return lockResult{}, fmt.Errorf("%w: hub row %d is gone", errSourceUnreadable, hubID)
	}
	source, err := t.permissionSourceOverwrites(hub)
	if err != nil {
		t.mu.Unlock()
		utils.Warn("Temp VC unlock refused, permission source unreadable",
			"channel_id", channelID, "hub_id", hubID, "user_id", by.UserID, "error", err)
		return lockResult{}, fmt.Errorf("%w: %w", errSourceUnreadable, err)
	}
	overwrites := copyOverwrites(source)
	t.beginAccessChangeLocked(channelID)
	t.mu.Unlock()
	defer t.endAccessChange(channelID)

	var noticeID string
	reason := fmt.Sprintf("unlocked by %s", by.auditName())
	if err := t.sendLockEdit(channelID, hubID, "unlock", reason, overwrites, func() {
		noticeID = t.locks[channelID].noticeMessageID
		delete(t.locks, channelID)
	}); err != nil {
		return lockResult{}, err
	}
	t.writeLock(channelID, store.ChannelLock{})
	t.closeLockNotice(channelID, noticeID, by.UserID)
	utils.Info("Temp VC unlocked", "channel_id", channelID, "hub_id", hubID, "user_id", by.UserID)
	return lockResult{}, nil
}

// sendLockEdit sends a lock's or an unlock's one edit, which replaces the
// channel's whole overwrite list and carries the audit log reason. Once
// Discord accepts it, the state cache holds the new list, commit applies
// the change to the record under mu, and the hub's capture streak opens
// again. errChannelGone reports a channel deleted while the edit was in
// flight, whose row went with it: nothing is left to lock or unlock. A
// refused edit changes nothing else, classified as every change to a
// spawned channel is (channelChangeFailed): it carried the whole list, so
// Discord applied none of it, and the lock stays as it was. A member who
// joined while an unlock was in flight is a guest of the channel that
// stayed locked (endAccessChange). Caller holds the channel's access
// change.
func (t *TempVC) sendLockEdit(channelID string, hubID int64, action, reason string, overwrites []*discordgo.PermissionOverwrite, commit func()) error {
	if err := t.mgr.ChannelOverwritesReplace(channelID, overwrites, reason); err != nil {
		if t.channelChangeFailed(channelID, action, t.lockCaptured, err) {
			return errChannelGone
		}
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.lockCaptured, hubID)
	if _, tracked := t.occupants[channelID]; !tracked {
		return errChannelGone
	}
	commit()
	return nil
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

// lockBits are the permission bits the bot writes into an overwrite of its
// own accord: Connect alone, which a lock denies and allows
// (lockOverwrites) and a guest add, the join's or a let-in's, allows
// (addGuest). denyLockBits and allowLockBits write them and nothing else.
const lockBits = discordgo.PermissionVoiceConnect

// lockBits must sit inside TempVCOverwriteCeiling. This constant fails to
// compile when they do not: a bit outside the ceiling makes the operand a
// negative constant, which overflows uint64.
const _ = uint64(-(lockBits &^ TempVCOverwriteCeiling))

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
// holder is stopped. The bot changes lockBits alone; every other bit an
// overwrite carried is kept. The current list is copied, never changed: it
// is the state cache's.
func lockOverwrites(current []*discordgo.PermissionOverwrite, everyoneID string, moderatorRoles, guests []string) []*discordgo.PermissionOverwrite {
	out := copyOverwrites(current)
	byID := make(map[string]*discordgo.PermissionOverwrite, len(out)+len(moderatorRoles)+len(guests)+1)
	for _, o := range out {
		byID[o.ID] = o
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
			denyLockBits(o)
		}
	}
	denyLockBits(ensure(everyoneID, discordgo.PermissionOverwriteTypeRole))
	for _, id := range moderatorRoles {
		allowLockBits(ensure(id, discordgo.PermissionOverwriteTypeRole))
	}
	for _, id := range guests {
		allowLockBits(ensure(id, discordgo.PermissionOverwriteTypeMember))
	}
	return out
}

// denyLockBits denies lockBits on one overwrite. Within one overwrite
// Discord applies the deny and then the allow, so the allow bits go too.
func denyLockBits(o *discordgo.PermissionOverwrite) {
	o.Deny |= lockBits
	o.Allow &^= lockBits
}

// allowLockBits allows lockBits on one overwrite and clears any deny of
// them.
func allowLockBits(o *discordgo.PermissionOverwrite) {
	o.Allow |= lockBits
	o.Deny &^= lockBits
}

// copyOverwrites copies an overwrite list entry by entry, so the copy can be
// changed and sent while the list it came from, the state cache's, stays as
// it was. An empty list copies to an empty list, which an unlock sends as
// it is.
func copyOverwrites(list []*discordgo.PermissionOverwrite) []*discordgo.PermissionOverwrite {
	out := make([]*discordgo.PermissionOverwrite, len(list))
	for i, o := range list {
		c := *o
		out[i] = &c
	}
	return out
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
