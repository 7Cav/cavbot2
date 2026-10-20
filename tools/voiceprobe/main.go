// Command voiceprobe joins a test guild voice channel as a recorder and
// measures two things a silent-drop decision waits on: how fast the voice
// server answers heartbeats sent more often than Hello asks, and whether it
// echoes the recorder's 8-byte UDP keepalive. It backs "Measure voice
// heartbeat answers and keepalive echoes on the test guild" (#830), and "Do
// the voice server's RTCP reports keep coming while members talk and without
// the keepalive?" (#837). It lives only on its research branch.
//
// It identifies on the voice server the way the recorder does, DAVE
// included, so the server treats it as a recorder. Then it sends opcode 3
// every -slow-every for -slow-for, and if no close came, every -fast-every
// for -fast-for. The whole time it sends the recorder's keepalive every
// -keepalive on the UDP socket, or none with -keepalive 0, and logs every
// datagram that isn't RTP. It counts the RTP, and tallies the UDP socket
// every -tally.
//
// It holds the smoke run lock while it runs, since it logs in as a recorder a
// smoke run would use. Its log on stdout names no token, session, user,
// channel, host or address: each is a placeholder that keeps equality.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	"github.com/thomas-vilte/dave-go/session"
)

type options struct {
	envFile   string
	recorder  int
	channel   string
	slowEvery time.Duration
	slowFor   time.Duration
	fastEvery time.Duration
	fastFor   time.Duration
	keepEvery time.Duration
	tally     time.Duration
}

func main() {
	var o options
	flag.StringVar(&o.envFile, "env", "", "env file holding GUILD_ID and RECORDER_TOKENS (default: the main checkout's .env)")
	flag.IntVar(&o.recorder, "recorder", 1, "which recorder token in RECORDER_TOKENS, counting from 1")
	flag.StringVar(&o.channel, "channel", "", "voice channel ID to join (required)")
	flag.DurationVar(&o.slowEvery, "slow-every", 5*time.Second, "heartbeat period, first phase")
	flag.DurationVar(&o.slowFor, "slow-for", 30*time.Minute, "first phase length")
	flag.DurationVar(&o.fastEvery, "fast-every", time.Second, "heartbeat period, second phase")
	flag.DurationVar(&o.fastFor, "fast-for", 5*time.Minute, "second phase length, 0 for no second phase")
	flag.DurationVar(&o.keepEvery, "keepalive", 5*time.Second, "UDP keepalive period, 0 for no keepalive")
	flag.DurationVar(&o.tally, "tally", 10*time.Second, "how often to log what the UDP socket got since the last tally")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: utcMillis}))
	if err := run(o, log); err != nil {
		log.Error("probe failed", "error", err)
		os.Exit(1)
	}
}

// utcMillis writes each record's time in UTC to the millisecond.
func utcMillis(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		return slog.String(slog.TimeKey, a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	return a
}

func run(o options, log *slog.Logger) error {
	if o.channel == "" {
		return errors.New("-channel is required")
	}
	common, err := gitCommonDir()
	if err != nil {
		return err
	}
	if o.envFile == "" {
		o.envFile = filepath.Join(filepath.Dir(common), ".env")
	}
	env, err := godotenv.Read(o.envFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", o.envFile, err)
	}
	guild := env["GUILD_ID"]
	tokens := strings.Split(env["RECORDER_TOKENS"], ",")
	if guild == "" || o.recorder < 1 || o.recorder > len(tokens) || strings.TrimSpace(tokens[o.recorder-1]) == "" {
		return fmt.Errorf("%s needs GUILD_ID and at least %d RECORDER_TOKENS", o.envFile, o.recorder)
	}
	token := strings.TrimSpace(tokens[o.recorder-1])

	unlock, err := holdSmokeLock(filepath.Join(common, "cavbot2-smoke", "lock"))
	if err != nil {
		return err
	}
	defer unlock()

	red := newRedactor()
	red.fix("guild", guild, "<guild>")
	red.fix("channel", o.channel, "<channel-A>")

	p := &probe{o: o, log: log, red: red, guild: guild}
	return p.run(token)
}

func gitCommonDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// holdSmokeLock takes the lock tools/smoke takes for a run, so no smoke run
// starts while the probe holds a recorder in a call.
func holdSmokeLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("a smoke run holds %s: %w", path, err)
	}
	return func() { _ = f.Close() }, nil
}

// redactor names each distinct value of a kind by first appearance, so a log
// keeps equality without the value.
type redactor struct {
	mu    sync.Mutex
	names map[string]string
	count map[string]int
}

func newRedactor() *redactor {
	return &redactor{names: map[string]string{}, count: map[string]int{}}
}

func (r *redactor) fix(kind, v, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names[kind+"\x00"+v] = name
}

func (r *redactor) name(kind, v string) string {
	if v == "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := kind + "\x00" + v
	if n, ok := r.names[key]; ok {
		return n
	}
	r.count[kind]++
	n := "<" + kind + "-" + strconv.Itoa(r.count[kind]) + ">"
	r.names[key] = n
	return n
}

type probe struct {
	o     options
	log   *slog.Logger
	red   *redactor
	guild string
	self  atomic.Value // string

	states  chan *discordgo.VoiceStateUpdate
	servers chan *discordgo.VoiceServerUpdate
}

func (p *probe) run(token string) error {
	p.states = make(chan *discordgo.VoiceStateUpdate, 16)
	p.servers = make(chan *discordgo.VoiceServerUpdate, 16)

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return err
	}
	dg.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.Ready) {
		p.self.Store(e.User.ID)
		p.red.fix("user", e.User.ID, "<recorder>")
		p.log.Info("gateway READY", "session", p.red.name("session", e.SessionID))
	})
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { p.log.Info("gateway RESUMED") })
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) { p.log.Warn("gateway disconnected") })
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.VoiceStateUpdate) {
		if self, _ := p.self.Load().(string); e.UserID != self {
			return
		}
		p.log.Info("gateway VOICE_STATE_UPDATE", "user", "<recorder>",
			"channel", p.red.name("channel", e.ChannelID), "session", p.red.name("session", e.SessionID))
		select {
		case p.states <- e:
		default:
		}
	})
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.VoiceServerUpdate) {
		if e.GuildID != p.guild {
			return
		}
		p.log.Info("gateway VOICE_SERVER_UPDATE", "endpoint", p.red.name("voice-server", e.Endpoint),
			"token", p.red.name("token", e.Token))
		select {
		case p.servers <- e:
		default:
		}
	})
	if err := dg.Open(); err != nil {
		return fmt.Errorf("gateway open: %w", err)
	}
	defer func() { _ = dg.Close() }()
	self := dg.State.User.ID
	p.self.Store(self)
	p.red.fix("user", self, "<recorder>")

	p.log.Info("sending opcode 4", "channel", "<channel-A>", "self_mute", true, "self_deaf", false)
	if err := dg.ChannelVoiceJoinManual(p.guild, p.o.channel, true, false); err != nil {
		return fmt.Errorf("voice state update: %w", err)
	}
	var state *discordgo.VoiceStateUpdate
	var server *discordgo.VoiceServerUpdate
	deadline := time.After(15 * time.Second)
	for state == nil || server == nil {
		select {
		case s := <-p.states:
			if s.ChannelID == p.o.channel {
				state = s
			}
		case s := <-p.servers:
			server = s
		case <-deadline:
			return errors.New("no voice state and voice server within 15 s")
		}
	}
	defer p.leave(dg)

	v := &voice{p: p, beats: map[int64]sentBeat{}, keeps: map[uint32]time.Time{}, closed: make(chan struct{}), hello: make(chan time.Duration, 1)}
	return v.run(state, server)
}

// leave sends opcode 4 with no channel and waits for the voice state that
// says the recorder left.
func (p *probe) leave(dg *discordgo.Session) {
	p.log.Info("sending opcode 4", "channel", nil)
	if err := dg.ChannelVoiceJoinManual(p.guild, "", true, false); err != nil {
		p.log.Warn("leave failed", "error", err)
		return
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-p.states:
			if s.ChannelID == "" {
				return
			}
		case <-deadline:
			p.log.Warn("no voice state for the leave within 5 s")
			return
		}
	}
}

type sentBeat struct {
	n      int
	phase  string
	at     time.Time
	warned bool
}

type phaseStats struct {
	name     string
	sent     int
	answered int
	rtts     []time.Duration
}

// voice is the probe's one voice connection: its gateway websocket, its UDP
// socket and its DAVE session.
type voice struct {
	p *probe

	ws  *websocket.Conn
	wmu sync.Mutex
	seq atomic.Int64

	dave godave.Session

	hello     chan time.Duration
	openedAt  time.Time
	closed    chan struct{}
	closeOnce sync.Once
	closeWhy  string

	bmu       sync.Mutex
	beats     map[int64]sentBeat
	lastNonce int64
	phases    []*phaseStats
	unknown   int

	udp      *net.UDPConn
	kmu      sync.Mutex
	keeps    map[uint32]time.Time
	keepSent int
	echoes   []time.Duration
	rtp      atomic.Int64
	rtcp     atomic.Int64
	other    map[int]int
	omu      sync.Mutex
	ssrcs    map[uint32]bool
	byType   map[int]*rtcpType
}

// rtcpType follows one RTCP packet type: when the last one came, how much
// RTP had come by then, and every gap between two of them.
type rtcpType struct {
	last      time.Time
	rtpAtLast int64
	gaps      []time.Duration
	rtpGaps   []time.Duration // the gaps RTP arrived in
}

func (v *voice) run(state *discordgo.VoiceStateUpdate, server *discordgo.VoiceServerUpdate) error {
	log := v.p.log
	guildID, err := snowflake.Parse(v.p.guild)
	if err != nil {
		return err
	}
	userID, err := snowflake.Parse(state.UserID)
	if err != nil {
		return err
	}
	v.other = map[int]int{}
	v.ssrcs = map[uint32]bool{}
	v.byType = map[int]*rtcpType{}
	v.dave = session.CreateFunc()(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		godave.UserID(state.UserID), v)
	defer func() { _ = v.dave.Close() }()
	v.dave.SetChannelID(godave.ChannelID(mustUint(v.p.o.channel)))

	url := "wss://" + server.Endpoint + "?v=8"
	log.Info("opening voice gateway", "endpoint", v.p.red.name("voice-server", server.Endpoint), "v", 8)
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("dial voice gateway: %w", err)
	}
	v.ws = ws
	v.openedAt = time.Now()
	ws.SetCloseHandler(func(int, string) error { return nil })

	identify := disgovoice.GatewayMessageDataIdentify{
		GuildID:                guildID,
		UserID:                 userID,
		SessionID:              state.SessionID,
		Token:                  server.Token,
		MaxDaveProtocolVersion: v.dave.MaxSupportedProtocolVersion(),
	}
	go v.read(identify)

	var interval time.Duration
	select {
	case interval = <-v.hello:
	case <-v.closed:
		return fmt.Errorf("voice gateway closed before Hello: %s", v.closeWhy)
	case <-time.After(10 * time.Second):
		return errors.New("no Hello within 10 s")
	}
	log.Info("heartbeat plan", "hello_interval", interval, "slow_every", v.p.o.slowEvery, "slow_for", v.p.o.slowFor,
		"fast_every", v.p.o.fastEvery, "fast_for", v.p.o.fastFor, "keepalive_every", v.p.o.keepEvery, "tally_every", v.p.o.tally)

	go v.watchDave()

	if v.beat("slow", v.p.o.slowEvery, v.p.o.slowFor) {
		if v.p.o.fastFor > 0 {
			v.beat("fast", v.p.o.fastEvery, v.p.o.fastFor)
		}
	} else {
		log.Warn("skipping the fast phase, since the voice gateway closed", "why", v.closeWhy)
	}

	// Give the last heartbeat time to be answered.
	select {
	case <-v.closed:
	case <-time.After(3 * time.Second):
	}
	v.stop()
	v.summary()
	return nil
}

func mustUint(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// beat sends opcode 3 every period for length, and reports whether the
// voice gateway was still open at the end.
func (v *voice) beat(phase string, period, length time.Duration) bool {
	st := &phaseStats{name: phase}
	v.bmu.Lock()
	v.phases = append(v.phases, st)
	v.bmu.Unlock()
	v.p.log.Info("heartbeat phase starts", "phase", phase, "every", period, "for", length)
	end := time.After(length)
	t := time.NewTicker(period)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-v.closed:
			return false
		case <-end:
			v.p.log.Info("heartbeat phase ends", "phase", phase, "sent", st.sent)
			return true
		case <-t.C:
		}
		v.sendBeat(phase, n, st)
	}
}

func (v *voice) sendBeat(phase string, n int, st *phaseStats) {
	v.bmu.Lock()
	for nonce, b := range v.beats {
		if b.warned {
			continue
		}
		v.p.log.Warn("heartbeat still unanswered as the next goes out", "phase", b.phase, "n", b.n,
			"age_ms", time.Since(b.at).Milliseconds())
		b.warned = true
		v.beats[nonce] = b
	}
	nonce := time.Now().UnixMilli()
	if nonce <= v.lastNonce {
		nonce = v.lastNonce + 1
	}
	v.lastNonce = nonce
	at := time.Now()
	v.beats[nonce] = sentBeat{n: n, phase: phase, at: at}
	st.sent++
	v.bmu.Unlock()
	if err := v.sendJSON(disgovoice.OpcodeHeartbeat, disgovoice.GatewayMessageDataHeartbeat{T: nonce, SeqAck: int(v.seq.Load())}); err != nil {
		v.p.log.Warn("heartbeat not sent", "phase", phase, "n", n, "error", err)
	}
}

func (v *voice) answered(nonce int64, at time.Time) {
	v.bmu.Lock()
	b, ok := v.beats[nonce]
	if ok {
		delete(v.beats, nonce)
	} else {
		v.unknown++
	}
	var st *phaseStats
	for _, s := range v.phases {
		if s.name == b.phase {
			st = s
		}
	}
	if ok && st != nil {
		st.answered++
		st.rtts = append(st.rtts, at.Sub(b.at))
	}
	v.bmu.Unlock()
	if !ok {
		v.p.log.Warn("heartbeat answer with a nonce the probe didn't send", "nonce", nonce)
		return
	}
	v.p.log.Info("heartbeat answered", "phase", b.phase, "n", b.n, "rtt_ms", fmtMillis(at.Sub(b.at)))
}

func fmtMillis(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64)
}

func (v *voice) sendJSON(op disgovoice.Opcode, d any) error {
	data, err := json.Marshal(struct {
		Op disgovoice.Opcode `json:"op"`
		D  any               `json:"d"`
	}{op, d})
	if err != nil {
		return err
	}
	v.wmu.Lock()
	defer v.wmu.Unlock()
	return v.ws.WriteMessage(websocket.TextMessage, data)
}

func (v *voice) sendBinary(op disgovoice.Opcode, payload []byte) error {
	v.wmu.Lock()
	defer v.wmu.Unlock()
	return v.ws.WriteMessage(websocket.BinaryMessage, append([]byte{byte(op)}, payload...))
}

// SendMLSKeyPackage and the three methods after it are the DAVE session's
// callbacks.
func (v *voice) SendMLSKeyPackage(pkg []byte) error {
	v.p.log.Info("voice gateway send", "op", int(disgovoice.OpcodeDaveMLSKeyPackage), "len", len(pkg))
	return v.sendBinary(disgovoice.OpcodeDaveMLSKeyPackage, pkg)
}

func (v *voice) SendMLSCommitWelcome(cw []byte) error {
	v.p.log.Info("voice gateway send", "op", int(disgovoice.OpcodeDaveMLSCommitWelcome), "len", len(cw))
	return v.sendBinary(disgovoice.OpcodeDaveMLSCommitWelcome, cw)
}

func (v *voice) SendReadyForTransition(id uint16) error {
	v.p.log.Info("voice gateway send", "op", int(disgovoice.OpcodeDaveTransitionReady), "transition_id", id)
	return v.sendJSON(disgovoice.OpcodeDaveTransitionReady, disgovoice.GatewayMessageDataDaveProtocolReadyForTransition{TransitionID: id})
}

func (v *voice) SendInvalidCommitWelcome(id uint16) error {
	v.p.log.Info("voice gateway send", "op", int(disgovoice.OpcodeDaveMLSInvalidCommitWelcome), "transition_id", id)
	return v.sendJSON(disgovoice.OpcodeDaveMLSInvalidCommitWelcome, disgovoice.GatewayMessageDataDaveInvalidCommitWelcome{TransitionID: id})
}

func (v *voice) markClosed(why string) {
	v.closeOnce.Do(func() {
		v.closeWhy = why
		close(v.closed)
	})
}

func (v *voice) read(identify disgovoice.GatewayMessageDataIdentify) {
	log := v.p.log
	for {
		mt, data, err := v.ws.ReadMessage()
		at := time.Now()
		if err != nil {
			select {
			case <-v.closed:
				return
			default:
			}
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				log.Error("voice gateway close received", "code", ce.Code, "text", ce.Text,
					"since_open", time.Since(v.openedAt).Round(time.Millisecond))
				v.markClosed(fmt.Sprintf("close %d %q", ce.Code, ce.Text))
			} else {
				log.Error("voice gateway read failed", "error", err, "since_open", time.Since(v.openedAt).Round(time.Millisecond))
				v.markClosed(err.Error())
			}
			return
		}
		var msg disgovoice.GatewayMessage
		switch mt {
		case websocket.TextMessage:
			err = json.Unmarshal(data, &msg)
			var s struct {
				Seq *int `json:"seq"`
			}
			if json.Unmarshal(data, &s) == nil && s.Seq != nil {
				v.seq.Store(int64(*s.Seq))
			}
		case websocket.BinaryMessage:
			err = msg.UnmarshalBinary(bytes.NewReader(data))
			if err == nil && len(data) >= 2 {
				v.seq.Store(int64(binary.BigEndian.Uint16(data)))
			}
		default:
			err = fmt.Errorf("message type %d", mt)
		}
		if err != nil {
			log.Warn("voice gateway message not parsed", "len", len(data), "error", err)
			continue
		}
		v.handle(msg, mt, len(data), at, identify)
	}
}

func (v *voice) handle(msg disgovoice.GatewayMessage, mt, size int, at time.Time, identify disgovoice.GatewayMessageDataIdentify) {
	log := v.p.log
	if d, ok := msg.D.(disgovoice.GatewayMessageDataHeartbeatACK); ok {
		v.answered(d.T, at)
		return
	}
	kind := "text"
	if mt == websocket.BinaryMessage {
		kind = "binary"
	}
	args := []any{"op", int(msg.Op), "seq", v.seq.Load(), "kind", kind, "len", size}

	switch d := msg.D.(type) {
	case disgovoice.GatewayMessageDataHello:
		interval := time.Duration(d.HeartbeatInterval * float64(time.Millisecond))
		log.Info("voice gateway message", append(args, "heartbeat_interval_ms", d.HeartbeatInterval)...)
		log.Info("voice gateway send", "op", int(disgovoice.OpcodeIdentify), "session", v.p.red.name("session", identify.SessionID),
			"token", v.p.red.name("token", identify.Token), "max_dave_protocol_version", identify.MaxDaveProtocolVersion)
		if err := v.sendJSON(disgovoice.OpcodeIdentify, identify); err != nil {
			log.Error("identify not sent", "error", err)
		}
		v.hello <- interval

	case disgovoice.GatewayMessageDataReady:
		log.Info("voice gateway message", append(args, "ssrc", d.SSRC, "ip", v.p.red.name("ip", d.IP), "port", d.Port, "modes", d.Modes)...)
		go v.openUDP(d)

	case disgovoice.GatewayMessageDataSessionDescription:
		log.Info("voice gateway message", append(args, "mode", d.Mode, "secret_key", "<secret-key>", "dave_protocol_version", d.DaveProtocolVersion)...)
		v.dave.OnSelectProtocolAck(uint16(d.DaveProtocolVersion))

	case disgovoice.GatewayMessageDataClientsConnect:
		users := make([]string, 0, len(d.UserIDs))
		for _, id := range d.UserIDs {
			users = append(users, v.p.red.name("user", id.String()))
			v.dave.AddUser(godave.UserID(id.String()))
		}
		log.Info("voice gateway message", append(args, "users", users)...)

	case disgovoice.GatewayMessageDataSpeaking:
		log.Info("voice gateway message", append(args, "user", v.p.red.name("user", d.UserID.String()), "ssrc", d.SSRC, "speaking", int(d.Speaking))...)

	case disgovoice.GatewayMessageDataClientDisconnect:
		log.Info("voice gateway message", append(args, "user", v.p.red.name("user", d.UserID.String()))...)
		v.dave.RemoveUser(godave.UserID(d.UserID.String()))

	case disgovoice.GatewayMessageDataDaveProtocolPrepareTransition:
		log.Info("voice gateway message", append(args, "transition_id", d.TransitionID, "protocol_version", d.ProtocolVersion)...)
		v.dave.OnDavePrepareTransition(d.TransitionID, d.ProtocolVersion)

	case disgovoice.GatewayMessageDataDaveProtocolExecuteTransition:
		log.Info("voice gateway message", append(args, "transition_id", d.TransitionID)...)
		v.dave.OnDaveExecuteTransition(d.TransitionID)

	case disgovoice.GatewayMessageDataDaveProtocolPrepareEpoch:
		log.Info("voice gateway message", append(args, "epoch", d.Epoch, "protocol_version", d.ProtocolVersion)...)
		v.dave.OnDavePrepareEpoch(d.Epoch, d.ProtocolVersion)

	case disgovoice.GatewayMessageDataDaveMLSExternalSenderPackage:
		log.Info("voice gateway message", args...)
		v.dave.OnDaveMLSExternalSenderPackage(d)

	case disgovoice.GatewayMessageDataDaveMLSProposals:
		log.Info("voice gateway message", args...)
		v.dave.OnDaveMLSProposals(d)

	case disgovoice.GatewayMessageDataDaveMLSAnnounceCommitTransition:
		log.Info("voice gateway message", append(args, "transition_id", d.TransitionID)...)
		v.dave.OnDaveMLSPrepareCommitTransition(d.TransitionID, d.CommitMessage)

	case disgovoice.GatewayMessageDataDaveMLSWelcome:
		log.Info("voice gateway message", append(args, "transition_id", d.TransitionID)...)
		v.dave.OnDaveMLSWelcome(d.TransitionID, d.WelcomeMessage)

	default:
		log.Info("voice gateway message", args...)
	}
}

// watchDave logs the moment the DAVE session first reports ready.
func (v *voice) watchDave() {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-v.closed:
			return
		case <-t.C:
			if v.dave.Ready() {
				v.p.log.Info("DAVE session ready", "since_open", time.Since(v.openedAt).Round(time.Millisecond))
				return
			}
		}
	}
}

func (v *voice) openUDP(d disgovoice.GatewayMessageDataReady) {
	log := v.p.log
	ip := net.ParseIP(d.IP)
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: ip, Port: d.Port})
	if err != nil {
		log.Error("UDP dial failed", "error", err)
		return
	}
	v.udp = conn
	addr, port, err := discoverIP(conn, d.SSRC)
	if err != nil {
		log.Error("IP discovery failed", "error", err)
		return
	}
	// A host address of its own needs no NAT mapping kept open, so a report
	// that stops without the keepalive stopped at the voice server.
	local, _ := conn.LocalAddr().(*net.UDPAddr)
	log.Info("IP discovery answered", "address", v.p.red.name("ip", addr), "port", port,
		"same_as_local_address", local != nil && local.IP.String() == addr)
	go v.readUDP()
	go v.tallyUDP()
	if v.p.o.keepEvery > 0 {
		go v.keepalive()
	} else {
		log.Info("keepalive off")
	}
	mode, err := disgovoice.ChooseEncryptionMode(d.Modes)
	if err != nil {
		log.Error("no encryption mode", "error", err)
		return
	}
	log.Info("voice gateway send", "op", int(disgovoice.OpcodeSelectProtocol), "address", v.p.red.name("ip", addr), "port", port, "mode", mode)
	if err := v.sendJSON(disgovoice.OpcodeSelectProtocol, disgovoice.GatewayMessageDataSelectProtocol{
		Protocol: disgovoice.ProtocolUDP,
		Data:     disgovoice.GatewayMessageDataSelectProtocolData{Address: addr, Port: port, Mode: mode},
	}); err != nil {
		log.Error("select protocol not sent", "error", err)
	}
}

// discoverIP is the recorder's IP discovery, as voice/udp.go does it.
func discoverIP(conn net.Conn, ssrc uint32) (string, int, error) {
	req := make([]byte, 74)
	binary.BigEndian.PutUint16(req[0:2], 1)
	binary.BigEndian.PutUint16(req[2:4], 70)
	binary.BigEndian.PutUint32(req[4:8], ssrc)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", 0, err
	}
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if _, err := conn.Write(req); err != nil {
		return "", 0, err
	}
	resp := make([]byte, 74)
	n, err := conn.Read(resp)
	if err != nil {
		return "", 0, err
	}
	if n != 74 || binary.BigEndian.Uint16(resp[0:2]) != 2 || binary.BigEndian.Uint32(resp[4:8]) != ssrc {
		return "", 0, errors.New("IP discovery answer malformed")
	}
	return string(bytes.TrimRight(resp[8:72], "\x00")), int(binary.BigEndian.Uint16(resp[72:74])), nil
}

// keepalive sends the recorder's 8 bytes, a little-endian 32-bit counter and
// four zeros, at once and then every -keepalive, as voice/udp.go does.
func (v *voice) keepalive() {
	t := time.NewTicker(v.p.o.keepEvery)
	defer t.Stop()
	buf := make([]byte, 8)
	for counter := uint32(0); ; counter++ {
		binary.LittleEndian.PutUint32(buf, counter)
		v.kmu.Lock()
		v.keeps[counter] = time.Now()
		v.keepSent++
		v.kmu.Unlock()
		if _, err := v.udp.Write(buf); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			v.p.log.Warn("keepalive not sent", "counter", counter, "error", err)
		}
		select {
		case <-v.closed:
			return
		case <-t.C:
		}
	}
}

// readUDP logs every datagram that isn't RTP, with its length and first
// bytes, and counts the RTP it skips.
func (v *voice) readUDP() {
	buf := make([]byte, 4096)
	for {
		n, err := v.udp.Read(buf)
		at := time.Now()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				v.p.log.Warn("UDP read failed", "error", err)
			}
			return
		}
		b := buf[:n]
		switch {
		case n >= 12 && b[0]&0xC0 == 0x80 && b[1]&0x7F == 0x78:
			count := v.rtp.Add(1)
			ssrc := binary.BigEndian.Uint32(b[8:12])
			v.omu.Lock()
			first := !v.ssrcs[ssrc]
			v.ssrcs[ssrc] = true
			v.omu.Unlock()
			if first {
				v.p.log.Info("first RTP datagram", "ssrc", ssrc, "len", n, "rtp_so_far", count)
			}
			continue
		case n == 8 && binary.LittleEndian.Uint32(b[4:8]) == 0:
			counter := binary.LittleEndian.Uint32(b[0:4])
			v.kmu.Lock()
			sent, ok := v.keeps[counter]
			if ok {
				delete(v.keeps, counter)
				v.echoes = append(v.echoes, at.Sub(sent))
			}
			v.kmu.Unlock()
			if ok {
				v.p.log.Info("keepalive echoed", "counter", counter, "rtt_ms", fmtMillis(at.Sub(sent)), "bytes", hex.EncodeToString(b))
				continue
			}
		case n >= 8 && b[0]&0xC0 == 0x80 && b[1] >= 200 && b[1] <= 204:
			v.rtcp.Add(1)
			typ := int(b[1])
			rtpNow := v.rtp.Load()
			v.omu.Lock()
			v.other[n]++
			t := v.byType[typ]
			if t == nil {
				t = &rtcpType{}
				v.byType[typ] = t
			}
			since, rtpSince := "first", rtpNow
			if !t.last.IsZero() {
				gap := at.Sub(t.last)
				t.gaps = append(t.gaps, gap)
				if rtpNow > t.rtpAtLast {
					t.rtpGaps = append(t.rtpGaps, gap)
				}
				since = fmtMillis(gap)
				rtpSince = rtpNow - t.rtpAtLast
			}
			t.last, t.rtpAtLast = at, rtpNow
			v.omu.Unlock()
			v.p.log.Info("non-RTP datagram", "len", n, "rtcp_type", typ, "since_last_rtcp_ms", since,
				"rtp_since_last_rtcp", rtpSince, "first_bytes", hex.EncodeToString(b[:min(n, 16)]))
			continue
		}
		v.omu.Lock()
		v.other[n]++
		v.omu.Unlock()
		// An IP discovery answer carries this host's address from byte 8 on.
		show := min(n, 16)
		if n == 74 && binary.BigEndian.Uint16(b[0:2]) == 2 {
			show = 8
		}
		v.p.log.Info("non-RTP datagram", "len", n, "first_bytes", hex.EncodeToString(b[:show]))
	}
}

func (v *voice) stop() {
	v.markClosed("probe done")
	v.wmu.Lock()
	_ = v.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "probe done"))
	v.wmu.Unlock()
	_ = v.ws.Close()
	if v.udp != nil {
		_ = v.udp.Close()
	}
	v.p.log.Info("voice gateway closed by the probe", "code", websocket.CloseNormalClosure)
}

func (v *voice) summary() {
	log := v.p.log
	v.bmu.Lock()
	for _, st := range v.phases {
		rtts := slices.Clone(st.rtts)
		slices.Sort(rtts)
		args := []any{"phase", st.name, "sent", st.sent, "answered", st.answered, "unanswered", st.sent - st.answered}
		if len(rtts) > 0 {
			args = append(args, "rtt_min_ms", fmtMillis(rtts[0]), "rtt_p50_ms", fmtMillis(pct(rtts, 50)),
				"rtt_p90_ms", fmtMillis(pct(rtts, 90)), "rtt_p99_ms", fmtMillis(pct(rtts, 99)), "rtt_max_ms", fmtMillis(rtts[len(rtts)-1]))
		}
		log.Info("heartbeat summary", args...)
	}
	log.Info("heartbeat answers with an unknown nonce", "count", v.unknown)
	v.bmu.Unlock()

	v.kmu.Lock()
	echoes := slices.Clone(v.echoes)
	sent := v.keepSent
	v.kmu.Unlock()
	slices.Sort(echoes)
	args := []any{"sent", sent, "echoed", len(echoes)}
	if len(echoes) > 0 {
		args = append(args, "rtt_min_ms", fmtMillis(echoes[0]), "rtt_p50_ms", fmtMillis(pct(echoes, 50)), "rtt_max_ms", fmtMillis(echoes[len(echoes)-1]))
	}
	log.Info("keepalive summary", args...)
	v.omu.Lock()
	lens := make([]string, 0, len(v.other))
	for n, c := range v.other {
		lens = append(lens, fmt.Sprintf("%d:%d", n, c))
	}
	types := make([]int, 0, len(v.byType))
	for typ := range v.byType {
		types = append(types, typ)
	}
	slices.Sort(types)
	for _, typ := range types {
		t := v.byType[typ]
		log.Info("RTCP gap summary", append([]any{"rtcp_type", typ, "gaps_counted", "all"}, gapArgs(t.gaps)...)...)
		log.Info("RTCP gap summary", append([]any{"rtcp_type", typ, "gaps_counted", "with RTP in them"}, gapArgs(t.rtpGaps)...)...)
	}
	ssrcs := len(v.ssrcs)
	v.omu.Unlock()
	slices.Sort(lens)
	log.Info("UDP summary", "rtp", v.rtp.Load(), "rtp_ssrcs", ssrcs, "rtcp", v.rtcp.Load(), "non_rtp_by_len", strings.Join(lens, ","))
}

func gapArgs(gaps []time.Duration) []any {
	if len(gaps) == 0 {
		return []any{"gaps", 0}
	}
	gaps = slices.Clone(gaps)
	slices.Sort(gaps)
	over := 0
	for _, g := range gaps {
		if g > 1500*time.Millisecond {
			over++
		}
	}
	return []any{"gaps", len(gaps), "min_ms", fmtMillis(gaps[0]), "p50_ms", fmtMillis(pct(gaps, 50)),
		"p99_ms", fmtMillis(pct(gaps, 99)), "max_ms", fmtMillis(gaps[len(gaps)-1]), "over_1500ms", over}
}

// tallyUDP logs, every -tally, what the UDP socket got since the tally
// before, so a socket that goes quiet still leaves lines.
func (v *voice) tallyUDP() {
	if v.p.o.tally <= 0 {
		return
	}
	t := time.NewTicker(v.p.o.tally)
	defer t.Stop()
	var rtp, rtcp int64
	for {
		select {
		case <-v.closed:
			return
		case <-t.C:
		}
		r, c := v.rtp.Load(), v.rtcp.Load()
		var last time.Time
		v.omu.Lock()
		for _, ty := range v.byType {
			if ty.last.After(last) {
				last = ty.last
			}
		}
		v.omu.Unlock()
		since := "none yet"
		if !last.IsZero() {
			since = fmtMillis(time.Since(last))
		}
		v.p.log.Info("UDP tally", "rtp", r-rtp, "rtcp", c-rtcp, "since_last_rtcp_ms", since)
		rtp, rtcp = r, c
	}
}

func pct(sorted []time.Duration, p int) time.Duration {
	i := (len(sorted) - 1) * p / 100
	return sorted[i]
}

var _ godave.Callbacks = (*voice)(nil)
