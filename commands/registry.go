package commands

import "github.com/bwmarrin/discordgo"

type Registry struct {
	commands []Command
}

func NewRegistry() *Registry {
	r := &Registry{}
	r.RegisterCommands(
		Milpac(),
		Warden(),
		WardenBulkAddInternal(),
		Zulu(),
		S6ITCheck(),
		Awol(),
		LOA(),
		AFSM(),
		GamertagSearch(),
		S3AAR(),
		Helpline(),
	)
	return r
}

// RegisterCommands decorates every handler with telemetry on the way in, so a
// new command is measured the moment it joins the registry and instrumentation
// stays in the one place commands are declared (ADR 0006).
func (r *Registry) RegisterCommands(cmds ...Command) {
	for _, cmd := range cmds {
		cmd.Handler = instrument(cmd.Definition.Name, cmd.Handler)
		r.commands = append(r.commands, cmd)
	}
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
