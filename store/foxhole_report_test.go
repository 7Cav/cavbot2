package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// purgeEntry is a purge's report entry, its diff naming a step so a test
// can tell one write of the report from another. The runtime decides the
// diff's shape; the store keeps the bytes.
func purgeEntry(step string) ChangeLogEntry {
	return ChangeLogEntry{ForumUserID: 1234, ForumUsername: "Doe.J", Action: ChangePurge, Diff: reportDiff(step)}
}

// reportDiff is a report's diff naming a step.
func reportDiff(step string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"step": step})
	return raw
}

// lastReport reads the newest report, and fails the test when the store
// has none.
func lastReport(t *testing.T, s Store) FoxholeReport {
	t.Helper()
	report, err := s.LastFoxholeReport(context.Background())
	if err != nil {
		t.Fatalf("LastFoxholeReport: %v", err)
	}
	return report
}

// A report started reads back as the newest report, running, under the ID
// and time the store gave it, with the forum user, action and diff given.
// It is an entry of the Foxhole change log, and no list of the hub page's
// change log holds it.
func TestStartedReportReadsBackRunningInTheFoxholeChangeLog(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		entry := purgeEntry("started")

		started, err := s.StartFoxholeReport(ctx, entry)

		if err != nil {
			t.Fatalf("StartFoxholeReport: %v", err)
		}
		if started.ID == 0 || started.At.IsZero() {
			t.Errorf("started entry = %+v, want an ID and a time set by the store", started)
		}
		report := lastReport(t, s)
		got := report.Entry
		if !report.Running || got.ID != started.ID {
			t.Errorf("the newest report is entry %d, running %v; want entry %d, running", got.ID, report.Running, started.ID)
		}
		if got.ForumUserID != entry.ForumUserID || got.ForumUsername != entry.ForumUsername || got.Action != ChangePurge {
			t.Errorf("report entry = %+v, want forum user %d %s and action %s", got, entry.ForumUserID, entry.ForumUsername, ChangePurge)
		}
		if diff, want := decodeDiff(t, got.Diff), decodeDiff(t, entry.Diff); !reflect.DeepEqual(diff, want) {
			t.Errorf("report diff = %v, want %v", diff, want)
		}
		if entries := foxholeChanges(t, s); len(entries) != 1 || entries[0].ID != started.ID {
			t.Errorf("the Foxhole change log holds %+v, want the report alone", entries)
		}
		hubLists := map[string]func() ([]ChangeLogEntry, error){
			"ListChangeLog(no hub)": func() ([]ChangeLogEntry, error) { return s.ListChangeLog(ctx, 0, 100) },
			"ListModeratorChanges":  func() ([]ChangeLogEntry, error) { return s.ListModeratorChanges(ctx, 100) },
		}
		for name, list := range hubLists {
			entries, err := list()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(entries) != 0 {
				t.Errorf("%s lists %+v, want nothing", name, entries)
			}
		}
	})
}

// assertReport fails unless the newest report is the entry with the ID
// given, its diff naming the step given, running or ended as given.
func assertReport(t *testing.T, s Store, id int64, step string, running bool) {
	t.Helper()
	report := lastReport(t, s)
	if report.Entry.ID != id || report.Running != running {
		t.Errorf("the newest report is entry %d, running %v; want entry %d, running %v", report.Entry.ID, report.Running, id, running)
	}
	if got, want := decodeDiff(t, report.Entry.Diff), decodeDiff(t, reportDiff(step)); !reflect.DeepEqual(got, want) {
		t.Errorf("the report's diff = %v, want %v", got, want)
	}
}

// A running report takes each update of its diff and stays running. Ending
// it writes its last diff and marks it ended, and from then on it takes no
// update and no second end: each is ErrNotFound and writes nothing. So is
// an update or an end of an ID no report has, a save's entry included.
func TestReportFillsInWhileRunningAndEndsOnce(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		started, err := s.StartFoxholeReport(ctx, purgeEntry("started"))
		if err != nil {
			t.Fatalf("StartFoxholeReport: %v", err)
		}

		if err := s.UpdateFoxholeReport(ctx, started.ID, reportDiff("one done")); err != nil {
			t.Fatalf("UpdateFoxholeReport: %v", err)
		}
		assertReport(t, s, started.ID, "one done", true)

		if err := s.EndFoxholeReport(ctx, started.ID, reportDiff("done")); err != nil {
			t.Fatalf("EndFoxholeReport: %v", err)
		}
		assertReport(t, s, started.ID, "done", false)

		if err := saveNote(s, memberDoe, "", "discharged 12 Sep"); err != nil {
			t.Fatalf("note save: %v", err)
		}
		saveEntry := foxholeChanges(t, s)[0].ID
		for _, id := range []int64{started.ID, saveEntry, started.ID + saveEntry + 100} {
			if err := s.UpdateFoxholeReport(ctx, id, reportDiff("late")); !errors.Is(err, ErrNotFound) {
				t.Errorf("UpdateFoxholeReport(%d) = %v, want ErrNotFound", id, err)
			}
			if err := s.EndFoxholeReport(ctx, id, reportDiff("late")); !errors.Is(err, ErrNotFound) {
				t.Errorf("EndFoxholeReport(%d) = %v, want ErrNotFound", id, err)
			}
		}
		assertReport(t, s, started.ID, "done", false)
		if got := noteOf(t, foxholeChanges(t, s)[0]); got != "discharged 12 Sep" {
			t.Errorf("the note save's entry reads %q after the refused writes, want its own note", got)
		}
	})
}

// The newest report is the one the last action started, however many
// saves' entries were appended after it: the Foxhole page shows it until
// the next action replaces it. A log that holds no report has none.
func TestLastReportIsTheNewestReportHoweverManySavesFollowIt(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if _, err := s.LastFoxholeReport(ctx); !errors.Is(err, ErrNotFound) {
			t.Errorf("LastFoxholeReport on a log with no report = %v, want ErrNotFound", err)
		}
		var newest int64
		for _, step := range []string{"first purge", "second purge"} {
			started, err := s.StartFoxholeReport(ctx, purgeEntry(step))
			if err != nil {
				t.Fatalf("StartFoxholeReport: %v", err)
			}
			if err := s.EndFoxholeReport(ctx, started.ID, reportDiff(step)); err != nil {
				t.Fatalf("EndFoxholeReport: %v", err)
			}
			newest = started.ID
		}
		loaded := ""
		for i := 1; i <= 12; i++ {
			note := "note " + string(rune('a'+i))
			if err := saveNote(s, memberDoe, loaded, note); err != nil {
				t.Fatalf("save %d: %v", i, err)
			}
			loaded = note
		}

		assertReport(t, s, newest, "second purge", false)
	})
}
