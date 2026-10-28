package commands

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// loaRefDate pins "now" for /loa tests at 2026-05-22 12:00 UTC. The stub
// 7Cav API's UTC today is 2026-05-22 too, so it states an LOA open from its
// start date through its end date and upcoming before.
var loaRefDate = time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)

// loaMember builds a roster-shape LiteProfileResponse for /loa tests, with
// the forum user ID an LOA's Subject is matched on. The uniformUrl matches
// the /<n>/<milpacID>.jpg shape the handler parses for the milpac link.
func loaMember(username, forumUserID, milpacID string) utils.LiteProfileResponse {
	return utils.LiteProfileResponse{
		User:       utils.User{UserID: forumUserID, Username: username},
		Rank:       utils.Rank{RankFull: "Specialist"},
		UniformUrl: "https://7cav.us/data/roster_uniforms/0/" + milpacID + ".jpg",
	}
}

// loaHeadings are the section headings a /loa reply can carry.
var loaHeadings = []string{loaActiveHeading, loaUpcomingHeading, loaNeedsReviewHeading}

// loaNumbers finds the numbers written in a title or footer.
var loaNumbers = regexp.MustCompile(`\d+`)

// loaSection returns the lines under heading across every embed the run's
// reply shows, in order, as loaPagedSection reads pages. It returns nil
// when no embed has such a section.
func loaSection(t *testing.T, f *fakeResponder, heading string) []string {
	t.Helper()
	return loaPagedSection(replyView(t, f).embeds, heading)
}

// loaListed is one LOA as /loa should list it: its Subject's name and its
// thread.
type loaListed struct{ name, threadID string }

// serveLOAPosition serves a position whose members hold open open LOAs,
// upcoming upcoming LOAs and review needs-review LOAs, one per member,
// with names, milpac IDs and thread IDs as long as the regiment's. It
// returns what /loa should list under each heading, in /loa's order. Open
// LOAs are sent latest end first and upcoming ones latest start first, so
// /loa's sort sets the order. Needs-review LOAs are listed as sent.
func serveLOAPosition(t *testing.T, open, upcoming, review int) map[string][]loaListed {
	t.Helper()
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	want := map[string][]loaListed{}
	var loas []stubLOA
	sections := 0
	add := func(heading string, n int, prefix string, loa func(k int, l stubLOA) stubLOA) {
		sections++
		for k := range n {
			name := fmt.Sprintf("%s.T%04d", prefix, k)
			forumUserID := fmt.Sprintf("%d", 10000*sections+k)
			milpacID := fmt.Sprintf("%d", 50000+len(roster.LiteProfiles))
			threadID := fmt.Sprintf("%d", 100000*sections+k)
			roster.LiteProfiles[milpacID] = loaMember(name, forumUserID, milpacID)
			want[heading] = append(want[heading], loaListed{name, threadID})
			loas = append(loas, loa(k, stubLOA{
				threadID: threadID, postDate: loaRefDate.AddDate(0, 0, -1),
				userID: forumUserID, username: name,
			}))
		}
	}
	day := func(offset int) string { return loaRefDate.AddDate(0, 0, offset).Format(time.DateOnly) }
	add(loaActiveHeading, open, "Open", func(k int, l stubLOA) stubLOA {
		l.start, l.end = day(-2), day(k)
		return l
	})
	add(loaUpcomingHeading, upcoming, "Upco", func(k int, l stubLOA) stubLOA {
		l.start, l.end = day(1+k), day(6+k)
		return l
	})
	add(loaNeedsReviewHeading, review, "Revw", func(_ int, l stubLOA) stubLOA {
		l.start, l.end = day(-2), day(30)
		l.needsReview, l.reviewReason = true, "LOA_REVIEW_REASON_TOO_LONG"
		return l
	})
	slices.Reverse(loas[:open+upcoming])
	serveCavAPI(t, loaRefDate, roster, loas...)
	return want
}

// assertListedInOrder fails unless lines list exactly want, in order, each
// line naming its LOA's Subject and linking its thread.
func assertListedInOrder(t *testing.T, heading string, lines []string, want []loaListed) {
	t.Helper()
	if len(lines) != len(want) {
		t.Fatalf("%s holds %d lines, want %d: %q", heading, len(lines), len(want), lines)
	}
	for k, w := range want {
		if !hasLOALine(lines[k:k+1], w.name, w.threadID) {
			t.Fatalf("%s line %d = %q, want %s linking thread %s", heading, k+1, lines[k], w.name, w.threadID)
		}
	}
}

// loaHeadingOf returns the heading line holds, or "" when it holds none.
func loaHeadingOf(line string) string {
	for _, h := range loaHeadings {
		if strings.Contains(line, h) {
			return h
		}
	}
	return ""
}

// hasLOALine reports whether one of lines names name and links threadID.
func hasLOALine(lines []string, name, threadID string) bool {
	for _, line := range lines {
		if strings.Contains(line, name) && lineLinksThread(line, threadID) {
			return true
		}
	}
	return false
}

// TestRunLoa_FirstRespondFails_BailsBeforeAPI covers the early-bail branch
// when the placeholder InteractionRespond fails: runLoa must call
// HandleError once and return before asking the 7Cav API for anything (the
// tripwire server fails the test if touched).
func TestRunLoa_FirstRespondFails_BailsBeforeAPI(t *testing.T) {
	tripwireAPIServer(t)

	f := &fakeResponder{RespondErrs: []error{errFirstRespond}}
	i := fakeAppCommandInteraction(stringOption("position", "1-7"))

	runLoa(f, nil, loaRefDate, i)

	assertPlaceholderFailedBailout(t, f.Calls())
}

// A roster fetch the 7Cav API fails tells the member to try again, and its
// error goes to Sentry, not to the member.
func TestRunLoaRosterFailureReachesSentryNotTheMember(t *testing.T) {
	serveCavAPI(t, loaRefDate, utils.LiteRosterResponse{}).failRoster(http.StatusInternalServerError)
	rec := recordCaptures(t)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertLookupFailed(t, f.Calls(), rec, "loa")
}

// /loa leaves no LOA call running once it has answered a failed roster
// call, even when the LOA call starts late, so nothing the run started
// outlives it.
func TestRunLoaLeavesNoLOACallRunningAfterAFailedRosterCall(t *testing.T) {
	serveCavAPI(t, loaRefDate, utils.LiteRosterResponse{}).failRoster(http.StatusInternalServerError)
	outlived := startLOACallsLate(t, 20*time.Millisecond)

	runLoa(&fakeResponder{}, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if outlived() {
		t.Fatal("an LOA call returned after the run did")
	}
}

// /loa returns soon after a failed roster call, without waiting out an
// LOA call the 7Cav API never answers.
func TestRunLoaDoesNotWaitOutAnUnansweredLOACallAfterAFailedRosterCall(t *testing.T) {
	api := serveCavAPI(t, loaRefDate, utils.LiteRosterResponse{})
	api.failRoster(http.StatusInternalServerError)
	api.neverAnswerLOAs()

	started := time.Now()
	runLoa(&fakeResponder{}, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if took := time.Since(started); took >= time.Second {
		t.Fatalf("the run returned %v after it started; it waited out the LOA call", took)
	}
}

// A position whose members have no open, upcoming or needs-review LOA gets
// the no-LOAs reply naming the position.
func TestRunLoa_NoMatchingLOAsReportsNone(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.Present", "40", "100"),
		},
	}
	serveCavAPI(t, loaRefDate, roster)

	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if reply := lastReply(t, f.Calls()); !strings.Contains(reply, fmt.Sprintf(loaNoneFormat, "1-7")) {
		t.Fatalf("want the no-LOAs reply for 1-7; got %q", reply)
	}
}

// A position search that finds nobody gets the format hint naming the
// position, as an error reply.
func TestRunLoa_EmptyRosterSurfacesFormatHint(t *testing.T) {
	serveCavAPI(t, loaRefDate, utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}})

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "Q/Z/9-9")))

	assertFixedReply(t, lastReply(t, f.Calls()), emptyRosterSearchMessage("Q/Z/9-9"), nil)
}

// A trooper with one open and one upcoming LOA gets a line for each, one
// under Active and one under Upcoming, so a second, later LOA shows up when
// a leader plans ahead.
func TestRunLoaListsAnOpenAndAnUpcomingLOAOfOneTrooperOnTwoLines(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.Two", "41", "100"),
		},
	}
	serveCavAPI(t, loaRefDate, roster,
		stubLOA{
			threadID: "7101", postDate: loaRefDate.AddDate(0, 0, -3),
			userID: "41", username: "Trooper.Two",
			start: "2026-05-20", end: "2026-05-25",
		},
		stubLOA{
			threadID: "7102", postDate: loaRefDate.AddDate(0, 0, -1),
			userID: "41", username: "Trooper.Two",
			start: "2026-06-10", end: "2026-06-20",
		},
	)
	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if active := loaSection(t, f, loaActiveHeading); !hasLOALine(active, "Trooper.Two", "7101") {
		t.Fatalf("want a line under Active naming Trooper.Two and linking thread 7101; Active holds %q", active)
	}
	if upcoming := loaSection(t, f, loaUpcomingHeading); !hasLOALine(upcoming, "Trooper.Two", "7102") {
		t.Fatalf("want a line under Upcoming naming Trooper.Two and linking thread 7102; Upcoming holds %q", upcoming)
	}
}

// A trooper on the last day of their leave is listed under Active for the
// whole UTC day, as the 7Cav API states the LOA open.
func TestRunLoaListsATrooperOnTheLastDayOfTheirLeaveUnderActive(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.Last", "43", "100"),
		},
	}
	serveCavAPI(t, loaRefDate, roster, stubLOA{
		threadID: "7106", postDate: loaRefDate.AddDate(0, 0, -10),
		userID: "43", username: "Trooper.Last",
		start: "2026-05-12", end: "2026-05-22",
	})
	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if active := loaSection(t, f, loaActiveHeading); !hasLOALine(active, "Trooper.Last", "7106") {
		t.Fatalf("want a line under Active naming Trooper.Last and linking thread 7106; Active holds %q", active)
	}
}

// An LOA is listed for its Subject, the roster member with the Subject's
// forum user ID, whatever username the Subject carries, as after a rename.
// It is never listed for a member who only shares the Subject's username.
func TestRunLoaListsAnLOAForTheMemberWithItsSubjectsForumUserIDOnly(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Renamed.R", "41", "100"),
			"200": loaMember("Shared.S", "50", "200"),
		},
	}
	serveCavAPI(t, loaRefDate, roster,
		stubLOA{
			threadID: "7103", postDate: loaRefDate.AddDate(0, 0, -3),
			userID: "41", username: "OldName.R",
			start: "2026-05-20", end: "2026-05-25",
		},
		stubLOA{
			threadID: "7104", postDate: loaRefDate.AddDate(0, 0, -3),
			userID: "51", username: "Shared.S",
			start: "2026-05-20", end: "2026-05-25",
		},
	)
	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	active := loaSection(t, f, loaActiveHeading)
	if !hasLOALine(active, "Renamed.R", "7103") {
		t.Fatalf("want a line under Active naming Renamed.R and linking thread 7103; Active holds %q", active)
	}
	for _, heading := range []string{loaActiveHeading, loaUpcomingHeading} {
		if line := lineContaining(strings.Join(loaSection(t, f, heading), "\n"), "Shared.S"); line != "" {
			t.Fatalf("Shared.S only shares the Subject's username, yet is listed: %q", line)
		}
	}
}

// A needs-review LOA is never listed under Active or Upcoming, even when its
// dates read and cover today, so a broken LOA never reads as approved leave.
func TestRunLoaNeverListsANeedsReviewLOAUnderActiveOrUpcoming(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Review.R", "44", "100"),
		},
	}
	serveCavAPI(t, loaRefDate, roster, stubLOA{
		threadID: "7107", postDate: loaRefDate.AddDate(0, 0, -1),
		userID: "44", username: "Review.R",
		start: "2026-05-20", end: "2026-06-25",
		needsReview: true, reviewReason: "LOA_REVIEW_REASON_TOO_LONG",
	})
	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	for _, heading := range []string{loaActiveHeading, loaUpcomingHeading} {
		if line := lineContaining(strings.Join(loaSection(t, f, heading), "\n"), "Review.R"); line != "" {
			t.Fatalf("a needs-review LOA is listed under %s: %q", heading, line)
		}
	}
}

// Each needs-review LOA naming a member of the position gets a line under
// Needs review that links its thread and says what is wrong in its reason's
// own words, so a leader knows what to ask the trooper to correct.
func TestRunLoaListsEachNeedsReviewLOAUnderNeedsReviewWithItsReason(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Unread.U", "51", "100"),
			"200": loaMember("Backward.B", "52", "200"),
			"300": loaMember("Long.L", "53", "300"),
		},
	}
	serveCavAPI(t, loaRefDate, roster,
		stubLOA{
			threadID: "7201", postDate: loaRefDate.AddDate(0, 0, -2),
			userID: "51", username: "Unread.U",
			needsReview: true, reviewReason: "LOA_REVIEW_REASON_DATES_UNREADABLE",
		},
		stubLOA{
			threadID: "7202", postDate: loaRefDate.AddDate(0, 0, -4),
			userID: "52", username: "Backward.B",
			start: "2026-06-10", end: "2026-06-01",
			needsReview: true, reviewReason: "LOA_REVIEW_REASON_END_BEFORE_START",
		},
		stubLOA{
			threadID: "7203", postDate: loaRefDate.AddDate(0, 0, -6),
			userID: "53", username: "Long.L",
			start: "2026-05-20", end: "2027-05-25",
			needsReview: true, reviewReason: "LOA_REVIEW_REASON_TOO_LONG",
		},
	)
	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	section := strings.Join(loaSection(t, f, loaNeedsReviewHeading), "\n")
	for _, want := range []struct{ name, threadID, reason string }{
		{"Unread.U", "7201", loaReviewDatesUnreadable},
		{"Backward.B", "7202", loaReviewEndBeforeStart},
		{"Long.L", "7203", loaReviewTooLong},
	} {
		line := lineContaining(section, want.name)
		if !lineLinksThread(line, want.threadID) || !strings.Contains(line, want.reason) {
			t.Errorf("want a line under Needs review naming %s, linking thread %s and saying %q; Needs review holds %q",
				want.name, want.threadID, want.reason, section)
		}
	}
}

// A needs-review LOA whose rule the bot has no phrase for, such as one the
// 7Cav API added since, is still listed under Needs review with its thread,
// saying only that it can't be taken as written.
func TestRunLoaListsANeedsReviewLOAOfARuleWithNoPhraseUnderNeedsReview(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Novel.N", "55", "100"),
		},
	}
	serveCavAPI(t, loaRefDate, roster, stubLOA{
		threadID: "7205", postDate: loaRefDate.AddDate(0, 0, -2),
		userID: "55", username: "Novel.N",
		start: "2026-05-20", end: "2026-05-25",
		needsReview: true, reviewReason: "LOA_REVIEW_REASON_NEW_RULE",
	})
	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	section := strings.Join(loaSection(t, f, loaNeedsReviewHeading), "\n")
	line := lineContaining(section, "Novel.N")
	if !lineLinksThread(line, "7205") || !strings.Contains(line, loaReviewOtherRule) {
		t.Fatalf("want a line under Needs review naming Novel.N, linking thread 7205 and saying %q; Needs review holds %q",
			loaReviewOtherRule, section)
	}
}

// A needs-review LOA whose Subject the API couldn't resolve, userId "0",
// is listed in no section, even for a member whose username equals the
// Subject's.
func TestRunLoaListsANeedsReviewLOANamingNoMemberNowhere(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Same.S", "54", "100"),
		},
	}
	serveCavAPI(t, loaRefDate, roster, stubLOA{
		threadID: "7204", postDate: loaRefDate.AddDate(0, 0, -2),
		userID: "0", username: "Same.S",
		start: "2026-05-20", end: "2026-06-25",
		needsReview: true, reviewReason: "LOA_REVIEW_REASON_TOO_LONG",
	})
	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	for _, heading := range loaHeadings {
		if line := lineContaining(strings.Join(loaSection(t, f, heading), "\n"), "Same.S"); line != "" {
			t.Fatalf("an LOA naming no member is listed for Same.S under %s: %q", heading, line)
		}
	}
}

// An LOA edited or deleted a minute ago shows as it stands now on the next
// run: /loa keeps no LOAs between runs.
func TestRunLoaShowsAnEditedOrDeletedLOAAsItStandsOnTheNextRun(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.E", "45", "100"),
		},
	}
	upcoming := stubLOA{
		threadID: "7105", postDate: loaRefDate.AddDate(0, 0, -1),
		userID: "45", username: "Trooper.E",
		start: "2026-06-01", end: "2026-06-10",
	}
	api := serveCavAPI(t, loaRefDate, roster, upcoming)
	run := func(now time.Time) *fakeResponder {
		f := &fakeResponder{}
		runLoa(f, nil, now, fakeAppCommandInteraction(stringOption("position", "1-7")))
		return f
	}

	first := run(loaRefDate)
	if lines := loaSection(t, first, loaUpcomingHeading); !hasLOALine(lines, "Trooper.E", "7105") {
		t.Fatalf("first run: want thread 7105 under Upcoming; Upcoming holds %q", lines)
	}

	edited := upcoming
	edited.start = "2026-05-22"
	api.setLOAs(edited)
	second := run(loaRefDate.Add(time.Minute))
	if lines := loaSection(t, second, loaActiveHeading); !hasLOALine(lines, "Trooper.E", "7105") {
		t.Fatalf("second run: want the edited thread 7105 under Active; Active holds %q", lines)
	}
	if lines := loaSection(t, second, loaUpcomingHeading); hasLOALine(lines, "Trooper.E", "7105") {
		t.Fatalf("second run: the edited thread 7105 is still under Upcoming: %q", lines)
	}

	api.setLOAs()
	third := run(loaRefDate.Add(2 * time.Minute))
	for _, heading := range []string{loaActiveHeading, loaUpcomingHeading} {
		if line := lineContaining(strings.Join(loaSection(t, third, heading), "\n"), "Trooper.E"); line != "" {
			t.Fatalf("third run: the deleted LOA is still listed under %s: %q", heading, line)
		}
	}
}

// When the LOA call answers 404, /loa says LOAs are unavailable and to try
// again: the shared client's not-found answer is a failure here, never "no
// LOAs". The failure reaches Sentry once and the reply carries none of it.
func TestRunLoaLOACallAnswering404RepliesUnavailableAndReachesSentry(t *testing.T) {
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.U", "46", "100"),
		},
	}
	serveCavAPI(t, loaRefDate, roster, stubLOA{
		threadID: "7108", postDate: loaRefDate.AddDate(0, 0, -3),
		userID: "46", username: "Trooper.U",
		start: "2026-05-20", end: "2026-05-25",
	}).failLOAs(http.StatusNotFound)
	rec := recordCaptures(t)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if rec.count != 1 {
		t.Fatalf("captures = %d, want 1", rec.count)
	}
	assertFixedReply(t, lastReply(t, f.Calls()), loaUnavailableReply, rec)
}

// A leader waits no longer for /loa than before: the LOA call adds no wait
// to the roster call. With the stub taking D to answer each call, the
// listing arrives in under 2D, which calls made one after the other can't.
func TestRunLoaWaitsNoLongerThanTheSlowerOfTheRosterAndLOACalls(t *testing.T) {
	const d = time.Second
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{
			"100": loaMember("Trooper.W", "47", "100"),
		},
	}
	serveCavAPI(t, loaRefDate, roster, stubLOA{
		threadID: "7109", postDate: loaRefDate.AddDate(0, 0, -3),
		userID: "47", username: "Trooper.W",
		start: "2026-05-20", end: "2026-05-25",
	}).delayCalls(d)

	f := &fakeResponder{}
	started := time.Now()
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))
	waited := time.Since(started)

	if active := loaSection(t, f, loaActiveHeading); !hasLOALine(active, "Trooper.W", "7109") {
		t.Fatalf("want a line under Active naming Trooper.W and linking thread 7109; Active holds %q", active)
	}
	if waited >= 2*d {
		t.Fatalf("the listing took %v with each call taking %v; the calls ran one after the other", waited, d)
	}
}

// A member whose uniform URL doesn't parse still has their LOA listed, with
// no milpac link, so a leader never reads a position as having nobody on
// leave because of a bad roster record. The bad URL reaches Sentry once.
func TestRunLoaListsTheLOAOfAMemberWhoseUniformURLDoesNotParse(t *testing.T) {
	member := loaMember("Trooper.N", "48", "100")
	member.UniformUrl = "not-a-uniform-url"
	roster := utils.LiteRosterResponse{
		LiteProfiles: map[string]utils.LiteProfileResponse{"100": member},
	}
	serveCavAPI(t, loaRefDate, roster, stubLOA{
		threadID: "7110", postDate: loaRefDate.AddDate(0, 0, -3),
		userID: "48", username: "Trooper.N",
		start: "2026-05-20", end: "2026-05-25",
	})
	rec := recordCaptures(t)

	f := &fakeResponder{}
	runLoa(f, nil, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if active := loaSection(t, f, loaActiveHeading); !hasLOALine(active, "Trooper.N", "7110") {
		t.Fatalf("want a line under Active naming Trooper.N and linking thread 7110; Active holds %q", active)
	}
	if rec.count != 1 {
		t.Fatalf("captures = %d, want 1", rec.count)
	}
}

// loaPagedSection returns the lines under heading across pages, in order,
// reading each page on its own as loaSection reads each embed.
func loaPagedSection(pages []*discordgo.MessageEmbed, heading string) []string {
	var lines []string
	for _, e := range pages {
		current := ""
		for _, line := range strings.Split(e.Description, "\n") {
			if h := loaHeadingOf(line); h != "" {
				current = h
				continue
			}
			if current == heading && strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
	}
	return lines
}

// When the runner pages a list of about 150 LOAs with ▶ from page 1 to
// page m, the pages show every LOA exactly once, under its own heading and
// in the run's order.
func TestRunLoaPagesShowEveryLOAOnceUnderItsHeadingInOrder(t *testing.T) {
	want := serveLOAPosition(t, 60, 50, 40)
	pages, _ := pageThrough(t, loaList, store.NewFake(), loaRefDate)

	for _, heading := range loaHeadings {
		assertListedInOrder(t, heading, loaPagedSection(pages, heading), want[heading])
	}
}

// A page that carries on a section from the page before opens with that
// section's heading, so a member reading any page knows what its lines
// are.
func TestRunLoaOpensAPageThatContinuesASectionWithItsHeading(t *testing.T) {
	want := serveLOAPosition(t, 60, 50, 40)
	pages, _ := pageThrough(t, loaList, store.NewFake(), loaRefDate)

	continued := 0
	for k := 1; k < len(pages); k++ {
		heading := loaListedUnder(firstLOALine(pages[k].Description), want)
		if heading == "" || heading != lastLOAHeading(pages[k-1].Description) {
			continue
		}
		continued++
		if !strings.HasPrefix(pages[k].Description, heading) {
			t.Errorf("page %d carries on %s from the page before but opens with %q",
				k+1, heading, strings.SplitN(pages[k].Description, "\n", 2)[0])
		}
	}
	if continued == 0 {
		t.Fatalf("no page carries on a section from the one before; the fixture no longer tests this")
	}
}

// firstLOALine returns the first line of desc that is neither a heading
// nor blank.
func firstLOALine(desc string) string {
	for _, line := range strings.Split(desc, "\n") {
		if loaHeadingOf(line) == "" && strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// lastLOAHeading returns the last heading in desc, or "" when it has none.
func lastLOAHeading(desc string) string {
	last := ""
	for _, line := range strings.Split(desc, "\n") {
		if h := loaHeadingOf(line); h != "" {
			last = h
		}
	}
	return last
}

// loaListedUnder returns the heading want lists line's LOA under, or ""
// when line lists none of want's LOAs.
func loaListedUnder(line string, want map[string][]loaListed) string {
	for heading, listed := range want {
		for _, l := range listed {
			if hasLOALine([]string{line}, l.name, l.threadID) {
				return heading
			}
		}
	}
	return ""
}

// Every page a press shows carries the Active, Upcoming and Needs review
// counts of the whole list, not of the page they sit on.
func TestRunLoaCountsTheWholeListOnEveryPage(t *testing.T) {
	serveLOAPosition(t, 60, 50, 40)
	pages, _ := pageThrough(t, loaList, store.NewFake(), loaRefDate)

	for k, e := range pages {
		if e.Footer == nil {
			t.Errorf("page %d shows no counts", k+1)
			continue
		}
		counts := loaNumbers.FindAllString(e.Footer.Text, -1)
		slices.Sort(counts)
		if want := []string{"40", "50", "60"}; !slices.Equal(counts, want) {
			t.Errorf("page %d shows the counts %q; want 60, 50 and 40", k+1, e.Footer.Text)
		}
	}
}
