package utils

import (
	"errors"

	"github.com/bwmarrin/discordgo"
)

// OpenSession opens the gateway connection for dg and reports an error unless
// the session actually reached READY.
//
// dg.Open() returning nil is not enough on its own. It reads exactly one frame
// after sending IDENTIFY and, if that frame is not READY/RESUMED, logs a
// warning it considers non-fatal and returns nil anyway (discordgo
// wsapi.go:178-182). The session then carries no State.User, and every caller
// that reads dg.State.User.ID — command registration does, three times — takes
// a nil dereference instead of a readable startup failure.
//
// Note which failures do *not* come through here: a gateway close, including
// 4004 (bad token) and 4014 (disallowed intent), makes the read fail and
// Open() reports it directly. Those already fail readably. What reaches the
// check below is the case where the gateway answered with something other than
// READY — Op 9 Invalid Session, say, which Discord sends when it will not
// start a session for this IDENTIFY.
func OpenSession(dg *discordgo.Session) error {
	if err := dg.Open(); err != nil {
		return err
	}

	if dg.State.User == nil {
		return errors.New("gateway session opened but never became ready: no READY received, so the bot user is unknown. Set DISCORDGO_LOG_LEVEL=WARN and restart to log the frame the gateway sent instead")
	}

	return nil
}
