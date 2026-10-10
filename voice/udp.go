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
	// stopKeepalive ends the keepalive of the socket open now.
	stopKeepalive chan struct{}

	// buf is the receive buffer. Only the receiver reads.
	buf []byte
}

// newUDPConn is a disgo UDPConnCreateFunc. The SSRC lookup goes unused:
// DAVE decryption, which needs the speaker, is the receiver's.
func newUDPConn(dave godave.Session, _ disgovoice.SsrcLookupFunc, _ ...disgovoice.UDPConnConfigOpt) disgovoice.UDPConn {
	return &udpConn{dave: dave, buf: make([]byte, 1500)}
}

// current is the socket and the transport encrypter in use now.
func (u *udpConn) current() (net.Conn, disgovoice.Encrypter) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.conn, u.encrypter
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
	addr, ourPort, err := discoverIP(conn, ssrc)
	if err != nil {
		_ = conn.Close()
		return "", 0, err
	}
	u.dave.AssignSsrcToCodec(ssrc, godave.CodecOpus)

	stop := make(chan struct{})
	u.mu.Lock()
	if u.conn != nil {
		_ = u.conn.Close()
	}
	if u.stopKeepalive != nil {
		close(u.stopKeepalive)
	}
	u.conn, u.stopKeepalive = conn, stop
	u.mu.Unlock()
	go keepalive(conn, stop)
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
// until stop, as Discord's own JavaScript library does.
func keepalive(conn net.Conn, stop chan struct{}) {
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

// ReadPacket reads the next voice packet, with the transport encryption,
// the header extension and any RTP padding off and DAVE's encryption still
// on. It waits at most udpReadWait, then returns os.ErrDeadlineExceeded.
// Before Open and SetSecretKey it returns errUDPNotOpen, and after Close
// net.ErrClosed. The payload is the caller's to keep.
func (u *udpConn) ReadPacket() (*disgovoice.Packet, error) {
	conn, enc := u.current()
	if conn == nil || enc == nil {
		return nil, errUDPNotOpen
	}
	if err := conn.SetReadDeadline(time.Now().Add(udpReadWait)); err != nil {
		return nil, err
	}
	for {
		n, err := conn.Read(u.buf)
		if err != nil {
			return nil, err
		}
		p, ok, err := readRTP(u.buf[:n], enc)
		if err != nil {
			return nil, err
		}
		if ok {
			return p, nil
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

// Close closes the socket and stops its keepalive. disgo may open it again.
func (u *udpConn) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stopKeepalive != nil {
		close(u.stopKeepalive)
		u.stopKeepalive = nil
	}
	if u.conn == nil {
		return nil
	}
	return u.conn.Close()
}

func (u *udpConn) LocalAddr() net.Addr {
	conn, _ := u.current()
	if conn == nil {
		return nil
	}
	return conn.LocalAddr()
}

func (u *udpConn) RemoteAddr() net.Addr {
	conn, _ := u.current()
	if conn == nil {
		return nil
	}
	return conn.RemoteAddr()
}

func (u *udpConn) SetDeadline(t time.Time) error {
	conn, _ := u.current()
	if conn == nil {
		return errUDPNotOpen
	}
	return conn.SetDeadline(t)
}

func (u *udpConn) SetReadDeadline(t time.Time) error {
	conn, _ := u.current()
	if conn == nil {
		return errUDPNotOpen
	}
	return conn.SetReadDeadline(t)
}

func (u *udpConn) SetWriteDeadline(t time.Time) error {
	conn, _ := u.current()
	if conn == nil {
		return errUDPNotOpen
	}
	return conn.SetWriteDeadline(t)
}
