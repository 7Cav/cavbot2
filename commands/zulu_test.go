package commands

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// respondCalls returns every recorded Respond call in order.
func respondCalls(calls []recordedCall) []recordedCall {
	var out []recordedCall
	for _, c := range calls {
		if c.Method == "Respond" {
			out = append(out, c)
		}
	}
	return out
}

// TestRunZulu_NoArguments is a characterization test, not a red-to-green slice.
// The feature request that added the time/date arguments specifies that bare
// /zulu is unchanged, which makes the rendered layout the contract for this
// path rather than incidental formatting. It was written before the argument
// handling landed so it guards that refactor.
func TestRunZulu_NoArguments(t *testing.T) {
	r := &fakeResponder{}
	now := time.Date(2026, time.August, 4, 17, 39, 1, 0, time.UTC)

	runZulu(r, now, fakeAppCommandInteraction())

	resps := respondCalls(r.Calls())
	if len(resps) != 1 {
		t.Fatalf("bare /zulu must send exactly one response; got %d", len(resps))
	}
	if resps[0].Response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("unexpected response type: %v", resps[0].Response.Type)
	}
	if got := resps[0].Response.Data.Content; !strings.Contains(got, "17:39:01 04AUG26") {
		t.Fatalf("bare /zulu must render the current Zulu time; got %q", got)
	}
}

// zuluOptions builds a /zulu interaction, omitting either option when empty so
// the handler sees the same shape Discord sends for an unsupplied option.
func zuluOptions(timeStr, dateStr string) *discordgo.InteractionCreate {
	var opts []*discordgo.ApplicationCommandInteractionDataOption
	if timeStr != "" {
		opts = append(opts, stringOption("time", timeStr))
	}
	if dateStr != "" {
		opts = append(opts, stringOption("date", dateStr))
	}
	return fakeAppCommandInteraction(opts...)
}

var zuluTokenPattern = regexp.MustCompile(`<t:(\d+):([fR])>`)

// zuluEpochs pulls the epoch out of each Discord timestamp token in a response.
// The epoch is the behavior under test — the prose around it deliberately is not.
func zuluEpochs(t *testing.T, content string) map[string]int64 {
	t.Helper()
	found := map[string]int64{}
	for _, m := range zuluTokenPattern.FindAllStringSubmatch(content, -1) {
		epoch, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			t.Fatalf("unparseable epoch %q in %q", m[1], content)
		}
		found[m[2]] = epoch
	}
	return found
}

func TestRunZulu_TimeNotYetPassedResolvesToToday(t *testing.T) {
	r := &fakeResponder{}
	now := time.Date(2026, time.August, 4, 17, 39, 1, 0, time.UTC)

	runZulu(r, now, zuluOptions("2300", ""))

	resps := respondCalls(r.Calls())
	if len(resps) != 1 {
		t.Fatalf("expected exactly one response; got %d", len(resps))
	}
	want := time.Date(2026, time.August, 4, 23, 0, 0, 0, time.UTC).Unix()
	got := zuluEpochs(t, resps[0].Response.Data.Content)
	if got["f"] != want || got["R"] != want {
		t.Fatalf("epochs = %v, want both tokens at %d (%s)", got, want, time.Unix(want, 0).UTC())
	}
}

// ephemeralRejection asserts the interaction was rejected without anything
// public going out: exactly one response, carrying the ephemeral flag.
func ephemeralRejection(t *testing.T, r *fakeResponder) {
	t.Helper()
	resps := respondCalls(r.Calls())
	if len(resps) != 1 {
		t.Fatalf("expected exactly one response; got %d: %+v", len(resps), r.Calls())
	}
	if resps[0].Response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("rejection must be ephemeral; got flags %d and content %q",
			resps[0].Response.Data.Flags, resps[0].Response.Data.Content)
	}
	if got := zuluEpochs(t, resps[0].Response.Data.Content); len(got) != 0 {
		t.Fatalf("a rejection must not carry a timestamp; got %v", got)
	}
}

func TestRunZulu_DateWithoutTimeIsRejected(t *testing.T) {
	r := &fakeResponder{}
	now := time.Date(2026, time.August, 4, 17, 39, 1, 0, time.UTC)

	runZulu(r, now, zuluOptions("", "01MAY26"))

	ephemeralRejection(t, r)
}

// The rows below cover branches that were already implemented when the
// not-yet-passed slice landed, so they were green on arrival rather than driven
// out red-first. They are kept as regression coverage.

func TestRunZulu_TimeAlreadyPassedRollsToTomorrow(t *testing.T) {
	r := &fakeResponder{}
	// 2330Z asking about 2300 means tonight has gone; the next 2300 is tomorrow.
	now := time.Date(2026, time.August, 4, 23, 30, 0, 0, time.UTC)

	runZulu(r, now, zuluOptions("2300", ""))

	want := time.Date(2026, time.August, 5, 23, 0, 0, 0, time.UTC).Unix()
	got := zuluEpochs(t, respondCalls(r.Calls())[0].Response.Data.Content)
	if got["f"] != want {
		t.Fatalf("epoch = %d (%s), want %d (%s)",
			got["f"], time.Unix(got["f"], 0).UTC(), want, time.Unix(want, 0).UTC())
	}
}

// TestRunZulu_TimeExactlyNowRollsToTomorrow pins the boundary. It is the row
// that separates `!instant.After(now)` from `instant.Before(now)`: asking about
// 2300 at exactly 2300Z means the next one, not the instant already elapsing.
func TestRunZulu_TimeExactlyNowRollsToTomorrow(t *testing.T) {
	r := &fakeResponder{}
	now := time.Date(2026, time.August, 4, 23, 0, 0, 0, time.UTC)

	runZulu(r, now, zuluOptions("2300", ""))

	want := time.Date(2026, time.August, 5, 23, 0, 0, 0, time.UTC).Unix()
	got := zuluEpochs(t, respondCalls(r.Calls())[0].Response.Data.Content)
	if got["f"] != want {
		t.Fatalf("epoch = %d (%s), want %d (%s)",
			got["f"], time.Unix(got["f"], 0).UTC(), want, time.Unix(want, 0).UTC())
	}
}

// TestRunZulu_ExplicitPastDateIsNotRolledForward is the row that distinguishes
// "a date was given" from "roll forward": rollover applied to an explicit date
// would push this into the future.
func TestRunZulu_ExplicitPastDateIsNotRolledForward(t *testing.T) {
	r := &fakeResponder{}
	now := time.Date(2026, time.August, 4, 17, 39, 1, 0, time.UTC)

	runZulu(r, now, zuluOptions("2300", "1MAY24"))

	want := time.Date(2024, time.May, 1, 23, 0, 0, 0, time.UTC).Unix()
	got := zuluEpochs(t, respondCalls(r.Calls())[0].Response.Data.Content)
	if got["f"] != want {
		t.Fatalf("epoch = %d (%s), want %d (%s)",
			got["f"], time.Unix(got["f"], 0).UTC(), want, time.Unix(want, 0).UTC())
	}
}

// TestRunZulu_UnparseableTimeIsRejected guards the error-swallowing mutation:
// discarding the parser's error would emit a zero epoch publicly instead of
// rejecting. The date-without-time row stays green under that mutation, so this
// row is not redundant with it.
func TestRunZulu_UnparseableTimeIsRejected(t *testing.T) {
	r := &fakeResponder{}
	now := time.Date(2026, time.August, 4, 17, 39, 1, 0, time.UTC)

	runZulu(r, now, zuluOptions("half past six", ""))

	ephemeralRejection(t, r)
}

// TestRunZulu_InvalidDateIsRejectedDistinctlyFromInvalidTime covers the defect
// where both parse failures collapsed into one message: /zulu 2300 BADDATE told
// the member to fix the time, which was the field they got right. Asserting the
// two rejections differ pins that behavior without pinning either wording.
func TestRunZulu_InvalidDateIsRejectedDistinctlyFromInvalidTime(t *testing.T) {
	now := time.Date(2026, time.August, 4, 17, 39, 1, 0, time.UTC)

	badDate := &fakeResponder{}
	runZulu(badDate, now, zuluOptions("2300", "BADDATE"))
	ephemeralRejection(t, badDate)

	badTime := &fakeResponder{}
	runZulu(badTime, now, zuluOptions("half past six", ""))
	ephemeralRejection(t, badTime)

	dateMsg := respondCalls(badDate.Calls())[0].Response.Data.Content
	timeMsg := respondCalls(badTime.Calls())[0].Response.Data.Content
	if dateMsg == timeMsg {
		t.Fatalf("a bad date and a bad time must not report the same message; both said %q", dateMsg)
	}
}

// TestRunZulu_ResolvedTimeIsPublic guards the point of the whole feature: the
// answer has to reach the channel. An ephemeral flag here would leave the member
// still hand-writing the message they asked the bot to replace.
func TestRunZulu_ResolvedTimeIsPublic(t *testing.T) {
	r := &fakeResponder{}
	now := time.Date(2026, time.August, 4, 17, 39, 1, 0, time.UTC)

	runZulu(r, now, zuluOptions("2300", ""))

	data := respondCalls(r.Calls())[0].Response.Data
	if data.Flags&discordgo.MessageFlagsEphemeral != 0 {
		t.Fatalf("resolved-time response must be public; got flags %d", data.Flags)
	}
}
