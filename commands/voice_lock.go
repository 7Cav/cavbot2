package commands

import (
	"errors"
	"fmt"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// /voice-lock and /voice-unlock (spec #347, #349): the owner of a spawned
// channel, or a member holding a moderator role of its hub, locks or unlocks
// the channel they are sitting in. No options. Every reply is ephemeral,
// through the deferred-ephemeral pattern /voice-rename uses.
//
// Neither command sets default member permissions. On the live guild every
// command starts denied to everyone, and the roles Nex enables in Server
// Settings decide who may run them (docs/temp-vc-decisions.md, #347 Q24).

// The registered slash-command names, the values the telemetry line and
// every log line carry under "command".
const (
	voiceLockCommandName   = "voice-lock"
	voiceUnlockCommandName = "voice-unlock"
)

// lockCommand is what tells /voice-lock and /voice-unlock apart in the
// handler they share.
type lockCommand struct {
	// action names the command and what it does, for the log lines and the
	// refusals.
	action voiceAction
	// logName is the name the "🚀 Starting" line carries.
	logName string
	// run is the runtime call.
	run func(tv *TempVC, by Invoker) (lockResult, error)
	// done is the invoker's reply on success.
	done string
}

var (
	voiceLock = lockCommand{
		action:  voiceAction{command: voiceLockCommandName, verb: "lock", past: "locked"},
		logName: "VoiceLock",
		run:     (*TempVC).Lock,
		done:    "🔒 Locked. The people inside can leave and come back, and the hub's moderators can join. Nobody else can.",
	}
	voiceUnlock = lockCommand{
		action:  voiceAction{command: voiceUnlockCommandName, verb: "unlock", past: "unlocked"},
		logName: "VoiceUnlock",
		run:     (*TempVC).Unlock,
		done:    "🔓 Unlocked. The channel has its hub's permissions again.",
	}
)

// VoiceLock declares /voice-lock over a runtime, nil on a host with no bot
// store (see NewRegistry for why the command exists anyway).
func VoiceLock(tv *TempVC) Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        voiceLockCommandName,
			Description: "Lock the voice channel you are in to the people inside it",
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			runVoiceLock(utils.NewSessionResponder(s), tv, i)
		},
	}
}

// VoiceUnlock declares /voice-unlock over a runtime, nil on a host with no
// bot store.
func VoiceUnlock(tv *TempVC) Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        voiceUnlockCommandName,
			Description: "Unlock the voice channel you are in",
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			runVoiceUnlock(utils.NewSessionResponder(s), tv, i)
		},
	}
}

// runVoiceLock is the /voice-lock handler behind the responder seam.
func runVoiceLock(r utils.InteractionResponder, tv *TempVC, interaction *discordgo.InteractionCreate) {
	runLockCommand(r, tv, interaction, voiceLock)
}

// runVoiceUnlock is the /voice-unlock handler behind the responder seam.
func runVoiceUnlock(r utils.InteractionResponder, tv *TempVC, interaction *discordgo.InteractionCreate) {
	runLockCommand(r, tv, interaction, voiceUnlock)
}

// runLockCommand defers an ephemeral reply first, so every outcome below is
// an edit of that one reply and nothing the invoker sees is public. The
// runtime logs the lock or unlock itself, naming the invoker, the channel
// and the hub. A press on a lock notice button reaches the same handler
// (ADR 0007) and goes to runLockNoticeComponent before anything below
// reads it as a slash command.
func runLockCommand(r utils.InteractionResponder, tv *TempVC, interaction *discordgo.InteractionCreate, c lockCommand) {
	if interaction.Type == discordgo.InteractionMessageComponent {
		runLockNoticeComponent(r, tv, interaction)
		return
	}
	if err := deferEphemeral(r, interaction); err != nil {
		utils.HandleError(r, interaction, fmt.Sprintf("❌ Failed to acknowledge: %v", err))
		return
	}

	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting "+c.logName, "command", c.action.command, "username", username, "discord_id", discordID)

	if tv == nil {
		utils.Warn("Temp VC "+c.action.verb+" refused, no bot store configured",
			"command", c.action.command, "discord_id", discordID)
		editEphemeral(r, interaction, "❌ Temporary voice channels are not enabled on this bot.")
		return
	}

	res, err := c.run(tv, Invoker{UserID: discordID, Roles: interactionRoles(interaction)})
	if err != nil {
		editEphemeral(r, interaction, lockRefusal(err, c, discordID))
		return
	}
	reply := c.done
	if res.NoticeFailed {
		reply += lockNoticeNotPosted
	}
	editEphemeral(r, interaction, reply)
	utils.Info("✨ Done!", "command", c.action.command)
}

// notLockedRefusal answers /voice-unlock and the notice's buttons on a
// channel that is not locked (#347 story 25).
const notLockedRefusal = "❌ This channel isn't locked."

// lockRefusal renders a failed Lock or Unlock as the invoker's ephemeral
// reply, the refusals every voice command shares first. No reply carries a
// raw Discord body.
func lockRefusal(err error, c lockCommand, discordID string) string {
	if reply, ok := voiceChannelRefusal(err, c.action, discordID); ok {
		return reply
	}
	switch {
	case errors.Is(err, errLockingNotAllowed):
		return "❌ Locking isn't turned on for this hub."
	case errors.Is(err, errAlreadyLocked):
		return "❌ Already locked."
	case errors.Is(err, errNotLocked):
		return notLockedRefusal
	case errors.Is(err, errSourceUnreadable):
		return "❌ Couldn't unlock the channel: its hub's permissions can't be read, so it stays locked until everyone leaves."
	case errors.Is(err, errChannelNotCached):
		// The cache drops a deleted channel before the runtime hears of the
		// delete, so a tracked channel it lacks is one on its way out.
		return channelGoneRefusal
	case classifySpawnedChannelError(err).rateLimited:
		return fmt.Sprintf("❌ Couldn't %s the channel: Discord is rate limiting changes to it. Try again in a few minutes.", c.action.verb)
	default:
		// The warden classifier's phrase, never the raw body. Unknown Channel
		// never reaches it: the runtime classifies that code first.
		return fmt.Sprintf("❌ Couldn't %s the channel: %s.", c.action.verb, classifyDiscordError(err).UserDetail)
	}
}
