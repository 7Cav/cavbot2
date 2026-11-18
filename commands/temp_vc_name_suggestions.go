package commands

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Name suggestions (#959): an ownership notice with an owner, on a hub with
// renaming on, carries a button for each channel name built from the owner's
// milpac billets. A press renames the channel under /voice-rename's rule. The
// name rides in the button's CustomID, <voice-rename>::name::<channel ID>::<name>
// (ADR 0007), so a press needs no stored state and works after a restart.

// renameSuggestionAction is the middle segment of a suggestion's CustomID.
const renameSuggestionAction = "name"

// maxNameSuggestions is one row of buttons.
const maxNameSuggestions = 5

// milpacLookupTimeout bounds the billet lookup a notice waits on.
const milpacLookupTimeout = 5 * time.Second

// unitPath matches a billet's unit: "A/3-7", "1/1/A/3-7", "1/2/D/DEVCOM", or
// a bare battalion "3-7". A bare letter word ("MP", "IT") is no unit.
var unitPath = regexp.MustCompile(`^(?:[0-9A-Z]+/)+(?:\d+-\d+|[A-Z]+)$|^\d+-\d+$`)

// staffWord matches the department word a staff billet starts with: "S6",
// "RRD", "NCOA".
var staffWord = regexp.MustCompile(`^[A-Z][A-Z0-9]+$`)

// The billet roles, the title with its unit taken out, that add a name to
// the unit's lounges.
var (
	hqRoles = []string{"Commander", "Commanding Officer", "Executive Officer", "First Sergeant", "Sergeant Major"}
	spRoles = []string{"Section Leader", "Assistant Section Leader"}
)

// suggestChannelNames builds the names offered for billets, the owner's
// position titles. scope is the hub's base string and category name; names
// related to it come first, then the rest, each list in billet order, with
// no name twice and at most maxNameSuggestions.
func suggestChannelNames(billets []string, scope string) []string {
	scope = strings.ToLower(scope)
	scopeWords := strings.FieldsFunc(scope, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	type suggestion struct {
		name    string
		related bool
	}
	var all []suggestion
	add := func(name string, related bool) {
		i := slices.IndexFunc(all, func(s suggestion) bool { return s.name == name })
		if i < 0 {
			all = append(all, suggestion{name, related})
			return
		}
		all[i].related = all[i].related || related
	}

	for _, title := range billets {
		words := strings.Fields(title)
		u := slices.IndexFunc(words, unitPath.MatchString)
		if u < 0 {
			if len(words) > 0 && staffWord.MatchString(words[0]) {
				dept := words[0]
				add(dept+" Lounge", slices.Contains(scopeWords, strings.ToLower(dept)) ||
					slices.ContainsFunc(scopeWords, func(w string) bool { return strings.HasPrefix(w, "department") }))
			}
			continue
		}
		unit := words[u]
		role := strings.Join(slices.Delete(slices.Clone(words), u, u+1), " ")

		// The unit's levels, battalion first: 1/1/A/3-7 is 3-7, A/3-7,
		// 1/A/3-7, 1/1/A/3-7. The billet is related when any is in scope.
		segments := strings.Split(unit, "/")
		levels := make([]string, 0, len(segments))
		for i := len(segments) - 1; i >= 0; i-- {
			levels = append(levels, strings.Join(segments[i:], "/"))
		}
		related := slices.ContainsFunc(levels, func(l string) bool { return strings.Contains(scope, strings.ToLower(l)) })

		for _, l := range levels {
			add(l+" Lounge", related)
		}
		switch {
		case slices.Contains(hqRoles, role):
			add(knockChannelPrefix+unit+" HQ", related || slices.Contains(scopeWords, "hq"))
		case slices.Contains(spRoles, role):
			add(unit+" SP", related)
		}
	}

	slices.SortStableFunc(all, func(a, b suggestion) int {
		return cmp.Compare(boolRank(b.related), boolRank(a.related))
	})
	names := make([]string, 0, min(len(all), maxNameSuggestions))
	for _, s := range all[:min(len(all), maxNameSuggestions)] {
		names = append(names, s.name)
	}
	return names
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// renameSuggestionCustomID assembles a suggestion button's CustomID, false
// when it would pass Discord's cap.
func renameSuggestionCustomID(channelID, name string) (string, bool) {
	id := strings.Join([]string{voiceRenameCommandName, renameSuggestionAction, channelID, name}, customIDSeparator)
	return id, len(id) <= discordCustomIDLimit
}

// parseRenameSuggestionCustomID reads the channel and name back out of a
// suggestion's CustomID. ok is false for any CustomID
// renameSuggestionCustomID could not have built.
func parseRenameSuggestionCustomID(customID string) (channelID, name string, ok bool) {
	parts := strings.Split(customID, customIDSeparator)
	if len(parts) != 4 || parts[0] != voiceRenameCommandName || parts[1] != renameSuggestionAction || parts[2] == "" || parts[3] == "" {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// nameSuggestionButtons builds the notice's row of suggestions for the
// owner, or nil when there is nothing to offer: no lookup configured, a
// failed lookup, or no name from the owner's billets. A failed lookup other
// than no milpac is a WARN line, as the notice still posts.
func (t *TempVC) nameSuggestionButtons(channelID, owner, baseString string) []discordgo.MessageComponent {
	if t.milpacByDiscordID == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), milpacLookupTimeout)
	defer cancel()
	profile, err := t.milpacByDiscordID(ctx, owner)
	if err != nil {
		if !errors.Is(err, utils.ErrNotFound) {
			utils.Warn("Temp VC name suggestions skipped, milpac lookup failed",
				"channel_id", channelID, "owner_id", owner, "error", err)
		}
		return nil
	}

	billets := []string{profile.Primary.PositionTitle}
	for _, p := range profile.Secondary {
		billets = append(billets, p.PositionTitle)
	}
	// The category name widens the scope: "▬ A/ACD-7 ▬" where the base
	// string is only "Voice". A cache miss leaves the base string alone.
	scope := baseString
	if ch, err := t.mgr.Channel(channelID); err == nil && ch.ParentID != "" {
		if category, err := t.mgr.Channel(ch.ParentID); err == nil {
			scope += " " + category.Name
		}
	}

	var row []discordgo.MessageComponent
	for _, name := range suggestChannelNames(billets, scope) {
		if id, ok := renameSuggestionCustomID(channelID, name); ok {
			row = append(row, discordgo.Button{Label: name, Style: discordgo.PrimaryButton, CustomID: id})
		}
	}
	if len(row) == 0 {
		return nil
	}
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: row}}
}

// runRenameSuggestion answers a press on a suggestion button: the rename
// /voice-rename would make from inside the channel, on the channel the
// button names, answered in a new ephemeral message.
func runRenameSuggestion(r utils.InteractionResponder, tv *TempVC, interaction *discordgo.InteractionCreate) {
	customID := interaction.MessageComponentData().CustomID
	if err := deferEphemeral(r, interaction); err != nil {
		captureAckFailure("Failed to acknowledge a name suggestion button", err,
			"command", voiceRenameCommandName, "custom_id", customID, "guild_id", interaction.GuildID)
		return
	}

	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting RenameSuggestion", "command", voiceRenameCommandName,
		"username", username, "discord_id", discordID, "custom_id", customID)

	if tv == nil {
		editNoticeReply(r, interaction, "❌ Temporary voice channels are not enabled on this bot.")
		return
	}
	channelID, name, ok := parseRenameSuggestionCustomID(customID)
	if !ok {
		// A button from an older build whose CustomID is gone.
		utils.Warn("Name suggestion button not recognised", "custom_id", customID, "discord_id", discordID)
		editNoticeReply(r, interaction, "❌ This button no longer does anything. Use /"+voiceRenameCommandName+" instead.")
		return
	}

	by := Invoker{UserID: discordID, Username: username, Roles: interactionRoles(interaction)}
	result, err := tv.renameFromNotice(by, channelID, name)
	if err != nil {
		editNoticeReply(r, interaction, renameRefusal(err, discordID))
		return
	}
	utils.Info("Temp VC renamed",
		"command", voiceRenameCommandName, "username", username, "discord_id", discordID,
		"channel_id", result.ChannelID, "before", result.Before, "after", result.After)
	editNoticeReply(r, interaction, renamedReply(result.After))
	utils.Info("✨ Done!", "command", voiceRenameCommandName)
}
