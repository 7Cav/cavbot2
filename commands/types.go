package commands

import "github.com/bwmarrin/discordgo"

type CommandHandler func(s *discordgo.Session, i *discordgo.InteractionCreate)

type Command struct {
	Definition *discordgo.ApplicationCommand
	Handler    CommandHandler
}
