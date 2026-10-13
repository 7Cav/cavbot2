package errordata

import (
	"errors"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// wrapped keeps a Discord error and says what it says, so its text is the
// Discord error's.
type wrapped struct {
	cause *discordgo.RESTError
}

func (w *wrapped) Error() string { return "lookup failed: " + w.cause.Error() }

func WrappedReply(err error) {
	var w *wrapped
	if errors.As(err, &w) {
		utils.HandleError(nil, nil, w.Error()) // want ""
	}
}

func WrappedLogged(err error) {
	var w *wrapped
	if errors.As(err, &w) {
		utils.Warn("lookup failed", "error", w)
	}
}

// notFound says no member matched what the member typed, in fixed text.
type notFound struct {
	Query string
}

func (e *notFound) Error() string { return "❌ No member found matching '" + e.Query + "'" }

func NotFoundReply(err error) {
	var nf *notFound
	if errors.As(err, &nf) {
		utils.HandleError(nil, nil, nf.Error())
		utils.HandleError(nil, nil, "❌ Try a mention instead of "+nf.Query)
	}
}

func MethodExpression(err error) {
	var w *wrapped
	if errors.As(err, &w) {
		f := (*wrapped).Error
		utils.HandleError(nil, nil, f(w)) // want ""
	}
}
