package commands

import "github.com/bwmarrin/discordgo"

// commandUser is a member about to use a command: where, who, and the
// permissions Discord resolved for them in that channel.
type commandUser struct {
	guildID, channelID, userID string
	roleIDs                    []string
	permissions                int64
}

// mayUseCommand reports whether Discord would let u run a command that sets
// no default member permissions, from the command's own permission overrides
// and the app-level ones. Discord applies these to the command itself but
// not to a button a command posted, so the button checks them by hand.
// Rules: Discord's "Commands Permission System" flowchart,
// https://github.com/discord/discord-api-docs/blob/main/images/new-permissions-flowchart.svg
// and https://docs.discord.com/developers/interactions/application-commands#permissions
func mayUseCommand(cmd, app []*discordgo.ApplicationCommandPermissions, u commandUser) bool {
	if u.permissions&discordgo.PermissionAdministrator != 0 {
		return true
	}
	// Discord runs no command in a channel where the member lacks Use
	// Application Commands.
	if u.permissions&discordgo.PermissionUseApplicationCommands == 0 {
		return false
	}
	allChannels, _ := discordgo.GuildAllChannelsID(u.guildID)
	allowed, decided := channelOverride(cmd, u.channelID, allChannels)
	if !decided {
		allowed, decided = channelOverride(app, u.channelID, allChannels)
	}
	if decided && !allowed {
		return false
	}
	if allowed, decided := memberOverride(cmd, u); decided {
		return allowed
	}
	allowed, decided = memberOverride(app, u)
	return allowed || !decided
}

// channelOverride is the override for channelID among perms, or failing
// that the one for every channel.
func channelOverride(perms []*discordgo.ApplicationCommandPermissions, channelID, allChannels string) (allowed, decided bool) {
	if p := findOverride(perms, discordgo.ApplicationCommandPermissionTypeChannel, channelID); p != nil {
		return p.Permission, true
	}
	if p := findOverride(perms, discordgo.ApplicationCommandPermissionTypeChannel, allChannels); p != nil {
		return p.Permission, true
	}
	return false, false
}

// memberOverride decides by the override for the user, then by those for
// the roles they hold, any one allowing, then by the one for @everyone.
func memberOverride(perms []*discordgo.ApplicationCommandPermissions, u commandUser) (allowed, decided bool) {
	if p := findOverride(perms, discordgo.ApplicationCommandPermissionTypeUser, u.userID); p != nil {
		return p.Permission, true
	}
	for _, roleID := range u.roleIDs {
		if roleID == u.guildID {
			continue
		}
		if p := findOverride(perms, discordgo.ApplicationCommandPermissionTypeRole, roleID); p != nil {
			decided = true
			if p.Permission {
				return true, true
			}
		}
	}
	if decided {
		return false, true
	}
	if p := findOverride(perms, discordgo.ApplicationCommandPermissionTypeRole, u.guildID); p != nil {
		return p.Permission, true
	}
	return false, false
}

func findOverride(perms []*discordgo.ApplicationCommandPermissions, kind discordgo.ApplicationCommandPermissionType, id string) *discordgo.ApplicationCommandPermissions {
	if id == "" {
		return nil
	}
	for _, p := range perms {
		if p.Type == kind && p.ID == id {
			return p
		}
	}
	return nil
}
