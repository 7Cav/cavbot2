package commands

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// loaSection returns the lines under heading across every embed the run
// sent, in order: in each embed, the lines after the one holding heading
// and before the next heading. Each embed is read on its own, as a member
// reads one page, so a line is under heading only when heading comes
// before it in the same embed. It returns nil when no embed has such a
// section.
func loaSection(t *testing.T, f *fakeResponder, heading string) []string {
	t.Helper()
	var lines []string
	for _, embeds := range loaParts(t, f) {
		for _, e := range embeds {
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
	}
	return lines
}

// loaParts returns the embeds of each message the run sent, in order: the
// reply as its last edit left it, then each follow-up after that edit. It
// fails the test when the run never edited its reply, or made any call but
// a follow-up after the edit.
func loaParts(t *testing.T, f *fakeResponder) [][]*discordgo.MessageEmbed {
	t.Helper()
	calls := f.Calls()
	last := -1
	for k, c := range calls {
		if c.Method == "Edit" && c.Edit != nil {
			last = k
		}
	}
	if last < 0 {
		t.Fatalf("the run never edited its reply; it last showed %q", lastReply(t, calls))
	}
	var reply []*discordgo.MessageEmbed
	if calls[last].Edit.Embeds != nil {
		reply = *calls[last].Edit.Embeds
	}
	parts := [][]*discordgo.MessageEmbed{reply}
	for _, c := range calls[last+1:] {
		if c.Method != "Followup" || c.Params == nil {
			t.Fatalf("the run's reply edit was followed by a %s call", c.Method)
		}
		parts = append(parts, c.Params.Embeds)
	}
	return parts
}

// loaEmbeds returns every embed the run sent, in the order a member reads
// them.
func loaEmbeds(t *testing.T, f *fakeResponder) []*discordgo.MessageEmbed {
	t.Helper()
	var all []*discordgo.MessageEmbed
	for _, embeds := range loaParts(t, f) {
		all = append(all, embeds...)
	}
	return all
}

// assertWithinDiscordLimits fails for each message Discord would refuse
// for its embeds: more than 10 embeds, a description over 4096
// characters, or over 6000 characters of embed text in all. The limits are
// Discord's, at
// https://docs.discord.com/developers/resources/message#embed-object-embed-limits.
func assertWithinDiscordLimits(t *testing.T, parts [][]*discordgo.MessageEmbed) {
	t.Helper()
	for p, embeds := range parts {
		if len(embeds) > 10 {
			t.Errorf("message %d holds %d embeds, over Discord's 10", p+1, len(embeds))
		}
		total := 0
		for e, embed := range embeds {
			if n := utf8.RuneCountInString(embed.Description); n > 4096 {
				t.Errorf("message %d embed %d has a %d-character description, over Discord's 4096", p+1, e+1, n)
			}
			total += embedTextChars(embed)
		}
		if total > 6000 {
			t.Errorf("message %d holds %d characters of embed text, over Discord's 6000", p+1, total)
		}
	}
}

// embedTextChars counts the characters of e that Discord holds to a
// message's 6000: its title, description, footer, author name and fields.
func embedTextChars(e *discordgo.MessageEmbed) int {
	n := utf8.RuneCountInString(e.Title) + utf8.RuneCountInString(e.Description)
	if e.Footer != nil {
		n += utf8.RuneCountInString(e.Footer.Text)
	}
	if e.Author != nil {
		n += utf8.RuneCountInString(e.Author.Name)
	}
	for _, field := range e.Fields {
		n += utf8.RuneCountInString(field.Name) + utf8.RuneCountInString(field.Value)
	}
	return n
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

// /loa leaves no LOA call running once it has answered a failed roster
// call, even when the LOA call starts late. A call left running would read
// the API address after the run, while a later test sets it.
func TestRunLoaLeavesNoLOACallRunningAfterAFailedRosterCall(t *testing.T) {
	serveCavAPI(t, loaRefDate, utils.LiteRosterResponse{}).failRoster(http.StatusInternalServerError)
	outlived := startLOACallsLate(t, 20*time.Millisecond)

	runLoa(&fakeResponder{}, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if outlived() {
		t.Fatal("an LOA call returned after the run did")
	}
}

// /loa answers a failed roster call without waiting out an LOA call the
// 7Cav API never answers.
func TestRunLoaDoesNotWaitOutAnUnansweredLOACallAfterAFailedRosterCall(t *testing.T) {
	api := serveCavAPI(t, loaRefDate, utils.LiteRosterResponse{})
	api.failRoster(http.StatusInternalServerError)
	api.neverAnswerLOAs()

	started := time.Now()
	runLoa(&fakeResponder{}, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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

// A position whose LOAs fit one embed gets them as before: one embed
// titled for the position, and no follow-up.
func TestRunLoaSendsAListThatFitsOneEmbedAsOneEmbedTitledForThePosition(t *testing.T) {
	serveLOAPosition(t, 1, 1, 1)
	f := &fakeResponder{}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	parts := loaParts(t, f)
	if len(parts) != 1 || len(parts[0]) != 1 {
		t.Fatalf("want one message of one embed; got %d messages, the reply holding %d embeds", len(parts), len(parts[0]))
	}
	if title := parts[0][0].Title; title != "LOAs for 1-7" {
		t.Fatalf("title = %q, want %q", title, "LOAs for 1-7")
	}
}

// A position whose LOAs run past one embed still gets every LOA once,
// under its own heading and in /loa's order, in messages Discord accepts.
func TestRunLoaListsEveryLOAOfAListPastOneEmbedOnceUnderItsHeadingInOrder(t *testing.T) {
	want := serveLOAPosition(t, 15, 15, 10)
	f := &fakeResponder{}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertWithinDiscordLimits(t, loaParts(t, f))
	for _, heading := range loaHeadings {
		assertListedInOrder(t, heading, loaSection(t, f, heading), want[heading])
	}
}

// Each page of a list past one embed names the position and its place in
// the list in its title, so a member can tell when a list stopped short.
func TestRunLoaTitlesEachPageOfAListPastOneEmbedWithItsPlaceInTheList(t *testing.T) {
	serveLOAPosition(t, 60, 50, 40)
	f := &fakeResponder{}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	embeds := loaEmbeds(t, f)
	if len(embeds) < 2 {
		t.Fatalf("want the list across several embeds; got %d", len(embeds))
	}
	for k, e := range embeds {
		numbers := loaNumbers.FindAllString(strings.Replace(e.Title, "1-7", "", 1), -1)
		want := []string{strconv.Itoa(k + 1), strconv.Itoa(len(embeds))}
		if !strings.Contains(e.Title, "1-7") || !slices.Equal(numbers, want) {
			t.Errorf("embed %d of %d is titled %q; want the position 1-7 and the numbers %v", k+1, len(embeds), e.Title, want)
		}
	}
}

// A position with LOAs enough for several messages gets them across the
// reply and follow-ups, each message within what Discord accepts.
func TestRunLoaSpreadsAListPastOneMessageOverFollowUpsDiscordAccepts(t *testing.T) {
	serveLOAPosition(t, 60, 50, 40)
	f := &fakeResponder{}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	parts := loaParts(t, f)
	if len(parts) < 2 {
		t.Fatalf("want the list across more than one message; got %d", len(parts))
	}
	assertWithinDiscordLimits(t, parts)
}

// A page that carries on a section from the page before opens with that
// section's heading, so a member reading any page knows what its lines
// are, even on a follow-up.
func TestRunLoaOpensAPageThatContinuesASectionWithItsHeading(t *testing.T) {
	want := serveLOAPosition(t, 60, 50, 40)
	f := &fakeResponder{}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	embeds := loaEmbeds(t, f)
	continued := 0
	for k := 1; k < len(embeds); k++ {
		heading := loaListedUnder(firstLOALine(embeds[k].Description), want)
		if heading == "" || heading != lastLOAHeading(embeds[k-1].Description) {
			continue
		}
		continued++
		if !strings.HasPrefix(embeds[k].Description, heading) {
			t.Errorf("embed %d carries on %s from the embed before but opens with %q",
				k+1, heading, strings.SplitN(embeds[k].Description, "\n", 2)[0])
		}
	}
	if continued == 0 {
		t.Fatalf("no embed carries on a section from the one before; the fixture no longer tests this")
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

// The Active, Upcoming and Needs review counts a list past one embed shows
// count the whole list, not the page they sit on.
func TestRunLoaCountsTheWholeListOnAListPastOneEmbed(t *testing.T) {
	serveLOAPosition(t, 60, 50, 40)
	f := &fakeResponder{}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	shown := 0
	for k, e := range loaEmbeds(t, f) {
		if e.Footer == nil {
			continue
		}
		shown++
		counts := loaNumbers.FindAllString(e.Footer.Text, -1)
		slices.Sort(counts)
		if want := []string{"40", "50", "60"}; !slices.Equal(counts, want) {
			t.Errorf("embed %d shows the counts %q; want 60, 50 and 40", k+1, e.Footer.Text)
		}
	}
	if shown == 0 {
		t.Fatalf("no embed shows the counts")
	}
}

// A follow-up Discord refuses reaches Sentry once, and no later part goes
// out, so the page numbers already sent show the member the list stopped
// short.
func TestRunLoaStopsAtARefusedFollowUpAndReportsItOnce(t *testing.T) {
	serveLOAPosition(t, 60, 50, 40)
	whole := &fakeResponder{}
	runLoa(whole, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))
	if n := len(loaParts(t, whole)); n < 3 {
		t.Fatalf("the fixture makes %d parts; the test needs one after the refused follow-up", n)
	}

	rec := recordCaptures(t)
	refused := errors.New("HTTP 400 Bad Request")
	f := &fakeResponder{FollowupErrs: []error{refused}}
	runLoa(f, loaRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertCaptureNames(t, singleCapture(t, rec, refused), map[string]string{"command": "loa"})
	followups := 0
	for _, c := range f.Calls() {
		if c.Method == "Followup" {
			followups++
		}
	}
	if followups != 1 {
		t.Fatalf("follow-ups = %d, want only the refused one", followups)
	}
}
