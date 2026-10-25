package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/voice"
	"github.com/bwmarrin/discordgo"
	"github.com/getsentry/sentry-go"
)

// A recording's gaps (#736, #856): the stretches its recorder can't hear the
// channel. Every case runs the runtime over the /record scene, and the fake
// voice adapter's connection tells the runtime when a gap starts and ends,
// as the voice adapter does. A gap that reaches 10 s cuts the recording
// short. What the runtime posts is read for its verdict marker, the times
// it names, who it would ping and the production constants that tell cases
// apart, never its wording.

// gapCutsShortAfter is how long a gap runs before it cuts the recording
// short. It is product contract, not tuning: the ping tells members "10
// seconds".
const gapCutsShortAfter = 10 * time.Second

// startGap has the recorder in channelID stop hearing it now, as the voice
// adapter tells the runtime, and returns when.
func (sc *recordScene) startGap(t *testing.T, channelID string) time.Time {
	t.Helper()
	start := sc.clock.read()
	sc.listener(t, channelID).GapStarted(start, voice.GapVoiceGatewayClosed)
	return start
}

// A gap that reaches 10 s stops the recording at that moment, and not a
// millisecond before. The voice adapter can tell of a gap only after it
// started, as when it notices a voice server went silent, so the 10 s count
// from the start it gives, not from when it tells.
func TestRecordingGapThatReaches10sStopsTheRecordingThen(t *testing.T) {
	for _, tc := range []struct {
		name     string
		toldLate time.Duration
	}{
		{"told at its start", 0},
		{"told 4 s late", 4 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, testRecorder)
			sc.startIn(t, recStarter, recordChannel)
			sc.clock.advance(time.Minute)
			start := sc.clock.read().Add(-tc.toldLate)
			sc.listener(t, recordChannel).GapStarted(start, voice.GapVoiceGatewayClosed)

			sc.clock.advance(gapCutsShortAfter - tc.toldLate - time.Millisecond)
			sc.assertRunning(t, recordChannel)

			sc.clock.advance(time.Millisecond)

			if got := sc.voice.inChannel(recordChannel); len(got) != 0 {
				t.Errorf("recorders in %s 10 s into the gap = %v, want none", recordChannel, got)
			}
			if rec := sc.onlyRecording(t); rec.StoppedAt.IsZero() {
				t.Errorf("recording 10 s into the gap = %+v, want it stopped", rec)
			}
		})
	}
}

// recordUntilCutShort starts a recording in which a speaker says one frame
// a minute in, has the recorder stop hearing the channel two minutes in,
// and runs the clock to that gap's 10 s mark. It returns the gap's start.
func (sc *recordScene) recordUntilCutShort(t *testing.T) time.Time {
	t.Helper()
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Minute)
	sc.hear(t, recordChannel, frame(speakerA, 11, 5000, "a1"))
	sc.clock.advance(time.Minute)
	return sc.cutShortAtTheMark(t, recordChannel)
}

// A recording a gap cuts short keeps what the recorder heard up to the
// gap's start, and ends there: the row the panel lists stops then, as a
// gap's cut short, every track ends there rather than 10 s of silence
// later, and the info file's Ended line names the gap.
func TestRecordingCutShortByAGapEndsAtTheGapsStart(t *testing.T) {
	sc := newRecordScene(t, testRecorder)

	start := sc.recordUntilCutShort(t)

	rec := sc.onlyRecording(t)
	if rec.Ended != store.RecordingEndGap || !rec.StoppedAt.Equal(start) {
		t.Errorf("recording ended %q at %v, want %q at the gap's start, %v", rec.Ended, rec.StoppedAt, store.RecordingEndGap, start)
	}
	// The gap starts two minutes in: 120 s of 48 kHz samples.
	const gapStartSamples = 120 * 48000
	tracks := sc.tracks(t)
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want the speaker's one", len(tracks))
	}
	if tracks[0].granule != gapStartSamples {
		t.Errorf("track ends at sample %d, want the gap's start, %d", tracks[0].granule, gapStartSamples)
	}
	infoLine(t, sc.zipEntries(t, rec)[infoFileName], infoEndedGap)
}

// discordMention matches a user mention, <@id> or <@!id>, or a role
// mention, <@&id>, in a message's content.
var discordMention = regexp.MustCompile(`<@([!&]?)(\d+|[\w-]+)>`)

// wouldPing lists whom Discord notifies for a message with content and
// allowed mentions am, in order: each user its content mentions, as their
// ID, each role as "role:<id>", and @everyone or @here as written, when am
// lets them through. Nil allowed mentions are Discord's default, under which
// every mention pings.
func wouldPing(content string, am *discordgo.MessageAllowedMentions) []string {
	parses := func(kind discordgo.AllowedMentionType) bool {
		return am == nil || slices.Contains(am.Parse, kind)
	}
	var out []string
	for _, m := range discordMention.FindAllStringSubmatch(content, -1) {
		switch id := m[2]; {
		case m[1] == "&" && (parses(discordgo.AllowedMentionTypeRoles) || slices.Contains(am.Roles, id)):
			out = append(out, "role:"+id)
		case m[1] != "&" && (parses(discordgo.AllowedMentionTypeUsers) || slices.Contains(am.Users, id)):
			out = append(out, id)
		}
	}
	for _, everyone := range []string{"@everyone", "@here"} {
		if strings.Contains(content, everyone) && parses(discordgo.AllowedMentionTypeEveryone) {
			out = append(out, everyone)
		}
	}
	return out
}

// discordCommandMention matches Discord's mention of a slash command,
// </name:id> or </name subcommand:id>, with any ID, an empty one included.
var discordCommandMention = regexp.MustCompile(`</([^:<>]+):([^>]*)>`)

// commandMentions lists the slash commands content mentions, each as
// "name:id", in order.
func commandMentions(content string) []string {
	var out []string
	for _, m := range discordCommandMention.FindAllStringSubmatch(content, -1) {
		out = append(out, m[1]+":"+m[2])
	}
	return out
}

// postedSince is every message still up in a channel's chat that posted
// after the one given, in order. Unlike noticesIn, it lets through messages
// that ping.
func postedSince(fake *fakeTempVCManager, channelID string, after fakeMessage) []fakeMessage {
	var out []fakeMessage
	seen := false
	for _, m := range fake.recordedMessages() {
		switch {
		case m.id == after.id:
			seen = true
		case seen && m.channelID == channelID:
			out = append(out, m)
		}
	}
	return out
}

// At the 10 s mark the starter hears of the cut short in the channel's
// chat: one new message, a reply to the recording notice, that opens with
// ❌, pings the starter alone, tells them to run /record start, and carries
// no panel link. The starter has moved to another call and a recording-role
// holder is still in the recorded one, so the ping finds the starter
// wherever they are and pings nobody else in the call.
func TestRecordingCutShortByAGapPingsTheStarterInAReplyToTheNotice(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	notice := recordingNotice(t, sc.fake, recordChannel)
	sc.fake.setVoice(recHolder.id, recordChannel)
	sc.fake.setVoice(recStarter.id, recordOtherChannel)
	sc.startGap(t, recordChannel)

	sc.clock.advance(gapCutsShortAfter)

	posted := postedSince(sc.fake, recordChannel, notice)
	if len(posted) != 1 {
		t.Fatalf("messages posted in %s at the cut short = %d, want the ping alone", recordChannel, len(posted))
	}
	ping := posted[0]
	content := ping.data.Content
	if ping.replyTo != notice.id {
		t.Errorf("ping replies to %q, want the recording notice %s", ping.replyTo, notice.id)
	}
	if !strings.HasPrefix(content, "❌") {
		t.Errorf("ping = %q, want a ❌ verdict", content)
	}
	if got := wouldPing(content, ping.data.AllowedMentions); !slices.Equal(got, []string{recStarter.id}) {
		t.Errorf("ping %q with allowed mentions %+v pings %v, want the starter %s alone", content, ping.data.AllowedMentions, got, recStarter.id)
	}
	if run := "/" + recordCommandName + " start"; !strings.Contains(content, run) {
		t.Errorf("ping = %q, want it to tell the starter to run %s", content, run)
	}
	if strings.Contains(content, testPanelURL) {
		t.Errorf("ping = %q, want no panel link", content)
	}
}

// cutShortPing has a gap cut short the recording in recordChannel and
// returns the content of the one ping it posts.
func (sc *recordScene) cutShortPing(t *testing.T) string {
	t.Helper()
	sc.cutShortAtTheMark(t, recordChannel)
	pings := pingsIn(sc.fake, recordChannel)
	if len(pings) != 1 {
		t.Fatalf("pings in %s at the cut short = %d, want one", recordChannel, len(pings))
	}
	return pings[0].data.Content
}

// Before the startup sync has registered the commands, the runtime doesn't
// know /record's ID. The ping then names /record start as plain text and
// mentions no command, so it still tells the starter what to run.
func TestRecordingCutShortPingBeforeTheCommandsRegisterNamesRecordStartPlain(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)

	content := sc.cutShortPing(t)

	if got := commandMentions(content); len(got) != 0 {
		t.Errorf("ping = %q mentions the commands %v, want none before they're registered", content, got)
	}
	if run := "/" + recordCommandName + " start"; !strings.Contains(content, run) {
		t.Errorf("ping = %q, want it to tell the starter to run %s", content, run)
	}
}

// discordTimes is the Unix seconds of each Discord timestamp in content, in
// order, whatever style each is shown in.
func discordTimes(content string) []int64 {
	var out []int64
	for _, m := range regexp.MustCompile(`<t:(\d+)(?::[a-zA-Z])?>`).FindAllStringSubmatch(content, -1) {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		out = append(out, n)
	}
	return out
}

// At the 10 s mark the recording notice says the recording was cut short,
// and from when: it opens with ⏹️, names one time, the gap's start, carries
// the link into the panel, loses its Stop button and pings nobody. With the
// notice edited and the ping posted, in a channel no hub spawned, the
// starter gets no DM.
func TestRecordingCutShortByAGapEditsTheNoticeWithTheGapsStart(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	notice := recordingNotice(t, sc.fake, recordChannel)
	sc.clock.advance(time.Minute)
	start := sc.startGap(t, recordChannel)

	sc.clock.advance(gapCutsShortAfter)

	edit := noticeEdit(t, sc.fake, notice)
	content := deref(edit.Content)
	if !strings.HasPrefix(content, "⏹️") {
		t.Errorf("notice edited to %q, want a ⏹️ verdict", content)
	}
	if got := discordTimes(content); !slices.Equal(got, []int64{start.Unix()}) {
		t.Errorf("notice edited to %q names times %v, want the gap's start, %d", content, got, start.Unix())
	}
	if !strings.Contains(content, testPanelURL) {
		t.Errorf("notice edited to %q, want a link into the panel at %s", content, testPanelURL)
	}
	// A nil list leaves the button in place; an empty one removes it.
	if edit.Components == nil || len(*edit.Components) != 0 {
		t.Errorf("notice edit components = %v, want an empty list", edit.Components)
	}
	if got := wouldPing(content, edit.AllowedMentions); len(got) != 0 {
		t.Errorf("notice edited to %q with allowed mentions %+v pings %v, want nobody", content, edit.AllowedMentions, got)
	}
	if dms := sc.fake.sentDMs(); len(dms) != 0 {
		t.Errorf("DMs after the ping and the notice edit went through = %+v, want none", dms)
	}
}

// gapLines is the INFO records of gaps that ended before cutting their
// recording short.
func gapLines(t *testing.T, logs *syncBuffer) []map[string]string {
	t.Helper()
	return logRecordsWith(t, logs, "INFO", map[string]string{"msg": recordingGapEndedMessage})
}

// lasted reads a gap line's length back as a duration.
func lasted(t *testing.T, line map[string]string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(line["lasted"])
	if err != nil {
		t.Fatalf("gap line %v: lasted %q is no duration: %v", line, line["lasted"], err)
	}
	return d
}

// A gap that ends before 10 s logs one gap line with its length and the
// cause the voice adapter gave at its start, and posts nothing in the chat,
// read once the clock has passed the gap's 10 s mark: the recording runs on.
func TestRecordingGapUnder10sLogsItsLengthAndCauseAndPostsNothing(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	logs := captureLogs(t)
	sc.startIn(t, recStarter, recordChannel)
	notice := recordingNotice(t, sc.fake, recordChannel)
	l := sc.listener(t, recordChannel)
	l.GapStarted(sc.clock.read(), voice.GapCallMoved)
	sc.clock.advance(9 * time.Second)

	l.GapEnded()
	sc.clock.advance(gapCutsShortAfter + time.Minute)

	lines := gapLines(t, logs)
	if len(lines) != 1 {
		t.Fatalf("gap lines = %v, want one", lines)
	}
	if got := lasted(t, lines[0]); got != 9*time.Second {
		t.Errorf("gap line says the gap lasted %v, want 9s", got)
	}
	if got := lines[0]["gap_cause"]; got != string(voice.GapCallMoved) {
		t.Errorf("gap line's gap_cause = %q, want %q, the cause the gap started with", got, voice.GapCallMoved)
	}
	if posted := postedSince(sc.fake, recordChannel, notice); len(posted) != 0 {
		t.Errorf("messages posted in %s after a 9 s gap = %d, want none", recordChannel, len(posted))
	}
	sc.assertRunning(t, recordChannel)
}

// A gap's cut short logs the recording's stop line with ended=gap, and no
// gap line: the gap never ended, it cut the recording short. The stop line
// is the positive signal that the cut short had its chance to log.
func TestRecordingCutShortByAGapLogsItsStopAndNoGapLine(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	logs := captureLogs(t)

	sc.recordUntilCutShort(t)

	id := strconv.FormatInt(sc.onlyRecording(t).ID, 10)
	stops := logRecordsWith(t, logs, "INFO", map[string]string{"msg": recordingStoppedMessage, "recording_id": id})
	if len(stops) != 1 || stops[0]["ended"] != string(store.RecordingEndGap) {
		t.Fatalf("stop lines for recording %s = %v, want one with ended=%s", id, stops, store.RecordingEndGap)
	}
	if lines := gapLines(t, logs); len(lines) != 0 {
		t.Errorf("gap lines at the cut short = %v, want none", lines)
	}
}

// A stop during a gap shorter than 10 s stops the recording when it
// happened, not at the gap's start, and the gap it ends logs its gap line,
// lasting until the stop.
func TestRecordingStopDuringAShortGapKeepsItsOwnTime(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	logs := captureLogs(t)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Minute)
	sc.startGap(t, recordChannel)
	sc.clock.advance(4 * time.Second)
	stoppedAt := sc.clock.read()

	sc.stopAsStarter(t)

	if rec := sc.onlyRecording(t); !rec.StoppedAt.Equal(stoppedAt) {
		t.Errorf("recording stopped at %v, want the stop's own time, %v", rec.StoppedAt, stoppedAt)
	}
	lines := gapLines(t, logs)
	if len(lines) != 1 {
		t.Fatalf("gap lines = %v, want one for the gap the stop ended", lines)
	}
	if got := lasted(t, lines[0]); got != 4*time.Second {
		t.Errorf("gap line says the gap lasted %v, want 4s, until the stop", got)
	}
}

// rowsRecordedBy is the store's recording rows whose recorder is the one
// given.
func (sc *recordScene) rowsRecordedBy(t *testing.T, recorderID string) []store.Recording {
	t.Helper()
	var out []store.Recording
	for _, rec := range sc.recordings(t) {
		if rec.RecorderID == recorderID {
			out = append(out, rec)
		}
	}
	return out
}

// The 10 s mark doesn't wait on the recorder's leave, which here hangs far
// past its 5 s bound. At the mark itself the ping has posted and the notice
// says the recording was cut short. The channel is free to record again
// from then: a start in it at the mark takes the other recorder and gets
// ✅. Once the hung leave returns, the cut-short recording's cleanup has
// left the new recording alone, and its starter stops it.
func TestRecordingCutShortByAGapDoesNotWaitOnTheLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder, testSecondRecorder)
		sc.startIn(t, recStarter, recordChannel)
		notice := recordingNotice(t, sc.fake, recordChannel)
		sc.voice.setLeaveTakes(hungLeave)
		// A failure before the end still lets the hung leave return, so the
		// bubble can exit.
		t.Cleanup(func() { time.Sleep(hungLeave) })
		start := time.Now()
		sc.listener(t, recordChannel).GapStarted(start, voice.GapVoiceGatewayClosed)

		time.Sleep(gapCutsShortAfter)
		synctest.Wait()

		if posted := postedSince(sc.fake, recordChannel, notice); len(posted) != 1 {
			t.Errorf("messages posted in %s at the mark = %d, want the ping", recordChannel, len(posted))
		}
		if got := discordTimes(deref(noticeEdit(t, sc.fake, notice).Content)); !slices.Equal(got, []int64{start.Unix()}) {
			t.Errorf("notice at the mark names times %v, want the gap's start, %d", got, start.Unix())
		}
		sc.fake.setVoice(recHolder.id, recordChannel)
		if reply := recordAs(t, sc.rt, recHolder, "start"); !strings.HasPrefix(reply, "✅") {
			t.Fatalf("start in %s at the mark = %q, want a ✅ verdict", recordChannel, reply)
		}
		if got := sc.voice.inChannel(recordChannel); !slices.Contains(got, testSecondRecorder) {
			t.Errorf("recorders in %s after the start at the mark = %v, want %s among them", recordChannel, got, testSecondRecorder)
		}

		time.Sleep(hungLeave)
		synctest.Wait()

		if rows := sc.rowsRecordedBy(t, testSecondRecorder); len(rows) != 1 || !rows[0].StoppedAt.IsZero() {
			t.Fatalf("recordings by %s once the hung leave returned = %+v, want one still running", testSecondRecorder, rows)
		}
		sc.voice.setLeaveTakes(0)
		if reply := recordAs(t, sc.rt, recHolder, "stop"); !strings.HasPrefix(reply, "✅") {
			t.Errorf("stop of the new recording = %q, want a ✅ verdict", reply)
		}
		if rows := sc.rowsRecordedBy(t, testSecondRecorder); len(rows) != 1 || rows[0].StoppedAt.IsZero() {
			t.Errorf("recordings by %s after its stop = %+v, want one stopped", testSecondRecorder, rows)
		}
	})
}

// pingsIn is every message still up in a channel's chat that would ping
// someone.
func pingsIn(fake *fakeTempVCManager, channelID string) []fakeMessage {
	var out []fakeMessage
	for _, m := range fake.recordedMessages() {
		if m.channelID == channelID && len(wouldPing(m.data.Content, m.data.AllowedMentions)) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// cutShortAtTheMark has the recorder in channelID stop hearing it now and
// runs the clock to the gap's 10 s mark. It returns the gap's start.
func (sc *recordScene) cutShortAtTheMark(t *testing.T, channelID string) time.Time {
	t.Helper()
	start := sc.startGap(t, channelID)
	sc.clock.advance(gapCutsShortAfter)
	return start
}

// The starter gets the link to a recording a gap cut short by DM as well
// when the chat may not reach them: in a spawned channel, whose hub takes
// the chat away once everyone has left, when the notice is gone, and when
// the ping didn't post. Each holds alone in its row, so each condition
// shows on its own. The DM names the gap's start and links into the panel.
func TestRecordingCutShortByAGapSendsTheStarterOneDM(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup starts the recording as the row needs and returns its
		// channel.
		setup func(t *testing.T, sc *recordScene) string
		// pinged is whether the ping posts.
		pinged bool
	}{
		{
			name: "in a spawned channel",
			setup: func(t *testing.T, sc *recordScene) string {
				sc.startInSpawned(t)
				return recordSpawned
			},
			pinged: true,
		},
		{
			name: "with the notice gone",
			setup: func(t *testing.T, sc *recordScene) string {
				sc.startIn(t, recStarter, recordChannel)
				deleteMessage(t, sc.fake, recordingNotice(t, sc.fake, recordChannel))
				return recordChannel
			},
			pinged: true,
		},
		{
			name: "with the ping refused",
			setup: func(t *testing.T, sc *recordScene) string {
				sc.startIn(t, recStarter, recordChannel)
				sc.fake.replyErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)
				return recordChannel
			},
		},
		{
			name: "in a spawned channel, with the notice gone and the ping refused",
			setup: func(t *testing.T, sc *recordScene) string {
				deleteMessage(t, sc.fake, sc.startInSpawned(t))
				sc.fake.replyErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)
				return recordSpawned
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, testRecorder)
			channelID := tc.setup(t, sc)
			before := len(sc.fake.sentDMs())

			start := sc.cutShortAtTheMark(t, channelID)

			if got, want := len(pingsIn(sc.fake, channelID)), map[bool]int{true: 1, false: 0}[tc.pinged]; got != want {
				t.Errorf("pings in %s = %d, want %d", channelID, got, want)
			}
			dms := sc.fake.sentDMs()[before:]
			assertOneLinkDM(t, dms)
			if got := discordTimes(dms[0].data.Content); !slices.Equal(got, []int64{start.Unix()}) {
				t.Errorf("DM %q names times %v, want the gap's start, %d", dms[0].data.Content, got, start.Unix())
			}
		})
	}
}

// deleteMessage takes a message out of its channel's chat, as a member
// deleting it does.
func deleteMessage(t *testing.T, fake *fakeTempVCManager, m fakeMessage) {
	t.Helper()
	if err := fake.ChannelMessageDelete(m.channelID, m.id); err != nil {
		t.Fatalf("deleting %s in %s: %v", m.id, m.channelID, err)
	}
}

// A ping Discord refuses logs a WARN naming the recording.
func TestRecordingCutShortPingThatFailsIsLogged(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	logs := captureLogs(t)
	sc.startIn(t, recStarter, recordChannel)
	sc.fake.replyErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)

	sc.cutShortAtTheMark(t, recordChannel)

	id := strconv.FormatInt(sc.onlyRecording(t).ID, 10)
	fields := map[string]string{"msg": recordingGapPingNotPostedMessage, "recording_id": id}
	if warns := logRecordsWith(t, logs, "WARN", fields); len(warns) != 1 {
		t.Errorf("WARN records with %v = %v, want one", fields, warns)
	}
}

// Only a gap's cut short pings. A restart's cut short and a stop at the cap
// each end their recording, their notice edited as before, and post nothing
// in the channel's chat that pings anyone.
func TestRecordingEndedOtherwisePingsNobody(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, sc *recordScene)
	}{
		{"a restart's cut short", func(t *testing.T, sc *recordScene) {
			sc.recordFlushedFrame(t)
			sc.restart(t, testRecorder)
			sc.clock.advance(0)
		}},
		{"the cap", func(t *testing.T, sc *recordScene) {
			sc.startIn(t, recStarter, recordChannel)
			sc.clock.advance(recordingCap)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, testRecorder)

			tc.end(t, sc)

			noticeEdit(t, sc.fake, fakeMessage{id: sc.onlyRecording(t).NoticeID, channelID: recordChannel})
			if pings := pingsIn(sc.fake, recordChannel); len(pings) != 0 {
				t.Errorf("messages in %s that ping someone = %+v, want none", recordChannel, pings)
			}
		})
	}
}

// The voice adapter goes on handing the runtime what the recorder hears
// until its leave returns, which here hangs. A rejoin that finishes a second
// after the 10 s mark says the gap ended, then hands over a speaker's frame
// on the new connection. The cut-short recording keeps none of it: once the
// leave's bound has passed, every track still ends at the gap's start.
func TestRecordingCutShortByAGapKeepsNothingHeardAfterTheMark(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := newBubbleRecordScene(t, testRecorder)
		sc.startIn(t, recStarter, recordChannel)
		l := sc.listener(t, recordChannel)
		time.Sleep(time.Minute)
		l.Frame(frame(speakerA, 11, 5000, "a1"))
		time.Sleep(time.Minute)
		sc.voice.setLeaveTakes(hungLeave)
		t.Cleanup(func() { time.Sleep(hungLeave) })
		l.GapStarted(time.Now(), voice.GapVoiceGatewayClosed)

		time.Sleep(gapCutsShortAfter + time.Second)
		l.GapEnded()
		l.Frame(frame(speakerA, 12, 90000, "a2"))
		time.Sleep(recordingLeaveTimeout)
		synctest.Wait()

		// The gap starts two minutes in: 120 s of 48 kHz samples.
		const gapStartSamples = 120 * 48000
		tracks := sc.tracks(t)
		if len(tracks) != 1 {
			t.Fatalf("tracks = %d, want the speaker's one", len(tracks))
		}
		if tracks[0].granule != gapStartSamples {
			t.Errorf("track ends at sample %d, want the gap's start, %d", tracks[0].granule, gapStartSamples)
		}
	})
}

// A gap's cut short sends Sentry one event, at the 10 s mark and not a
// millisecond before. The fake clock runs the mark on the test's goroutine,
// and Sentry hands the event to its transport inside the capture, so a send
// any earlier would show by then.
func TestRecordingCutShortByAGapSendsSentryOneEventAtTheMark(t *testing.T) {
	events := recordSentry(t)
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(time.Minute)
	sc.startGap(t, recordChannel)

	sc.clock.advance(gapCutsShortAfter - time.Millisecond)
	sc.assertRunning(t, recordChannel)
	if got := events.Events(); len(got) != 0 {
		t.Fatalf("Sentry events before the 10 s mark = %d, want none", len(got))
	}

	sc.clock.advance(time.Millisecond)

	if got := events.Events(); len(got) != 1 {
		t.Errorf("Sentry events at the 10 s mark = %d, want one", len(got))
	}
}

// cutShortBy records in the fixture's channel until a gap that cause
// started cuts the recording short, with the voice adapter reporting rep
// at the 10 s mark. The recorder is free again once it returns.
func (sc *recordScene) cutShortBy(t *testing.T, cause voice.GapCause, rep voice.GapReport) {
	t.Helper()
	sc.startIn(t, recStarter, recordChannel)
	sc.voice.setGapReport(rep)
	sc.clock.advance(time.Minute)
	sc.listener(t, recordChannel).GapStarted(sc.clock.read(), cause)
	sc.clock.advance(gapCutsShortAfter)
}

// Every recording a gap cuts short reaches Sentry under one message tag,
// whatever the cause, and with an error that opens as every cut short's
// does, so the cut-short issues sort together. Each cause gets a Sentry
// issue of its own, and two recordings the same cause cut short share one,
// however much else differs between them: here their close codes, voice
// servers and close texts.
func TestRecordingCutShortEventsLandInAnIssuePerCause(t *testing.T) {
	events := recordSentry(t)
	sc := newRecordScene(t, testRecorder)

	sc.cutShortBy(t, voice.GapVoiceGatewayClosed, voice.GapReport{CloseCode: "4006", VoiceServer: "c-dfw-1.discord.media:443",
		Close: errors.New("websocket: close 4006: session is no longer valid")})
	sc.cutShortBy(t, voice.GapVoiceGatewayClosed, voice.GapReport{CloseCode: "none", VoiceServer: "c-ord-2.discord.media:443",
		Close: errors.New("read tcp 10.0.0.2:41000: connection reset by peer")})
	sc.cutShortBy(t, voice.GapCallMoved, voice.GapReport{VoiceServer: "c-atl-3.discord.media:443"})

	got := events.Events()
	if len(got) != 3 {
		t.Fatalf("Sentry events = %d, want one for each of the 3 cut shorts", len(got))
	}
	for i, e := range got {
		if e.Tags["message"] != recordingCutShortMessage {
			t.Errorf("event %d: message tag = %q, want %q", i, e.Tags["message"], recordingCutShortMessage)
		}
		if len(e.Exception) == 0 || !strings.HasPrefix(e.Exception[len(e.Exception)-1].Value, "voice: recording cut short:") {
			t.Errorf("event %d: exceptions = %+v, want an error opening %q", i, e.Exception, "voice: recording cut short:")
		}
	}
	if !slices.Equal(got[0].Fingerprint, got[1].Fingerprint) {
		t.Errorf("fingerprints of two %s cut shorts = %v and %v, want the same issue", voice.GapVoiceGatewayClosed, got[0].Fingerprint, got[1].Fingerprint)
	}
	if slices.Equal(got[0].Fingerprint, got[2].Fingerprint) {
		t.Errorf("fingerprints of a %s and a %s cut short = %v and %v, want issues of their own",
			voice.GapVoiceGatewayClosed, voice.GapCallMoved, got[0].Fingerprint, got[2].Fingerprint)
	}
}

// A server deafen's cut short goes to Sentry at warning level, which
// cavbot2's high-priority issue alert skips: a person chose the deafen, and
// no recovery work fixes it. Any other cause goes at error level, so the
// first of a new cause opens a high-priority issue.
func TestRecordingCutShortEventOfAServerDeafenIsAWarning(t *testing.T) {
	for _, tc := range []struct {
		cause voice.GapCause
		want  sentry.Level
	}{
		{voice.GapServerDeafened, sentry.LevelWarning},
		{voice.GapHeartbeatOverdue, sentry.LevelError},
	} {
		t.Run(string(tc.cause), func(t *testing.T) {
			events := recordSentry(t)
			sc := newRecordScene(t, testRecorder)

			sc.cutShortBy(t, tc.cause, voice.GapReport{})

			got := events.Events()
			if len(got) != 1 || got[0].Level != tc.want {
				t.Fatalf("Sentry events = %d, the first at level %v, want one at %v", len(got), levelOf(got), tc.want)
			}
		})
	}
}

// levelOf is the first event's level, empty when there is none.
func levelOf(events []*sentry.Event) sentry.Level {
	if len(events) == 0 {
		return ""
	}
	return events[0].Level
}

// A cut short's event tags what started the gap and the recorder, so
// Sentry can cut its count by either, and the code of the voice gateway
// close that started the gap when a close did. A gap no close started has
// no close code to tag.
func TestRecordingCutShortEventTagsTheCauseRecorderAndCloseCode(t *testing.T) {
	events := recordSentry(t)
	sc := newRecordScene(t, testRecorder)

	sc.cutShortBy(t, voice.GapVoiceGatewayClosed, voice.GapReport{CloseCode: "4006",
		Close: errors.New("websocket: close 4006: session is no longer valid")})
	sc.cutShortBy(t, voice.GapDAVEStalled, voice.GapReport{})

	got := events.Events()
	if len(got) != 2 {
		t.Fatalf("Sentry events = %d, want one for each of the 2 cut shorts", len(got))
	}
	for key, want := range map[string]string{
		"gap_cause":  string(voice.GapVoiceGatewayClosed),
		"recorder":   testRecorder,
		"close_code": "4006",
	} {
		if tag := got[0].Tags[key]; tag != want {
			t.Errorf("closed gateway's event: tag %s = %q, want %q", key, tag, want)
		}
	}
	if tag, ok := got[1].Tags["close_code"]; ok {
		t.Errorf("stall's event: tag close_code = %q, want none", tag)
	}
}

// A cut short's event carries what a maintainer reads it by: the recording
// and its channel, the voice server Discord named last, when the gap
// started, how long the recording ran before it, whether the recorder's
// gateway session was online, and the cause's own fields, passed through
// from the voice adapter. Times read as RFC 3339 in UTC to the millisecond,
// and lengths as Go duration text rounded to the millisecond, whatever zone
// and precision the voice adapter hands over. Here the gap starts 0.3 ms
// short of a minute in, told in a zone 5 hours behind UTC, and the cause's
// field is 1.2346 s. The voice adapter hands the close that started the gap
// over as an error, and the event carries its text.
func TestRecordingCutShortEventCarriesTheGapsExtras(t *testing.T) {
	events := recordSentry(t)
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	row := sc.onlyRecording(t)
	const closeText = "websocket: close 4006: session is no longer valid"
	sc.voice.setGapReport(voice.GapReport{VoiceServer: "c-dfw-1.discord.media:443", Close: errors.New(closeText),
		Fields: []voice.GapField{{Key: "held_for", Value: 1234600 * time.Microsecond}}})
	sc.clock.advance(time.Minute)
	start := sc.clock.read().Add(-300 * time.Microsecond).In(time.FixedZone("UTC-5", -5*60*60))
	sc.listener(t, recordChannel).GapStarted(start, voice.GapDAVEStalled)

	sc.clock.advance(gapCutsShortAfter)

	got := events.Events()
	if len(got) != 1 {
		t.Fatalf("Sentry events = %d, want one", len(got))
	}
	extras := got[0].Contexts["extra"]
	for key, want := range map[string]any{
		"recording_id":    row.ID,
		"channel_id":      recordChannel,
		"voice_server":    "c-dfw-1.discord.media:443",
		"gateway_session": gatewaySessionOffline,
		"close_error":     closeText,
	} {
		if extras[key] != want {
			t.Errorf("extra %s = %v (%T), want %v (%T)", key, extras[key], extras[key], want, want)
		}
	}
	at, err := time.Parse("2006-01-02T15:04:05.000Z", fmt.Sprint(extras["gap_started_at"]))
	if err != nil || at.After(start) || start.Sub(at) >= time.Millisecond {
		t.Errorf("extra gap_started_at = %v, want the gap's start, %v, in UTC to the millisecond", extras["gap_started_at"], start)
	}
	for key, want := range map[string]time.Duration{
		"recorded_for": time.Minute,
		"held_for":     1235 * time.Millisecond,
	} {
		text := fmt.Sprint(extras[key])
		if d, err := time.ParseDuration(text); err != nil || d != want || text != want.String() {
			t.Errorf("extra %s = %v, want %v as Go duration text", key, extras[key], want)
		}
	}
}

// A cut short's event lists the recovery steps the voice adapter reports,
// in its order, so the last still says what was under way at the 10 s mark.
// Each entry gives the step's kind, a dial's token, when it started from
// the gap's start, and how it ended, with the code of the voice gateway close that ended it
// when one did. The entries are read as Sentry reads the event, in JSON.
func TestRecordingCutShortEventListsTheRecoverySteps(t *testing.T) {
	events := recordSentry(t)
	sc := newRecordScene(t, testRecorder)
	const dialAt = 1234600 * time.Microsecond

	sc.cutShortBy(t, voice.GapVoiceGatewayClosed, voice.GapReport{Steps: []voice.RecoveryStep{
		{Kind: voice.StepDial, Token: voice.TokenHeld, At: dialAt, Outcome: voice.StepRefused, CloseCode: "4004"},
		{Kind: voice.StepRejoin, Outcome: voice.StepUnderWay},
	}})

	got := events.Events()
	if len(got) != 1 {
		t.Fatalf("Sentry events = %d, want one", len(got))
	}
	raw, err := json.Marshal(got[0].Contexts["extra"]["attempts"])
	var attempts []map[string]any
	if err == nil {
		err = json.Unmarshal(raw, &attempts)
	}
	if err != nil || len(attempts) != 2 {
		t.Fatalf("extra attempts = %s (%v), want the 2 steps the voice adapter reported", raw, err)
	}
	dial, rejoin := attempts[0], attempts[1]
	if dial["kind"] != string(voice.StepDial) || dial["token"] != string(voice.TokenHeld) || dial["outcome"] != string(voice.StepRefused) {
		t.Errorf("first attempt = %v, want the %s with the %s token that was %s", dial, voice.StepDial, voice.TokenHeld, voice.StepRefused)
	}
	if rejoin["kind"] != string(voice.StepRejoin) || rejoin["outcome"] != string(voice.StepUnderWay) {
		t.Errorf("last attempt = %v, want the %s still %s", rejoin, voice.StepRejoin, voice.StepUnderWay)
	}
	if at, err := time.ParseDuration(fmt.Sprint(dial["at"])); err != nil || (at-dialAt).Abs() >= time.Millisecond {
		t.Errorf("dial's at = %v, want %v from the gap's start as Go duration text", dial["at"], dialAt)
	}
	if code := fmt.Sprint(dial["close_code"]); code != "4004" {
		t.Errorf("dial's close_code = %v, want 4004", dial["close_code"])
	}
	if code, ok := rejoin["close_code"]; ok {
		t.Errorf("rejoin's close_code = %v, want none, since no close ended it", code)
	}
}

// A cut short's ERROR log line carries every field its Sentry event
// carries, tags and extras alike, so the log alone tells what Sentry heard.
func TestRecordingCutShortLogLineCarriesTheEventsFields(t *testing.T) {
	events := recordSentry(t)
	logs := captureLogs(t)
	sc := newRecordScene(t, testRecorder)

	sc.cutShortBy(t, voice.GapVoiceGatewayClosed, voice.GapReport{CloseCode: "4006", VoiceServer: "c-dfw-1.discord.media:443",
		Fields: []voice.GapField{{Key: "closed_server", Value: "c-dfw-1.discord.media:443"}}})

	got := events.Events()
	lines := logRecordsWith(t, logs, "ERROR", map[string]string{"msg": recordingCutShortMessage})
	if len(got) != 1 || len(lines) != 1 {
		t.Fatalf("Sentry events = %d and ERROR lines = %d, want one of each", len(got), len(lines))
	}
	var keys []string
	for key := range got[0].Tags {
		keys = append(keys, key)
	}
	for key := range got[0].Contexts["extra"] {
		keys = append(keys, key)
	}
	for _, key := range keys {
		if _, ok := lines[0][key]; !ok && key != "message" {
			t.Errorf("ERROR line %v has no %s, which the event carries", lines[0], key)
		}
	}
}
