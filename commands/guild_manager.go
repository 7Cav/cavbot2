package commands

import (
	"net/url"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// GuildManager is the subset of *discordgo.Session that /foxhole uses to read
// and mutate guild roles, members, and channel permission overwrites, plus send
// a plain channel message as the purge summary's token-independent fallback
// surface. Command code depends on this interface so tests can substitute a fake
// that records calls and injects per-call errors without touching the live
// Discord gateway.
//
// Following the InteractionResponder precedent (utils/discord_responder.go),
// the production wrapper is a thin pass-through. Each call that changes the
// guild takes an audit log reason, as the temp VC seam's do, so the reason
// travels with the call. Discord shows the bot as the actor of every such
// change, so the reason is where a moderator reads who made it.
type GuildManager interface {
	GuildRoles(guildID string) ([]*discordgo.Role, error)
	GuildRoleCreate(guildID string, data *discordgo.RoleParams, auditReason string) (*discordgo.Role, error)
	GuildRoleDelete(guildID, roleID, auditReason string) error
	GuildChannels(guildID string) ([]*discordgo.Channel, error)
	ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64, auditReason string) error
	GuildMember(guildID, userID string) (*discordgo.Member, error)
	GuildMembersSearch(guildID, query string, limit int) ([]*discordgo.Member, error)
	GuildMemberRoleAdd(guildID, userID, roleID, auditReason string) error
	GuildMemberRoleRemove(guildID, userID, roleID, auditReason string) error
	// ChannelMessageSend posts a plain message to a channel. Unlike the
	// interaction-response surfaces, it does not depend on the (15-minute)
	// interaction token, so it is the fallback surface a long purge uses to
	// deliver its summary once the deferred edit can no longer be delivered.
	ChannelMessageSend(channelID, content string) error
}

// sessionGuildManager adapts *discordgo.Session to GuildManager. Each method is
// a one-line pass-through; keeping it trivial means the (hard-to-unit-test)
// wrapper adds negligible uncovered surface to the package.
type sessionGuildManager struct {
	s *discordgo.Session
}

// auditLogReason sends reason the way Discord reads the header, as
// URL-encoded UTF-8. discordgo sets the header as given, so an accented
// letter would arrive as raw bytes and a % would start an escape. A space
// goes as %20, never '+', since Discord documents no reading of '+'.
func auditLogReason(reason string) discordgo.RequestOption {
	return discordgo.WithAuditLogReason(strings.ReplaceAll(url.QueryEscape(reason), "+", "%20"))
}

// NewSessionGuildManager wraps a real Discord session for production use.
func NewSessionGuildManager(s *discordgo.Session) GuildManager {
	return &sessionGuildManager{s: s}
}

func (g *sessionGuildManager) GuildRoles(guildID string) ([]*discordgo.Role, error) {
	return g.s.GuildRoles(guildID)
}

func (g *sessionGuildManager) GuildRoleCreate(guildID string, data *discordgo.RoleParams, auditReason string) (*discordgo.Role, error) {
	return g.s.GuildRoleCreate(guildID, data, auditLogReason(auditReason))
}

func (g *sessionGuildManager) GuildRoleDelete(guildID, roleID, auditReason string) error {
	return g.s.GuildRoleDelete(guildID, roleID, auditLogReason(auditReason))
}

func (g *sessionGuildManager) GuildChannels(guildID string) ([]*discordgo.Channel, error) {
	return g.s.GuildChannels(guildID)
}

func (g *sessionGuildManager) ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64, auditReason string) error {
	return g.s.ChannelPermissionSet(channelID, targetID, targetType, allow, deny, auditLogReason(auditReason))
}

func (g *sessionGuildManager) GuildMember(guildID, userID string) (*discordgo.Member, error) {
	return g.s.GuildMember(guildID, userID)
}

func (g *sessionGuildManager) GuildMembersSearch(guildID, query string, limit int) ([]*discordgo.Member, error) {
	return g.s.GuildMembersSearch(guildID, query, limit)
}

func (g *sessionGuildManager) GuildMemberRoleAdd(guildID, userID, roleID, auditReason string) error {
	return g.s.GuildMemberRoleAdd(guildID, userID, roleID, auditLogReason(auditReason))
}

func (g *sessionGuildManager) GuildMemberRoleRemove(guildID, userID, roleID, auditReason string) error {
	return g.s.GuildMemberRoleRemove(guildID, userID, roleID, auditLogReason(auditReason))
}

func (g *sessionGuildManager) ChannelMessageSend(channelID, content string) error {
	_, err := g.s.ChannelMessageSend(channelID, content)
	return err
}
