// Package voice is the voice adapter (spec #381, ADR 0014). It owns the
// recorder sessions: one gateway session per recorder, in the bot process
// beside the main one. A recorder joins a voice channel through disgo's voice
// package with a dave-go DAVE session, and none of disgo's types leave this
// package.
package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	// Receive hands what the recorder hears from now on to l, until Leave:
	// every frame, one at a time, in the order they arrive, and each gap.
	Receive(l Listener)
	// Leave takes the recorder out of the channel.
	Leave(ctx context.Context)
	// OwnLeaves is how many times the recorder has left the channel on its
	// own to join it again, each counted from just before its leave goes
	// out. Each is an absence from the channel that isn't the recorder
	// leaving. The count never goes down.
	OwnLeaves() int
	// GapReport is what the recorder knows now of the gap it last told of,
	// for the recording runtime to read at the gap's 10 s mark.
	GapReport() GapReport
}

// GapReport is what the voice adapter knows of a gap when the recording
// runtime asks, at the 10 s mark that cuts the recording short.
type GapReport struct {
	// CloseCode is the code of the voice gateway close that started the
	// gap, CloseCodeNone for a read of the voice gateway that failed, and
	// empty when the gap started some other way.
	CloseCode string
	// Close is that close or failed read, nil when the gap started some
	// other way. Its data goes to the log and Sentry alone (ADR 0018).
	Close error
	// VoiceServer is the voice server Discord named last for the call,
	// empty before it named one.
	VoiceServer string
	// GatewaySessionOnline is whether the recorder's own gateway session
	// is online, so that a rejoin could go out.
	GatewaySessionOnline bool
	// Fields are the cause's own, in a fixed order. A field the cause
	// measures at the 10 s mark is measured when the report is read.
	Fields []GapField
}

// GapField is one of a gap cause's own fields. Value is a string, an int
// or a time.Duration.
type GapField struct {
	Key   string
	Value any
}

// CloseCodeNone is a gap's close code when a read of the voice gateway
// failed, rather than the voice server closing it.
const CloseCodeNone = "none"

// Listener hears a call through its recorder (GLOSSARY.md, Gap). The voice
// adapter calls it from one goroutine, one call at a time, so a call that
// blocks holds up the frames after it.
type Listener interface {
	// Frame is one frame a speaker sent.
	Frame(Frame)
	// GapStarted is the recorder ceasing to hear the channel since then:
	// its voice connection to the call dropped, or DAVE held every frame
	// past its stall limit. cause names what started the gap. GapEnded is
	// the recorder hearing it again, connected at the voice server Discord
	// named last with DAVE passing frames. The two take turns, GapStarted
	// first.
	GapStarted(since time.Time, cause GapCause)
	GapEnded()
}

// GapCause names what started a gap (GLOSSARY.md, Gap). It never changes
// for the rest of the gap, whatever trouble follows.
type GapCause string

// The causes a gap can have.
const (
	// GapVoiceGatewayClosed is the voice gateway closing or failing a read.
	GapVoiceGatewayClosed GapCause = "voice_gateway_closed"
	// GapHeartbeatOverdue is the recorder's check finding the voice
	// server's heartbeat answer overdue.
	GapHeartbeatOverdue GapCause = "heartbeat_overdue"
	// GapCallMoved is the call moving to another voice server: a 4022 for
	// a channel that still exists, or Discord naming another server for
	// the call.
	GapCallMoved GapCause = "call_moved"
	// GapDAVEStalled is the DAVE setup holding frames past its stall limit.
	GapDAVEStalled GapCause = "dave_stalled"
	// GapServerDeafened is the recorder's own voice state saying it is
	// server-deafened.
	GapServerDeafened GapCause = "server_deafened"
)

// cutShortErrors is the error each cause's cut short reaches Sentry with.
// Each opens with "voice: recording cut short:", so the cut-short issues
// sort together.
var cutShortErrors = map[GapCause]error{
	GapVoiceGatewayClosed: errors.New("voice: recording cut short: the voice gateway closed"),
	GapHeartbeatOverdue:   errors.New("voice: recording cut short: the voice server stopped answering heartbeats"),
	GapCallMoved:          errors.New("voice: recording cut short: the call moved to another voice server"),
	GapDAVEStalled:        errors.New("voice: recording cut short: the DAVE setup stalled"),
	GapServerDeafened:     errors.New("voice: recording cut short: the recorder was server-deafened"),
}

// CutShortError is the error a recording that a gap of this cause cut short
// reaches Sentry with.
func (c GapCause) CutShortError() error {
	if err, ok := cutShortErrors[c]; ok {
		return err
	}
	return fmt.Errorf("voice: recording cut short: %s", c)
}

// Frame is one Opus packet a speaker sent, as Discord delivered it, with
// both encryptions taken off.
type Frame struct {
	// UserID is the speaker's Discord user ID.
	UserID string
	// SSRC is the RTP stream the packet came on. A speaker who rejoins
	// comes back on a new one.
	SSRC uint32
	// Timestamp is the packet's RTP timestamp, in 48 kHz samples from a
	// random base each SSRC picks.
	Timestamp uint32
	// Opus is the packet's Opus payload, the caller's to keep.
	Opus []byte
}

// Opener opens one recorder's gateway session from its token. recorder is
// the token's place in the list, from 1, which names the recorder in its
// reports.
type Opener func(recorder int, token string) (GatewaySession, error)

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
		s, err := open(i+1, token)
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
