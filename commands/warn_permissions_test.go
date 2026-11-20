package commands

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestMayUseCommandFollowsDiscordsPermissionRules(t *testing.T) {
	const (
		guild       = "100"
		allChannels = "99"
		channel     = "400"
		user        = "u"
		held        = "r-held"
		alsoHeld    = "r-also"
		notHeld     = "r-not"
	)
	role := func(id string, allow bool) *discordgo.ApplicationCommandPermissions {
		return &discordgo.ApplicationCommandPermissions{ID: id, Type: discordgo.ApplicationCommandPermissionTypeRole, Permission: allow}
	}
	member := func(id string, allow bool) *discordgo.ApplicationCommandPermissions {
		return &discordgo.ApplicationCommandPermissions{ID: id, Type: discordgo.ApplicationCommandPermissionTypeUser, Permission: allow}
	}
	inChannel := func(id string, allow bool) *discordgo.ApplicationCommandPermissions {
		return &discordgo.ApplicationCommandPermissions{ID: id, Type: discordgo.ApplicationCommandPermissionTypeChannel, Permission: allow}
	}
	type perms = []*discordgo.ApplicationCommandPermissions

	cases := []struct {
		name        string
		cmd, app    perms
		noUse       bool
		permissions int64
		want        bool
	}{
		{name: "an admin past a denying override", cmd: perms{member(user, false)}, permissions: discordgo.PermissionAdministrator, want: true},
		{name: "a user allow over a role deny", cmd: perms{role(held, false), member(user, true)}, want: true},
		{name: "a user deny over a role allow", cmd: perms{role(held, true), member(user, false)}, want: false},
		{name: "any held role allowing", cmd: perms{role(held, false), role(alsoHeld, true)}, want: true},
		{name: "every held role denying, whatever @everyone gets", cmd: perms{role(held, false), role(alsoHeld, false), role(guild, true)}, want: false},
		{name: "a role not held is no override", cmd: perms{role(notHeld, true), role(guild, false)}, want: false},
		{name: "@everyone denied", cmd: perms{role(guild, false)}, want: false},
		{name: "no overrides", want: true},
		{name: "no Use Application Commands in the channel", cmd: perms{member(user, true)}, noUse: true, want: false},
		{name: "an admin without Use Application Commands", noUse: true, permissions: discordgo.PermissionAdministrator, want: true},
		{name: "@everyone allowed", cmd: perms{role(guild, true)}, want: true},
		{name: "an app allow", app: perms{member(user, true)}, want: true},
		{name: "a channel deny over a user allow", cmd: perms{inChannel(channel, false), member(user, true)}, want: false},
		{name: "every channel denied", cmd: perms{inChannel(allChannels, false)}, want: false},
		{name: "this channel allowed over every channel denied", cmd: perms{inChannel(allChannels, false), inChannel(channel, true)}, want: true},
		{name: "a command channel override over the app's", cmd: perms{inChannel(channel, true)}, app: perms{inChannel(channel, false)}, want: true},
		{name: "an app channel deny", app: perms{inChannel(allChannels, false)}, want: false},
		{name: "a command role allow over an app @everyone deny", cmd: perms{role(held, true)}, app: perms{role(guild, false)}, want: true},
		{name: "a command @everyone allow over an app user deny", cmd: perms{role(guild, true)}, app: perms{member(user, false)}, want: true},
		{name: "an app user deny", app: perms{member(user, false)}, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := commandUser{guildID: guild, channelID: channel, userID: user, roleIDs: []string{held, alsoHeld}, permissions: c.permissions}
			if !c.noUse {
				u.permissions |= discordgo.PermissionUseApplicationCommands
			}
			if got := mayUseCommand(c.cmd, c.app, u); got != c.want {
				t.Errorf("mayUseCommand = %v, want %v", got, c.want)
			}
		})
	}
}
