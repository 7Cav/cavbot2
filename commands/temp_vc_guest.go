package commands

import (
	"fmt"
	"maps"
	"slices"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The guest list (spec #347, #350). Anyone who gets into a locked channel,
// however they got in, is a guest and can leave and come back: dragged in
// by a moderator, walking in on a moderator role or Administrator, or inside
// when the restart sweep finds the channel locked. A lock puts whoever is
// inside on the list in its own edit (lockOverwrites); everyone after that
// is added one member overwrite at a time, through addGuest. The list is
// never stored: it lives on Discord as member overwrites, and an unlock
// drops it by putting the permission source back.
//
// A join during an access change (a lock, unlock or let-in in flight,
// temp_vc_lock.go) waits for it to finish. Its guest add, sent at once,
// could land on either side of the change's edit: before a lock's, which
// replaces the whole list and drops it, or after an unlock's, which would
// carry the guest past the unlock. So the change holds the join
// (accessChange.joins), and when it ends the members it held become guests
// if the channel is locked then, and are dropped if not.

// guestReasonJoined and guestReasonSwept are the audit-log reasons of a
// guest add for a join and for the restart sweep.
const (
	guestReasonJoined = "joined while locked"
	guestReasonSwept  = "inside when the restart sweep ran"
)

// joinGuestsLocked decides what members joining a spawned channel mean for
// its guest list, and returns the ones to add now. A locked channel takes
// them all. A channel with an access change in flight holds them until it
// ends (endAccessChange). An unlocked or untracked channel takes nobody.
// Caller holds mu.
func (t *TempVC) joinGuestsLocked(channelID string, userIDs ...string) []string {
	if _, tracked := t.occupants[channelID]; !tracked || len(userIDs) == 0 {
		return nil
	}
	if change, inFlight := t.accessChanges[channelID]; inFlight {
		if change.joins == nil {
			change.joins = make(map[string]struct{}, len(userIDs))
		}
		for _, userID := range userIDs {
			change.joins[userID] = struct{}{}
		}
		return nil
	}
	if _, locked := t.locks[channelID]; !locked {
		return nil
	}
	return userIDs
}

// addGuests adds each member to a locked channel's guest list, off-lock.
// The stale voice state rule holds as for every other action: a member the
// cache no longer shows in the channel, or a guild missing from the cache,
// gets no add. A failed add is a WARN line and nothing else.
func (t *TempVC) addGuests(channelID string, userIDs []string, reason string) {
	for _, userID := range userIDs {
		if snap := t.mgr.VoiceStates(t.guildID); !snap.Present || snap.channelOf(userID) != channelID {
			utils.Info("Temp VC guest add skipped, member not confirmed in the channel",
				"channel_id", channelID, "user_id", userID)
			continue
		}
		added, err := t.addGuest(channelID, userID, reason)
		if err != nil {
			utils.Warn("Temp VC guest add failed", "channel_id", channelID, "user_id", userID, "error", err)
			continue
		}
		if added {
			utils.Info("Temp VC guest added", "channel_id", channelID, "user_id", userID)
		}
	}
}

// addGuest puts one member on a channel's guest list: a member overwrite
// that allows Connect, through one overwrite set carrying the audit-log
// reason. Discord replaces the member's whole overwrite, so the member's
// existing one is read from the state cache and kept, with Connect allowed
// and any Connect deny cleared; no other bit changes, so a guest never gains
// View Channel. A member whose overwrite already allows Connect is a guest
// already, and nothing is sent: added reports whether a set went out. A
// channel missing from the cache is an error, since the member's other bits
// cannot be kept.
func (t *TempVC) addGuest(channelID, userID, reason string) (added bool, err error) {
	ch, err := t.mgr.Channel(channelID)
	if err != nil {
		return false, fmt.Errorf("channel %s not in the state cache: %w", channelID, err)
	}
	guest := discordgo.PermissionOverwrite{ID: userID, Type: discordgo.PermissionOverwriteTypeMember}
	if i := slices.IndexFunc(ch.PermissionOverwrites, func(o *discordgo.PermissionOverwrite) bool {
		return o.ID == userID && o.Type == discordgo.PermissionOverwriteTypeMember
	}); i >= 0 {
		guest = *ch.PermissionOverwrites[i]
	}
	if guest.Allow&discordgo.PermissionVoiceConnect != 0 {
		return false, nil
	}
	allowConnect(&guest)
	if err := t.mgr.ChannelPermissionSet(channelID, userID, guest.Type, guest.Allow, guest.Deny, reason); err != nil {
		return false, err
	}
	return true, nil
}

// sortedIDs returns a set's IDs in order.
func sortedIDs(set map[string]struct{}) []string {
	return slices.Sorted(maps.Keys(set))
}
