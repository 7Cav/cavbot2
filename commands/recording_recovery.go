package commands

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// Cut-short recordings (spec #381, #393). A recording the last process was
// in the middle of when it shut down or crashed still has its row open. The
// runtime's startup recovery closes each one as cut short, keeping what
// reached the disk, on every host with a bot store, recording on or off.

// recoverCutShort closes every recording the last process left running as
// cut short. A recording this process started is never one of them, even
// one a member started before recovery ran.
func (r *RecordingRuntime) recoverCutShort() {
	defer utils.RecoverPanic("recording-recovery")
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	recs, err := r.st.ListRecordings(ctx, r.guildID)
	cancel()
	if err != nil {
		captureError("Recordings unreadable for recovery", err)
		return
	}
	for _, rec := range recs {
		if rec.StoppedAt.IsZero() && rec.StartedAt.Before(r.builtAt) {
			r.recoverRecording(rec)
		}
	}
}

// recoverRecording closes one recording the last process left running as
// cut short. A track with no audio on disk goes, since ffmpeg won't mix it.
// The recording stopped where its longest track ends: when the shutdown
// came, since a shutdown ends every track there, or a crash's last flush.
func (r *RecordingRuntime) recoverRecording(rec store.Recording) {
	_, longest, err := keepAudioTracks(r.dir, rec.ID)
	if err != nil {
		captureError("Tracks not read at recovery", err, "recording_id", rec.ID)
	}
	stoppedAt := rec.StartedAt.Add(time.Duration(longest) * time.Second / opusSampleRate)
	ctx, cancel := context.WithTimeout(context.Background(), recordingStoreTimeout)
	defer cancel()
	if err := r.st.StopRecording(ctx, rec.ID, stoppedAt, store.RecordingEndCutShort, nil); err != nil {
		captureError("Recording row not closed at recovery", err, "recording_id", rec.ID)
		return
	}
	rec.StoppedAt, rec.Ended = stoppedAt, store.RecordingEndCutShort
	r.closeRecordingNotice(rec)
	utils.Info("Recording cut short", "recording_id", rec.ID, "channel_id", rec.ChannelID)
}

// keepAudioTracks removes each of a recording's tracks that holds no audio
// on disk, such as one whose speaker was first heard after the last flush.
// It returns the user ID of each speaker whose track it kept, and how far
// the longest runs, in 48 kHz samples.
func keepAudioTracks(dir string, id int64) (userIDs []string, longest uint64, err error) {
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
			errs = append(errs, os.Remove(path))
		default:
			userIDs = append(userIDs, userID)
			longest = max(longest, end)
		}
	}
	return userIDs, longest, errors.Join(errs...)
}

// trackEnd reads how far a track on disk runs, in 48 kHz samples: the
// granule position of its last whole page. A crash can leave the page it
// was writing cut short, and that page doesn't count. Zero means no audio
// reached the disk.
func trackEnd(path string) (uint64, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	br := bufio.NewReader(f)
	var end uint64
	hdr := make([]byte, 27)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			return end, nil
		}
		if string(hdr[:4]) != "OggS" {
			return end, nil
		}
		lacing := make([]byte, hdr[26])
		if _, err := io.ReadFull(br, lacing); err != nil {
			return end, nil
		}
		size := 0
		for _, l := range lacing {
			size += int(l)
		}
		if n, _ := br.Discard(size); n < size {
			return end, nil
		}
		end = binary.LittleEndian.Uint64(hdr[6:14])
	}
}
