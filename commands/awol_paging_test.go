package commands

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// serveAwolFlagged serves a position of n flagged troopers whose rows all
// take 86 characters, as the regiment's do: usernames like
// U000_xxxxxxxxxxxx, milpac IDs from 100 up and a last post on 2026-03-01,
// so each row reads 68d AWOL · last post 75d at awolRefDate.
func serveAwolFlagged(t *testing.T, n int) *stubCavAPI {
	t.Helper()
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	for k := range n {
		id := fmt.Sprintf("%d", 100+k)
		roster.LiteProfiles[id] = awolMember(awolFlaggedName(k), id, "2026-03-01 12:00:00")
	}
	return serveCavAPI(t, awolRefDate, roster)
}

// awolFlaggedName is the username serveAwolFlagged gives trooper k.
func awolFlaggedName(k int) string {
	return fmt.Sprintf("U%03d_%s", k, strings.Repeat("x", 12))
}

// A list of 100 flagged troopers, past what one message holds, reaches the
// staff member in messages Discord accepts.
func TestRunAwolSendsAListPastOneMessageInMessagesDiscordAccepts(t *testing.T) {
	serveAwolFlagged(t, 100)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertWithinDiscordLimits(t, replyMessages(t, f))
}

// With the LOAs unread, every page of a list past one message carries the
// degraded warning, and every message stays within what Discord accepts.
func TestRunAwolWarnsOnEveryPageOfADegradedListPastOneMessage(t *testing.T) {
	serveAwolFlagged(t, 100).failLOAs(http.StatusInternalServerError)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	messages := replyMessages(t, f)
	assertWithinDiscordLimits(t, messages)
	for m, embeds := range messages {
		for e, embed := range embeds {
			if embed.Footer == nil || embed.Footer.Text != awolDegradedFooter {
				t.Errorf("message %d embed %d carries footer %+v, want the degraded warning", m+1, e+1, embed.Footer)
			}
		}
	}
}

// awolRowName finds the troopers these tests flag, by the usernames they
// give them.
var awolRowName = regexp.MustCompile(`[A-Z]\d{3}_x{12}`)

// Across the reply and its follow-ups, a list past one message names each
// flagged trooper once, worst first, and by username among troopers AWOL as
// long.
func TestRunAwolListsEveryTrooperOfAListPastOneMessageOnceWorstFirst(t *testing.T) {
	// Four groups of 25, worst first. The worst group's names sort last, so
	// only the days AWOL put it first.
	groups := []struct{ prefix, lastPost string }{
		{"D", "2026-03-01 12:00:00"},
		{"C", "2026-03-15 12:00:00"},
		{"B", "2026-04-01 12:00:00"},
		{"A", "2026-04-20 12:00:00"},
	}
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{}}
	var want []string
	for _, g := range groups {
		for k := range 25 {
			name := fmt.Sprintf("%s%03d_%s", g.prefix, k, strings.Repeat("x", 12))
			id := fmt.Sprintf("%d", 100+len(roster.LiteProfiles))
			roster.LiteProfiles[id] = awolMember(name, id, g.lastPost)
			want = append(want, name)
		}
	}
	serveCavAPI(t, awolRefDate, roster)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	messages := replyMessages(t, f)
	if len(messages) < 2 {
		t.Fatalf("want the list across more than one message; got %d", len(messages))
	}
	var got []string
	for _, embeds := range messages {
		for _, e := range embeds {
			got = append(got, awolRowName.FindAllString(e.Description, -1)...)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the rows name\n%q\nwant\n%q", got, want)
	}
}

// 440 flagged troopers, the most whose rows fit the ten descriptions that
// decide the file, arrive as embeds over the reply and follow-ups that
// Discord accepts.
func TestRunAwolSendsTheLargestListUnderTheFileLineAsEmbeds(t *testing.T) {
	serveAwolFlagged(t, 440)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	for _, c := range f.Calls() {
		if c.Edit != nil && len(c.Edit.Files) != 0 {
			t.Fatalf("440 troopers went out as a file")
		}
	}
	messages := replyMessages(t, f)
	if len(messages) < 2 {
		t.Fatalf("want the list across more than one message; got %d", len(messages))
	}
	assertWithinDiscordLimits(t, messages)
}

// 441 flagged troopers, one past the file line, arrive as the file with the
// large-report note, and nothing follows it.
func TestRunAwolSendsAListPastTheFileLineAsAFile(t *testing.T) {
	serveAwolFlagged(t, 441)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	edit := finalEdit(t, f)
	if len(edit.Files) != 1 {
		t.Fatalf("want the report as one file; got %d files", len(edit.Files))
	}
	if want := fmt.Sprintf(awolLargeFileFormat, "1-7"); edit.Content == nil || !strings.Contains(*edit.Content, want) {
		t.Fatalf("want the large-report note %q; got %v", want, edit.Content)
	}
	if n := len(followups(f.Calls())); n != 0 {
		t.Fatalf("follow-ups = %d, want none", n)
	}
}

// force_file_output sends a list past one message as the file with the
// forced-file note, and nothing follows it.
func TestRunAwolForcedFileSendsAListPastOneMessageAsTheFileAlone(t *testing.T) {
	serveAwolFlagged(t, 100)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(
		stringOption("position", "1-7"),
		boolOption("force_file_output", true),
	))

	edit := finalEdit(t, f)
	if len(edit.Files) != 1 {
		t.Fatalf("want the report as one file; got %d files", len(edit.Files))
	}
	if want := fmt.Sprintf(awolForcedFileFormat, "1-7"); edit.Content == nil || !strings.Contains(*edit.Content, want) {
		t.Fatalf("want the forced-file note %q; got %v", want, edit.Content)
	}
	if n := len(followups(f.Calls())); n != 0 {
		t.Fatalf("follow-ups = %d, want none", n)
	}
}

// A list that fits one embed arrives as that one embed, titled for the
// position with no page marker.
func TestRunAwolSendsAListThatFitsOneEmbedAsOneEmbedTitledForThePosition(t *testing.T) {
	serveAwolFlagged(t, 2)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	messages := replyMessages(t, f)
	if len(messages) != 1 || len(messages[0]) != 1 {
		t.Fatalf("want one message of one embed; got %d messages, the reply holding %d embeds", len(messages), len(messages[0]))
	}
	if title := messages[0][0].Title; title != "AWOL — 1-7" {
		t.Fatalf("title = %q, want %q", title, "AWOL — 1-7")
	}
}

// shownReplyText is the text the run's reply shows once the run ends: what
// it acknowledged with, as the last edit that set text left it.
func shownReplyText(t *testing.T, f *fakeResponder) (acknowledged, shown string) {
	t.Helper()
	for _, c := range f.Calls() {
		switch {
		case c.Method == "Respond" && c.Response != nil && c.Response.Data != nil && acknowledged == "":
			acknowledged = c.Response.Data.Content
			shown = acknowledged
		case c.Method == "Edit" && c.Edit != nil && c.Edit.Content != nil:
			shown = *c.Edit.Content
		}
	}
	if acknowledged == "" {
		t.Fatalf("the run never acknowledged with text")
	}
	return acknowledged, shown
}

// Once the list arrives as embeds, on one page or over several messages,
// the reply no longer says it is fetching the list.
func TestRunAwolClearsTheFetchingTextOnceTheListArrives(t *testing.T) {
	for _, n := range []int{2, 100} {
		t.Run(fmt.Sprintf("%d troopers", n), func(t *testing.T) {
			serveAwolFlagged(t, n)
			f := &fakeResponder{}
			runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

			replyMessages(t, f)
			if acknowledged, shown := shownReplyText(t, f); strings.Contains(shown, acknowledged) {
				t.Fatalf("the reply still shows %q above the list", shown)
			}
		})
	}
}

// 45 flagged troopers fill more than one embed but fit one message, so
// they arrive in the reply alone.
func TestRunAwolSendsAListThatFitsOneMessageInTheReplyAlone(t *testing.T) {
	serveAwolFlagged(t, 45)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	messages := replyMessages(t, f)
	if len(messages) != 1 || len(messages[0]) < 2 {
		t.Fatalf("want one message of several embeds; got %d messages, the reply holding %d embeds", len(messages), len(messages[0]))
	}
}

// awolPages returns every page the run sent, in the order a leader reads
// them.
func awolPages(t *testing.T, f *fakeResponder) []*discordgo.MessageEmbed {
	t.Helper()
	var pages []*discordgo.MessageEmbed
	for _, embeds := range replyMessages(t, f) {
		pages = append(pages, embeds...)
	}
	return pages
}

// Each page of a list past one message names its place among every page
// of the list, so a leader can tell when a list stopped short.
func TestRunAwolNumbersThePagesOfAListPastOneMessageAcrossEveryMessage(t *testing.T) {
	serveAwolFlagged(t, 100)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if n := len(replyMessages(t, f)); n < 2 {
		t.Fatalf("want the list across more than one message; got %d", n)
	}
	pages := awolPages(t, f)
	for k, page := range pages {
		if want := fmt.Sprintf("AWOL — 1-7 (Page %d/%d)", k+1, len(pages)); page.Title != want {
			t.Errorf("page %d is titled %q, want %q", k+1, page.Title, want)
		}
	}
}

// Every page of a list past one message opens with the whole list's
// flagged count and the note that the run skipped a record.
func TestRunAwolOpensEveryPageOfAListPastOneMessageWithTheWholeListsSummary(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"99": awolMember("Broken.B", "99", "not-a-date"),
	}}
	for k := range 100 {
		id := fmt.Sprintf("%d", 100+k)
		roster.LiteProfiles[id] = awolMember(awolFlaggedName(k), id, "2026-03-01 12:00:00")
	}
	serveCavAPI(t, awolRefDate, roster)
	f := &fakeResponder{}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	if n := len(replyMessages(t, f)); n < 2 {
		t.Fatalf("want the list across more than one message; got %d", n)
	}
	for k, page := range awolPages(t, f) {
		opening := page.Description
		if row := awolRowName.FindStringIndex(opening); row != nil {
			opening = opening[:row[0]]
		}
		for _, want := range []string{awolSummaryLine(100), awolSkippedNote(1)} {
			if !strings.Contains(opening, want) {
				t.Errorf("page %d opens with %q, want it to carry %q", k+1, opening, want)
			}
		}
	}
}

// A follow-up Discord refuses reaches Sentry once, and no later message
// goes out, so the page numbers already sent show the leader the list
// stopped short.
func TestRunAwolStopsAtARefusedFollowUpAndReportsItOnce(t *testing.T) {
	serveAwolFlagged(t, 440)
	whole := &fakeResponder{}
	runAwol(whole, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))
	if n := len(replyMessages(t, whole)); n < 3 {
		t.Fatalf("the fixture makes %d messages; the test needs one after the refused follow-up", n)
	}

	rec := recordCaptures(t)
	refused := errors.New("HTTP 400 Bad Request")
	f := &fakeResponder{FollowupErrs: []error{refused}}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertCaptureNames(t, singleCapture(t, rec, refused), map[string]string{"command": "awol"})
	if n := len(followups(f.Calls())); n != 1 {
		t.Fatalf("follow-ups = %d, want only the refused one", n)
	}
}

// A reply edit Discord refuses reaches Sentry once, and no follow-up goes
// out after it.
func TestRunAwolSendsNoFollowUpAfterARefusedReplyEdit(t *testing.T) {
	serveAwolFlagged(t, 100)
	whole := &fakeResponder{}
	runAwol(whole, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))
	if n := len(replyMessages(t, whole)); n < 2 {
		t.Fatalf("the fixture makes %d messages; the test needs a follow-up after the reply", n)
	}

	rec := recordCaptures(t)
	refused := errors.New("HTTP 400 Bad Request")
	f := &fakeResponder{EditErrs: []error{refused}}
	runAwol(f, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertCaptureNames(t, singleCapture(t, rec, refused), map[string]string{"command": "awol"})
	if n := len(followups(f.Calls())); n != 0 {
		t.Fatalf("follow-ups = %d, want none", n)
	}
}
