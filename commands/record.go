package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// /record (spec #381, #386): /record start, with an optional title, makes a
// free recorder join the voice or stage channel the member is in, and
// /record stop makes it leave. Every reply is ephemeral, through the
// deferred-ephemeral pattern the voice commands use.
//
// The command sets no default member permissions. On the live guild every
// command starts denied to everyone, and Server Settings decide who may run
// it. It is declared whether recording is on or off (see NewRegistry).

// recordCommandName is the registered slash-command name.
const recordCommandName = "record"

// Record declares /record over the recording runtime, nil on a host with no
// bot store.
func Record(rt *RecordingRuntime) Command {
	return Command{
		Definition: &discordgo.ApplicationCommand{
			Name:        recordCommandName,
			Description: "Record the voice channel you are in",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "start",
					Description: "Start recording the voice channel you are in",
					Options: []*discordgo.ApplicationCommandOption{{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "title",
						Description: "A title to find the recording by",
					}},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "stop",
					Description: "Stop the recording of the voice channel you are in",
				},
			},
		},
		Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			runRecord(utils.NewSessionResponder(s), rt, i)
		},
	}
}

// runRecord is the /record handler behind the responder seam. It defers an
// ephemeral reply first, so every outcome is an edit of that one reply.
func runRecord(r utils.InteractionResponder, rt *RecordingRuntime, interaction *discordgo.InteractionCreate) {
	if err := deferEphemeral(r, interaction); err != nil {
		replyAckFailed(r, interaction, err)
		return
	}
	username, discordID := interactionUsernameAndID(interaction)
	utils.Info("🚀 Starting Record", "command", recordCommandName, "username", username, "discord_id", discordID)

	if rt == nil {
		utils.Warn("Record refused, no bot store configured", "command", recordCommandName, "discord_id", discordID)
		refuse(r, interaction, recordingOffRefusal)
		return
	}

	by := Invoker{UserID: discordID, Username: username, Roles: interactionRoles(interaction)}
	sub := interaction.ApplicationCommandData().Options[0]
	switch sub.Name {
	case "start":
		title := ""
		for _, o := range sub.Options {
			if o.Name == "title" {
				title = o.StringValue()
			}
		}
		channelID, err := rt.Start(by, title)
		if err != nil {
			reply, refused := startRefusal(err)
			replyRecordError(r, interaction, reply, refused)
			return
		}
		editEphemeral(r, interaction, fmt.Sprintf("✅ Recording <#%s>.", channelID))
	case "stop":
		channelID, err := rt.Stop(by)
		if err != nil {
			reply, refused := stopRefusal(err)
			replyRecordError(r, interaction, reply, refused)
			return
		}
		editEphemeral(r, interaction, fmt.Sprintf("✅ Stopped recording <#%s>.", channelID))
	}
	utils.Info("✨ Done!", "command", recordCommandName)
}

// recordFailedReply answers a start or stop that failed after its checks
// passed. The runtime has reported the cause.
const recordFailedReply = "❌ Something went wrong with the recorder. The error has been reported."

// replyRecordError sends a run's refusal when it was refused, and the
// failed reply when it failed after its checks.
func replyRecordError(r utils.InteractionResponder, interaction *discordgo.InteractionCreate, refusal string, refused bool) {
	if refused {
		refuse(r, interaction, refusal)
		return
	}
	replyError(r, interaction, recordFailedReply)
}

// The refusals of /record, one per reason.
const (
	// recordingOffRefusal answers /record on a host where recording is
	// off: no recorder token, or no bot store.
	recordingOffRefusal     = "❌ Recording is off on this bot."
	recordNotInVoiceRefusal = "❌ Join the voice or stage channel you want to record, then run /record start again."
	recordInHubRefusal      = "❌ Hubs can't be recorded. Start the recording in the channel you're moved to."
	noRecordingRoleRefusal  = "❌ You need a recording role to record. The recording roles are set in the panel."
	notCavMemberRefusal     = "❌ Only Cav members can record."
	// noFreeRecorderRefusal opens the refusal of a start when every
	// recorder is recording. The channels they record follow it.
	noFreeRecorderRefusal   = "❌ Every recorder is busy."
	nothingToStopRefusal    = "❌ There's no recording here for you to stop."
	notAllowedToStopRefusal = "❌ Only the person who started this recording, or someone with a recording role, can stop it."
)

// startRefusal renders a refused start as the member's reply. refused is
// false for a start that failed after its checks.
func startRefusal(err error) (reply string, refused bool) {
	var busy *noFreeRecorderError
	switch {
	case errors.Is(err, errRecordingOff):
		return recordingOffRefusal, true
	case errors.Is(err, errRecordNotInVoice):
		return recordNotInVoiceRefusal, true
	case errors.Is(err, errRecordInHub):
		return recordInHubRefusal, true
	case errors.Is(err, errNoRecordingRole):
		return noRecordingRoleRefusal, true
	case errors.Is(err, errNotCavMember):
		return notCavMemberRefusal, true
	case errors.As(err, &busy):
		mentions := make([]string, len(busy.ChannelIDs))
		for i, id := range busy.ChannelIDs {
			mentions[i] = "<#" + id + ">"
		}
		return noFreeRecorderRefusal + " Recording now: " + strings.Join(mentions, ", ") + ".", true
	default:
		return "", false
	}
}

// stopRefusal renders a refused stop as the member's reply. refused is
// false for a stop that failed after its checks.
func stopRefusal(err error) (reply string, refused bool) {
	switch {
	case errors.Is(err, errNothingToStop):
		return nothingToStopRefusal, true
	case errors.Is(err, errNotAllowedToStop):
		return notAllowedToStopRefusal, true
	default:
		return "", false
	}
}
