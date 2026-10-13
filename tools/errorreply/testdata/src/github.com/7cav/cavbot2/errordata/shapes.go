package errordata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"os/exec"
	"slices"

	"github.com/7cav/cavbot2/utils"
)

// Shapes the bot's own code takes, each passing.

// track keeps the error its last write failed with, in an unexported field
// no printer or template can read.
type track struct {
	name string
	err  error
}

func LostTracks(tracks map[string]*track) {
	for _, id := range slices.Sorted(maps.Keys(tracks)) {
		if tracks[id].err != nil {
			utils.HandleError(nil, nil, "❌ Lost the track of "+id)
		}
	}
}

// exec.Cmd holds an error in its exported Err field, and running it hands
// over no error.
func Ran(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "ffmpeg")
	if err := cmd.Run(); err != nil {
		utils.Warn("ffmpeg failed", "error", err)
	}
}

// watchClose hands the library's close handler back the error it was
// handed, through a function value that returns nothing.
func watchClose(onClose func(io.Writer, error)) func(io.Writer, error) {
	return func(w io.Writer, err error) {
		onClose(w, err)
		utils.Warn("connection closed", "error", err)
	}
}

var lastFailureDigest string

// A digest of an error's text can only be compared.
func Remembered(err error) {
	sum := sha256.Sum256([]byte(err.Error()))
	lastFailureDigest = hex.EncodeToString(sum[:])
}
