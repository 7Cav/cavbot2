package commands

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
// in the channel, and its row written after that, with the notice's ID. A
// start whose row isn't written removes the notice again. The row is closed
// with its stop time and how it ended once the recorder has left, or its
// leave has run out its deadline, and the notice edited to say it stopped.
// Its tracks (recording_tracks.go) take what the recorder hears from the
// row write on, and end at the stop time. Besides
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

// recordingLeaveTimeout bounds a recorder's leave. A stop waits for the
// leave no longer than that, so /record stop answers within about 5 s.
const recordingLeaveTimeout = 5 * time.Second

// leaveTimedOutMessage is the WARN the runtime logs when a leave runs past
// recordingLeaveTimeout. The stop, or the failed start, goes on without it.
const leaveTimedOutMessage = "Recorder leave timed out, going on without it"

// recordingStoreTimeout bounds each store call the runtime makes.
const recordingStoreTimeout = 5 * time.Second

// The reasons a start is refused. The command turns each into the member's
// reply.
var (
	// errRecordingOff: no recorder is configured, or every one is out until
	// a restart, so nobody can record.
	errRecordingOff = errors.New("recording: no recorder can record")
	// errRecordNotInVoice: the invoker is in no voice or stage channel.
	errRecordNotInVoice = errors.New("recording: invoker is not in a voice channel")
	// errRecordInHub: the invoker's channel is a hub, which nobody stays in.
	errRecordInHub = errors.New("recording: channel is a hub")
	// errNoRecordingRole: the invoker holds none of the recording roles.
	errNoRecordingRole = errors.New("recording: invoker holds no recording role")
	// errNotCavMember: the invoker holds a recording role but no rank role,
	// or holds the Discharged role, so isn't a Cav member, and the panel
	// couldn't find the recording for them.
	errNotCavMember = errors.New("recording: invoker is no Cav member")
	// errRecordTitleTooLong: the title is over recordTitleLimit characters,
	// which would push the recording notice past Discord's message limit.
	errRecordTitleTooLong = errors.New("recording: title is too long")
	// errAlreadyRecorded: a recording of the invoker's channel is starting,
	// running or stopping. A channel has one recording at a time.
	errAlreadyRecorded = errors.New("recording: channel is already being recorded")
	// errRecorderComingBack: the start took an offline recorder, which
	// didn't log back in within the join's deadline.
	errRecorderComingBack = errors.New("recording: the recorder is still logging back in")
)

// recorderStillOfflineMessage is the INFO line of a start refused because
// its offline recorder didn't log back in in time. Sentry hears nothing: a
// recorder logging back in isn't a fault.
const recorderStillOfflineMessage = "Start refused, the recorder is still logging back in"

// recorderJoinedDeafenedMessage is the INFO line of a start whose recorder
// joined server-deafened, which it lifts. Discord keeps a moderator's
// deafen of a recorder after it leaves. Sentry hears nothing, since the lift
// is routine.
const recorderJoinedDeafenedMessage = "Recorder joined deafened, lifting it"

// recorderDeafenNotLiftedMessage is the Sentry event of a start that
// couldn't lift the deafen its recorder joined with, so it failed. Each step
// of the lift that can fail files in a Sentry issue of its own.
const recorderDeafenNotLiftedMessage = "Recorder deafen not lifted"

// The steps of a deafen's lift that can fail: Discord refusing the
// undeafen, and the recorder still deafened when the start's join deadline
// ends.
const (
	deafenLiftRefused  = "undeafen_refused"
	deafenLiftTimedOut = "still_deafened"
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
// the recorders, each in one voice channel at a time, and each online,
// offline or out. *voice.Recorders implements it.
type RecorderVoice interface {
	// UserIDs returns the recorders' user IDs, fixed from startup. None
	// means recording is off.
	UserIDs() []string
	// State says whether a recorder is online, offline or out.
	State(recorderID string) voice.RecorderState
	// Join connects the recorder to a voice channel of the guild. A join of
	// an offline recorder waits for its login, and returns
	// voice.ErrRecorderOffline when ctx ends first.
	Join(ctx context.Context, recorderID, guildID, channelID string) (voice.Conn, error)
	// TakeOffline counts a recorder a gap cut short offline from now. Once
	// its leave has returned or given up, relogin has it log out and back
	// in by itself.
	TakeOffline(recorderID string) (relogin func())
}

// RecordingManager is the subset of the Discord session the recording
// runtime uses: what it reads, all of it from discordgo's state cache, and
// the calls it makes.
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
	// ChannelMessageDelete removes the recording notice of a start that
	// failed after posting it.
	ChannelMessageDelete(channelID, messageID string) error
	// MemberList is TempVCManager's: one copied snapshot of the guild's
	// member list, which names a recording's speakers at its stop.
	MemberList(guildID string) MemberListSnapshot
	// UserChannelCreate opens the DM channel with a member, for the link
	// to an ended recording (closeRecordingNotice).
	UserChannelCreate(recipientID string) (*discordgo.Channel, error)
	// GuildMemberMove is TempVCManager's. With no channel it disconnects a
	// recorder the last process left in voice.
	GuildMemberMove(guildID, userID string, channelID *string) error
	// GuildMemberDeafen is TempVCManager's. It lifts a server deafen a
	// recorder joined with.
	GuildMemberDeafen(guildID, userID string, deaf bool) error
}

// RecordingRuntime holds the recordings running now. Built on every host
// with a bot store, recording on or off: with no recorder, every start is
// refused as recording off.
type RecordingRuntime struct {
	mgr       RecordingManager
	st        store.Store
	tv        *TempVC
	voice     RecorderVoice
	mixer     Mixer
	guildID   string
	recorders []string
	// dir is the recordings directory, which holds a directory of tracks
	// per recording.
	dir string
	// panelURL is the panel's base URL, which a stopped recording's link
	// starts with. Empty when the panel is off, and then a stop gives no
	// link.
	panelURL string
	// builtAt is when the runtime was built. A recording that started
	// before it is one the last process left running, and one that stopped
	// before it with its mix unbuilt is one the last process was mixing.
	builtAt time.Time

	mu sync.Mutex
	// running maps each busy recorder's user ID to its recording.
	running map[string]*activeRecording

	// recordCommandID is /record's ID, empty until the startup sync has
	// registered the commands.
	recordCommandID atomic.Value
}

// activeRecording is one recording a recorder is busy with. conn is nil
// while its join is in flight.
type activeRecording struct {
	row    store.Recording
	conn   voice.Conn
	tracks *recordingTracks
	// cancelCap cancels the stop at the cap, and cancelDiskCheck the next
	// disk check. Both are set before the recording is running, so a stop
	// always finds them. A disk check sets cancelDiskCheck again under the
	// runtime's mutex, and only while the recording isn't stopping.
	cancelCap, cancelDiskCheck func()
	// stopping is set once a stop has taken it, so a second stop doesn't.
	stopping bool
	// recorderSeen is set once the cache has shown the recorder in the
	// channel. ownLeaves is the count of the recorder's own leaves the conn
	// last reported, and ownLeaveAt when the runtime read it go up, zero
	// before it ever has.
	recorderSeen bool
	ownLeaves    int
	ownLeaveAt   time.Time
	// gap is the gap running now, nil while the recorder hears the
	// channel. The runtime's mutex guards it.
	gap *recordingGap
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
// voice adapter, whose recorders it takes now. Tracks go under dir, and
// the mixer builds each recording's mix there. A stopped recording's link
// starts with panelURL, the panel's base URL, empty when the panel is off.
// The retention sweep (recording_retention.go) starts now, with or without
// a recorder, and so does the recovery of every recording the last process
// left running, or stopped with its mix unbuilt (recording_recovery.go).
func NewRecordingRuntime(mgr RecordingManager, st store.Store, tv *TempVC, guildID string, rv RecorderVoice, mixer Mixer, dir, panelURL string) *RecordingRuntime {
	r := &RecordingRuntime{
		mgr:       mgr,
		st:        st,
		tv:        tv,
		voice:     rv,
		mixer:     mixer,
		guildID:   guildID,
		recorders: rv.UserIDs(),
		dir:       dir,
		panelURL:  panelURL,
		builtAt:   recordingNow(),
		running:   make(map[string]*activeRecording),
	}
	recordingAfterFunc(0, r.sweepRetention)
	recordingAfterFunc(0, r.recoverWhenListed)
	return r
}

// setRecordCommandID keeps /record's ID, which the startup sync hands over
// once Discord has registered the commands.
func (r *RecordingRuntime) setRecordCommandID(id string) {
	r.recordCommandID.Store(id)
}

// Start starts a recording of the voice channel the invoker is in, with a
// free recorder, and returns the channel. With recording off it refuses
// before anything else, so the member learns that first.
func (r *RecordingRuntime) Start(by Invoker, title string) (channelID string, err error) {
	if r.recordingOff() {
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
	if !r.tv.RankLadder().CavMember(by.Roles) {
		return "", errNotCavMember
	}
	if utf8.RuneCountInString(title) > recordTitleLimit {
		return "", errRecordTitleTooLong
	}

	recorderID, err := r.reserveRecorder(channelID)
	if err != nil {
		return "", err
	}

	joinCtx, cancelJoin := context.WithTimeout(context.Background(), recordingJoinTimeout)
	defer cancelJoin()
	conn, err := r.voice.Join(joinCtx, recorderID, r.guildID, channelID)
	if errors.Is(err, voice.ErrRecorderOffline) {
		r.release(recorderID)
		utils.Info(recorderStillOfflineMessage, "recorder", recorderID, "channel_id", channelID)
		return "", errRecorderComingBack
	}
	if err != nil {
		r.release(recorderID)
		captureError("Recorder failed to join", err, "recorder", recorderID, "channel_id", channelID)
		return "", errRecordingFailed
	}
	// A recorder still deafened records silence, so the start fails as a
	// failed join does rather than tell the starter it records.
	if conn.Deafened() && !r.liftDeafen(joinCtx, conn, recorderID, channelID) {
		r.releaseWhenLeft(recorderID, r.leave(conn, recorderID, channelID))
		return "", errRecordingFailed
	}
	// The notice is how the channel learns it is recorded, so a recording
	// whose notice didn't post doesn't go on: the recorder leaves.
	noticeID, err := r.postRecordingNotice(channelID, by.UserID, title)
	if err != nil {
		r.releaseWhenLeft(recorderID, r.leave(conn, recorderID, channelID))
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
		NoticeID:    noticeID,
		StartedAt:   recordingNow(),
	}
	storeCtx, cancelStore := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancelStore()
	row, err = r.st.StartRecording(storeCtx, row)
	if err != nil {
		// A recording with no row is one the panel and the deploy gate
		// can't see, so the recorder leaves rather than record it, and the
		// notice goes with it.
		r.releaseWhenLeft(recorderID, r.leave(conn, recorderID, channelID))
		r.removeRecordingNotice(channelID, noticeID)
		captureError("Recording row not written at start", err, "recorder", recorderID, "channel_id", channelID)
		return "", errRecordingFailed
	}
	// The recorder starts handing over what it hears only now, with the
	// row written: frames before wait in the socket. A frame arrives at the
	// runtime's clock when it's handed over. It receives before a stop can
	// find it, so the stop's leave ends the receiving.
	tracks := newRecordingTracks(r.dir, row.ID, row.StartedAt)
	rec := &activeRecording{row: row, conn: conn, tracks: tracks}
	conn.Receive(recordingListener{r, rec})
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

// liftDeafen lifts the server deafen a recorder joined with, through the
// bot's own session, and reports whether the recorder's connection said it
// is undeafened before ctx ended. Discord keeps a moderator's deafen of a
// recorder after it leaves, so a deafen from an earlier recording would have
// this one record silence. The bot never lifts a deafen put on during a
// recording, since a person chose it. A lift that fails goes to Sentry, in
// the issue of the step that failed: Discord refusing the undeafen, or the
// recorder still deafened at ctx's end.
func (r *RecordingRuntime) liftDeafen(ctx context.Context, conn voice.Conn, recorderID, channelID string) bool {
	utils.Info(recorderJoinedDeafenedMessage, "recorder", recorderID, "channel_id", channelID)
	step, err := deafenLiftRefused, r.mgr.GuildMemberDeafen(r.guildID, recorderID, false)
	if err == nil {
		step, err = deafenLiftTimedOut, conn.WaitUndeafened(ctx)
	}
	if err == nil {
		return true
	}
	issue := utils.SentryIssue{Fingerprint: []string{recorderDeafenNotLiftedMessage, step}}
	utils.CaptureErrorIn(issue, recorderDeafenNotLiftedMessage, err, "recorder", recorderID, "channel_id", channelID)
	return false
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

	r.finish(target, store.RecordingEndStopped, by.UserID, recordingNow())
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

	r.finish(target, store.RecordingEndStopped, by.UserID, recordingNow())
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

// finish ends a recording a stop took at stoppedAt: the tracks end at the
// stop time, so nothing the recorder hands over after is kept, then the
// recorder leaves, the row records the stop time, how it ended and its
// speakers, the recorder is free again once its leave has returned, the
// notice says it stopped, and the mix starts. stoppedBy is the member who
// stopped it, empty when it stopped by itself.
func (r *RecordingRuntime) finish(target *activeRecording, end store.RecordingEnd, stoppedBy string, stoppedAt time.Time) {
	r.cancelTimers(target)
	r.endGapAtStop(target, stoppedAt)
	endTracks(target, stoppedAt)
	returned := r.leave(target.conn, target.row.RecorderID, target.row.ChannelID)
	row := r.closeRow(target, stoppedAt, end)
	r.releaseWhenLeft(row.RecorderID, returned)
	r.closeRecordingNotice(row)
	logRecordingStopped(row, stoppedBy)
	r.startMix(row)
}

// endTracks ends a stopped recording's tracks at stoppedAt. Frames the
// recorder hands over after are dropped.
func endTracks(target *activeRecording, stoppedAt time.Time) {
	if err := target.tracks.close(stoppedAt); err != nil {
		captureError("Tracks not closed at stop", err, "recording_id", target.row.ID)
	}
}

// closeRow writes the info file of a stopped recording whose tracks have
// ended, and closes its row with the stop time, how it ended and the
// speakers its tracks heard. It returns the row as closed.
func (r *RecordingRuntime) closeRow(target *activeRecording, stoppedAt time.Time, end store.RecordingEnd) store.Recording {
	row := target.row
	members := r.mgr.MemberList(r.guildID)
	row.StoppedAt, row.Ended, row.Speakers = stoppedAt, end, speakersOf(members, target.tracks.heard())
	if err := writeRecordingInfo(r.dir, row, members.DisplayName(row.StarterID)); err != nil {
		captureError("Info file not written", err, "recording_id", row.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancel()
	if err := r.st.StopRecording(ctx, row.ID, stoppedAt, end, row.Speakers); err != nil {
		captureError("Recording row not closed at stop", err, "recording_id", row.ID)
	}
	return row
}

// recordingStoppedMessage is the INFO line of every recording a stop or a
// gap ends, with how it ended.
const recordingStoppedMessage = "Recording stopped"

// logRecordingStopped logs a stopped recording's INFO line. stoppedBy is the
// member who stopped it, empty when it stopped by itself.
func logRecordingStopped(row store.Recording, stoppedBy string) {
	utils.Info(recordingStoppedMessage, "recording_id", row.ID, "channel_id", row.ChannelID,
		"recorder", row.RecorderID, "ended", string(row.Ended), "stopped_by", stoppedBy)
}

// cancelTimers cancels a recording a stop or the shutdown has taken: its
// stop at the cap and its next disk check.
func (r *RecordingRuntime) cancelTimers(rec *activeRecording) {
	r.mu.Lock()
	cancelDiskCheck := rec.cancelDiskCheck
	r.mu.Unlock()
	rec.cancelCap()
	cancelDiskCheck()
}

// speakersOf names everyone a recording heard by their display name in the
// member list, in the order of their names.
func speakersOf(members MemberListSnapshot, heard []store.Speaker) []store.Speaker {
	out := make([]store.Speaker, 0, len(heard))
	for _, sp := range heard {
		sp.DisplayName = members.DisplayName(sp.ID)
		out = append(out, sp)
	}
	slices.SortFunc(out, func(a, b store.Speaker) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)), strings.Compare(a.ID, b.ID))
	})
	return out
}

// recordingOff reports whether no recorder can record: none is configured,
// or every one is out until a restart.
func (r *RecordingRuntime) recordingOff() bool {
	return !slices.ContainsFunc(r.recorders, func(id string) bool { return r.voice.State(id) != voice.RecorderOut })
}

// reserveRecorder marks a free recorder busy with a recording of the
// channel whose join is about to go out, and returns it: the first free
// online one in configured order, or else the first free offline one,
// which the join waits for. Out recorders are skipped. A channel already
// being recorded is refused before any recorder is looked at. A recording
// counts from its reservation until its recorder is released at the end of
// its stop, its join in flight or not, so two starts in one channel can't
// both pass. A stop whose leave ran past its deadline releases the recorder
// only once that leave returns. A recording a gap cut short stops counting
// at the cut short, for its channel and its recorder both, and the recorder
// is offline from then until it logs back in.
func (r *RecordingRuntime) reserveRecorder(channelID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.running {
		if rec.row.ChannelID == channelID {
			return "", errAlreadyRecorded
		}
	}
	busy := &noFreeRecorderError{}
	offline := ""
	for _, id := range r.recorders {
		if rec, ok := r.running[id]; ok {
			busy.ChannelIDs = append(busy.ChannelIDs, rec.row.ChannelID)
			continue
		}
		switch r.voice.State(id) {
		case voice.RecorderOnline:
			r.reserveLocked(id, channelID)
			return id, nil
		case voice.RecorderOffline:
			if offline == "" {
				offline = id
			}
		}
	}
	switch {
	case offline != "":
		r.reserveLocked(offline, channelID)
		return offline, nil
	case len(busy.ChannelIDs) == 0:
		// Every recorder went out since the start's first check.
		return "", errRecordingOff
	}
	return "", busy
}

// reserveLocked marks a recorder busy with a recording of the channel whose
// join is about to go out. Caller holds mu.
func (r *RecordingRuntime) reserveLocked(recorderID, channelID string) {
	r.running[recorderID] = &activeRecording{row: store.Recording{ChannelID: channelID}}
}

// leave takes a recorder out of its channel, and waits no longer than
// recordingLeaveTimeout, the deadline it hands the leave. When a leave runs
// past it, as one frozen in the voice adapter does, leave logs a WARN and
// returns to its caller. The leave finishes on its own goroutine, if ever,
// and returned closes once it has.
func (r *RecordingRuntime) leave(conn voice.Conn, recorderID, channelID string) (returned <-chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), recordingLeaveTimeout)
	defer cancel()
	done := make(chan struct{})
	// left carries whether the leave ran past its deadline, judged the
	// moment the leave returns rather than when this goroutine next runs.
	left := make(chan bool, 1)
	go func() {
		late := false
		// done closes before the verdict goes out, so a caller that has
		// read a leave in time finds its recorder free to release.
		defer func() { left <- late }()
		defer close(done)
		defer utils.RecoverPanic("recording-leave", "recorder", recorderID, "channel_id", channelID)
		conn.Leave(ctx)
		late = ctx.Err() != nil
	}()
	late := true
	select {
	case late = <-left:
	case <-ctx.Done():
		// A leave that returned in time can still be waiting to be read.
		select {
		case late = <-left:
		default:
		}
	}
	if late {
		utils.Warn(leaveTimedOutMessage, "recorder", recorderID, "channel_id", channelID)
	}
	return done
}

// release frees a recorder.
func (r *RecordingRuntime) release(recorderID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, recorderID)
}

// releaseWhenLeft frees a recorder once its leave has returned: at once
// after a leave in time, and later after one that ran past its deadline.
// Until then the recorder stays busy, since the voice adapter can't join it
// again while it is still closing its connection.
func (r *RecordingRuntime) releaseWhenLeft(recorderID string, returned <-chan struct{}) {
	select {
	case <-returned:
		r.release(recorderID)
	default:
		go func() {
			<-returned
			r.release(recorderID)
		}()
	}
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

// Shutdown closes every running recording's tracks at SIGTERM, so what was
// captured up to now is on disk within Docker's 10 s. Each row stays open
// and each notice as it is: the next process closes the recording as cut
// short (recording_recovery.go). main.go calls it before anything else
// closes.
func (r *RecordingRuntime) Shutdown() {
	r.mu.Lock()
	var taken []*activeRecording
	for _, rec := range r.running {
		if rec.live() {
			rec.stopping = true
			taken = append(taken, rec)
		}
	}
	r.mu.Unlock()
	now := recordingNow()
	for _, rec := range taken {
		r.cancelTimers(rec)
		if err := rec.tracks.close(now); err != nil {
			captureError("Tracks not closed at shutdown", err, "recording_id", rec.row.ID)
		}
	}
	utils.Info("Recordings closed for shutdown", "count", len(taken))
}
