package commands

import "github.com/bwmarrin/discordgo"

type CommandHandler func(s *discordgo.Session, i *discordgo.InteractionCreate)

type Command struct {
	Definition *discordgo.ApplicationCommand
	Handler    CommandHandler
	// OnRegistered, when set, gets the ID Discord gave this command in the
	// startup sync's overwrite.
	OnRegistered func(id string)
}
