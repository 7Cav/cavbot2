package commands

import "github.com/bwmarrin/discordgo"

// interactionUser safely extracts the invoking *discordgo.User from an
// interaction, with no nil dereference for any context.
//
// Discord populates interaction.Member (with Member.User) for GUILD interactions
// and interaction.User for DM interactions; for a guild interaction
// interaction.User is typically nil and vice versa. So the safe extraction
// prefers Member.User when present, falls back to interaction.User, and returns
// nil when neither is available (a malformed or forwarded interaction).
//
// Command entry points read username/discord_id off the interaction for their
// "🚀 Starting ..." log line; doing so directly (interaction.Member.User.…)
// panics for a member-less interaction. Route those reads through this helper.
func interactionUser(i *discordgo.InteractionCreate) *discordgo.User {
	if i == nil || i.Interaction == nil {
		return nil
	}
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User
	}
	return i.User
}

// interactionUsernameAndID returns the invoking user's username and ID, or empty
// strings when no user can be resolved. It is the convenience pair for entry-log
// fields ("username"/"discord_id"), built on interactionUser so the nil-safety
// lives in one place.
func interactionUsernameAndID(i *discordgo.InteractionCreate) (username, id string) {
	user := interactionUser(i)
	if user == nil {
		return "", ""
	}
	return user.Username, user.ID
}
