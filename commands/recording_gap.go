package commands

import (
	"fmt"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/7cav/cavbot2/voice"
	"github.com/bwmarrin/discordgo"
)

// A recording's gaps (#736, #856, GLOSSARY.md Gap): the stretches its
// recorder can't hear the channel, whatever the cause. The voice adapter
// says when one starts and ends, and can tell of a start only later (#769).
// One that reaches recordingGapLimit cuts the recording short.

// recordingGapLimit is the longest a gap runs before it cuts the recording
// short. The ping tells the starter so in words.
const recordingGapLimit = 10 * time.Second

// recordingGapEndedMessage is the INFO line of each gap that ends before
// cutting its recording short, ended by the recorder hearing the channel
// again or by a stop. It carries what started the gap and how long it
// lasted.
const recordingGapEndedMessage = "Recording gap ended"

// recordingGapPing is the ping a gap's cut short posts in the recorded
// channel's chat, word for word as the issue gives it: the starter's
// mention, then the command that records again.
const recordingGapPing = "❌ <@%s> Your recording stopped because the recorder couldn't hear this channel for 10 seconds. Run %s to record again."

// recordingCutShortMessage is the Sentry message tag of every recording a
// gap cut short, whatever the cause, so one search lists them all.
const recordingCutShortMessage = "Recording cut short, the recorder couldn't hear the channel"

// The gateway_session extra of a cut short's event: whether the recorder's
// own gateway session was online at the 10 s mark, so that a rejoin could
// go out.
const (
	gatewaySessionOnline  = "online"
	gatewaySessionOffline = "offline"
)

// cutShortTimeLayout is RFC 3339 in UTC to the millisecond, how a cut
// short's event gives its times.
const cutShortTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// recordingGapPingNotPostedMessage is the WARN a ping Discord refused logs.
// The bot never posts it again, and the starter gets the link by DM.
const recordingGapPingNotPostedMessage = "Cut-short ping not posted"

// recordingGap is the gap running in a recording, which started at start
// for cause.
type recordingGap struct {
	start time.Time
	cause voice.GapCause
	// cancelLimit cancels the cut short at recordingGapLimit. The runtime's
	// mutex guards it.
	cancelLimit func()
}

// recordingListener is what a recording's recorder hands it: the frames it
// hears, which go to the tracks at the runtime's clock, and its gaps.
type recordingListener struct {
	r   *RecordingRuntime
	rec *activeRecording
}

func (l recordingListener) Frame(f voice.Frame) { l.rec.tracks.write(f, recordingNow()) }
func (l recordingListener) GapStarted(since time.Time, cause voice.GapCause) {
	l.r.gapStarted(l.rec, since, cause)
}
func (l recordingListener) GapEnded() { l.r.gapEnded(l.rec) }

// gapStarted starts a gap in a recording no stop has taken, at since for
// cause, and times its limit from then. The voice adapter can tell of a gap
// only after it started, as when a voice server went silent.
func (r *RecordingRuntime) gapStarted(rec *activeRecording, since time.Time, cause voice.GapCause) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.stopping || rec.gap != nil {
		return
	}
	g := &recordingGap{start: since, cause: cause}
	rec.gap = g
	g.cancelLimit = recordingAfterFunc(recordingGapLimit-recordingNow().Sub(since), func() { r.gapLimitReached(rec, g) })
}

// gapEnded ends the gap running in a recording no stop has taken.
func (r *RecordingRuntime) gapEnded(rec *activeRecording) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.stopping || rec.gap == nil {
		return
	}
	r.closeGapLocked(rec, recordingNow())
}

// gapLimitReached cuts short a recording whose gap has run to
// recordingGapLimit, unless a stop has taken it first or the gap has ended.
// From then the recording no longer counts as running, so its channel and
// its recorder are free for a start, and the voice adapter counts the
// recorder offline, so that start waits for its next login.
func (r *RecordingRuntime) gapLimitReached(rec *activeRecording, g *recordingGap) {
	defer utils.RecoverPanic("recording-gap-limit", "recording_id", rec.row.ID)
	r.mu.Lock()
	if rec.stopping || rec.gap != g || r.running[rec.row.RecorderID] != rec {
		r.mu.Unlock()
		return
	}
	rec.stopping, rec.gap = true, nil
	delete(r.running, rec.row.RecorderID)
	relogin := r.voice.TakeOffline(rec.row.RecorderID)
	r.mu.Unlock()
	r.cutShort(rec, g, relogin)
}

// cutShort ends a recording whose gap reached recordingGapLimit as a gap's
// cut short, at the gap's start. Its tracks end there at once: the recorder
// goes on handing over what it hears until its leave returns, and none of
// that is kept. The starter and the channel hear of it at once too, by the
// ping and the notice, without waiting on the recorder's leave. The starter
// gets the link by DM as well when the chat may not reach them: in a spawned
// channel, with the notice gone, or with the ping refused. One DM at most,
// whichever holds. Then the recorder leaves, and once its leave has
// returned or given up, relogin has it log out and back in. The row stops
// at gapStart, and the mix starts. None of it touches a later recording of
// the same channel or recorder.
func (r *RecordingRuntime) cutShort(rec *activeRecording, g *recordingGap, relogin func()) {
	gapStart := g.start
	r.cancelTimers(rec)
	endTracks(rec, gapStart)
	reportCutShort(rec.row, g, rec.conn)
	// ended is the row as it ends, which the ping, the notice and the DM
	// tell of before the store has it.
	ended := rec.row
	ended.StoppedAt, ended.Ended = gapStart, store.RecordingEndGap
	pinged := r.pingStarter(ended)
	if gone := r.editRecordingNotice(ended); gone || !pinged || r.tv.IsSpawned(ended.ChannelID) {
		r.sendRecordingLink(ended)
	}
	r.leave(rec.conn, rec.row.RecorderID, rec.row.ChannelID)
	relogin()
	row := r.closeRow(rec, gapStart, store.RecordingEndGap)
	logRecordingStopped(row, "")
	r.startMix(row)
}

// reportCutShort sends Sentry the one event of a recording gap g cut
// short, in the Sentry issue of g's cause, with the recovery steps the
// recorder took since the gap's start. A server deafen's goes at
// warning level, which the high-priority issue alert skips: a person chose
// the deafen, and no recovery work fixes it.
func reportCutShort(row store.Recording, g *recordingGap, conn voice.Conn) {
	issue := utils.SentryIssue{
		Fingerprint: []string{recordingCutShortMessage, string(g.cause)},
		Warning:     g.cause == voice.GapServerDeafened,
	}
	rep := conn.GapReport()
	session := gatewaySessionOffline
	if rep.GatewaySessionOnline {
		session = gatewaySessionOnline
	}
	kv := []any{"recording_id", row.ID, "channel_id", row.ChannelID, "recorder", row.RecorderID,
		"gap_cause", string(g.cause), "voice_server", rep.VoiceServer,
		"gap_started_at", g.start.UTC().Format(cutShortTimeLayout),
		"recorded_for", cutShortLength(g.start.Sub(row.StartedAt)), "gateway_session", session}
	if rep.Close != nil {
		// The close's text goes to the log and Sentry alone (ADR 0018).
		kv = append(kv, "close_error", rep.Close.Error())
	}
	if rep.CloseCode != "" {
		kv = append(kv, "close_code", rep.CloseCode)
	}
	for _, f := range rep.Fields {
		value := f.Value
		if d, ok := value.(time.Duration); ok {
			value = cutShortLength(d)
		}
		kv = append(kv, f.Key, value)
	}
	kv = append(kv, "attempts", cutShortAttempts(rep.Steps))
	if rep.EarlierSteps > 0 {
		kv = append(kv, "earlier_attempts", rep.EarlierSteps)
	}
	utils.CaptureErrorIn(issue, recordingCutShortMessage, g.cause.CutShortError(), kv...)
}

// cutShortLength is how a cut short's event gives a length: Go duration
// text, rounded to the millisecond.
func cutShortLength(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

// cutShortAttempts is how a cut short's event lists the recovery steps the
// recorder took, in the voice adapter's order: one entry for each, with its
// kind, a dial's token, when it started from the gap's start, how it ended,
// and the code of the voice gateway close that ended it when one did. An
// empty list says the recorder tried nothing.
func cutShortAttempts(steps []voice.RecoveryStep) []map[string]string {
	attempts := make([]map[string]string, 0, len(steps))
	for _, s := range steps {
		attempt := map[string]string{"kind": string(s.Kind), "at": cutShortLength(s.At), "outcome": string(s.Outcome)}
		if s.Token != "" {
			attempt["token"] = string(s.Token)
		}
		if s.CloseCode != "" {
			attempt["close_code"] = s.CloseCode
		}
		attempts = append(attempts, attempt)
	}
	return attempts
}

// pingStarter posts the ping of a recording a gap cut short: a new message
// in the recorded channel's chat that pings the starter alone, wherever
// they are, and replies to the recording notice. A notice gone by then
// gives a ping with no reply rather than none. A post that fails is a WARN
// line. It reports whether the ping posted.
func (r *RecordingRuntime) pingStarter(row store.Recording) bool {
	failIfNoticeGone := false
	_, err := r.mgr.ChannelMessageSendComplex(row.ChannelID, &discordgo.MessageSend{
		Content: fmt.Sprintf(recordingGapPing, row.StarterID, r.recordStartText()),
		Reference: &discordgo.MessageReference{
			MessageID: row.NoticeID, ChannelID: row.ChannelID, GuildID: row.GuildID, FailIfNotExists: &failIfNoticeGone,
		},
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}, Users: []string{row.StarterID}},
	})
	if err != nil {
		utils.Warn(recordingGapPingNotPostedMessage, "recording_id", row.ID, "channel_id", row.ChannelID, "error", err)
		return false
	}
	return true
}

// recordStartText is how the ping names /record start. Once the runtime
// knows /record's ID it is Discord's command mention, which fills the chat
// box of whoever clicks it. Before then it is plain text.
func (r *RecordingRuntime) recordStartText() string {
	run := recordCommandName + " start"
	if id, _ := r.recordCommandID.Load().(string); id != "" {
		return "</" + run + ":" + id + ">"
	}
	return "/" + run
}

// endGapAtStop ends the gap running in a recording a stop has taken, at
// stoppedAt.
func (r *RecordingRuntime) endGapAtStop(rec *activeRecording, stoppedAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.gap != nil {
		r.closeGapLocked(rec, stoppedAt)
	}
}

// closeGapLocked ends a recording's running gap at end, and logs what
// started it and how long it lasted. Caller holds mu, and the recording has
// a gap.
func (r *RecordingRuntime) closeGapLocked(rec *activeRecording, end time.Time) {
	g := rec.gap
	rec.gap = nil
	g.cancelLimit()
	utils.Info(recordingGapEndedMessage, "recording_id", rec.row.ID, "channel_id", rec.row.ChannelID,
		"recorder", rec.row.RecorderID, "gap_cause", string(g.cause), "lasted", end.Sub(g.start).Round(time.Millisecond))
}
