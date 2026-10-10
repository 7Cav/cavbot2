// Package voice is the voice adapter (spec #381, ADR 0014). It owns the
// recorder sessions: one gateway session per recorder, in the bot process
// beside the main one.
package voice

import (
	"strings"

	"github.com/7cav/cavbot2/utils"
)

// Session is one recorder's gateway session.
type Session interface {
	// UserID is the recorder account's Discord user ID.
	UserID() string
	// Close ends the session, and the recorder goes offline.
	Close() error
}

// Open opens one recorder's gateway session from its token.
type Open func(token string) (Session, error)

// Recorders is the set of recorders connected at startup.
type Recorders struct {
	sessions []Session
}

// Connect opens one session per token in raw, the RECORDER_TOKENS value: a
// comma-separated list. With no token, recording is off and nothing is
// opened. A token that fails to open goes to Sentry and is skipped, so a
// bad or revoked token costs only its own recorder. The report names the
// token by its position in the list, never by its text, since a token is
// the recorder account.
func Connect(raw string, open Open) *Recorders {
	var tokens []string
	for token := range strings.SplitSeq(raw, ",") {
		if token = strings.TrimSpace(token); token != "" {
			tokens = append(tokens, token)
		}
	}
	r := &Recorders{}
	if len(tokens) == 0 {
		utils.Warn("RECORDER_TOKENS not set, recording off")
		return r
	}
	for i, token := range tokens {
		s, err := open(token)
		if err != nil {
			utils.CaptureError("Recorder failed to connect", err, "recorder", i+1)
			continue
		}
		utils.Info("Recorder connected", "recorder", i+1, "user_id", s.UserID())
		r.sessions = append(r.sessions, s)
	}
	return r
}

// UserIDs returns the connected recorders' user IDs.
func (r *Recorders) UserIDs() []string {
	ids := make([]string, 0, len(r.sessions))
	for _, s := range r.sessions {
		ids = append(ids, s.UserID())
	}
	return ids
}

// Close ends every recorder's session, at shutdown.
func (r *Recorders) Close() {
	for _, s := range r.sessions {
		if err := s.Close(); err != nil {
			utils.Warn("Recorder did not close cleanly", "user_id", s.UserID(), "error", err)
		}
	}
}
