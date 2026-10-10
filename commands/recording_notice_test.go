package commands

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// The recording notice and the automatic stops (#388). Every case runs the
// runtime over the /record scene: members in voice through the fake cache,
// voice events and channel deletes handed to the runtime on the test's own
// goroutine, the cap and the disk check on the fake clock. A recording is
// judged by where the fake voice adapter's recorders are, by its row, and by
// the notice the fake Discord manager recorded. No notice or reply wording
// is asserted.

// A start posts the recording notice in the recorded channel's chat, and it
// pings nobody: naming the starter doesn't notify them.
func TestRecordingNoticeIsPostedAtStartAndPingsNobody(t *testing.T) {
	sc := newRecordScene(t, testRecorder)

	sc.startIn(t, recStarter, recordChannel)

	// noticesIn fails the test for any message that could ping someone.
	if notices := noticesIn(t, sc.fake, recordChannel); len(notices) == 0 {
		t.Errorf("messages in %s = none, want the recording notice", recordChannel)
	}
}

// A notice Discord refuses fails the start: nobody would be told about the
// recording, so the recorder leaves, no row is written, and the starter's
// reply is a ❌.
func TestRecordingNoticeThatFailsToPostFailsTheStart(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.fake.messageErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)
	sc.fake.setVoice(recStarter.id, recordChannel)

	reply := recordAs(t, sc.rt, recStarter, "start")

	if !strings.HasPrefix(reply, "❌") {
		t.Errorf("reply = %q, want a ❌ verdict", reply)
	}
	if got := sc.voice.inChannel(recordChannel); len(got) != 0 {
		t.Errorf("recorders in %s after the refused notice = %v, want none", recordChannel, got)
	}
	if recs := sc.recordings(t); len(recs) != 0 {
		t.Errorf("recordings after the refused notice = %+v, want none", recs)
	}
}

// recordingNotice returns the last message posted in a channel: the
// recording notice, in a scene where nothing else posts there.
func recordingNotice(t *testing.T, fake *fakeTempVCManager, channelID string) fakeMessage {
	t.Helper()
	notices := noticesIn(t, fake, channelID)
	if len(notices) == 0 {
		t.Fatalf("no recording notice in %s", channelID)
	}
	return notices[len(notices)-1]
}

// stopButton returns the CustomID of a recording notice's Stop button, and
// fails the test when it carries none.
func stopButton(t *testing.T, notice fakeMessage) string {
	t.Helper()
	for _, b := range buttonsOf(notice.data.Components) {
		if action, _, ok := parseRecordingNoticeCustomID(b.CustomID); ok && action == recordingNoticeStop {
			return b.CustomID
		}
	}
	t.Fatalf("notice %s carries no Stop button: %+v", notice.id, notice.data.Components)
	return ""
}

// pressStop delivers m's press on a Stop button to the handler main.go's
// dispatcher routes its CustomID to: the registered command its first
// segment names. It checks the answer is a new ephemeral message, a
// deferral and then an edit of it, and returns it.
func pressStop(t *testing.T, rt *RecordingRuntime, customID string, m permMember) string {
	t.Helper()
	prefix := strings.Split(customID, customIDSeparator)[0]
	if _, ok := NewRegistry(nil, nil, nil).GetHandler(prefix); !ok {
		t.Fatalf("CustomID %q routes to %q, which is not a registered command", customID, prefix)
	}
	if prefix != recordCommandName {
		t.Fatalf("CustomID %q routes to /%s, not /%s", customID, prefix, recordCommandName)
	}
	f := &fakeResponder{}
	runRecord(f, rt, pressInteraction(customID, m))
	return ephemeralReply(t, f)
}

// The Stop button answers to the starter, from anywhere, and to a
// recording-role holder in the channel, as /record stop does. The starter
// presses from another channel, so only being the starter lets them stop
// it. Anyone else is refused, a recording-role holder elsewhere among them,
// and the recording goes on: the recorder stays and the row stays open.
func TestRecordingNoticeStopButtonAnswersToTheStarterAndRecordingRoleHolders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		presser permMember
		// in is the channel the presser is in when they press.
		in    string
		stops bool
	}{
		{"the starter, from another channel", recStarter, recordOtherChannel, true},
		{"a recording-role holder in the channel", recHolder, recordChannel, true},
		{"a Cav member in the channel with no recording role", recOutsider, recordChannel, false},
		{"a recording-role holder in another channel", recHolder, recordOtherChannel, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, testRecorder)
			sc.startIn(t, recStarter, recordChannel)
			button := stopButton(t, recordingNotice(t, sc.fake, recordChannel))
			sc.fake.setVoice(recStarter.id, recordOtherChannel)
			sc.fake.setVoice(tc.presser.id, tc.in)
			sc.clock.advance(5 * time.Minute)

			reply := pressStop(t, sc.rt, button, tc.presser)

			if tc.stops {
				sc.assertStopped(t, recordChannel, store.RecordingEndStopped)
				return
			}
			if !strings.HasPrefix(reply, notAllowedToStopRefusal) {
				t.Errorf("reply = %q, want the refusal %q", reply, notAllowedToStopRefusal)
			}
			sc.assertRunning(t, recordChannel)
		})
	}
}

// deliver applies a voice event to the fake cache and then hands it to the
// recording runtime, the order discordgo keeps, on the test's goroutine.
func (sc *recordScene) deliver(userID, channelID string) {
	vs := voiceEvent(userID, channelID, nil)
	sc.fake.setVoice(userID, channelID)
	sc.rt.HandleVoiceStateUpdate(vs)
}

// autoStop is one way a recording stops by itself: what happens after the
// starter has started it in recordChannel and the cache has seen the
// recorder join, and how the row then records it ended.
type autoStop struct {
	name string
	// cause makes the recording stop.
	cause func(t *testing.T, sc *recordScene)
	end   store.RecordingEnd
}

var autoStops = []autoStop{
	{
		name: "the last human leaves, with a bot still in the channel",
		cause: func(t *testing.T, sc *recordScene) {
			sc.fake.setBot("user-bot")
			sc.deliver("user-bot", recordChannel)
			sc.assertRunning(t, recordChannel)
			sc.deliver(recStarter.id, "")
		},
		end: store.RecordingEndEmpty,
	},
	{
		name: "the cap",
		cause: func(t *testing.T, sc *recordScene) {
			sc.clock.advance(recordingCap - time.Second)
			sc.assertRunning(t, recordChannel)
			sc.clock.advance(time.Second)
		},
		end: store.RecordingEndCap,
	},
	{
		name: "free disk space below the threshold",
		cause: func(t *testing.T, sc *recordScene) {
			sc.freeDisk.Store(recordingMinFreeDisk)
			sc.clock.advance(recordingDiskCheckInterval)
			sc.assertRunning(t, recordChannel)
			sc.freeDisk.Store(recordingMinFreeDisk - 1)
			sc.clock.advance(recordingDiskCheckInterval)
		},
		end: store.RecordingEndDisk,
	},
	{
		name: "the recorder is moved to another channel",
		cause: func(_ *testing.T, sc *recordScene) {
			sc.deliver(testRecorder, recordOtherChannel)
		},
		end: store.RecordingEndRecorderLeft,
	},
	{
		name: "the channel is deleted",
		cause: func(_ *testing.T, sc *recordScene) {
			sc.fake.dropChannel(recordChannel)
			sc.rt.HandleChannelDelete(&discordgo.ChannelDelete{Channel: &discordgo.Channel{
				ID: recordChannel, GuildID: testTempVCGuild, Type: discordgo.ChannelTypeGuildVoice,
			}})
		},
		end: store.RecordingEndRecorderLeft,
	},
}

// A recording stops by itself, and records how it ended: the recorder
// leaves, the row is closed at the clock's now, and the notice loses its
// button.
func TestRecordingStopsByItself(t *testing.T) {
	for _, tc := range autoStops {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, testRecorder)
			sc.startIn(t, recStarter, recordChannel)
			sc.deliver(testRecorder, recordChannel)

			tc.cause(t, sc)

			sc.assertStopped(t, recordChannel, tc.end)
		})
	}
}

// The starter leaving doesn't stop the recording while others stay: a
// meeting keeps its tail when the starter leaves early.
func TestRecordingGoesOnWhenTheStarterLeavesAndOthersStay(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.deliver(testRecorder, recordChannel)
	sc.deliver(recOutsider.id, recordChannel)

	sc.deliver(recStarter.id, "")

	sc.assertRunning(t, recordChannel)
}

// The bot's cache can show the recorder in the channel only some time after
// its join has returned. A voice event in that window, here a second member
// joining, doesn't read the recorder's absence as it leaving.
func TestRecordingGoesOnBeforeTheCacheShowsTheRecorder(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)

	sc.deliver(recOutsider.id, recordChannel)

	sc.assertRunning(t, recordChannel)
}
