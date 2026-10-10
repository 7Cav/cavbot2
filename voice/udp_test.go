package voice

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
)

// The recorder's UDP connection against a fake voice server on localhost:
// the server answers IP discovery, then sends a voice packet the way
// Discord's voice server sends one to a listener.

// fakeVoiceServer is the voice server's UDP socket.
type fakeVoiceServer struct {
	conn net.PacketConn
}

func newFakeVoiceServer(t *testing.T) *fakeVoiceServer {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &fakeVoiceServer{conn: conn}
}

func (s *fakeVoiceServer) port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

// answerDiscovery reads the client's IP discovery request and answers it
// (Discord's voice docs, "IP Discovery"), and returns the client's address.
func (s *fakeVoiceServer) answerDiscovery(t *testing.T) net.Addr {
	t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(waitFor))
	req := make([]byte, 74)
	n, client, err := s.conn.ReadFrom(req)
	if err != nil || n != 74 || binary.BigEndian.Uint16(req[0:2]) != 1 {
		t.Errorf("IP discovery request = %d bytes %x, %v, want a 74-byte request", n, req[:n], err)
		return client
	}
	resp := make([]byte, 74)
	binary.BigEndian.PutUint16(resp[0:2], 2)
	binary.BigEndian.PutUint16(resp[2:4], 70)
	copy(resp[4:8], req[4:8])
	copy(resp[8:], "127.0.0.1")
	binary.BigEndian.PutUint16(resp[72:74], uint16(client.(*net.UDPAddr).Port))
	if _, err := s.conn.WriteTo(resp, client); err != nil {
		t.Errorf("IP discovery response: %v", err)
	}
	return client
}

// serverPacket is a voice packet as Discord's voice server sends it in the
// aead_aes256_gcm_rtpsize mode (Discord's voice docs, "Transport Encryption
// Modes"): the RTP header with the extension bit set, then the extension's
// 4-byte preamble, both in the clear and authenticated; then, encrypted,
// the extension's body and the payload; then the tag and the 4-byte nonce
// counter.
func serverPacket(t *testing.T, key []byte, ssrc, timestamp uint32, counter uint32, payload []byte) []byte {
	t.Helper()
	header := make([]byte, 12, 16)
	header[0] = 0x90 // version 2, extension
	header[1] = 0x78 // Discord's Opus payload type
	binary.BigEndian.PutUint16(header[2:4], 4321)
	binary.BigEndian.PutUint32(header[4:8], timestamp)
	binary.BigEndian.PutUint32(header[8:12], ssrc)
	header = append(header, 0xBE, 0xDE, 0x00, 0x01) // one-byte extensions, one word
	extension := []byte{0x10, 0x7F, 0x00, 0x00}     // ID 1, one byte: audio level; padding
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	binary.BigEndian.PutUint32(nonce, counter)
	sealed := gcm.Seal(nil, nonce, append(extension, payload...), header)
	pkt := append(append(header, sealed...), nonce[:4]...)
	return pkt
}

// A voice packet Discord's server sends reaches the reader with its SSRC,
// its RTP timestamp, and exactly its payload: the transport encryption and
// the header extension are off, and DAVE's encryption is left to the
// receiver.
func TestUDPConnReadsAPacketAsDiscordsServerSendsIt(t *testing.T) {
	server := newFakeVoiceServer(t)
	conn := newUDPConn(godave.NewNoopSession(slog.New(slog.DiscardHandler), "", nil), nil)
	t.Cleanup(func() { _ = conn.Close() })

	discovered := make(chan net.Addr, 1)
	go func() { discovered <- server.answerDiscovery(t) }()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if _, _, err := conn.Open(ctx, "127.0.0.1", server.port(), 42); err != nil {
		t.Fatalf("Open: %v", err)
	}
	client := <-discovered
	key := bytes.Repeat([]byte{7}, 32)
	if err := conn.SetSecretKey(disgovoice.EncryptionModeAEADAES256GCMRTPSize, key); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}

	payload := []byte("dave-encrypted opus frame")
	if _, err := server.conn.WriteTo(serverPacket(t, key, 9001, 123456, 1, payload), client); err != nil {
		t.Fatalf("send: %v", err)
	}

	deadline := time.Now().Add(waitFor)
	for {
		p, err := conn.ReadPacket()
		if errors.Is(err, os.ErrDeadlineExceeded) && time.Now().Before(deadline) {
			continue
		}
		if err != nil {
			t.Fatalf("ReadPacket: %v", err)
		}
		if p.SSRC != 9001 || p.Timestamp != 123456 || !bytes.Equal(p.Opus, payload) {
			t.Errorf("packet read = SSRC %d, timestamp %d, payload %q; want 9001, 123456, %q",
				p.SSRC, p.Timestamp, p.Opus, payload)
		}
		return
	}
}
