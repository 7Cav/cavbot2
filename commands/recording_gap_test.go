package commands

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
)

// A recording's gaps (#736): the stretches its recorder can't hear the
// channel. Every case runs the runtime over the /record scene, and the fake
// voice adapter's connection tells the runtime when a gap starts and ends,
// as the voice adapter does. A gap notice is read for its verdict marker
// and the times it names, never its wording.

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

// startGap has the recorder in channelID stop hearing it, as the voice
// adapter tells the runtime, and returns when.
func (sc *recordScene) startGap(t *testing.T, channelID string) time.Time {
	t.Helper()
	sc.listener(t, channelID).GapStarted()
	return sc.clock.read()
}

// gapNotices is the messages posted in channelID since the scene had
// before of them: the gap notices, in a scene where only the recording
// notice posts besides.
func gapNotices(t *testing.T, sc *recordScene, channelID string, before int) []fakeMessage {
	t.Helper()
	return noticesIn(t, sc.fake, channelID)[before:]
}

// A gap that passes 10 s posts one gap notice in the recorded channel's
// chat. It opens with ⚠️, pings nobody, and names when the gap started.
func TestRecordingGapPast10sPostsAGapNotice(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	before := len(noticesIn(t, sc.fake, recordChannel))
	sc.clock.advance(time.Minute)
	start := sc.startGap(t, recordChannel)

	sc.clock.advance(11 * time.Second)

	// noticesIn fails the test for any message that could ping someone.
	notices := gapNotices(t, sc, recordChannel, before)
	if len(notices) != 1 {
		t.Fatalf("messages posted 11 s into the gap = %d, want one gap notice", len(notices))
	}
	content := notices[0].data.Content
	if !strings.HasPrefix(content, "⚠️") {
		t.Errorf("gap notice = %q, want a ⚠️ verdict", content)
	}
	if got := discordTimes(content); !slices.Equal(got, []int64{start.Unix()}) {
		t.Errorf("gap notice %q names times %v, want the gap's start, %d", content, got, start.Unix())
	}
}

// endGap has the recorder in channelID hear it again, as the voice adapter
// tells the runtime, and returns when.
func (sc *recordScene) endGap(t *testing.T, channelID string) time.Time {
	t.Helper()
	sc.listener(t, channelID).GapEnded()
	return sc.clock.read()
}

// infoGapLines is the Gap lines of the only recording's info file, as its
// zip holds it.
func (sc *recordScene) infoGapLines(t *testing.T) []string {
	t.Helper()
	var out []string
	for line := range strings.Lines(string(sc.zipEntries(t, sc.onlyRecording(t))[infoFileName])) {
		if strings.HasPrefix(line, infoGapLabel) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// A gap that ends before 10 s posts nothing, however long the recording
// runs after it, and the info file written at the stop lists no gap.
func TestRecordingGapUnder10sPostsNothing(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	before := len(noticesIn(t, sc.fake, recordChannel))
	sc.startGap(t, recordChannel)
	sc.clock.advance(9 * time.Second)

	sc.endGap(t, recordChannel)
	sc.clock.advance(time.Minute)

	if notices := gapNotices(t, sc, recordChannel, before); len(notices) != 0 {
		t.Errorf("messages posted after a 9 s gap = %d, want none", len(notices))
	}
	recordAs(t, sc.rt, recStarter, "stop")
	if lines := sc.infoGapLines(t); len(lines) != 0 {
		t.Errorf("info file Gap lines = %q, want none for a 9 s gap", lines)
	}
}

// When a gap ends, its notice is edited to say the recorder is back and
// what it missed, from the gap's start to its end, and no other message
// posts for it. Each gap gets its own notice: a second gap posts a new one,
// and its end edits that one.
func TestRecordingGapNoticeIsEditedWhenItsGapEnds(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	before := len(noticesIn(t, sc.fake, recordChannel))
	firstStart := sc.startGap(t, recordChannel)
	sc.clock.advance(73 * time.Second)

	firstEnd := sc.endGap(t, recordChannel)
	sc.clock.advance(0)

	notices := gapNotices(t, sc, recordChannel, before)
	if len(notices) != 1 {
		t.Fatalf("messages posted for the first gap = %d, want its notice alone", len(notices))
	}
	assertGapNoticeEdit(t, sc, notices[0], "✅", firstStart, firstEnd)

	sc.clock.advance(5 * time.Minute)
	secondStart := sc.startGap(t, recordChannel)
	sc.clock.advance(20 * time.Second)
	secondEnd := sc.endGap(t, recordChannel)
	sc.clock.advance(0)

	notices = gapNotices(t, sc, recordChannel, before)
	if len(notices) != 2 {
		t.Fatalf("messages posted for two gaps = %d, want a notice each", len(notices))
	}
	assertGapNoticeEdit(t, sc, notices[1], "✅", secondStart, secondEnd)
}

// assertGapNoticeEdit fails the test unless notice was last edited to open
// with marker and name times, in order.
func assertGapNoticeEdit(t *testing.T, sc *recordScene, notice fakeMessage, marker string, times ...time.Time) {
	t.Helper()
	edit := noticeEdit(t, sc.fake, notice)
	if edit.Content == nil {
		t.Fatalf("gap notice %s edited with no content", notice.id)
	}
	content := *edit.Content
	if !strings.HasPrefix(content, marker) {
		t.Errorf("gap notice edited to %q, want a %s verdict", content, marker)
	}
	want := make([]int64, 0, len(times))
	for _, at := range times {
		want = append(want, at.Unix())
	}
	if got := discordTimes(content); !slices.Equal(got, want) {
		t.Errorf("gap notice edited to %q names times %v, want %v", content, got, want)
	}
}

// A recording that stops during a gap, whatever stops it, has the gap's
// notice edited to say it stopped before the recorder got back, and what
// it missed from when. A member's stop, the cap and an empty channel each
// reach the stop their own way.
func TestRecordingStopDuringAGapEditsItsNotice(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(t *testing.T, sc *recordScene)
	}{
		{"a member's stop", func(t *testing.T, sc *recordScene) { recordAs(t, sc.rt, recStarter, "stop") }},
		{"the cap", func(_ *testing.T, sc *recordScene) { sc.clock.advance(time.Minute) }},
		{"an empty channel", func(_ *testing.T, sc *recordScene) { sc.deliver(recStarter.id, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, testRecorder)
			sc.startIn(t, recStarter, recordChannel)
			before := len(noticesIn(t, sc.fake, recordChannel))
			sc.clock.advance(recordingCap - time.Minute)
			start := sc.startGap(t, recordChannel)
			sc.clock.advance(15 * time.Second)

			tc.stop(t, sc)

			notices := gapNotices(t, sc, recordChannel, before)
			if len(notices) != 1 {
				t.Fatalf("messages posted for the gap = %d, want its notice alone", len(notices))
			}
			assertGapNoticeEdit(t, sc, notices[0], "⏹️", start)
		})
	}
}

// A gap that reaches 10 minutes ends the recording as the recorder leaving
// does: the recorder leaves, the row records it stopped then, and the
// recording notice says it stopped. The gap notice says the recorder
// couldn't get back, and what the recording missed from when.
func TestRecordingGapThatReaches10MinutesEndsTheRecording(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	before := len(noticesIn(t, sc.fake, recordChannel))
	start := sc.startGap(t, recordChannel)
	sc.clock.advance(10*time.Minute - time.Second)
	sc.assertRunning(t, recordChannel)

	sc.clock.advance(time.Second)

	sc.assertStopped(t, recordChannel, store.RecordingEndRecorderLeft)
	notices := gapNotices(t, sc, recordChannel, before)
	if len(notices) != 1 {
		t.Fatalf("messages posted for the gap = %d, want its notice alone", len(notices))
	}
	assertGapNoticeEdit(t, sc, notices[0], "❌", start)
}

// gapLine is one info file Gap line read back: the gap's start and end, to
// the minute the info file's Zulu times give, and its length.
type gapLine struct {
	start, end time.Time
	length     time.Duration
}

// zuluTime matches a time as the info file's Zulu layout writes it.
var zuluTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{4}z`)

// parseGapLine reads an info file Gap line's values in order: the two times,
// in the info file's Zulu layout, and the one word that reads as a length.
func parseGapLine(t *testing.T, line string) gapLine {
	t.Helper()
	times := zuluTime.FindAllString(line, -1)
	if len(times) != 2 {
		t.Fatalf("Gap line %q holds %d Zulu times, want a start and an end", line, len(times))
	}
	var got gapLine
	for i, at := range []*time.Time{&got.start, &got.end} {
		parsed, err := time.Parse(zuluLayout, times[i])
		if err != nil {
			t.Fatalf("Gap line %q time %q: %v", line, times[i], err)
		}
		*at = parsed
	}
	var lengths []time.Duration
	for _, word := range strings.FieldsFunc(zuluTime.ReplaceAllString(line, ""), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.'
	}) {
		if d, err := time.ParseDuration(word); err == nil {
			lengths = append(lengths, d)
		}
	}
	if len(lengths) != 1 {
		t.Fatalf("Gap line %q holds lengths %v, want one", line, lengths)
	}
	got.length = lengths[0]
	return got
}

// The info file lists each gap past 10 s, in order, with its start, end
// and length. A gap still running at the stop runs to the stop time.
func TestRecordingInfoFileListsEachGapPast10s(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.clock.advance(3 * time.Minute)
	firstStart := sc.startGap(t, recordChannel)
	sc.clock.advance(73 * time.Second)
	firstEnd := sc.endGap(t, recordChannel)
	sc.clock.advance(4 * time.Minute)
	secondStart := sc.startGap(t, recordChannel)
	sc.clock.advance(45 * time.Second)

	stopped := sc.clock.read()
	recordAs(t, sc.rt, recStarter, "stop")

	lines := sc.infoGapLines(t)
	want := []gapLine{
		{start: firstStart, end: firstEnd, length: 73 * time.Second},
		{start: secondStart, end: stopped, length: 45 * time.Second},
	}
	if len(lines) != len(want) {
		t.Fatalf("info file Gap lines = %q, want %d", lines, len(want))
	}
	for i, line := range lines {
		got, w := parseGapLine(t, line), want[i]
		if !got.start.Equal(w.start.Truncate(time.Minute)) || !got.end.Equal(w.end.Truncate(time.Minute)) || got.length != w.length {
			t.Errorf("Gap line %d = %q, want %v to %v (%v)", i+1, line, w.start, w.end, w.length)
		}
	}
}

// A gap notice Discord refuses changes nothing for the recording: the
// recorder stays and the row stays open. Unlike the recording notice at
// start, the channel was already told it is being recorded.
func TestRecordingGoesOnWhenItsGapNoticeIsRefused(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.fake.messageErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)
	sc.startGap(t, recordChannel)

	sc.clock.advance(11 * time.Second)

	sc.assertRunning(t, recordChannel)
}
