package errordata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"os/exec"
	"slices"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
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

func LoggedInSteps(name string, n int, err error) {
	kv := append([]any(nil), "error", err)
	utils.Warn("lookup failed", kv...)

	steps := append([]any(nil), "command", name)
	steps = append(steps, "error", err)
	utils.Warn("lookup failed", steps...)

	made := make([]any, 0, n)
	made = append(made, "error", err)
	utils.Warn("lookup failed", made...)
}

var permissionReplies = map[int]string{
	50013: "❌ I don't have permission to do that.",
}

func PickedByCode(err error) {
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Message != nil {
		utils.HandleError(nil, nil, permissionReplies[restErr.Message.Code])
	}
}

var statusReplies = []string{"", "", "", "", "❌ Discord refused that.", "❌ Discord is having trouble."}

func PickedByStatus(err error) {
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Response != nil {
		utils.HandleError(nil, nil, statusReplies[restErr.Response.StatusCode/100])
	}
}

func Joined(err, closeErr error) {
	utils.Warn("shutdown failed", "error", errors.Join(err, closeErr))
}

type chain struct {
	Next *chain
	Name string
}

func LoggedChain(c *chain) {
	utils.Warn("chain", "head", c)
}
