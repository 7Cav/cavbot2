package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

type Registry struct {
	commands []Command
}

// NewRegistry declares every command (ADR 0006). tempVC is the temporary
// voice channel runtime and foxhole the Foxhole runtime, each nil on a host
// with no bot store. /voice-rename,
// /voice-lock and /voice-unlock are declared either way: the startup sync
// deletes any guild command the registry lacks, and Discord keys command
// permissions by command ID, so a command that came and went with
// configuration would shed its Server Settings restriction on every
// store-less start. With no runtime each handler refuses. With no Foxhole
// runtime the Foxhole commands refuse nothing: there is no Foxhole page to
// share the roles with.
func NewRegistry(tempVC *TempVC, foxhole *FoxholeRuntime) *Registry {
	r := &Registry{}
	r.RegisterCommands(
		Milpac(),
		Foxhole(foxhole),
		Warden(foxhole),
		Enlist(),
		FoxholeBulkAddInternal(foxhole),
		WardenBulkAddInternal(foxhole),
		Zulu(),
		S6ITCheck(),
		Awol(),
		LOA(),
		AFSM(),
		GamertagSearch(),
		S3AARDisabled(),
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

// guildCommandAPI is the Discord REST surface the startup sync uses.
// *discordgo.Session satisfies it.
type guildCommandAPI interface {
	ApplicationCommands(appID, guildID string, options ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error)
	ApplicationCommandBulkOverwrite(appID string, guildID string, commands []*discordgo.ApplicationCommand, options ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error)
}

// errCommandSyncPanicked is what Sync returns when its Discord calls panic.
// RecoverPanic has already logged the panic and sent it to Sentry.
var errCommandSyncPanicked = errors.New("command sync panicked")

// Sync makes the guild's commands the registry's (ADR 0006) in one bulk
// overwrite. Discord keeps the ID of each command whose name it already
// holds, even when the definition changed, so the permission overrides it
// keys by that ID survive. It deletes the commands the registry no longer
// declares, and the list beforehand only names them in the log.
//
// One request replaces the create-each loop that Discord paced to about a
// minute (#470). The overwrite has its own rate limit. After two in quick
// succession, Discord holds the next for up to about a minute, so a start
// seconds after two others waits. Sync returns ctx's error as soon as ctx
// ends, so a stop is never held by that wait. The request it leaves behind
// ends with the process.
func (r *Registry) Sync(ctx context.Context, api guildCommandAPI, appID, guildID string) error {
	done := make(chan error, 1)
	go func() {
		err := errCommandSyncPanicked
		defer func() { done <- err }()
		defer utils.RecoverPanic("command-sync")
		err = r.overwrite(api, appID, guildID)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Registry) overwrite(api guildCommandAPI, appID, guildID string) error {
	existing, err := api.ApplicationCommands(appID, guildID)
	if err != nil {
		utils.Warn("Could not list the guild's commands, so removed ones go unnamed", "error", err)
	}
	if _, err := api.ApplicationCommandBulkOverwrite(appID, guildID, r.GetCommands()); err != nil {
		return fmt.Errorf("overwrite the guild's commands: %w", err)
	}
	for _, cmd := range existing {
		if _, declared := r.GetHandler(cmd.Name); !declared {
			utils.Info("Removed deprecated command", "command", cmd.Name)
		}
	}
	return nil
}

func (r *Registry) GetHandler(name string) (CommandHandler, bool) {
	for _, cmd := range r.commands {
		if cmd.Definition.Name == name {
			return cmd.Handler, true
		}
	}
	return nil, false
}
