package voice

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/7cav/cavbot2/utils"
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

// newVoiceDialer is the production voice gateway dialer: the host's
// resolver, and the zone's nameservers reached through it. Each smoke hook
// is on when its variable, smokeDNSFailEnv or smokeDropEnv, names a file.
func newVoiceDialer() *voiceDialer {
	d := &voiceDialer{host: net.DefaultResolver, atNameserver: nameserverResolver}
	if failFile := os.Getenv(smokeDNSFailEnv); failFile != "" {
		d.unresolvable = func() bool {
			_, err := os.Stat(failFile)
			return err == nil
		}
	}
	if dropFile := os.Getenv(smokeDropEnv); dropFile != "" {
		d.drops = watchDrops(dropFile)
	}
	return d
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

// connDrops is the voice connections the smoke hook can drop: the voice
// gateway connections, and the UDP sockets audio arrives on. Each time the
// drop file appears, every connection open then drops: silently when the
// file says "silent", abruptly otherwise. An abrupt drop leaves the UDP
// sockets alone, as a broken gateway connection does. A connection opened
// while the file stays drops at its next appearance, so a recorder can
// connect again after a drop.
type connDrops struct {
	mu   sync.Mutex
	live map[*droppableConn]struct{}
}

// watchDrops starts looking for file every dropCheckEvery, for as long as
// the bot runs.
func watchDrops(file string) *connDrops {
	c := &connDrops{live: make(map[*droppableConn]struct{})}
	go func() {
		defer utils.RecoverPanic("smoke-voice-drop")
		present := false
		for range time.Tick(dropCheckEvery) {
			content, err := os.ReadFile(file)
			appeared := err == nil && !present
			present = err == nil
			if appeared {
				mode := dropAbrupt
				if strings.TrimSpace(string(content)) == "silent" {
					mode = dropSilent
				}
				c.dropAll(mode)
			}
		}
	}()
	return c
}

// trackGateway hands back conn, a voice gateway connection, which the next
// drop drops.
func (c *connDrops) trackGateway(conn net.Conn) net.Conn { return c.track(conn, false) }

// trackSocket hands back conn, a UDP socket audio arrives on, which the next
// silent drop drops.
func (c *connDrops) trackSocket(conn net.Conn) net.Conn { return c.track(conn, true) }

func (c *connDrops) track(conn net.Conn, silentOnly bool) net.Conn {
	dc := &droppableConn{Conn: conn, drops: c, silentOnly: silentOnly, closed: make(chan struct{})}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live[dc] = struct{}{}
	return dc
}

// dropAll drops every connection open now, the way mode says.
func (c *connDrops) dropAll(mode dropMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	utils.Warn("Smoke hook dropping voice connections", "connections", len(c.live), "silent", mode == dropSilent)
	for dc := range c.live {
		dc.drop(mode)
	}
}

func (c *connDrops) forget(dc *droppableConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.live, dc)
}

// droppableConn is a voice connection the smoke hook can drop. silentOnly
// is a UDP socket, which only a silent drop drops.
type droppableConn struct {
	net.Conn
	drops      *connDrops
	silentOnly bool
	// dropped is how the connection dropped, zero while it hasn't.
	dropped atomic.Int32
	// closed closes once the recorder closes the connection.
	closed    chan struct{}
	closeOnce sync.Once
}

// drop drops the connection the way mode says. An abrupt drop closes the
// socket underneath, which ends a read waiting on it.
func (dc *droppableConn) drop(mode dropMode) {
	if dc.silentOnly && mode != dropSilent {
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
