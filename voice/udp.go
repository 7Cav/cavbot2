package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/7cav/cavbot2/utils"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
)

// The recorder's UDP connection to Discord's voice server, in place of
// disgo's. disgo's takes both encryptions off in one read, so a frame whose
// SSRC op 5 hasn't mapped yet is decrypted for no user and lost. This one
// takes the transport encryption off and leaves DAVE's to the receiver,
// which knows the speaker. It also sends a keepalive, which disgo's lacks:
// a recorder never speaks, so nothing else leaves the socket, and a NAT can
// stop delivering audio through a long silence. A recorder sends no audio,
// so Write refuses.

const (
	// udpReadWait bounds one read, so the receiver looks at its held
	// frames and its stop at least this often.
	udpReadWait = 100 * time.Millisecond
	// keepaliveInterval is how often the keepalive goes out.
	keepaliveInterval = 5 * time.Second
	// discoverySize is an IP discovery packet's size, request and answer.
	discoverySize = 74
	// discoveryWait bounds the IP discovery exchange.
	discoveryWait = 5 * time.Second
)

// errNoAudioSent is Write's answer: a recorder sends no audio.
var errNoAudioSent = errors.New("voice: a recorder sends no audio")

// errUDPNotOpen is a read before disgo has opened the UDP connection and
// set its key.
var errUDPNotOpen = errors.New("voice: UDP connection not open")

// udpConn is a recorder's UDP connection. disgo opens it again on the same
// value after a voice reconnect.
type udpConn struct {
	dave godave.Session

	mu        sync.Mutex
	conn      net.Conn
	encrypter disgovoice.Encrypter
	// sockets counts the sockets Open has opened. The one open now is
	// socket number sockets.
	sockets int
	// stopKeepalive ends the keepalive of the socket open now, and
	// keepaliveDone closes once it has returned.
	stopKeepalive chan struct{}
	keepaliveDone chan struct{}

	// buf is the receive buffer. Only the receiver reads.
	buf []byte
	// last is when the last datagram arrived, in Unix nanoseconds, zero
	// before any has.
	last atomic.Int64
	// drops, when set, can drop each socket, as the smoke hook does.
	drops *connDrops
}

// newUDPConn is a disgo UDPConnCreateFunc. The SSRC lookup goes unused:
// DAVE decryption, which needs the speaker, is the receiver's.
func newUDPConn(dave godave.Session, _ disgovoice.SsrcLookupFunc, _ ...disgovoice.UDPConnConfigOpt) disgovoice.UDPConn {
	return &udpConn{dave: dave, buf: make([]byte, 1500)}
}

// droppableUDPConns is newUDPConn whose sockets drops, when set, can drop,
// as the smoke hook does.
func droppableUDPConns(drops *connDrops) disgovoice.UDPConnCreateFunc {
	return func(dave godave.Session, lookup disgovoice.SsrcLookupFunc, opts ...disgovoice.UDPConnConfigOpt) disgovoice.UDPConn {
		u := newUDPConn(dave, lookup, opts...).(*udpConn)
		u.drops = drops
		return u
	}
}

// current is the socket and the transport encrypter in use now, and the
// socket's number.
func (u *udpConn) current() (net.Conn, disgovoice.Encrypter, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.conn, u.encrypter, u.sockets
}

// Open dials the voice server, runs IP discovery (Discord's voice docs, "IP
// Discovery"), and starts the keepalive. A socket from before a reconnect is
// closed.
func (u *udpConn) Open(ctx context.Context, ip string, port int, ssrc uint32) (string, int, error) {
	dialer := net.Dialer{Timeout: disgovoice.UDPTimeout}
	conn, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return "", 0, fmt.Errorf("dial voice server: %w", err)
	}
	if u.drops != nil {
		conn = u.drops.trackSocket(conn)
	}
	addr, ourPort, err := discoverIP(conn, ssrc)
	if err != nil {
		_ = conn.Close()
		return "", 0, err
	}
	u.dave.AssignSsrcToCodec(ssrc, godave.CodecOpus)

	stop, done := make(chan struct{}), make(chan struct{})
	u.mu.Lock()
	if u.conn != nil {
		_ = u.conn.Close()
	}
	if u.stopKeepalive != nil {
		close(u.stopKeepalive)
	}
	u.conn, u.stopKeepalive, u.keepaliveDone = conn, stop, done
	u.sockets++
	u.mu.Unlock()
	go keepalive(conn, stop, done)
	return addr, ourPort, nil
}

// discoverIP asks the voice server for the address and port it sees this
// socket at, which the select protocol message carries.
func discoverIP(conn net.Conn, ssrc uint32) (string, int, error) {
	req := make([]byte, discoverySize)
	binary.BigEndian.PutUint16(req[0:2], 1)
	binary.BigEndian.PutUint16(req[2:4], 70)
	binary.BigEndian.PutUint32(req[4:8], ssrc)
	if err := conn.SetDeadline(time.Now().Add(discoveryWait)); err != nil {
		return "", 0, err
	}
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if _, err := conn.Write(req); err != nil {
		return "", 0, fmt.Errorf("IP discovery request: %w", err)
	}
	resp := make([]byte, discoverySize)
	if _, err := conn.Read(resp); err != nil {
		return "", 0, fmt.Errorf("IP discovery answer: %w", err)
	}
	if binary.BigEndian.Uint16(resp[0:2]) != 2 || binary.BigEndian.Uint16(resp[2:4]) != 70 ||
		binary.BigEndian.Uint32(resp[4:8]) != ssrc {
		return "", 0, errors.New("IP discovery answer malformed")
	}
	return string(bytes.TrimRight(resp[8:72], "\x00")), int(binary.BigEndian.Uint16(resp[72:74])), nil
}

// keepalive sends 8 bytes, a little-endian counter, every keepaliveInterval
// until stop, as Discord's own JavaScript library does. It closes done as it
// returns.
func keepalive(conn net.Conn, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer utils.RecoverPanic("recorder-keepalive")
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	buf := make([]byte, 8)
	for counter := uint32(0); ; counter++ {
		binary.LittleEndian.PutUint32(buf, counter)
		if _, err := conn.Write(buf); err != nil && !errors.Is(err, net.ErrClosed) {
			utils.Debug("Voice keepalive not sent", "error", err)
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// SetSecretKey sets the transport encryption the session description
// chose.
func (u *udpConn) SetSecretKey(mode disgovoice.EncryptionMode, key []byte) error {
	enc, err := disgovoice.NewEncrypter(mode, key)
	if err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.encrypter = enc
	return nil
}

// datagram is one datagram the UDP connection read: the socket it arrived
// on, counted from 1 by Open, and when it arrived. rtcp is its RTCP packet
// type when it is RTCP, zero otherwise. packet is the voice packet it
// carried, nil when it carried none, and err why it couldn't be read, nil
// when it could.
type datagram struct {
	socket int
	at     time.Time
	rtcp   byte
	packet *disgovoice.Packet
	err    error
}

// readDatagram reads the next datagram, of any kind, noting when it arrived
// before it takes it apart. A voice packet comes with the transport
// encryption, the header extension and any RTP padding off and DAVE's
// encryption still on, its payload the caller's to keep. It waits at most
// udpReadWait, then returns os.ErrDeadlineExceeded. Before Open and
// SetSecretKey it returns errUDPNotOpen, and after Close net.ErrClosed. Its
// datagram names the socket read even when nothing arrived.
func (u *udpConn) readDatagram() (datagram, error) {
	conn, enc, socket := u.current()
	d := datagram{socket: socket}
	if conn == nil || enc == nil {
		return d, errUDPNotOpen
	}
	if err := conn.SetReadDeadline(time.Now().Add(udpReadWait)); err != nil {
		return d, err
	}
	n, err := conn.Read(u.buf)
	if err != nil {
		return d, err
	}
	d.at = time.Now()
	u.last.Store(d.at.UnixNano())
	b := u.buf[:n]
	if isRTCP(b) {
		d.rtcp = b[1]
		return d, nil
	}
	d.packet, _, d.err = readRTP(b, enc)
	return d, nil
}

// lastDatagram is when the last datagram arrived on any of the connection's
// sockets, zero before any has.
func (u *udpConn) lastDatagram() time.Time {
	if last := u.last.Load(); last != 0 {
		return time.Unix(0, last)
	}
	return time.Time{}
}

// rtcpReceiverReport is the RTCP packet type of a receiver report (RFC
// 3550). The voice server sends one every second, even to a recorder alone
// in the channel.
const rtcpReceiverReport = 201

// isRTCP reports whether b is an RTCP datagram: version 2, with a packet
// type in the range RTCP takes (RFC 5761, section 4). In the rtpsize modes
// its first 8 bytes are in the clear.
func isRTCP(b []byte) bool {
	return len(b) >= 8 && b[0]>>6 == 2 && b[1] >= 192 && b[1] <= 223
}

// ReadPacket reads the next voice packet, as readDatagram reads it,
// skipping datagrams that carry none. It waits at most udpReadWait for each
// datagram.
func (u *udpConn) ReadPacket() (*disgovoice.Packet, error) {
	for {
		d, err := u.readDatagram()
		switch {
		case err != nil:
			return nil, err
		case d.err != nil:
			return nil, d.err
		case d.packet != nil:
			return d.packet, nil
		}
	}
}

// readRTP takes a datagram apart (RFC 3550, and Discord's voice docs,
// "Transport Encryption Modes"). In the rtpsize modes the fixed header, any
// CSRCs and the extension's 4-byte preamble are in the clear, and the
// extension's body is encrypted with the payload. false for a datagram that
// isn't a voice packet or carries no media, such as RTP padding alone.
func readRTP(b []byte, enc disgovoice.Encrypter) (*disgovoice.Packet, bool, error) {
	if len(b) < disgovoice.RTPHeaderSize || b[1] != disgovoice.RTPPayloadType {
		return nil, false, nil
	}
	p := &disgovoice.Packet{
		Type:         b[1],
		Sequence:     binary.BigEndian.Uint16(b[2:4]),
		Timestamp:    binary.BigEndian.Uint32(b[4:8]),
		SSRC:         binary.BigEndian.Uint32(b[8:12]),
		HasExtension: b[0]&0x10 != 0,
	}
	hasPadding := b[0]&0x20 != 0
	headerSize := disgovoice.RTPHeaderSize + 4*int(b[0]&0x0F)
	extensionSize := 0
	if p.HasExtension {
		if len(b) < headerSize+4 {
			return nil, false, nil
		}
		extensionSize = 4 * int(binary.BigEndian.Uint16(b[headerSize+2:headerSize+4]))
		headerSize += 4
	}
	p.HeaderSize = headerSize
	if len(b) < headerSize+4 {
		return nil, false, nil
	}
	decrypted, err := enc.Decrypt(headerSize, b)
	if err != nil {
		return nil, false, fmt.Errorf("transport decrypt: %w", err)
	}
	if extensionSize > len(decrypted) {
		return nil, false, nil
	}
	payload := decrypted[extensionSize:]
	if hasPadding && len(payload) > 0 {
		if pad := int(payload[len(payload)-1]); pad > 0 && pad <= len(payload) {
			payload = payload[:len(payload)-pad]
		}
	}
	if len(payload) == 0 {
		return nil, false, nil
	}
	p.Opus = bytes.Clone(payload)
	return p, true, nil
}

// Read reads the next packet's payload into b.
func (u *udpConn) Read(b []byte) (int, error) {
	p, err := u.ReadPacket()
	if err != nil {
		return 0, err
	}
	return copy(b, p.Opus), nil
}

// Write refuses: a recorder sends no audio.
func (u *udpConn) Write([]byte) (int, error) { return 0, errNoAudioSent }

// Close closes the socket and returns once its keepalive has stopped. disgo
// may open it again.
func (u *udpConn) Close() error {
	u.mu.Lock()
	var err error
	if u.conn != nil {
		err = u.conn.Close()
	}
	stop, done := u.stopKeepalive, u.keepaliveDone
	u.stopKeepalive, u.keepaliveDone = nil, nil
	u.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	return err
}

func (u *udpConn) LocalAddr() net.Addr {
	conn, _, _ := u.current()
	if conn == nil {
		return nil
	}
	return conn.LocalAddr()
}

func (u *udpConn) RemoteAddr() net.Addr {
	conn, _, _ := u.current()
	if conn == nil {
		return nil
	}
	return conn.RemoteAddr()
}

func (u *udpConn) SetDeadline(t time.Time) error {
	conn, _, _ := u.current()
	if conn == nil {
		return errUDPNotOpen
	}
	return conn.SetDeadline(t)
}

func (u *udpConn) SetReadDeadline(t time.Time) error {
	conn, _, _ := u.current()
	if conn == nil {
		return errUDPNotOpen
	}
	return conn.SetReadDeadline(t)
}

func (u *udpConn) SetWriteDeadline(t time.Time) error {
	conn, _, _ := u.current()
	if conn == nil {
		return errUDPNotOpen
	}
	return conn.SetWriteDeadline(t)
}
