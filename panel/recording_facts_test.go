package panel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// The facts a recording's block gives, as a starter reads them on arriving
// from the link in Discord. Each recording a case reads is written through
// the store with the times the case gives, so the times the page shows have
// a known answer.

// utcMinus5 is a zone the panel doesn't write in, so a time the page shows
// without converting it to Zulu reads differently.
var utcMinus5 = time.FixedZone("UTC-5", -5*60*60)

// storeStopped writes a recording of the fixture's channel through the
// store, started by the member with the Discord ID given at start and
// stopped at stop, with one speaker. It returns the recording's ID.
func (w *testWorld) storeStopped(t *testing.T, discordID string, start, stop time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id := w.storeRunning(t, discordID, start)
	speakers := []store.Speaker{{ID: discordID, DisplayName: "Doe.J"}}
	if err := w.store.StopRecording(ctx, id, stop, store.RecordingEndStopped, speakers); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	return id
}

// storeRunning writes a recording of the fixture's channel through the
// store, started by the member with the Discord ID given at start and still
// running. It returns the recording's ID.
func (w *testWorld) storeRunning(t *testing.T, discordID string, start time.Time) int64 {
	t.Helper()
	rec, err := w.store.StartRecording(context.Background(), store.Recording{GuildID: testGuildID, ChannelID: testRecordChannel,
		ChannelName: "Briefing Room", StarterID: discordID, RecorderID: testRecorder, StartedAt: start})
	if err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	return rec.ID
}

// listedBlock is recording id's block on the page at path, failing the test
// when the page doesn't list it.
func listedBlock(t *testing.T, b *browser, path string, id int64) *html.Node {
	t.Helper()
	block, ok := listedRecordings(t, parseHTML(t, b.get(path)))[id]
	if !ok {
		t.Fatalf("%s doesn't list recording %d", path, id)
	}
	return block
}

// A stopped recording's block says when it stopped as well as when it
// started, and writes both times in Zulu, the regiment's standard time
// (GLOSSARY.md), whatever zone the time was recorded in. A recording still
// running has no stop time to show.
func TestRecordingsPageGivesTheStartAndStopInZulu(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	start := time.Date(2026, 10, 14, 14, 30, 0, 0, utcMinus5)
	stop := time.Date(2026, 10, 14, 15, 17, 0, 0, utcMinus5)
	stopped := w.storeStopped(t, a.discordID, start, stop)
	running := w.storeRunning(t, a.discordID, start.Add(2*time.Hour))
	signInAs(t, w.forum, w.b, a.account)

	block := listedBlock(t, w.b, recordingsPath, stopped)

	for field, want := range map[string]string{"started": "1930z", "stopped": "2017z"} {
		el := findElement(block, "time", "data-field", field)
		if el == nil {
			t.Errorf("recording %d's block shows no %s time", stopped, field)
			continue
		}
		if got := textOf(el); !strings.Contains(got, want) {
			t.Errorf("recording %d's %s time reads %q, want %s in Zulu", stopped, field, got, want)
		}
	}
	if el := findElement(block, "time", "data-field", "stopped"); el != nil && !parseDatetime(t, el).Equal(stop) {
		t.Errorf("recording %d's stop time is %v, want %v", stopped, parseDatetime(t, el), stop)
	}
	if findElement(listedBlock(t, w.b, recordingsPath, running), "time", "data-field", "stopped") != nil {
		t.Errorf("recording %d, still running, shows a stop time", running)
	}
}

// A recording's length reads as a media player shows one, hours, minutes
// and seconds with colons, rather than Go's 1h2m3s.
func TestRecordingsPageGivesTheLengthAsAPlayerDoes(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	start := time.Date(2026, 10, 14, 19, 30, 0, 0, time.UTC)
	cases := map[int64]string{
		w.storeStopped(t, a.discordID, start, start.Add(time.Hour+2*time.Minute+3*time.Second)):      "1:02:03",
		w.storeStopped(t, a.discordID, start.Add(3*time.Hour), start.Add(3*time.Hour+3*time.Minute)): "3:00",
	}
	signInAs(t, w.forum, w.b, a.account)

	rows := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))

	for id, want := range cases {
		row, ok := rows[id]
		if !ok {
			t.Errorf("the page doesn't list recording %d", id)
			continue
		}
		el := findElement(row, "", "data-field", "length")
		if el == nil {
			t.Errorf("recording %d's block shows no length", id)
			continue
		}
		if got := strings.TrimSpace(textOf(el)); got != want {
			t.Errorf("recording %d's length reads %q, want %q", id, got, want)
		}
	}
}

// A recording whose mix is still being built, as it is when its starter
// follows the link a stop gives, offers a way to check again, which opens
// the recording with its mix as it stands. One whose mix is ready has
// nothing to wait for and offers none.
func TestRecordingBeingMixedOffersAWayToCheckAgain(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	start := time.Date(2026, 10, 14, 19, 30, 0, 0, time.UTC)
	mixing := w.storeStopped(t, a.discordID, start, start.Add(time.Hour))
	ready := w.storeStopped(t, a.discordID, start.Add(2*time.Hour), start.Add(3*time.Hour))
	if err := w.store.SetRecordingMix(context.Background(), ready, store.MixReady); err != nil {
		t.Fatalf("SetRecordingMix: %v", err)
	}
	signInAs(t, w.forum, w.b, a.account)

	page := parseHTML(t, w.b.get(recordingsPath))

	again := parseHTML(t, w.b.get(linkHref("check-again")(t, page, mixing)))
	if _, ok := listedRecordings(t, again)[mixing]; !ok {
		t.Errorf("checking again on recording %d opens a page that doesn't show it", mixing)
	}
	if row := listedRecordings(t, page)[ready]; row == nil || findElement(row, "a", "data-field", "check-again") != nil {
		t.Errorf("recording %d, its mix ready, offers to check again or isn't listed", ready)
	}
}
