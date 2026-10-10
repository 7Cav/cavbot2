package commands

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// foxholeRoleBaseNameDefault is what the Foxhole roles are named in Discord.
const foxholeRoleBaseNameDefault = "Verified Foxhole"

// foxholeRoleBaseNameEnv overrides the base name every Foxhole role is
// composed from.
const foxholeRoleBaseNameEnv = "FOXHOLE_ROLE_BASE_NAME"

// foxholeRoleBaseNameOldEnv is foxholeRoleBaseNameEnv's name before the
// rename. A host that still passes it keeps working until the cleanup in
// #455 drops it.
const foxholeRoleBaseNameOldEnv = "WARDEN_ROLE_BASE_NAME"

// foxholeRoleBaseName returns the configured base name, or the default when
// neither variable is set. Read at the point of use, as s3aar.go reads
// BM_TOKEN.
func foxholeRoleBaseName() string {
	name, _ := resolveFoxholeRoleBaseName()
	return name
}

// resolveFoxholeRoleBaseName returns the base name and whether it came from
// the old variable. The new variable wins when set; unset and empty both
// fall through.
func resolveFoxholeRoleBaseName() (name string, fromOldEnv bool) {
	if configured := os.Getenv(foxholeRoleBaseNameEnv); configured != "" {
		return configured, false
	}
	if configured := os.Getenv(foxholeRoleBaseNameOldEnv); configured != "" {
		return configured, true
	}
	return foxholeRoleBaseNameDefault, false
}

// LogFoxholeRoleBaseName logs the resolved base name, once, at startup. When
// the name came from the old variable it warns first, naming the variable to
// rename before the cleanup drops it. Commands resolve the name on every run
// and never warn, so the warning shows once per start.
func LogFoxholeRoleBaseName() {
	name, fromOldEnv := resolveFoxholeRoleBaseName()
	if fromOldEnv {
		utils.Warn("Foxhole role base name read from the old variable; rename it",
			"variable", foxholeRoleBaseNameOldEnv,
			"rename_to", foxholeRoleBaseNameEnv,
			"cleanup_on_or_after", foxholeRenameCutoff.Format(time.DateOnly))
	}
	utils.Info("Foxhole role base name resolved", "base_name", name)
}

// maxBulkAddEntries caps how many comma-separated entries a single /foxhole
// bulkadd may carry. Each entry can trigger a GuildMembersSearch plus a per-role
// GuildMemberRoleAdd, and Discord allows a multi-thousand-character string
// option, so without a bound an operator could submit hundreds of names and fan
// out a serial API storm that outruns the rate limiter and the 15-minute
// interaction-token window (#173). The over-count is rejected up front, before
// any Discord API call.
const maxBulkAddEntries = 50

// purgeOverwriteDelay throttles successive channel-permission writes during a
// purge to stay under Discord's rate limit. A package var (not a const) so
// tests can zero it out and avoid sleeping. See foxhole_test.go.
var purgeOverwriteDelay = 200 * time.Millisecond

var (
	foxholeRoleScopes  = []string{"internal", "external", "both"}
	foxholeSubcommands = []string{
		"add",
		"remove",
		"bulkadd",
		"purge",
	}

	foxholeTitleCaser = cases.Title(language.Und, cases.NoLower)
)

// Foxhole declares /foxhole over the Foxhole runtime, nil on a host with
// no bot store.
func Foxhole(fx *FoxholeRuntime) Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "foxhole",
			Description: "Add, remove, bulk-add or purge the Foxhole roles",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "command",
					Description: "Choose between " + strings.Join(foxholeSubcommands, ", "),
					Required:    true,
					Choices:     stringChoices(foxholeSubcommands),
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "flag",
					Description: "Internal/external scope, or both",
					Required:    true,
					Choices:     stringChoices(foxholeRoleScopes),
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "discordname",
					Description: "Mention/ID/partial name; for bulkadd use comma-separated list; optional for purge",
					Required:    false,
				},
			},
		},
		Handler: func(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
			runFoxhole(utils.NewSessionResponder(session), NewSessionGuildManager(session), fx, interaction)
		},
	}
}

func runFoxhole(
	r utils.InteractionResponder,
	gm GuildManager,
	fx *FoxholeRuntime,
	interaction *discordgo.InteractionCreate,
) {
	// A purge runs on in the background, so it gives the one-action-at-a-time
	// rule back and sends its own note once its summary is out. A missed
	// acknowledgement leaves no interaction to send the note on.
	purging, missedAck := false, false
	end := func() {}
	defer func() {
		if purging {
			return
		}
		end()
		if !missedAck {
			sendRenameNote(r, interaction, "warden", "foxhole")
		}
	}()

	// Guild-context guard runs FIRST, before any read of interaction.Member.
	// /foxhole requires guild context, and Discord only populates Member for guild
	// interactions; a DM-shaped or malformed interaction has a nil Member and a
	// nil GuildID. Rejecting on the empty GuildID here both gives a clear
	// server-only message and removes the latent nil-deref the entry log would
	// otherwise hit (#177).
	guildID := interaction.GuildID
	if guildID == "" {
		missedAck = refuse(r, interaction, "❌ This command can only be used in a server (guild).")
		return
	}

	// Entry log reads the invoking user through the nil-safe helper rather than
	// interaction.Member.User directly, so it never panics regardless of context.
	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting Foxhole", "command", commandNameOf(interaction), "username", username, "discord_id", discordID)

	commandData := interaction.ApplicationCommandData()

	subcommand, ok := getOptionString(commandData, "command")
	if !ok || !slices.Contains(foxholeSubcommands, subcommand) {
		missedAck = refuse(
			r,
			interaction,
			"❌ Invalid /"+commandNameOf(interaction)+" command; must be "+strings.Join(foxholeSubcommands, ", "),
		)
		return
	}

	roleScope, ok := getOptionString(commandData, "flag")
	if !ok || !slices.Contains(foxholeRoleScopes, roleScope) {
		missedAck = refuse(
			r,
			interaction,
			"❌ Missing or invalid flag argument; must be 'internal', 'external', or 'both'",
			"subcommand", subcommand,
		)
		return
	}

	query, _ := getOptionString(
		commandData,
		"discordname",
	)

	query = strings.TrimSpace(query)

	if subcommand != "purge" && query == "" {
		missedAck = refuse(r, interaction, "❌ Missing discordname argument for this command", "subcommand", subcommand)
		return
	}

	utils.Debug("Foxhole command invoked", "command", subcommand, "query", query, "flag", roleScope)

	// Every subcommand changes roles, so none runs alongside a Foxhole
	// action started on the Foxhole page.
	run := foxholeCommandRun(interaction, subcommand)
	var refused *RunningAction
	if end, refused = fx.startCommand(run); refused != nil {
		missedAck = refuseForPageAction(r, interaction, run, *refused, "subcommand", subcommand)
		return
	}

	// Every subcommand answers in one reply only its member sees.
	if err := deferEphemeral(r, interaction); err != nil {
		missedAck = isUnknownInteraction(err)
		replyAckFailed(r, interaction, err, "subcommand", subcommand)
		return
	}

	switch subcommand {
	case "add":
		handleFoxholeAdd(r, gm, interaction, guildID, query, roleScope)
	case "remove":
		handleFoxholeRemove(r, gm, interaction, guildID, query, roleScope)
	case "bulkadd":
		handleFoxholeBulkAdd(r, gm, interaction, guildID, query, roleScope)
	case "purge":
		purging = true
		handleFoxholePurge(r, gm, interaction, guildID, roleScope, end)
	default:
		replyError(r, interaction, "❌ Unknown subcommand")
	}

	utils.Info("✨ Done!", "command", commandNameOf(interaction))
}

// refuseForPageAction answers a role-changing Foxhole command run sent
// while a Foxhole action started on the Foxhole page runs, with a reply only
// its member sees naming the action, who started it and how far it has
// got. The command changes nothing. The reply goes through refuse, with
// kv, and refuseForPageAction returns what refuse returns.
func refuseForPageAction(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, run CommandRun, action RunningAction, kv ...any) (missedAck bool) {
	utils.Info("Foxhole command refused: a page action is running", "command", commandNameOf(interaction),
		"typed", run.Command, "action", action.Name, "started_by", action.StartedBy)
	return refuse(r, interaction, fmt.Sprintf(
		"❌ A Foxhole action is running on the Foxhole page: %s, started by %s, %d of %d done. Nothing changed. Try again when it ends.",
		action.Name, action.StartedBy, action.Done, action.Total), kv...)
}

// foxholeCommandTyped is a Foxhole command run as its member typed it,
// under the name it ran: the command name, then typed, what the member
// picked after it, the subcommand or the roster add's unit.
func foxholeCommandTyped(interaction *discordgo.InteractionCreate, typed string) string {
	return fmt.Sprintf("/%s %s", commandNameOf(interaction), typed)
}

// foxholeAuditReason is the audit log reason a Foxhole command run carries
// on every change it makes, in the temp VC format: the command as typed,
// as foxholeCommandTyped gives it, then the member who ran it.
func foxholeAuditReason(interaction *discordgo.InteractionCreate, typed string) string {
	username, discordID := interactionUsernameAndID(interaction)
	by := Invoker{UserID: discordID, Username: username}
	return foxholeCommandTyped(interaction, typed) + " by " + by.auditName()
}

// foxholeCommandRun is a role-changing Foxhole command run as the Foxhole
// page names it while it runs: as typed, as foxholeCommandTyped gives it,
// the member who ran it under the names the interaction carries, and now.
func foxholeCommandRun(interaction *discordgo.InteractionCreate, typed string) CommandRun {
	run := CommandRun{Command: foxholeCommandTyped(interaction, typed), StartedAt: foxholeNow().UTC()}
	if user := interactionUser(interaction); user != nil {
		var nick string
		if interaction.Member != nil {
			nick = interaction.Member.Nick
		}
		run.By = store.MemberNames{MemberID: user.ID, Username: user.Username, DisplayName: cmp.Or(nick, user.GlobalName, user.Username)}
	}
	return run
}

func handleFoxholeAdd(r utils.InteractionResponder, gm GuildManager, interaction *discordgo.InteractionCreate, guildID, query, roleScope string) {
	member, err := findGuildMember(gm, guildID, query)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	roleIDs, roleNames, err := resolveFoxholeRoleIDs(gm, commandNameOf(interaction), guildID, roleScope)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	reason := foxholeAuditReason(interaction, "add")
	for index, roleID := range roleIDs {
		roleName := roleNames[index]
		if err := gm.GuildMemberRoleAdd(guildID, member.User.ID, roleID, reason); err != nil {
			editEphemeral(
				r,
				interaction,
				roleMutationErrorReply(
					"add", roleName, formatUser(member), err,
					"Failed to add Foxhole role", "user", member.User.ID, "role", roleName,
				),
			)
			return
		}
	}

	utils.Info("Foxhole role(s) added", "user", member.User.ID, "roles", strings.Join(roleNames, ", "))
	editEphemeral(
		r,
		interaction,
		fmt.Sprintf(
			"✅ Added Foxhole role(s) (%s) to %s",
			strings.Join(roleNames, ", "),
			formatUser(member),
		),
	)
}

func handleFoxholeRemove(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	guildID string,
	query string,
	roleScope string,
) {
	member, err := findGuildMember(gm, guildID, query)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	roleIDs, roleNames, err := resolveFoxholeRoleIDs(gm, commandNameOf(interaction), guildID, roleScope)
	if err != nil {
		editEphemeral(r, interaction, err.Error())
		return
	}

	reason := foxholeAuditReason(interaction, "remove")
	for index, roleID := range roleIDs {
		roleName := roleNames[index]
		if err := gm.GuildMemberRoleRemove(guildID, member.User.ID, roleID, reason); err != nil {
			editEphemeral(r, interaction, roleMutationErrorReply(
				"remove", roleName, formatUser(member), err,
				"Failed to remove Foxhole role", "user", member.User.ID, "role", roleName,
			))
			return
		}
	}

	utils.Info("Foxhole role(s) removed", "user", member.User.ID, "roles", strings.Join(roleNames, ", "))
	editEphemeral(r, interaction, fmt.Sprintf("✅ Removed Foxhole role(s) (%s) from %s", strings.Join(roleNames, ", "), formatUser(member)))
}

func handleFoxholeBulkAdd(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	guildID string,
	query string,
	roleScope string,
) {
	// Parse and bound the entry list BEFORE any Discord API call (role
	// resolution, member search, role-add). resolveFoxholeRoleIDs below issues a
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

	roleIDs, roleNames, err := resolveFoxholeRoleIDs(gm, commandNameOf(interaction), guildID, roleScope)
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
		"command", commandNameOf(interaction), "guild", guildID,
	)
	faultCapture := newFaultCollector()
	defer faultCapture.flush(
		"Failed to add Foxhole role in bulk",
		"command", commandNameOf(interaction), "guild", guildID,
	)
	reason := foxholeAuditReason(interaction, "bulkadd")
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
			if err := gm.GuildMemberRoleAdd(guildID, member.User.ID, roleID, reason); err != nil {
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

// handleFoxholePurge starts the purge in the background, which calls end
// once it has run.
func handleFoxholePurge(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	guildID string,
	roleScope string,
	end func(),
) {
	go func() {
		defer utils.RecoverPanic("foxhole-purge")
		defer end()
		runFoxholePurge(r, gm, interaction, guildID, roleScope)
		sendRenameNote(r, interaction, "warden", "foxhole")
	}()
}

// runFoxholePurge performs the role-recreation purge. Extracted from the inline
// goroutine in handleFoxholePurge so it is directly callable from tests with a
// fake GuildManager/responder; the caller (handleFoxholePurge) owns the
// goroutine + panic recovery, and runFoxhole the deferred-ephemeral
// acknowledge.
func runFoxholePurge(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
	guildID string,
	roleScope string,
) {
	roleIDsToRecreate, roleNamesToRecreate, err := resolveFoxholeRoleIDs(gm, commandNameOf(interaction), guildID, roleScope)
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
			"Failed to retrieve guild channels for Foxhole purge",
			"command", commandNameOf(interaction), "guild", guildID,
		).Error())
		return
	}

	var summaryLines []string
	reason := foxholeAuditReason(interaction, "purge")

	for index, roleIDToRecreate := range roleIDsToRecreate {
		roleName := roleNamesToRecreate[index]

		newRoleID, reappliedOverwriteCount, recreateErr := recreateRoleWithChannelOverwrites(
			gm,
			guildID,
			roleIDToRecreate,
			guildChannels,
			reason,
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
					"Foxhole purge could not delete the old role after recreation",
					"guild", guildID,
					"roleName", roleName,
					"oldRoleID", roleIDToRecreate,
					"newRoleID", newRoleID,
				))
				continue
			}

			summaryLines = append(summaryLines, purgeRecreateErrorReply(
				roleName, recreateErr,
				"Foxhole purge role recreation failed",
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
				"command", commandNameOf(interaction),
				"subcommand", foxholeSubcommandOf(interaction),
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
			"command", commandNameOf(interaction),
			"subcommand", foxholeSubcommandOf(interaction),
			"guild_id", interaction.GuildID,
			"channel_id", interaction.ChannelID,
		)
		return
	}

	// Unexpected (non-expiry) edit failure: capture with context, same seam the
	// other edit helpers funnel through.
	captureEditFailure(interaction, editErr)
}

// recreateRoleWithChannelOverwrites carries reason, the purge's audit log
// reason, on every change it makes.
func recreateRoleWithChannelOverwrites(
	gm GuildManager,
	guildID string,
	oldRoleID string,
	guildChannels []*discordgo.Channel,
	reason string,
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
	}, reason)

	if err != nil {
		return "", 0, fmt.Errorf("create role: %w", err)
	}

	reappliedOverwriteCount, err := reapplyRoleOverwrites(
		gm,
		newRole.ID,
		channelOverwritesByChannelID,
		reason,
	)

	if err != nil {
		// The new role exists but its overwrites are incomplete and the old role
		// is still present: that is the orphan-duplicate state #178 is about.
		// Delete the new role so the guild is left with only the (untouched) old
		// role, not a half-configured duplicate. Its reason says so, since a
		// role created and deleted moments apart reads oddly in the audit log.
		cleanupOrphanRole(gm, guildID, newRole.ID, reason+", undoing a failed recreate")
		return "", reappliedOverwriteCount, fmt.Errorf("reapply overwrites: %w", err)
	}

	if err := gm.GuildRoleDelete(guildID, oldRoleID, reason); err != nil {
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
func cleanupOrphanRole(gm GuildManager, guildID, roleID, reason string) {
	if err := gm.GuildRoleDelete(guildID, roleID, reason); err != nil {
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
	reason string,
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
			reason,
		); err != nil {
			return reappliedCount, fmt.Errorf("channel %s: %w", channelID, err)
		}

		reappliedCount++
		time.Sleep(purgeOverwriteDelay)
	}

	return reappliedCount, nil
}

// FoxholeRoleNames returns the names of the two Foxhole roles, Internal and
// External, composed from the configured base name, which it reads at each
// call as the commands do. The panel's Foxhole page finds the roles by these
// names in the guild's roles, the same exact match the commands make.
func FoxholeRoleNames() (internal, external string) {
	names := resolveFoxholeRoleNames("both")
	return names[0], names[1]
}

func resolveFoxholeRoleNames(roleScope string) []string {
	base := foxholeRoleBaseName()

	if roleScope == "both" {
		return []string{
			base + " Internal",
			base + " External",
		}
	}

	// Any other scope names one role; callers validate the scope first, so an
	// unrecognised one just composes a name that matches nothing.
	return []string{
		base + " " + foxholeTitleCaser.String(roleScope),
	}
}

func resolveFoxholeRoleIDs(
	gm GuildManager,
	command string,
	guildID string,
	roleScope string,
) (roleIDs []string, roleNames []string, err error) {
	roleNames = resolveFoxholeRoleNames(roleScope)
	roleIDs = make([]string, 0, len(roleNames))

	for _, roleName := range roleNames {
		roleID, findErr := findGuildRoleIDByName(gm, guildID, roleName)
		if errors.Is(findErr, errRoleNotFound) {
			return nil, nil, lookupReplyf("❌ '%s' role not found in guild", roleName)
		}
		if findErr != nil {
			// A genuine GuildRoles fault (5xx/transport) is classified and captured
			// to Sentry; a 4xx stays a non-captured actionable message. The not-found
			// branch above is handled first, so it never reaches here (#194).
			return nil, nil, roleResolveErrorReply(
				findErr,
				"Failed to retrieve guild roles for Foxhole role resolution",
				"command", command, "guild", guildID, "role", roleName,
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

// getOptionBool reads a boolean option, false when the invoker left it
// unset.
func getOptionBool(
	commandData discordgo.ApplicationCommandInteractionData,
	optionName string,
) bool {
	for _, option := range commandData.Options {
		if option != nil && option.Name == optionName {
			return option.BoolValue()
		}
	}
	return false
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
// to Sentry with the command, subcommand and guild read off the interaction, so
// on-call can attribute it even though the command may already have mutated
// state. The command is the registered name the run came in under, which also
// names /voice-rename, /voice-lock and /s3aar, the other callers. This
// is the single failure-handling seam both edit helpers funnel through; future
// fallback-delivery handling can extend this single seam.
func captureEditFailure(interaction *discordgo.InteractionCreate, err error) {
	captureError(
		"Failed to deliver deferred-ephemeral edit",
		err,
		"command", commandNameOf(interaction),
		"subcommand", foxholeSubcommandOf(interaction),
		"guild_id", interaction.GuildID,
	)
}

// foxholeSubcommandOf reads the chosen /foxhole subcommand off the interaction's
// `command` option for failure context, falling back to "unknown" when it can't
// be resolved (e.g. a malformed interaction) so capture context is never blank.
//
// It rides captures under the "subcommand" key, never "command": the latter is
// promoted to a Sentry tag (see utils.promoteCommandTag) and must hold the
// registered slash-command name, or /foxhole's failures split across one group
// per subcommand and per-command error rate stops being answerable.
func foxholeSubcommandOf(interaction *discordgo.InteractionCreate) string {
	if sub, ok := getOptionString(interaction.ApplicationCommandData(), "command"); ok {
		return sub
	}
	return "unknown"
}

// addedEmbedMore is the line that ends the added members embed when it
// can't name them all, counting the members it leaves out.
const addedEmbedMore = "... and %d more."

func buildAddedMembersEmbed(members []*discordgo.Member) *discordgo.MessageEmbed {
	var sb strings.Builder
	for i, m := range members {
		line := fmt.Sprintf("<@%s>\n", m.User.ID)
		// Name a member only while the count line for the members after it
		// still fits, so the count line that ends a cut-short list never
		// pushes the description past the limit.
		countLine := ""
		if i < len(members)-1 {
			countLine = fmt.Sprintf(addedEmbedMore, len(members)-i-1)
		}
		if sb.Len()+len(line)+len(countLine) > discordEmbedDescriptionLimit {
			_, _ = fmt.Fprintf(&sb, addedEmbedMore, len(members)-i)
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

// findGuildMember resolves a single roster entry to a guild member, capturing any
// genuine lookup system fault to Sentry inline. It is the entry point for the
// single /foxhole add and /foxhole remove sites, which capture immediately; the
// bulk loop uses findGuildMemberCollecting with a collector-backed sink instead.
func findGuildMember(gm GuildManager, guildID, query string) (*discordgo.Member, error) {
	return findGuildMemberCollecting(gm, guildID, query, nil)
}

// findGuildMemberCollecting is findGuildMember with the lookup-fault capture
// decision delegated to faultSink. With a nil sink the leaf helpers capture each
// genuine system fault to Sentry inline (the single add/remove behavior); with a
// collector-backed sink the /foxhole bulkadd loop routes those captures through a
// faultCollector so a lookup-fault storm collapses to one event per signature
// (#216). The user-facing messages and the non-captured outcomes (empty/too-long
// query, not-in-server, no match, too many matches, 4xx) are identical either way.
func findGuildMemberCollecting(gm GuildManager, guildID, query string, faultSink lookupFaultSink) (*discordgo.Member, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return nil, lookupReplyf("❌ Empty query")
	}

	// Discord's member-search query must be 1-100 characters. Reject an
	// over-length input here, before it reaches GuildMembersSearch and 400s
	// (which gets captured to Sentry as an error). This runs ahead of the
	// mention/ID branches below, but real mentions and snowflakes are well
	// under 100 chars, so in practice only a long name search trips it.
	if utf8.RuneCountInString(trimmedQuery) > 100 {
		return nil, lookupReplyf("❌ Query too long (max 100 characters); use a mention/ID instead")
	}

	// Mentions: <@123>, <@!123>. A mention unambiguously names one user, so the
	// targeted GuildMember lookup is authoritative — we never fall through to a
	// name search of the raw "<@...>" string (which can't match a display name
	// and would report a misleading "no member found" even for a transient API
	// fault). A 404 means the user really isn't here; any other failure is
	// surfaced (and captured if it's a genuine system fault).
	if userID, ok := MentionUserID(trimmedQuery); ok {
		return resolveMemberByID(gm, guildID, userID, faultSink)
	}

	// Raw snowflake ID: same authoritative treatment as a mention.
	if IsSnowflakeID(trimmedQuery) {
		return resolveMemberByID(gm, guildID, trimmedQuery, faultSink)
	}

	// Name search
	members, err := gm.GuildMembersSearch(guildID, trimmedQuery, 10)
	if err != nil {
		return nil, searchErrorReply(err, faultSink, trimmedQuery)
	}

	switch len(members) {
	case 0:
		return nil, lookupReplyf("❌ No member found matching '%s'", query)
	case 1:
		return members[0], nil
	default:
		return nil, lookupReplyf("❌ Too many matches for '%s' (be more specific, or use a mention/ID)", query)
	}
}

// MentionUserID extracts the user snowflake from a Discord mention
// (<@123> or <@!123>). It returns ("", false) for anything that isn't a
// non-empty mention, so callers can branch on whether the input was a mention.
func MentionUserID(query string) (string, bool) {
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
			return nil, lookupReplyf("❌ <@%s> %s", userID, adviceAbsent)
		case class.SystemFault:
			recordLookupFault(faultSink, err, userID, "Failed to look up guild member by ID", "user_id", userID)
			if class.ConfigFault {
				return nil, lookupReplyf("❌ Could not look up <@%s>: %s", userID, configFaultHint(class))
			}
			return nil, lookupReplyf("❌ Member lookup is temporarily unavailable (Discord error); please %s", adviceTransient)
		default:
			return nil, lookupReplyf("❌ Could not look up <@%s> (%s)", userID, class.UserDetail)
		}
	}
	if member == nil {
		return nil, lookupReplyf("❌ <@%s> %s", userID, adviceAbsent)
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
		lines = append(lines, fmt.Sprintf("✅ Added Foxhole role(s) to %d user(s).", successCount))
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

// IsSnowflakeID reports whether value reads as a Discord ID: digits only,
// at least 15 of them. The Foxhole page reads a pasted line's ID the same
// way the command reads its argument.
func IsSnowflakeID(value string) bool {
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
