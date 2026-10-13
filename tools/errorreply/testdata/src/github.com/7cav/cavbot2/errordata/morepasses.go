package errordata

import (
	"errors"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

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
