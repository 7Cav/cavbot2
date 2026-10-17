package panel

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// Finding a recording again among many, as a panel admin does working
// through S6 deletion tickets in Every recording: each way back to a list
// opens it at the recording it came from.

// navWorld is a panel admin signed in with two recordings of their own,
// older first, and one another starter started, the newest.
type navWorld struct {
	*testWorld
	own   [2]int64
	other int64
}

func newNavWorld(t *testing.T) navWorld {
	t.Helper()
	w := newTestWorld(t)
	w.milpacs.set(testUsername, "discord-admin")
	b := w.addStarter(5002, "Baker.B", "discord-b")
	start := time.Date(2026, 10, 14, 19, 30, 0, 0, time.UTC)
	nw := navWorld{testWorld: w}
	nw.own[0] = w.storeStopped(t, "discord-admin", start, start.Add(time.Hour))
	nw.own[1] = w.storeStopped(t, "discord-admin", start.Add(2*time.Hour), start.Add(3*time.Hour))
	nw.other = w.storeStopped(t, b.discordID, start.Add(4*time.Hour), start.Add(5*time.Hour))
	signIn(t, w.forum, w.b)
	return nw
}

// listedIDs is the IDs of the recordings a page lists, in order of ID.
func listedIDs(t *testing.T, doc *html.Node) []int64 {
	t.Helper()
	return slices.Sorted(maps.Keys(listedRecordings(t, doc)))
}

// assertLandsOnRecording checks a page opened at fragment lists exactly the
// recordings want and lands on recording id's heading.
func assertLandsOnRecording(t *testing.T, doc *html.Node, fragment string, id int64, want []int64) {
	t.Helper()
	if got := listedIDs(t, doc); !slices.Equal(got, want) {
		t.Errorf("the page lists recordings %v, want %v", got, want)
	}
	block, ok := listedRecordings(t, doc)[id]
	if !ok {
		t.Fatalf("the page doesn't list recording %d", id)
	}
	landedHeading(t, doc, fragment, block, "the recording's heading")
}

// Cancel on a delete preview goes back to the page whose Delete opened it.
// Back in a list, it opens at the recording, so a panel admin working down
// Every recording carries on from where they were.
func TestRecordingDeleteCancelGoesBackToWhereDeleteWasPressed(t *testing.T) {
	cases := []struct {
		name string
		page func(nw navWorld) (path string, id int64)
		// want is the recordings the page Cancel opens lists, and list is
		// set when that page is a list that must open at the recording.
		want func(nw navWorld) []int64
		list bool
	}{
		{"Every recording", func(nw navWorld) (string, int64) { return recordingsPath + "?view=all", nw.other },
			func(nw navWorld) []int64 { return []int64{nw.own[0], nw.own[1], nw.other} }, true},
		{"the admin's own list", func(nw navWorld) (string, int64) { return recordingsPath, nw.own[0] },
			func(nw navWorld) []int64 { return []int64{nw.own[0], nw.own[1]} }, true},
		{"the recording's own page", func(nw navWorld) (string, int64) { return commands.RecordingPath(nw.own[0]), nw.own[0] },
			func(nw navWorld) []int64 { return []int64{nw.own[0]} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nw := newNavWorld(t)
			path, id := tc.page(nw)
			deleteHref := linkHref("delete")(t, parseHTML(t, nw.b.get(path)), id)
			cancel := findElement(parseHTML(t, nw.b.get(deleteHref)), "a", "data-field", "cancel")
			if cancel == nil {
				t.Fatalf("recording %d's delete preview has no Cancel", id)
			}
			href := attrOf(cancel, "href")

			doc, fragment := landing(t, nw.b, href, nw.b.get(href))

			if !tc.list {
				if got := listedIDs(t, doc); !slices.Equal(got, tc.want(nw)) {
					t.Errorf("Cancel opens a page listing recordings %v, want %v", got, tc.want(nw))
				}
				return
			}
			assertLandsOnRecording(t, doc, fragment, id, tc.want(nw))
		})
	}
}

// recordingAt is the recording a page opened at fragment lands at: the one
// whose block holds the element the fragment names, or else the first
// listed after it. It fails the test when the fragment names nothing.
func recordingAt(t *testing.T, doc *html.Node, fragment string) int64 {
	t.Helper()
	landed := landedOn(doc, fragment)
	if landed == nil {
		t.Fatalf("the page opens at fragment %q, which names nothing on it", fragment)
	}
	for n := landed; n != nil; n = n.Parent {
		if _, ok := attrValue(n, "data-recording"); ok {
			return recordingIDOf(t, n)
		}
	}
	var at int64
	passed := false
	eachElement(doc, func(n *html.Node) {
		if n == landed {
			passed = true
		}
		if _, ok := attrValue(n, "data-recording"); ok && passed && at == 0 {
			at = recordingIDOf(t, n)
		}
	})
	if at == 0 {
		t.Fatalf("the page opens at fragment %q, below every recording it lists", fragment)
	}
	return at
}

// recordingIDOf is the recording ID a block's data-recording names.
func recordingIDOf(t *testing.T, n *html.Node) int64 {
	t.Helper()
	for id, block := range listedRecordings(t, n) {
		if block == n {
			return id
		}
	}
	t.Fatal("the block names no recording")
	return 0
}

// After a delete from a list, the page the Confirm lands on is that list,
// open at the recording that came after the deleted one, so a panel admin
// working down Every recording, or a starter clearing out their own, picks
// up where they were.
func TestRecordingDeletedFromAListLandsAtTheNextRecording(t *testing.T) {
	cases := []struct {
		name    string
		viewer  func(w *testWorld) // signs the viewer in
		starter string
		list    string
	}{
		{"a panel admin in Every recording", func(w *testWorld) { signIn(t, w.forum, w.b) }, "discord-b", recordingsPath + "?view=all"},
		{"a starter in their own list", func(w *testWorld) {
			signInAs(t, w.forum, w.b, w.forum.addUser(5001, "Able.A", 2, []int{35}))
		}, "discord-a", recordingsPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			w.milpacs.set("Able.A", "discord-a")
			start := time.Date(2026, 10, 14, 19, 30, 0, 0, time.UTC)
			older := w.storeStopped(t, tc.starter, start, start.Add(time.Hour))
			deleted := w.storeStopped(t, tc.starter, start.Add(2*time.Hour), start.Add(3*time.Hour))
			newer := w.storeStopped(t, tc.starter, start.Add(4*time.Hour), start.Add(5*time.Hour))
			someoneElses := w.storeStopped(t, "discord-c", start.Add(6*time.Hour), start.Add(7*time.Hour))
			tc.viewer(w)
			deleteHref := linkHref("delete")(t, parseHTML(t, w.b.get(tc.list)), deleted)
			form := findElement(parseHTML(t, w.b.get(deleteHref)), "form", "data-field", "delete-confirm")
			if form == nil {
				t.Fatalf("recording %d's delete preview has no Confirm", deleted)
			}

			doc, fragment := landing(t, w.b, attrOf(form, "action"), submit(t, w.b, form))

			want := []int64{older, newer}
			if tc.list != recordingsPath {
				want = []int64{older, newer, someoneElses}
			}
			if got := listedIDs(t, doc); !slices.Equal(got, want) {
				t.Errorf("the delete lands on a page listing recordings %v, want %v", got, want)
			}
			if got := recordingAt(t, doc, fragment); got != older {
				t.Errorf("the delete lands at recording %d, want %d, the one after the deleted %d", got, older, deleted)
			}
		})
	}
}

// A recording's own page leads back to the list it belongs to, open at the
// recording: Every recording for a panel admin on a recording someone else
// started, and their own list for its starter.
func TestRecordingPageLeadsBackToItsListAtTheRecording(t *testing.T) {
	cases := []struct {
		name string
		id   func(nw navWorld) int64
		want func(nw navWorld) []int64
	}{
		{"someone else's, for a panel admin", func(nw navWorld) int64 { return nw.other },
			func(nw navWorld) []int64 { return []int64{nw.own[0], nw.own[1], nw.other} }},
		{"their own, for its starter", func(nw navWorld) int64 { return nw.own[0] },
			func(nw navWorld) []int64 { return []int64{nw.own[0], nw.own[1]} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nw := newNavWorld(t)
			id := tc.id(nw)
			back := findElement(parseHTML(t, nw.b.get(commands.RecordingPath(id))), "a", "data-field", "back")
			if back == nil {
				t.Fatalf("recording %d's page has no way back to its list", id)
			}
			href := attrOf(back, "href")

			doc, fragment := landing(t, nw.b, href, nw.b.get(href))

			assertLandsOnRecording(t, doc, fragment, id, tc.want(nw))
		})
	}
}

// In a list, a recording's title opens the recording's own page, the one
// the link a stop gives opens, which shows that recording alone.
func TestRecordingTitleInAListOpensItsOwnPage(t *testing.T) {
	nw := newNavWorld(t)

	href := linkHref("recording-link")(t, parseHTML(t, nw.b.get(recordingsPath+"?view=all")), nw.own[1])

	if got := listedIDs(t, parseHTML(t, nw.b.get(href))); !slices.Equal(got, []int64{nw.own[1]}) {
		t.Errorf("recording %d's title opens a page listing recordings %v, want it alone", nw.own[1], got)
	}
}

// A list says how many recordings it holds: the visitor's own in their
// list, and everyone's in Every recording.
func TestRecordingsListSaysHowManyItHolds(t *testing.T) {
	nw := newNavWorld(t)

	for path, want := range map[string]string{recordingsPath: "2", recordingsPath + "?view=all": "3"} {
		count := findElement(parseHTML(t, nw.b.get(path)), "", "data-field", "count")
		if count == nil {
			t.Errorf("%s gives no count", path)
			continue
		}
		if got := strings.TrimSpace(textOf(count)); got != want {
			t.Errorf("%s counts %q recordings, want %s", path, got, want)
		}
	}
}
