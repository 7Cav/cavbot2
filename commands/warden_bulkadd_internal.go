package commands

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// wardenInternalRosterTimeout bounds the single milpac roster fetch. The
// per-trooper role-adds that follow are plain Discord calls inside the
// interaction's 15-minute window, so only the roster lookup needs a deadline.
const wardenInternalRosterTimeout = 30 * time.Second

// wardenInternalUnit is one row of the unit registry behind the
// /warden-bulkadd-internal picker. value is what the operator's choice emits and
// what fingerprints captures; label is shown in the dropdown and the report;
// query is the author-controlled milpac position-group search verified to
// isolate exactly that unit's roster.
//
// The registry is both the extension seam and the safety boundary (ADR 0009):
// adding a unit later is one new row with no logic change, and the operator can
// only ever emit a value the registry already contains.
type wardenInternalUnit struct {
	value string
	label string
	query string
}

// wardenInternalUnits is the unit registry. Each query is verified to
// substring-match only its own position group's titles before being added here.
// D/ACD is the one validated internal unit today; the next is one more row.
var wardenInternalUnits = []wardenInternalUnit{
	{value: "D/ACD", label: "D/ACD", query: "D/ACD"},
}

// lookupWardenInternalUnit resolves a picker value to its registry row. The
// boolean is the safety check: an unregistered value never resolves, so no query
// outside the registry can ever reach the milpac API.
func lookupWardenInternalUnit(value string) (wardenInternalUnit, bool) {
	for _, unit := range wardenInternalUnits {
		if unit.value == value {
			return unit, true
		}
	}
	return wardenInternalUnit{}, false
}

// wardenInternalUnitChoices builds the dropdown choices from the registry, so a
// new registry row automatically becomes a new picker option with no edit here.
func wardenInternalUnitChoices() []*discordgo.ApplicationCommandOptionChoice {
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(wardenInternalUnits))
	for _, unit := range wardenInternalUnits {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  unit.label,
			Value: unit.value,
		})
	}
	return choices
}

func WardenBulkAddInternal() Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        "warden-bulkadd-internal",
			Description: "Add a validated unit's roster to Verified Warden Internal",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "unit",
					Description: "Validated unit whose roster is added to Verified Warden Internal",
					Required:    true,
					Choices:     wardenInternalUnitChoices(),
				},
			},
		},
		Handler: handleWardenBulkAddInternal,
	}
}

func handleWardenBulkAddInternal(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	runWardenBulkAddInternal(utils.NewSessionResponder(session), NewSessionGuildManager(session), interaction)
}

func runWardenBulkAddInternal(
	r utils.InteractionResponder,
	gm GuildManager,
	interaction *discordgo.InteractionCreate,
) {
	// Guild-context guard first: warden commands require guild context. Rejecting
	// on an empty GuildID here gives a clear server-only message and guarantees a
	// non-empty guildID for every downstream Discord role call.
	guildID := interaction.GuildID
	if guildID == "" {
		utils.HandleError(r, interaction, "❌ This command can only be used in a server (guild).")
		return
	}

	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting Warden Bulk Add Internal", "command", "warden-bulkadd-internal", "username", username, "discord_id", discordID)

	commandData := interaction.ApplicationCommandData()
	unitValue, ok := getOptionString(commandData, "unit")
	if !ok {
		utils.HandleError(r, interaction, "❌ Missing unit argument.")
		return
	}
	// The picker only ever emits a registered value, but validate against the
	// registry anyway: it is the safety boundary, and a crafted interaction must
	// not be able to express a query the registry never authorized.
	unit, ok := lookupWardenInternalUnit(unitValue)
	if !ok {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Unknown unit %q; pick one from the list.", unitValue))
		return
	}

	if err := deferEphemeral(r, interaction); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to acknowledge: %v", err))
		return
	}

	roleName := resolveWardenRoleNames("internal")[0]
	roleID, err := findGuildRoleIDByName(gm, guildID, roleName)
	if errors.Is(err, errRoleNotFound) {
		editEphemeral(r, interaction, fmt.Sprintf("❌ '%s' role not found in guild", roleName))
		return
	}
	if err != nil {
		// A genuine GuildRoles fault (5xx/transport) captures to Sentry; a 4xx
		// stays a non-captured actionable message. The raw Discord body is never
		// interpolated into the reply.
		editEphemeral(r, interaction, roleResolveErrorReply(
			err,
			"Failed to retrieve guild roles for warden internal bulk add",
			"command", "warden-bulkadd-internal", "guild", guildID, "role", roleName,
		).Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), wardenInternalRosterTimeout)
	defer cancel()

	roster, err := utils.GetRosterByFuzzyPositionSearch(ctx, unit.query)
	if err != nil {
		// A roster fetch failure on fixed input is a genuine milpac fault, not a
		// user-input miss: capture it (tagged with the unit value) and surface a
		// retry message.
		captureError(
			"Failed to fetch warden internal unit roster",
			err,
			"command", "warden-bulkadd-internal", "guild", guildID, "unit", unit.value,
		)
		editEphemeral(r, interaction, fmt.Sprintf(
			"❌ Failed to fetch the %s roster (milpac error); please try again shortly.",
			unit.label,
		))
		return
	}

	// Empty vs failure, per ADR 0002. The picker -> registry -> fixed query makes
	// this fixed input, so an empty result is structurally a bug, not "the unit is
	// empty". Change nothing, report it (tagged with the unit value so each
	// registry entry fingerprints separately), and tell the operator it shouldn't
	// have happened — never a silent "added 0".
	if len(roster.LiteProfiles) == 0 {
		captureError(
			"Warden internal bulk add roster lookup returned zero members",
			fmt.Errorf("empty roster for unit %q", unit.value),
			"command", "warden-bulkadd-internal", "guild", guildID, "unit", unit.value,
		)
		editEphemeral(r, interaction, fmt.Sprintf(
			"⚠️ The %s roster came back empty. That shouldn't happen for a validated unit, so nothing was changed and the issue has been reported.",
			unit.label,
		))
		return
	}

	var added []*discordgo.Member
	var notInDiscord []string
	var noDiscordLinked []string
	var faults []string
	var sawMissingPermissions bool
	// One collector per run collapses the per-member system-fault captures: a role
	// deleted mid-run 404s every add with the same signature, which would
	// otherwise page on-call once per member. Faults feed it during the loop and
	// it flushes once after (#214). The flush is deferred immediately, so an early
	// return or a panic between the loop and the flush can never silently drop
	// pending captures (the exact per-member silent drop this collector fixes); the
	// message and the unit tag are fixed for the run, so evaluating the deferred
	// args here is exact.
	faultCapture := newFaultCollector()
	defer faultCapture.flush(
		"Failed to add warden internal role in bulk",
		"command", "warden-bulkadd-internal", "guild", guildID, "unit", unit.value,
	)
	for _, profile := range roster.LiteProfiles {
		memberDiscordID := strings.TrimSpace(profile.DiscordID)
		if memberDiscordID == "" {
			// No Discord connection on the milpac: nothing to add, and a mention
			// would render as a dead raw ID. List by forum username instead.
			noDiscordLinked = append(noDiscordLinked, profile.User.Username)
			continue
		}

		err := gm.GuildMemberRoleAdd(guildID, memberDiscordID, roleID)
		if err == nil {
			// Idempotent PUT: a fresh add and a re-add of an existing member both
			// land here, so this is "added or confirmed".
			added = append(added, &discordgo.Member{
				User: &discordgo.User{ID: memberDiscordID, Username: profile.User.Username},
			})
			continue
		}

		class := classifyDiscordError(err)
		if class.NotFound {
			// 404 Unknown Member: the trooper has a linked Discord but isn't in this
			// server. List by forum username (a mention would be a dead raw ID); not
			// a fault, so no capture, and the run continues.
			notInDiscord = append(notInDiscord, profile.User.Username)
			continue
		}
		if class.SystemFault {
			// Genuine fault (5xx/transport, or a stale-role/guild config 404): hand it
			// to the collector keyed by signature instead of capturing per member, so
			// a role deleted mid-run pages once rather than once per trooper. List the
			// member and continue past it. The raw Discord body never reaches the reply.
			faultCapture.recordSystemFault(err, memberDiscordID)
			faults = append(faults, profile.User.Username)
			continue
		}

		// A non-404 client fault (e.g. 403 missing Manage Roles, or the role above
		// the bot) is operator-fixable: surface it so it is never silently dropped,
		// but do not capture (ADR 0001). Typically this hits every member at once,
		// which is itself the signal the bot's permissions need fixing. Remember a
		// 403 so the summary can add an actionable hint instead of leaving the
		// operator with only an opaque "Could not be added" list.
		if class.MissingPermissions {
			sawMissingPermissions = true
		}
		faults = append(faults, profile.User.Username)
	}

	slices.SortFunc(added, func(a, b *discordgo.Member) int {
		return strings.Compare(a.User.Username, b.User.Username)
	})
	slices.Sort(notInDiscord)
	slices.Sort(noDiscordLinked)
	slices.Sort(faults)

	content := buildWardenInternalBulkAddSummary(unit.label, roleName, len(added), notInDiscord, noDiscordLinked, faults, sawMissingPermissions)
	var embed *discordgo.MessageEmbed
	if len(added) > 0 {
		embed = buildAddedMembersEmbed(added)
	}
	editEphemeralWithEmbed(r, interaction, content, embed)

	utils.Info("✨ Done!", "command", "warden-bulkadd-internal", "unit", unit.value, "added", len(added))
}

// buildWardenInternalBulkAddSummary composes the ephemeral report. The
// added-or-confirmed count always leads (the command never silently reports
// nothing); the not-in-Discord, no-Discord-linked, and could-not-be-added
// buckets are listed by forum username only when non-empty. When any fault was a
// 403, a permissions hint trails the buckets so a misconfigured bot reads as an
// actionable fix rather than an opaque list of failures.
func buildWardenInternalBulkAddSummary(
	unitLabel, roleName string,
	addedCount int,
	notInDiscord, noDiscordLinked, faults []string,
	missingPermissions bool,
) string {
	sections := []string{
		fmt.Sprintf("✅ Added or confirmed %d %s member(s) in %s.", addedCount, unitLabel, roleName),
	}
	if section := formatWardenInternalSection("Not in this Discord", notInDiscord); section != "" {
		sections = append(sections, section)
	}
	if section := formatWardenInternalSection("No Discord linked", noDiscordLinked); section != "" {
		sections = append(sections, section)
	}
	if section := formatWardenInternalSection("Could not be added", faults); section != "" {
		sections = append(sections, section)
	}
	if missingPermissions {
		sections = append(sections, wardenInternalPermissionsHint(roleName))
	}
	return clampToDiscordMessageLimit(strings.Join(sections, "\n\n"))
}

// wardenInternalPermissionsHint is the actionable line appended when a per-member
// add failed on a 403. It reuses the substance of roleMutationErrorMessage's
// missing-permissions branch (Manage Roles plus the role-hierarchy requirement)
// without interpolating any raw Discord body.
func wardenInternalPermissionsHint(roleName string) string {
	return fmt.Sprintf(
		"⚠️ Some members couldn't be added because the bot is missing permissions. It needs Manage Roles, and its own role must sit above '%s'.",
		roleName,
	)
}

// wardenInternalSummaryMaxLen is Discord's per-message limit. The success count
// is collapsed into one line and the added members ride in the embed, so the
// content only grows with the by-username buckets; for a curated company-sized
// unit this stays well under the limit, but clamp anyway so a pathologically
// large bucket can never make the edit itself fail. clampToDiscordMessageLimit
// measures bytes (len), not runes: that is deliberate, since byte length >= rune
// count it is a safe over-estimate of Discord's UTF-8 code-point limit, so don't
// "fix" it into a rune count and weaken the margin.
const wardenInternalSummaryMaxLen = 2000

// clampToDiscordMessageLimit keeps as many whole lines as fit under the limit,
// then appends a truncation marker. The lead summary line is short and comes
// first, so it always survives.
func clampToDiscordMessageLimit(message string) string {
	if len(message) <= wardenInternalSummaryMaxLen {
		return message
	}
	const marker = "... (truncated)"
	lines := strings.Split(message, "\n")
	var kept []string
	used := 0
	for _, line := range lines {
		addition := len(line) + 1 // +1 for the newline join
		if used+addition+len(marker)+1 > wardenInternalSummaryMaxLen {
			break
		}
		kept = append(kept, line)
		used += addition
	}
	kept = append(kept, marker)
	return strings.Join(kept, "\n")
}

// formatWardenInternalSection renders a labelled, count-headed list of forum
// usernames, or "" when the bucket is empty. The slices.Sort calls at the call
// site make the within-bucket username order deterministic; the header count is
// what keeps the bucket *size* stable in a test even when which member lands in
// the bucket is map-order-dependent.
func formatWardenInternalSection(title string, usernames []string) string {
	if len(usernames) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%d):", title, len(usernames))
	for _, username := range usernames {
		fmt.Fprintf(&sb, "\n- %s", username)
	}
	return sb.String()
}
