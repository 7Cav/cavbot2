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
// can't hear the channel, because its connection to the voice server
// dropped, the call moved to a voice server the recorder hasn't reached yet,
// or its DAVE setup stalled. The voice adapter says when one starts and
// ends, and can tell of a start only later (#769). One that passes 10 s gets
// a gap notice in the recorded channel's chat, edited when the gap ends, and
// a line in the info file. One that reaches 10 minutes ends the recording as
// the recorder leaving the channel does.

// gapNoticeAfter is how long a gap runs before its notice posts: as long as
// the voice adapter waits before Sentry hears of it.
const gapNoticeAfter = voice.GapReportedAfter

// recordingGapLimit is the longest a gap runs before it ends the recording.
const recordingGapLimit = 10 * time.Minute

// The gap notice's lines, word for word as the issue gives them. Each %d is
// a time in Unix seconds, which Discord shows in each reader's own time zone.
const (
	// gapNoticeRunning is the notice while its gap runs, from the gap's
	// start.
	gapNoticeRunning = "⚠️ The recorder can't hear this channel since <t:%d:T>. It's reconnecting, and nothing said since then is recorded."
	// gapNoticeBack is the notice once the recorder is back: the gap's
	// start, its end, and its length rounded to the second.
	gapNoticeBack = "✅ The recorder is back. It missed <t:%d:T> to <t:%d:T> (%s)."
	// gapNoticeStopped is the notice of a gap a stop ended, and
	// gapNoticeLimit of one that ended the recording at recordingGapLimit,
	// each from the gap's start.
	gapNoticeStopped = "⏹️ The recording stopped before the recorder got back. It missed everything from <t:%d:T>."
	gapNoticeLimit   = "❌ The recorder couldn't get back to this call, so the recording stopped. It missed everything from <t:%d:T>."
)

// recordingGap is one gap in a recording. Its end is zero while it runs.
type recordingGap struct {
	gapSpan
	// cancelNotice cancels the post of the gap's notice, and cancelLimit
	// the stop at recordingGapLimit. noticed is set once the gap has passed
	// gapNoticeAfter, when its notice posts, and endedRecording once it has
	// reached recordingGapLimit. The runtime's mutex guards them and the
	// gap's end.
	cancelNotice, cancelLimit func()
	noticed, endedRecording   bool

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

func (l recordingListener) Frame(f voice.Frame)        { l.rec.tracks.write(f, recordingNow()) }
func (l recordingListener) GapStarted(since time.Time) { l.r.gapStarted(l.rec, since) }
func (l recordingListener) GapEnded()                  { l.r.gapEnded(l.rec) }

// gapStarted starts a gap in a recording no stop has taken, at since, and
// times its notice and its limit from then. The voice adapter can tell of a
// gap only after it started, as when a voice server went silent.
func (r *RecordingRuntime) gapStarted(rec *activeRecording, since time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.stopping || rec.gap != nil {
		return
	}
	g := &recordingGap{gapSpan: gapSpan{start: since}}
	rec.gap = g
	elapsed := recordingNow().Sub(since)
	g.cancelNotice = recordingAfterFunc(gapNoticeAfter-elapsed, func() { r.postGapNotice(rec, g) })
	g.cancelLimit = recordingAfterFunc(recordingGapLimit-elapsed, func() { r.gapLimitReached(rec, g) })
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
	g.noticed = true
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

// gapEnded ends the gap running in a recording no stop has taken. A gap
// past gapNoticeAfter has its notice edited to say the recorder is back,
// on a goroutine of its own: the voice adapter waits on this call.
func (r *RecordingRuntime) gapEnded(rec *activeRecording) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.stopping || rec.gap == nil {
		return
	}
	if g := r.closeGapLocked(rec, recordingNow()); g != nil {
		content := fmt.Sprintf(gapNoticeBack, g.start.Unix(), g.end.Unix(), g.length())
		recordingAfterFunc(0, func() { r.editGapNotice(rec, g, content) })
	}
}

// gapLimitReached stops a recording whose gap has run to
// recordingGapLimit, unless a stop has taken it first or the gap has ended.
func (r *RecordingRuntime) gapLimitReached(rec *activeRecording, g *recordingGap) {
	defer utils.RecoverPanic("recording-gap-limit", "recording_id", rec.row.ID)
	r.mu.Lock()
	if rec.stopping || rec.gap != g || r.running[rec.row.RecorderID] != rec {
		r.mu.Unlock()
		return
	}
	rec.stopping, g.endedRecording = true, true
	r.mu.Unlock()
	r.finish(rec, store.RecordingEndRecorderLeft, "")
}

// endGapAtStop ends the gap running in a recording a stop has taken, at
// stoppedAt, and returns it when it passed gapNoticeAfter, nil otherwise.
func (r *RecordingRuntime) endGapAtStop(rec *activeRecording, stoppedAt time.Time) *recordingGap {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.gap == nil {
		return nil
	}
	return r.closeGapLocked(rec, stoppedAt)
}

// closeGapLocked ends a recording's running gap at end, and returns it when
// it passed gapNoticeAfter: the recording lists it in its info file, and the
// log says how long it lasted. Caller holds mu, and the recording has a gap.
func (r *RecordingRuntime) closeGapLocked(rec *activeRecording, end time.Time) *recordingGap {
	g := rec.gap
	rec.gap = nil
	g.cancelNotice()
	g.cancelLimit()
	if !g.noticed {
		return nil
	}
	g.end = end
	rec.gaps = append(rec.gaps, g.gapSpan)
	msg := "Recording gap ended"
	if g.endedRecording {
		msg = "Recording gap reached the limit, recording stopped"
	}
	utils.Info(msg, "recording_id", rec.row.ID, "channel_id", rec.row.ChannelID, "recorder", rec.row.RecorderID,
		"lasted", g.length())
	return g
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
	if g.endedRecording {
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
