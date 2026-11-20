package commands

import "github.com/bwmarrin/discordgo"

// optionsByName keys a command's top-level options by name. Discord sends only
// the options the member filled in, so reading by position breaks once a
// command has more than one optional option.
func optionsByName(i *discordgo.InteractionCreate) map[string]*discordgo.ApplicationCommandInteractionDataOption {
	options := i.ApplicationCommandData().Options
	byName := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, opt := range options {
		byName[opt.Name] = opt
	}
	return byName
}
