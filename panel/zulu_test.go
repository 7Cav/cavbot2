package panel

import (
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// zuluText is a time as the regiment writes it, in Zulu (GLOSSARY.md), as
// in 17 Oct 2026 0253z: the day, the month's three letters, the year, and
// the hour and minute in UTC as four digits with a z.
var zuluText = regexp.MustCompile(`^(\d{1,2}) ([A-Z][a-z]{2}) (\d{4}) (\d{2})(\d{2})z$`)

// assertInZulu fails the test unless el, a <time>, reads in Zulu as the
// minute its datetime names. place names el in the failure.
func assertInZulu(t *testing.T, el *html.Node, place string) {
	t.Helper()
	if el == nil {
		t.Errorf("%s shows no time", place)
		return
	}
	raw := attrOf(el, "datetime")
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Errorf("%s's datetime %q is not RFC 3339: %v", place, raw, err)
		return
	}
	text := textOf(el)
	m := zuluText.FindStringSubmatch(text)
	if m == nil {
		t.Errorf("%s reads %q, want a time in Zulu as in 17 Oct 2026 0253z", place, text)
		return
	}
	month := time.Month(0)
	for mo := time.January; mo <= time.December; mo++ {
		if mo.String()[:3] == m[2] {
			month = mo
		}
	}
	day, _ := strconv.Atoi(m[1])
	year, _ := strconv.Atoi(m[3])
	hour, _ := strconv.Atoi(m[4])
	minute, _ := strconv.Atoi(m[5])
	read := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	if want := at.UTC().Truncate(time.Minute); month == 0 || !read.Equal(want) {
		t.Errorf("%s reads %q, want the UTC minute its datetime %s names", place, text, raw)
	}
}

// #754: a hub's change log gives an entry's time in Zulu, even when the
// store hands it back in another zone.
func TestHubChangeLogGivesAnEntrysTimeInZulu(t *testing.T) {
	w := newTestWorld(t)
	w.st.HoldTimesIn(utcMinus5)
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/hubs", registerForm("hub-1", "Arma Voice")), "/")

	entry := newestEntryOnTheForm(t, w.b, storedHubID(t, w.st, "hub-1"))

	assertInZulu(t, findElement(entry, "time", "data-field", "at"), "the hub's change log entry")
}

// #754: after a removal the hub list gives the removal's time in Zulu, in
// the line naming who removed the hub and in the hub's Removed hubs row,
// and the row's change log gives each entry's time in Zulu.
func TestARemovalGivesItsTimesInZulu(t *testing.T) {
	w := newTestWorld(t, testHub())
	w.st.HoldTimesIn(utcMinus5)
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	landing := location(t, removeHubThroughThePreview(t, w.b, id)).RequestURI()

	doc := parseHTML(t, w.b.get(landing))

	notice := findElement(doc, "", "data-field", "removed")
	if notice == nil {
		t.Fatal("the hub list names no removed hub")
	}
	assertInZulu(t, findElement(notice, "time", "", ""), "the removal notice")
	row := removedRowOf(t, doc, id)
	assertInZulu(t, findElement(removedHubFacts(t, row), "time", "data-field", "removed_at"), "the Removed hubs row")
	entries := entryElements(row)
	if len(entries) == 0 {
		t.Fatal("the Removed hubs row opens to no change log entry")
	}
	for _, entry := range entries {
		assertInZulu(t, findElement(entry, "time", "data-field", "at"), "an entry under the Removed hubs row")
	}
}

// #754: the hub list's Last failure column gives a failed spawn's time in
// Zulu, with its year.
func TestHubListGivesTheLastFailuresTimeInZulu(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setCreateErr(restError(http.StatusInternalServerError))
	w.join("user-a", "hub-1")

	row := findElement(parseHTML(t, w.b.get("/")), "", "data-hub", strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10))

	if row == nil {
		t.Fatal("the hub list has no row for hub-1")
	}
	assertInZulu(t, findElement(row, "time", "data-field", "failure_at"), "the Last failure column")
}

// #754: once a Foxhole action ends, the Foxhole page gives its change log
// entry's time in Zulu, and its report says when it started and ended in
// Zulu, to the minute.
func TestAFinishedFoxholeActionGivesItsTimesInZulu(t *testing.T) {
	w := newFoxholeWorld(t)
	w.st.HoldTimesIn(utcMinus5)
	startPurge(t, w, "external")
	w.awaitActionEnd(t)

	doc := parseHTML(t, w.b.get(foxholePath))

	entries := entryElements(foxholeBlock(t, doc, "changes"))
	if len(entries) == 0 {
		t.Fatal("the Foxhole change log shows no entry")
	}
	assertInZulu(t, findElement(entries[0], "time", "data-field", "at"), "the Foxhole change log entry")
	report := reportBlock(t, doc)
	assertInZulu(t, findElement(report, "time", "data-field", "started"), "the report's start")
	assertInZulu(t, findElement(report, "time", "data-field", "ended"), "the report's end")
}

// #754: the page a Retry opens says when the action it retries started, in
// Zulu, to the minute.
func TestRetryGivesTheStartOfTheActionItRetriesInZulu(t *testing.T) {
	w := newFoxholeWorld(t)
	w.st.HoldTimesIn(utcMinus5)
	w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
	startPurge(t, w, "external")
	w.awaitActionEnd(t)

	retrying := findElement(openRetry(t, w.b), "", "data-field", "retrying")

	if retrying == nil {
		t.Fatal("the page the Retry opens doesn't say what it retries")
	}
	assertInZulu(t, findElement(retrying, "time", "", ""), "the retry's start")
}

// #754: while a role-changing command runs, the Foxhole page says when it
// started in Zulu, to the minute.
func TestFoxholePageGivesTheStartOfTheCommandRunningInZulu(t *testing.T) {
	w := newFoxholeWorld(t)
	holdFoxholeAdd(t, w)

	block := commandBlock(t, parseHTML(t, w.b.get(foxholePath)))

	assertInZulu(t, findElement(block, "time", "data-field", "started"), "the command's start")
}
