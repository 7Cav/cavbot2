package voice

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/7cav/cavbot2/utils"
	"github.com/disgoorg/godave"
	"github.com/gorilla/websocket"
)

// The recorder's voice gateway dialer. Discord can move a call to a voice
// server whose hostname is newer than the host's resolver's answer for it.
// A resolver that asked for the name before its record existed keeps the
// "no such host" answer for as long as the zone's SOA record allows, and
// discord.media allows 30 minutes, longer than a recording can wait. So when
// the host's resolver answers "no such host" for a voice server, the
// recorder asks the zone's own nameservers, the ones its NS records name.
// They answer from the zone itself, with nothing cached, and the recorder
// connects at the address they give. TLS and Host still carry the voice
// server's hostname, so its certificate is checked as on any other dial.
// The recorder asks both again on every dial, so a record that turns up
// later is found.

// nameserverPort is the port a nameserver answers queries on.
const nameserverPort = "53"

// voiceDialer dials voice servers' gateways.
type voiceDialer struct {
	// host is the host's resolver.
	host *net.Resolver
	// atNameserver is a resolver that asks the nameserver named host
	// itself.
	atNameserver func(host string) *net.Resolver
	// unresolvable, when set and true, has every voice server's hostname
	// answer "no such host" from both, which lets a smoke test make the
	// server a call moves to unreachable (docs/smoke-test.md).
	unresolvable func() bool
	// drops, when set, can drop every connection the dialer made, and the
	// UDP sockets of the same recorder, which lets a smoke test drop a
	// recording's voice connection.
	drops *connDrops
}

// smokeDNSFailEnv names a file whose existence makes every voice server
// unreachable to the recorders, as voiceDialer's unresolvable says. Only a
// smoke run sets it: the compose file's environment block doesn't carry it.
const smokeDNSFailEnv = "SMOKE_VOICE_DNS_FAIL_FILE"

// smokeDropEnv names a file whose appearance drops every voice connection
// the recorders hold then, as connDrops says. Only a smoke run
// sets it: the compose file's environment block doesn't carry it.
const smokeDropEnv = "SMOKE_VOICE_DROP_FILE"

// smokeUDPCutEnv names a file whose appearance cuts the UDP path of every
// voice connection the recorders hold then, and leaves their voice gateways
// up, as connDrops says. Only a smoke run sets it: the compose file's
// environment block doesn't carry it.
const smokeUDPCutEnv = "SMOKE_VOICE_UDP_CUT_FILE"

// smokeDecryptFailEnv names a file whose existence has every DAVE session
// the recorders hold refuse every frame, as a session whose keys fell out of
// step with the speakers' would. Only a smoke run sets it: the compose file's
// environment block doesn't carry it.
const smokeDecryptFailEnv = "SMOKE_VOICE_DECRYPT_FAIL_FILE"

// smokeLeaveHangEnv names a file whose existence holds every recorder leave,
// as a leave frozen in the voice adapter hangs, until the file is gone. Only
// a smoke run sets it: the compose file's environment block doesn't carry
// it.
const smokeLeaveHangEnv = "SMOKE_VOICE_LEAVE_HANG_FILE"

// smokeLeaveHangPoll is how often a held leave looks for the file
// smokeLeaveHangEnv names.
const smokeLeaveHangPoll = 250 * time.Millisecond

// hangingLeaveConn is a recorder's voice connection whose leave waits while
// file exists before it starts. The wait ignores the leave's context, as a
// leave frozen in the voice adapter does.
type hangingLeaveConn struct {
	Conn
	file string
}

// Leave waits while the file exists, looking again every
// smokeLeaveHangPoll, then leaves.
func (c *hangingLeaveConn) Leave(ctx context.Context) {
	for {
		if _, err := os.Stat(c.file); err != nil {
			break
		}
		time.Sleep(smokeLeaveHangPoll)
	}
	c.Conn.Leave(ctx)
}

// smokeDecryptFailMessage is the WARN a DAVE session logs when it starts
// refusing every frame for the smoke hook.
const smokeDecryptFailMessage = "Smoke hook refusing to decrypt voice frames"

// errSmokeDecryptFail is the refusal of a frame the smoke hook refuses.
var errSmokeDecryptFail = errors.New("voice: the smoke hook refuses every frame")

// smokeDecryptFailures is create, with each DAVE session it makes refusing
// every frame while the file smokeDecryptFailEnv names exists, when it names
// one.
func smokeDecryptFailures(create godave.SessionCreateFunc) godave.SessionCreateFunc {
	failFile := os.Getenv(smokeDecryptFailEnv)
	if failFile == "" {
		return create
	}
	return func(logger *slog.Logger, userID godave.UserID, callbacks godave.Callbacks) godave.Session {
		return &refusingDAVE{Session: create(logger, userID, callbacks), recorder: userID, file: failFile}
	}
}

// refusingDAVE is the recorder's DAVE session, refusing every frame while
// file exists. refusing is whether it refused the last frame it was handed.
type refusingDAVE struct {
	godave.Session
	recorder godave.UserID
	file     string
	refusing atomic.Bool
}

// Decrypt refuses the frame while the file exists, and warns once each time
// it starts to.
func (d *refusingDAVE) Decrypt(userID godave.UserID, frame, out []byte) (int, error) {
	if _, err := os.Stat(d.file); err != nil {
		d.refusing.Store(false)
		return d.Session.Decrypt(userID, frame, out)
	}
	if !d.refusing.Swap(true) {
		utils.Warn(smokeDecryptFailMessage, "recorder", string(d.recorder))
	}
	return 0, errSmokeDecryptFail
}

// ShouldHoldFrames is the session's own, as holdFrames reads it.
func (d *refusingDAVE) ShouldHoldFrames() bool { return holdFrames(d.Session) }

// newVoiceDialer is the production voice gateway dialer: the host's
// resolver, and the zone's nameservers reached through it. The smoke hook
// that makes voice servers unreachable is on when smokeDNSFailEnv names a
// file. drops is the smoke drop hook's, nil when it is off.
func newVoiceDialer(drops *connDrops) *voiceDialer {
	d := &voiceDialer{host: net.DefaultResolver, atNameserver: nameserverResolver, drops: drops}
	if failFile := os.Getenv(smokeDNSFailEnv); failFile != "" {
		d.unresolvable = func() bool {
			_, err := os.Stat(failFile)
			return err == nil
		}
	}
	return d
}

// watchSmokeDrops starts the watch of the smoke drop hook's file, and of the
// UDP cut hook's, for the recorder at its place in RECORDER_TOKENS, when
// smokeDropEnv or smokeUDPCutEnv names one, and is nil when neither does. A
// recorder takes one for its whole life, so each appearance of a file drops
// or cuts it once, however many times it has logged in.
func watchSmokeDrops(recorder int) *connDrops {
	dropFile, cutFile := os.Getenv(smokeDropEnv), os.Getenv(smokeUDPCutEnv)
	if dropFile == "" && cutFile == "" {
		return nil
	}
	c := &connDrops{recorder: recorder, live: make(map[*droppableConn]struct{})}
	if dropFile != "" {
		go c.watch(dropFile, "smoke-voice-drop", func(content string) {
			mode := dropAbrupt
			if strings.TrimSpace(content) == "silent" {
				mode = dropSilent
			}
			c.dropAll(mode)
		})
	}
	if cutFile != "" {
		go c.watch(cutFile, "smoke-voice-udp-cut", func(string) { c.cutUDP() })
	}
	return c
}

// nameserverResolver is a resolver that sends every query to the nameserver
// named host.
func nameserverResolver(host string) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(host, nameserverPort))
	}}
}

// websocket is the dialer disgo's voice gateways connect with: gorilla's
// default dialer, dialing through d.
func (d *voiceDialer) websocket() *websocket.Dialer {
	return &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 45 * time.Second,
		NetDialContext:   d.dialContext,
	}
}

// dialContext dials addr, a voice server's host and port, as dial does, and
// hands the connection to the smoke hook's drops when it is on.
func (d *voiceDialer) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := d.dial(ctx, network, addr)
	if err != nil || d.drops == nil {
		return conn, err
	}
	return d.drops.trackGateway(conn), nil
}

// dial dials addr, a voice server's host and port. It looks the host up at
// the host's resolver, and at the zone's nameservers when that answers "no
// such host".
func (d *voiceDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if d.unresolvable != nil && d.unresolvable() {
		return nil, &net.OpError{Op: "dial", Net: network,
			Err: &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}}
	}
	dialer := &net.Dialer{Resolver: d.host}
	conn, err := dialer.DialContext(ctx, network, addr)
	if !notFound(err) {
		return conn, err
	}
	addrs, lookupErr := d.atZone(ctx, host)
	if lookupErr != nil {
		return nil, errors.Join(err, lookupErr)
	}
	for _, a := range addrs {
		if conn, err = dialer.DialContext(ctx, network, net.JoinHostPort(a, port)); err == nil {
			return conn, nil
		}
	}
	return nil, err
}

// atZone looks host up at the nameservers of the zone it is in: the nearest
// name above it, short of a top-level domain, that has NS records.
func (d *voiceDialer) atZone(ctx context.Context, host string) ([]string, error) {
	err := error(&net.DNSError{Err: "no nameservers found", Name: host, IsNotFound: true})
	for zone := parentOf(host); zone != ""; zone = parentOf(zone) {
		nameservers, nsErr := d.host.LookupNS(ctx, zone)
		if nsErr != nil || len(nameservers) == 0 {
			continue
		}
		for _, ns := range nameservers {
			addrs, lookupErr := d.atNameserver(ns.Host).LookupHost(ctx, host)
			if lookupErr == nil && len(addrs) > 0 {
				return addrs, nil
			}
			err = lookupErr
		}
		return nil, err
	}
	return nil, err
}

// parentOf is the name above name, empty when that would be a top-level
// domain.
func parentOf(name string) string {
	_, parent, ok := strings.Cut(strings.TrimSuffix(name, "."), ".")
	if !ok || !strings.Contains(parent, ".") {
		return ""
	}
	return parent
}

// notFound reports whether err is a lookup answering "no such host".
func notFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// dropCheckEvery is how often the smoke hook looks for its drop file.
const dropCheckEvery = 250 * time.Millisecond

// dropMode is how a connection drops. dropAbrupt is a voice gateway
// connection the network broke: its next read fails as a reset does, which
// disgo resumes from, where a close of the recorder's own it doesn't.
// dropSilent is a network that stops passing traffic, audio included:
// reads get nothing but their own deadline or close, writes vanish, and
// neither end hears the connection close.
type dropMode int32

const (
	dropAbrupt dropMode = iota + 1
	dropSilent
)

// errDropReset is the read error of a connection dropped abruptly.
var errDropReset = &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}

// connDrops is the voice connections the smoke hooks can drop: the voice
// gateway connections, and the UDP sockets audio arrives on. Each time the
// drop file appears, every connection open then drops: silently when the
// file says "silent", abruptly otherwise. An abrupt drop leaves the UDP
// sockets alone, as a broken gateway connection does. Each time the UDP cut
// file appears, every UDP socket open then drops silently, and the voice
// gateway connections stay up. A connection opened while a file stays drops
// at its next appearance, so a recorder can connect again after a drop.
type connDrops struct {
	// recorder is the place in RECORDER_TOKENS of the recorder whose
	// connections these are.
	recorder int

	mu   sync.Mutex
	live map[*droppableConn]struct{}
}

// watch looks for file every dropCheckEvery, for as long as the bot runs,
// and hands appeared the file's content each time it appears. A panic is
// reported as the hook's, which hook names.
func (c *connDrops) watch(file, hook string, appeared func(content string)) {
	defer utils.RecoverPanic(hook)
	present := false
	for range time.Tick(dropCheckEvery) {
		content, err := os.ReadFile(file)
		if err == nil && !present {
			appeared(string(content))
		}
		present = err == nil
	}
}

// trackGateway hands back conn, a voice gateway connection, which the next
// drop drops.
func (c *connDrops) trackGateway(conn net.Conn) net.Conn { return c.track(conn, false) }

// trackSocket hands back conn, a UDP socket audio arrives on, which the next
// silent drop or UDP cut drops.
func (c *connDrops) trackSocket(conn net.Conn) net.Conn { return c.track(conn, true) }

func (c *connDrops) track(conn net.Conn, udp bool) net.Conn {
	dc := &droppableConn{Conn: conn, drops: c, udp: udp, closed: make(chan struct{})}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live[dc] = struct{}{}
	return dc
}

// smokeDropMessage is the WARN the smoke hook logs each time it drops a
// recorder's voice connections.
const smokeDropMessage = "Smoke hook dropping voice connections"

// dropAll drops every connection open now, the way mode says.
func (c *connDrops) dropAll(mode dropMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	utils.Warn(smokeDropMessage, "recorder", c.recorder, "connections", len(c.live), "silent", mode == dropSilent)
	for dc := range c.live {
		dc.drop(mode)
	}
}

// smokeUDPCutMessage is the WARN the smoke hook logs each time it cuts a
// recorder's UDP path.
const smokeUDPCutMessage = "Smoke hook cutting voice UDP path"

// cutUDP drops every UDP socket open now silently, as a NAT or a media node
// that stops passing the recorder's traffic does, and leaves the voice
// gateway connections up.
func (c *connDrops) cutUDP() {
	c.mu.Lock()
	defer c.mu.Unlock()
	sockets := 0
	for dc := range c.live {
		if dc.udp {
			dc.drop(dropSilent)
			sockets++
		}
	}
	utils.Warn(smokeUDPCutMessage, "recorder", c.recorder, "sockets", sockets)
}

func (c *connDrops) forget(dc *droppableConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.live, dc)
}

// droppableConn is a voice connection the smoke hooks can drop. udp is
// whether it is a UDP socket, which only a silent drop or a UDP cut drops.
type droppableConn struct {
	net.Conn
	drops *connDrops
	udp   bool
	// dropped is how the connection dropped, zero while it hasn't.
	dropped atomic.Int32
	// closed closes once the recorder closes the connection.
	closed    chan struct{}
	closeOnce sync.Once
}

// drop drops the connection the way mode says. An abrupt drop closes the
// socket underneath, which ends a read waiting on it.
func (dc *droppableConn) drop(mode dropMode) {
	if dc.udp && mode != dropSilent {
		return
	}
	dc.dropped.Store(int32(mode))
	if mode == dropAbrupt {
		_ = dc.Conn.Close()
	}
}

// Read reads as the connection underneath does until a drop. After an
// abrupt one it fails as a reset. After a silent one nothing arrives: what
// does is thrown away, and only the reader's own deadline or close ends the
// read, since the peer's close would never arrive either.
func (dc *droppableConn) Read(b []byte) (int, error) {
	for {
		n, err := dc.Conn.Read(b)
		switch dropMode(dc.dropped.Load()) {
		case dropAbrupt:
			return 0, errDropReset
		case dropSilent:
			var netErr net.Error
			switch {
			case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, net.ErrClosed):
				return 0, err
			case err != nil:
				<-dc.closed
				return 0, net.ErrClosed
			}
			continue
		}
		return n, err
	}
}

func (dc *droppableConn) Write(b []byte) (int, error) {
	if dropMode(dc.dropped.Load()) == dropSilent {
		return len(b), nil
	}
	return dc.Conn.Write(b)
}

func (dc *droppableConn) Close() error {
	dc.closeOnce.Do(func() {
		close(dc.closed)
		dc.drops.forget(dc)
	})
	return dc.Conn.Close()
}
