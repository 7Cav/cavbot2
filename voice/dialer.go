package voice

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

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
}

// smokeDNSFailEnv names a file whose existence makes every voice server
// unreachable to the recorders, as voiceDialer's unresolvable says. Only a
// smoke run sets it: the compose file's environment block doesn't carry it.
const smokeDNSFailEnv = "SMOKE_VOICE_DNS_FAIL_FILE"

// newVoiceDialer is the production voice gateway dialer: the host's
// resolver, and the zone's nameservers reached through it. The smoke hook
// is on when smokeDNSFailEnv names a file.
func newVoiceDialer() *voiceDialer {
	d := &voiceDialer{host: net.DefaultResolver, atNameserver: nameserverResolver}
	if failFile := os.Getenv(smokeDNSFailEnv); failFile != "" {
		d.unresolvable = func() bool {
			_, err := os.Stat(failFile)
			return err == nil
		}
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

// dialContext dials addr, a voice server's host and port. It looks the host
// up at the host's resolver, and at the zone's nameservers when that answers
// "no such host".
func (d *voiceDialer) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
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
