package commands

import (
	"slices"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// A recording's automatic stops (spec #381, #388). A recording stops by
// itself when no human is left in its channel, when its recorder leaves the
// channel, when it reaches the cap, and when free disk space runs low. Each
// channel check reads a fresh snapshot of discordgo's state cache, as temp
// VC's do, so an event handled late judges the channel as it is now.

// recordingCap is the longest a recording runs, so a forgotten one can't
// run all night.
const recordingCap = 6 * time.Hour

// recordingMinFreeDisk is the free space on the recordings volume below
// which a running recording stops, so what it captured is kept whole and
// its mix has room.
const recordingMinFreeDisk uint64 = 1 << 30

// recordingDiskCheckInterval is how often a running recording reads the
// free space.
const recordingDiskCheckInterval = 30 * time.Second

// recordingFreeDisk reads the free space of the filesystem holding a
// directory, a package var so tests can set it.
var recordingFreeDisk = freeDiskSpace

// checkDisk stops a running recording when the recordings volume has less
// than recordingMinFreeDisk free, and otherwise checks again after
// recordingDiskCheckInterval. A read that fails is a WARN line, and the
// recording goes on.
func (r *RecordingRuntime) checkDisk(rec *activeRecording) {
	defer utils.RecoverPanic("recording-disk-check", "recording_id", rec.row.ID)
	free, err := recordingFreeDisk(r.dir)
	if err != nil {
		utils.Warn("Free disk space unreadable", "recording_id", rec.row.ID, "dir", r.dir, "error", err)
	} else if free < recordingMinFreeDisk {
		r.autoStop(rec, store.RecordingEndDisk)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !rec.stopping {
		rec.cancelDiskCheck = recordingAfterFunc(recordingDiskCheckInterval, func() { r.checkDisk(rec) })
	}
}

// autoStop stops a running recording by itself, as end says, unless a stop
// has taken it first. The timers call it on goroutines of their own.
func (r *RecordingRuntime) autoStop(rec *activeRecording, end store.RecordingEnd) {
	defer utils.RecoverPanic("recording-auto-stop", "recording_id", rec.row.ID)
	r.mu.Lock()
	if rec.stopping || r.running[rec.row.RecorderID] != rec {
		r.mu.Unlock()
		return
	}
	rec.stopping = true
	r.mu.Unlock()
	r.finish(rec, end, "")
}

// Listen registers the runtime's gateway handlers on the main session: its
// voice events and channel deletes. main.go calls it before the session
// opens.
func (r *RecordingRuntime) Listen(dg *discordgo.Session) {
	dg.AddHandler(func(_ *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
		defer utils.RecoverPanic("recording-voice-state")
		r.HandleVoiceStateUpdate(vs)
	})
	dg.AddHandler(func(_ *discordgo.Session, c *discordgo.ChannelDelete) {
		defer utils.RecoverPanic("recording-channel-delete")
		r.HandleChannelDelete(c)
	})
}

// HandleVoiceStateUpdate checks every running recording's channel after a
// voice event in the guild.
func (r *RecordingRuntime) HandleVoiceStateUpdate(vs *discordgo.VoiceStateUpdate) {
	if vs == nil || vs.VoiceState == nil || vs.GuildID != r.guildID {
		return
	}
	r.checkChannels()
}

// checkChannels checks every running recording against a fresh snapshot of
// the cache. It stops each one whose recorder is no longer in its channel,
// and each one whose channel holds no human. A voice event runs it, and so
// does each start once the recording runs, since the cache may already
// show the recorder's arrival and no later event would.
func (r *RecordingRuntime) checkChannels() {
	snap := r.mgr.VoiceStates(r.guildID)

	type taken struct {
		rec *activeRecording
		end store.RecordingEnd
	}
	var stops []taken
	r.mu.Lock()
	for _, rec := range r.running {
		if !rec.live() {
			continue
		}
		// The cache can show the recorder in the channel only some time
		// after its join returns, so it counts as leaving only once the
		// cache has shown it there.
		recorderIn := snap.channelOf(rec.row.RecorderID) == rec.row.ChannelID
		if recorderIn {
			rec.recorderSeen = true
		}
		switch {
		case rec.recorderSeen && !recorderIn:
			rec.stopping = true
			stops = append(stops, taken{rec, store.RecordingEndRecorderLeft})
		case !r.holdsHuman(snap, rec.row.ChannelID):
			rec.stopping = true
			stops = append(stops, taken{rec, store.RecordingEndEmpty})
		}
	}
	r.mu.Unlock()
	for _, s := range stops {
		r.finish(s.rec, s.end, "")
	}
}

// HandleChannelDelete stops the recording of a channel deleted while it
// runs: its recorder has left with the channel.
func (r *RecordingRuntime) HandleChannelDelete(c *discordgo.ChannelDelete) {
	if c == nil || c.Channel == nil || c.GuildID != r.guildID {
		return
	}
	r.mu.Lock()
	rec := r.runningInLocked(c.ID)
	r.mu.Unlock()
	if rec != nil {
		r.autoStop(rec, store.RecordingEndRecorderLeft)
	}
}

// holdsHuman reports whether anyone in a channel is human: neither a
// recorder nor a bot.
func (r *RecordingRuntime) holdsHuman(snap VoiceSnapshot, channelID string) bool {
	for userID, ch := range snap.ChannelByUser {
		if ch != channelID || slices.Contains(r.recorders, userID) {
			continue
		}
		if _, bot := snap.Bots[userID]; !bot {
			return true
		}
	}
	return false
}
