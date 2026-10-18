package commands

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/utils"
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

// loaSection returns the lines under heading in the run's final edit: the
// lines after the one holding heading and before the next heading. It
// returns nil when the reply has no such section.
func loaSection(t *testing.T, f *fakeResponder, heading string) []string {
	t.Helper()
	edit := finalEdit(t, f)
	if edit.Embeds == nil {
		return nil
	}
	var lines []string
	current := ""
	for _, e := range *edit.Embeds {
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

	runLoa(f, loaRefDate, i)

	assertPlaceholderFailedBailout(t, f.Calls())
}

// A roster fetch the 7Cav API fails tells the member to try again, and its
// error goes to Sentry, not to the member.
func TestRunLoaRosterFailureReachesSentryNotTheMember(t *testing.T) {
	serveCavAPI(t, loaRefDate, utils.LiteRosterResponse{}).failRoster(http.StatusInternalServerError)
	rec := recordCaptures(t)

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertLookupFailed(t, f.Calls(), rec, "loa")
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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if reply := lastReply(t, f.Calls()); !strings.Contains(reply, fmt.Sprintf(loaNoneFormat, "1-7")) {
		t.Fatalf("want the no-LOAs reply for 1-7; got %q", reply)
	}
}

// A position search that finds nobody gets the format hint naming the
// position, as an error reply.
func TestRunLoa_EmptyRosterSurfacesFormatHint(t *testing.T) {
	serveCavAPI(t, loaRefDate, utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}})

	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "Q/Z/9-9")))

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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
		runLoa(f, now, fakeAppCommandInteraction(stringOption("position", "1-7")))
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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))
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
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if active := loaSection(t, f, loaActiveHeading); !hasLOALine(active, "Trooper.N", "7110") {
		t.Fatalf("want a line under Active naming Trooper.N and linking thread 7110; Active holds %q", active)
	}
	if rec.count != 1 {
		t.Fatalf("captures = %d, want 1", rec.count)
	}
}
