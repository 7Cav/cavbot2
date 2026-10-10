// Package voice is the voice adapter (spec #381, ADR 0014). It owns the
// recorder sessions: one gateway session per recorder, in the bot process
// beside the main one. A recorder joins a voice channel through disgo's voice
// package with a dave-go DAVE session, and none of disgo's types leave this
// package.
package voice

import (
	"context"
	"fmt"
	"strings"

	"github.com/7cav/cavbot2/utils"
)

// GatewaySession is one recorder's gateway session.
type GatewaySession interface {
	// UserID is the recorder account's Discord user ID.
	UserID() string
	// Join connects the recorder to a voice channel of the guild, and
	// returns once Discord has let it in, or with ctx's error.
	Join(ctx context.Context, guildID, channelID string) (Conn, error)
	// Close ends the session, and the recorder goes offline.
	Close() error
}

// Conn is one recorder's connection to a voice channel.
type Conn interface {
	// Leave takes the recorder out of the channel.
	Leave(ctx context.Context)
}

// Opener opens one recorder's gateway session from its token.
type Opener func(token string) (GatewaySession, error)

// Recorders is the set of recorders connected at startup.
type Recorders struct {
	sessions []GatewaySession
}

// Connect opens one session per token in raw, the RECORDER_TOKENS value: a
// comma-separated list. With no token, recording is off and nothing is
// opened. A token that fails to open goes to Sentry and is skipped, so a
// bad or revoked token costs only its own recorder. The report names the
// token by its position in the list, never by its text, since a token is
// the recorder account.
func Connect(raw string, open Opener) *Recorders {
	var tokens []string
	// strings.Split, not SplitSeq: the gate's error data check can't follow
	// a value out of a range-over-func loop, and every recorder session is
	// built from a token.
	for _, token := range strings.Split(raw, ",") {
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

// Join connects the recorder with the given user ID to a voice channel of
// the guild. A recorder is in one voice channel at a time, so the caller
// joins one it isn't already recording with.
func (r *Recorders) Join(ctx context.Context, recorderID, guildID, channelID string) (Conn, error) {
	for _, s := range r.sessions {
		if s.UserID() == recorderID {
			return s.Join(ctx, guildID, channelID)
		}
	}
	return nil, fmt.Errorf("recorder %s is not connected", recorderID)
}

// Close ends every recorder's session, at shutdown.
func (r *Recorders) Close() {
	for _, s := range r.sessions {
		if err := s.Close(); err != nil {
			utils.Warn("Recorder did not close cleanly", "user_id", s.UserID(), "error", err)
		}
	}
}
