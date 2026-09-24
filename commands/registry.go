package commands

import "github.com/bwmarrin/discordgo"

type Registry struct {
	commands []Command
}

// NewRegistry declares every command (ADR 0006). tempVC is the temporary
// voice channel runtime, nil on a host with no bot store. /voice-rename,
// /voice-lock and /voice-unlock are declared either way: the startup sync
// deletes any guild command the registry lacks, and Discord keys command
// permissions by command ID, so a command that came and went with
// configuration would shed its Server Settings restriction on every
// store-less start. With no runtime each handler refuses.
func NewRegistry(tempVC *TempVC) *Registry {
	r := &Registry{}
	r.RegisterCommands(
		Milpac(),
		Warden(),
		Enlist(),
		WardenBulkAddInternal(),
		Zulu(),
		S6ITCheck(),
		Awol(),
		LOA(),
		AFSM(),
		GamertagSearch(),
		S3AAR(),
		Helpline(),
		VoiceRename(tempVC),
		VoiceLock(tempVC),
		VoiceUnlock(tempVC),
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
