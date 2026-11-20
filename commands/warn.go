package commands

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// /warn and /infractions (#971) replace MEE6's: a warning is a row in the
// bot store and a public notice in the channel it was given in. The notice
// names neither the moderator nor anyone else who acts on it.

const (
	warnCommandName        = "warn"
	infractionsCommandName = "infractions"
	warnRemoveAction       = "remove"
	// warnReasonLimit keeps an /infractions line short enough that a reply
	// of 2000 characters always holds several.
	warnReasonLimit  = 200
	warnStoreTimeout = 5 * time.Second

	warnNoticeColor  = 0xF0B232
	warnRemovedColor = 0x4E5058
)

// The phrases that tell /warn's, its button's and /infractions' replies
// apart.
const (
	warningsOffPhrase        = "Warnings are not enabled on this bot."
	warnBotPhrase            = "Bots can't be warned."
	warnAdminPhrase          = "is a server admin"
	warnImmunePhrase         = "holds a role that is immune to warnings"
	warnCantPostPhrase       = "can't post in this channel"
	warnNotPostedPhrase      = "Couldn't post the warning"
	warnOrphanPhrase         = "The warning may still show in /" + infractionsCommandName + "."
	warnNotSavedPhrase       = "Couldn't save the warning"
	warnUnreadablePhrase     = "Couldn't read"
	warnedPhrase             = "Warned"
	warnNotPermittedPhrase   = "Only members who can use /" + warnCommandName + " can remove a warning."
	warnStartingPhrase       = "Cavbot2 is still starting."
	warnAlreadyRemovedPhrase = "That warning was already removed."
	warnRemovedPhrase        = "Removed the warning for"
	warnNoticeStalePhrase    = "The notice didn't update."
	warnNotRemovedPhrase     = "Couldn't remove the warning"
	warnStaleButtonPhrase    = "This button no longer does anything."
	noWarningsPhrase         = "has no warnings."
	olderWarningsPhrase      = "older warning"
	warnRemovedFooter        = "Warning removed"
)

// warnNoticeStaleSentence ends a press's reply when the notice's edit
// failed.
const warnNoticeStaleSentence = " " + warnNoticeStalePhrase + " Press its button again to update it."

// warnStore is the subset of the bot store warnings live in.
type warnStore interface {
	GetWarningImmuneRoles(ctx context.Context, guildID string) (store.WarningImmuneRoles, error)
	AddWarning(ctx context.Context, w store.Warning) (store.Warning, error)
	SetWarningNotice(ctx context.Context, guildID string, id int64, messageID string) error
	ListWarnings(ctx context.Context, guildID, memberID string) ([]store.Warning, error)
	RemoveWarning(ctx context.Context, guildID string, id int64) (store.Warning, error)
}

// warnDiscord is what /warn and its button ask of Discord beyond the
// interaction itself. sessionWarnDiscord serves it from the session.
type warnDiscord interface {
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageEditComplex(m *discordgo.MessageEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	GuildApplicationCommandsPermissions(appID, guildID string, options ...discordgo.RequestOption) ([]*discordgo.GuildApplicationCommandPermissions, error)
	Channel(channelID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
	// cachedChannel is the type and parent of a channel or thread in the
	// session's cache, ok false when the cache lacks it.
	cachedChannel(guildID, channelID string) (kind discordgo.ChannelType, parentID string, ok bool)
}

type sessionWarnDiscord struct{ *discordgo.Session }

// cachedChannel copies the fields under State.RLock, as GuildData in
// temp_vc.go does: discordgo's State.ChannelAdd overwrites a cached channel
// in place under State.Lock.
func (s sessionWarnDiscord) cachedChannel(guildID, channelID string) (discordgo.ChannelType, string, bool) {
	st := s.State
	if st == nil {
		return 0, "", false
	}
	st.RLock()
	defer st.RUnlock()
	for _, g := range st.Guilds {
		if g.ID != guildID {
			continue
		}
		for _, c := range slices.Concat(g.Channels, g.Threads) {
			if c.ID == channelID {
				return c.Type, c.ParentID, true
			}
		}
	}
	return 0, "", false
}

// commandChannel is the channel whose command permissions apply in
// channelID: a thread's parent, or the channel itself.
func commandChannel(d warnDiscord, guildID, channelID string) (string, error) {
	kind, parentID, ok := d.cachedChannel(guildID, channelID)
	if !ok {
		c, err := d.Channel(channelID)
		if err != nil {
			return "", err
		}
		kind, parentID = c.Type, c.ParentID
	}
	if (&discordgo.Channel{Type: kind}).IsThread() {
		return parentID, nil
	}
	return channelID, nil
}

// warnings is the state /warn's button reads: the store, and /warn's
// command ID, which keys its permission overrides.
type warnings struct {
	store warnStore
	// commandID is /warn's ID, empty until the startup sync hands it over.
	commandID atomic.Value
}

// Warn declares /warn over the bot store, nil on a host without one, where
// it refuses. It sets no default member permissions: Server Settings decide
// who may run it, and its button follows the same settings.
func Warn(st store.Store) Command {
	w := &warnings{store: st}
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        warnCommandName,
			Description: "Warn a member",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "member",
					Description: "The member to warn",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Why, shown publicly in the channel",
					Required:    true,
					MaxLength:   warnReasonLimit,
				},
			},
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			runWarn(utils.NewSessionResponder(s), sessionWarnDiscord{s}, w, i)
		},
		OnRegistered: func(id string) { w.commandID.Store(id) },
	}
}

// runWarn is the /warn handler behind the responder seam. A press on a
// notice's button reaches it too (ADR 0007).
func runWarn(r utils.InteractionResponder, d warnDiscord, w *warnings, interaction *discordgo.InteractionCreate) {
	if interaction.Type == discordgo.InteractionMessageComponent {
		runWarnRemove(r, d, w, interaction)
		return
	}
	if err := deferEphemeral(r, interaction); err != nil {
		replyAckFailed(r, interaction, err)
		return
	}
	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting Warn", "command", warnCommandName, "username", username, "discord_id", discordID)

	if w.store == nil {
		utils.Warn("Warn refused, no bot store configured", "command", warnCommandName, "discord_id", discordID)
		refuse(r, interaction, "❌ "+warningsOffPhrase)
		return
	}
	opts := optionsByName(interaction)
	target := opts["member"].UserValue(nil).ID
	reason := opts["reason"].StringValue()
	guildID := interaction.GuildID

	user, member := resolvedTarget(interaction, target)
	// Discord resolves the owner with every permission, Administrator too.
	switch {
	case user.Bot:
		refuse(r, interaction, "❌ "+warnBotPhrase)
		return
	case member != nil && member.Permissions&discordgo.PermissionAdministrator != 0:
		refuse(r, interaction, "❌ "+mention(target)+" "+warnAdminPhrase+", so they can't be warned.")
		return
	}

	if member != nil {
		ctx, cancel := context.WithTimeout(context.Background(), warnStoreTimeout)
		immune, err := w.store.GetWarningImmuneRoles(ctx, guildID)
		cancel()
		if err != nil {
			captureError("Warning immune roles unreadable, /warn refused", err, "guild_id", guildID)
			replyError(r, interaction, "❌ "+warnUnreadablePhrase+" the warning immune roles, so nothing was saved. Try again in a moment.")
			return
		}
		if slices.ContainsFunc(member.Roles, func(role string) bool { return slices.Contains(immune.RoleIDs, role) }) {
			refuse(r, interaction, "❌ "+mention(target)+" "+warnImmunePhrase+", so nothing was saved.")
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), warnStoreTimeout)
	warning, err := w.store.AddWarning(ctx, store.Warning{
		GuildID:     guildID,
		MemberID:    target,
		ModeratorID: discordID,
		ChannelID:   interaction.ChannelID,
		Reason:      reason,
	})
	cancel()
	if err != nil {
		captureError("Warning not saved", err, "guild_id", guildID, "discord_id", target)
		replyError(r, interaction, "❌ "+warnNotSavedPhrase+". Try again in a moment.")
		return
	}

	msg, err := postWarnNotice(d, warning, user)
	// The store's deadline starts after the post, which takes as long as
	// Discord makes it.
	ctx, cancel = context.WithTimeout(context.Background(), warnStoreTimeout)
	defer cancel()
	if err != nil {
		_, rmErr := w.store.RemoveWarning(ctx, guildID, warning.ID)
		noAccess := classifyDiscordError(err).MissingPermissions
		if noAccess {
			utils.Warn("Warn refused, no access to post in the channel", "channel_id", interaction.ChannelID, "error", err)
		} else {
			captureError("Warning notice not posted", err, "guild_id", guildID, "channel_id", interaction.ChannelID)
		}
		switch {
		case rmErr != nil:
			captureError("Warning kept without a notice", rmErr, "guild_id", guildID, "warning_id", warning.ID)
			replyError(r, interaction, "❌ "+warnNotPostedPhrase+" notice. "+warnOrphanPhrase)
		case noAccess:
			refuse(r, interaction, "❌ Cavbot2 "+warnCantPostPhrase+", so nothing was saved.")
		default:
			replyError(r, interaction, "❌ "+warnNotPostedPhrase+" notice, so nothing was saved. Try again in a moment.")
		}
		return
	}
	// The notice stands either way. Without its message ID /infractions
	// just lists the warning with no link.
	if err := w.store.SetWarningNotice(ctx, guildID, warning.ID, msg.ID); err != nil {
		captureError("Warning notice not recorded", err, "guild_id", guildID, "warning_id", warning.ID)
	}
	editEphemeral(r, interaction, "✅ "+warnedPhrase+" "+mention(target)+".")
	utils.Info("✨ Done!", "command", warnCommandName)
}

// resolvedTarget is the picked member as Discord resolved them: always a
// user, and a member when they are in the server.
func resolvedTarget(interaction *discordgo.InteractionCreate, id string) (*discordgo.User, *discordgo.Member) {
	resolved := interaction.ApplicationCommandData().Resolved
	if resolved == nil {
		return &discordgo.User{ID: id}, nil
	}
	user := resolved.Users[id]
	if user == nil {
		user = &discordgo.User{ID: id}
	}
	return user, resolved.Members[id]
}

// postWarnNotice posts the warning's public notice as a channel message
// of the bot's own, not a reply, so it doesn't show who ran /warn.
func postWarnNotice(d warnDiscord, w store.Warning, target *discordgo.User) (*discordgo.Message, error) {
	customID, err := warnRemoveCustomID(w.ID)
	if err != nil {
		return nil, err
	}
	name := target.Username
	if name == "" {
		name = target.ID
	}
	return d.ChannelMessageSendComplex(w.ChannelID, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{{
			Color:       warnNoticeColor,
			Author:      &discordgo.MessageEmbedAuthor{Name: name + " has been warned", IconURL: target.AvatarURL("")},
			Description: "**Reason:** " + w.Reason,
		}},
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Remove this infraction", Style: discordgo.DangerButton, CustomID: customID},
		}}},
		AllowedMentions: noMentions(),
	})
}

func warnRemoveCustomID(id int64) (string, error) {
	customID := strings.Join([]string{warnCommandName, warnRemoveAction, strconv.FormatInt(id, 10)}, customIDSeparator)
	if len(customID) > discordCustomIDLimit {
		return "", fmt.Errorf("warning CustomID %q is over Discord's %d characters", customID, discordCustomIDLimit)
	}
	return customID, nil
}

func parseWarnRemoveCustomID(customID string) (int64, bool) {
	parts := strings.Split(customID, customIDSeparator)
	if len(parts) != 3 || parts[0] != warnCommandName || parts[1] != warnRemoveAction {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	return id, err == nil
}

// runWarnRemove answers a press on a notice's Remove button. Every answer
// is a new ephemeral message, never an update of the public notice.
func runWarnRemove(r utils.InteractionResponder, d warnDiscord, w *warnings, interaction *discordgo.InteractionCreate) {
	customID := interaction.MessageComponentData().CustomID
	if err := deferEphemeral(r, interaction); err != nil {
		captureAckFailure("Failed to acknowledge a warning notice button", err,
			"command", warnCommandName, "custom_id", customID, "guild_id", interaction.GuildID)
		return
	}
	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting WarnRemove", "command", warnCommandName,
		"username", username, "discord_id", discordID, "custom_id", customID)

	if w.store == nil {
		utils.Warn("Warning removal refused, no bot store configured", "custom_id", customID, "discord_id", discordID)
		editNoticeReply(r, interaction, "❌ "+warningsOffPhrase)
		return
	}
	id, ok := parseWarnRemoveCustomID(customID)
	if !ok {
		utils.Warn("Warning notice button not recognised", "custom_id", customID, "discord_id", discordID)
		editNoticeReply(r, interaction, "❌ "+warnStaleButtonPhrase)
		return
	}
	if reply, permitted := mayRemoveWarning(d, w, interaction); !permitted {
		editNoticeReply(r, interaction, reply)
		return
	}

	guildID := interaction.GuildID
	ctx, cancel := context.WithTimeout(context.Background(), warnStoreTimeout)
	defer cancel()
	removed, err := w.store.RemoveWarning(ctx, guildID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// The edit repairs a notice whose edit at the removal failed.
		reply := "❌ " + warnAlreadyRemovedPhrase
		if !editWarnNotice(d, interaction) {
			reply += warnNoticeStaleSentence
		}
		editNoticeReply(r, interaction, reply)
		return
	case err != nil:
		captureError("Warning not removed", err, "guild_id", guildID, "warning_id", id)
		editNoticeReply(r, interaction, "❌ "+warnNotRemovedPhrase+". Try again in a moment.")
		return
	}

	utils.Info("Warning removed", "warning_id", id, "member_id", removed.MemberID, "discord_id", discordID)
	reply := "✅ " + warnRemovedPhrase + " " + mention(removed.MemberID) + "."
	if !editWarnNotice(d, interaction) {
		reply += warnNoticeStaleSentence
	}
	editNoticeReply(r, interaction, reply)
	utils.Info("✨ Done!", "command", warnCommandName)
}

// mayRemoveWarning checks the presser against /warn's permissions, which
// Discord doesn't apply to the button, and returns the reply for a press
// it refuses.
func mayRemoveWarning(d warnDiscord, w *warnings, interaction *discordgo.InteractionCreate) (reply string, permitted bool) {
	commandID, _ := w.commandID.Load().(string)
	if commandID == "" {
		utils.Warn("Warning removal refused, /warn not registered yet", "guild_id", interaction.GuildID)
		return "❌ " + warnStartingPhrase + " Try again in a moment.", false
	}
	all, err := d.GuildApplicationCommandsPermissions(interaction.AppID, interaction.GuildID)
	if err != nil {
		captureError("Command permissions unreadable, warning removal refused", err, "guild_id", interaction.GuildID)
		return "❌ " + warnUnreadablePhrase + " who may use /" + warnCommandName + ", so nothing changed. Try again in a moment.", false
	}
	var cmd, app []*discordgo.ApplicationCommandPermissions
	for _, p := range all {
		switch p.ID {
		case commandID:
			cmd = p.Permissions
		case interaction.AppID:
			app = p.Permissions
		}
	}
	channelID, err := commandChannel(d, interaction.GuildID, interaction.ChannelID)
	if err != nil {
		captureError("Channel unreadable, warning removal refused", err, "guild_id", interaction.GuildID, "channel_id", interaction.ChannelID)
		return "❌ " + warnUnreadablePhrase + " this channel, so nothing changed. Try again in a moment.", false
	}
	u := commandUser{guildID: interaction.GuildID, channelID: channelID}
	if interaction.Member != nil {
		u.roleIDs, u.permissions = interaction.Member.Roles, interaction.Member.Permissions
		if interaction.Member.User != nil {
			u.userID = interaction.Member.User.ID
		}
	}
	if !mayUseCommand(cmd, app, u) {
		return "❌ " + warnNotPermittedPhrase, false
	}
	return "", true
}

// editWarnNotice edits the notice the pressed button sits on to the removed
// state, built from the notice itself, and reports whether Discord took the
// edit. A notice with no embed only loses its button.
func editWarnNotice(d warnDiscord, interaction *discordgo.InteractionCreate) bool {
	m := interaction.Message
	if m == nil {
		return false
	}
	edit := &discordgo.MessageEdit{
		ID:              m.ID,
		Channel:         interaction.ChannelID,
		Components:      &[]discordgo.MessageComponent{},
		AllowedMentions: noMentions(),
	}
	if len(m.Embeds) > 0 {
		notice := m.Embeds[0]
		removed := &discordgo.MessageEmbed{
			Color:       warnRemovedColor,
			Description: notice.Description,
			Footer:      &discordgo.MessageEmbedFooter{Text: warnRemovedFooter},
		}
		if a := notice.Author; a != nil {
			removed.Author = &discordgo.MessageEmbedAuthor{Name: a.Name, URL: a.URL, IconURL: a.IconURL}
		}
		if !strings.HasPrefix(removed.Description, "~~") || !strings.HasSuffix(removed.Description, "~~") {
			removed.Description = "~~" + removed.Description + "~~"
		}
		edit.Embeds = &[]*discordgo.MessageEmbed{removed}
	}
	if _, err := d.ChannelMessageEditComplex(edit); err != nil {
		captureError("Warning notice not edited", err, "guild_id", interaction.GuildID, "message_id", m.ID)
		return false
	}
	return true
}

// Infractions declares /infractions over the bot store, nil on a host
// without one, where it refuses.
func Infractions(st store.Store) Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        infractionsCommandName,
			Description: "List a member's warnings",
			Options: []*discordgo.ApplicationCommandOption{{
				Type:        discordgo.ApplicationCommandOptionUser,
				Name:        "member",
				Description: "The member",
				Required:    true,
			}},
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			runInfractions(utils.NewSessionResponder(s), st, i)
		},
	}
}

// runInfractions is the /infractions handler behind the responder seam.
func runInfractions(r utils.InteractionResponder, st warnStore, interaction *discordgo.InteractionCreate) {
	if err := deferEphemeral(r, interaction); err != nil {
		replyAckFailed(r, interaction, err)
		return
	}
	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting Infractions", "command", infractionsCommandName, "username", username, "discord_id", discordID)

	if st == nil {
		utils.Warn("Infractions refused, no bot store configured", "command", infractionsCommandName, "discord_id", discordID)
		refuse(r, interaction, "❌ "+warningsOffPhrase)
		return
	}
	target := optionsByName(interaction)["member"].UserValue(nil).ID
	ctx, cancel := context.WithTimeout(context.Background(), warnStoreTimeout)
	defer cancel()
	list, err := st.ListWarnings(ctx, interaction.GuildID, target)
	if err != nil {
		captureError("Warnings unreadable, /infractions can't list them", err, "guild_id", interaction.GuildID, "discord_id", target)
		replyError(r, interaction, "❌ "+warnUnreadablePhrase+" "+mention(target)+"'s warnings. Try again in a moment.")
		return
	}
	content := infractionsReply(interaction.GuildID, target, list)
	if err := r.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{
		Content:         &content,
		AllowedMentions: noMentions(),
	}); err != nil {
		captureEditFailure(interaction, err)
	}
	utils.Info("✨ Done!", "command", infractionsCommandName)
}

// infractionsReply lists the member's warnings, newest first as list holds
// them, as many as fit in one message, then counts the rest. It measures
// bytes, which never undercount Discord's characters.
func infractionsReply(guildID, memberID string, list []store.Warning) string {
	if len(list) == 0 {
		return "✅ " + mention(memberID) + " " + noWarningsPhrase
	}
	reply := fmt.Sprintf("⚠️ %s for %s, newest first:", countWarnings(len(list), "warning"), mention(memberID))
	for i, w := range list {
		line := fmt.Sprintf("`%d` <t:%d:d> by %s: %s", w.ID, w.CreatedAt.Unix(), mention(w.ModeratorID), w.Reason)
		if w.MessageID != "" {
			line += " · https://discord.com/channels/" + guildID + "/" + w.ChannelID + "/" + w.MessageID
		}
		next := reply + "\n" + line
		fits := len(next) <= discordMessageLimit
		if rest := len(list) - i - 1; rest > 0 {
			fits = len(next+"\n"+olderWarnings(rest)) <= discordMessageLimit
		}
		if !fits {
			return reply + "\n" + olderWarnings(len(list)-i)
		}
		reply = next
	}
	return reply
}

func olderWarnings(n int) string {
	return "…and " + countWarnings(n, olderWarningsPhrase) + "."
}

// countWarnings is n of noun, made plural unless n is 1.
func countWarnings(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
