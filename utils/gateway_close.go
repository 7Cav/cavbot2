package utils

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"net"

	"github.com/gorilla/websocket"
)

// gatewayDialer is the websocket dialer a watched session logs in through.
// Each connection it dials hands onClose the code of a close frame Discord
// sends on it, which discordgo tells no handler of. It does a wss URL's TLS
// itself, so what the connection reads is the websocket's frames. It takes
// no proxy, as none is set where the bot runs.
func gatewayDialer(onClose func(code int)) *websocket.Dialer {
	dial := func(d contextDialer) func(context.Context, string, string) (net.Conn, error) {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &closeReader{Conn: conn, onClose: onClose}, nil
		}
	}
	return &websocket.Dialer{
		NetDialContext:    dial(&net.Dialer{}),
		NetDialTLSContext: dial(&tls.Dialer{}),
		HandshakeTimeout:  websocket.DefaultDialer.HandshakeTimeout,
	}
}

// contextDialer is net.Dialer or tls.Dialer.
type contextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// endOfHeaders is the blank line that ends the response to the upgrade.
const endOfHeaders = "\r\n\r\n"

// closeReader is a gateway connection that reads its incoming frames as
// they pass, for the code of a close frame. It skips the response to the
// upgrade, then each frame's payload by the length its header gives, so it
// reads a code from a close frame alone. Discord masks no frame it sends
// (RFC 6455 5.1), and gorilla refuses one that is masked.
//
// One goroutine reads the connection at a time: discordgo's Open, then the
// listener it starts.
type closeReader struct {
	net.Conn
	onClose func(code int)

	// matched is how much of endOfHeaders has passed, and upgraded is set
	// once all of it has.
	matched  int
	upgraded bool
	// header is the frame header read so far. left is how many bytes of the
	// frame's payload are still to pass, and code is the part of a close
	// frame's code read so far.
	header  []byte
	left    uint64
	closing bool
	code    []byte
}

func (c *closeReader) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.scan(p[:n])
	return n, err
}

// scan reads b, the bytes that came in next.
func (c *closeReader) scan(b []byte) {
	for len(b) > 0 {
		switch {
		case !c.upgraded:
			c.matchEndOfHeaders(b[0])
			b = b[1:]
		case c.left > 0 && c.closing && len(c.code) < 2:
			c.code = append(c.code, b[0])
			c.left--
			b = b[1:]
			if len(c.code) == 2 {
				c.onClose(int(binary.BigEndian.Uint16(c.code)))
			}
		case c.left > 0:
			n := min(c.left, uint64(len(b)))
			c.left -= n
			b = b[n:]
		default:
			c.header = append(c.header, b[0])
			b = b[1:]
			c.readHeader()
		}
	}
}

func (c *closeReader) matchEndOfHeaders(x byte) {
	switch x {
	case endOfHeaders[c.matched]:
		c.matched++
	case '\r':
		c.matched = 1
	default:
		c.matched = 0
	}
	c.upgraded = c.matched == len(endOfHeaders)
}

// readHeader starts the frame once its header is all read. The header's
// second byte gives the payload's length, or says the next 2 or 8 bytes do.
func (c *closeReader) readHeader() {
	h := c.header
	if len(h) < 2 {
		return
	}
	switch length := h[1] & 0x7f; {
	case length == 126 && len(h) == 4:
		c.left = uint64(binary.BigEndian.Uint16(h[2:]))
	case length == 127 && len(h) == 10:
		c.left = binary.BigEndian.Uint64(h[2:])
	case length < 126:
		c.left = uint64(length)
	default:
		return
	}
	c.closing = int(h[0]&0x0f) == websocket.CloseMessage
	c.code = c.code[:0]
	c.header = c.header[:0]
}
