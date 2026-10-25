package commands

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// The winged aviators (#869, GLOSSARY.md): the Winged Aviator role follows
// each winged aviator's milpac. The bot takes the role on a discharge and
// gives it back when the discharge ends, and the status lasts until a person
// removes the role. The store holds the list, and every decision reads it
// there.

// The audit log reasons of the role changes the bot makes by itself, one
// per trigger. A change /winged-aviator makes names who ran it instead.
const (
	wingedAviatorDischargeReason = "Winged aviator discharged"
	wingedAviatorReEnlistReason  = "Winged aviator re-enlisted"
	wingedAviatorRejoinReason    = "Winged aviator rejoined the server"
	wingedAviatorStartupReason   = "Winged aviator check at startup"
)

// wingedAviatorReadTimeout bounds one milpac read.
const wingedAviatorReadTimeout = 10 * time.Second

// wingedAviatorStoreTimeout bounds one store call.
const wingedAviatorStoreTimeout = 5 * time.Second

// wingedAviatorAfterFunc schedules the runtime's waits: the poll for the
// member list and the hourly pass. Each runtime takes it when it is built,
// and the tests swap it to run the waits by hand.
var wingedAviatorAfterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }

// WingedAviatorGuild is the subset of GuildManager the runtime changes the
// role through, with the audit log reason each change carries.
type WingedAviatorGuild interface {
	GuildMemberRoleAdd(guildID, userID, roleID, auditReason string) error
	GuildMemberRoleRemove(guildID, userID, roleID, auditReason string) error
}

// WingedAviatorStore is the subset of the bot store that holds the list.
type WingedAviatorStore interface {
	IsWingedAviator(ctx context.Context, guildID, userID string) (bool, error)
	ListWingedAviators(ctx context.Context, guildID string) ([]string, error)
	AddWingedAviator(ctx context.Context, guildID, userID string) error
	RemoveWingedAviator(ctx context.Context, guildID, userID string) error
}

// WingedAviatorMembers reads the member list, which the passes check the
// winged aviators against.
type WingedAviatorMembers interface {
	MemberList(guildID string) MemberListSnapshot
}

// WingedAviators is the runtime that keeps the Winged Aviator role on the
// winged aviators. It handles one member at a time, so a command run, a
// gateway event and a pass never decide about the same member at once.
type WingedAviators struct {
	guild     WingedAviatorGuild
	members   WingedAviatorMembers
	store     WingedAviatorStore
	guildID   string
	roleID    string
	afterFunc func(time.Duration, func())

	mu sync.Mutex
	// locks holds one lock per member the runtime has decided about.
	locks map[string]*sync.Mutex
	// moving notes, per member, whether the bot's own last role change
	// left them holding the role, until the update it causes arrives. That
	// update is the bot's, not a hand edit, so a removal for a discharge
	// doesn't end the status.
	moving map[string]bool
}

// NewWingedAviators builds the runtime over the bot store, or returns nil
// when roleID is empty, and logs that winged aviators are off. Every method
// of a nil runtime does nothing, and /winged-aviator refuses.
func NewWingedAviators(guild WingedAviatorGuild, members WingedAviatorMembers, st WingedAviatorStore, guildID, roleID string) *WingedAviators {
	if roleID == "" {
		utils.Warn("WINGED_AVIATOR_ROLE_ID not set, winged aviators off")
		return nil
	}
	return &WingedAviators{
		guild:     guild,
		members:   members,
		store:     st,
		guildID:   guildID,
		roleID:    roleID,
		afterFunc: wingedAviatorAfterFunc,
		locks:     map[string]*sync.Mutex{},
		moving:    map[string]bool{},
	}
}

// lock takes the member's lock and returns its unlock.
func (w *WingedAviators) lock(userID string) (unlock func()) {
	w.mu.Lock()
	l, ok := w.locks[userID]
	if !ok {
		l = &sync.Mutex{}
		w.locks[userID] = l
	}
	w.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Listen hears the session's member events: a role change and a rejoin
// move the role. main registers it before the session opens, so no event is
// missed. A nil runtime registers nothing.
func (w *WingedAviators) Listen(dg *discordgo.Session) {
	if w == nil {
		return
	}
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.GuildMemberUpdate) {
		defer utils.RecoverPanic("winged-aviator-member-update")
		w.memberUpdated(e)
	})
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.GuildMemberAdd) {
		defer utils.RecoverPanic("winged-aviator-member-add")
		w.memberJoined(e)
	})
}

// Start runs the startup pass once the member list is complete. It
// records every role holder the store doesn't list as a winged aviator,
// then checks every winged aviator in the server against their milpac.
func (w *WingedAviators) Start() {
	if w == nil {
		return
	}
	w.passWhenListed(true)
}

// wingedAviatorListPoll is how often a pass reads the member list while it
// waits for it.
const wingedAviatorListPoll = 5 * time.Second

// wingedAviatorPassEvery is how long after one pass the next runs.
const wingedAviatorPassEvery = time.Hour

// passWhenListed runs a pass once the member list is complete with the bot
// connected, then schedules the next an hour on. A restart fetches the
// list again, and a pass over a list still arriving would find nobody in
// the server.
func (w *WingedAviators) passWhenListed(startup bool) {
	if !listReady(w.members.MemberList(w.guildID)) {
		w.afterFunc(wingedAviatorListPoll, func() { w.passWhenListed(startup) })
		return
	}
	w.pass(startup)
	w.afterFunc(wingedAviatorPassEvery, func() { w.passWhenListed(false) })
}

// pass checks every winged aviator in the member list against their
// milpac. The startup pass records every role holder first, and gives the
// role back by the startup reason, since it can't tell what happened while
// the bot was down. Any later pass gives it by the re-enlistment reason.
func (w *WingedAviators) pass(startup bool) {
	defer utils.RecoverPanic("winged-aviator-pass")
	list := w.members.MemberList(w.guildID)
	listed := make(map[string]ListedMember, len(list.Members))
	for _, m := range list.Members {
		listed[m.ID] = m
	}
	giveReason := wingedAviatorReEnlistReason
	if startup {
		giveReason = wingedAviatorStartupReason
		for _, m := range list.Members {
			if slices.Contains(m.RoleIDs, w.roleID) {
				w.recordHolder(m.ID)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), wingedAviatorStoreTimeout)
	ids, err := w.store.ListWingedAviators(ctx, w.guildID)
	cancel()
	if err != nil {
		captureError("Winged aviators unreadable for a pass", err, "startup", startup)
		return
	}
	for _, id := range ids {
		m, inServer := listed[id]
		if !inServer {
			continue
		}
		func() {
			defer w.lock(id)()
			if w.isWingedAviator(id) {
				w.check(id, slices.Contains(m.RoleIDs, w.roleID), giveReason)
			}
		}()
	}
}

// recordHolder makes a holder of the role a winged aviator when the store
// doesn't list them.
func (w *WingedAviators) recordHolder(userID string) {
	defer w.lock(userID)()
	if !w.isWingedAviator(userID) {
		_ = w.record(userID)
	}
}

// memberJoined gives the role back to a winged aviator who rejoins the
// server, unless their milpac shows a discharge.
func (w *WingedAviators) memberJoined(e *discordgo.GuildMemberAdd) {
	if w == nil || e.Member == nil || e.User == nil || e.GuildID != w.guildID {
		return
	}
	id := e.User.ID
	defer w.lock(id)()
	if !w.isWingedAviator(id) {
		return
	}
	w.check(id, slices.Contains(e.Roles, w.roleID), wingedAviatorRejoinReason)
}

// memberUpdated handles a member's GUILD_MEMBER_UPDATE. A role given by
// hand makes the member a winged aviator, and a role removed by hand ends
// the status. When a winged aviator's roles change, the bot reads their
// milpac again and makes the role follow it: the forum moves their roles on
// a discharge and on a re-enlistment.
func (w *WingedAviators) memberUpdated(e *discordgo.GuildMemberUpdate) {
	if w == nil || e.Member == nil || e.User == nil || e.GuildID != w.guildID {
		return
	}
	id := e.User.ID
	u := roleUpdate{holds: slices.Contains(e.Roles, w.roleID), known: e.BeforeUpdate != nil}
	u.held = u.known && slices.Contains(e.BeforeUpdate.Roles, w.roleID)
	defer w.lock(id)()
	if w.ownChange(id, u) {
		return
	}
	aviator := w.isWingedAviator(id)
	switch {
	case u.holds && !aviator:
		if w.record(id) != nil {
			return
		}
		aviator = true
	case u.held && !u.holds && aviator:
		_ = w.unrecord(id)
		return
	}
	if !aviator || u.known && sameRoles(e.BeforeUpdate.Roles, e.Roles) {
		return
	}
	w.check(id, u.holds, wingedAviatorReEnlistReason)
}

// roleUpdate is what a member's GUILD_MEMBER_UPDATE says about the Winged
// Aviator role: whether they hold it after the update, and whether they
// held it before, when the update says. discordgo knows the roles before
// only for a member its cache held.
type roleUpdate struct {
	holds, held, known bool
}

// still reports whether the update surely left the role as it was.
func (u roleUpdate) still() bool {
	return u.known && u.held == u.holds
}

// ownChange reports whether the update is the one the bot's own last role
// change caused, and forgets the note when it is. An update that surely
// moved the role the other way forgets the note too: the change it noted
// never came. Caller holds the member's lock.
func (w *WingedAviators) ownChange(userID string, u roleUpdate) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	leftHolding, noted := w.moving[userID]
	switch {
	case !noted || u.still():
		return false
	case leftHolding == u.holds:
		delete(w.moving, userID)
		return true
	case u.known:
		delete(w.moving, userID)
	}
	return false
}

// sameRoles reports whether a and b hold the same roles, in any order.
func sameRoles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, role := range a {
		if !slices.Contains(b, role) {
			return false
		}
	}
	return true
}

// isWingedAviator reads the member's status from the store. A read that fails
// counts as no status, so the bot changes nothing for them.
func (w *WingedAviators) isWingedAviator(userID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), wingedAviatorStoreTimeout)
	defer cancel()
	is, err := w.store.IsWingedAviator(ctx, w.guildID, userID)
	if err != nil {
		captureError("Winged aviator status unreadable", err, "discord_id", userID)
		return false
	}
	return is
}

// check reads a winged aviator's milpac and makes the role follow it: it
// takes the role on a discharge and gives it, for giveReason, when there is
// none. holds is whether they hold the role now. A read that fails changes
// nothing. Caller holds the member's lock.
func (w *WingedAviators) check(userID string, holds bool, giveReason string) {
	read := w.readMilpac(userID)
	if read == milpacUnread {
		return
	}
	discharged := read == milpacDischarged
	// A role change Discord refuses has gone to Sentry, and the next check
	// tries again.
	switch {
	case discharged && holds:
		_ = w.setRole(userID, false, wingedAviatorDischargeReason)
	case !discharged && !holds:
		_ = w.setRole(userID, true, giveReason)
	}
}

// milpacRead is what a read of a member's milpac found.
type milpacRead int

const (
	// milpacUnread is a read that failed.
	milpacUnread milpacRead = iota
	// milpacNone is no milpac naming the member. No discharge can reach
	// them.
	milpacNone
	// milpacActive is a milpac that shows no discharge.
	milpacActive
	// milpacDischarged is a milpac that shows a discharge.
	milpacDischarged
)

// readMilpac reads the member's milpac by their Discord ID.
func (w *WingedAviators) readMilpac(userID string) milpacRead {
	ctx, cancel := context.WithTimeout(context.Background(), wingedAviatorReadTimeout)
	defer cancel()
	milpac, err := utils.GetMilpacByDiscordID(ctx, userID)
	switch {
	case errors.Is(err, utils.ErrNotFound):
		return milpacNone
	case err != nil:
		captureError("Winged aviator milpac unreadable, role unchanged", err, "discord_id", userID)
		return milpacUnread
	case isDischarge(milpac.Primary.PositionTitle):
		return milpacDischarged
	default:
		return milpacActive
	}
}

// isDischarge reports whether a primary position is a discharge
// (GLOSSARY.md). Retired is not one.
func isDischarge(position string) bool {
	return position == "Discharged" || position == "Dishonorably Discharged"
}

// setRole leaves the member holding the role, or not, for reason: it
// gives the role or takes it. Caller holds the member's lock.
func (w *WingedAviators) setRole(userID string, holds bool, reason string) error {
	w.mu.Lock()
	w.moving[userID] = holds
	w.mu.Unlock()
	var err error
	if holds {
		err = w.guild.GuildMemberRoleAdd(w.guildID, userID, w.roleID, reason)
	} else {
		err = w.guild.GuildMemberRoleRemove(w.guildID, userID, w.roleID, reason)
	}
	if err != nil {
		w.mu.Lock()
		delete(w.moving, userID)
		w.mu.Unlock()
		reportRoleFailure(userID, holds, reason, err)
		return err
	}
	utils.Info("Winged Aviator role changed", "discord_id", userID, "holds", holds, "reason", reason)
	return nil
}

// reportRoleFailure files a role change Discord refused in the Sentry
// issue of its kind, since every event from this one site shares a stack
// trace. A member no longer in the server is no failure: a rejoin brings
// the role back.
func reportRoleFailure(userID string, holds bool, reason string, err error) {
	kv := []any{"discord_id", userID, "holds", holds, "reason", reason}
	class := classifyDiscordError(err)
	var kind string
	switch {
	case class.NotFound:
		utils.Info("Winged Aviator role unchanged, member not in the server", kv...)
		return
	case class.MissingPermissions:
		kind = "missing-permissions"
	case class.ConfigFault:
		kind = "unknown-role"
	case class.SystemFault:
		kind = "discord-error"
	default:
		kind = "rejected"
	}
	utils.CaptureErrorIn(utils.SentryIssue{Fingerprint: []string{"winged-aviator-role-change", kind}},
		"Winged Aviator role change failed", err, kv...)
}

// wingedAviatorCommandName is the name /winged-aviator registers under.
const wingedAviatorCommandName = "winged-aviator"

// The phrases that tell /winged-aviator's replies apart.
const (
	aviatorAddedPhrase      = "is now a winged aviator"
	aviatorNoMilpacPhrase   = "No milpac names"
	aviatorReadFailedPhrase = "Couldn't read"
	aviatorNotGivenPhrase   = "Couldn't give"
	aviatorNotSavedPhrase   = "Couldn't save"
	aviatorAlreadyPhrase    = "is already a winged aviator"
	aviatorRemovedPhrase    = "is no longer a winged aviator"
	aviatorNeitherPhrase    = "isn't a winged aviator"
	aviatorNotTakenPhrase   = "Couldn't take"
	// aviatorReEnlistPhrase ends the reply for a winged aviator
	// whose milpac shows a discharge.
	aviatorReEnlistPhrase = "The Winged Aviator role comes when they re-enlist."
)

// wingedAviatorsOffRefusal answers /winged-aviator on a bot with no
// Winged Aviator role configured, or no bot store.
const wingedAviatorsOffRefusal = "❌ Winged aviators aren't set up on this bot."

// aviatorReply is one /winged-aviator outcome: what the invoker reads, and
// how it goes out.
type aviatorReply struct {
	text string
	kind aviatorReplyKind
}

// aviatorReplyKind is how a /winged-aviator reply goes out.
type aviatorReplyKind int

const (
	aviatorDone aviatorReplyKind = iota
	aviatorRefused
	aviatorFailed
)

// WingedAviator declares /winged-aviator over the runtime, nil when winged
// aviators are off. It sets no default member permissions: Discord's
// Server Settings open it to the aviation staff.
func WingedAviator(wa *WingedAviators) Command {
	member := []*discordgo.ApplicationCommandOption{{
		Type:        discordgo.ApplicationCommandOptionUser,
		Name:        "member",
		Description: "The member",
		Required:    true,
	}}
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        wingedAviatorCommandName,
			Description: "Give or take the Winged Aviator role",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "add",
					Description: "Make a member a winged aviator and give them the Winged Aviator role",
					Options:     member,
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "remove",
					Description: "End a member's status as a winged aviator and take the Winged Aviator role",
					Options:     member,
				},
			},
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			runWingedAviator(utils.NewSessionResponder(s), wa, i)
		},
	}
}

// runWingedAviator is the /winged-aviator handler behind the responder
// seam. It defers a reply only the invoker sees, so every outcome edits
// that one reply.
func runWingedAviator(r utils.InteractionResponder, wa *WingedAviators, interaction *discordgo.InteractionCreate) {
	if err := deferEphemeral(r, interaction); err != nil {
		replyAckFailed(r, interaction, err)
		return
	}
	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting Winged Aviator", "command", wingedAviatorCommandName, "username", username, "discord_id", discordID)

	if wa == nil {
		utils.Warn("Winged Aviator refused, winged aviators off", "command", wingedAviatorCommandName, "discord_id", discordID)
		refuse(r, interaction, wingedAviatorsOffRefusal)
		return
	}
	data := interaction.ApplicationCommandData()
	sub := data.Options[0]
	target := sub.Options[0].UserValue(nil).ID
	by := Invoker{UserID: discordID, Username: username}
	reason := "/" + wingedAviatorCommandName + " " + sub.Name + " by " + by.auditName()

	// Discord's member picker resolves a member in the server with their
	// roles.
	holds := false
	if data.Resolved != nil && data.Resolved.Members[target] != nil {
		holds = slices.Contains(data.Resolved.Members[target].Roles, wa.roleID)
	}
	var reply aviatorReply
	if sub.Name == "remove" {
		reply = wa.remove(target, holds, reason)
	} else {
		reply = wa.add(target, holds, reason)
	}
	switch reply.kind {
	case aviatorRefused:
		refuse(r, interaction, reply.text)
		return
	case aviatorFailed:
		replyError(r, interaction, reply.text)
		return
	}
	editEphemeral(r, interaction, reply.text)
	utils.Info("✨ Done!", "command", wingedAviatorCommandName)
}

// add makes the member a winged aviator, as /winged-aviator add, once
// their milpac is read. A member with no discharge gets the role, and
// becomes one only if Discord gives it. One whose milpac shows a discharge
// becomes one without it. holds is whether they hold the role as the
// command resolved them.
func (w *WingedAviators) add(userID string, holds bool, reason string) aviatorReply {
	defer w.lock(userID)()
	read := w.readMilpac(userID)
	if read == milpacUnread {
		return aviatorReply{kind: aviatorFailed, text: "❌ " + aviatorReadFailedPhrase + " " + mention(userID) +
			"'s milpac, so nothing changed. Try again in a moment."}
	}
	already := w.isWingedAviator(userID)
	// A winged aviator no milpac names, as one given the role by hand can
	// be, has no discharge, so the command goes on for them.
	if read == milpacNone && !already {
		return aviatorReply{kind: aviatorRefused, text: "❌ " + aviatorNoMilpacPhrase + " " + mention(userID) +
			"'s Discord account, so nothing changed. Check you picked the right member."}
	}
	status := aviatorAddedPhrase
	if already {
		status = aviatorAlreadyPhrase
	}
	if read == milpacDischarged {
		if holds {
			if err := w.setRole(userID, false, reason); err != nil {
				return aviatorReply{kind: aviatorFailed, text: "❌ " + aviatorNotTakenPhrase + " the Winged Aviator role from " +
					mention(userID) + ", whose milpac shows a discharge, so nothing changed. " + wingedAviatorRoleRefusal(err)}
			}
		}
		if !already && w.record(userID) != nil {
			return aviatorNotSaved(userID)
		}
		return aviatorReply{text: "⚠️ " + mention(userID) + " " + status +
			", but their milpac shows a discharge. " + aviatorReEnlistPhrase}
	}
	if !holds {
		if err := w.setRole(userID, true, reason); err != nil {
			outcome := "so they aren't a winged aviator."
			if already {
				outcome = "They're still a winged aviator."
			}
			return aviatorReply{kind: aviatorFailed, text: "❌ " + aviatorNotGivenPhrase + " " + mention(userID) +
				" the Winged Aviator role, " + outcome + " " + wingedAviatorRoleRefusal(err)}
		}
	}
	if !already && w.record(userID) != nil {
		return aviatorNotSaved(userID)
	}
	return aviatorReply{text: "✅ " + mention(userID) + " " + status + ", with the Winged Aviator role."}
}

// remove ends the member's status, as /winged-aviator remove, and takes
// the role when they hold it, so a later re-enlistment gives nothing back.
// holds is whether they hold it as the command resolved them.
func (w *WingedAviators) remove(userID string, holds bool, reason string) aviatorReply {
	defer w.lock(userID)()
	was := w.isWingedAviator(userID)
	if !was && !holds {
		return aviatorReply{text: "⚠️ " + mention(userID) + " " + aviatorNeitherPhrase +
			" and doesn't hold the Winged Aviator role, so nothing changed."}
	}
	if holds {
		if err := w.setRole(userID, false, reason); err != nil {
			return aviatorReply{kind: aviatorFailed, text: "❌ " + aviatorNotTakenPhrase + " the Winged Aviator role from " +
				mention(userID) + ", so nothing changed. " + wingedAviatorRoleRefusal(err)}
		}
	}
	if was && w.unrecord(userID) != nil {
		return aviatorNotSaved(userID)
	}
	return aviatorReply{text: "✅ " + mention(userID) + " " + aviatorRemovedPhrase + "."}
}

// aviatorNotSaved answers a command whose store write failed. The write
// has gone to Sentry.
func aviatorNotSaved(userID string) aviatorReply {
	return aviatorReply{kind: aviatorFailed, text: "❌ " + aviatorNotSavedPhrase + " " + mention(userID) +
		"'s status as a winged aviator. Try again in a moment."}
}

// wingedAviatorRoleRefusal says why Discord refused a role change, in words
// staff can act on. It never carries Discord's own answer.
func wingedAviatorRoleRefusal(err error) string {
	class := classifyDiscordError(err)
	switch {
	case class.MissingPermissions:
		return "The bot needs Manage Roles, and its highest role above Winged Aviator."
	case class.NotFound:
		return "Discord says they aren't in this server."
	case class.ConfigFault:
		return "Discord doesn't know the role, so WINGED_AVIATOR_ROLE_ID may name a deleted one."
	case class.SystemFault:
		return "Discord had a problem. Try again in a moment."
	}
	return "Discord rejected the change."
}

// record makes the member a winged aviator in the store.
func (w *WingedAviators) record(userID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), wingedAviatorStoreTimeout)
	defer cancel()
	if err := w.store.AddWingedAviator(ctx, w.guildID, userID); err != nil {
		captureError("Winged aviator not recorded", err, "discord_id", userID)
		return err
	}
	utils.Info("Winged aviator recorded", "discord_id", userID)
	return nil
}

// unrecord ends the member's status in the store.
func (w *WingedAviators) unrecord(userID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), wingedAviatorStoreTimeout)
	defer cancel()
	if err := w.store.RemoveWingedAviator(ctx, w.guildID, userID); err != nil {
		captureError("Winged aviator status not ended", err, "discord_id", userID)
		return err
	}
	utils.Info("Winged aviator status ended", "discord_id", userID)
	return nil
}

// mention names a member in a reply.
func mention(userID string) string {
	return "<@" + userID + ">"
}
