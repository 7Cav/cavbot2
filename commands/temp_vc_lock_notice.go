package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The lock notice (spec #347, #351): the message a lock posts in the
// channel's text chat. It names who locked the channel, pings nobody, and
// carries the lock's buttons. At unlock, by a button or by /voice-unlock, it
// is edited to name who unlocked the channel and loses its buttons. Its
// message ID is stored with the lock, so the edit works after a restart.
//
// A button's CustomID follows ADR 0007: <command>::<action>::<channel ID>.
// The command is /voice-lock's, so main.go's dispatcher routes every press
// to runVoiceLock, which hands any component interaction to
// runLockNoticeComponent (voice_lock_notice.go) before it reads anything a
// slash command carries.
//
// The buttons answer to a moderator of the channel's hub, read live, and to
// the member who locked it (#347 Q20). Server Settings do not reach
// buttons, so that check is their only gate. The owner gets no pass here:
// an owner who did not lock the channel unlocks with /voice-unlock, which
// Server Settings do cover.

// The lock notice's button actions, the middle segment of a CustomID.
const (
	// lockNoticeUnlock is the Unlock button.
	lockNoticeUnlock = "unlock"
)

// lockNoticeButtons lists the lock notice's buttons, in the order they sit
// in its one row. Each shares the channel ID payload and the authority
// check in lockNoticeAuthorityLocked.
var lockNoticeButtons = []struct {
	action string
	label  string
	style  discordgo.ButtonStyle
}{
	{lockNoticeUnlock, "Unlock", discordgo.SecondaryButton},
}

// discordCustomIDLimit is Discord's cap on a component's CustomID, in
// characters. Discord refuses a message carrying a longer one.
const discordCustomIDLimit = 100

// customIDSeparator splits a CustomID into its segments. main.go's
// dispatcher splits on the same string (ADR 0007).
const customIDSeparator = "::"

// errNotLockerOrModerator: a lock notice button refuses it to anyone who
// neither holds a moderator role of the channel's hub nor locked it.
var errNotLockerOrModerator = errors.New("only a moderator or whoever locked the channel can use its lock notice")

// lockNoticeCustomID assembles a lock notice button's CustomID, and refuses
// one longer than Discord's cap: a truncated ID would misroute.
func lockNoticeCustomID(action, channelID string) (string, error) {
	id := strings.Join([]string{voiceLockCommandName, action, channelID}, customIDSeparator)
	if len(id) > discordCustomIDLimit {
		return "", fmt.Errorf("lock notice CustomID for channel %s is %d characters, over Discord's %d", channelID, len(id), discordCustomIDLimit)
	}
	return id, nil
}

// parseLockNoticeCustomID reads a lock notice button's action and channel
// ID back out of its CustomID. ok is false for any CustomID
// lockNoticeCustomID could not have built.
func parseLockNoticeCustomID(customID string) (action, channelID string, ok bool) {
	parts := strings.Split(customID, customIDSeparator)
	if len(parts) != 3 || parts[0] != voiceLockCommandName || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// lockNoticeComponents builds the lock notice's row of buttons for a
// channel. It errors when any CustomID would pass Discord's cap.
func lockNoticeComponents(channelID string) ([]discordgo.MessageComponent, error) {
	row := make([]discordgo.MessageComponent, 0, len(lockNoticeButtons))
	for _, b := range lockNoticeButtons {
		id, err := lockNoticeCustomID(b.action, channelID)
		if err != nil {
			return nil, err
		}
		row = append(row, discordgo.Button{Label: b.label, Style: b.style, CustomID: id})
	}
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: row}}, nil
}

// lockNoticeLine is the lock notice's content while the channel is locked.
func lockNoticeLine(locker string) string {
	return fmt.Sprintf("🔒 <@%s> locked this channel. The people inside can leave and come back, and the hub's moderators can join. Nobody else can. Unlock with the button below or /%s.",
		locker, voiceUnlockCommandName)
}

// lockNoticeUnlockedLine is the lock notice's content once the channel is
// unlocked.
func lockNoticeUnlockedLine(unlocker string) string {
	return fmt.Sprintf("🔓 <@%s> unlocked this channel.", unlocker)
}

// noMentions is the allowed mentions of every lock notice send and edit: a
// mention renders and pings nobody.
func noMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

// postLockNotice posts the lock notice in a channel's text chat and returns
// its message ID, or false when it was not posted. A failed post is a WARN
// line, as a failed ownership notice is: the lock itself has already
// happened and stands, and /voice-unlock still ends it.
func (t *TempVC) postLockNotice(channelID, locker string) (string, bool) {
	components, err := lockNoticeComponents(channelID)
	if err == nil {
		var msg *discordgo.Message
		msg, err = t.mgr.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Content:         lockNoticeLine(locker),
			Components:      components,
			AllowedMentions: noMentions(),
		})
		if err == nil {
			return msg.ID, true
		}
	}
	utils.Warn("Temp VC lock notice not sent",
		"channel_id", channelID, "locker_id", locker, "error", err)
	return "", false
}

// closeLockNotice edits an unlocked channel's lock notice to name who
// unlocked it, and removes its buttons. A lock whose notice never posted
// has no message ID, and a failed edit changes nothing: either is a WARN
// line and never holds up the unlock, which has already happened.
func (t *TempVC) closeLockNotice(channelID, messageID, unlocker string) {
	if messageID == "" {
		utils.Warn("Temp VC lock notice not edited, no message ID stored",
			"channel_id", channelID, "unlocker_id", unlocker)
		return
	}
	content := lockNoticeUnlockedLine(unlocker)
	_, err := t.mgr.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:              messageID,
		Channel:         channelID,
		Content:         &content,
		Components:      &[]discordgo.MessageComponent{},
		AllowedMentions: noMentions(),
	})
	if err != nil {
		utils.Warn("Temp VC lock notice not edited",
			"channel_id", channelID, "message_id", messageID, "unlocker_id", unlocker, "error", err)
	}
}

// lockNoticeAuthorityLocked applies the lock notice buttons' rule to a
// press on a channel's notice: the channel must be locked, and the presser
// must hold one of its hub's moderator roles now or be the member who
// locked it. A channel that is not locked refuses first, so a button left
// over from an earlier lock says so whoever presses it. Caller holds mu.
func (t *TempVC) lockNoticeAuthorityLocked(channelID, userID string, memberRoles []string) error {
	lock, locked := t.locks[channelID]
	if !locked {
		return errNotLocked
	}
	if t.isModeratorLocked(channelID, memberRoles) || (lock.locker != "" && lock.locker == userID) {
		return nil
	}
	return errNotLockerOrModerator
}

// unlockFromNotice unlocks the channel a lock notice's Unlock button names,
// on the presser's behalf, under the buttons' rule. The presser need not
// sit in the channel. Everything after the rule is /voice-unlock's unlock.
func (t *TempVC) unlockFromNotice(channelID, userID string, memberRoles []string) (lockResult, error) {
	return t.unlock(userID, func() (string, error) {
		return channelID, t.lockNoticeAuthorityLocked(channelID, userID, memberRoles)
	})
}
