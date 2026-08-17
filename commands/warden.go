package commands

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

const wardenRoleBaseNameDefault = "Verified Warden"

// wardenRoleBaseNameEnv overrides the base name every warden role is composed
// from.
const wardenRoleBaseNameEnv = "WARDEN_ROLE_BASE_NAME"

// WardenRoleBaseName returns the configured base name, or the default when the
// variable is unset or empty. Read at call time rather than cached in a package
// var, as s3aar.go reads BM_TOKEN at the point of use — these are
// per-invocation Discord commands, so a getenv is free next to the API calls
// that follow. Exported because main() logs the resolved value at startup; a
// wrong name fails every warden subcommand identically, so the operator needs
// to read it back without reproducing that.
func WardenRoleBaseName() string {
	if configured := os.Getenv(wardenRoleBaseNameEnv); configured != "" {
		return configured
	}
	return wardenRoleBaseNameDefault
}

// maxBulkAddEntries caps how many comma-separated entries a single /warden
// bulkadd may carry. Each entry can trigger a GuildMembersSearch plus a per-role
// GuildMemberRoleAdd, and Discord allows a multi-thousand-character string
// option, so without a bound an operator could submit hundreds of names and fan
// out a serial API storm that outruns the rate limiter and the 15-minute
// interaction-token window (#173). The over-count is rejected up front, before
// any Discord API call.
const maxBulkAddEntries = 50

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
	// Guild-context guard runs FIRST, before any read of interaction.Member.
	// Warden requires guild context, and Discord only populates Member for guild
	// interactions; a DM-shaped or malformed interaction has a nil Member and a
	// nil GuildID. Rejecting on the empty GuildID here both gives a clear
	// server-only message and removes the latent nil-deref the entry log would
	// otherwise hit (#177).
	guildID := interaction.GuildID
	if guildID == "" {
		utils.HandleError(r, interaction, "❌ This command can only be used in a server (guild).")
		return
	}

	// Entry log reads the invoking user through the nil-safe helper rather than
	// interaction.Member.User directly, so it never panics regardless of context.
	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting Warden", "command", "Warden", "username", username, "discord_id", discordID)

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
			editEphemeral(
				r,
				interaction,
				roleMutationErrorReply(
					"add", roleName, formatUser(member), err,
					"Failed to add warden role", "user", member.User.ID, "role", roleName,
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
			editEphemeral(r, interaction, roleMutationErrorReply(
				"remove", roleName, formatUser(member), err,
				"Failed to remove warden role", "user", member.User.ID, "role", roleName,
			))
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

	// Parse and bound the entry list BEFORE any Discord API call (role
	// resolution, member search, role-add). resolveWardenRoleIDs below issues a
	// GuildRoles request, and the per-entry loop fans out a GuildMembersSearch +
	// per-role GuildMemberRoleAdd for every entry, so the count check has to run
	// ahead of all of it to actually prevent the storm (#173).
	requestedQueries := splitCommaSeparated(query)
	if len(requestedQueries) == 0 {
		editEphemeral(r, interaction, "⚠️ Nothing to do.")
		return
	}
	if len(requestedQueries) > maxBulkAddEntries {
		editEphemeral(r, interaction, fmt.Sprintf(
			"❌ Too many entries (%d). You can add at most %d at once; split this into smaller batches.",
			len(requestedQueries), maxBulkAddEntries,
		))
		return
	}

	roleIDs, roleNames, err := resolveWardenRoleIDs(gm, guildID, roleScope)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	var addedMembers []*discordgo.Member
	var failures []string
	// Two collectors per run, one per operation phase (member lookup vs role add),
	// each collapsing its per-entry system-fault captures to one event per fault
	// signature (#214, #216). They are kept SEPARATE rather than shared because a
	// member LOOKUP (GuildMember/GuildMembersSearch) and a role ADD
	// (GuildMemberRoleAdd) are distinct root causes with their own flush message: a
	// lookup 500 and a role-add 500 carry the same signature but must not fold into
	// one event. System faults feed the collectors during the loop and they flush
	// once after; non-captured client faults (403, not-in-server 404) are listed but
	// never routed here, per ADR 0001. Both flushes are deferred immediately, so an
	// early return or a panic between the loop and the flush can never silently drop
	// pending captures; guildID is fixed for the run, so the deferred args are exact.
	//
	// Each collector MUST keep its own matching deferred flush: records only reach
	// Sentry at flush, so dropping one defer would silently discard that phase's
	// pending captures (partial Sentry blindness for that phase).
	lookupFaultCapture := newFaultCollector()
	defer lookupFaultCapture.flush(
		"Failed to look up guild member in bulk",
		"command", "warden", "guild", guildID,
	)
	faultCapture := newFaultCollector()
	defer faultCapture.flush(
		"Failed to add warden role in bulk",
		"command", "warden", "guild", guildID,
	)
	for _, singleQuery := range requestedQueries {
		// A lookup system fault feeds the lookup collector keyed by signature
		// instead of capturing once per entry, so a 5xx storm during resolution
		// pages once per signature rather than once per roster entry (#216).
		member, memberErr := findGuildMemberCollecting(gm, guildID, singleQuery, lookupFaultCapture.recordSystemFault)
		if memberErr != nil {
			failures = append(failures, memberErr.Error())
			continue
		}

		allOK := true
		for index, roleID := range roleIDs {
			roleName := roleNames[index]
			if err := gm.GuildMemberRoleAdd(guildID, member.User.ID, roleID); err != nil {
				// Build the per-member message immediately, but hand a genuine system
				// fault to the collector instead of capturing it here, so a deleted-role
				// storm pages once per signature rather than once per member-role add.
				class := classifyDiscordError(err)
				if class.SystemFault {
					faultCapture.recordSystemFault(err, member.User.ID)
				}
				failures = append(failures, roleMutationErrorMessage("add", roleName, formatUser(member), class))
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
		// A genuine GuildChannels fault (5xx/transport) is classified and captured
		// to Sentry; a 4xx stays a non-captured actionable message. The raw Discord
		// response body is never interpolated into the operator reply (#195).
		editEphemeral(r, interaction, channelsResolveErrorReply(
			err,
			"Failed to retrieve guild channels for warden purge",
			"command", "warden", "guild", guildID,
		).Error())
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
			// Two distinct failure shapes share recreateErr != nil:
			//
			//   - newRoleID == "": nothing usable was left behind (the create
			//     failed, or an overwrite step failed and the new role was cleaned
			//     up). Tell the operator the role was NOT recreated and they can
			//     retry.
			//   - newRoleID != "": the new role was created and configured, but
			//     deleting the OLD role failed, so a duplicate now lingers. Tell the
			//     operator the opposite — the recreate happened and the leftover old
			//     role needs manual cleanup.
			//
			// Both route the underlying error through the classifier so no raw
			// Discord body leaks and only genuine system faults page Sentry (ADR
			// 0001), via the swappable capture seam.
			if newRoleID != "" {
				summaryLines = append(summaryLines, purgePartialDeleteSummary(
					roleName, newRoleID, roleIDToRecreate, recreateErr,
					"Warden purge could not delete the old role after recreation",
					"guild", guildID,
					"roleName", roleName,
					"oldRoleID", roleIDToRecreate,
					"newRoleID", newRoleID,
				))
				continue
			}

			summaryLines = append(summaryLines, purgeRecreateErrorReply(
				roleName, recreateErr,
				"Warden purge role recreation failed",
				"guild", guildID,
				"roleName", roleName,
				"roleID", roleIDToRecreate,
			))
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

	deliverPurgeSummary(r, gm, interaction, joinOrFallback(summaryLines, "✅ Purge complete."))
}

// deliverPurgeSummary delivers the purge summary, with a token-expiry fallback.
// A purge re-applies channel overwrites with a per-channel throttle, so across
// many channels it can outlive Discord's 15-minute interaction token. Once that
// window closes the deferred-ephemeral edit can no longer be delivered, and the
// operator would otherwise be left with a spinner that never resolves on a
// destructive op — an unanswerable "did it finish?" that risks a re-run.
//
// On a successful edit nothing else happens (the normal ephemeral reply). When
// the edit fails:
//   - token expiry → post the summary to the invoking channel. This surface
//     does not depend on the interaction token, so it reaches the operator even
//     after the window. Trade-off: the channel message is NOT ephemeral, unlike
//     the normal reply, but a "your destructive op finished" notice is an
//     acceptable thing to leave visible.
//   - any other (unexpected) failure → capture to Sentry with context via the
//     shared seam, the same as the non-purge edit helpers.
func deliverPurgeSummary(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	summary string,
) {
	editErr := r.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{
		Content: &summary,
	})
	if editErr == nil {
		return
	}

	if isInteractionTokenExpired(editErr) {
		if sendErr := gm.ChannelMessageSend(interaction.ChannelID, summary); sendErr != nil {
			// Both surfaces failed: the operator can't be reached. This is a
			// genuine delivery fault worth paging on. Carry the original edit
			// error too, so on-call sees the full chain — the edit expired AND
			// the channel send failed — not just the second failure.
			captureError(
				"Failed to deliver purge summary via channel fallback after token expiry",
				sendErr,
				"command", "warden",
				"subcommand", wardenSubcommandOf(interaction),
				"guild_id", interaction.GuildID,
				"channel_id", interaction.ChannelID,
				"edit_error", editErr,
			)
			return
		}
		// Recovery, not a fault: the deferred edit expired but the channel
		// fallback reached the operator. Log (don't capture, per ADR 0001) so
		// "did the long purge ever surface its result?" is answerable from logs.
		utils.Info(
			"purge summary delivered via channel fallback after interaction token expired",
			"command", "warden",
			"subcommand", wardenSubcommandOf(interaction),
			"guild_id", interaction.GuildID,
			"channel_id", interaction.ChannelID,
		)
		return
	}

	// Unexpected (non-expiry) edit failure: capture with context, same seam the
	// other edit helpers funnel through.
	captureEditFailure(interaction, editErr)
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

	// GuildRoleCreate's POST sets every field below (name, color, hoist,
	// mentionable, permissions) in one call, so the role is fully configured the
	// moment it exists. There is deliberately NO follow-up GuildRoleEdit: a
	// redundant re-apply of these same fields could fail after the new role
	// already existed, leaving a duplicate orphan with the old role still in
	// place (#178). Dropping it removes that failure window entirely.
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

	reappliedOverwriteCount, err := reapplyRoleOverwrites(
		gm,
		newRole.ID,
		channelOverwritesByChannelID,
	)

	if err != nil {
		// The new role exists but its overwrites are incomplete and the old role
		// is still present: that is the orphan-duplicate state #178 is about.
		// Delete the new role so the guild is left with only the (untouched) old
		// role, not a half-configured duplicate.
		cleanupOrphanRole(gm, guildID, newRole.ID)
		return "", reappliedOverwriteCount, fmt.Errorf("reapply overwrites: %w", err)
	}

	if err := gm.GuildRoleDelete(guildID, oldRoleID); err != nil {
		// The new role is fully built and is the intended keeper; only the old
		// role's deletion failed. Do NOT delete the new role here — that would
		// throw away the completed recreation. Report the partial state up so the
		// operator knows the old role lingers and may need a manual delete.
		return newRole.ID, reappliedOverwriteCount, fmt.Errorf("delete old role: %w", err)
	}

	return newRole.ID, reappliedOverwriteCount, nil
}

// cleanupOrphanRole best-effort deletes a role created during a recreation that
// then failed downstream, so a partial recreate does not leave a duplicate
// orphan behind. A delete failure here is logged (not returned): the caller is
// already returning the original downstream error, and the cleanup-delete
// failing is a secondary fault — surfacing it would mask the real cause. If the
// delete genuinely fails the role may still linger, which the purge summary's
// "failed to recreate" line already warns the operator about.
func cleanupOrphanRole(gm GuildManager, guildID, roleID string) {
	if err := gm.GuildRoleDelete(guildID, roleID); err != nil {
		utils.Warn(
			"failed to clean up orphan role after a failed recreation",
			"guild", guildID,
			"roleID", roleID,
			"error", err,
		)
	}
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
	base := WardenRoleBaseName()

	if roleScope == "both" {
		return []string{
			base + " Internal",
			base + " External",
		}
	}

	// Every other scope names one role. Callers validate the scope against
	// wardenRoleScopes first, so in practice this is "internal" or "external";
	// an unvalidated scope composes a name that simply won't match a role.
	return []string{
		base + " " + wardenTitleCaser.String(roleScope),
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
		if errors.Is(findErr, errRoleNotFound) {
			return nil, nil, fmt.Errorf("❌ '%s' role not found in guild", roleName)
		}
		if findErr != nil {
			// A genuine GuildRoles fault (5xx/transport) is classified and captured
			// to Sentry; a 4xx stays a non-captured actionable message. The not-found
			// branch above is handled first, so it never reaches here (#194).
			return nil, nil, roleResolveErrorReply(
				findErr,
				"Failed to retrieve guild roles for warden role resolution",
				"command", "warden", "guild", guildID, "role", roleName,
			)
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
		captureEditFailure(interaction, err)
	}
}

func editEphemeralWithEmbed(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, content string, embed *discordgo.MessageEmbed) {
	edit := &discordgo.WebhookEdit{Content: &content}
	if embed != nil {
		edit.Embeds = &[]*discordgo.MessageEmbed{embed}
	}
	if err := r.InteractionResponseEdit(interaction.Interaction, edit); err != nil {
		captureEditFailure(interaction, err)
	}
}

// captureEditFailure handles a failed deferred-ephemeral edit. The interaction
// is already acknowledged by the defer, so the identical InteractionResponseEdit
// cannot be retried, and InteractionRespond would only be rejected as
// already-acknowledged. That is the dead-end loop utils.HandleError walks into
// here: Respond, get already-acknowledged, re-issue the same edit. So instead of
// retrying, we treat the lost reply as a genuine delivery failure and capture it
// to Sentry with the subcommand and guild read off the interaction, so on-call
// can attribute it even though the command may already have mutated state. This
// is the single failure-handling seam both edit helpers funnel through; future
// fallback-delivery handling can extend this single seam.
func captureEditFailure(interaction *discordgo.InteractionCreate, err error) {
	captureError(
		"Failed to deliver deferred-ephemeral edit",
		err,
		"command", "warden",
		"subcommand", wardenSubcommandOf(interaction),
		"guild_id", interaction.GuildID,
	)
}

// wardenSubcommandOf reads the chosen warden subcommand off the interaction's
// `command` option for failure context, falling back to "unknown" when it can't
// be resolved (e.g. a malformed interaction) so capture context is never blank.
//
// It rides captures under the "subcommand" key, never "command": the latter is
// promoted to a Sentry tag (see utils.promoteCommandTag) and must hold the
// registered slash-command name, or /warden's failures split across one group
// per subcommand and per-command error rate stops being answerable.
func wardenSubcommandOf(interaction *discordgo.InteractionCreate) string {
	if sub, ok := getOptionString(interaction.ApplicationCommandData(), "command"); ok {
		return sub
	}
	return "unknown"
}

func buildAddedMembersEmbed(members []*discordgo.Member) *discordgo.MessageEmbed {
	const maxDescLen = 4096
	var sb strings.Builder
	rendered := 0
	for _, m := range members {
		line := fmt.Sprintf("<@%s>\n", m.User.ID)
		if sb.Len()+len(line) > maxDescLen {
			_, _ = fmt.Fprintf(&sb, "... and %d more.", len(members)-rendered)
			break
		}
		sb.WriteString(line)
		rendered++
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

// findGuildMember resolves a single roster entry to a guild member, capturing any
// genuine lookup system fault to Sentry inline. It is the entry point for the
// single /warden add and /warden remove sites, which capture immediately; the
// bulk loop uses findGuildMemberCollecting with a collector-backed sink instead.
func findGuildMember(gm GuildManager, guildID, query string) (*discordgo.Member, error) {
	return findGuildMemberCollecting(gm, guildID, query, nil)
}

// findGuildMemberCollecting is findGuildMember with the lookup-fault capture
// decision delegated to faultSink. With a nil sink the leaf helpers capture each
// genuine system fault to Sentry inline (the single add/remove behavior); with a
// collector-backed sink the /warden bulkadd loop routes those captures through a
// faultCollector so a lookup-fault storm collapses to one event per signature
// (#216). The user-facing messages and the non-captured outcomes (empty/too-long
// query, not-in-server, no match, too many matches, 4xx) are identical either way.
func findGuildMemberCollecting(gm GuildManager, guildID, query string, faultSink lookupFaultSink) (*discordgo.Member, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, fmt.Errorf("❌ Empty query")
	}

	// Discord's member-search query must be 1-100 characters. Reject an
	// over-length input here, before it reaches GuildMembersSearch and 400s
	// (which gets captured to Sentry as an error). This runs ahead of the
	// mention/ID branches below, but real mentions and snowflakes are well
	// under 100 chars, so in practice only a long name search trips it.
	if utf8.RuneCountInString(trimmedQuery) > 100 {
		return nil, fmt.Errorf("❌ Query too long (max 100 characters); use a mention/ID instead")
	}

	// Mentions: <@123>, <@!123>. A mention unambiguously names one user, so the
	// targeted GuildMember lookup is authoritative — we never fall through to a
	// name search of the raw "<@...>" string (which can't match a display name
	// and would report a misleading "no member found" even for a transient API
	// fault). A 404 means the user really isn't here; any other failure is
	// surfaced (and captured if it's a genuine system fault).
	if userID, ok := mentionUserID(trimmedQuery); ok {
		return resolveMemberByID(gm, guildID, userID, faultSink)
	}

	// Raw snowflake ID: same authoritative treatment as a mention.
	if isSnowflakeID(trimmedQuery) {
		return resolveMemberByID(gm, guildID, trimmedQuery, faultSink)
	}

	// Name search
	members, err := gm.GuildMembersSearch(guildID, trimmedQuery, 10)
	if err != nil {
		return nil, searchErrorReply(err, faultSink, trimmedQuery)
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

// mentionUserID extracts the user snowflake from a Discord mention
// (<@123> or <@!123>). It returns ("", false) for anything that isn't a
// non-empty mention, so callers can branch on whether the input was a mention.
func mentionUserID(query string) (string, bool) {
	if !strings.HasPrefix(query, "<@") || !strings.HasSuffix(query, ">") {
		return "", false
	}
	userID := strings.TrimSuffix(strings.TrimPrefix(query, "<@"), ">")
	userID = strings.TrimPrefix(userID, "!")
	if userID == "" {
		return "", false
	}
	return userID, true
}

// resolveMemberByID performs the authoritative targeted member lookup used for
// mentions and raw snowflakes. The result is trusted: it never falls through to
// a name search. A 404 yields a clear "not in this server" message; any other
// failure is routed through the shared classifier so a genuine system fault is
// captured (inline when faultSink is nil, or collected when the bulk loop supplies
// a collector-backed sink) and the raw Discord body never reaches the reply.
func resolveMemberByID(gm GuildManager, guildID, userID string, faultSink lookupFaultSink) (*discordgo.Member, error) {
	member, err := gm.GuildMember(guildID, userID)
	if err != nil {
		class := classifyDiscordError(err)
		switch {
		case class.NotFound:
			return nil, fmt.Errorf("❌ <@%s> is not in this server", userID)
		case class.SystemFault:
			recordLookupFault(faultSink, err, userID, "Failed to look up guild member by ID", "user_id", userID)
			if class.ConfigFault {
				return nil, fmt.Errorf("❌ Could not look up <@%s>: %s", userID, configFaultHint(class))
			}
			return nil, errors.New("❌ Member lookup is temporarily unavailable (Discord error); please try again shortly")
		default:
			return nil, fmt.Errorf("❌ Could not look up <@%s> (%s)", userID, class.UserDetail)
		}
	}
	if member == nil {
		return nil, fmt.Errorf("❌ <@%s> is not in this server", userID)
	}
	return member, nil
}

// errRoleNotFound is the sentinel findGuildRoleIDByName returns when no guild
// role matches the requested name. It makes the not-found case explicit and
// checkable (errors.Is) so a caller cannot misread it as success, and keeps it
// distinct from a genuine GuildRoles API failure (which surfaces as a different,
// wrapped error). See ADR 0002 / the "empty vs failure" invariant: ("",
// errRoleNotFound) means definitively absent; ("", someOtherErr) means upstream
// failure; (id, nil) means found.
var errRoleNotFound = errors.New("role not found")

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

	return "", fmt.Errorf("%q: %w", roleName, errRoleNotFound)
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
