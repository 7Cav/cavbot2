package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
)

// stats is everything the prototype has seen, printed as a table every few
// seconds and written to summary.txt at the end.
type stats struct {
	start time.Time
	names *nameBook

	keepalives atomic.Uint64
	dave       atomic.Pointer[session.Session]

	mu              sync.Mutex
	users           map[snowflake.ID]*userStats
	ssrcs           map[uint32]*ssrcSeq
	mappedSSRCs     map[uint32]bool
	sessionDescr    bool
	protocolVersion int
	transportErrs   uint64
	otherReadErrs   uint64
	lastReadErr     string
	notReady        time.Duration
	lastAudio       time.Time
	log             []string
	bursts          []string
}

type userStats struct {
	frames    uint64
	daveFails uint64
	lost      uint64 // audio frames that never arrived: the RTP timestamp jumped with the sequence number
	padding   uint64 // skipped sequence numbers with no timestamp jump: RTP padding, not audio
	late      uint64
	lastAt    time.Time
	trackSecs float64

	failRun   uint64
	failStart time.Time
	failErr   string
}

type ssrcSeq struct {
	last uint16
	ts   uint32
}

type daveSnapshot struct {
	session.State
	session.Stats
	sessionDescribed bool
}

func newStats() *stats {
	return &stats{
		start:       time.Now(),
		users:       map[snowflake.ID]*userStats{},
		ssrcs:       map[uint32]*ssrcSeq{},
		mappedSSRCs: map[uint32]bool{},
	}
}

func (s *stats) elapsed(t time.Time) string {
	d := t.Sub(s.start).Round(100 * time.Millisecond)
	return fmt.Sprintf("%02d:%04.1f", int(d.Minutes()), (d % time.Minute).Seconds())
}

// printf prints a timestamped line and keeps it for summary.txt.
func (s *stats) printf(format string, args ...any) {
	line := s.elapsed(time.Now()) + "  " + fmt.Sprintf(format, args...)
	fmt.Println(line)
	s.mu.Lock()
	if len(s.log) < 5000 {
		s.log = append(s.log, line)
	}
	s.mu.Unlock()
}

func (s *stats) setDave(sess *session.Session) { s.dave.Store(sess) }

func (s *stats) daveState() *daveSnapshot {
	sess := s.dave.Load()
	if sess == nil {
		return nil
	}
	s.mu.Lock()
	described := s.sessionDescr
	s.mu.Unlock()
	return &daveSnapshot{State: sess.State(), Stats: sess.Stats(), sessionDescribed: described}
}

func (s *stats) user(id snowflake.ID) *userStats {
	u, ok := s.users[id]
	if !ok {
		u = &userStats{}
		s.users[id] = u
	}
	return u
}

// decrypted records one DAVE Decrypt result. A run of failures is printed when
// it starts and again, with its length, when the user's next frame decrypts.
func (s *stats) decrypted(userID string, err error) {
	id, _ := snowflake.Parse(userID)
	now := time.Now()
	s.mu.Lock()
	u := s.user(id)
	if err != nil {
		u.daveFails++
		if u.failRun == 0 {
			u.failStart, u.failErr = now, err.Error()
		}
		u.failRun++
		first := u.failRun == 1
		s.mu.Unlock()
		if first {
			s.printf("DAVE decrypt failing for %s: %v", s.names.of(id), err)
		}
		return
	}
	if u.failRun == 0 {
		s.mu.Unlock()
		return
	}
	line := fmt.Sprintf("%s  %s: %d frames failed DAVE decrypt over %s (%s)",
		s.elapsed(u.failStart), s.names.of(id), u.failRun, now.Sub(u.failStart).Round(time.Millisecond), u.failErr)
	s.bursts = append(s.bursts, line)
	u.failRun = 0
	s.mu.Unlock()
	s.printf("DAVE decrypt recovered for %s after %s", s.names.of(id), now.Sub(u.failStart).Round(time.Millisecond))
}

// received records a frame that made it through both decryptions. A sequence
// gap is either audio that never arrived here (lost on the network, or dropped
// by a failed decrypt) or RTP padding packets, which disgo skips. Padding
// carries the previous timestamp, so the timestamp jump tells them apart.
func (s *stats) received(id snowflake.ID, p *voice.Packet, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.user(id)
	u.frames++
	u.lastAt = now
	s.lastAudio = now
	seq, ok := s.ssrcs[p.SSRC]
	if !ok {
		s.ssrcs[p.SSRC] = &ssrcSeq{last: p.Sequence, ts: p.Timestamp}
		return
	}
	diff := p.Sequence - seq.last
	if diff == 0 || diff > 0x8000 {
		return // duplicate or reordered; the track drops it as late
	}
	if skipped := uint64(diff - 1); skipped > 0 {
		var lost uint64
		if frames := int64(int32(p.Timestamp-seq.ts)) / silenceFrame; frames > 1 {
			lost = min(skipped, uint64(frames-1))
		}
		u.lost += lost
		u.padding += skipped - lost
	}
	seq.last, seq.ts = p.Sequence, p.Timestamp
}

func (s *stats) late(id snowflake.ID) {
	s.mu.Lock()
	s.user(id).late++
	s.mu.Unlock()
}

func (s *stats) trackAt(id snowflake.ID, secs float64) {
	s.mu.Lock()
	s.user(id).trackSecs = secs
	s.mu.Unlock()
}

func (s *stats) readError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isDaveError(err) {
		return // counted per user in decrypted
	}
	if strings.Contains(err.Error(), "failed to decrypt packet") {
		s.transportErrs++
	} else {
		s.otherReadErrs++
	}
	s.lastReadErr = err.Error()
}

func (s *stats) addNotReady(d time.Duration) {
	s.mu.Lock()
	s.notReady += d
	s.mu.Unlock()
}

// onVoiceEvent prints the voice gateway events that matter for DAVE: who is
// in the call, SSRC mappings, and every MLS step.
func (s *stats) onVoiceEvent(_ voice.Gateway, op voice.Opcode, _ int, data voice.GatewayMessageData) {
	switch d := data.(type) {
	case voice.GatewayMessageDataSessionDescription:
		s.mu.Lock()
		s.sessionDescr, s.protocolVersion = true, d.DaveProtocolVersion
		s.mu.Unlock()
		s.printf("voice session up: transport %s, DAVE protocol version %d", d.Mode, d.DaveProtocolVersion)
	case voice.GatewayMessageDataClientsConnect:
		names := make([]string, len(d.UserIDs))
		for i, id := range d.UserIDs {
			names[i] = s.names.of(id)
		}
		s.printf("in the call: %s", strings.Join(names, ", "))
	case voice.GatewayMessageDataClientDisconnect:
		s.printf("left the call: %s", s.names.of(d.UserID))
	case voice.GatewayMessageDataSpeaking:
		s.mu.Lock()
		seen := s.mappedSSRCs[d.SSRC]
		s.mappedSSRCs[d.SSRC] = true
		s.mu.Unlock()
		if !seen {
			s.printf("SSRC %d is %s", d.SSRC, s.names.of(d.UserID))
		}
	case voice.GatewayMessageDataDaveProtocolPrepareTransition:
		s.printf("DAVE op %d prepare transition %d to protocol version %d", op, d.TransitionID, d.ProtocolVersion)
	case voice.GatewayMessageDataDaveProtocolExecuteTransition:
		s.printf("DAVE op %d execute transition %d", op, d.TransitionID)
	case voice.GatewayMessageDataDaveProtocolPrepareEpoch:
		s.printf("DAVE op %d prepare epoch %d, protocol version %d", op, d.Epoch, d.ProtocolVersion)
	case voice.GatewayMessageDataDaveMLSExternalSenderPackage:
		s.printf("DAVE op %d external sender package", op)
	case voice.GatewayMessageDataDaveMLSProposals:
		s.printf("DAVE op %d proposals (%d bytes)", op, len(d))
	case voice.GatewayMessageDataDaveMLSAnnounceCommitTransition:
		s.printf("DAVE op %d commit for transition %d", op, d.TransitionID)
	case voice.GatewayMessageDataDaveMLSWelcome:
		s.printf("DAVE op %d welcome for transition %d", op, d.TransitionID)
	}
}

func (s *stats) printStatus(w io.Writer) {
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "\n== %s ", s.elapsed(now))
	if d := s.daveState(); d != nil {
		fmt.Fprintf(&b, "DAVE ready=%t epoch=%d v%d | commits %d ok %d failed | welcomes %d ok %d failed | recoveries %d | decrypt failures %d | downgrades %d",
			d.Ready, d.EpochID, d.ProtocolVersion, d.CommitsProcessed, d.CommitsFailed,
			d.WelcomesJoined, d.WelcomesFailed, d.RecoveryAttempts, d.DecryptFailures, d.DowngradeToV0)
	} else {
		b.WriteString("DAVE session not created yet")
	}
	s.mu.Lock()
	fmt.Fprintf(&b, "\n   keepalives sent %d | transport decrypt errors %d | other read errors %d | receiver waited on DAVE %s",
		s.keepalives.Load(), s.transportErrs, s.otherReadErrs, s.notReady.Round(100*time.Millisecond))
	if s.lastAudio.IsZero() {
		b.WriteString(" | no audio yet\n")
	} else {
		fmt.Fprintf(&b, " | last audio %s ago\n", now.Sub(s.lastAudio).Round(100*time.Millisecond))
	}
	if s.lastReadErr != "" {
		fmt.Fprintf(&b, "   last read error: %s\n", s.lastReadErr)
	}
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "   speaker\tframes\tDAVE fails\tlost\tpadding\tlate\tlast heard\ttrack\tdrift\t")
	ids := make([]snowflake.ID, 0, len(s.users))
	for id := range s.users {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		u := s.users[id]
		name := s.names.of(id)
		if id == 0 {
			name = "(SSRC not mapped yet)"
		}
		heard, drift := "-", "-"
		if !u.lastAt.IsZero() {
			heard = now.Sub(u.lastAt).Round(100*time.Millisecond).String() + " ago"
			wall := u.lastAt.Sub(s.start).Seconds()
			drift = fmt.Sprintf("%+.2fs", u.trackSecs-wall)
		}
		loss := ""
		if total := u.frames + u.lost; total > 0 && u.lost > 0 {
			loss = fmt.Sprintf(" (%.2f%%)", 100*float64(u.lost)/float64(total))
		}
		fmt.Fprintf(tw, "   %s\t%d\t%d\t%d%s\t%d\t%d\t%s\t%.1fs\t%s\t\n",
			name, u.frames, u.daveFails, u.lost, loss, u.padding, u.late, heard, u.trackSecs, drift)
	}
	s.mu.Unlock()
	_ = tw.Flush()
	_, _ = io.WriteString(w, b.String())
}

func (s *stats) printBursts(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(w, "\nDAVE decrypt failure runs:")
	if len(s.bursts) == 0 {
		fmt.Fprintln(w, "   none that ended")
	}
	for _, line := range s.bursts {
		fmt.Fprintln(w, "   "+line)
	}
	for id, u := range s.users {
		if u.failRun > 0 {
			fmt.Fprintf(w, "   %s  %s: %d frames failing at shutdown, never recovered (%s)\n",
				s.elapsed(u.failStart), s.names.of(id), u.failRun, u.failErr)
		}
	}
}

func (s *stats) writeSummary(dir string) error {
	f, err := os.Create(filepath.Join(dir, "summary.txt"))
	if err != nil {
		return err
	}
	s.printStatus(f)
	s.printBursts(f)
	s.mu.Lock()
	fmt.Fprintln(f, "\nEvent log:")
	for _, line := range s.log {
		fmt.Fprintln(f, "   "+line)
	}
	fmt.Fprintln(f, "\nTracks:")
	for id := range s.users {
		if id != 0 {
			fmt.Fprintf(f, "   %s.ogg  %s\n", id, s.names.of(id))
		}
	}
	s.mu.Unlock()
	return f.Close()
}

// nameBook turns user IDs into display names for the output. It asks the
// REST API once per unknown user, in the background, and prints the ID until
// the answer arrives.
type nameBook struct {
	dg      *discordgo.Session
	guildID string

	mu    sync.Mutex
	names map[snowflake.ID]string
}

func newNameBook(dg *discordgo.Session, guildID string) *nameBook {
	return &nameBook{dg: dg, guildID: guildID, names: map[snowflake.ID]string{}}
}

func (n *nameBook) learn(m *discordgo.Member) {
	if n == nil || m == nil || m.User == nil {
		return
	}
	id, err := snowflake.Parse(m.User.ID)
	if err != nil {
		return
	}
	n.mu.Lock()
	n.names[id] = m.DisplayName()
	n.mu.Unlock()
}

func (n *nameBook) of(id snowflake.ID) string {
	if n == nil || id == 0 {
		return id.String()
	}
	n.mu.Lock()
	name, ok := n.names[id]
	if !ok {
		n.names[id] = ""
	}
	n.mu.Unlock()
	if !ok {
		go func() {
			if m, err := n.dg.GuildMember(n.guildID, id.String()); err == nil {
				n.learn(m)
			}
		}()
	}
	if name == "" {
		return id.String()
	}
	return name + " (" + id.String() + ")"
}
