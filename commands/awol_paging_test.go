package commands

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
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

// With the LOAs unread, every page a press shows carries the degraded
// warning.
func TestRunAwolWarnsOnEveryPageOfADegradedList(t *testing.T) {
	serveAwolFlagged(t, 100).failLOAs(http.StatusInternalServerError)
	pages, _ := pageThrough(t, awolList, store.NewFake(), awolRefDate)

	for k, page := range pages {
		if page.Footer == nil || page.Footer.Text != awolDegradedFooter {
			t.Errorf("page %d carries footer %+v, want the degraded warning", k+1, page.Footer)
		}
	}
}

// awolRowName finds the troopers these tests flag, by the usernames they
// give them.
var awolRowName = regexp.MustCompile(`[A-Z]\d{3}_x{12}`)

// When the runner pages a list of 100 flagged troopers with ▶ from page 1
// to page m, the pages name each flagged trooper once, worst first, and by
// username among troopers AWOL as long.
func TestRunAwolPagesShowEveryTrooperOnceWorstFirst(t *testing.T) {
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
	pages, _ := pageThrough(t, awolList, store.NewFake(), awolRefDate)

	var got []string
	for _, page := range pages {
		got = append(got, awolRowName.FindAllString(page.Description, -1)...)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the rows name\n%q\nwant\n%q", got, want)
	}
}

// 441 flagged troopers, one past the file line, arrive as the file with the
// large-report note, and nothing follows it.
func TestRunAwolSendsAListPastTheFileLineAsAFile(t *testing.T) {
	serveAwolFlagged(t, 441)
	f := &fakeResponder{}
	runAwol(f, nil, awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

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
	runAwol(f, nil, awolRefDate, fakeAppCommandInteraction(
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

// Once the list arrives as embeds, on one page or on many, the reply no
// longer says it is fetching the list.
func TestRunAwolClearsTheFetchingTextOnceTheListArrives(t *testing.T) {
	for _, n := range []int{2, 100} {
		t.Run(fmt.Sprintf("%d troopers", n), func(t *testing.T) {
			serveAwolFlagged(t, n)
			f := &fakeResponder{}
			runAwol(f, store.NewFake(), awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

			replyView(t, f)
			if acknowledged, shown := shownReplyText(t, f); strings.Contains(shown, acknowledged) {
				t.Fatalf("the reply still shows %q above the list", shown)
			}
		})
	}
}

// Every page a press shows opens with the whole list's flagged count and
// the note that the run skipped a record.
func TestRunAwolOpensEveryPageWithTheWholeListsSummary(t *testing.T) {
	roster := utils.LiteRosterResponse{LiteProfiles: map[string]utils.LiteProfileResponse{
		"99": awolMember("Broken.B", "99", "not-a-date"),
	}}
	for k := range 100 {
		id := fmt.Sprintf("%d", 100+k)
		roster.LiteProfiles[id] = awolMember(awolFlaggedName(k), id, "2026-03-01 12:00:00")
	}
	serveCavAPI(t, awolRefDate, roster)
	pages, _ := pageThrough(t, awolList, store.NewFake(), awolRefDate)

	if len(pages) < 2 {
		t.Fatalf("want the list across more than one page; got %d", len(pages))
	}
	for k, page := range pages {
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

// A reply edit Discord refuses on a long list reaches Sentry once.
func TestRunAwolReportsARefusedReplyEditOfALongListOnce(t *testing.T) {
	serveAwolFlagged(t, 100)
	rec := recordCaptures(t)
	refused := errors.New("HTTP 400 Bad Request")
	f := &fakeResponder{EditErrs: []error{refused}}
	runAwol(f, store.NewFake(), awolRefDate, fakeAppCommandInteraction(stringOption("position", "1-7")))

	assertCaptureNames(t, singleCapture(t, rec, refused), map[string]string{"command": "awol"})
}
