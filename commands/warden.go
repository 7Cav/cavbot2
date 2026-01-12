package commands

import (
	"fmt"
	"strings"

	"slices"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

const wardenRoleBaseName = "Verified Warden"

var (
	wardenRoleScopes  = []string{"internal", "external", "both"}
	wardenSubcommands = []string{
		"add",
		"remove",
		"bulkadd",
		// "purge",
	}

	wardenTitleCaser = cases.Title(language.Und, cases.NoLower)
)

func Warden() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "warden",
			Description: "Warden role management",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "command",
					Description: "Choose between " + strings.Join(wardenSubcommands, ", "),
					Required:    true,
					Choices:     stringChoices(wardenSubcommands),
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "flag",
					Description: "Internal/external scope, or both",
					Required:    true,
					Choices:     stringChoices(wardenRoleScopes),
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "discordname",
					Description: "Mention/ID/partial name; for bulkadd use comma-separated list; optional for purge",
					Required:    false,
				},
			},
		},
		Handler: handleWarden,
	}
}

func handleWarden(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	commandData := interaction.ApplicationCommandData()

	subcommand, ok := getOptionString(commandData, "command")
	if !ok || !slices.Contains(wardenSubcommands, subcommand) {
		utils.HandleError(session, interaction, "❌ Invalid warden command; must be "+strings.Join(wardenSubcommands, ", "))
		return
	}

	roleScope, ok := getOptionString(commandData, "flag")
	if !ok || !slices.Contains(wardenRoleScopes, roleScope) {
		utils.HandleError(session, interaction, "❌ Missing or invalid flag argument; must be 'internal', 'external', or 'both'")
		return
	}

	guildID := interaction.GuildID
	if guildID == "" {
		utils.HandleError(session, interaction, "❌ This command can only be used in a server (guild).")
		return
	}

	query, _ := getOptionString(commandData, "discordname")
	query = strings.TrimSpace(query)

	if subcommand != "purge" && query == "" {
		utils.HandleError(session, interaction, "❌ Missing discordname argument for this command")
		return
	}

	utils.Info("Warden command invoked", "command", subcommand, "query", query, "flag", roleScope)

	switch subcommand {
	case "add":
		handleWardenAdd(session, interaction, guildID, query, roleScope)
	case "remove":
		handleWardenRemove(session, interaction, guildID, query, roleScope)
	case "bulkadd":
		handleWardenBulkAdd(session, interaction, guildID, query, roleScope)
	// case "purge":
	// 	handleWardenPurge(session, interaction, guildID, roleScope)
	default:
		utils.HandleError(session, interaction, "❌ Unknown subcommand")
	}
}

func handleWardenAdd(session *discordgo.Session, interaction *discordgo.InteractionCreate, guildID, query, roleScope string) {
	if err := deferEphemeral(session, interaction); err != nil {
		utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to acknowledge: %v", err))
		return
	}

	member, err := findGuildMember(session, guildID, query)
	if err != nil {
		editEphemeral(session, interaction, err.Error())
		return
	}

	roleIDs, roleNames, err := resolveWardenRoleIDs(session, guildID, roleScope)
	if err != nil {
		editEphemeral(session, interaction, err.Error())
		return
	}

	for index, roleID := range roleIDs {
		roleName := roleNames[index]
		if err := session.GuildMemberRoleAdd(guildID, member.User.ID, roleID); err != nil {
			utils.Error("Failed to add warden role", "user", member.User.ID, "role", roleName, "error", err)
			editEphemeral(session, interaction, fmt.Sprintf("❌ Failed to add '%s' role to %s: %v", roleName, formatUser(member), err))
			return
		}
	}

	utils.Info("Warden role(s) added", "user", member.User.ID, "roles", strings.Join(roleNames, ", "))
	editEphemeral(session, interaction, fmt.Sprintf("✅ Added warden role(s) (%s) to %s", strings.Join(roleNames, ", "), formatUser(member)))
}

func handleWardenRemove(session *discordgo.Session, interaction *discordgo.InteractionCreate, guildID, query, roleScope string) {
	if err := deferEphemeral(session, interaction); err != nil {
		utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to acknowledge: %v", err))
		return
	}

	member, err := findGuildMember(session, guildID, query)
	if err != nil {
		editEphemeral(session, interaction, err.Error())
		return
	}

	roleIDs, roleNames, err := resolveWardenRoleIDs(session, guildID, roleScope)
	if err != nil {
		editEphemeral(session, interaction, err.Error())
		return
	}

	for index, roleID := range roleIDs {
		roleName := roleNames[index]
		if err := session.GuildMemberRoleRemove(guildID, member.User.ID, roleID); err != nil {
			utils.Error("Failed to remove warden role", "user", member.User.ID, "role", roleName, "error", err)
			editEphemeral(session, interaction, fmt.Sprintf("❌ Failed to remove '%s' role from %s: %v", roleName, formatUser(member), err))
			return
		}
	}

	utils.Info("Warden role(s) removed", "user", member.User.ID, "roles", strings.Join(roleNames, ", "))
	editEphemeral(session, interaction, fmt.Sprintf("✅ Removed warden role(s) (%s) from %s", strings.Join(roleNames, ", "), formatUser(member)))
}

func handleWardenBulkAdd(session *discordgo.Session, interaction *discordgo.InteractionCreate, guildID, query, roleScope string) {
	if err := deferEphemeral(session, interaction); err != nil {
		utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to acknowledge bulk add: %v", err))
		return
	}

	roleIDs, roleNames, err := resolveWardenRoleIDs(session, guildID, roleScope)
	if err != nil {
		editEphemeral(session, interaction, err.Error())
		return
	}

	requestedQueries := splitCommaSeparated(query)
	if len(requestedQueries) == 0 {
		editEphemeral(session, interaction, "⚠️ Nothing to do.")
		return
	}

	var results []string
	for _, singleQuery := range requestedQueries {
		member, memberErr := findGuildMember(session, guildID, singleQuery)
		if memberErr != nil {
			results = append(results, memberErr.Error())
			continue
		}

		for index, roleID := range roleIDs {
			roleName := roleNames[index]
			if err := session.GuildMemberRoleAdd(guildID, member.User.ID, roleID); err != nil {
				utils.Error("Failed to add warden role in bulk", "user", member.User.ID, "role", roleName, "error", err)
				results = append(results, fmt.Sprintf("❌ Failed to add '%s' role to %s: %v", roleName, formatUser(member), err))
				continue
			}
			results = append(results, fmt.Sprintf("✅ Added '%s' role to %s", roleName, formatUser(member)))
		}
	}

	editEphemeral(session, interaction, joinOrFallback(results, "⚠️ Nothing to do."))
}

func handleWardenPurge(session *discordgo.Session, interaction *discordgo.InteractionCreate, guildID, roleScope string) {
	if err := deferEphemeral(session, interaction); err != nil {
		utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to acknowledge purge: %v", err))
		return
	}

	roleIDs, roleNames, err := resolveWardenRoleIDs(session, guildID, roleScope)
	if err != nil {
		editEphemeral(session, interaction, err.Error())
		return
	}

	var (
		afterUserID        string
		removedAssignments int
		results            []string
	)

	for {
		members, err := session.GuildMembers(guildID, afterUserID, 1000)
		if err != nil {
			editEphemeral(session, interaction, fmt.Sprintf("❌ Failed to retrieve guild members: %v", err))
			return
		}
		if len(members) == 0 {
			break
		}

		for _, member := range members {
			if member == nil || member.User == nil {
				continue
			}

			for index, roleID := range roleIDs {
				roleName := roleNames[index]
				if !memberHasRole(member, roleID) {
					continue
				}

				if err := session.GuildMemberRoleRemove(guildID, member.User.ID, roleID); err != nil {
					utils.Error("Failed to remove warden role during purge", "user", member.User.ID, "role", roleName, "error", err)
					results = append(results, fmt.Sprintf("❌ Failed to remove '%s' role from %s: %v", roleName, formatUser(member), err))
					continue
				}

				removedAssignments++
				results = append(results, fmt.Sprintf("✅ Removed '%s' role from %s", roleName, formatUser(member)))
			}
		}

		afterUserID = members[len(members)-1].User.ID
		if len(members) < 1000 {
			break
		}
	}

	if removedAssignments == 0 {
		editEphemeral(session, interaction, "✅ Purge complete: no members had the role(s).")
		return
	}

	summary := fmt.Sprintf("✅ Purge complete: removed %d role assignment(s).\n\n%s", removedAssignments, joinOrFallback(results, ""))
	editEphemeral(session, interaction, summary)
}

func resolveWardenRoleNames(roleScope string) []string {
	switch roleScope {
	case "both":
		return []string{
			wardenRoleBaseName + " Internal",
			wardenRoleBaseName + " External",
		}
	case "internal", "external":
		return []string{
			wardenRoleBaseName + " " + wardenTitleCaser.String(roleScope),
		}
	default:
		return []string{
			wardenRoleBaseName + " " + wardenTitleCaser.String(roleScope),
		}
	}
}

func resolveWardenRoleIDs(session *discordgo.Session, guildID, roleScope string) (roleIDs []string, roleNames []string, err error) {
	roleNames = resolveWardenRoleNames(roleScope)
	roleIDs = make([]string, 0, len(roleNames))

	for _, roleName := range roleNames {
		roleID, findErr := findGuildRoleIDByName(session, guildID, roleName)
		if findErr != nil {
			return nil, nil, fmt.Errorf("❌ Failed to retrieve guild roles: %v", findErr)
		}
		if roleID == "" {
			return nil, nil, fmt.Errorf("❌ '%s' role not found in guild", roleName)
		}
		roleIDs = append(roleIDs, roleID)
	}

	return roleIDs, roleNames, nil
}

func getOptionString(commandData discordgo.ApplicationCommandInteractionData, optionName string) (string, bool) {
	for _, option := range commandData.Options {
		if option != nil && option.Name == optionName {
			return option.StringValue(), true
		}
	}
	return "", false
}

func stringChoices(values []string) []*discordgo.ApplicationCommandOptionChoice {
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(values))
	for _, value := range values {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  value,
			Value: value,
		})
	}
	return choices
}

func deferEphemeral(session *discordgo.Session, interaction *discordgo.InteractionCreate) error {
	return session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
}

func editEphemeral(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string) {
	_, err := session.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{
		Content: &content,
	})
	if err != nil {
		utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to edit response: %v", err))
	}
}

func memberHasRole(member *discordgo.Member, roleID string) bool {
	return slices.Contains(member.Roles, roleID)
}

func formatUser(member *discordgo.Member) string {
	if member == nil || member.User == nil {
		return "<unknown>"
	}

	if member.User.Discriminator != "" && member.User.Discriminator != "0" {
		return fmt.Sprintf("%s#%s", member.User.Username, member.User.Discriminator)
	}

	return member.User.Username
}

func findGuildMember(session *discordgo.Session, guildID, query string) (*discordgo.Member, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, fmt.Errorf("❌ Empty query")
	}

	// Mentions: <@123>, <@!123>
	if strings.HasPrefix(trimmedQuery, "<@") && strings.HasSuffix(trimmedQuery, ">") {
		userID := strings.TrimSuffix(strings.TrimPrefix(trimmedQuery, "<@"), ">")
		userID = strings.TrimPrefix(userID, "!")
		if userID != "" {
			member, err := session.GuildMember(guildID, userID)
			if err == nil && member != nil {
				return member, nil
			}
		}
	}

	// Raw snowflake ID
	if isSnowflakeID(trimmedQuery) {
		member, err := session.GuildMember(guildID, trimmedQuery)
		if err == nil && member != nil {
			return member, nil
		}
	}

	// Name search
	members, err := session.GuildMembersSearch(guildID, trimmedQuery, 10)
	if err != nil {
		utils.Error("Failed to search members", "error", err)
		return nil, fmt.Errorf("❌ Failed to search members: %v", err)
	}

	switch len(members) {
	case 0:
		return nil, fmt.Errorf("❌ No member found matching '%s'", query)
	case 1:
		return members[0], nil
	default:
		return nil, fmt.Errorf("❌ Too many matches for '%s' (be more specific, or use a mention/ID)", query)
	}
}

func findGuildRoleIDByName(session *discordgo.Session, guildID, roleName string) (string, error) {
	roles, err := session.GuildRoles(guildID)
	if err != nil {
		return "", err
	}

	for _, role := range roles {
		if role != nil && role.Name == roleName {
			return role.ID, nil
		}
	}

	return "", nil
}

func splitCommaSeparated(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func joinOrFallback(lines []string, fallback string) string {
	joined := strings.Join(lines, "\n")
	if strings.TrimSpace(joined) == "" {
		return fallback
	}
	return joined
}

func isSnowflakeID(value string) bool {
	if len(value) < 15 {
		return false
	}
	for _, runeValue := range value {
		if runeValue < '0' || runeValue > '9' {
			return false
		}
	}
	return true
}
