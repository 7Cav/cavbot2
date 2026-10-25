// Package voice is the voice adapter (spec #381, ADR 0014). It owns the
// recorder sessions: one gateway session at a time per recorder, in the bot
// process beside the main one. A recorder joins a voice channel through disgo's voice
// package with a dave-go DAVE session, and none of disgo's types leave this
// package.
package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	// Close ends the session: the recorder logs out of this login.
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
	// Close is that close or failed read when the cause is
	// GapVoiceGatewayClosed, one of that cause's own fields, and nil
	// otherwise. Its data goes to the log and Sentry alone (ADR 0018).
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
	// Steps is the recovery steps the recorder took since the gap's start,
	// the last stepsKept of them: those that ended, in the order they
	// started, then those still under way, so the last says what was still
	// under way when the report was read. EarlierSteps counts the steps
	// before them.
	Steps        []RecoveryStep
	EarlierSteps int
}

// RecoveryStep is one step the recorder took during a gap to hear the
// channel again.
type RecoveryStep struct {
	Kind StepKind
	// Token is the voice server token a dial dialed with, empty for any
	// other kind of step.
	Token DialToken
	// At is when the step started, from the gap's start.
	At time.Duration
	// Outcome is how the step ended, StepUnderWay while it hadn't.
	Outcome StepOutcome
	// CloseCode is the code of the voice gateway close that ended the
	// step, empty when no close did.
	CloseCode string
}

// StepKind names a kind of recovery step.
type StepKind string

// The kinds of recovery step.
const (
	// StepRejoin is a rejoin of the call's channel, after its voice
	// connection dropped.
	StepRejoin StepKind = "rejoin"
	// StepVoiceStateUpdate is a voice state update that couldn't go out,
	// as while the recorder's own gateway session is offline.
	StepVoiceStateUpdate StepKind = "voice_state_update"
	// StepDial is a dial of a voice server.
	StepDial StepKind = "dial"
	// StepLeaveToJoinAgain is the leave that follows a voice server
	// refusing the token the recorder held, so that it joins again for a
	// new one.
	StepLeaveToJoinAgain StepKind = "leave_to_join_again"
	// StepFreshConnection is a fresh voice connection, which starts DAVE
	// over.
	StepFreshConnection StepKind = "fresh_connection"
)

// DialToken says which voice server token a dial dialed with.
type DialToken string

const (
	// TokenHeld is a token the recorder has connected with before, and
	// TokenNew one it never has.
	TokenHeld DialToken = "held"
	TokenNew  DialToken = "new"
)

// StepOutcome is how a recovery step ended.
type StepOutcome string

// The outcomes of a recovery step.
const (
	StepSucceeded StepOutcome = "succeeded"
	StepFailed    StepOutcome = "failed"
	// StepRefused is a voice server refusing the token or session a dial
	// identified with.
	StepRefused  StepOutcome = "refused"
	StepUnderWay StepOutcome = "under_way"
)

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

// cutShortOpening opens the error of every cut short, so the cut-short
// issues sort together in Sentry.
const cutShortOpening = "voice: recording cut short: "

// cutShortErrors is the error each cause's cut short reaches Sentry with.
var cutShortErrors = map[GapCause]error{
	GapVoiceGatewayClosed: errors.New(cutShortOpening + "the voice gateway closed"),
	GapHeartbeatOverdue:   errors.New(cutShortOpening + "the voice server stopped answering heartbeats"),
	GapCallMoved:          errors.New(cutShortOpening + "the call moved to another voice server"),
	GapDAVEStalled:        errors.New(cutShortOpening + "the DAVE setup stalled"),
	GapServerDeafened:     errors.New(cutShortOpening + "the recorder was server-deafened"),
}

// CutShortError is the error a recording that a gap of this cause cut short
// reaches Sentry with.
func (c GapCause) CutShortError() error {
	if err, ok := cutShortErrors[c]; ok {
		return err
	}
	return errors.New(cutShortOpening + string(c))
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

// Opener reaches Discord as a recorder account, by the account's token.
type Opener interface {
	// Open is one login: it opens a new gateway session for the recorder.
	// recorder is the token's place in the list, from 1, which names the
	// recorder in its reports.
	Open(recorder int, token string) (GatewaySession, error)
	// UserID reads the account's user ID from Discord's REST API, with no
	// gateway session.
	UserID(ctx context.Context, token string) (string, error)
}

// RecorderState is whether a recorder can join a channel now (GLOSSARY.md,
// Offline recorder).
type RecorderState string

// The states a recorder is in.
const (
	// RecorderOnline is logged in and ready.
	RecorderOnline RecorderState = "online"
	// RecorderOffline is logged out and logging back in by itself.
	RecorderOffline RecorderState = "offline"
	// RecorderOut is out until a restart: no login of its token can work.
	RecorderOut RecorderState = "out"
)

// ErrTokenRefused is a login Discord refused the recorder's token for. No
// login of that token can work until it is reset, and the bot restarted.
var ErrTokenRefused = errors.New("voice: Discord refused the recorder's token")

// ErrRecorderOffline is a join of an offline recorder whose context ended
// before the recorder logged back in.
var ErrRecorderOffline = errors.New("voice: the recorder is still logging back in")

// loginRefusedMessage is the WARN a recorder logs once when Discord refuses
// its token. It is out until a restart.
const loginRefusedMessage = "Discord refused the recorder's token, recorder out until a restart"

// recorderFailedToConnectMessage is the Sentry event of a recorder whose
// first login and user ID read both failed at startup.
const recorderFailedToConnectMessage = "Recorder failed to connect"

// loginRetryFirst is how long a recorder waits after its first failed login
// before it tries again.
const loginRetryFirst = time.Second

// loginRetryMax is the longest a recorder waits between two logins.
const loginRetryMax = 600 * time.Second

// loginFailedMessage is the WARN each failed login of a recorder logs, with
// how long it waits before the next.
const loginFailedMessage = "Recorder login failed, trying again"

// nextLoginWait is how long a recorder waits after a failed login that it
// tried wait after the one before: loginRetryFirst after its first, and
// then each wait double the last, up to loginRetryMax.
func nextLoginWait(wait time.Duration) time.Duration {
	if wait == 0 {
		return loginRetryFirst
	}
	return min(2*wait, loginRetryMax)
}

// logoutTimeout bounds the logout before a recorder logs back in. The login
// waits for the logout no longer than that.
const logoutTimeout = 5 * time.Second

// logoutTimedOutMessage is the WARN a logout that runs past logoutTimeout
// logs. The login goes on without it.
const logoutTimedOutMessage = "Recorder logout timed out, logging in again without it"

// userIDReadTimeout bounds the read of a recorder's user ID.
const userIDReadTimeout = 5 * time.Second

// Recorders is the recorders configured at startup, each online, offline or
// out.
type Recorders struct {
	open Opener
	// closed closes at shutdown, which stops every login.
	closed chan struct{}

	mu        sync.Mutex
	recorders []*recorder
	shutDown  bool
	// changed closes, and is replaced, whenever a recorder comes online or
	// goes out, which a join waiting on an offline one reads.
	changed chan struct{}
}

// recorder is one recorder account. Recorders' mutex guards state and
// session.
type recorder struct {
	place  int
	token  string
	userID string

	state RecorderState
	// session is the recorder's login: the one up while it is online, and
	// the one it is about to log out of while it is offline before its
	// relogin. Nil otherwise.
	session GatewaySession
}

// Connect makes one login per token in raw, the RECORDER_TOKENS value: a
// comma-separated list. With no token, recording is off and nothing is
// opened. Connect waits for each recorder's first login alone. A recorder
// whose first login fails is offline, under the user ID Discord's REST API
// gives for its token, and goes on logging in in the background. One whose
// user ID can't be read either is out, and goes to Sentry, so a bad or
// revoked token costs only its own recorder. The report names the token by
// its position in the list, never by its text, since a token is the
// recorder account.
func Connect(raw string, open Opener) *Recorders {
	var tokens []string
	for _, token := range strings.Split(raw, ",") {
		if token = strings.TrimSpace(token); token != "" {
			tokens = append(tokens, token)
		}
	}
	r := &Recorders{open: open, closed: make(chan struct{}), changed: make(chan struct{})}
	if len(tokens) == 0 {
		utils.Warn("RECORDER_TOKENS not set, recording off")
		return r
	}
	for i, token := range tokens {
		rec := &recorder{place: i + 1, token: token}
		s, err := open.Open(rec.place, token)
		if err == nil {
			rec.userID, rec.state, rec.session = s.UserID(), RecorderOnline, s
			utils.Info("Recorder connected", "recorder", rec.place, "user_id", rec.userID)
			r.recorders = append(r.recorders, rec)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), userIDReadTimeout)
		userID, readErr := open.UserID(ctx, token)
		cancel()
		if readErr != nil {
			utils.CaptureError(recorderFailedToConnectMessage, fmt.Errorf("login: %w; user ID read: %w", err, readErr), "recorder", rec.place)
			continue
		}
		rec.userID, rec.state = userID, RecorderOffline
		r.recorders = append(r.recorders, rec)
		wait := nextLoginWait(0)
		rec.loginFailed(wait, err)
		go r.logIn(rec, wait)
	}
	return r
}

// logIn logs an offline recorder in, trying first after wait and again
// after each failed login, until shutdown. A login that goes through after
// shutdown is logged out again.
func (r *Recorders) logIn(rec *recorder, wait time.Duration) {
	defer utils.RecoverPanic("recorder-login", "recorder", rec.place)
	for {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-r.closed:
			timer.Stop()
			return
		}
		s, err := r.open.Open(rec.place, rec.token)
		if errors.Is(err, ErrTokenRefused) {
			r.mu.Lock()
			r.setStateLocked(rec, RecorderOut, nil)
			r.mu.Unlock()
			utils.Warn(loginRefusedMessage, "recorder", rec.place, "user_id", rec.userID, "error", err)
			return
		}
		if err != nil {
			wait = nextLoginWait(wait)
			rec.loginFailed(wait, err)
			continue
		}
		r.mu.Lock()
		if r.shutDown {
			r.mu.Unlock()
			r.logOut(rec, s)
			return
		}
		r.setStateLocked(rec, RecorderOnline, s)
		r.mu.Unlock()
		utils.Info("Recorder connected", "recorder", rec.place, "user_id", rec.userID)
		return
	}
}

// setStateLocked puts a recorder in state with its login s, and wakes every
// join waiting on a change. Caller holds mu.
func (r *Recorders) setStateLocked(rec *recorder, state RecorderState, s GatewaySession) {
	rec.state, rec.session = state, s
	close(r.changed)
	r.changed = make(chan struct{})
}

// TakeOffline counts the recorder with the given user ID offline from now,
// for a recording a gap cut short, so a join waits for its next login. The
// caller calls relogin once the recorder's leave has returned or given up,
// and the recorder then logs out and back in by itself. Only the first call
// of relogin does anything.
func (r *Recorders) TakeOffline(recorderID string) (relogin func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.find(recorderID)
	if rec == nil || rec.state != RecorderOnline {
		return func() {}
	}
	rec.state = RecorderOffline
	var once sync.Once
	return func() {
		once.Do(func() { go r.logBackIn(rec) })
	}
}

// logBackIn logs a recorder TakeOffline took offline out of its old login,
// unless shutdown has already, then in again.
func (r *Recorders) logBackIn(rec *recorder) {
	defer utils.RecoverPanic("recorder-relogin", "recorder", rec.place)
	r.mu.Lock()
	old := rec.session
	rec.session = nil
	r.mu.Unlock()
	if old != nil {
		r.logOut(rec, old)
	}
	r.logIn(rec, 0)
}

// logOut ends one of a recorder's logins, and waits for it no longer than
// logoutTimeout. A logout that runs past it finishes on its own goroutine,
// if ever.
func (r *Recorders) logOut(rec *recorder, s GatewaySession) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer utils.RecoverPanic("recorder-logout", "recorder", rec.place)
		if err := s.Close(); err != nil {
			utils.Warn("Recorder did not close cleanly", "recorder", rec.place, "user_id", rec.userID, "error", err)
		}
	}()
	timer := time.NewTimer(logoutTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		utils.Warn(logoutTimedOutMessage, "recorder", rec.place, "user_id", rec.userID)
	}
}

// loginFailed logs a failed login of the recorder, which tries again after
// wait. The error goes to the log alone (ADR 0018).
func (rec *recorder) loginFailed(wait time.Duration, err error) {
	utils.Warn(loginFailedMessage, "recorder", rec.place, "user_id", rec.userID, "retry_in", wait, "error", err)
}

// UserIDs returns the user ID of every recorder that has one, in
// configured order. The list never changes after Connect.
func (r *Recorders) UserIDs() []string {
	ids := make([]string, 0, len(r.recorders))
	for _, rec := range r.recorders {
		ids = append(ids, rec.userID)
	}
	return ids
}

// State says whether the recorder with the given user ID is online, offline
// or out. One that isn't configured is out.
func (r *Recorders) State(recorderID string) RecorderState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec := r.find(recorderID); rec != nil {
		return rec.state
	}
	return RecorderOut
}

// find is the recorder with the given user ID, nil when none has it.
func (r *Recorders) find(recorderID string) *recorder {
	for _, rec := range r.recorders {
		if rec.userID == recorderID {
			return rec
		}
	}
	return nil
}

// Join connects the recorder with the given user ID to a voice channel of
// the guild. A recorder is in one voice channel at a time, so the caller
// joins one it isn't already recording with. A join of an offline recorder
// waits for its next login, and returns ErrRecorderOffline when ctx ends
// first. A join of an out recorder fails at once.
func (r *Recorders) Join(ctx context.Context, recorderID, guildID, channelID string) (Conn, error) {
	for {
		r.mu.Lock()
		rec := r.find(recorderID)
		if rec == nil || rec.state == RecorderOut {
			r.mu.Unlock()
			return nil, fmt.Errorf("recorder %s is out until a restart", recorderID)
		}
		if s := rec.session; rec.state == RecorderOnline {
			r.mu.Unlock()
			if s == nil {
				return nil, fmt.Errorf("recorder %s logged out at shutdown", recorderID)
			}
			return s.Join(ctx, guildID, channelID)
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, fmt.Errorf("recorder %s: %w", recorderID, ErrRecorderOffline)
		}
	}
}

// Close stops every recorder logging in, and logs out every recorder that
// is online, at shutdown.
func (r *Recorders) Close() {
	r.mu.Lock()
	if !r.shutDown {
		r.shutDown = true
		close(r.closed)
	}
	var sessions []GatewaySession
	for _, rec := range r.recorders {
		if rec.session != nil {
			sessions = append(sessions, rec.session)
			rec.session = nil
		}
	}
	r.mu.Unlock()
	for _, s := range sessions {
		if err := s.Close(); err != nil {
			utils.Warn("Recorder did not close cleanly", "user_id", s.UserID(), "error", err)
		}
	}
}
