package main

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
)

// keepaliveUDPConn is disgo's own UDPConn plus a keepalive, which disgo lacks.
// A recorder never speaks, so without one nothing leaves the socket after the
// IP discovery packet, and NAT or Discord may stop delivering audio during a
// long silence. The packet copies @discordjs/voice: 8 bytes holding a
// little-endian counter, every 5 seconds.
//
// disgo's UDPConn gives no way to write a raw datagram, so the dialer's
// Control hook keeps a duplicate descriptor of the socket disgo dials, and the
// keepalive writes through that. Unix only, which is fine for a prototype.
type keepaliveUDPConn struct {
	voice.UDPConn
	interval time.Duration
	st       *stats

	mu   sync.Mutex
	fd   int
	stop chan struct{}
}

func keepaliveUDPConns(interval time.Duration, st *stats) voice.UDPConnCreateFunc {
	return func(dave godave.Session, lookup voice.SsrcLookupFunc, opts ...voice.UDPConnConfigOpt) voice.UDPConn {
		k := &keepaliveUDPConn{interval: interval, st: st, fd: -1}
		if interval > 0 {
			opts = append(opts, voice.WithUDPConnDialer(&net.Dialer{Timeout: voice.UDPTimeout, Control: k.dupSocket}))
		}
		k.UDPConn = voice.NewUDPConn(dave, lookup, opts...)
		return k
	}
}

func (k *keepaliveUDPConn) dupSocket(_, _ string, c syscall.RawConn) error {
	var dupErr error
	if err := c.Control(func(fd uintptr) {
		nfd, err := syscall.Dup(int(fd))
		if err != nil {
			dupErr = err
			return
		}
		k.mu.Lock()
		k.closeFDLocked()
		k.fd = nfd
		k.mu.Unlock()
	}); err != nil {
		return err
	}
	return dupErr
}

func (k *keepaliveUDPConn) Open(ctx context.Context, ip string, port int, ssrc uint32) (string, int, error) {
	addr, p, err := k.UDPConn.Open(ctx, ip, port, ssrc)
	if err == nil && k.interval > 0 {
		k.startKeepalive()
	}
	return addr, p, err
}

func (k *keepaliveUDPConn) startKeepalive() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stop != nil {
		close(k.stop)
	}
	stop := make(chan struct{})
	k.stop = stop
	go func() {
		ticker := time.NewTicker(k.interval)
		defer ticker.Stop()
		buf := make([]byte, 8)
		var counter uint32
		for {
			binary.LittleEndian.PutUint32(buf, counter)
			counter++
			// Write under the lock, after checking stop, so a descriptor that
			// Close already released can never be written to.
			k.mu.Lock()
			select {
			case <-stop:
				k.mu.Unlock()
				return
			default:
			}
			_, err := syscall.Write(k.fd, buf)
			k.mu.Unlock()
			if err != nil {
				k.st.printf("keepalive write failed: %v", err)
			} else {
				k.st.keepalives.Add(1)
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
}

func (k *keepaliveUDPConn) Close() error {
	k.mu.Lock()
	if k.stop != nil {
		close(k.stop)
		k.stop = nil
	}
	k.closeFDLocked()
	k.mu.Unlock()
	return k.UDPConn.Close()
}

func (k *keepaliveUDPConn) closeFDLocked() {
	if k.fd >= 0 {
		_ = syscall.Close(k.fd)
		k.fd = -1
	}
}

// receiver replaces disgo's defaultAudioReceiver. It keeps disgo's rule of
// reading only while the DAVE session is ready, and changes three things:
//   - it sleeps while DAVE is not ready, where disgo spins a CPU core;
//   - it keeps reading after the UDP socket closes, because disgo reopens the
//     same socket on a voice reconnect and its receiver would have quit;
//   - it counts what it drops instead of only logging it.
//
// It also reads in a transport-only session (DAVE protocol version 0, as in a
// stage channel), where dave-go's Ready stays false forever and disgo's
// receiver would never read at all.
type receiver struct {
	conn    voice.Conn
	handler voice.OpusFrameReceiver
	st      *stats
	stop    chan struct{}
	once    sync.Once
}

func (r *receiver) Open() { go r.loop() }

func (r *receiver) loop() {
	closedLogged := false
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		if !r.readable() {
			r.st.addNotReady(5 * time.Millisecond)
			time.Sleep(5 * time.Millisecond)
			continue
		}
		p, err := r.conn.UDP().ReadPacket()
		switch {
		case errors.Is(err, net.ErrClosed):
			if !closedLogged {
				r.st.printf("voice UDP socket closed; waiting for disgo to reopen it")
				closedLogged = true
			}
			time.Sleep(250 * time.Millisecond)
		case err != nil:
			r.st.readError(err)
		default:
			closedLogged = false
			if err := r.handler.ReceiveOpusFrame(r.conn.UserIDBySSRC(p.SSRC), p); err != nil {
				r.st.printf("writing frame: %v", err)
			}
		}
	}
}

func (r *receiver) readable() bool {
	if r.conn.DAVE().Ready() {
		return true
	}
	s := r.st.daveState()
	return s != nil && s.sessionDescribed && s.ProtocolVersion == 0
}

func (r *receiver) CleanupUser(userID snowflake.ID) { r.handler.CleanupUser(userID) }

func (r *receiver) Close() { r.once.Do(func() { close(r.stop) }) }

// countingSession wraps the dave-go session so every Decrypt result is
// counted per user. disgo's ReadPacket drops the SSRC from its error, so this
// is the only place a DAVE failure can be tied to a speaker.
type countingSession struct {
	godave.Session
	st *stats
}

func countingDaveSessions(inner godave.SessionCreateFunc, st *stats) godave.SessionCreateFunc {
	return func(logger *slog.Logger, userID godave.UserID, callbacks godave.Callbacks) godave.Session {
		return &countingSession{Session: inner(logger, userID, callbacks), st: st}
	}
}

func (c *countingSession) Decrypt(userID godave.UserID, frame []byte, out []byte) (int, error) {
	n, err := c.Session.Decrypt(userID, frame, out)
	c.st.decrypted(string(userID), err)
	return n, err
}

func isDaveError(err error) bool {
	return strings.Contains(err.Error(), "DAVE decrypt")
}
