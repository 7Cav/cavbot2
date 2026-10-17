package commands

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/7cav/cavbot2/voice"
	"github.com/bwmarrin/discordgo"
)

// A recording's gaps (#736, GLOSSARY.md Gap): the stretches its recorder
// can't hear the channel, because the call moved to a voice server the
// recorder hasn't reached yet or its DAVE setup stalled. The voice adapter
// says when one starts and ends. One that passes 10 s gets a gap notice in
// the recorded channel's chat.

// gapNoticeAfter is how long a gap runs before its notice posts: as long as
// the voice adapter waits before Sentry hears of it.
const gapNoticeAfter = voice.GapReportedAfter

// recordingGapLimit is the longest a gap runs before it ends the recording,
// as the recorder leaving the channel does.
const recordingGapLimit = 10 * time.Minute

// gapNoticeRunning is the gap notice while its gap runs, word for word as
// the issue gives it. The %d is the gap's start in Unix seconds, which
// Discord shows in each reader's own time zone.
const gapNoticeRunning = "⚠️ The recorder can't hear this channel since <t:%d:T>. It's reconnecting, and nothing said since then is recorded."

// gapNoticeBack is the gap notice once the recorder is back, word for word
// as the issue gives it: the gap's start and end in Unix seconds, and its
// length rounded to the second.
const gapNoticeBack = "✅ The recorder is back. It missed <t:%d:T> to <t:%d:T> (%s)."

// gapNoticeStopped is the gap notice of a gap a stop ended, word for word as
// the issue gives it, with the gap's start in Unix seconds.
const gapNoticeStopped = "⏹️ The recording stopped before the recorder got back. It missed everything from <t:%d:T>."

// gapNoticeLimit is the gap notice of a gap that ended the recording at
// recordingGapLimit, word for word as the issue gives it, with the gap's
// start in Unix seconds.
const gapNoticeLimit = "❌ The recorder couldn't get back to this call, so the recording stopped. It missed everything from <t:%d:T>."

// recordingGap is one gap in a recording.
type recordingGap struct {
	start time.Time
	// end is when the gap ended, zero while it runs.
	end time.Time
	// cancelNotice cancels the post of the gap's notice, and cancelLimit
	// the stop at recordingGapLimit. long is set once the gap has passed
	// gapNoticeAfter, when its notice posts, and limit once it has reached
	// recordingGapLimit. The runtime's mutex guards long, limit and end.
	cancelNotice, cancelLimit func()
	long, limit               bool

	// mu is held while the gap notice posts.
	mu sync.Mutex
	// noticeID is the gap notice's message ID, empty until it posts.
	noticeID string
}

// recordingListener is what a recording's recorder hands it: the frames it
// hears, which go to the tracks at the runtime's clock, and its gaps.
type recordingListener struct {
	r   *RecordingRuntime
	rec *activeRecording
}

func (l recordingListener) Frame(f voice.Frame) { l.rec.tracks.write(f, recordingNow()) }
func (l recordingListener) GapStarted()         { l.r.gapStarted(l.rec) }
func (l recordingListener) GapEnded()           { l.r.gapEnded(l.rec) }

// gapStarted starts a gap in a recording no stop has taken, timing its
// notice from now.
func (r *RecordingRuntime) gapStarted(rec *activeRecording) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.stopping || rec.gap != nil {
		return
	}
	g := &recordingGap{start: recordingNow()}
	rec.gap = g
	g.cancelNotice = recordingAfterFunc(gapNoticeAfter, func() { r.postGapNotice(rec, g) })
	g.cancelLimit = recordingAfterFunc(recordingGapLimit, func() { r.gapLimitReached(rec, g) })
}

// gapLimitReached stops a recording whose gap has run to
// recordingGapLimit, unless a stop has taken it first or the gap has ended.
// It ends as the recorder leaving the channel does.
func (r *RecordingRuntime) gapLimitReached(rec *activeRecording, g *recordingGap) {
	defer utils.RecoverPanic("recording-gap-limit", "recording_id", rec.row.ID)
	r.mu.Lock()
	if rec.stopping || rec.gap != g || r.running[rec.row.RecorderID] != rec {
		r.mu.Unlock()
		return
	}
	rec.stopping, g.limit = true, true
	r.mu.Unlock()
	r.finish(rec, store.RecordingEndRecorderLeft, "")
}

// gapEnded ends the gap running in a recording no stop has taken. A gap
// past gapNoticeAfter has its notice edited to say the recorder is back,
// on a goroutine of its own: the voice adapter waits on this call.
func (r *RecordingRuntime) gapEnded(rec *activeRecording) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := rec.gap
	if rec.stopping || g == nil {
		return
	}
	rec.gap = nil
	g.end = recordingNow()
	g.cancelNotice()
	g.cancelLimit()
	if g.long {
		rec.gaps = append(rec.gaps, gapSpan{g.start, g.end})
		logGapEnd(rec, g)
		content := fmt.Sprintf(gapNoticeBack, g.start.Unix(), g.end.Unix(), g.end.Sub(g.start).Round(time.Second))
		recordingAfterFunc(0, func() { r.editGapNotice(rec, g, content) })
	}
}

// endGapAtStop ends the gap running in a recording a stop has taken, at
// stoppedAt, and returns it when it passed gapNoticeAfter, nil otherwise.
func (r *RecordingRuntime) endGapAtStop(rec *activeRecording, stoppedAt time.Time) *recordingGap {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := rec.gap
	if g == nil {
		return nil
	}
	rec.gap = nil
	g.cancelNotice()
	g.cancelLimit()
	if !g.long {
		return nil
	}
	g.end = stoppedAt
	rec.gaps = append(rec.gaps, gapSpan{g.start, g.end})
	logGapEnd(rec, g)
	return g
}

// logGapEnd records how long a gap past gapNoticeAfter lasted once it has
// ended, or that it ended the recording at recordingGapLimit.
func logGapEnd(rec *activeRecording, g *recordingGap) {
	msg := "Recording gap ended"
	if g.limit {
		msg = "Recording gap reached the limit, recording stopped"
	}
	utils.Info(msg, "recording_id", rec.row.ID, "channel_id", rec.row.ChannelID, "recorder", rec.row.RecorderID,
		"lasted", g.end.Sub(g.start).Round(time.Second))
}

// gapsOf is a recording's gaps past 10 s that have ended, in order.
func (r *RecordingRuntime) gapsOf(rec *activeRecording) []gapSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(rec.gaps)
}

// stoppedContent is the notice of a gap a stop ended: at the limit, or by
// any other stop.
func (g *recordingGap) stoppedContent() string {
	if g.limit {
		return fmt.Sprintf(gapNoticeLimit, g.start.Unix())
	}
	return fmt.Sprintf(gapNoticeStopped, g.start.Unix())
}

// editGapNotice edits a gap's notice to content, once its post has
// returned. A gap whose notice didn't post has none to edit. An edit that
// fails is a WARN line, and the recording goes on.
func (r *RecordingRuntime) editGapNotice(rec *activeRecording, g *recordingGap, content string) {
	defer utils.RecoverPanic("recording-gap-notice-edit", "recording_id", rec.row.ID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.noticeID == "" {
		return
	}
	_, err := r.mgr.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:              g.noticeID,
		Channel:         rec.row.ChannelID,
		Content:         &content,
		AllowedMentions: noMentions(),
	})
	if err != nil {
		utils.Warn("Gap notice not edited", "recording_id", rec.row.ID, "channel_id", rec.row.ChannelID,
			"message_id", g.noticeID, "error", err)
	}
}

// postGapNotice posts the notice of a gap still running in a recording no
// stop has taken. It pings nobody. A post that fails is a WARN line, and the
// recording goes on.
func (r *RecordingRuntime) postGapNotice(rec *activeRecording, g *recordingGap) {
	defer utils.RecoverPanic("recording-gap-notice", "recording_id", rec.row.ID)
	r.mu.Lock()
	if rec.stopping || rec.gap != g {
		r.mu.Unlock()
		return
	}
	g.long = true
	g.mu.Lock()
	r.mu.Unlock()
	defer g.mu.Unlock()
	msg, err := r.mgr.ChannelMessageSendComplex(rec.row.ChannelID, &discordgo.MessageSend{
		Content:         fmt.Sprintf(gapNoticeRunning, g.start.Unix()),
		AllowedMentions: noMentions(),
	})
	if err != nil {
		utils.Warn("Gap notice not posted", "recording_id", rec.row.ID, "channel_id", rec.row.ChannelID, "error", err)
		return
	}
	g.noticeID = msg.ID
}
