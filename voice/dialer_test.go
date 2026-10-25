package voice

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/dns/dnsmessage"
)

// The recorder's voice gateway dialer, against a voice gateway on localhost
// that holds a certificate for a voice server's hostname. The host's
// resolver and the zone's nameservers are DNS servers the test plays, which
// a *net.Resolver reaches through its Dial.

// voiceHost is the voice server the call moved to, in discord.media.
const voiceHost = "c-hel02-2d506172.discord.media"

// zoneNameserver is the nameserver discord.media's NS record names.
const zoneNameserver = "ns1.example.net."

// fakeDNS answers DNS queries from its records, authoritatively, and
// answers "no such host" for a name it holds none for.
type fakeDNS struct {
	mu sync.Mutex
	// a maps a name to its IPv4 addresses, and ns a zone to its
	// nameservers. Names end with a dot.
	a, ns map[string][]string
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{a: make(map[string][]string), ns: make(map[string][]string)}
}

// addA gives name the IPv4 address addr.
func (d *fakeDNS) addA(name, addr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.a[name+"."] = append(d.a[name+"."], addr)
}

// addNS names nameserver as one of zone's.
func (d *fakeDNS) addNS(zone, nameserver string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ns[zone+"."] = append(d.ns[zone+"."], nameserver)
}

// resolver is a *net.Resolver that asks d, over a pipe, whatever server
// the host's configuration names.
func (d *fakeDNS) resolver() *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go d.serve(server)
		return client, nil
	}}
}

// serve answers the queries that arrive on conn, framed as over TCP: each
// message after its length in two bytes.
func (d *fakeDNS) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	for {
		var size [2]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		answer, err := d.answer(query)
		if err != nil {
			return
		}
		framed := binary.BigEndian.AppendUint16(nil, uint16(len(answer)))
		if _, err := conn.Write(append(framed, answer...)); err != nil {
			return
		}
	}
}

// answer is d's answer to one query.
func (d *fakeDNS) answer(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}
	name := q.Name.String()
	d.mu.Lock()
	addrs, zone := d.a[name], d.ns[name]
	d.mu.Unlock()
	rcode := dnsmessage.RCodeSuccess
	if addrs == nil && zone == nil {
		rcode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true,
		RecursionAvailable: true, RCode: rcode})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}
	rh := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}
	switch q.Type {
	case dnsmessage.TypeA:
		for _, a := range addrs {
			if err := b.AResource(rh, dnsmessage.AResource{A: netip.MustParseAddr(a).As4()}); err != nil {
				return nil, err
			}
		}
	case dnsmessage.TypeNS:
		for _, ns := range zone {
			if err := b.NSResource(rh, dnsmessage.NSResource{NS: dnsmessage.MustNewName(ns)}); err != nil {
				return nil, err
			}
		}
	}
	return b.Finish()
}

// voiceGateway is a voice gateway on localhost, served over TLS with a
// certificate for voiceHost. It records the server name each TLS handshake
// asked for and the Host each upgrade carried.
type voiceGateway struct {
	srv   *httptest.Server
	roots *x509.CertPool

	mu         sync.Mutex
	sni, hosts []string
}

func newVoiceGateway(t *testing.T) *voiceGateway {
	t.Helper()
	g := &voiceGateway{}
	cert, roots := certFor(t, voiceHost)
	g.roots = roots
	upgrader := websocket.Upgrader{}
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.sni, g.hosts = append(g.sni, r.TLS.ServerName), append(g.hosts, r.Host)
		g.mu.Unlock()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			_ = conn.Close()
		}
	}))
	g.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	g.srv.StartTLS()
	t.Cleanup(g.srv.Close)
	return g
}

// url is the gateway's address as disgo dials it, by voiceHost's name.
func (g *voiceGateway) url() string {
	_, port, _ := net.SplitHostPort(g.srv.Listener.Addr().String())
	return "wss://" + net.JoinHostPort(voiceHost, port) + "?v=8"
}

// seen is the server names and Hosts the gateway was reached with.
func (g *voiceGateway) seen() (sni, hosts []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sni, g.hosts
}

// certFor is a self-signed certificate for host, and a pool that trusts it.
func certFor(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

// dnsScene is the DNS a recorder looks a voice server up in: the host's
// resolver, which names discord.media's nameserver and knows nothing of
// voiceHost, and that nameserver.
type dnsScene struct {
	host, zone *fakeDNS
}

func newDNSScene() *dnsScene {
	sc := &dnsScene{host: newFakeDNS(), zone: newFakeDNS()}
	sc.host.addNS("discord.media", zoneNameserver)
	return sc
}

// dialer is the recorder's voice gateway dialer over the scene, trusting
// gw's certificate.
func (sc *dnsScene) dialer(gw *voiceGateway) *websocket.Dialer {
	d := (&voiceDialer{
		host: sc.host.resolver(),
		atNameserver: func(host string) *net.Resolver {
			if host == zoneNameserver {
				return sc.zone.resolver()
			}
			return newFakeDNS().resolver()
		},
	}).websocket()
	d.TLSClientConfig = &tls.Config{RootCAs: gw.roots}
	return d
}

// dial dials gw's voice gateway with d, as disgo's voice gateway does.
func dial(t *testing.T, d *websocket.Dialer, gw *voiceGateway) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	conn, _, err := d.DialContext(ctx, gw.url(), nil)
	if err == nil {
		_ = conn.Close()
	}
	return err
}

// The host's resolver can keep answering "no such host" for a voice server
// whose record is newer than the resolver's answer, as discord.media's
// 30-minute negative caching lets it. The recorder asks the zone's own
// nameservers then, and connects at the address they give, with the voice
// server's hostname for TLS and Host, so the certificate is checked
// against that name.
func TestVoiceDialerConnectsWhereTheZonesNameserversSay(t *testing.T) {
	gw := newVoiceGateway(t)
	sc := newDNSScene()
	sc.zone.addA(voiceHost, "127.0.0.1")

	if err := dial(t, sc.dialer(gw), gw); err != nil {
		t.Fatalf("dial %s: %v", gw.url(), err)
	}

	sni, hosts := gw.seen()
	_, port, _ := net.SplitHostPort(gw.srv.Listener.Addr().String())
	if len(sni) != 1 || sni[0] != voiceHost || hosts[0] != net.JoinHostPort(voiceHost, port) {
		t.Errorf("voice gateway reached with server names %v and Hosts %v, want %s", sni, hosts, voiceHost)
	}
}

// When the zone's nameservers don't know the voice server either, the dial
// fails, and the next dial asks both again: once the nameservers have the
// record, it connects.
func TestVoiceDialerAsksAgainOnEachDial(t *testing.T) {
	gw := newVoiceGateway(t)
	sc := newDNSScene()
	d := sc.dialer(gw)
	if err := dial(t, d, gw); err == nil {
		t.Fatal("dial with no record anywhere connected, want it to fail")
	}

	sc.zone.addA(voiceHost, "127.0.0.1")

	if err := dial(t, d, gw); err != nil {
		t.Errorf("dial once the nameserver has the record: %v", err)
	}
}

// A smoke run drops the recorders' voice connections on demand through the
// file SMOKE_VOICE_DROP_FILE names (docs/smoke-test.md). Dropped abruptly, a
// live connection's next read fails at once as a connection the network
// broke does, which disgo resumes from. It isn't a close of the recorder's
// own, which disgo doesn't resume from, so a smoke check of the drop would
// pass without reaching the resume.
func TestSmokeDropFailsALiveConnectionsReadAsTheNetworkDoes(t *testing.T) {
	dropFile := filepath.Join(t.TempDir(), "drop")
	t.Setenv(smokeDropEnv, dropFile)
	server := newQuietVoiceServer(t)
	d := newVoiceDialer(smokeDrops()).websocket()
	d.TLSClientConfig = server.dialer().TLSClientConfig
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	conn, _, err := d.DialContext(ctx, "wss://"+server.endpoint(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := os.WriteFile(dropFile, []byte("abrupt\n"), 0o600); err != nil {
		t.Fatalf("writing the drop file: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(waitFor))
	_, _, err = conn.ReadMessage()
	var netErr net.Error
	switch {
	case err == nil:
		t.Fatal("read after the drop = a message, want it to fail")
	case errors.As(err, &netErr) && netErr.Timeout():
		t.Fatalf("read after the drop = %v, want it to fail at the drop", err)
	case errors.Is(err, net.ErrClosed):
		t.Errorf("read after the drop = %v, want a connection error, not the recorder's own close", err)
	}
}

// The DNS switch a smoke run sets, SMOKE_VOICE_DNS_FAIL_FILE, works beside
// the drop switch: while its file exists, a dial of a voice server fails as
// a lookup answering "no such host", and once the file is gone it connects.
func TestSmokeDNSFailMakesVoiceServersUnreachableWhileItsFileExists(t *testing.T) {
	failFile := filepath.Join(t.TempDir(), "dns-fail")
	t.Setenv(smokeDNSFailEnv, failFile)
	t.Setenv(smokeDropEnv, filepath.Join(t.TempDir(), "drop"))
	server := newQuietVoiceServer(t)
	d := newVoiceDialer(smokeDrops()).websocket()
	d.TLSClientConfig = server.dialer().TLSClientConfig
	dialServer := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()
		conn, _, err := d.DialContext(ctx, "wss://"+server.endpoint(), nil)
		if err == nil {
			_ = conn.Close()
		}
		return err
	}
	if err := os.WriteFile(failFile, nil, 0o600); err != nil {
		t.Fatalf("writing the DNS fail file: %v", err)
	}

	var dnsErr *net.DNSError
	if err := dialServer(); !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
		t.Errorf("dial with the DNS fail file = %v, want no such host", err)
	}
	if err := os.Remove(failFile); err != nil {
		t.Fatalf("removing the DNS fail file: %v", err)
	}
	if err := dialServer(); err != nil {
		t.Errorf("dial once the DNS fail file is gone = %v, want it to connect", err)
	}
}
