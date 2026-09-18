package commands

import (
	"bytes"
	"errors"
	"net/http"

	"github.com/bwmarrin/discordgo"
)

// Classification of Discord errors on the spawned channel paths (create,
// move-into, delete). The warden classifier in warden_errors.go is not used
// here: its not-found branch pages on Unknown Channel, and for a spawned
// channel "already gone" is the normal end of a hand delete. Nothing here
// changes the warden paths.

// categoryCapMarker is the field error code Discord puts under parent_id when
// a category already holds its 50 channels. Discord documents the limit but
// gives it no application error code of its own: the response is HTTP 400,
// code 50035 Invalid Form Body, with this marker in the errors object.
// discordgo does not decode field errors, so the raw body is searched.
const categoryCapMarker = "CHANNEL_PARENT_MAX_CHANNELS"

// spawnedChannelFault is what a Discord error on a spawned channel call means
// to the runtime. Each field is one fact; the call sites decide what to do
// with them, because the create and delete paths do not agree on which faults
// reach Sentry: a 429 captures on a create and is a WARN line on a delete.
type spawnedChannelFault struct {
	// gone is Unknown Channel (10003): the channel no longer exists.
	gone bool
	// full is the category or guild channel cap: the area has no room.
	full bool
	// rateLimited is a 429. The call was not retried.
	rateLimited bool
	// forbidden is a 403: a category the bot cannot see, or a permission it
	// lost. Administrator prevents it, so it is a setting to fix.
	forbidden bool
	// hardFault is a 403, a 5xx, or no HTTP response at all (transport):
	// the faults that reach Sentry on every spawned channel path.
	hardFault bool
}

// classifySpawnedChannelError reads the facts off a Discord error. An error
// with no structured HTTP response is a transport failure.
func classifySpawnedChannelError(err error) spawnedChannelFault {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr.Response == nil {
		return spawnedChannelFault{hardFault: true}
	}
	code := 0
	if restErr.Message != nil {
		code = restErr.Message.Code
	}
	status := restErr.Response.StatusCode
	forbidden := status == http.StatusForbidden
	return spawnedChannelFault{
		gone: code == discordgo.ErrCodeUnknownChannel,
		full: code == discordgo.ErrCodeMaximumNumberOfGuildChannelsReached ||
			(code == discordgo.ErrCodeInvalidFormBody && bytes.Contains(restErr.ResponseBody, []byte(categoryCapMarker))),
		rateLimited: status == http.StatusTooManyRequests,
		forbidden:   forbidden,
		hardFault:   forbidden || status >= 500,
	}
}

// capturesOnCreate reports whether a create or move-into failure with these
// facts reaches Sentry: the cap, 403, 429, 5xx, transport. Any other 4xx is a
// WARN line.
func (f spawnedChannelFault) capturesOnCreate() bool {
	return f.full || f.rateLimited || f.hardFault
}

// capturesOnDelete reports whether a delete failure with these facts reaches
// Sentry: 403, 5xx, transport. A 429 and any other 4xx is a WARN line.
func (f spawnedChannelFault) capturesOnDelete() bool {
	return f.hardFault
}

// capturesOnRename is the delete rule again: 403, 5xx, transport. A 429 is
// the limit the runtime counts against itself, so it is a WARN line only.
func (f spawnedChannelFault) capturesOnRename() bool {
	return f.hardFault
}

// cause is the spawn failure cause a create failure with these facts records.
func (f spawnedChannelFault) cause() SpawnFailureCause {
	switch {
	case f.full:
		return SpawnFailureFull
	case f.rateLimited:
		return SpawnFailureRateLimited
	case f.forbidden:
		return SpawnFailureForbidden
	default:
		return SpawnFailureDiscordError
	}
}
