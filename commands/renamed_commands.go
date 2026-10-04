package commands

import (
	"fmt"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// renameCutoff is the date the old names stop working: the rename's expected
// release date plus 30 days. Bump it if that release slips. The bot never
// enforces it; the cleanup release in #455 deletes the old names on or after
// it. Noon UTC keeps the date a manager's client shows for the reply notice's
// timestamp the same as the descriptions' plain-text date from UTC-11 to
// UTC+11.
var renameCutoff = time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)

// renamedCommands lists each command still registered under the name it had
// before a rename, next to the name that replaced it. An old name runs its new
// name's handler, so it keeps its command ID and the Integrations overrides
// Discord keys by that ID. The cleanup in #455 deletes the old names.
var renamedCommands = []struct{ oldName, newName string }{
	{oldName: "warden", newName: "foxhole"},
	{oldName: "warden-bulkadd-internal", newName: "foxhole-bulkadd-internal"},
}

// oldNameCommands returns a copy of each renamed command in cmds, registered
// under its old name with the same options and handler. cmds must hold the
// handlers as declared, before RegisterCommands instruments them, so a run
// under an old name is instrumented once, under that name.
func oldNameCommands(cmds []Command) []Command {
	var out []Command
	for _, renamed := range renamedCommands {
		for _, cmd := range cmds {
			if cmd.Definition.Name != renamed.newName {
				continue
			}
			definition := *cmd.Definition
			definition.Name = renamed.oldName
			// Plain text: Discord renders no markup in a description.
			definition.Description = fmt.Sprintf("Renamed to /%s. Stops working on %s.",
				renamed.newName, renameCutoff.Format("2 January 2006"))
			out = append(out, Command{Definition: &definition, Handler: cmd.Handler})
		}
	}
	return out
}

// renameNotice returns the line every reply under an old name ends with, or ""
// when interaction ran under any other name. Its timestamp markup shows the
// cutoff in each manager's own locale and time zone.
func renameNotice(interaction *discordgo.InteractionCreate) string {
	name := commandNameOf(interaction)
	for _, renamed := range renamedCommands {
		if renamed.oldName == name {
			return fmt.Sprintf("`/%s` is now `/%s` and stops working on <t:%d:D>.",
				renamed.oldName, renamed.newName, renameCutoff.Unix())
		}
	}
	return ""
}

// commandNameOf returns the registered name a slash command interaction ran
// under, or "" for any other interaction.
func commandNameOf(interaction *discordgo.InteractionCreate) string {
	if interaction == nil || interaction.Interaction == nil || interaction.Type != discordgo.InteractionApplicationCommand {
		return ""
	}
	return interaction.ApplicationCommandData().Name
}

// appendRenameNotice ends content with notice, or returns content unchanged
// when notice is empty. Content that would push the reply past Discord's
// message limit is trimmed first, so the reply still arrives.
func appendRenameNotice(content, notice string) string {
	if notice == "" {
		return content
	}
	suffix := "\n\n" + notice
	return clampToLimit(content, discordMessageLimit-len(suffix)) + suffix
}

// withRenameNotice returns r, wrapped so every reply it sends ends with the
// rename notice when interaction ran under an old name.
func withRenameNotice(r utils.InteractionResponder, interaction *discordgo.InteractionCreate) utils.InteractionResponder {
	notice := renameNotice(interaction)
	if notice == "" {
		return r
	}
	return renameNoticeResponder{InteractionResponder: r, notice: notice}
}

// renameNoticeResponder appends the rename notice to the content of every
// message reply and edit. A deferral carries no content, so it passes
// through unchanged. Foxhole commands send no followups.
type renameNoticeResponder struct {
	utils.InteractionResponder
	notice string
}

func (r renameNoticeResponder) InteractionRespond(i *discordgo.Interaction, resp *discordgo.InteractionResponse) error {
	if resp != nil && resp.Data != nil && resp.Data.Content != "" {
		data := *resp.Data
		data.Content = appendRenameNotice(data.Content, r.notice)
		resp = &discordgo.InteractionResponse{Type: resp.Type, Data: &data}
	}
	return r.InteractionResponder.InteractionRespond(i, resp)
}

func (r renameNoticeResponder) InteractionResponseEdit(i *discordgo.Interaction, edit *discordgo.WebhookEdit) error {
	if edit != nil && edit.Content != nil {
		withNotice := *edit
		content := appendRenameNotice(*edit.Content, r.notice)
		withNotice.Content = &content
		edit = &withNotice
	}
	return r.InteractionResponder.InteractionResponseEdit(i, edit)
}
