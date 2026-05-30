package commands

import "github.com/bwmarrin/discordgo"

// GuildManager is the subset of *discordgo.Session that /warden uses to read
// and mutate guild roles, members, and channel permission overwrites. Command
// code depends on this interface so tests can substitute a fake that records
// calls and injects per-call errors without touching the live Discord gateway.
//
// Following the InteractionResponder precedent (utils/discord_responder.go),
// the production wrapper is a thin pass-through; the variadic
// discordgo.RequestOption arguments are dropped because no /warden call site
// uses them.
type GuildManager interface {
	GuildRoles(guildID string) ([]*discordgo.Role, error)
	GuildRoleCreate(guildID string, data *discordgo.RoleParams) (*discordgo.Role, error)
	GuildRoleEdit(guildID, roleID string, data *discordgo.RoleParams) (*discordgo.Role, error)
	GuildRoleDelete(guildID, roleID string) error
	GuildChannels(guildID string) ([]*discordgo.Channel, error)
	ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64) error
	GuildMember(guildID, userID string) (*discordgo.Member, error)
	GuildMembersSearch(guildID, query string, limit int) ([]*discordgo.Member, error)
	GuildMemberRoleAdd(guildID, userID, roleID string) error
	GuildMemberRoleRemove(guildID, userID, roleID string) error
}

// sessionGuildManager adapts *discordgo.Session to GuildManager. Each method is
// a one-line pass-through; keeping it trivial means the (hard-to-unit-test)
// wrapper adds negligible uncovered surface to the package.
type sessionGuildManager struct {
	s *discordgo.Session
}

// NewSessionGuildManager wraps a real Discord session for production use.
func NewSessionGuildManager(s *discordgo.Session) GuildManager {
	return &sessionGuildManager{s: s}
}

func (g *sessionGuildManager) GuildRoles(guildID string) ([]*discordgo.Role, error) {
	return g.s.GuildRoles(guildID)
}

func (g *sessionGuildManager) GuildRoleCreate(guildID string, data *discordgo.RoleParams) (*discordgo.Role, error) {
	return g.s.GuildRoleCreate(guildID, data)
}

func (g *sessionGuildManager) GuildRoleEdit(guildID, roleID string, data *discordgo.RoleParams) (*discordgo.Role, error) {
	return g.s.GuildRoleEdit(guildID, roleID, data)
}

func (g *sessionGuildManager) GuildRoleDelete(guildID, roleID string) error {
	return g.s.GuildRoleDelete(guildID, roleID)
}

func (g *sessionGuildManager) GuildChannels(guildID string) ([]*discordgo.Channel, error) {
	return g.s.GuildChannels(guildID)
}

func (g *sessionGuildManager) ChannelPermissionSet(channelID, targetID string, targetType discordgo.PermissionOverwriteType, allow, deny int64) error {
	return g.s.ChannelPermissionSet(channelID, targetID, targetType, allow, deny)
}

func (g *sessionGuildManager) GuildMember(guildID, userID string) (*discordgo.Member, error) {
	return g.s.GuildMember(guildID, userID)
}

func (g *sessionGuildManager) GuildMembersSearch(guildID, query string, limit int) ([]*discordgo.Member, error) {
	return g.s.GuildMembersSearch(guildID, query, limit)
}

func (g *sessionGuildManager) GuildMemberRoleAdd(guildID, userID, roleID string) error {
	return g.s.GuildMemberRoleAdd(guildID, userID, roleID)
}

func (g *sessionGuildManager) GuildMemberRoleRemove(guildID, userID, roleID string) error {
	return g.s.GuildMemberRoleRemove(guildID, userID, roleID)
}
