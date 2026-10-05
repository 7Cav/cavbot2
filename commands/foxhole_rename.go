package commands

import (
	"fmt"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// /warden and /warden-bulkadd-internal became /foxhole and
// /foxhole-bulkadd-internal. The old names stay registered on the same
// handlers until the cutoff, so their command IDs, and the Integrations
// overrides Discord keys by them, survive the release. The cleanup in #455
// deletes this file and the two old names from the registry.

// foxholeRenameCutoff is the date the old names stop working: the rename's
// expected release date plus 30 days. Bump it if that release slips. The bot
// never enforces it. Noon UTC keeps the date a manager's client shows for the
// note's timestamp the same as the descriptions' plain-text date from UTC-11
// to UTC+11.
var foxholeRenameCutoff = time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)

// Warden is /foxhole under its old name.
func Warden() Command {
	cmd := Foxhole()
	cmd.Definition.Name = "warden"
	cmd.Definition.Description = renamedDescription("foxhole")
	return cmd
}

// WardenBulkAddInternal is /foxhole-bulkadd-internal under its old name.
func WardenBulkAddInternal() Command {
	cmd := FoxholeBulkAddInternal()
	cmd.Definition.Name = "warden-bulkadd-internal"
	cmd.Definition.Description = renamedDescription("foxhole-bulkadd-internal")
	return cmd
}

// renamedDescription is an old name's description, in plain text, since
// Discord renders no markup in a description.
func renamedDescription(newName string) string {
	return fmt.Sprintf("Renamed to /%s. Stops working on %s.", newName, foxholeRenameCutoff.Format("2 January 2006"))
}

// sendRenameNote follows a run under oldName with a note only the invoker
// sees, naming newName and the cutoff in timestamp markup, so each manager's
// client shows it in their own time zone. A run under any other name gets
// none.
func sendRenameNote(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, oldName, newName string) {
	if commandNameOf(interaction) != oldName {
		return
	}
	note := fmt.Sprintf("`/%s` is now `/%s` and stops working on <t:%d:D>.", oldName, newName, foxholeRenameCutoff.Unix())
	if err := r.FollowupMessageCreate(interaction.Interaction, false, &discordgo.WebhookParams{
		Content: note,
		Flags:   discordgo.MessageFlagsEphemeral,
	}); err != nil {
		utils.Warn("Failed to send the rename note", "command", oldName, "error", err)
	}
}
