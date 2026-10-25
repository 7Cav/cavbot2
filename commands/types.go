package commands

import "github.com/bwmarrin/discordgo"

type CommandHandler func(s *discordgo.Session, i *discordgo.InteractionCreate)

type Command struct {
	Definition *discordgo.ApplicationCommand
	Handler    CommandHandler
	// Registered, when set, receives the ID Discord answers the startup
	// sync's overwrite with for this command.
	Registered func(id string)
}
