package voice

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

// Server deafens (#866, GLOSSARY.md Gap). A moderator can server-deafen the
// recorder, and Discord then sends it no audio while its connection stays
// up. The deafen reaches the recorder's session as its own voice state,
// through the session's discordgo handler. Every case runs the call harness.

// deafen is Discord telling the recorder's session of its own voice state in
// the call's channel, server-deafened or not, through the session's
// discordgo handler, as a moderator's deafen or undeafen reaches it.
func (h *callHarness) deafen(deaf bool) {
	h.s.onVoiceStateUpdate(recorderVoiceStateUpdate(callChannel, deaf))
}

// A server deafen during a recording starts a gap named for it at once,
// dated at the deafen. The gap lasts while the deafen holds, though the
// connection stays up, and the undeafen ends it.
func TestRecorderHearsAServerDeafenAsAGapUntilTheUndeafen(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		l := listen(nil)
		conn := h.record(l)
		defer h.hangUp(conn)
		// Off the receiver's 250 ms poll, so a gap dated when the receiver
		// notices the deafen starts late.
		time.Sleep(1100 * time.Millisecond)

		deafened := time.Now()
		h.deafen(true)
		time.Sleep(2 * time.Second)
		synctest.Wait()

		if got := l.gapCauses(); !slices.Equal(got, []GapCause{GapServerDeafened}) {
			t.Fatalf("gap causes while deafened = %v, want [%s]", got, GapServerDeafened)
		}
		if got := l.gapStarts()[0]; !got.Equal(deafened) {
			t.Errorf("gap started %v after the deafen, want at it", got.Sub(deafened))
		}
		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted}) {
			t.Errorf("gap events while deafened = %v, want the start alone", got)
		}

		h.deafen(false)
		time.Sleep(time.Second)
		synctest.Wait()

		if got := l.gapEvents(); !slices.Equal(got, []string{gapStarted, gapEnded}) {
			t.Errorf("gap events after the undeafen = %v, want the start, then the end", got)
		}
	})
}

// A recorder a moderator deafened in an earlier call is still deafened when
// it joins again, and Discord's answer to the join says so. The connection
// the join returns says it is deafened, and its wait for the undeafen
// returns once Discord says the recorder is undeafened, and not before.
func TestRecorderJoinedDeafenedWaitsForTheUndeafen(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		h.deafened.Store(true)
		conn := h.start()
		defer h.hangUp(conn)

		if !conn.Deafened() {
			t.Fatal("the join Discord answered deafened returned a connection that says it isn't deafened")
		}
		waited := make(chan error, 1)
		go func() { waited <- conn.Undeafened(context.Background()) }()
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case err := <-waited:
			t.Fatalf("the wait for the undeafen returned %v while the recorder was deafened", err)
		default:
		}

		h.deafen(false)
		synctest.Wait()

		select {
		case err := <-waited:
			if err != nil {
				t.Errorf("the wait for the undeafen = %v, want nil", err)
			}
		default:
			t.Error("the wait for the undeafen still waits after the undeafen")
		}
	})
}

// A start waits for the undeafen no longer than its own deadline. A wait
// whose context ends while the recorder is still deafened returns, with an
// error, so the start can fail rather than hang.
func TestRecorderWaitForTheUndeafenEndsWithItsContext(t *testing.T) {
	recordSentry(t)
	synctest.Test(t, func(t *testing.T) {
		h := newCallHarness(t)
		h.transportOnly.Store(true)
		h.deafened.Store(true)
		conn := h.start()
		defer h.hangUp(conn)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		waited := make(chan error, 1)
		go func() { waited <- conn.Undeafened(ctx) }()
		time.Sleep(2 * time.Second)
		synctest.Wait()

		select {
		case err := <-waited:
			if err == nil {
				t.Error("the wait for the undeafen = nil past its deadline, the recorder still deafened, want an error")
			}
		default:
			t.Error("the wait for the undeafen still waits past its deadline")
		}
	})
}
