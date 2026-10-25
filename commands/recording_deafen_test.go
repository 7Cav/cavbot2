package commands

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/7cav/cavbot2/voice"
	"github.com/bwmarrin/discordgo"
)

// Server deafens (#866). Discord keeps a moderator's deafen of a recorder
// after it leaves, so a later start can join deafened, and the start lifts
// the deafen before the recording runs. A deafen during a recording is a gap
// like any other, and the bot leaves it be. Every case runs the /record
// scene in a synctest bubble, so a start that waits on the fake voice
// adapter waits on the bubble's clock.

// A start whose recorder joins deafened lifts the deafen through the bot's
// own session, and posts the notice and answers ✅ only once the recorder is
// undeafened, so the recording doesn't record silence from its first
// second. The lift logs one Info line naming the recorder and the channel,
// and Sentry hears nothing.
func TestRecordStartLiftsTheDeafenItsRecorderJoinedWith(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		captures := countCaptures(t)
		logs := captureLogs(t)
		sc.voice.joinDeafened()
		sc.fake.setVoice(recStarter.id, recordChannel)

		f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
		done := make(chan struct{})
		go func() {
			defer close(done)
			runRecord(f, sc.rt, recordInteraction(recStarter, "start"))
		}()
		time.Sleep(time.Second)
		synctest.Wait()

		want := []fakeDeafen{{guildID: testTempVCGuild, userID: testRecorder, deaf: false}}
		if got := sc.fake.recordedDeafens(); !slices.Equal(got, want) {
			t.Errorf("member deafens = %+v, want %+v, the recorder's deafen lifted", got, want)
		}
		if notices := noticesIn(t, sc.fake, recordChannel); len(notices) != 0 {
			t.Errorf("notices in %s while the recorder is deafened = %d, want none", recordChannel, len(notices))
		}
		if calls := f.Calls(); len(calls) != 1 {
			t.Errorf("responder calls while the recorder is deafened = %+v, want the deferral alone", calls)
		}

		sc.voice.undeafen(testRecorder)
		<-done

		if reply := ephemeralRecordReply(t, f); !strings.HasPrefix(reply, "✅") {
			t.Errorf("reply = %q, want a ✅ verdict", reply)
		}
		if notices := noticesIn(t, sc.fake, recordChannel); len(notices) != 1 {
			t.Errorf("notices in %s after the undeafen = %d, want 1", recordChannel, len(notices))
		}
		lifts := logRecordsWith(t, logs, "INFO", map[string]string{"msg": recorderJoinedDeafenedMessage})
		if len(lifts) != 1 {
			t.Fatalf("INFO records %q = %d, want 1", recorderJoinedDeafenedMessage, len(lifts))
		}
		if lifts[0]["recorder"] != testRecorder || lifts[0]["channel_id"] != recordChannel {
			t.Errorf("INFO record %q = %v, want recorder %s and channel_id %s", recorderJoinedDeafenedMessage, lifts[0], testRecorder, recordChannel)
		}
		if *captures != 0 {
			t.Errorf("Sentry captures = %d, want none", *captures)
		}
	})
}

// A start whose lift of its recorder's deafen fails fails the way a failed
// join does, so the starter is never told a deaf recorder is recording.
// Sentry hears of it, and the recorder leaves the channel, free for the
// next start.
func TestRecordStartWhoseDeafenLiftFailsFailsLikeAFailedJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		captures := countCaptures(t)
		sc.voice.joinDeafened()
		sc.fake.memberDeafenErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)
		sc.fake.setVoice(recStarter.id, recordChannel)

		reply := recordAs(t, sc.rt, recStarter, "start")

		if reply != recordFailedReply {
			t.Errorf("reply = %q, want %q", reply, recordFailedReply)
		}
		if *captures != 1 {
			t.Errorf("Sentry captures = %d, want 1", *captures)
		}
		if got := sc.voice.inChannel(recordChannel); len(got) != 0 {
			t.Errorf("recorders in %s after the failed lift = %v, want none", recordChannel, got)
		}

		sc.voice.undeafen(testRecorder)
		reply = recordAs(t, sc.rt, recStarter, "start")

		if !strings.HasPrefix(reply, "✅") {
			t.Errorf("reply to the next start = %q, want a ✅ verdict", reply)
		}
		if got := sc.voice.inChannel(recordChannel); !slices.Equal(got, []string{testRecorder}) {
			t.Errorf("recorders in %s after the next start = %v, want [%s]", recordChannel, got, testRecorder)
		}
	})
}

// A start whose recorder Discord still has deafened when the start's 30 s
// run out fails the way a failed join does, rather than tell the starter a
// deaf recorder is recording.
func TestRecordStartWhoseRecorderStaysDeafenedFailsLikeAFailedJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.voice.joinDeafened()
		sc.fake.setVoice(recStarter.id, recordChannel)

		reply := recordAs(t, sc.rt, recStarter, "start")

		if reply != recordFailedReply {
			t.Errorf("reply = %q, want %q", reply, recordFailedReply)
		}
	})
}

// A moderator's deafen of the recorder during a recording stands: the gap
// it starts cuts the recording short at 10 s like any other, and the bot
// never lifts the deafen. A person chose it. The cut short's ping shows the
// path ran.
func TestRecordingNeverLiftsADeafenPutOnDuringIt(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.voice.deafen(testRecorder)
	sc.listener(t, recordChannel).GapStarted(sc.clock.read(), voice.GapServerDeafened)

	sc.clock.advance(gapCutsShortAfter)

	if pings := pingsIn(sc.fake, recordChannel); len(pings) != 1 {
		t.Fatalf("pings in %s at the cut short = %d, want one", recordChannel, len(pings))
	}
	if got := sc.fake.recordedDeafens(); len(got) != 0 {
		t.Errorf("member deafens = %+v, want none: the moderator's deafen stands", got)
	}
}
