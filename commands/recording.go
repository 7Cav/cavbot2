package commands

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/7cav/cavbot2/voice"
	"github.com/bwmarrin/discordgo"
)

// Voice recording (spec #381). The bot replaces Craig: a Cav member holding
// a recording role runs /record start in the voice or stage channel they are
// in, and a free recorder (GLOSSARY.md) joins it. /record stop makes it
// leave. The recording stays with the channel it started in: the starter
// moving elsewhere takes nothing with them.
//
// The runtime follows temp VC's pattern: one Discord-facing interface
// (RecordingManager, which the session's one TempVCManager satisfies), each
// decision checked against a fresh copy of discordgo's state cache, and
// package clock variables. The voice adapter sits behind RecorderVoice, so
// no disgo type reaches this package. The recording roles are read from the
// store at each start and stop, so a panel save takes effect at once.
//
// A recording's notice (recording_notice.go) is posted once its recorder is
// in the channel, and its row written after that. The row is closed with
// its stop time and how it ended once the recorder has left, and the notice
// edited to say it stopped. Its tracks (recording_tracks.go) take what the
// recorder hears from the row write on, and end at the stop time. Besides
// /record stop and the notice's button, a recording stops by itself
// (recording_stops.go).

// recordingNow is the recording runtime's clock, a package var as tempVCNow
// is, so tests can pin a recording's start and stop times.
var recordingNow = time.Now

// recordingAfterFunc runs f on a goroutine of its own once d has passed, as
// tempVCAfterFunc does. A recording's flushes are timed through it, so tests
// run them on the fake clock.
var recordingAfterFunc = func(d time.Duration, f func()) (stop func()) {
	timer := time.AfterFunc(d, f)
	return func() { timer.Stop() }
}

// recordingJoinTimeout bounds a recorder's join. The command has deferred
// its reply by then, so the member waits on it.
const recordingJoinTimeout = 30 * time.Second

// recordingLeaveTimeout bounds a recorder's leave.
const recordingLeaveTimeout = 5 * time.Second

// recordingStoreTimeout bounds each store call the runtime makes.
const recordingStoreTimeout = 5 * time.Second

// The reasons a start is refused. The command turns each into the member's
// reply.
var (
	// errRecordingOff: no recorder is configured, so nobody can record.
	errRecordingOff = errors.New("recording: no recorder configured")
	// errRecordNotInVoice: the invoker is in no voice or stage channel.
	errRecordNotInVoice = errors.New("recording: invoker is not in a voice channel")
	// errRecordInHub: the invoker's channel is a hub, which nobody stays in.
	errRecordInHub = errors.New("recording: channel is a hub")
	// errNoRecordingRole: the invoker holds none of the recording roles.
	errNoRecordingRole = errors.New("recording: invoker holds no recording role")
	// errNotCavMember: the invoker holds a recording role and no rank role,
	// so isn't a Cav member, and the panel couldn't find the recording for
	// them.
	errNotCavMember = errors.New("recording: invoker holds no rank role")
)

// errRecordingFailed is a start or stop that failed after the checks passed,
// such as a recorder that couldn't join. The runtime has reported the
// cause to Sentry.
var errRecordingFailed = errors.New("recording: failed")

// The reasons a stop is refused.
var (
	// errNothingToStop: no recording runs in the invoker's channel, and
	// none they started runs anywhere.
	errNothingToStop = errors.New("recording: nothing to stop")
	// errNotAllowedToStop: the invoker's channel is recorded, and they are
	// neither its starter nor a recording-role holder.
	errNotAllowedToStop = errors.New("recording: invoker may not stop this recording")
)

// noFreeRecorderError: every recorder is recording. ChannelIDs are the
// channels they record, which the refusal names.
type noFreeRecorderError struct {
	ChannelIDs []string
}

func (e *noFreeRecorderError) Error() string { return "recording: no recorder is free" }

// RecorderVoice is the voice adapter as the recording runtime sees it:
// the recorders, each in one voice channel at a time. *voice.Recorders
// implements it.
type RecorderVoice interface {
	// UserIDs returns the recorders' user IDs. None means recording is off.
	UserIDs() []string
	// Join connects the recorder to a voice channel of the guild.
	Join(ctx context.Context, recorderID, guildID, channelID string) (voice.Conn, error)
}

// RecordingManager is the subset of the Discord session the recording
// runtime reads, all of it from discordgo's state cache.
type RecordingManager interface {
	// VoiceStates is TempVCManager's: one copied snapshot of who is in
	// which voice channel.
	VoiceStates(guildID string) VoiceSnapshot
	// Channel reads a channel from the cache, for its name.
	Channel(channelID string) (*discordgo.Channel, error)
	// ChannelMessageSendComplex posts the recording notice.
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend) (*discordgo.Message, error)
	// ChannelMessageEditComplex edits the recording notice at stop.
	ChannelMessageEditComplex(edit *discordgo.MessageEdit) (*discordgo.Message, error)
}

// RecordingRuntime holds the recordings running now. Built on every host
// with a bot store, recording on or off: with no recorder, every start is
// refused as recording off.
type RecordingRuntime struct {
	mgr       RecordingManager
	st        store.Store
	tv        *TempVC
	voice     RecorderVoice
	guildID   string
	recorders []string
	// dir is the recordings directory, which holds a directory of tracks
	// per recording.
	dir string

	mu sync.Mutex
	// running maps each busy recorder's user ID to its recording.
	running map[string]*activeRecording
}

// activeRecording is one recording a recorder is busy with. conn is nil
// while its join is in flight.
type activeRecording struct {
	row    store.Recording
	conn   voice.Conn
	tracks *recordingTracks
	// noticeID is the recording notice's message ID.
	noticeID string
	// cancelCap cancels the stop at the cap, and cancelDiskCheck the next
	// disk check. Both are set before the recording is running, so a stop
	// always finds them. A disk check sets cancelDiskCheck again under the
	// runtime's mutex, and only while the recording isn't stopping.
	cancelCap, cancelDiskCheck func()
	// stopping is set once a stop has taken it, so a second stop doesn't.
	stopping bool
	// recorderSeen is set once the cache has shown the recorder in the
	// channel.
	recorderSeen bool
}

// live reports whether the recording runs and no stop has taken it: its
// join has returned, and nothing is stopping it.
func (rec *activeRecording) live() bool {
	return rec.conn != nil && !rec.stopping
}

// mayStop applies /record stop's rule to someone: the starter may stop the
// recording from anywhere, and a recording-role holder from inside its
// channel. The button follows the same rule.
func (rec *activeRecording) mayStop(userID string, holdsRole, inChannel bool) bool {
	return rec.row.StarterID == userID || (holdsRole && inChannel)
}

// NewRecordingRuntime builds the recording runtime over the Discord
// session's manager, the store, temp VC (which knows the hubs) and the
// voice adapter, whose recorders it takes now. Tracks go under dir.
func NewRecordingRuntime(mgr RecordingManager, st store.Store, tv *TempVC, guildID string, rv RecorderVoice, dir string) *RecordingRuntime {
	return &RecordingRuntime{
		mgr:       mgr,
		st:        st,
		tv:        tv,
		voice:     rv,
		guildID:   guildID,
		recorders: rv.UserIDs(),
		dir:       dir,
		running:   make(map[string]*activeRecording),
	}
}

// Start starts a recording of the voice channel the invoker is in, with a
// free recorder, and returns the channel. With recording off it refuses
// before anything else, so the member learns that first.
func (r *RecordingRuntime) Start(by Invoker, title string) (channelID string, err error) {
	if len(r.recorders) == 0 {
		return "", errRecordingOff
	}
	channelID = r.mgr.VoiceStates(r.guildID).channelOf(by.UserID)
	if channelID == "" {
		return "", errRecordNotInVoice
	}
	if r.tv.IsHub(channelID) {
		return "", errRecordInHub
	}
	holds, err := r.holdsRecordingRole(by)
	if err != nil {
		return "", err
	}
	if !holds {
		return "", errNoRecordingRole
	}
	if _, ok := SeniorRankRole(by.Roles); !ok {
		return "", errNotCavMember
	}

	recorderID, err := r.reserveRecorder(channelID)
	if err != nil {
		return "", err
	}

	joinCtx, cancelJoin := context.WithTimeout(context.Background(), recordingJoinTimeout)
	defer cancelJoin()
	conn, err := r.voice.Join(joinCtx, recorderID, r.guildID, channelID)
	if err != nil {
		r.release(recorderID)
		captureError("Recorder failed to join", err, "recorder", recorderID, "channel_id", channelID)
		return "", errRecordingFailed
	}
	// The notice is how the channel learns it is recorded, so a recording
	// whose notice didn't post doesn't go on: the recorder leaves.
	noticeID, err := r.postRecordingNotice(channelID, by.UserID, title)
	if err != nil {
		r.leave(conn)
		r.release(recorderID)
		captureError("Recording notice not posted", err, "recorder", recorderID, "channel_id", channelID)
		return "", errRecordingFailed
	}
	row := store.Recording{
		GuildID:     r.guildID,
		ChannelID:   channelID,
		ChannelName: r.channelName(channelID),
		StarterID:   by.UserID,
		Title:       title,
		RecorderID:  recorderID,
		StartedAt:   recordingNow(),
	}
	storeCtx, cancelStore := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancelStore()
	row, err = r.st.StartRecording(storeCtx, row)
	if err != nil {
		// A recording with no row is one the panel and the deploy gate
		// can't see, so the recorder leaves rather than record it.
		r.leave(conn)
		r.release(recorderID)
		captureError("Recording row not written at start", err, "recorder", recorderID, "channel_id", channelID)
		return "", errRecordingFailed
	}
	// The recorder starts handing over what it hears only now, with the
	// row written: frames before wait in the socket. A frame arrives at the
	// runtime's clock when it's handed over. It receives before a stop can
	// find it, so the stop's leave ends the receiving.
	tracks := newRecordingTracks(r.dir, row.ID, row.StartedAt)
	conn.Receive(func(f voice.Frame) { tracks.write(f, recordingNow()) })
	rec := &activeRecording{row: row, conn: conn, tracks: tracks, noticeID: noticeID}
	rec.cancelCap = recordingAfterFunc(recordingCap, func() { r.autoStop(rec, store.RecordingEndCap) })
	rec.cancelDiskCheck = recordingAfterFunc(recordingDiskCheckInterval, func() { r.checkDisk(rec) })
	r.mu.Lock()
	r.running[recorderID] = rec
	r.mu.Unlock()
	utils.Info("Recording started", "recording_id", row.ID, "channel_id", channelID,
		"starter", by.UserID, "recorder", recorderID)
	// The recorder's own voice event was most likely handled while its join
	// was in flight, before the recording ran, so the channel is checked
	// now: the cache's view of it then counts as seen.
	r.checkChannels()
	return channelID, nil
}

// Stop stops a recording the invoker may stop, and returns its channel.
// The recorder leaves, and the row records the stop time. The starter may
// stop theirs from anywhere. A recording-role holder may stop the one in
// the channel they are in. The one in the invoker's channel comes first,
// then the invoker's own.
func (r *RecordingRuntime) Stop(by Invoker) (channelID string, err error) {
	here := r.mgr.VoiceStates(r.guildID).channelOf(by.UserID)
	holds, err := r.holdsRecordingRole(by)
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	var hereRec, ownRec *activeRecording
	for _, rec := range r.running {
		if !rec.live() {
			continue
		}
		if here != "" && rec.row.ChannelID == here {
			hereRec = rec
		}
		if rec.row.StarterID == by.UserID {
			ownRec = rec
		}
	}
	var target *activeRecording
	switch {
	case hereRec != nil && hereRec.mayStop(by.UserID, holds, true):
		target = hereRec
	case ownRec != nil:
		target = ownRec
	case hereRec != nil:
		r.mu.Unlock()
		return "", errNotAllowedToStop
	default:
		r.mu.Unlock()
		return "", errNothingToStop
	}
	target.stopping = true
	r.mu.Unlock()

	r.finish(target, store.RecordingEndStopped, by.UserID)
	return target.row.ChannelID, nil
}

// stopFromNotice stops the recording of the channel a notice's Stop button
// names, on the presser's behalf, under /record stop's rule: the starter
// from anywhere, or a recording-role holder in the channel.
func (r *RecordingRuntime) stopFromNotice(channelID string, by Invoker) error {
	in := r.mgr.VoiceStates(r.guildID).channelOf(by.UserID) == channelID
	holds, err := r.holdsRecordingRole(by)
	if err != nil {
		return err
	}

	r.mu.Lock()
	target := r.runningInLocked(channelID)
	switch {
	case target == nil:
		r.mu.Unlock()
		return errNothingToStop
	case !target.mayStop(by.UserID, holds, in):
		r.mu.Unlock()
		return errNotAllowedToStop
	}
	target.stopping = true
	r.mu.Unlock()

	r.finish(target, store.RecordingEndStopped, by.UserID)
	return nil
}

// runningInLocked returns the live recording of a channel, or nil when none
// runs there. Caller holds mu.
func (r *RecordingRuntime) runningInLocked(channelID string) *activeRecording {
	for _, rec := range r.running {
		if rec.live() && rec.row.ChannelID == channelID {
			return rec
		}
	}
	return nil
}

// finish ends a recording a stop has taken: the recorder leaves, the tracks
// end at the stop time, the row records the stop time and how it ended, and
// the notice says it stopped. stoppedBy is the member who stopped it, empty
// when it stopped by itself.
func (r *RecordingRuntime) finish(target *activeRecording, end store.RecordingEnd, stoppedBy string) {
	recorderID := target.row.RecorderID
	r.mu.Lock()
	cancelDiskCheck := target.cancelDiskCheck
	r.mu.Unlock()
	target.cancelCap()
	cancelDiskCheck()
	r.leave(target.conn)
	stoppedAt := recordingNow()
	if err := target.tracks.close(stoppedAt); err != nil {
		captureError("Tracks not closed at stop", err, "recording_id", target.row.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancel()
	if err := r.st.StopRecording(ctx, target.row.ID, stoppedAt, end); err != nil {
		captureError("Recording row not closed at stop", err, "recording_id", target.row.ID)
	}
	r.release(recorderID)
	r.closeRecordingNotice(target)
	utils.Info("Recording stopped", "recording_id", target.row.ID, "channel_id", target.row.ChannelID,
		"recorder", recorderID, "ended", string(end), "stopped_by", stoppedBy)
}

// reserveRecorder marks the first free recorder, in configured order, busy
// with a recording of the channel whose join is about to go out, and
// returns it.
func (r *RecordingRuntime) reserveRecorder(channelID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	busy := &noFreeRecorderError{}
	for _, id := range r.recorders {
		rec, ok := r.running[id]
		if !ok {
			r.running[id] = &activeRecording{row: store.Recording{ChannelID: channelID}}
			return id, nil
		}
		busy.ChannelIDs = append(busy.ChannelIDs, rec.row.ChannelID)
	}
	return "", busy
}

// leave takes a recorder out of its channel, with a deadline of its own.
func (r *RecordingRuntime) leave(conn voice.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), recordingLeaveTimeout)
	defer cancel()
	conn.Leave(ctx)
}

// release frees a recorder.
func (r *RecordingRuntime) release(recorderID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, recorderID)
}

// holdsRecordingRole reports whether the invoker holds a recording role,
// read from the store on every call, so a save on the panel decides the
// next start or stop with no restart.
func (r *RecordingRuntime) holdsRecordingRole(by Invoker) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancel()
	roles, err := r.st.GetRecordingRoles(ctx, r.guildID)
	if err != nil {
		captureError("Recording roles unreadable", err)
		return false, errRecordingFailed
	}
	return slices.ContainsFunc(by.Roles, func(id string) bool { return slices.Contains(roles.RoleIDs, id) }), nil
}

// channelName reads a channel's name from the cache, empty when the cache
// lacks it.
func (r *RecordingRuntime) channelName(channelID string) string {
	ch, err := r.mgr.Channel(channelID)
	if err != nil || ch == nil {
		return ""
	}
	return ch.Name
}
