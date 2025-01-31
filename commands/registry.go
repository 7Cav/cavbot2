package commands

import "github.com/bwmarrin/discordgo"

type Registry struct {
	commands []Command
}

func NewRegistry() *Registry {
	r := &Registry{}
	r.RegisterCommands(
		Milpac(),
		AppsBetaDeploy(),
		Zulu(),
		S6Afsm(),
		S6ITCheck(),
	)
	return r
}

func (r *Registry) RegisterCommands(cmds ...Command) {
	r.commands = append(r.commands, cmds...)
}

func (r *Registry) GetCommands() []*discordgo.ApplicationCommand {
	cmds := make([]*discordgo.ApplicationCommand, len(r.commands))
	for i, cmd := range r.commands {
		cmds[i] = cmd.Definition
	}
	return cmds
}

func (r *Registry) GetHandler(name string) (CommandHandler, bool) {
	for _, cmd := range r.commands {
		if cmd.Definition.Name == name {
			return cmd.Handler, true
		}
	}
	return nil, false
}
