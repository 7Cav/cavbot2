package commands

import (
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
