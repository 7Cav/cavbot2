package commands

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

const wardenRoleBaseName = "Verified Warden"

// wardenOverwriteDelay throttles successive channel-permission writes during a
// purge to stay under Discord's rate limit. A package var (not a const) so
// tests can zero it out and avoid sleeping. See warden_test.go.
var wardenOverwriteDelay = 200 * time.Millisecond

var (
	wardenRoleScopes  = []string{"internal", "external", "both"}
	wardenSubcommands = []string{
		"add",
		"remove",
		"bulkadd",
		"purge",
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

func handleWarden(
	session *discordgo.Session,
	interaction *discordgo.InteractionCreate,
) {
	runWarden(utils.NewSessionResponder(session), NewSessionGuildManager(session), interaction)
}

func runWarden(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
) {
	utils.Info("🚀 Starting Warden", "command", "Warden", "username", interaction.Member.User.Username, "discord_id", interaction.Member.User.ID)

	commandData := interaction.ApplicationCommandData()

	subcommand, ok := getOptionString(commandData, "command")
	if !ok || !slices.Contains(wardenSubcommands, subcommand) {
		utils.HandleError(
			r,
			interaction,
			"❌ Invalid warden command; must be "+strings.Join(wardenSubcommands, ", "),
		)
		return
	}

	roleScope, ok := getOptionString(commandData, "flag")
	if !ok || !slices.Contains(wardenRoleScopes, roleScope) {
		utils.HandleError(
			r,
			interaction,
			"❌ Missing or invalid flag argument; must be 'internal', 'external', or 'both'",
		)
		return
	}

	guildID := interaction.GuildID
	if guildID == "" {
		utils.HandleError(r, interaction, "❌ This command can only be used in a server (guild).")
		return
	}

	query, _ := getOptionString(
		commandData,
		"discordname",
	)

	query = strings.TrimSpace(query)

	if subcommand != "purge" && query == "" {
		utils.HandleError(r, interaction, "❌ Missing discordname argument for this command")
		return
	}

	utils.Debug("Warden command invoked", "command", subcommand, "query", query, "flag", roleScope)

	switch subcommand {
	case "add":
		handleWardenAdd(r, gm, interaction, guildID, query, roleScope)
	case "remove":
		handleWardenRemove(r, gm, interaction, guildID, query, roleScope)
	case "bulkadd":
		handleWardenBulkAdd(r, gm, interaction, guildID, query, roleScope)
	case "purge":
		handleWardenPurge(r, gm, interaction, guildID, roleScope)
	default:
		utils.HandleError(r, interaction, "❌ Unknown subcommand")
	}

	utils.Info("✨ Done!", "command", "Warden")
}

func handleWardenAdd(r utils.InteractionResponder, gm GuildManager, interaction *discordgo.InteractionCreate, guildID, query, roleScope string) {
	if err := deferEphemeral(r, interaction); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to acknowledge: %v", err))
		return
	}

	member, err := findGuildMember(gm, guildID, query)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	roleIDs, roleNames, err := resolveWardenRoleIDs(gm, guildID, roleScope)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	for index, roleID := range roleIDs {
		roleName := roleNames[index]
		if err := gm.GuildMemberRoleAdd(guildID, member.User.ID, roleID); err != nil {
			utils.CaptureError("Failed to add warden role", err, "user", member.User.ID, "role", roleName)
			editEphemeral(
				r,
				interaction,
				fmt.Sprintf(
					"❌ Failed to add '%s' role to %s: %v",
					roleName,
					formatUser(member),
					err,
				),
			)
			return
		}
	}

	utils.Info("Warden role(s) added", "user", member.User.ID, "roles", strings.Join(roleNames, ", "))
	editEphemeral(
		r,
		interaction,
		fmt.Sprintf(
			"✅ Added warden role(s) (%s) to %s",
			strings.Join(roleNames, ", "),
			formatUser(member),
		),
	)
}

func handleWardenRemove(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	guildID string,
	query string,
	roleScope string,
) {
	if err := deferEphemeral(r, interaction); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to acknowledge: %v", err))
		return
	}

	member, err := findGuildMember(gm, guildID, query)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	roleIDs, roleNames, err := resolveWardenRoleIDs(gm, guildID, roleScope)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	for index, roleID := range roleIDs {
		roleName := roleNames[index]
		if err := gm.GuildMemberRoleRemove(guildID, member.User.ID, roleID); err != nil {
			utils.CaptureError("Failed to remove warden role", err, "user", member.User.ID, "role", roleName)
			editEphemeral(r, interaction, fmt.Sprintf("❌ Failed to remove '%s' role from %s: %v", roleName, formatUser(member), err))
			return
		}
	}

	utils.Info("Warden role(s) removed", "user", member.User.ID, "roles", strings.Join(roleNames, ", "))
	editEphemeral(r, interaction, fmt.Sprintf("✅ Removed warden role(s) (%s) from %s", strings.Join(roleNames, ", "), formatUser(member)))
}

func handleWardenBulkAdd(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	guildID string,
	query string,
	roleScope string,
) {
	if err := deferEphemeral(r, interaction); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to acknowledge bulk add: %v", err))
		return
	}

	roleIDs, roleNames, err := resolveWardenRoleIDs(gm, guildID, roleScope)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	requestedQueries := splitCommaSeparated(query)
	if len(requestedQueries) == 0 {
		editEphemeral(r, interaction, "⚠️ Nothing to do.")
		return
	}

	var addedMembers []*discordgo.Member
	var failures []string
	for _, singleQuery := range requestedQueries {
		member, memberErr := findGuildMember(gm, guildID, singleQuery)
		if memberErr != nil {
			failures = append(failures, memberErr.Error())
			continue
		}

		allOK := true
		for index, roleID := range roleIDs {
			roleName := roleNames[index]
			if err := gm.GuildMemberRoleAdd(guildID, member.User.ID, roleID); err != nil {
				utils.CaptureError("Failed to add warden role in bulk", err, "user", member.User.ID, "role", roleName)
				failures = append(failures, fmt.Sprintf("❌ Failed to add '%s' role to %s: %v", roleName, formatUser(member), err))
				allOK = false
			}
		}
		if allOK {
			addedMembers = append(addedMembers, member)
		}
	}

	content := buildBulkAddSummary(len(addedMembers), failures)
	var embed *discordgo.MessageEmbed
	if len(addedMembers) > 0 {
		embed = buildAddedMembersEmbed(addedMembers)
	}
	editEphemeralWithEmbed(r, interaction, content, embed)
}

func handleWardenPurge(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	guildID string,
	roleScope string,
) {
	if err := deferEphemeral(r, interaction); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to acknowledge purge: %v", err))
		return
	}

	go func() {
		defer utils.RecoverPanic("warden-purge")
		runWardenPurge(r, gm, interaction, guildID, roleScope)
	}()
}

// runWardenPurge performs the role-recreation purge. Extracted from the inline
// goroutine in handleWardenPurge so it is directly callable from tests with a
// fake GuildManager/responder; the caller (handleWardenPurge) owns the
// goroutine + panic recovery and the deferred-ephemeral acknowledge.
func runWardenPurge(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	guildID string,
	roleScope string,
) {
	roleIDsToRecreate, roleNamesToRecreate, err := resolveWardenRoleIDs(gm, guildID, roleScope)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	if len(roleIDsToRecreate) == 0 {
		editEphemeral(r, interaction, "❌ No roles resolved for purge scope.")
		return
	}

	guildChannels, err := gm.GuildChannels(guildID)
	if err != nil {
		editEphemeral(r, interaction, fmt.Sprintf("❌ Failed to retrieve guild channels: %v", err))
		return
	}

	var summaryLines []string

	for index, roleIDToRecreate := range roleIDsToRecreate {
		roleName := roleNamesToRecreate[index]

		newRoleID, reappliedOverwriteCount, recreateErr := recreateRoleWithChannelOverwrites(
			gm,
			guildID,
			roleIDToRecreate,
			guildChannels,
		)

		if recreateErr != nil {
			utils.CaptureError(
				"Warden purge role recreation failed",
				recreateErr,
				"guild", guildID,
				"roleName", roleName,
				"roleID", roleIDToRecreate,
			)

			summaryLines = append(summaryLines, fmt.Sprintf("❌ Failed to recreate '%s': %v", roleName, recreateErr))
			continue
		}

		summaryLines = append(
			summaryLines,
			fmt.Sprintf(
				"✅ Recreated '%s' (old: `%s`, new: `%s`), re-applied %d overwrite(s).",
				roleName,
				roleIDToRecreate,
				newRoleID,
				reappliedOverwriteCount,
			),
		)
	}

	editEphemeral(r, interaction, joinOrFallback(summaryLines, "✅ Purge complete."))
}

func recreateRoleWithChannelOverwrites(
	gm GuildManager,
	guildID string,
	oldRoleID string,
	guildChannels []*discordgo.Channel,
) (string, int, error) {
	oldRole, err := fetchGuildRoleByID(gm, guildID, oldRoleID)
	if err != nil {
		return "", 0, fmt.Errorf("fetch role: %w", err)
	}

	channelOverwritesByChannelID := collectRoleOverwritesByChannelID(oldRoleID, guildChannels)

	newRole, err := gm.GuildRoleCreate(guildID, &discordgo.RoleParams{
		Name:        oldRole.Name,
		Color:       &oldRole.Color,
		Hoist:       &oldRole.Hoist,
		Mentionable: &oldRole.Mentionable,
		Permissions: &oldRole.Permissions,
	})

	if err != nil {
		return "", 0, fmt.Errorf("create role: %w", err)
	}

	if err := applyRoleProperties(gm, guildID, newRole.ID, oldRole); err != nil {
		return "", 0, fmt.Errorf("apply role properties: %w", err)
	}

	reappliedOverwriteCount, err := reapplyRoleOverwrites(
		gm,
		newRole.ID,
		channelOverwritesByChannelID,
	)

	if err != nil {
		return "", reappliedOverwriteCount, fmt.Errorf("reapply overwrites: %w", err)
	}

	if err := gm.GuildRoleDelete(guildID, oldRoleID); err != nil {
		return newRole.ID, reappliedOverwriteCount, fmt.Errorf("delete old role: %w", err)
	}

	return newRole.ID, reappliedOverwriteCount, nil
}

func fetchGuildRoleByID(gm GuildManager, guildID, roleID string) (*discordgo.Role, error) {
	guildRoles, err := gm.GuildRoles(guildID)

	if err != nil {
		return nil, err
	}

	for _, role := range guildRoles {
		if role.ID == roleID {
			return role, nil
		}
	}

	return nil, fmt.Errorf("role id %s not found", roleID)
}

func collectRoleOverwritesByChannelID(
	roleID string,
	guildChannels []*discordgo.Channel,
) map[string]*discordgo.PermissionOverwrite {
	channelOverwritesByChannelID := make(map[string]*discordgo.PermissionOverwrite, 32)

	for _, channel := range guildChannels {
		for _, overwrite := range channel.PermissionOverwrites {
			if overwrite.Type != discordgo.PermissionOverwriteTypeRole {
				continue
			}
			if overwrite.ID != roleID {
				continue
			}

			overwriteCopy := *overwrite
			channelOverwritesByChannelID[channel.ID] = &overwriteCopy
		}
	}

	return channelOverwritesByChannelID
}

func applyRoleProperties(
	gm GuildManager,
	guildID string,
	roleID string,
	sourceRole *discordgo.Role,
) error {
	_, err := gm.GuildRoleEdit(guildID, roleID, &discordgo.RoleParams{
		Name:        sourceRole.Name,
		Color:       &sourceRole.Color,
		Hoist:       &sourceRole.Hoist,
		Mentionable: &sourceRole.Mentionable,
		Permissions: &sourceRole.Permissions,
	})
	return err
}

func reapplyRoleOverwrites(
	gm GuildManager,
	newRoleID string,
	channelOverwritesByChannelID map[string]*discordgo.PermissionOverwrite,
) (int, error) {
	reappliedCount := 0

	// Stable order (maps iterate randomly)
	channelIDs := make([]string, 0, len(channelOverwritesByChannelID))
	for channelID := range channelOverwritesByChannelID {
		channelIDs = append(channelIDs, channelID)
	}
	slices.Sort(channelIDs)

	for _, channelID := range channelIDs {
		overwrite := channelOverwritesByChannelID[channelID]

		if err := gm.ChannelPermissionSet(
			channelID,
			newRoleID,
			discordgo.PermissionOverwriteTypeRole,
			overwrite.Allow,
			overwrite.Deny,
		); err != nil {
			return reappliedCount, fmt.Errorf("channel %s: %w", channelID, err)
		}

		reappliedCount++
		time.Sleep(wardenOverwriteDelay)
	}

	return reappliedCount, nil
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

func resolveWardenRoleIDs(
	gm GuildManager,
	guildID string,
	roleScope string,
) (roleIDs []string, roleNames []string, err error) {
	roleNames = resolveWardenRoleNames(roleScope)
	roleIDs = make([]string, 0, len(roleNames))

	for _, roleName := range roleNames {
		roleID, findErr := findGuildRoleIDByName(gm, guildID, roleName)
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

func getOptionString(
	commandData discordgo.ApplicationCommandInteractionData,
	optionName string,
) (string, bool) {
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

func deferEphemeral(r utils.InteractionResponder, interaction *discordgo.InteractionCreate) error {
	return r.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
}

func editEphemeral(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, content string) {
	if err := r.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{
		Content: &content,
	}); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to edit response: %v", err))
	}
}

func editEphemeralWithEmbed(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, content string, embed *discordgo.MessageEmbed) {
	edit := &discordgo.WebhookEdit{Content: &content}
	if embed != nil {
		edit.Embeds = &[]*discordgo.MessageEmbed{embed}
	}
	if err := r.InteractionResponseEdit(interaction.Interaction, edit); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to edit response: %v", err))
	}
}

func buildAddedMembersEmbed(members []*discordgo.Member) *discordgo.MessageEmbed {
	const maxDescLen = 4096
	var sb strings.Builder
	for _, m := range members {
		line := fmt.Sprintf("<@%s>\n", m.User.ID)
		if sb.Len()+len(line) > maxDescLen {
			_, _ = fmt.Fprintf(&sb, "... and %d more.", len(members)-strings.Count(sb.String(), "\n"))
			break
		}
		sb.WriteString(line)
	}
	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Added %d user(s)", len(members)),
		Description: strings.TrimRight(sb.String(), "\n"),
		Color:       0xfbcc29, // Cav Yellow
	}
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

func findGuildMember(gm GuildManager, guildID, query string) (*discordgo.Member, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, fmt.Errorf("❌ Empty query")
	}

	// Mentions: <@123>, <@!123>
	if strings.HasPrefix(trimmedQuery, "<@") && strings.HasSuffix(trimmedQuery, ">") {
		userID := strings.TrimSuffix(strings.TrimPrefix(trimmedQuery, "<@"), ">")
		userID = strings.TrimPrefix(userID, "!")
		if userID != "" {
			member, err := gm.GuildMember(guildID, userID)
			if err == nil && member != nil {
				return member, nil
			}
		}
	}

	// Raw snowflake ID
	if isSnowflakeID(trimmedQuery) {
		member, err := gm.GuildMember(guildID, trimmedQuery)
		if err == nil && member != nil {
			return member, nil
		}
	}

	// Name search
	members, err := gm.GuildMembersSearch(guildID, trimmedQuery, 10)
	if err != nil {
		utils.CaptureError("Failed to search members", err)
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

func findGuildRoleIDByName(gm GuildManager, guildID, roleName string) (string, error) {
	roles, err := gm.GuildRoles(guildID)
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

// buildBulkAddSummary composes the bulk-add response message.
// Successes are collapsed into a count to keep the message short.
// Failures are listed individually, then truncated if needed to stay
// within Discord's 2000-character message limit.
func buildBulkAddSummary(successCount int, failures []string) string {
	const maxLen = 2000

	var lines []string
	if successCount > 0 {
		lines = append(lines, fmt.Sprintf("✅ Added warden role(s) to %d user(s).", successCount))
	}
	lines = append(lines, failures...)

	if len(lines) == 0 {
		return "⚠️ Nothing to do."
	}

	msg := strings.Join(lines, "\n")
	if len(msg) <= maxLen {
		return msg
	}

	// Truncate: fit as many lines as possible, then append an overflow count.
	var kept []string
	for i, line := range lines {
		overflowNote := fmt.Sprintf("\n... and %d more.", len(lines)-i)
		if len(strings.Join(append(kept, line), "\n"))+len(overflowNote) > maxLen {
			kept = append(kept, fmt.Sprintf("... and %d more.", len(lines)-i))
			break
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
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
