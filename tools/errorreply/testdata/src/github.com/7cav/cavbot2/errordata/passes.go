package errordata

import (
	"errors"
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Each function here sends an error's data only where it may go, so the
// check reports none of them.

func Logged(err error) {
	utils.Warn("lookup failed", "error", err)
	utils.HandleError(nil, nil, "❌ Lookup failed.")
}

func Captured(err error) {
	utils.CaptureError("lookup failed", err, "detail", err.Error())
}

var captureError = utils.CaptureError

func CapturedThroughVar(err, editErr error) {
	captureError("delivery failed", err, "edit_error", editErr)
}

func Wrapped(err error) error {
	return fmt.Errorf("lookup failed: %w", err)
}

func Crashed(err error) {
	panic(err)
}

func Matched(err error) {
	msg := "❌ Something went wrong."
	if strings.Contains(err.Error(), "Missing Permissions") {
		msg = "❌ I don't have permission to do that."
	}
	utils.HandleError(nil, nil, msg)
}

func Classified(err error) {
	var restErr *discordgo.RESTError
	msg := "❌ Something went wrong."
	if errors.As(err, &restErr) && restErr.Response != nil {
		code := 0
		if restErr.Message != nil {
			code = restErr.Message.Code
		}
		if restErr.Response.StatusCode == 403 || code == 50013 {
			msg = "❌ I don't have permission to do that."
		}
	}
	utils.HandleError(nil, nil, msg)
}

func Waited(err error) {
	var rl *discordgo.RateLimitError
	if errors.As(err, &rl) {
		utils.HandleError(nil, nil, "❌ Try again in "+rl.RetryAfter.String())
	}
}

var errGone = errors.New("gone")

func Gone(err error) {
	if errors.Is(err, errGone) {
		utils.HandleError(nil, nil, "❌ That channel is gone.")
	}
}
