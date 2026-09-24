package commands

import (
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Letting someone in (spec #347, #352). The lock notice's Let someone in
// button answers with a member picker, a user select that takes up to 25
// members. Submitting it puts each picked member on the guest list with one
// guest add (addGuest), a member Connect allow and nothing more, then posts
// one chat line that pings the new guests and nobody else.
//
// It skips bots, current guests and anyone who cannot see the channel: a
// Connect allow never shows a member a channel the permission source hides
// from them (#347 Q8), so letting them in would promise what it cannot
// give. The visibility check takes the picked member's roles from the
// select's resolved data, which Discord fills at the submission, rather
// than from the state cache, which holds no offline member of a large
// guild.
//
// The button and the submission answer to the lock notice buttons' rule
// (lockNoticeAuthorityLocked), read live at each: a picker opened by a
// moderator who has since lost the role lets nobody in. Neither needs the
// hub's "Locking allowed": that setting gates new locks only (Q17).
//
// A let-in holds the channel as a lock or unlock does (lockBusy) until its
// guest adds are answered. An unlock pressed meanwhile is refused with "try
// again": its edit could land before a guest add, which would then carry
// the guest past the unlock. A let-in submitted while a lock or unlock is
// in flight is refused the same way.

// letInMaxPicks is how many members the picker takes at once, Discord's
// maximum for a select (#347 Q15).
const letInMaxPicks = 25

// letInPick is one member picked in the Let someone in picker, as the
// submission carries them.
type letInPick struct {
	userID string
	// bot is set for a bot account.
	bot bool
	// resolved is set when the submission carries the member's guild data,
	// and roles are then the roles they hold. A user with none is not a
	// member of the guild and sees none of its channels.
	resolved bool
	roles    []string
}

// letInSkipReason is why a picked member was left off the guest list on
// purpose.
type letInSkipReason int

const (
	// skipBot: a bot account.
	skipBot letInSkipReason = iota + 1
	// skipGuest: already on the guest list.
	skipGuest
	// skipCannotSee: the member cannot see the channel.
	skipCannotSee
)

// letInSkipped is one picked member left off, and why.
type letInSkipped struct {
	UserID string
	Reason letInSkipReason
}

// letInResult is what a let-in did with each picked member.
type letInResult struct {
	// LetIn lists the new guests, in pick order.
	LetIn []string
	// Skipped lists the members left off on purpose.
	Skipped []letInSkipped
	// Failed lists the members the bot could not let in: Discord refused
	// the guest add, or the visibility check failed.
	Failed []string
	// PingFailed is set when the chat line naming the new guests did not
	// post. They are guests all the same.
	PingFailed bool
}

// letInPicks reads the picked members off a picker submission.
func letInPicks(data discordgo.MessageComponentInteractionData) []letInPick {
	picks := make([]letInPick, 0, len(data.Values))
	for _, id := range data.Values {
		p := letInPick{userID: id}
		if u := data.Resolved.Users[id]; u != nil {
			p.bot = u.Bot
		}
		if m := data.Resolved.Members[id]; m != nil {
			p.resolved = true
			p.roles = m.Roles
		}
		picks = append(picks, p)
	}
	return picks
}

// letInPicker builds the member picker for a channel: one row holding a
// user select whose CustomID names the channel. It errors when the
// CustomID would pass Discord's cap.
func letInPicker(channelID string) ([]discordgo.MessageComponent, error) {
	id, err := lockNoticeCustomID(lockNoticeLetInPick, channelID)
	if err != nil {
		return nil, err
	}
	one := 1
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.SelectMenu{
			MenuType:    discordgo.UserSelectMenu,
			CustomID:    id,
			Placeholder: "Members to let in",
			MinValues:   &one,
			MaxValues:   letInMaxPicks,
		},
	}}}, nil
}

// letInAllowed applies the lock notice buttons' rule to a Let someone in
// press, before the picker is offered.
func (t *TempVC) letInAllowed(channelID string, by Invoker) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lockNoticeAuthorityLocked(channelID, by)
}

// letIn puts the picked members on a locked channel's guest list, on the
// presser's behalf, under the lock notice buttons' rule. Each guest add
// carries an audit log reason naming the presser. It returns an error only
// when it let nobody in because of the channel or the presser; what
// happened to each member is in the result.
func (t *TempVC) letIn(channelID string, by Invoker, picks []letInPick) (letInResult, error) {
	t.mu.Lock()
	if err := t.lockNoticeAuthorityLocked(channelID, by); err != nil {
		t.mu.Unlock()
		return letInResult{}, err
	}
	if _, busy := t.lockBusy[channelID]; busy {
		t.mu.Unlock()
		return letInResult{}, errLockInFlight
	}
	hubID := t.channelHub[channelID]
	t.lockBusy[channelID] = struct{}{}
	t.mu.Unlock()

	var res letInResult
	reason := fmt.Sprintf("let in by %s", by.UserID)
	seen := make(map[string]struct{}, len(picks))
	for _, p := range picks {
		if _, dup := seen[p.userID]; dup {
			continue
		}
		seen[p.userID] = struct{}{}
		t.letInOne(channelID, &res, p, reason)
	}
	t.endLockOp(channelID)

	if len(res.LetIn) > 0 {
		res.PingFailed = !t.postLetInPing(channelID, by.UserID, res.LetIn)
	}
	utils.Info("Temp VC let in", "channel_id", channelID, "hub_id", hubID, "user_id", by.UserID,
		"let_in", res.LetIn, "skipped", len(res.Skipped), "failed", res.Failed)
	return res, nil
}

// letInOne decides one member picked to be let into a channel and records
// the outcome in res: a bot, a member who cannot see the channel and a
// current guest are skipped, anyone else gets a guest add. A failed check
// or add is a WARN line.
func (t *TempVC) letInOne(channelID string, res *letInResult, p letInPick, reason string) {
	skip := func(why letInSkipReason) {
		res.Skipped = append(res.Skipped, letInSkipped{UserID: p.userID, Reason: why})
	}
	if p.bot {
		skip(skipBot)
		return
	}
	if !p.resolved {
		skip(skipCannotSee)
		return
	}
	sees, err := t.mgr.CanSeeChannel(channelID, p.userID, p.roles)
	if err != nil {
		utils.Warn("Temp VC let-in skipped, visibility check failed",
			"channel_id", channelID, "user_id", p.userID, "error", err)
		res.Failed = append(res.Failed, p.userID)
		return
	}
	if !sees {
		skip(skipCannotSee)
		return
	}
	added, err := t.addGuest(channelID, p.userID, reason)
	switch {
	case err != nil:
		utils.Warn("Temp VC guest add failed", "channel_id", channelID, "user_id", p.userID, "error", err)
		res.Failed = append(res.Failed, p.userID)
	case !added:
		skip(skipGuest)
	default:
		res.LetIn = append(res.LetIn, p.userID)
	}
}

// postLetInPing posts the chat line naming the new guests and letInBy, who
// let them in. It pings the guests and nobody else, so letInBy is named
// without a ping. It reports whether the line posted; a failure is a WARN
// line.
func (t *TempVC) postLetInPing(channelID, letInBy string, guests []string) bool {
	_, err := t.mgr.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: letInLine(letInBy, guests),
		AllowedMentions: &discordgo.MessageAllowedMentions{
			Parse: []discordgo.AllowedMentionType{},
			Users: append([]string(nil), guests...),
		},
	})
	if err != nil {
		utils.Warn("Temp VC let-in ping not sent", "channel_id", channelID, "user_id", letInBy, "error", err)
		return false
	}
	return true
}

// letInLine is the chat line posted when letInBy lets the guests in.
func letInLine(letInBy string, guests []string) string {
	return fmt.Sprintf("🚪 <@%s> let in %s. You can join now.", letInBy, mentionList(guests))
}

// mentionList renders user IDs as mentions joined into one list.
func mentionList(userIDs []string) string {
	mentions := make([]string, len(userIDs))
	for i, id := range userIDs {
		mentions[i] = "<@" + id + ">"
	}
	return strings.Join(mentions, ", ")
}
