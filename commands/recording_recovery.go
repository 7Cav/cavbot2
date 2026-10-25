package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// Startup recovery (spec #381, #393, #622). A recording the last process was
// in the middle of when it shut down or crashed still has its row open. The
// runtime's startup recovery closes each one as cut short, keeping what
// reached the disk, on every host with a bot store, recording on or off.
// It also builds each mix the last process went down while building
// (#622): a recording that stopped before this runtime was built, its mix
// still processing, goes to the mixer as it would at a stop.

// recordingRecoveryPoll is how often recovery reads the member list while
// it waits for it.
const recordingRecoveryPoll = 5 * time.Second

// recordingRecoveryWait is the longest recovery waits for the member list:
// as long as a list counts as on time. A list still partial by then is
// late, and Sentry hears of it.
const recordingRecoveryWait = memberListLateAfter

// recoverWhenListed runs recovery once the member list is complete with the
// bot connected, which a restart has to fetch again: a recording cut short
// names its speakers from it. The voice states have arrived by then too, so
// the cache shows where each recorder is. Once recordingRecoveryWait has
// passed it runs anyway, naming whoever the list lacks by their Discord ID,
// so no recording stays open for want of a list.
func (r *RecordingRuntime) recoverWhenListed() {
	if !listReady(r.mgr.MemberList(r.guildID)) && recordingNow().Before(r.builtAt.Add(recordingRecoveryWait)) {
		recordingAfterFunc(recordingRecoveryPoll, r.recoverWhenListed)
		return
	}
	r.recoverLastProcess()
}

// recoverLastProcess closes every recording the last process left running
// as cut short, and builds each mix it went down while building. A
// recording this process started or stopped is never one of them, even one
// a member started or stopped before recovery ran.
func (r *RecordingRuntime) recoverLastProcess() {
	defer utils.RecoverPanic("recording-recovery")
	r.disconnectIdleRecorders()
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	recs, err := r.st.ListRecordings(ctx, r.guildID)
	cancel()
	if err != nil {
		captureError("Recordings unreadable for recovery", err)
		return
	}
	members := r.mgr.MemberList(r.guildID)
	for _, rec := range recs {
		switch {
		case rec.StoppedAt.IsZero() && rec.StartedAt.Before(r.builtAt):
			r.recoverRecording(rec, members)
		case rec.Mix == store.MixProcessing && rec.StoppedAt.Before(r.builtAt):
			// The last process stopped it and went down before its mix was
			// built (#622).
			r.startMix(rec)
		}
	}
}

// disconnectIdleRecorders disconnects each recorder the cache shows in a
// voice channel with no recording of this process holding it: the last
// process left it there. With recording off there is none to check.
func (r *RecordingRuntime) disconnectIdleRecorders() {
	snap := r.mgr.VoiceStates(r.guildID)
	for _, id := range r.recorders {
		channelID := snap.channelOf(id)
		r.mu.Lock()
		_, held := r.running[id]
		r.mu.Unlock()
		if channelID == "" || held {
			continue
		}
		if err := r.mgr.GuildMemberMove(r.guildID, id, nil); err != nil {
			captureError("Recorder not disconnected at recovery", err, "recorder", id, "channel_id", channelID)
			continue
		}
		utils.Info("Recorder disconnected at recovery", "recorder", id, "channel_id", channelID)
	}
}

// recoverRecording closes one recording the last process left running as
// cut short. A track with no audio on disk goes, since ffmpeg won't mix it,
// and its speaker stays listed with their track lost.
// The recording stopped where its longest track ends: when the shutdown
// came, since a shutdown ends every track there, or a crash's last flush.
func (r *RecordingRuntime) recoverRecording(rec store.Recording, members MemberListSnapshot) {
	heard, longest, err := keepAudioTracks(r.dir, rec.ID)
	if err != nil {
		captureError("Tracks not read at recovery", err, "recording_id", rec.ID)
	}
	rec.StoppedAt = rec.StartedAt.Add(time.Duration(longest) * time.Second / opusSampleRate)
	rec.Ended, rec.Speakers = store.RecordingEndRestart, speakersOf(members, heard)
	if err := writeRecordingInfo(r.dir, rec, members.DisplayName(rec.StarterID), nil); err != nil {
		captureError("Info file not written at recovery", err, "recording_id", rec.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancel()
	if err := r.st.StopRecording(ctx, rec.ID, rec.StoppedAt, rec.Ended, rec.Speakers); err != nil {
		captureError("Recording row not closed at recovery", err, "recording_id", rec.ID)
		return
	}
	r.closeRecordingNotice(rec)
	utils.Info("Recording cut short", "recording_id", rec.ID, "channel_id", rec.ChannelID)
	r.startMix(rec)
}

// keepAudioTracks removes each of a recording's tracks that holds no audio
// on disk, such as one whose speaker was first heard after the last flush.
// It returns every speaker with a track on disk, each whose track it
// removed with their track lost, and how far the longest kept track runs,
// in 48 kHz samples.
func keepAudioTracks(dir string, id int64) (heard []store.Speaker, longest uint64, err error) {
	entries, err := os.ReadDir(recordingDir(dir, id))
	if errors.Is(err, os.ErrNotExist) {
		// Nobody was heard, so no track was opened.
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var errs []error
	for _, e := range entries {
		userID, ok := strings.CutSuffix(e.Name(), ".ogg")
		if !ok || e.IsDir() {
			continue
		}
		path := trackPath(dir, id, userID)
		end, err := trackEnd(path)
		switch {
		case err != nil:
			errs = append(errs, err)
		case end == 0:
			heard = append(heard, store.Speaker{ID: userID, TrackLost: true})
			errs = append(errs, os.Remove(path))
		default:
			heard = append(heard, store.Speaker{ID: userID})
			longest = max(longest, end)
		}
	}
	return heard, longest, errors.Join(errs...)
}

// trackEnd reads how far a track on disk runs, in 48 kHz samples. Zero
// means no audio reached the disk.
func trackEnd(path string) (uint64, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	return oggStreamEnd(f)
}
